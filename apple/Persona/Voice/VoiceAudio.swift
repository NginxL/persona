import Foundation
import Observation
#if os(iOS) || os(visionOS)
import AVFoundation
import UIKit
#endif

/// The rates of a call's audio: the microphone's PCM16 goes up at `inputRate`, the voice comes down at `outputRate`
/// (the `ready` frame's `input_rate` / `output_rate`).
nonisolated struct VoiceAudioFormat: Equatable, Hashable, Sendable {
    var inputRate: Int
    var outputRate: Int

    /// What the microphone records at before `ready` says (the extension's defaults); a call remembers the last one.
    static let provisional = VoiceAudioFormat(inputRate: VoiceFrame.defaultInputRate, outputRate: VoiceFrame.defaultOutputRate)
}

/// What the audio tells the call.
nonisolated enum VoiceAudioEvent: Equatable, Sendable {
    /// The voice started (true) or finished (false) playing what it was given.
    case playing(Bool)
    /// The voice's loudness right now, 0…1, for the orb.
    case outputLevel(Float)
    /// A phone call, or another app, took the audio session (docs/assistant/voice.md 6.5「来电」).
    case interrupted
    /// The interruption is over (`shouldResume`: the system says the audio may go on): a call to Persona's (#9290) and the
    /// phone's voice chat's, which paused at `interrupted` (#9887).
    case interruptionEnded(shouldResume: Bool)
    /// The system's media services restarted: every audio object is gone.
    case reset
    /// The route changed (headphones, Bluetooth) and the microphone could not be set up again on the new one.
    case failed
}

nonisolated enum VoiceAudioError: Error, Equatable {
    case unsupported
    case session
    case format
    case engine
    /// The app may not record (the call ends as `permissionDenied`).
    case permission
    /// There is no input to record from, or it would not start (#5029).
    case noInput
}

/// How a call opens the microphone, decided before anything reads `AVAudioEngine.inputNode` (docs/assistant/voice.md 6.4, #5029).
/// Reading that property initializes the audio I/O unit, and an input that cannot start makes the initialization wait
/// for an RPC that times out and aborts the app: there is no error to catch, so the call has to know beforehand.
nonisolated enum VoiceCapture: Equatable, Sendable {
    /// Voice processing (echo cancellation); plain capture if it will not run.
    case voiceProcessing
    /// Plain capture, no echo cancellation: the simulator.
    case plain
    /// Nothing to record from: the call cannot start.
    case unavailable(Problem)

    nonisolated enum Problem: Equatable, Sendable {
        /// Recording is not allowed.
        case permission
        /// The audio session has no input.
        case noInput
        /// The simulator's input did not start (the Mac does not let the simulator record).
        case inputWillNotStart
    }

    /// What starting the simulator's input came to (`VoiceInputProbe`).
    nonisolated enum Probe: Equatable, Sendable {
        case started
        case failed(Int32)
        /// No answer in time, or an earlier try has not come back yet.
        case timedOut
    }

    /// The order matters: the probe runs only on the simulator, and only once the session says there is an input that
    /// may be recorded. On a device the session's word is enough; on the simulator it is not (it reports the Mac's
    /// microphone whether or not the Mac lets the simulator use it), so a short recording has to start first. Voice
    /// processing is not asked for there: it does not run on the simulator (it turns on, then the engine will not start
    /// with it), and every I/O unit set up for nothing is one more initialization that can time out.
    static func plan(recordingAllowed: Bool, inputAvailable: Bool, simulator: Bool, probe: () -> Probe) -> VoiceCapture {
        guard recordingAllowed else { return .unavailable(.permission) }
        guard inputAvailable else { return .unavailable(.noInput) }
        guard simulator else { return .voiceProcessing }
        return probe() == .started ? .plain : .unavailable(.inputWillNotStart)
    }

    static var simulator: Bool {
        #if targetEnvironment(simulator)
        true
        #else
        false
        #endif
    }
}

