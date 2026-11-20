import Foundation
import SwiftUI
import Testing
import UIKit
@testable import Persona

private actor OlderNotFoundGate {
    private var started = false
    private var continuation: CheckedContinuation<Void, Error>?

    func wait() async throws {
        started = true
        try await withCheckedThrowingContinuation { continuation = $0 }
    }

    func waitForRequest() async -> Bool {
        for _ in 0..<200 {
            if started { return true }
            try? await Task.sleep(for: .milliseconds(10))
        }
        return started
    }

    func release(_ error: APIError) -> Bool {
        guard let continuation else { return false }
        self.continuation = nil
        continuation.resume(throwing: error)
        return true
    }
}

/// A node can report that a chat's history is gone (#11451). That answer ends history loading; ordinary read failures
/// keep their existing loading and explicit-retry behavior.
@MainActor @Suite(.serialized)
struct HistoryNotFoundTests {
    nonisolated private static let missingSentence = "This chat's history is no longer available on this computer."
    nonisolated private static let plain404Sentence = "The history request could not be found."
    nonisolated private static let readFailureSentence = "The synthetic history read failed."

    enum Control: String, CaseIterable, CustomTestStringConvertible {
        case plain404, readFailure

        var testDescription: String { rawValue }

        var error: APIError {
            switch self {
            case .plain404:
                APIError(status: 404, message: HistoryNotFoundTests.plain404Sentence,
                         fields: ["reason": "history_removed", "error": HistoryNotFoundTests.plain404Sentence])
            case .readFailure:
                APIError(status: 503, message: HistoryNotFoundTests.readFailureSentence,
                         fields: ["code": "internal", "reason": "read_failed", "error": HistoryNotFoundTests.readFailureSentence])
            }
        }

        var sentence: String { error.localizedDescription }
    }

    private var notFound: APIError {
        APIError(status: 404, message: Self.missingSentence,
                 fields: ["code": "not_found", "reason": "history_removed", "error": Self.missingSentence])
    }

