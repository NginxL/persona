import Foundation
import Testing
@testable import Persona

/// The relay's answer, per path: "/refused/…" a 403 in its words, "/moved/…" a 301 to its new address.
private final class RelayStub: URLProtocol, @unchecked Sendable {
    override class func canInit(with request: URLRequest) -> Bool { request.url?.host == "relay.invalid" }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        let url = request.url!
        let (status, headers, body): (Int, [String: String], String) = url.path.hasPrefix("/moved")
            ? (301, ["Location": "https://relay.example.com/relay/v1/register"], "")
            : (403, ["Content-Type": "application/json"], #"{"error":"topic not served"}"#)
        client?.urlProtocol(self, didReceive: HTTPURLResponse(url: url, statusCode: status, httpVersion: "HTTP/1.1", headerFields: headers)!,
                            cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Data(body.utf8))
        client?.urlProtocolDidFinishLoading(self)
    }
    override func stopLoading() {}
}

/// Background push says whether it works and why not (#5998): a relay that refused this app or moved used to leave a
/// log line, and on a hub without its own APNs key nothing came.
@MainActor
struct PushHealthTests {
    private func session() -> URLSession {
        let c = URLSessionConfiguration.ephemeral
        c.protocolClasses = [RelayStub.self]
        return URLSession(configuration: c)
    }

    private func outcome(_ path: String) async -> PushHealth.Relay? {
        do {
            _ = try await PushRelay.grant(relay: URL(string: "https://relay.invalid\(path)")!, token: UUID().uuidString, env: "sandbox",
                                          topic: "app.example", session: session())
            return .registered
        } catch {
            return PushRelay.outcome(of: error)
        }
    }

    @Test func aRefusalKeepsTheRelaysWords() async {
        let o = await outcome("/refused/relay")
        #expect(o == .refused(status: 403, message: "topic not served"))
        #expect(PushHealth(relay: o).relayError == "403 topic not served")
    }

    @Test func aRedirectIsAMoveNotARegistration() async {
        let o = await outcome("/moved/relay")
        #expect(o == .moved(status: 301, location: "https://relay.example.com/relay/v1/register"))
        #expect(PushHealth(relay: o).relayError == "301 https://relay.example.com/relay/v1/register")
    }

    @Test func aNetworkErrorIsUnreachable() {
        #expect(PushRelay.outcome(of: URLError(.timedOut)) == .unreachable(URLError(.timedOut).localizedDescription))
    }

    @Test func theHubsWayDecides() {
        var h = PushHealth(notificationsAllowed: true, relay: .refused(status: 403, message: "topic not served"))
        // An older hub says nothing: unknown, with what the phone knows.
        #expect(h.status(group: "g", allowed: true).level == .unknown)
        #expect(h.status(group: "g", allowed: true).reason?.contains("403 topic not served") == true)
        // No way: unavailable, the relay's words, something to do.
        h.routes["g"] = APIClient.PushRoute(route: "none", reason: "relay_failed", detail: "403 topic not served")
        let none = h.status(group: "g", allowed: true)
        #expect(none.level == .unavailable && none.reason?.contains("403 topic not served") == true && none.action != nil)
        // The hub's own APNs key: available whatever the relay said.
        h.routes["g"] = APIClient.PushRoute(route: "direct")
        #expect(h.status(group: "g", allowed: true) == PushStatus(level: .available, title: h.status(group: "g", allowed: true).title))
        // The way is there, the last push did not go.
        h.routes["g"] = APIClient.PushRoute(route: "relay", reason: "push_failed", detail: "502 relay down")
        let failed = h.status(group: "g", allowed: true)
        #expect(failed.level == .lastFailed && failed.reason?.contains("502 relay down") == true)
        // Notifications off in Settings comes first.
        #expect(h.status(group: "g", allowed: false).level == .unavailable)
        // Another group's answer is not this one's.
        #expect(h.status(group: "other", allowed: true).level == .unknown)
    }

