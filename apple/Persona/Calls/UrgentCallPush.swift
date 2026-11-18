#if os(iOS)
import Foundation
import PushKit

/// The PushKit registration for urgent calls (docs/assistant/urgent-call.md): only while the owner has urgent calls on,
/// so a phone that never takes calls never holds a VoIP token. Its token goes to the hub with the device's other push
/// fields (`PushRegistrar`), and a ring goes to `UrgentCallCenter`. The registry is made at launch when calls are on:
/// a VoIP push that launches the app is delivered only to a registry made before the first frame finished.
@MainActor final class UrgentCallPush: NSObject, PKPushRegistryDelegate {
    static let shared = UrgentCallPush()

    /// Urgent calls were on when the settings were last read or changed on this phone (read once at launch).
    static let onKey = "urgentCalls.on"

    private let isiOSAppOnMac: Bool
    private let defaults: UserDefaults
    private let makeRegistry: @MainActor () -> PKPushRegistry
    private var registry: PKPushRegistry?
    /// The PushKit token (hex) while registered.
    private(set) var token: String?

    init(isiOSAppOnMac: Bool = CallPlatform.isiOSAppOnMac, defaults: UserDefaults = .standard,
         makeRegistry: @escaping @MainActor () -> PKPushRegistry = { PKPushRegistry(queue: .main) }) {
        self.isiOSAppOnMac = isiOSAppOnMac
        self.defaults = defaults
        self.makeRegistry = makeRegistry
    }

    /// At launch: registers when urgent calls were on. A Mac never creates a registry.
    func start() {
        guard !isiOSAppOnMac else { return }
        if defaults.bool(forKey: Self.onKey) { register() }
    }

    /// Urgent calls were turned on or off (Settings › Urgent calls, or read from the hub): registers or lets go, and the
    /// hub hears the token, or that there is none.
    func set(on: Bool) {
        guard !isiOSAppOnMac else { return }
        if defaults.bool(forKey: Self.onKey) != on { defaults.set(on, forKey: Self.onKey) }
        if on {
            register()
        } else if registry != nil {
            registry?.desiredPushTypes = []
            registry = nil
            token = nil
            PushRegistrar.shared.reregister()
        }
    }

    private func register() {
        guard !isiOSAppOnMac, registry == nil else { return }
        let r = makeRegistry()
        r.delegate = self
        r.desiredPushTypes = [.voIP]
        registry = r
    }

    nonisolated func pushRegistry(_ registry: PKPushRegistry, didUpdate pushCredentials: PKPushCredentials, for type: PKPushType) {
        let hex = pushCredentials.token.map { String(format: "%02x", $0) }.joined()
        Self.onMain {
            let push = UrgentCallPush.shared
            guard push.token != hex else { return }
            push.token = hex
            PushRegistrar.shared.reregister()
        }
    }

    nonisolated func pushRegistry(_ registry: PKPushRegistry, didInvalidatePushTokenFor type: PKPushType) {
        Self.onMain {
            UrgentCallPush.shared.token = nil
            PushRegistrar.shared.reregister()
        }
    }

    nonisolated func pushRegistry(_ registry: PKPushRegistry, didReceiveIncomingPushWith payload: PKPushPayload, for type: PKPushType,
                                  completion: @escaping () -> Void) {
        nonisolated(unsafe) let dictionary = payload.dictionaryPayload
        nonisolated(unsafe) let done = completion
        Self.onMain { UrgentCallCenter.shared.incoming(dictionary, done: done) }
    }

    /// Runs `body` on the main actor: at once on the main thread (the registry's queue), else as soon as the main queue
    /// can. PushKit calls on the registry's queue, but nothing here traps if it ever does not.
    nonisolated static func onMain(_ body: @escaping @MainActor @Sendable () -> Void) {
        if Thread.isMainThread {
            MainActor.assumeIsolated { body() }
        } else {
            DispatchQueue.main.async { MainActor.assumeIsolated { body() } }
        }
    }
}

/// Whether an urgent call's ring reaches this phone, as each hub said when the phone registered (`call` in
/// `POST /api/v1/devices`): Settings › Urgent calls says so.
@MainActor @Observable final class UrgentCallReach {
    static let shared = UrgentCallReach()
    var routes: [String: APIClient.PushRoute?] = [:]
}
#endif