    private func fixture(_ error: APIError) -> (model: AppModel, key: SessionKey, store: ChatStore, hub: FakeHub, path: String) {
        let hub = FakeHub()
        let key = SessionKey(machineID: "box", sessionID: "history-404-" + UUID().uuidString)
        let path = "/api/v1/sessions/box/\(key.sessionID)/items"
        hub.on("GET", path) { _ in throw error }
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "history-404-" + UUID().uuidString))
        model.forgetRestoredForTests()
        model.apiForTests = hub.client
        return (model, key, model.chat(key), hub, path)
    }

    private func item(_ seq: Int) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: "assistant", kind: .text, text: "Synthetic cached item",
             toolUseID: nil, toolName: nil, toolInput: nil, isError: nil, image: nil)
    }

    private func seedOlderPage(in store: ChatStore) {
        store.apply([item(8)], span: ItemSpan(history: "synthetic", from: nil, to: 8), backlog: true, hasMore: true)
        #expect(store.loadedOnce && store.hasMore && store.items.map(\.seq) == [8])
    }

    @Test func latestNotFoundShowsTheHubSentenceWithoutTheHistorySkeleton() async throws {
        let (model, key, store, _, _) = fixture(notFound)
        #expect(store.items.isEmpty && !store.loadedOnce, "this chat must start without cached history")
        await model.loadInitial(key)
        #expect(store.error == Self.missingSentence)

        let window = try host(model, key: key)
        defer { close(window) }
        let elements = await waitForHubSentence(Self.missingSentence, in: window)
        await holdForScreenshot()
        #expect(elements.contains { $0.label == Self.missingSentence },
                "the chat must draw the hub's exact sentence: \(elements.map(\.label))")
        #expect(!elements.contains { $0.id == "chat-loading-placeholder" },
                "a missing latest page must not leave ChatSkeleton beside the hub error")
        #expect(!store.hasMore && !store.isLoadingOlder, "no older-history spinner can remain for an empty missing chat")
    }

    @Test func reloadNotFoundShowsTheHubSentenceWithoutTheHistorySkeleton() async throws {
        let (model, key, store, _, _) = fixture(notFound)
        await model.reload(key)
        #expect(store.error == Self.missingSentence)

        let window = try host(model, key: key)
        defer { close(window) }
        let elements = await waitForHubSentence(Self.missingSentence, in: window)
        #expect(elements.contains { $0.label == Self.missingSentence })
        #expect(!elements.contains { $0.id == "chat-loading-placeholder" })
        #expect(!store.hasMore && !store.isLoadingOlder)
    }

    @Test(arguments: Control.allCases) func latestErrorsKeepTheExistingSkeleton(_ control: Control) async throws {
        let (model, key, store, _, _) = fixture(control.error)
        #expect(store.items.isEmpty && !store.loadedOnce)
        await model.loadInitial(key)
        #expect(store.error == control.sentence)

        let window = try host(model, key: key)
        defer { close(window) }
        let elements = await waitForSkeleton(in: window)
        #expect(elements.contains { $0.label == control.sentence })
        #expect(elements.contains { $0.id == "chat-loading-placeholder" },
                "a plain 404 or another read failure must preserve the existing loading path")
    }

    @Test func olderNotFoundEndsHistoryAndASecondLoadSendsNoRequest() async throws {
        let (model, key, store, hub, path) = fixture(notFound)
        seedOlderPage(in: store)

        #expect(await model.loadOlder(key) == false)
        #expect(!store.hasMore, "a missing older page must end pagination and remove its loading indicator")
        #expect(!store.isLoadingOlder)
        #expect(hub.sent("GET", path).count == 1)
        await Task.yield()
        #expect(hub.sent("GET", path).count == 1, "the failed page must not retry automatically")

        #expect(await model.loadOlder(key) == false)
        #expect(!store.hasMore && !store.isLoadingOlder)
        #expect(hub.sent("GET", path).count == 1, "an explicit load after history ended must not send another request")
    }

    @Test func anOlderNotFoundFromThePreviousHistoryDoesNotEndItsReplacement() async throws {
        let (model, key, store, hub, path) = fixture(notFound)
        seedOlderPage(in: store)
        let gate = OlderNotFoundGate()
        hub.onAsync("GET", path) { _ in
            try await gate.wait()
            return Data()
        }
        let loading = Task { await model.loadOlder(key) }
        #expect(await gate.waitForRequest())

        store.apply([item(20)], span: ItemSpan(history: "replacement", from: nil, to: 20), backlog: true, hasMore: true)
        #expect(store.history == "replacement" && store.hasMore && store.items.map(\.seq) == [20])
        #expect(await gate.release(notFound))
        #expect(await loading.value == false)
        #expect(store.hasMore, "the older response belongs to the previous history and must not close the replacement")
        #expect(store.items.map(\.seq) == [20])
        #expect(!store.isLoadingOlder)
    }

    @Test(arguments: Control.allCases) func olderErrorsKeepHasMoreAndAllowAnExplicitRetry(_ control: Control) async throws {
        let (model, key, store, hub, path) = fixture(control.error)
        seedOlderPage(in: store)

        #expect(await model.loadOlder(key) == false)
        #expect(store.hasMore, "this control must preserve the existing retry path")
        #expect(!store.isLoadingOlder)
        #expect(hub.sent("GET", path).count == 1)

        #expect(await model.loadOlder(key) == false)
        #expect(store.hasMore && !store.isLoadingOlder)
        #expect(hub.sent("GET", path).count == 2, "an ordinary failure still permits an explicit retry")
    }

    private func host(_ model: AppModel, key: SessionKey) throws -> UIWindow {
        try automation(true)
        let scene = try #require(UIApplication.shared.connectedScenes.compactMap { $0 as? UIWindowScene }.first)
        let window = UIWindow(windowScene: scene)
        window.frame = UIScreen.main.bounds
        window.rootViewController = UIHostingController(rootView: NavigationStack {
            Group {
                if FileManager.default.fileExists(atPath: "/tmp/persona-history-not-found-large") {
                    ChatView(key: key).environment(\.sizeCategory, .accessibilityExtraExtraExtraLarge)
                } else {
                    ChatView(key: key)
                }
            }.environment(model)
        })
        if FileManager.default.fileExists(atPath: "/tmp/persona-history-not-found-dark") {
            window.overrideUserInterfaceStyle = .dark
        } else if FileManager.default.fileExists(atPath: "/tmp/persona-history-not-found-light") {
            window.overrideUserInterfaceStyle = .light
        }
        window.isHidden = false
        window.layoutIfNeeded()
        return window
    }

    private func close(_ window: UIWindow) {
        window.isHidden = true
        window.rootViewController = nil
        try? automation(false)
    }

    private func elements(in window: UIWindow) -> [RuntimeCLI.Element] {
        RuntimeCLI.elements(matching: ["containers": true]).filter {
            RuntimeCLI.view(of: $0.object)?.window === window
        }
    }

    private func waitForHubSentence(_ sentence: String, in window: UIWindow) async -> [RuntimeCLI.Element] {
        var current = elements(in: window)
        for _ in 0..<60 where !current.contains(where: { $0.label == sentence }) {
            try? await Task.sleep(for: .milliseconds(50))
            window.layoutIfNeeded()
            current = elements(in: window)
        }
        // Let the same completed history state finish drawing its loading branch before reading the tree.
        try? await Task.sleep(for: .milliseconds(100))
        window.layoutIfNeeded()
        return elements(in: window)
    }

    private func waitForSkeleton(in window: UIWindow) async -> [RuntimeCLI.Element] {
        var current = elements(in: window)
        for _ in 0..<60 where !current.contains(where: { $0.id == "chat-loading-placeholder" }) {
            try? await Task.sleep(for: .milliseconds(50))
            window.layoutIfNeeded()
            current = elements(in: window)
        }
        return current
    }

    private func holdForScreenshot() async {
        let files = FileManager.default
        guard files.fileExists(atPath: "/tmp/persona-history-not-found-light")
                || files.fileExists(atPath: "/tmp/persona-history-not-found-dark") else { return }
        try? Data("ready\n".utf8).write(to: URL(fileURLWithPath: "/tmp/persona-history-not-found-ready"))
        try? await Task.sleep(for: .seconds(10))
    }

    private func automation(_ on: Bool) throws {
        let handle = try #require(dlopen("/usr/lib/libAccessibility.dylib", RTLD_NOW))
        let set = try #require(dlsym(handle, "_AXSSetAutomationEnabled"))
        typealias Enable = @convention(c) (Int32) -> Void
        unsafeBitCast(set, to: Enable.self)(on ? 1 : 0)
    }
}
