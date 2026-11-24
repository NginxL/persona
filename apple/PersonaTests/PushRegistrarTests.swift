import Testing
import UserNotifications
@testable import Persona

/// The system as the registrar sees it: the notification switch, the permission prompt and APNs registration.
@MainActor
private final class FakeSystem {
    var paired = true
    /// The system's switch now (nil: never asked).
    var allowed: Bool?
    private(set) var requests = 0
    private(set) var registrations = 0
    /// The groups' first hubs; nil: the pairings can't be read (before the first unlock).
    var hubs: [PushRegistrar.Hub]? = []
    var grants = PushRegistrar.Grants()
    var relayURL: URL?
    var now = Date(timeIntervalSince1970: 1_000_000)
    /// Waits for a retry due while the app stays open: none by default (the test moves the clock and comes back).
    var sleep: @MainActor (Duration) async -> Bool = { _ in false }
    var waits: [Duration] = []
    /// The prompt on screen, waiting for an answer.
    private(set) var prompt: CheckedContinuation<Bool, Never>?

    var system: PushRegistrar.System {
        PushRegistrar.System(
            paired: { self.paired },
            request: {
                self.requests += 1
                if let allowed = self.allowed { return allowed } // asked before: no prompt, the switch's answer
                return await withCheckedContinuation { self.prompt = $0 }
            },
            allowed: { self.allowed },
            register: { self.registrations += 1 },
            hubs: { self.hubs },
            grants: { _, _, _ in self.grants },
            relayURL: { self.relayURL },
            now: { self.now },
            sleep: { await self.sleep($0) })
    }

    /// The prompt's tap reaches the system's switch first; the request's answer comes after.
    func answer(_ granted: Bool) {
        allowed = granted
    }

    func closePrompt() {
        prompt?.resume(returning: allowed ?? false)
        prompt = nil
    }
}

/// Allowing notifications in Settings after a denial registers for pushes when the app comes back, without a relaunch
/// (#1861): before, the hub had no token for this device until Persona was quit and opened again.
@MainActor
struct PushRegistrarTests {
    private func registrar(_ fake: FakeSystem) -> (PushRegistrar, PushHealthStore) {
        let health = PushHealthStore(health: PushHealth())
        return (PushRegistrar(system: fake.system, health: health), health)
    }

    @Test func allowingAfterADenialRegistersOnReturn() async {
        let fake = FakeSystem()
        fake.allowed = false
        let (r, health) = registrar(fake)
        await r.registerIfPossible()?.value
        #expect(fake.registrations == 0 && health.health.notificationsAllowed == false)

        await r.foreground()?.value // still off: nothing
        #expect(fake.registrations == 0 && !r.registered)

        fake.allowed = true // allowed in Settings, then back to Persona
        await r.foreground()?.value
        #expect(fake.registrations == 1 && r.registered)
        #expect(health.health.notificationsAllowed == true)

        // Later returns don't register again, and don't even check.
        #expect(r.foreground() == nil)
        #expect(fake.registrations == 1)
    }

    @Test func allowedAtLaunchRegistersOnce() async {
        let fake = FakeSystem()
        fake.allowed = true
        let (r, _) = registrar(fake)
        await r.registerIfPossible()?.value
        #expect(fake.registrations == 1)
        #expect(r.foreground() == nil)
        #expect(fake.registrations == 1)
    }

    /// The prompt's answer makes the app active again before the launch's request has its answer: the check waits for
    /// that request, so one answer registers once.
    @Test func anOpenPromptIsNotAnsweredTwice() async throws {
        let fake = FakeSystem()
        let (r, health) = registrar(fake)
        let launch = r.registerIfPossible()
        for _ in 0..<100 where fake.prompt == nil { await Task.yield() }
        try #require(fake.prompt != nil)

        fake.answer(true)
        let back = r.foreground()
        for _ in 0..<20 { await Task.yield() }
        fake.closePrompt()
        await launch?.value
        await back?.value
        #expect(fake.registrations == 1)
        #expect(health.health.notificationsAllowed == true)
    }

