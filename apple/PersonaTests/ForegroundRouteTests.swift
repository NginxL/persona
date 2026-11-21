import Foundation
import Testing
@testable import Persona

/// Foreground reads share the socket's route selection; they do not leave for an unavailable direct address while
/// SSH is still starting. The held route deliberately ignores cancellation to exercise stale completions too.
@MainActor
@Suite(.timeLimit(.minutes(1)))
struct ForegroundRouteTests {
    private let first = ServerConfig(url: URL(string: "http://127.0.0.1:9")!, token: "first")
    private let next = ServerConfig(url: URL(string: "http://127.0.0.1:10")!, token: "next")

    @MainActor private final class HeldRoute {
        var continuation: CheckedContinuation<ServerConfig?, Never>?
        var returned = false

        func pick() async -> ServerConfig? {
            let result = await withCheckedContinuation { continuation = $0 }
            returned = true
            return result
        }

        func finish(_ config: ServerConfig?) {
            let pending = continuation
            continuation = nil
            pending?.resume(returning: config)
        }
    }

    private func waitUntil(_ condition: () -> Bool) async throws {
        let deadline = ContinuousClock.now.advanced(by: .seconds(5))
        while !condition(), ContinuousClock.now < deadline { await Task.yield() }
        try #require(condition())
    }

    private func link(_ hub: FakeHub, group: String = "") -> HubLink {
        let pins = PinsStore(deps: .init(api: { nil }, report: { _ in }, suffix: UUID().uuidString))
        let link = HubLink(group: group, config: first, pins: pins)
        link.transportForTests = hub.client.transport
        return link
    }

