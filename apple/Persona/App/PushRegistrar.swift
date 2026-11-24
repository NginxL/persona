import Foundation
import UserNotifications
import UIKit

/// Registers for APNs and tells the hub where to push (interactions, finished turns), as kind `iphone`, through the
/// relay for a hub without the app's key.
final class PushRegistrar {
    static let shared = PushRegistrar()

    /// A group's first hub, the one its registration goes to.
    struct Hub {
        var group: String
        var api: APIClient
    }

    /// The push relay's grants for this device's tokens (docs/operations/push-relay.md), for every hub of one
    /// registration.
    struct Grants {
        var relay = PushHealth.Relay.notConfigured
        var grant: String?
        /// The urgent calls' PushKit token, while calls are on, with its own grant or why there is none.
        var voip: String?
        var voipGrant: String?
        var voipError: String?
    }

    /// What the registrar asks of the system; a test passes its own.
    struct System {
        /// Paired with a hub that takes pushes (not the demo, not simulator automation).
        var paired: @MainActor () -> Bool
        /// Asks for permission, with the system's prompt the first time.
        var request: @MainActor () async -> Bool
        /// Whether notifications are allowed now, without asking (nil: not asked yet).
        var allowed: @MainActor () async -> Bool?
        /// Registers with APNs; the token comes back in `didRegister(token:)`.
        var register: @MainActor () -> Void
        /// Each group's first hub (not the demo); nil: the pairings can't be read now (before the first unlock).
        var hubs: @MainActor () async -> [Hub]? = { [] }
        /// The relay's grants for the APNs token (hex), in this environment and topic.
        var grants: @MainActor (_ token: String, _ env: String, _ topic: String?) async -> Grants = { _, _, _ in Grants() }
        /// The relay this build can attempt, from its Info.plist; nil: no relay is configured.
        var relayURL: @MainActor () -> URL? = { PushRelay.url }
        var now: @MainActor () -> Date = { Date() }
        /// Waits before a retry that is due while the app stays open; false: cancelled.
        var sleep: @MainActor (Duration) async -> Bool = { d in (try? await Task.sleep(for: d)) != nil }
    }

    /// What a registration that did not reach the hub was for: every group (no token reached the hubs, or the
    /// pairings couldn't be read) or one group's hub.
    private enum Target: Hashable {
        case all
        case group(String)
    }

    /// A registration that did not reach its hub: tried again after `PushRegistrar.backoff(count)` (#9612).
    private struct Failure {
        var count: Int
        var at: Date
    }

    private let system: System
    private let health: PushHealthStore
    /// Asked APNs for a token in this process. A denial at launch or pairing leaves it false until Settings allows
    /// notifications and the app comes back (`foreground()`).
    private(set) var registered = false
    /// The last ask or check: the next one waits for it, so a prompt still open is never answered twice.
    private var last: Task<Void, Never>?
    /// The APNs token this process registered with, so a change of the urgent calls' PushKit token registers again.
    private var lastToken: Data?
    /// Registrations that did not reach a hub, and APNs refusing a token (`apns`): retried with backoff when the app
    /// comes to the front or, while it stays open, when they are due. Before #9612 each was left until the next launch.
    private var failures: [Target: Failure] = [:]
    private var apnsFailure: Failure?
    /// The registration on its way to the hubs: one at a time, so a hub never has two from this device in flight.
    private var sending: (id: Int, target: Target, task: Task<Set<String>, Never>)?
    private var sendings = 0
    /// When each group's hub last confirmed (or was asked) that it has a push route for this device's credential.
    private var checked: [String: Date] = [:]
    /// When a 409 `push_registration_required` last registered each group: a hub that says so again soon after is not
    /// answered with another registration (no loop).
    private var required: [String: Date] = [:]
    private var retry: Task<Void, Never>?

    /// How often coming to the front asks a hub whether it still has a push route for this device.
    static let checkInterval: TimeInterval = 3600
    /// How long after a 409 registered a group another 409 from it starts no registration.
    static let requiredCooldown: TimeInterval = 600

    /// The wait before trying again after `count` failures in a row: 30 s, doubling, at most an hour.
    nonisolated static func backoff(_ count: Int) -> TimeInterval {
        min(30 * pow(2, Double(max(count, 1) - 1)), 3600)
    }

    init(system: System = .live, health: PushHealthStore = .shared) {
        self.system = system
        self.health = health
    }

    /// Asks for permission and registers if it is given: at launch, after pairing and on a sign-out that leaves a hub.
    /// Registers each time, so a newly paired hub learns this device's token.
    @discardableResult
    func registerIfPossible() -> Task<Void, Never>? {
        guard system.paired() else { return nil }
        return queue { [self] in
            // Asked here, not when the first notification is due: a push that comes while the app is closed shows
            // only if this was allowed before.
            let granted = await system.request()
            health.update { $0.notificationsAllowed = granted }
            if granted { register() }
        }
    }