    /// Coming back never shows the prompt: a permission not asked yet is the launch's or the pairing's to ask.
    @Test func returningNeverPrompts() async {
        let fake = FakeSystem()
        let (r, health) = registrar(fake)
        let back = r.foreground()
        for _ in 0..<100 where fake.prompt == nil { await Task.yield() }
        #expect(fake.requests == 0 && fake.prompt == nil)
        fake.closePrompt() // a prompt shown by mistake would hold the check forever
        await back?.value
        #expect(fake.registrations == 0)
        #expect(health.health.notificationsAllowed == nil)
    }

    @Test func unpairedDoesNothing() async {
        let fake = FakeSystem()
        fake.paired = false
        fake.allowed = true
        let (r, _) = registrar(fake)
        #expect(r.registerIfPossible() == nil)
        #expect(r.foreground() == nil)
        #expect(fake.requests == 0 && fake.registrations == 0)
    }

    /// Pairing another hub registers again, so that hub learns the token too.
    @Test func pairingRegistersAgain() async {
        let fake = FakeSystem()
        fake.allowed = true
        let (r, _) = registrar(fake)
        await r.registerIfPossible()?.value
        await r.registerIfPossible()?.value
        #expect(fake.registrations == 2)
    }

    /// Settings › Notifications reads the hub's route for this device again (#1859): the one Settings › Devices shows,
    /// not the one kept since launch.
    @Test func theRouteIsReadAgainFromTheHub() async {
        let fake = FakeSystem()
        let (r, health) = registrar(fake)
        health.update { $0.routes["g"] = APIClient.PushRoute(route: "relay") } // what the launch's registration said
        let failing = APIClient.PushRoute(route: "relay", reason: "push_failed", detail: "503 push relay not configured")
        func tokens(_ push: [String: APIClient.PushRoute]?) -> APIClient.DeviceTokens {
            APIClient.DeviceTokens(tokens: [], current: "me", push: push)
        }

        await r.refreshRoute(group: "g") { tokens(["me": failing, "watch": APIClient.PushRoute(route: "relay")]) }
        #expect(health.health.routes["g"] == failing)
        #expect(health.health.status(group: "g", allowed: true).level == .lastFailed)

        // An older hub sends no routes: what is known stays.
        await r.refreshRoute(group: "g") { tokens(nil) }
        #expect(health.health.routes["g"] == failing)

        // Unreachable: unchecked, not the route from before; a cancelled read is no answer either way.
        await r.refreshRoute(group: "g") { throw URLError(.timedOut) }
        #expect(health.health.unchecked?["g"] == URLError(.timedOut).localizedDescription)
        #expect(health.health.status(group: "g", allowed: true).level == .unknown)
        await r.refreshRoute(group: "g") { throw CancellationError() }
        #expect(health.health.unchecked?["g"] == URLError(.timedOut).localizedDescription)

        // Back, with no registration from this device at that hub.
        await r.refreshRoute(group: "g") { tokens(["watch": APIClient.PushRoute(route: "relay")]) }
        #expect(health.health.routes["g"] == nil && health.health.unchecked == nil)
    }

    /// The main-screen notice also sees the system switch turned off after a successful registration (#9612 item 4).
    @Test func returningAfterRegistrationRefreshesTheNotificationSwitch() async {
        let fake = FakeSystem(), hub = Hub()
        let (r, health) = await registered(fake, hub)
        #expect(health.health.notificationsAllowed == true)

        fake.allowed = false
        await r.foreground()?.value
        #expect(health.health.notificationsAllowed == false)
        #expect(hub.posts == 1)
    }

    // MARK: - Registering again (#9612)

    /// A hub that takes this device's registration; `retired` is its push registration gone (#9611), which a
    /// registration brings back unless `keepsRetiring`.
    @MainActor
    private final class Hub {
        let fake = FakeHub()
        var retired = false
        var keepsRetiring = false
        var refusing = false
        /// The push routes `GET /api/v1/auth/tokens` lists, by credential ("me" is this device's).
        var routes: [String: String] = ["me": "direct"]

