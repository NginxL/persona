import Foundation
import Darwin
import os
import Testing
@testable import Persona
import PersonaWire

/// iOS ends an app that is suspended while it holds a file lock in the App Group (0xdead10cc, #11360). The store's lock
/// is only ever held inside background time, and a refresh cut short by the background writes nothing more.
@MainActor struct ExtensionSurfaceSuspensionTests {
    /// What a lease saw: whether the store's lock was free when background time began and when it ended.
    private final class LeaseLog: Sendable {
        let events = OSAllocatedUnfairLock<[String]>(initialState: [])
        func add(_ event: String) { events.withLock { $0.append(event) } }
        var all: [String] { events.withLock { $0 } }
    }

    @Test func theLockIsHeldOnlyInsideBackgroundTime() throws {
        try withStore { root in
            let log = LeaseLog()
            let store = ExtensionSurfaceStore(root: root, lease: {
                log.add(Self.lockIsFree(root) ? "begin, unlocked" : "begin, LOCKED")
                return { log.add(Self.lockIsFree(root) ? "end, unlocked" : "end, LOCKED") }
            })
            try store.transaction { _ in log.add(Self.lockIsFree(root) ? "write, UNLOCKED" : "write, locked") }
            try store.replaceCredentials(["": credential])
            #expect(log.all == ["begin, unlocked", "write, locked", "end, unlocked", "begin, unlocked", "end, unlocked"])
            #expect(store.read().groups[""]?.credential == credential)
        }
    }

    @Test func noBackgroundTimeMeansNoLockAndNoWrite() throws {
        try withStore { root in
            let open = ExtensionSurfaceStore(root: root)
            try open.replaceCredentials(["": credential])
            let file = root.appendingPathComponent("snapshot.json")
            let before = try Data(contentsOf: file)
            // The app is on its way to being suspended: iOS grants no more background time.
            let refused = ExtensionSurfaceStore(root: root, lease: { nil })
            #expect(throws: CancellationError.self) { try refused.replaceCredentials([:]) }
            #expect(refused.read().groups.isEmpty, "a refused read is unknown, not the stored snapshot")
            #expect(try Data(contentsOf: file) == before)
            #expect(Self.lockIsFree(root))
            #expect(open.read().groups[""]?.credential == credential)
        }
    }

    /// The catalog answer arrives just as the user leaves the app again: the refresh is cancelled and drops it.
    @Test func aRefreshCancelledByTheBackgroundWritesNothing() async throws {
        try await withAsyncStore { root in
            let log = LeaseLog()
            let store = ExtensionSurfaceStore(root: root, lease: {
                log.add("begin")
                return { log.add("end") }
            })
            try store.replaceCredentials(["": credential])
            let state = try #require(store.read().groups[""])
            let file = root.appendingPathComponent("snapshot.json")
            let before = try Data(contentsOf: file)
            let client = ExtensionSurfaceClient(credential: credential, transport: { request in
                // Answers only once the refresh has been cancelled, as a reply in flight would.
                while !Task.isCancelled { await Task.yield() }
                let body = switch request.url?.path {
                case "/api/v1/health": #"{"features":["extension_widgets","extension_shortcuts"]}"#
                case "/api/v1/extensions/-/widgets": #"{"widgets":[{"extension":"sample","id":"today","title":"Today","families":["small"],"refresh_minutes":30}]}"#
                default: #"{"shortcuts":[]}"#
                }
                return (Data(body.utf8), HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil, headerFields: nil)!)
            })
            let refresh = Task { try await client.refreshCatalog(group: "", expected: state, store: store) }
            refresh.cancel()
            let result = await refresh.result
            #expect(throws: CancellationError.self) { try result.get() }
            #expect(try Data(contentsOf: file) == before, "a cancelled refresh must not rewrite the shared snapshot")
            #expect(store.read().groups[""]?.widgets.isEmpty == true)
            #expect(Self.lockIsFree(root))
            let events = log.all
            #expect(events.filter { $0 == "begin" }.count == events.filter { $0 == "end" }.count)
        }
    }

    @Test func goingToTheBackgroundCancelsTheRefreshAndComingBackStartsAnew() async throws {
        let runs = OSAllocatedUnfairLock(initialState: (started: 0, cancelled: 0))
        let refresh = ExtensionSurfaceForegroundRefresh {
            runs.withLock { $0.started += 1 }
            while !Task.isCancelled { try? await Task.sleep(for: .milliseconds(5)) }
            runs.withLock { $0.cancelled += 1 }
        }
        refresh.start()
        refresh.start() // a second foreground signal while it runs is coalesced
        try await waitFor { runs.withLock { $0.started } == 1 }
        #expect(refresh.task != nil)
        refresh.cancel() // background
        #expect(refresh.task == nil)
        refresh.start() // straight back to the app: not held up by the cancelled run
        try await waitFor { runs.withLock { $0.started == 2 && $0.cancelled == 1 } }
        #expect(refresh.task != nil, "the cancelled run finishing must not forget the new one")
        refresh.cancel()
        try await waitFor { runs.withLock { $0.cancelled } == 2 }
    }

    // MARK: - Helpers

    private var credential: ExtensionSurfaceCredential {
        ExtensionSurfaceCredential(url: URL(string: "https://hub.example.com")!, token: "fixture-device-token", deviceID: "device", name: "Example")
    }

    /// `flock` locks belong to an open file, so a second open in this process sees the store's own lock.
    nonisolated private static func lockIsFree(_ root: URL) -> Bool {
        let descriptor = Darwin.open(root.appendingPathComponent("store.lock").path, O_RDWR)
        guard descriptor >= 0 else { return errno == ENOENT } // no lock file yet: nobody can hold it
        defer { close(descriptor) }
        guard flock(descriptor, LOCK_EX | LOCK_NB) == 0 else { return false }
        flock(descriptor, LOCK_UN)
        return true
    }

    private func withStore(_ body: (URL) throws -> Void) throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        try body(root)
    }

    private func withAsyncStore(_ body: (URL) async throws -> Void) async throws {
        let root = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: root) }
        try await body(root)
    }

    private func waitFor(_ condition: () -> Bool) async throws {
        let deadline = ContinuousClock.now.advanced(by: .seconds(5))
        while !condition() {
            guard ContinuousClock.now < deadline else { Issue.record("timed out"); return }
            try await Task.sleep(for: .milliseconds(10))
        }
    }
}