    @Test func readsAndConditionalReadsWaitForRoutingButNotHello() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: "[]")
        hub.on("GET", "/api/v1/inbox", json: "{}")
        let link = link(hub), gate = HeldRoute()
        link.live.route = { await gate.pick() }
        link.live.connect(first)
        defer { gate.finish(nil); link.live.disconnect() }
        try await waitUntil { gate.continuation != nil }
        let api = link.api
        let read = Task { try await api.request("GET", "/api/v1/machines") }
        let cached = Task { try await api.conditionalGet("/api/v1/inbox", query: [:], etag: "previous") }
        try await waitUntil { link.live.waitingForRouteForTests == 2 }
        #expect(hub.sent("GET", "/api/v1/machines").isEmpty)
        #expect(hub.sent("GET", "/api/v1/inbox").isEmpty)
        gate.finish(next)
        #expect(try await read.value == Data("[]".utf8))
        #expect(try await cached.value.data == Data("{}".utf8))
        #expect(hub.sent("GET", "/api/v1/inbox").first?.query["If-None-Match"] == "previous")
        #expect(link.live.currentConfig == next)
        #expect(link.live.state != .connected, "Neither closed loopback port can send a WebSocket hello")
    }

    @Test func backgroundCancelsWaitingReadsBeforeTheRouteFinishes() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: "[]")
        let link = link(hub), gate = HeldRoute()
        link.live.route = { await gate.pick() }
        link.live.connect(first)
        defer { gate.finish(nil); link.live.disconnect() }
        try await waitUntil { gate.continuation != nil }
        let api = link.api
        let read = Task { try await api.request("GET", "/api/v1/machines") }
        try await waitUntil { link.live.waitingForRouteForTests == 1 }
        link.live.suspended = true
        link.live.disconnect()
        await #expect(throws: CancellationError.self) { try await read.value }
        #expect(hub.sent("GET", "/api/v1/machines").isEmpty)
        gate.finish(next)
        try await waitUntil { gate.returned }
        #expect(link.live.state == .idle)
        #expect(link.live.currentConfig == first, "A late route must not become the background connection")
    }

    @Test func cancellingOneReadDoesNotCancelTheSharedRoute() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: "[]")
        let link = link(hub), gate = HeldRoute()
        link.live.route = { await gate.pick() }
        link.live.connect(first)
        defer { gate.finish(nil); link.live.disconnect() }
        try await waitUntil { gate.continuation != nil }
        let api = link.api
        let cancelled = Task { try await api.request("GET", "/api/v1/machines") }
        let kept = Task { try await api.request("GET", "/api/v1/machines") }
        try await waitUntil { link.live.waitingForRouteForTests == 2 }
        cancelled.cancel()
        await #expect(throws: CancellationError.self) { try await cancelled.value }
        #expect(link.live.waitingForRouteForTests == 1)
        gate.finish(next)
        _ = try await kept.value
        #expect(hub.sent("GET", "/api/v1/machines").count == 1)
    }

    @Test func aReplacementAttemptDoesNotAcceptTheOldRoutesCompletion() async throws {
        let live = LiveConnection(), old = HeldRoute(), replacement = HeldRoute()
        live.route = { await old.pick() }
        live.connect(first)
        defer { old.finish(nil); replacement.finish(nil); live.disconnect() }
        try await waitUntil { old.continuation != nil }
        let before = Task { try await live.waitForRoute() }
        try await waitUntil { live.waitingForRouteForTests == 1 }
        live.route = { await replacement.pick() }
        live.reconnectNow()
        await #expect(throws: CancellationError.self) { try await before.value }
        try await waitUntil { replacement.continuation != nil }
        let after = Task { try await live.waitForRoute() }
        try await waitUntil { live.waitingForRouteForTests == 1 }
        old.finish(first)
        try await waitUntil { old.returned }
        #expect(live.waitingForRouteForTests == 1)
        replacement.finish(next)
        #expect(try await after.value == next)
        #expect(try await live.waitForRoute() == next, "A previously captured client uses the current route on its next request too")
    }

    @Test func anotherHubDoesNotWaitForTheSlowGroupsRoute() async throws {
        let slow = LiveConnection(), gate = HeldRoute(), hub = FakeHub()
        hub.on("GET", "/api/v1/machines", json: "[]")
        slow.route = { await gate.pick() }
        slow.connect(first)
        defer { gate.finish(nil); slow.disconnect() }
        try await waitUntil { gate.continuation != nil }
        let fast = link(hub, group: "other")
        _ = try await fast.api.request("GET", "/api/v1/machines")
        #expect(hub.sent("GET", "/api/v1/machines").count == 1)
        #expect(!gate.returned)
    }

    @Test func anOutsideActionJoinsTheForegroundRouteInsteadOfReplacingIt() async throws {
        let model = AppModel(stateFolder: FileManager.default.temporaryDirectory.appending(path: "foreground-route-" + UUID().uuidString))
        var hub = PairedHub(id: UUID().uuidString, config: first)
        hub.addresses = [HubAddress(url: next.url)] // Makes outside use the group's route even without an SSH fixture.
        model.registry = HubRegistry(hubs: [hub])
        model.buildLinks()
        let live = try #require(model.link(hub.group)).live, gate = HeldRoute()
        live.route = { await gate.pick() }
        live.connect(first)
        defer { gate.finish(nil); live.disconnect() }
        try await waitUntil { gate.continuation != nil }
        let action = Task {
            try await model.outside(APIClient(config: first, group: hub.group)) { $0.config }
        }
        try await waitUntil { live.waitingForRouteForTests == 1 }
        #expect(!gate.returned)
        gate.finish(next)
        #expect(try await action.value == next)
    }

    @Test func aCapturedClientUsesTheCurrentAddressAndCredentialOnEveryRequest() async throws {
        let link = link(FakeHub())
        link.live.suspended = true // Keep this URL-construction test independent of a real WebSocket.
        link.live.connect(first)
        var api = link.api
        api.transport = nil
        api.managerID = "chosen-manager"
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [ForegroundRouteEcho.self]
        api.session = URLSession(configuration: configuration)
        defer { api.session.invalidateAndCancel(); link.live.disconnect() }

        let initial = try JSONDecoder().decode([String: String].self, from: await api.request("GET", "/api/v1/machines"))
        #expect(initial["address"] == first.url.appending(path: "api/v1/machines").absoluteString)
        #expect(initial["authorization"] == "Bearer first")
        link.live.connect(next)
        let recovered = try JSONDecoder().decode([String: String].self, from: await api.request("GET", "/api/v1/machines"))
        #expect(recovered["address"] == next.url.appending(path: "api/v1/machines").absoluteString)
        #expect(recovered["authorization"] == "Bearer next")
        #expect(recovered["manager"] == "chosen-manager")
    }

    @Test func everyAsyncTransportChecksItsRouteBeforeNetworkOrFileIO() async {
        var api = APIClient(config: first)
        api.requestRoute = { throw CancellationError() }
        let file = URL(filePath: "/does-not-exist/foreground-route-test")
        await #expect(throws: CancellationError.self) { try await api.request("GET", "/api/v1/machines") }
        await #expect(throws: CancellationError.self) { try await api.conditionalGet("/api/v1/inbox", query: [:], etag: nil) }
        await #expect(throws: CancellationError.self) { try await api.streamed("POST", "/api/v1/voice/speak") }
        await #expect(throws: CancellationError.self) { try await api.putBytes("/api/v1/audio", data: Data()) }
        await #expect(throws: CancellationError.self) { try await api.uploadBytes(file: file, path: "/api/v1/files", query: [:]) }
        await #expect(throws: CancellationError.self) { try await api.upload(file: file, name: "test", machine: "m", progress: { _ in }) }
        await #expect(throws: CancellationError.self) { try await api.download("/api/v1/files", query: [:], to: file, progress: { _, _ in }) }
        await #expect(throws: CancellationError.self) { try await api.image(ref: "m/s/i") }
        await #expect(throws: CancellationError.self) { try await api.fullItem(SessionKey(machineID: "m", sessionID: "s"), id: "i") }
    }
}

private final class ForegroundRouteEcho: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let body = ["address": request.url!.absoluteString,
                    "authorization": request.value(forHTTPHeaderField: "Authorization") ?? "",
                    "manager": request.value(forHTTPHeaderField: "X-Persona-Manager") ?? ""]
        let response = HTTPURLResponse(url: request.url!, statusCode: 200, httpVersion: nil,
                                       headerFields: ["Content-Type": "application/json"])!
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: try! JSONEncoder().encode(body))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}