    @Test func noTokenSaysAPNsError() {
        var h = PushHealth(apnsError: "no valid aps-environment entitlement")
        h.routes["g"] = APIClient.PushRoute(route: "none", reason: "no_token")
        let s = h.status(group: "g", allowed: true)
        #expect(s.level == .unavailable && s.reason?.contains("aps-environment") == true)
    }

    @Test func aMovedRelayTellsToUpdate() {
        var h = PushHealth(relay: .moved(status: 301, location: "https://relay.example.com/x"))
        h.routes["g"] = APIClient.PushRoute(route: "none", reason: "relay_failed", detail: h.relayError)
        let s = h.status(group: "g", allowed: nil)
        #expect(s.level == .unavailable && s.reason?.contains("https://relay.example.com/x") == true)
    }

    /// A route that couldn't be read is unchecked, not the older one as if current (#1859); turned-off notifications
    /// still come first, and another hub's route is its own.
    @Test func aFailedCheckIsUncheckedNotAvailable() {
        var h = PushHealth(notificationsAllowed: true, relay: .registered)
        h.routes["g"] = APIClient.PushRoute(route: "relay")
        h.routes["other"] = APIClient.PushRoute(route: "relay")
        h.readFailed("The request timed out.", group: "g")
        let s = h.status(group: "g", allowed: true)
        #expect(s.level == .unknown && s.reason?.contains("The request timed out.") == true)
        #expect(s.action == String(localized: "Persona checks again when you open this page."))
        #expect(h.status(group: "g", allowed: false).level == .unavailable)
        #expect(h.status(group: "other", allowed: true).level == .available)

        h.read(APIClient.PushRoute(route: "relay", reason: "push_failed", detail: "503 push relay not configured"), group: "g")
        #expect(h.status(group: "g", allowed: true).level == .lastFailed)
        #expect(h.unchecked == nil)
    }

    @Test func aReadClearsOnlyItsHub() {
        var h = PushHealth()
        h.readFailed("offline", group: "a")
        h.readFailed("timed out", group: "b")
        #expect(h.unchecked == ["a": "offline", "b": "timed out"])
        h.read(nil, group: "a")
        #expect(h.unchecked == ["b": "timed out"] && h.routes["a"] == nil)
    }

    /// The main-screen notice distinguishes a current hub confirming this credential has no route (#9612 item 4) from
    /// an older hub that supplied no route data at all, which stays silent.
    @Test func aConfirmedMissingRouteIsAnOutageWhileAnOlderHubStaysUnknown() {
        var h = PushHealth(notificationsAllowed: true, relay: .registered)
        #expect(h.status(group: "g", allowed: true).level == .unknown)

        h.routes["g"] = APIClient.PushRoute(route: "relay")
        h.read(nil, group: "g")
        let missing = h.status(group: "g", allowed: true)
        #expect(missing.level == .unavailable)
        #expect(missing.action == String(localized: "Open Persona again to register once more."))

        h.readRegistration(APIClient.PushRoute(route: "direct"), group: "g")
        #expect(h.missingRoutes == nil && h.status(group: "g", allowed: true).level == .available)
    }

    @Test func anOlderRegistrationDoesNotConfirmAMissingRoute() {
        var h = PushHealth(notificationsAllowed: true, relay: .registered)
        h.readRegistration(nil, group: "g")
        #expect(h.missingRoutes == nil)
        #expect(h.status(group: "g", allowed: true).level == .unknown)
    }

    @Test func theStoreKeepsWhatChanged() {
        let store = PushHealthStore(health: PushHealth())
        store.update { $0.relay = .registered; $0.routes["g"] = APIClient.PushRoute(route: "relay") }
        #expect(store.health.relay == .registered && store.health.routes["g"]?.route == "relay")
        // An older hub's answer (nil) clears what a newer one said.
        store.update { $0.routes["g"] = nil }
        #expect(store.health.routes["g"] == nil)
    }
}