    /// The app came back to the front (#1861). Notifications allowed in Settings after a denial take effect now: this
    /// registers, which tells every hub this device's token, as a launch does. Before, nothing registered until the app
    /// was quit and opened again, so the hub had no token for it. Never prompts: a permission not asked yet is left to
    /// the launch's or the pairing's request.
    ///
    /// Already registered (#9612): a registration that did not reach a hub is tried again once its backoff is over,
    /// and at most once an hour each hub is asked whether it still has a push route for this device's credential
    /// (a hub retires it when the credential is revoked); one that has none is registered with again.
    @discardableResult
    func foreground() -> Task<Void, Never>? {
        guard system.paired() else { return nil }
        if registered {
            guard lastToken != nil || apnsFailure != nil else { return nil } // APNs hasn't answered yet
            return queue { [self] in
                // The main-screen notice needs the system switch now, not only the value cached at launch (#9612 item 4).
                if let allowed = await system.allowed() { health.update { $0.notificationsAllowed = allowed } }
                await retryDue()
                await checkRoutes()
            }
        }
        return queue { [self] in
            guard !registered, let allowed = await system.allowed() else { return }
            health.update { $0.notificationsAllowed = allowed }
            if allowed { register() }
        }
    }

    /// A hub answered a write for this device with 409 `push_registration_required` (it retired the registration,
    /// #9611): registers with that group again, once, and says whether that reached it, so the write is tried again.
    /// A registration already on its way is waited for; a hub that says so again soon after one, or one still in its
    /// backoff after a failure, gets none (false).
    func registrationRequired(group: String) -> Task<Bool, Never> {
        if let s = sending, s.target == .all || s.target == .group(group) {
            return Task { await s.task.value.contains(group) }
        }
        let now = system.now()
        if let at = required[group], now.timeIntervalSince(at) < Self.requiredCooldown { return Task { false } }
        if let f = failures[.group(group)], now < f.at.addingTimeInterval(Self.backoff(f.count)) { return Task { false } }
        guard system.paired(), let task = send(.group(group)) else {
            // No APNs token in this process yet: the registration it brings goes to every hub.
            return Task { false }
        }
        required[group] = now
        return Task { await task.value.contains(group) }
    }

    /// The way the hub at group pushes to this device now, as Settings › Devices reads it (`GET /api/v1/auth/tokens`):
    /// Settings › Notifications asks when it opens (#1859). The registration's answer is kept from launch or pairing,
    /// so pushes that started failing since (or stopped failing) showed only after the next launch, while Settings ›
    /// Devices already said so. No entry for this credential: the hub has no push registration from it. A hub that sends
    /// no routes (older) leaves what is known. A failed read says so instead of the older route; a cancelled one is no
    /// answer.
    func refreshRoute(group: String, tokens: () async throws -> APIClient.DeviceTokens) async {
        _ = await readRoute(group: group, tokens: tokens)
    }

    /// `refreshRoute`'s read; true when the hub lists no push route for this device's credential.
    private func readRoute(group: String, tokens: () async throws -> APIClient.DeviceTokens) async -> Bool {
        do {
            let r = try await tokens()
            guard let routes = r.push, let current = r.current else { return false }
            health.update { $0.read(routes[current], group: group) }
            return routes[current] == nil
        } catch {
            if error.isCancellation { return false }
            health.update { $0.readFailed(error.localizedDescription, group: group) }
            return false
        }
    }

    private func queue(_ work: @escaping @MainActor () async -> Void) -> Task<Void, Never> {
        let previous = last
        let task = Task {
            await previous?.value
            await work()
        }
        last = task
        return task
    }

    private func register() {
        registered = true
        system.register()
    }

    /// Registers with every group: at its first hub, the one that issued the credential (docs/archive/operations/plan-hub-mesh.md 2.6: the
    /// group's other hubs pass their pushes to it, one push per device), each group sealing to a key of its own
    /// (docs/protocol/multi-hub.md「Push and notifications」).
    @discardableResult
    func didRegister(token: Data) -> Task<Set<String>, Never>? {
        lastToken = token
        apnsFailure = nil
        return send(.all)
    }

    /// The urgent calls' PushKit token came, changed or went: the hubs hear it with this device's other push fields.
    func reregister() {
        guard lastToken != nil else { return } // no APNs token yet: the first registration carries it
        send(.all)
    }