        init() {
            fake.on("POST", "/api/v1/devices") { [unowned self] _ in
                if refusing { throw URLError(.notConnectedToInternet) }
                if !keepsRetiring { retired = false }
                return Data(#"{"push":{"route":"direct"}}"#.utf8)
            }
            fake.on("PUT", "/api/v1/devices/d/prefs") { [unowned self] _ in
                if retired {
                    throw APIError(status: 409, message: "register again", fields: ["code": APIClient.pushRegistrationRequired])
                }
                return Data(#"{"input":true}"#.utf8)
            }
            fake.on("GET", "/api/v1/auth/tokens") { [unowned self] _ in
                let push = routes.map { #""\#($0.key)":{"route":"\#($0.value)"}"# }.joined(separator: ",")
                return Data(#"{"tokens":[],"current":"me","push":{\#(push)}}"#.utf8)
            }
        }

        var posts: Int { fake.sent("POST", "/api/v1/devices").count }
        var puts: Int { fake.sent("PUT", "/api/v1/devices/d/prefs").count }
        var reads: Int { fake.sent("GET", "/api/v1/auth/tokens").count }

        /// The app's client for this hub, answering a 409 through `r`, as `APIClient.onPushRegistrationRequired` does.
        func api(_ r: PushRegistrar) -> APIClient {
            var api = fake.client
            api.registerPush = { group in await r.registrationRequired(group: group).value }
            return api
        }
    }

    private let token = Data([0xAB, 0xCD])

    /// Notifications allowed, APNs answered and the hub took the launch's registration.
    private func registered(_ fake: FakeSystem, _ hub: Hub) async -> (PushRegistrar, PushHealthStore) {
        fake.allowed = true
        fake.hubs = [PushRegistrar.Hub(group: "", api: hub.fake.client)]
        let (r, health) = registrar(fake)
        await r.registerIfPossible()?.value
        await r.didRegister(token: token)?.value
        return (r, health)
    }

    private struct DevicePost: Decodable {
        var pushKey: String?
        var relayURL: String?
        var relayGrant: String?
        var relayError: String?
        enum CodingKeys: String, CodingKey {
            case pushKey = "push_key", relayURL = "relay_url", relayGrant = "relay_grant", relayError = "relay_error"
        }
    }

    private func devicePost(_ hub: Hub) throws -> DevicePost {
        let post = try #require(hub.fake.sent("POST", "/api/v1/devices").first)
        let body = try #require(post.body)
        return try JSONDecoder().decode(DevicePost.self, from: body)
    }

    /// A refusal names the relay that gave it, so the hub can identify which relay needs fixing (#12988).
    @Test func aRefusedRelayRegistrationSendsTheAttemptedURLAndError() async throws {
        let fake = FakeSystem(), hub = Hub()
        fake.relayURL = URL(string: "https://relay.example.com/relay")
        fake.grants.relay = .refused(status: 403, message: "topic not served")
        _ = await registered(fake, hub)

        let body = try devicePost(hub)
        let relay = try #require(fake.relayURL)
        #expect(body.relayError == "403 topic not served")
        #expect(body.relayGrant == nil)
        #expect(body.relayURL == relay.absoluteString)
    }

    @Test func aSuccessfulRelayRegistrationSendsTheURLGrantAndPushKey() async throws {
        let fake = FakeSystem(), hub = Hub()
        let key = try #require(PushCrypto.ensureKey())
        fake.relayURL = URL(string: "https://relay.example.com/relay")
        fake.grants.relay = .registered
        fake.grants.grant = "test-grant"
        _ = await registered(fake, hub)

        let body = try devicePost(hub)
        let relay = try #require(fake.relayURL)
        #expect(body.relayURL == relay.absoluteString)
        #expect(body.relayGrant == "test-grant")
        #expect(body.pushKey == PushCrypto.publicKey(key))
        #expect(body.relayError == nil)
    }

    @Test func aBuildWithoutARelaySendsNoRelayFields() async throws {
        let fake = FakeSystem(), hub = Hub()
        _ = await registered(fake, hub)

        let body = try devicePost(hub)
        #expect(body.relayURL == nil)
        #expect(body.relayGrant == nil)
        #expect(body.relayError == nil)
    }

    /// 409 `push_registration_required` on a settings write: exactly one registration, then the write again.
    @Test func aRetiredRegistrationRegistersAgainAndTheWriteGoesThrough() async throws {
        let fake = FakeSystem(), hub = Hub()
        let (r, _) = await registered(fake, hub)
        #expect(hub.posts == 1)

        hub.retired = true // the hub revoked the credential it was under
        let prefs = try await hub.api(r).setNotifyPrefs(device: "d", NotifyPrefs())
        #expect(prefs.input == true)
        #expect(hub.posts == 2 && hub.puts == 2)

        // Registered again, the next write goes straight through.
        _ = try await hub.api(r).setNotifyPrefs(device: "d", NotifyPrefs())
        #expect(hub.posts == 2 && hub.puts == 3)
    }

    /// A hub that keeps answering 409 after taking the registration doesn't make a loop: one registration, then the
    /// 409 stands; another 409 soon after starts none.
    @Test func aRepeated409DoesNotLoop() async {
        let fake = FakeSystem(), hub = Hub()
        let (r, _) = await registered(fake, hub)
        hub.retired = true
        hub.keepsRetiring = true

        await #expect(throws: APIError.self) { try await hub.api(r).setNotifyPrefs(device: "d", NotifyPrefs()) }
        #expect(hub.posts == 2 && hub.puts == 2)
        await #expect(throws: APIError.self) { try await hub.api(r).setNotifyPrefs(device: "d", NotifyPrefs()) }
        #expect(hub.posts == 2 && hub.puts == 3)

        // Ten minutes on, a 409 may register once more.
        fake.now += PushRegistrar.requiredCooldown
        await #expect(throws: APIError.self) { try await hub.api(r).setNotifyPrefs(device: "d", NotifyPrefs()) }
        #expect(hub.posts == 3)
    }

    /// Without an APNs token in this process there is nothing to register with: the 409 stands, nothing is sent.
    @Test func a409BeforeAnyTokenSendsNothing() async {
        let fake = FakeSystem(), hub = Hub()
        fake.hubs = [PushRegistrar.Hub(group: "", api: hub.fake.client)]
        let (r, _) = registrar(fake)
        hub.retired = true
        await #expect(throws: APIError.self) { try await hub.api(r).setNotifyPrefs(device: "d", NotifyPrefs()) }
        #expect(hub.posts == 0 && hub.puts == 1)
    }

    /// A registration POST that failed is tried again when the app comes back, once its backoff is over.
    @Test func aFailedRegistrationIsTriedAgainOnReturn() async {
        let fake = FakeSystem(), hub = Hub()
        hub.refusing = true
        let (r, health) = await registered(fake, hub)
        #expect(hub.posts == 1)
        #expect(health.health.unchecked?[""] != nil)

        await r.foreground()?.value // back at once: still waiting out the backoff
        #expect(hub.posts == 1 && hub.reads == 0)

        fake.now += PushRegistrar.backoff(1)
        await r.foreground()?.value // due, still refused: the wait doubles
        #expect(hub.posts == 2)
        fake.now += PushRegistrar.backoff(1)
        await r.foreground()?.value
        #expect(hub.posts == 2)

        hub.refusing = false
        fake.now += PushRegistrar.backoff(2)
        await r.foreground()?.value
        #expect(hub.posts == 3)
        #expect(health.health.routes[""]?.route == "direct" && health.health.unchecked == nil)
        #expect(fake.registrations == 1) // APNs isn't asked again: its token is kept
    }

    /// While the app stays open, a failed registration is tried again when it is due, and stops once it reaches the hub.
    @Test func aFailedRegistrationIsTriedAgainWhileOpen() async throws {
        let fake = FakeSystem(), hub = Hub()
        fake.sleep = { d in
            fake.waits.append(d)
            fake.now += TimeInterval(d.components.seconds)
            if fake.waits.count == 2 { hub.refusing = false }
            return fake.waits.count <= 3
        }
        hub.refusing = true
        _ = await registered(fake, hub)
        for _ in 0..<200 where hub.posts < 3 { await Task.yield() }
        #expect(hub.posts == 3)
        #expect(fake.waits == [.seconds(PushRegistrar.backoff(1)), .seconds(PushRegistrar.backoff(2))])
    }

    /// A registration to every group that one hub missed goes to every hub again after its backoff; only a pass that
    /// reached them all ends the retries.
    @Test func aRegistrationThatMissedOneHubIsSentToEveryHubAgain() async {
        let fake = FakeSystem(), a = Hub(), b = Hub()
        b.refusing = true
        fake.allowed = true
        fake.hubs = [PushRegistrar.Hub(group: "a", api: a.fake.client), PushRegistrar.Hub(group: "b", api: b.fake.client)]
        let (r, health) = registrar(fake)
        await r.registerIfPossible()?.value
        #expect(await r.didRegister(token: token)?.value == ["a"])
        #expect(a.posts == 1 && b.posts == 1)

        await r.foreground()?.value // back at once: still waiting out the backoff
        #expect(a.posts == 1 && b.posts == 1)
        fake.now += PushRegistrar.backoff(1)
        await r.foreground()?.value // due: every hub again; b still refuses, so the wait doubles
        #expect(a.posts == 2 && b.posts == 2)
        fake.now += PushRegistrar.backoff(1)
        await r.foreground()?.value
        #expect(a.posts == 2 && b.posts == 2)

        b.refusing = false
        fake.now += PushRegistrar.backoff(2)
        await r.foreground()?.value
        #expect(a.posts == 3 && b.posts == 3)
        #expect(health.health.routes["b"]?.route == "direct")

        fake.now += PushRegistrar.backoff(3) // every hub took it: nothing left to try again
        await r.foreground()?.value
        #expect(a.posts == 3 && b.posts == 3 && a.reads == 0 && b.reads == 0)
    }

    @Test func theBackoff() {
        #expect(PushRegistrar.backoff(1) == 30 && PushRegistrar.backoff(2) == 60 && PushRegistrar.backoff(3) == 120)
        #expect(PushRegistrar.backoff(20) == 3600)
    }

    /// Once the hub took it, coming back registers no more and asks the hub at most once an hour.
    @Test func aRegistrationThatReachedTheHubIsNotRepeated() async {
        let fake = FakeSystem(), hub = Hub()
        let (r, _) = await registered(fake, hub)
        for _ in 0..<3 {
            fake.now += 600
            await r.foreground()?.value
        }
        #expect(hub.posts == 1 && hub.reads == 0)

        fake.now += PushRegistrar.checkInterval // the hub still lists a route for this credential: nothing to do
        await r.foreground()?.value
        await r.foreground()?.value
        #expect(hub.posts == 1 && hub.reads == 1)
        #expect(fake.registrations == 1)
    }

    /// Coming back an hour later to a hub that lists no push route for this credential registers with it, though this
    /// process registered long ago.
    @Test func aHubWithNoRouteForThisDeviceIsRegisteredWithAgain() async {
        let fake = FakeSystem(), hub = Hub()
        let (r, health) = await registered(fake, hub)
        #expect(r.registered)
        hub.routes = ["watch": "relay"] // retired: only the watch's credential has a route
        fake.now += PushRegistrar.checkInterval
        await r.foreground()?.value
        #expect(hub.reads == 1 && hub.posts == 2)
        #expect(health.health.routes[""]?.route == "direct")
    }

    /// Pairings that can't be read when APNs answers (before the first unlock) are registered with once they can.
    @Test func unreadablePairingsAreTriedAgain() async {
        let fake = FakeSystem(), hub = Hub()
        fake.allowed = true
        fake.hubs = nil
        let (r, _) = registrar(fake)
        await r.registerIfPossible()?.value
        await r.didRegister(token: token)?.value
        #expect(hub.posts == 0)

        fake.hubs = [PushRegistrar.Hub(group: "", api: hub.fake.client)]
        fake.now += PushRegistrar.backoff(1)
        await r.foreground()?.value
        #expect(hub.posts == 1)
    }

    /// APNs gave no token: asked again when the app comes back after the backoff, not at every return.
    @Test func apnsIsAskedAgainAfterItFailed() async {
        let fake = FakeSystem()
        fake.allowed = true
        let (r, health) = registrar(fake)
        await r.registerIfPossible()?.value
        r.didFail(URLError(.notConnectedToInternet))
        #expect(health.health.apnsError != nil)
        await r.foreground()?.value
        #expect(fake.registrations == 1)
        fake.now += PushRegistrar.backoff(1)
        await r.foreground()?.value
        #expect(fake.registrations == 2)
        await r.foreground()?.value
        #expect(fake.registrations == 2)
    }

    @Test func theSystemsSwitch() {
        #expect(UNAuthorizationStatus.notDetermined.allowsNotifications == nil)
        #expect(UNAuthorizationStatus.denied.allowsNotifications == false)
        #expect(UNAuthorizationStatus.authorized.allowsNotifications == true)
        #expect(UNAuthorizationStatus.provisional.allowsNotifications == true)
        #expect(UNAuthorizationStatus.ephemeral.allowsNotifications == true)
    }
}