/// The call's audio: the microphone into the uplink, the voice out of the speaker, `clear` to drop what has not been
/// played (docs/assistant/voice.md 6.4). `PhoneVoiceAudio` on iOS and visionOS, `NoVoiceAudio` on the Mac, a fake in tests.
@MainActor protocol VoiceAudio: AnyObject {
    /// Opens the microphone (PCM16 at `format.inputRate` into `uplink`) and the player (`format.outputRate`).
    func start(_ format: VoiceAudioFormat, uplink: VoiceUplink, events: @escaping @MainActor (VoiceAudioEvent) -> Void) throws
    /// The extension said other rates than the ones it started with.
    func reconfigure(_ format: VoiceAudioFormat) throws
    /// The voice: PCM16 at the output rate.
    func play(_ pcm: Data)
    /// A short sound of the app's own (PCM16 at the output rate) through the same player, so echo cancellation takes it
    /// out of the microphone too; it does not count as the voice speaking.
    func cue(_ pcm: Data)
    /// Drops everything queued and not yet played.
    func clear()
    func stop()
    /// After an interruption that may go on (`interruptionEnded(shouldResume: true)`): the session active again and the
    /// microphone and the player running again (the phone's voice chat, #9887). Throws when the session will not come back.
    func resume() throws
    /// The microphone records with echo cancellation: what the speaker plays is taken out of it. Without it the voice
    /// can come back as the user's caption, which the `start` frame tells the extension (docs/assistant/voice.md 3.2).
    var cancelsEcho: Bool { get }
}

extension VoiceAudio {
    /// Nothing to bring back by default (the Mac's, a meeting's on the recorder's capture, the samples).
    func resume() throws {}
}

/// A system call's audio (#9290, a call to Persona): the system owns the audio session. It activates it for the call
/// (`activate`, LiveCommunicationKit's `didActivate`) and takes it away (`deactivate`: a hold, another call) without the
/// call ending; the app never sets the category or activates it, and never moves the route by itself.
@MainActor protocol SystemCallAudio: VoiceAudio {
    /// The system activated the session: the engine starts (now, or once `start` has come).
    func activate()
    /// The system took the session away: the engine stops, the call goes on.
    func deactivate()
    /// On hold: nothing starts the engine (an activation, a route change) until it is off hold again.
    func setHeld(_ held: Bool)
    /// Each time the engine runs (voice processing on, the microphone open).
    var onRunning: (@MainActor () -> Void)? { get set }
    /// Just before a new engine starts on the session the call has (a route change rebuilt it): turning voice processing
    /// on can surface as a system mute again. `onRunning` follows once it runs.
    var onStarting: (@MainActor () -> Void)? { get set }
}

/// The microphone and the voice's loudness, about 20 times a second: an object of its own, so only the orb redraws.
@MainActor @Observable final class VoiceLevels {
    private(set) var input: Float = 0
    private(set) var output: Float = 0

    /// Changes smaller than this are not worth a redraw.
    static let step: Float = 0.01

    func set(input value: Float) {
        if abs(value - input) >= Self.step || (value == 0 && input != 0) { input = value }
    }

    func set(output value: Float) {
        if abs(value - output) >= Self.step || (value == 0 && output != 0) { output = value }
    }

    func reset() {
        set(input: 0)
        set(output: 0)
    }
}

/// Asks for the microphone (the system's prompt the first time).
@MainActor protocol MicPermission {
    func request() async -> Bool
}

/// Keeps the screen from locking while a call waits on a long task (docs/assistant/voice.md 6.5).
@MainActor protocol IdleTimerControl: AnyObject {
    var isDisabled: Bool { get set }
}

/// Time for the call's timers: the system's, or the tests' that they move on by hand.
nonisolated protocol VoiceClock: Sendable {
    var now: Date { get }
    func sleep(for duration: Duration) async throws
}

nonisolated struct SystemVoiceClock: VoiceClock {
    var now: Date { .now }
    func sleep(for duration: Duration) async throws { try await Task.sleep(for: duration) }
}

struct SystemMicPermission: MicPermission {
    func request() async -> Bool {
        #if os(iOS) || os(visionOS)
        await AVAudioApplication.requestRecordPermission()
        #else
        false
        #endif
    }
}

final class SystemIdleTimer: IdleTimerControl {
    var isDisabled: Bool {
        get {
            #if os(iOS) || os(visionOS)
            UIApplication.shared.isIdleTimerDisabled
            #else
            false
            #endif
        }
        set {
            #if os(iOS) || os(visionOS)
            UIApplication.shared.isIdleTimerDisabled = newValue
            #endif
        }
    }
}

/// Where there is no call audio (the Mac for now, docs/assistant/voice.md 6.9): starting fails.
final class NoVoiceAudio: VoiceAudio {
    func start(_ format: VoiceAudioFormat, uplink: VoiceUplink, events: @escaping @MainActor (VoiceAudioEvent) -> Void) throws {
        throw VoiceAudioError.unsupported
    }
    func reconfigure(_ format: VoiceAudioFormat) throws { throw VoiceAudioError.unsupported }
    func play(_ pcm: Data) {}
    func cue(_ pcm: Data) {}
    func clear() {}
    func stop() {}
    var cancelsEcho: Bool { true }
}