    /// APNs gave no token (no network, a build without the push entitlement): Settings › Notifications says so, and
    /// the app asks again later (`retryDue`).
    func didFail(_ error: any Error) {
        let text = error.localizedDescription
        health.update { $0.apnsError = text }
        apnsFailure = Failure(count: (apnsFailure?.count ?? 0) + 1, at: system.now())
        scheduleRetry()
    }

    /// Sends this device's push fields to the target's hubs, after the registration on its way (one at a time).
    /// Returns the groups whose hub took it; nil without an APNs token.
    @discardableResult
    private func send(_ target: Target) -> Task<Set<String>, Never>? {
        guard let token = lastToken else { return nil }
        sendings += 1
        let id = sendings
        let previous = sending?.task
        let task = Task { [self] in
            await previous?.value
            let done = await post(token: token, target: target)
            if sending?.id == id { sending = nil }
            return done
        }
        sending = (id, target, task)
        return task
    }

    private func post(token: Data, target: Target) async -> Set<String> {
        guard let all = await system.hubs() else {
            fail(target) // the pairings can't be read yet: the hubs hear nothing until they can
            return []
        }
        let hubs = all.filter { if case .group(let g) = target { $0.group == g } else { true } }
        guard !hubs.isEmpty else {
            failures[target] = nil // nothing paired there now
            return []
        }
        let hex = token.map { String(format: "%02x", $0) }.joined()
        #if DEBUG
        let env = "sandbox"
        #else
        let env = "production"
        #endif
        let kind = "iphone"
        let features: [String]? = Self.features(RequestClientMetadata.current)
        let base = APIClient.DeviceBody(deviceID: Self.deviceID, name: UIDevice.current.name, kind: kind, apnsToken: hex,
                                        apnsEnv: env, topic: Bundle.main.bundleIdentifier, features: features)
        // A hub without the app's APNs key pushes through the relay, sealed to this group's key (docs/operations/push-relay.md);
        // the notification service extension opens it. The hub decides: with its own key it ignores these.
        let grants = await system.grants(hex, env, base.topic)
        let relay = system.relayURL()
        let relayError = PushHealth(relay: grants.relay).relayError
        health.update { $0.apnsError = nil; $0.relay = grants.relay }
        var done = Set<String>()
        for hub in hubs {
            var body = base
            if let relay, grants.relay != .notConfigured {
                body.relayURL = relay.absoluteString
            }
            if let grant = grants.grant, relay != nil, let key = PushCrypto.ensureKey(group: hub.group) {
                body.relayGrant = grant
                body.pushKey = PushCrypto.publicKey(key)
            } else {
                body.relayError = relayError
            }
            if let voip = grants.voip {
                body.voipToken = voip
                if let voipGrant = grants.voipGrant, body.relayGrant != nil { body.voipGrant = voipGrant } else { body.voipError = grants.voipError }
            }
            do {
                let routes = try await hub.api.registerRoutes(device: body) // nil routes: an older hub, which does not say
                #if os(iOS)
                UrgentCallReach.shared.routes[hub.group] = routes.call
                #endif
                health.update { $0.readRegistration(routes.push, group: hub.group) }
                checked[hub.group] = system.now()
                done.insert(hub.group)
            } catch {
                // Not the previous route as if current: the page says it couldn't check (#1859).
                health.update { $0.readFailed(error.localizedDescription, group: hub.group) }
            }
        }
        // A pass to every group stands for each group's own retry. It ends the retries only when it reached every hub;
        // one hub it missed keeps it, and the whole registration goes out again after its backoff.
        if target == .all { for hub in hubs { failures[.group(hub.group)] = nil } }
        if done.count == hubs.count { failures[target] = nil } else { fail(target) }
        return done
    }

    private func fail(_ target: Target) {
        failures[target] = Failure(count: (failures[target]?.count ?? 0) + 1, at: system.now())
        scheduleRetry()
    }

    /// When the earliest retry is due; nil: nothing to retry.
    private var nextDue: Date? {
        let all = failures.values.map { $0.at.addingTimeInterval(Self.backoff($0.count)) }
            + [apnsFailure.map { $0.at.addingTimeInterval(Self.backoff($0.count)) }].compactMap { $0 }
        return all.min()
    }

    /// While the app stays open, the earliest retry runs when it is due; coming to the front runs the due ones too.
    private func scheduleRetry() {
        retry?.cancel()
        guard let due = nextDue else { return }
        let wait = max(due.timeIntervalSince(system.now()), 1)
        retry = Task { [weak self, sleep = system.sleep] in
            guard await sleep(.seconds(wait)), let self else { return }
            await self.retryDue()
        }
    }

