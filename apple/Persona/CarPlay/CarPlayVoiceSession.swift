#if os(iOS)
import AVFoundation
import Foundation

/// Why CarPlay will not open the microphone prompt on the phone (in a pocket while driving).
nonisolated enum CarPlayMicGate {
    static let undecided = String(localized: "First dictate or record once in Persona on the phone and allow the microphone")
    static let denied = String(localized: "No microphone access. Turn it on in Settings › Persona")
    static let noHub = String(localized: "Not paired with a hub yet. Open Persona on the phone first")
    static let noMain = String(localized: "Open Persona on the phone first and wait for the main chat to be set up")
    static let noExtension = String(localized: "No voice extension is running on the hub yet. Set up the duplex voice entry on the hub first")

    /// Why a call cannot start, or nil when it can. Undetermined ends it too: nobody can answer the phone's prompt from the car.
    static func blockReason() -> String? {
        switch AVAudioApplication.shared.recordPermission {
        case .granted: nil
        case .denied: denied
        case .undetermined: undecided
        @unknown default: undecided
        }
    }
}

/// Mic permission that never prompts: CarPlay must not hang on the phone's dialog (docs/apple/carplay.md).
struct CarPlayMicPermission: MicPermission {
    func request() async -> Bool {
        AVAudioApplication.shared.recordPermission == .granted
    }
}

/// CarPlay's thin shell over `VoiceCalls` / `VoiceConversation(surface: .carPlay)` (docs/assistant/voice.md 6.4, #3789).
@MainActor enum CarPlayVoiceSession {
    /// Starts (or restarts) the car's call on the assistant's main conversation. Returns a Chinese sentence when it
    /// cannot start; nil once `VoiceCalls` owns the call (it may still end itself with `VoiceEnd`).
    static func start() async -> String? {
        // A call to Persona on the phone goes on (#9290 follow-up: nothing ends it by itself); the car says it is there,
        // and plays it when the car is its audio.
        if let onPhone = onPhoneCall() { return onPhone }
        if let reason = CarPlayMicGate.blockReason() { return reason }
        do {
            let (key, extensionID) = try await resolve()
            // Not under Persona's urgent call (#9290): its audio is the system's.
            if let refused = await VoiceCalls.shared.start(key, extensionID: extensionID, surface: .carPlay, deps: deps(group: key.group)) {
                return refused.title
            }
            if let call = VoiceCalls.shared.current, call.surface == .carPlay, case .ended(let end) = call.phase {
                return CarPlayVoiceScreen.endedHeadline(for: end)
            }
            return nil
        } catch let error as CarPlayVoiceStartError {
            return error.sentence
        } catch {
            return IntentFailure.sentence(for: error, host: IntentSupport.host())
        }
    }

    /// Continue on an ended car call, or a fresh start when there is none.
    static func resume() async -> String? {
        if let onPhone = onPhoneCall() { return onPhone }
        if let call = VoiceCalls.shared.current, call.surface == .carPlay, call.phase.isEnded {
            if let refused = await VoiceCalls.shared.resume(call.key) { return refused.title }
            if let call = VoiceCalls.shared.current, call.surface == .carPlay, case .ended(let end) = call.phase {
                return CarPlayVoiceScreen.endedHeadline(for: end)
            }
            return nil
        }
        return await start()
    }

    /// What the car says while a call to Persona is on the phone, or nil when there is none: the car's voice chat would
    /// end that call, so it does not start.
    static func onPhoneCall() -> String? {
        PersonaCalls.shared.liveOwnerCallName.map { String(localized: "You're on a call with \($0) on the phone") }
    }

    static func endFromUser() {
        VoiceCalls.shared.end(.user, from: .carPlay)
    }

    /// Leaving the CarPlay voice screen (Apple: voice only while the template is on screen).
    static func endFromBackground() {
        VoiceCalls.shared.end(.left, from: .carPlay)
    }

    static func interrupt() {
        guard let call = VoiceCalls.shared.current, call.surface == .carPlay else { return }
        call.interruptSpeech()
    }

    /// A live car call (not ended). Used so auto-start does not reopen after End.
    static var isRunning: Bool {
        guard let call = VoiceCalls.shared.current, call.surface == .carPlay else { return false }
        return !call.phase.isEnded
    }

    /// The car call to mirror on the template, if any (live or ended).
    static var call: VoiceConversation? {
        guard let call = VoiceCalls.shared.current, call.surface == .carPlay else { return nil }
        return call
    }

    private static func deps(group: String) -> VoiceDeps {
        var d = VoiceDeps.live(group: group)
        d.mic = CarPlayMicPermission()
        return d
    }

    private static func resolve() async throws -> (SessionKey, String) {
        let api: APIClient
        let key: SessionKey
        if let main = AppModel.shared.assistantMain,
           let client = AppModel.shared.api(main.group) {
            api = client
            key = main
        } else {
            do {
                api = try IntentSupport.client()
            } catch {
                throw CarPlayVoiceStartError.noHub
            }
            let st = try await api.assistant()
            guard let confirmed = st.mainKey(group: api.group) else { throw CarPlayVoiceStartError.noMain }
            key = confirmed
        }
        let list = try await api.pluginList()
        guard let voice = VoiceAvailability.pick(list.plugins) else { throw CarPlayVoiceStartError.noExtension }
        return (key, voice.id)
    }
}

nonisolated enum CarPlayVoiceStartError: Error {
    case noHub, noMain, noExtension

    var sentence: String {
        switch self {
        case .noHub: CarPlayMicGate.noHub
        case .noMain: CarPlayMicGate.noMain
        case .noExtension: CarPlayMicGate.noExtension
        }
    }
}
#endif
