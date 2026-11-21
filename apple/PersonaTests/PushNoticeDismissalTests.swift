import Foundation
import PersonaWire
import Testing
@testable import Persona

/// The main screen's "Notifications can't reach this device" notice can be closed (#12319): a developer build that can
/// never get pushes kept it above the composer for good. A closed cause stays hidden on this device until pushes reach
/// it again; another cause or another hub shows its notice, and Settings › Notifications keeps the full status.
@MainActor
struct PushNoticeDismissalTests {
    /// A developer build: the relay does not serve this app and the hub has no APNs key of its own.
    private static func developerBuild(group: String = "g") -> PushHealth {
        var h = PushHealth(notificationsAllowed: true, relay: .refused(status: 403, message: "topic not served"))
        h.routes[group] = APIClient.PushRoute(route: "none", reason: "relay_failed", detail: h.relayError)
        return h
    }

    private func defaults() -> UserDefaults {
        let name = "PushNoticeDismissalTests-" + UUID().uuidString
        let d = UserDefaults(suiteName: name)!
        d.removePersistentDomain(forName: name)
        return d
    }

    @Test func eachOutageNamesItsCause() {
        #expect(Self.developerBuild().mainNotice(group: "g", allowed: nil)?.cause == .setup)
        var off = Self.developerBuild()
        off.notificationsAllowed = false
        #expect(off.mainNotice(group: "g", allowed: nil)?.cause == .notificationsOff)
        var moved = Self.developerBuild()
        moved.relay = .moved(status: 301, location: "https://relay.example.com/x")
        #expect(moved.mainNotice(group: "g", allowed: nil)?.cause == .updateApp)
    }

    @Test func closingHidesItAndTheNextLaunchRemembers() throws {
        let d = defaults()
        let store = PushHealthStore(health: Self.developerBuild(), defaults: d)
        let notice = try #require(store.notice(group: "g"))
        store.dismiss(notice, group: "g")
        #expect(store.notice(group: "g") == nil)

        // The next launch: the same device's defaults, the same outage.
        let relaunched = PushHealthStore(health: Self.developerBuild(), defaults: d)
        #expect(relaunched.notice(group: "g") == nil)
        // Settings › Notifications still says it.
        #expect(relaunched.health.status(group: "g", allowed: nil).level == .unavailable)
    }

    @Test func anotherCauseShowsAgain() throws {
        let store = PushHealthStore(health: Self.developerBuild(), defaults: defaults())
        store.dismiss(try #require(store.notice(group: "g")), group: "g")

        store.update { $0.notificationsAllowed = false }
        #expect(store.notice(group: "g")?.cause == .notificationsOff)

        // Turned back on: the closed cause is still hidden.
        store.update { $0.notificationsAllowed = true }
        #expect(store.notice(group: "g") == nil)
    }

    @Test func anotherHubShowsItsOwn() throws {
        var both = Self.developerBuild(group: "a")
        both.routes["b"] = both.routes["a"]
        let store = PushHealthStore(health: both, defaults: defaults())
        store.dismiss(try #require(store.notice(group: "a")), group: "a")
        #expect(store.notice(group: "a") == nil)
        #expect(store.notice(group: "b")?.cause == .setup)
    }

    @Test func aRecoveryForgetsItSoTheNextOutageShows() throws {
        let store = PushHealthStore(health: Self.developerBuild(), defaults: defaults())
        store.dismiss(try #require(store.notice(group: "g")), group: "g")

        store.update { $0.relay = .registered; $0.routes["g"] = APIClient.PushRoute(route: "relay") }
        #expect(store.dismissed["g"] == nil)

        store.update { $0 = Self.developerBuild() }
        #expect(store.notice(group: "g")?.cause == .setup)
    }

    @Test func anUncheckedHubIsNotARecovery() throws {
        let store = PushHealthStore(health: Self.developerBuild(), defaults: defaults())
        store.dismiss(try #require(store.notice(group: "g")), group: "g")

        // A failed check makes the status unknown; that says nothing about pushes coming back.
        store.update { $0.readFailed("The request timed out.", group: "g") }
        #expect(store.dismissed["g"] == [.setup])
        store.update { $0.read(Self.developerBuild().routes["g"], group: "g") }
        #expect(store.notice(group: "g") == nil)
    }
}