    /// Tries again what is due: APNs for a token, every group, or one group's hub.
    private func retryDue() async {
        let now = system.now()
        func due(_ f: Failure) -> Bool { now >= f.at.addingTimeInterval(Self.backoff(f.count)) }
        if lastToken == nil {
            if let f = apnsFailure, due(f), system.paired() {
                apnsFailure = Failure(count: f.count, at: now) // the answer (didRegister / didFail) settles it
                system.register()
            }
            return
        }
        let targets = failures.filter { due($0.value) }.map(\.key)
        if targets.contains(.all) {
            await send(.all)?.value
        } else {
            for target in targets { await send(target)?.value }
        }
    }

    /// Asks each hub not asked within the hour whether it still has a push route for this device's credential, and
    /// registers with the ones that have none (their registration was retired, #9611). An older hub doesn't say.
    private func checkRoutes() async {
        guard lastToken != nil, let hubs = await system.hubs() else { return }
        let now = system.now()
        for hub in hubs {
            if let at = checked[hub.group], now.timeIntervalSince(at) < Self.checkInterval { continue }
            if failures[.group(hub.group)] != nil || failures[.all] != nil { continue } // its retry is under way
            checked[hub.group] = now
            if await readRoute(group: hub.group, tokens: { try await hub.api.deviceTokens(timeout: 15) }) {
                await send(.group(hub.group))?.value
            }
        }
    }

    /// What this app takes in pushes beyond the first ones (`features`, docs/protocol/protocol.md devices): withdrawals
    /// (AppDelegate removes the notifications they name, #4349), and MCP forms and links (`INTERACTION_ELICITATION`,
    /// #2433) where `client_hello` declares them, the iOS app alone (`LiveConnection.helloFeatures`): the hub pushes
    /// those cards only to such a phone, and waits for it while no app that shows them is open. An older hub drops the name.
    nonisolated static func features(_ client: RequestClientMetadata) -> [String] {
        ["withdraw"] + LiveConnection.helloFeatures(client).filter { $0 == "elicitation" }
    }

    /// This device's id at the hub: its push token and its notification settings hang off it.
    static var deviceID: String {
        if let id = UserDefaults.standard.string(forKey: "deviceID") { return id }
        let id = UUID().uuidString
        UserDefaults.standard.set(id, forKey: "deviceID")
        return id
    }
}

extension PushRegistrar.System {
    static let live = PushRegistrar.System(
        paired: {
            guard AppModel.shared.isPaired, !AppModel.shared.isDemo else { return false }
            #if DEBUG
            // Simulator and snapshot automation: no permission alert covering the screenshots.
            if UserDefaults.standard.string(forKey: "pairURL") != nil { return false }
            #endif
            return true
        },
        request: {
            (try? await UNUserNotificationCenter.current().requestAuthorization(options: [.alert, .sound, .badge, .timeSensitive])) ?? false
        },
        allowed: { await UNUserNotificationCenter.current().notificationSettings().authorizationStatus.allowsNotifications },
        register: {
            UIApplication.shared.registerForRemoteNotifications()
        },
        hubs: {
            // The keychain read stays off the main thread (docs/apple/performance.md P1).
            guard case .registry(let registry) = await Task.detached(operation: { HubStore.read() }).value else { return nil }
            return registry.groups.compactMap { g in registry.primary(g).map { PushRegistrar.Hub(group: g, api: APIClient(config: $0.config, group: g)) } }
                .filter { !$0.api.config.isDemo }
        },
        grants: { token, env, topic in
            var g = PushRegistrar.Grants()
            #if os(iOS)
            g.voip = UrgentCallPush.shared.token // only while urgent calls are on
            #endif
            guard let relay = PushRelay.url, let topic else { return g }
            do {
                g.grant = try await PushRelay.grant(relay: relay, token: token, env: env, topic: topic)
                g.relay = .registered
            } catch {
                PushRelay.log.error("relay registration: \(error.localizedDescription, privacy: .public)")
                g.relay = PushRelay.outcome(of: error)
            }
            // The urgent calls' PushKit token has a grant of its own, for the ".voip" topic (docs/operations/push-relay.md
            // "VoIP pushes"); an older relay refuses it, and the hub then says this phone cannot be rung.
            if let voip = g.voip {
                do {
                    g.voipGrant = try await PushRelay.grant(relay: relay, token: voip, env: env, topic: topic + ".voip", account: PushRelay.voipAccount)
                } catch {
                    g.voipError = PushHealth(relay: PushRelay.outcome(of: error)).relayError
                }
            }
            return g
        })
}

extension UNAuthorizationStatus {
    /// Notifications allowed by the system's switch (provisional and an App Clip's ephemeral count); nil: not asked yet.
    nonisolated var allowsNotifications: Bool? {
        switch self {
        case .notDetermined: return nil
        case .authorized, .provisional: return true
        #if os(iOS) || os(visionOS)
        case .ephemeral: return true
        #endif
        default: return false
        }
    }
}
