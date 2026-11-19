import Foundation
import Observation

// One voice call bound to one conversation (docs/assistant/voice.md 6.3): the socket to the hub's voice extension, the frames,
// the captions, the runs it handed over, and the audio. The state lives here and in `VoiceCalls`, not in AppModel or
// ChatView: ChatView is rebuilt per key and is the hot path (docs/apple/ios-state.md), it only asks `VoiceCalls.isBound`.

/// Where a call was started from. The rules that end a call when the phone's app goes away (background, leaving the
/// page, the screen lock) are the phone's only: a call started from the car goes on, and so does a system call.
nonisolated enum VoiceSurface: String, Equatable, Hashable, Sendable {
    case phone
    case carPlay
    /// A system call to Persona (#9290): LiveCommunicationKit's, on the main conversation, run by `PersonaCalls`. It goes
    /// on in the background, locked and off the page; the system owns its audio session, and an interruption holds it
    /// instead of ending it.
    case call
}

nonisolated enum VoiceLiveState: Equatable, Sendable {
    case listening
    /// The user is speaking (their caption is still being written).
    case hearing
    /// The voice is playing.
    case speaking
}

nonisolated enum VoicePhase: Equatable, Sendable {
    case connecting
    case live(VoiceLiveState)
    /// The socket to the hub went away: the n-th of three tries (1, 4 and 10 seconds apart).
    case reconnecting(attempt: Int)
    case ended(VoiceEnd)

    var isEnded: Bool { if case .ended = self { true } else { false } }
    var isLive: Bool { if case .live = self { true } else { false } }
}

/// Why a call ended. The words for each are the page's (I3); `failed` keeps the extension's own message for Details.
nonisolated enum VoiceEnd: Equatable, Sendable {
    /// Stop tapped.
    case user
    /// The page left, or was hidden in the shell.
    case left
    /// The app went to the background (a call from the phone only).
    case background
    /// Another call started: one at a time in the whole app.
    case replaced
    /// A phone call, or another app, took the audio.
    case interrupted
    /// A recording or dictation started and took the microphone.
    case microphoneTaken
    /// It could not start: a recording or dictation has the microphone.
    case microphoneBusy
    case permissionDenied
    /// The microphone could not be opened, or set up again after the route changed.
    case audioFailed
    /// The system's media services restarted.
    case audioReset
    /// Ten minutes nobody spoke, nothing handed over is open and the conversation is not working.
    case idle
    /// Three reconnects failed.
    case lost
    /// The hub refused the socket: 401 (the token), 403 (a device that may only answer), 404 (no such extension, on the
    /// first connection only: see `VoiceConversation.refusedForGood`).
    case rejected(status: Int)
    /// The extension does not speak protocol 2, or bound another conversation than asked.
    case incompatible
    /// The model chosen for the extension cannot be used (the use's status, or a model the extension no longer lists).
    case modelUnavailable(String)
    /// The conversation's group of hubs is not paired any more.
    case noHub
    /// A fatal `error` frame.
    case failed(code: VoiceErrorCode?, message: String)
    /// Persona's urgent call was answered: the voice chat ended first, so its audio could not end the call's (#9290).
    case urgentCall

    /// Ended by the user's own doing: nothing stays on the page. After any other ending 「语音已结束 · 继续」 stays until
    /// the page goes (docs/assistant/voice.md 1.2 L).
    var byUser: Bool {
        switch self {
        case .user, .left, .replaced: true
        default: false
        }
    }
}

/// One line the call heard or said. Ids carry the connection's generation: a new connection's `u1` is a new line.
nonisolated struct VoiceCaption: Equatable, Identifiable, Sendable {
    var id: String
    var generation: Int
    /// "user" or "assistant".
    var role: String
    var text: String
    var final: Bool

    var isUser: Bool { role == VoiceLine.user }
}

/// A function tool at work (protocol 2 declares read-only ones only).
nonisolated struct VoiceTool: Equatable, Identifiable, Sendable {
    var id: String
    var label: String
}

/// When a call that nothing happens in ends by itself (docs/assistant/voice.md 6.5「空闲」). The cost of waiting on a long task
/// is the extension's to save (it pauses the upstream); this is only for a call left open.
nonisolated enum VoiceIdle {
    static let limit: TimeInterval = 600

    static func expired(now: Date, lastActivity: Date, openRuns: Bool, targetWorking: Bool, playing: Bool) -> Bool {
        !playing && !openRuns && !targetWorking && now.timeIntervalSince(lastActivity) >= limit
    }
}

/// The voice a call speaks with, remembered on this device per group and extension (docs/assistant/voice.md 5.7). Written only
/// when the user picks one.
nonisolated enum VoiceTimbre {
    static func key(group: String, extensionID: String) -> String {
        "voice.timbre." + (group.isEmpty ? "-" : group) + "." + extensionID
    }

    static func chosen(group: String, extensionID: String) -> String? {
        UserDefaults.standard.string(forKey: key(group: group, extensionID: extensionID)).flatMap { $0.isEmpty ? nil : $0 }
    }

    static func choose(_ voice: String?, group: String, extensionID: String) {
        let k = key(group: group, extensionID: extensionID)
        if let voice, !voice.isEmpty { UserDefaults.standard.set(voice, forKey: k) } else { UserDefaults.standard.removeObject(forKey: k) }
    }
}

/// A meeting call (#3862 phase 4, docs/assistant/meeting-minutes.md「Phase 4: realtime voice in the meeting」): bound to the
/// recording's room, on the recorder's capture, hearing only Talk spans. It is not a phone call: the recording holds the
/// microphone (the call never claims it), the screen may lock, it never ends after idle minutes, and after the three
/// reconnects it goes on trying every `retryEvery` while the meeting records.
nonisolated struct VoiceMeeting: Equatable, Sendable {
    var recordingID: UUID

    /// How often a meeting call tries again once its three reconnects failed.
    static let retryEvery: Duration = .seconds(30)
    /// The feature an extension lists in `/info` and `ready` when it takes meeting calls.
    static let feature = "meeting"
}

/// A system call on hold (#9290): the extension that lists this feature in `ready` hears `hold` frames, and pauses the
/// realtime upstream until the hold ends. Without it the call still holds here (silence up, nothing played).
nonisolated enum VoiceHold {
    static let feature = "hold"
}

/// What a system call's voice tells its manager (`PersonaCalls`, #9290), through `VoiceDeps.callEvents`.
nonisolated enum VoiceCallEvent: Equatable, Sendable {
    /// The first `ready`: the call is connected.
    case ready
    case ended(VoiceEnd)
    /// The audio session was interrupted: the call holds, it does not end.
    case interrupted
    case interruptionEnded(shouldResume: Bool)
    /// The page muted or unmuted the voice itself (the bar's Mute): the system hears it.
    case muted(Bool)
}

/// Only Send What I Just Said (docs/assistant/voice.md 6.7, #5904): whether the extension's user-speech gate holds back what
/// the voice hands over when the user was not heard asking for it just before, remembered on this device per group and
/// extension. On unless the user turned it off; the call's `start` says which (`speech_gate`).
nonisolated enum VoiceSpeechGate {
    static func key(group: String, extensionID: String) -> String {
        "voice.speechGate." + (group.isEmpty ? "-" : group) + "." + extensionID
    }

    static func isOn(group: String, extensionID: String) -> Bool {
        UserDefaults.standard.object(forKey: key(group: group, extensionID: extensionID)) as? Bool ?? true
    }

    /// On is the default: kept as no value.
    static func set(_ on: Bool, group: String, extensionID: String) {
        let k = key(group: group, extensionID: extensionID)
        if on { UserDefaults.standard.removeObject(forKey: k) } else { UserDefaults.standard.set(false, forKey: k) }
    }
}

/// What a call needs from the rest of the app; the tests hand in fakes (`FakeVoiceSocket`, `FakeVoiceAudio`, a clock
/// they move by hand).
struct VoiceDeps {
    /// The group's client on its current route: asked again for every connection, so a reconnect follows a new route.
    var api: () -> APIClient?
    var socket: (URLRequest, APIClient) -> any VoiceSocket
    var audio: () -> any VoiceAudio
    var mic: any MicPermission
    /// The microphone's owner: recording and dictation take it from a call (`claim(_:givingWay:)`).
    var microphone: RecordingMicrophone
    var idleTimer: any IdleTimerControl
    var clock: any VoiceClock
    /// The bound conversation as the app has it now (whether it works, for the idle rule).
    var session: (SessionKey) -> Session?
    /// The voice picked on this device for the group's extension.
    var voice: (_ group: String, _ extensionID: String) -> String?
    /// Whether the user-speech gate is on for the group's extension on this device (`VoiceSpeechGate`; the tests set
    /// its preference under their own group).
    var speechGate: (_ group: String, _ extensionID: String) -> Bool = VoiceSpeechGate.isOn(group:extensionID:)
    /// A system call's manager (`PersonaCalls`): it hears the call connect, end, be interrupted and be muted on the page.
    /// nil for every other call.
    var callEvents: (@MainActor (VoiceCallEvent) -> Void)? = nil

    /// A system call to Persona (#9290): the system's audio session (`audio`, which its manager activates and
    /// deactivates as the system does), and its manager told what happens (`events`).
    static func systemCall(group: String, audio: any SystemCallAudio, events: @escaping @MainActor (VoiceCallEvent) -> Void) -> VoiceDeps {
        var deps = live(group: group)
        deps.audio = { audio }
        deps.callEvents = events
        return deps
    }

    static func live(group: String) -> VoiceDeps {
        VoiceDeps(api: { AppModel.shared.api(group) },
                  socket: { request, api in HubVoiceSocket(request: request, session: api.session) },
                  audio: {
                      #if os(iOS) || os(visionOS)
                      PhoneVoiceAudio()
                      #else
                      NoVoiceAudio()
                      #endif
                  },
                  mic: SystemMicPermission(), microphone: .shared, idleTimer: SystemIdleTimer(), clock: SystemVoiceClock(),
                  session: { AppModel.shared.latest($0) },
                  voice: { VoiceTimbre.chosen(group: $0, extensionID: $1) },
                  speechGate: { VoiceSpeechGate.isOn(group: $0, extensionID: $1) })
    }
}

@MainActor @Observable final class VoiceConversation {
    let key: SessionKey
    let surface: VoiceSurface
    let extensionID: String
    /// A meeting call's recording; nil for every other call.
    let meeting: VoiceMeeting?

    private(set) var phase: VoicePhase = .connecting {
        didSet { if isOver != phase.isEnded { isOver = phase.isEnded } }
    }
    /// Whether it ended: changes only as it ends or is picked up again, so the page can swap the bar and the composer
    /// without being drawn again on every phase (listening, speaking…).
    private(set) var isOver = false
    /// The upstream model's connection (paused while the agent works).
    private(set) var link: VoiceLink = .live
    /// At most 40, oldest first.
    private(set) var captions: [VoiceCaption] = []
    /// The instructions handed over, in the order they came; a reconnect hands the open ones back.
    private(set) var runs: [VoiceRun] = []
    /// The card being asked about by voice (`ask` frame).
    private(set) var askingID: String?
    /// The bound conversation as `ready` described it.
    private(set) var target: VoiceTargetInfo?
    private(set) var features: Set<String> = []
    /// The latest error that did not end the call (machine_offline, model_unavailable…).
    private(set) var notice: VoiceFailure?
    private(set) var tools: [VoiceTool] = []
    private(set) var model: String?
    private(set) var voice: String?
    /// The user-speech gate as this device has it for the call (Only Send What I Just Said), said in `start`.
    private(set) var speechGate = true
    /// Instructions that did not go whose line the user put away (Never Mind): the bar no longer offers anything for them.
    private(set) var dismissed: Set<String> = []
    /// The microphone sends silence while muted; the voice still speaks. A system call's mute is its manager's
    /// (`setCallMuted`); set here (the bar's Mute), the manager hears it so the system's call UI says the same.
    var muted = false {
        didSet {
            uplink.isMuted = muted || held
            if surface == .call, oldValue != muted, !applyingCallMute { deps.callEvents?(.muted(muted)) }
        }
    }
    /// On hold: a system call the system paused or whose audio was interrupted (#9290, `PersonaCalls` sets it), or the
    /// phone's voice chat paused by an interruption until it may go on (#9887). The microphone sends silence, the voice is
    /// dropped and nothing plays, the quiet minutes stop counting, and an extension that takes `hold` hears it.
    private(set) var held = false
    /// When the call first connected (its first `ready`): what its duration counts from.
    private(set) var connectedAt: Date?
    /// About 20 times a second: read by the orb only.
    let levels = VoiceLevels()
    /// A meeting call's Talk span is open (`talk(_:)`).
    private(set) var talking = false
    /// The voice is playing: changes only as it starts or stops, so a page can offer Stop Speaking without being drawn
    /// again on every phase (#3862 phase 4).
    private(set) var speaking = false
    /// The owner said something and the voice has not begun to answer it yet (the call screen's Thinking, #9887): from the
    /// owner's caption turning final until the voice plays or says anything, the owner speaks again, the connection
    /// changes, or `thinkingAtMost` passed (an answer that never comes). Changes only as it starts or stops.
    private(set) var thinking = false
    /// The extension said `ready` on the connection that is up: changes only as it connects, drops or ends, so the
    /// meeting page can send Talk to the call without being drawn again on every phase (#3862 phase 4).
    private(set) var connected = false
    /// A meeting call's latest answer: the voice's newest final caption. Changes only when one is final, so the Live
    /// Activity can show it (#3862 phase 4) without following every word of the captions; nil in every other call.
    private(set) var lastAnswer: VoiceCaption?

    /// 1, 4 and 10 seconds before each of the three reconnects (docs/assistant/voice.md 1.2 M).
    static let retryDelays: [Duration] = [.seconds(1), .seconds(4), .seconds(10)]
    /// A connection that has not said `ready` by then is given up like a dropped one.
    static let readyTimeout: Duration = .seconds(15)
    static let captionLimit = VoiceLine.historyLines
    static let runLimit = 20
    /// The user's caption stops counting as 「在说」 this long after it last changed.
    static let hearingQuiet: Duration = .seconds(2)
    /// The longest the voice is shown thinking before an answer (`thinking`): past it, it listens again.
    static let thinkingAtMost: Duration = .seconds(8)
    static let idleCheck: Duration = .seconds(30)

    /// The rates the extension said last, per group and extension: the next call records at them before `ready`.
    private static var formats: [String: VoiceAudioFormat] = [:]

    @ObservationIgnored let deps: VoiceDeps
    @ObservationIgnored private var uplink = VoiceUplink()
    @ObservationIgnored private var audio: (any VoiceAudio)?
    @ObservationIgnored private var socket: (any VoiceSocket)?
    /// The connection's: bumped by every socket, so captions and late events of an older one stay apart.
    @ObservationIgnored private(set) var generation = 0
    @ObservationIgnored private(set) var attempt = 0
    @ObservationIgnored private var ready = false
    /// Some connection of this start said `ready`: the extension is there, so a 404 after that is passing.
    @ObservationIgnored private var everReady = false
    /// The connection that last reported each run: only that one knows it (see `canResend`).
    @ObservationIgnored private var runSeen: [String: Int] = [:]
    @ObservationIgnored private var playing = false
    @ObservationIgnored private var hearing = false
    @ObservationIgnored private var hearingTick = 0
    /// The owner's line the voice was last shown thinking about (`thinking`): once for each.
    @ObservationIgnored private var thoughtAbout: String?
    @ObservationIgnored private var resolved = false
    @ObservationIgnored private var format: VoiceAudioFormat
    @ObservationIgnored private let recent: [VoiceLine]
    @ObservationIgnored private(set) var lastActivity: Date
    @ObservationIgnored private var idleTimerWas: Bool?
    @ObservationIgnored private let microphoneOwner = UUID()
    /// Bumped by every start and end: an await that comes back after it does nothing.
    @ObservationIgnored private var epoch = 0
    @ObservationIgnored private var timers: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var applyingCallMute = false
    /// The phone's voice chat is paused by an interruption (a phone call, Siri, another app's audio), until its end says
    /// it may go on (#9887).
    @ObservationIgnored private(set) var pausedByInterruption = false
    /// Rates a `ready` asked for while paused by an interruption (the first one came after it began, or a reconnect's):
    /// the audio takes them as it comes back (`interruptionEnded`), not under the other audio (#9887).
    @ObservationIgnored private var formatOnResume: VoiceAudioFormat?
    /// The voice's levels reach `levels` (the orb's): not while the app is in the background, where nobody sees them.
    @ObservationIgnored private var levelsShown = true
    /// The app's word on the bound conversation (`deps.session`) is not kept up to date: in the background the app's
    /// socket to the hub is closed (`AppModel.suspendLinks`), so the state it last said (working, perhaps) may long be
    /// over while this call goes on there (#9887). The idle rule takes the voice connection's own word then (`targetBusy`).
    @ObservationIgnored private(set) var sessionStale = false
    /// Told once it ended, after everything went (`VoiceCalls`: the background tunnels of its group may close).
    @ObservationIgnored var onEnded: (@MainActor (VoiceEnd) -> Void)?

    init(key: SessionKey, extensionID: String, surface: VoiceSurface, recent: [VoiceLine] = [], meeting: VoiceMeeting? = nil,
         deps: VoiceDeps) {
        self.key = key
        self.extensionID = extensionID
        self.surface = surface
        self.recent = recent
        self.meeting = meeting
        self.deps = deps
        format = Self.formats[Self.formatKey(key.group, extensionID)] ?? .provisional
        lastActivity = deps.clock.now
    }

    private static func formatKey(_ group: String, _ id: String) -> String { group + "|" + id }

    // MARK: Start and end

    /// Starts the call: the microphone permission, the microphone (recording at once, held until `ready`), the model,
    /// then the socket. Returns once the `start` frame went out, or the call ended.
    func begin() async {
        epoch += 1
        let epoch = epoch
        phase = .connecting
        link = .live
        notice = nil
        attempt = 0
        ready = false
        everReady = false
        // A meeting call is on the recorder's capture: the recording has the permission and holds the microphone.
        if meeting == nil {
            guard await deps.mic.request() else { return end(.permissionDenied, epoch: epoch) }
            guard isCurrent(epoch) else { return }
            // A system call keeps the microphone (#9290 follow-up: nothing in the app ends it by itself): whatever else
            // asks for it while the call is on is refused, rather than ending the call (dictation and recordings ask
            // `LiveCallGate` first and say why). An in-app voice chat gives way, as before.
            let claimed = surface == .call ? deps.microphone.claim(microphoneOwner)
                : deps.microphone.claim(microphoneOwner, givingWay: { [weak self] in self?.end(.microphoneTaken) })
            guard claimed else { return end(.microphoneBusy) }
        }
        uplink = VoiceUplink(gated: meeting != nil)
        uplink.isMuted = muted || held
        uplink.levelsPaused = !levelsShown
        uplink.onLevel { @Sendable [weak self] level in
            Task { @MainActor in
                guard let self, !self.phase.isEnded else { return }
                self.levels.set(input: level)
            }
        }
        let audio = deps.audio()
        do {
            try audio.start(format, uplink: uplink) { [weak self] event in self?.audioEvent(event, epoch: epoch) }
        } catch VoiceAudioError.permission {
            return end(.permissionDenied)
        } catch {
            // No microphone to record from (#5029) or one that would not open: the page says so, with Retry.
            return end(.audioFailed)
        }
        self.audio = audio
        // A meeting call keeps no screen on: the meeting goes on with the phone locked.
        if surface == .phone, meeting == nil, idleTimerWas == nil {
            idleTimerWas = deps.idleTimer.isDisabled
            deps.idleTimer.isDisabled = true
        }
        lastActivity = deps.clock.now
        if !resolved {
            guard let api = deps.api() else { return end(.noHub) }
            let pick = await Self.resolveModel(api, extensionID)
            guard isCurrent(epoch) else { return }
            switch pick {
            case .unavailable(let why): return end(.modelUnavailable(why))
            case .ready(let chosen): model = chosen
            }
            voice = deps.voice(key.group, extensionID)
            resolved = true
        }
        speechGate = deps.speechGate(key.group, extensionID)
        // A meeting call never ends after idle minutes: it runs for the whole meeting (decision 7).
        if meeting == nil { watchIdle(epoch) }
        connect()
    }

    /// Continue: picks an ended call up again, with its captions and its open runs (`history`, `resume`).
    func restart() async {
        guard phase.isEnded else { return }
        await begin()
    }

    /// Ends the call: the socket, the microphone and the player go, the screen may lock again.
    func end(_ reason: VoiceEnd) {
        guard !phase.isEnded else { return }
        epoch += 1
        for task in timers.values { task.cancel() }
        timers = [:]
        socket?.close()
        socket = nil
        uplink.close()
        audio?.stop()
        audio = nil
        deps.microphone.release(microphoneOwner)
        if let was = idleTimerWas {
            deps.idleTimer.isDisabled = was
            idleTimerWas = nil
        }
        ready = false
        if connected { connected = false }
        playing = false
        hearing = false
        if talking { talking = false }
        tools = []
        askingID = nil
        link = .live
        levels.reset()
        if speaking { speaking = false }
        if thinking { thinking = false }
        if held { held = false }
        pausedByInterruption = false
        formatOnResume = nil
        phase = .ended(reason)
        deps.callEvents?(.ended(reason))
        onEnded?(reason)
    }

    /// The app went to the background (`on`) or came back (`VoiceCalls`): the levels stop there, and the app's word on
    /// the conversation is not kept up to date there (`sessionStale`).
    func appInBackground(_ on: Bool) {
        showsLevels(!on)
        sessionStale = on
    }

    /// Whether the voice's levels reach the orb: off in the background (nothing sees them there, and each is a hop to
    /// the main actor 20 times a second), on again in front.
    func showsLevels(_ on: Bool) {
        guard levelsShown != on else { return }
        levelsShown = on
        uplink.levelsPaused = !on
        if !on { levels.reset() }
    }

    private func end(_ reason: VoiceEnd, epoch: Int) {
        guard epoch == self.epoch else { return }
        end(reason)
    }

    private func isCurrent(_ epoch: Int) -> Bool { epoch == self.epoch && !phase.isEnded }

    // MARK: What the page does

    /// Type a line: the same as saying it.
    func say(_ text: String) {
        let text = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty, ready, let socket else { return }
        socket.send(text: VoiceMessage.text(text))
        lastActivity = deps.clock.now
    }

    /// A meeting call's Talk span opens or closes (#3862 phase 4): the `talk` frame, and between `on` and `off` the
    /// microphone's audio goes up (`VoiceUplink.talk`). A span opens only while the call is ready; false when it didn't
    /// (the caller asks without the call then). Closing always succeeds; with the socket away the span is gone already.
    @discardableResult func talk(_ on: Bool) -> Bool {
        guard meeting != nil else { return false }
        if on {
            guard ready, let socket, !talking else { return false }
            uplink.talk(true) { socket.send(text: VoiceMessage.talk(true)) }
            talking = true
            lastActivity = deps.clock.now
            return true
        }
        guard talking else { return true }
        talking = false
        let socket = ready ? socket : nil
        uplink.talk(false) { socket?.send(text: VoiceMessage.talk(false)) }
        lastActivity = deps.clock.now
        return true
    }

    /// A system call's mute, from its manager (the system's Mute, AirPods, the call page through the system).
    func setCallMuted(_ on: Bool) {
        applyingCallMute = true
        muted = on
        applyingCallMute = false
    }

    /// On hold or off it, from a system call's manager: silence goes up, what the voice was saying is dropped and nothing
    /// plays until it is off; the quiet minutes start again from then. An extension that takes `hold` hears both.
    func setHeld(_ on: Bool) {
        guard held != on else { return }
        held = on
        uplink.isMuted = muted || held
        if on {
            // What the microphone caught while the connection waited for `ready` stays here: the extension never hears
            // speech from a held call, and one without `hold` gets its silence.
            uplink.discardHeld()
            dropSpeech()
        } else {
            lastActivity = deps.clock.now
        }
        refreshPhase()
        sendHold()
    }

    private func sendHold() {
        guard ready, features.contains(VoiceHold.feature), let socket else { return }
        socket.send(text: VoiceMessage.hold(held))
    }

    /// Interrupt Voice: stops what the voice says (never the agent). What was queued is dropped here at once.
    func interruptSpeech() {
        guard ready, let socket else { return }
        socket.send(text: VoiceMessage.interrupt)
        audio?.clear()
        lastActivity = deps.clock.now
    }

    /// Whether Resend / Clear and Send / Send Anyway can go for a run: refused, and reported by the connection that is up
    /// now. A reconnect rebuilds only the open runs (`resume`), so after one the extension no longer knows a refused run
    /// and the bar offers no button for it (docs/assistant/voice.md 3.2). One the user-speech gate held back (`unprompted`)
    /// only with an extension that keeps it (`speech_gate`, #5904), and only with a task: the extension keeps none
    /// without one, and an older extension none at all, so a resend would do nothing.
    func canResend(_ runID: String) -> Bool {
        guard ready, socket != nil, let run = runs.first(where: { $0.id == runID }) else { return false }
        if run.code == "unprompted", !run.resendable || !features.contains(VoiceInfo.speechGateFeature)
            || run.task.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return false
        }
        return run.state == .refused && runSeen[runID] == generation
    }

    /// Resend / Clear and Send / Send Anyway a refused run: the same id; `clearingDraft` sends the hub's `clearable` text
    /// for the node to clear first. Clear and Send is offered only when the run has one (`run.clearable`).
    @discardableResult func resend(_ runID: String, clearingDraft: Bool = false) -> Bool {
        guard canResend(runID), let socket, let run = runs.first(where: { $0.id == runID }) else { return false }
        if clearingDraft, run.clearable == nil { return false }
        socket.send(text: VoiceMessage.resend(id: runID, clearDraft: clearingDraft ? run.clearable : nil))
        lastActivity = deps.clock.now
        return true
    }

    /// Another voice: a new connection that picks the call up where it was.
    func change(voice: String?) {
        guard voice != self.voice else { return }
        self.voice = voice
        reopen()
    }

    /// Only Send What I Just Said turned on or off on the settings page while the call ran: a new connection that
    /// picks the call up with it (the gate is the extension's, set by `start`).
    func change(speechGate: Bool) {
        guard speechGate != self.speechGate else { return }
        guard meeting == nil else { return } // its start says nothing of the gate
        self.speechGate = speechGate
        reopen()
    }

    /// Another model (the use `plugin.<id>` changed while the call ran).
    func change(model: String?) {
        guard model != self.model else { return }
        self.model = model
        reopen()
    }

    /// A short sound through the voice's player (see `VoiceAudio.cue`).
    func cue(_ pcm: Data) { audio?.cue(pcm) }

    /// One of the call's cues (`Theme.VoiceCue`), made at the rate the voice plays at.
    func play(_ cue: Theme.VoiceCue) {
        guard audio != nil else { return }
        self.cue(VoiceCueTone.pcm(cue, rate: format.outputRate))
    }

    /// Never Mind on what did not go: its line goes away.
    func dismiss(run id: String) {
        if !dismissed.contains(id) { dismissed.insert(id) }
    }

    // MARK: Socket

    /// What `start` says now: the target, the conversation's last turns, whether the microphone cancels echo, the
    /// user-speech gate on or off (always said: it also tells the extension this app has Send Anyway, #5904), and when
    /// it picks up again the captions so far and the runs not over yet, each as last reported. A meeting call says its
    /// mode and recording, and nothing of the gate: the meeting page has no Send Anyway, and the extension keeps the
    /// gate on for what goes to the room as the owner's words.
    var startFrame: VoiceStart {
        VoiceStart(model: model, voice: voice, target: VoiceTarget(key), recent: recent, history: history,
                   resume: runs.filter { $0.state.isOpen }, echoCancellation: audio?.cancelsEcho,
                   speechGate: meeting == nil ? speechGate : nil,
                   mode: meeting == nil ? nil : VoiceStart.meetingMode, recordingID: meeting?.recordingID.uuidString.lowercased(),
                   surface: surface == .call ? VoiceStart.callSurface : nil)
    }

    var history: [VoiceLine] {
        captions.filter { !$0.text.isEmpty }.suffix(VoiceLine.historyLines).map { VoiceLine(role: $0.role, text: $0.text) }
    }

    private func connect() {
        guard let api = deps.api() else { return end(.noHub) }
        generation += 1
        let generation = generation
        ready = false
        // A new connection is a new conversation with the extension, which reports again what still holds: the last
        // one's link state, the card it was asking about and its notices are not this one's.
        if link != .live { link = .live }
        if askingID != nil { askingID = nil }
        if notice != nil { notice = nil }
        think(false)
        let socket = deps.socket(VoiceEndpoint.request(api, extensionID: extensionID), api)
        self.socket = socket
        socket.open { [weak self] event in self?.received(event, generation: generation) }
        socket.send(text: VoiceMessage.start(startFrame))
        after(Self.readyTimeout, "ready") { me in
            guard me.generation == generation, !me.ready, !me.phase.isEnded else { return }
            me.socket?.close()
            me.socket = nil
            me.dropped(VoiceSocketClose(reason: "no ready"), generation: generation)
        }
    }

    /// A new connection now, not counted as a reconnect (another voice or model).
    private func reopen() {
        guard !phase.isEnded, audio != nil else { return }
        timers["ready"]?.cancel()
        timers["retry"]?.cancel()
        socket?.close()
        socket = nil
        ready = false
        if connected { connected = false }
        uplink.hold()
        if talking { talking = false }
        dropSpeech()
        attempt = 0
        phase = .connecting
        connect()
    }

    private func received(_ event: VoiceSocketEvent, generation: Int) {
        guard generation == self.generation, !phase.isEnded else { return }
        switch event {
        case .text(let text):
            if let frame = VoiceFrame.parse(text) { handle(frame) }
        case .data(let pcm):
            // On hold nothing plays: what the voice says meanwhile is dropped, not saved for later.
            if ready, !held { audio?.play(pcm) }
        case .closed(let close):
            socket = nil
            dropped(close, generation: generation)
        }
    }

    /// The socket went away: three tries, 1, 4 and 10 seconds apart, each with `history` and `resume`; then Continue. A
    /// handshake the hub refused for good is not tried again.
    private func dropped(_ close: VoiceSocketClose, generation: Int) {
        guard generation == self.generation, !phase.isEnded else { return }
        timers["ready"]?.cancel()
        ready = false
        if connected { connected = false }
        tools = []
        uplink.hold()
        // The span went with the socket; the extension ends it with the connection.
        if talking { talking = false }
        dropSpeech()
        if let status = close.status, refusedForGood(status) { return end(.rejected(status: status)) }
        attempt += 1
        // A meeting call goes on trying while the meeting records (every `retryEvery` after the three tries).
        guard attempt <= Self.retryDelays.count || meeting != nil else { return end(.lost) }
        phase = .reconnecting(attempt: attempt)
        let delay = attempt <= Self.retryDelays.count ? Self.retryDelays[attempt - 1] : VoiceMeeting.retryEvery
        after(delay, "retry") { me in
            guard me.generation == generation, !me.phase.isEnded else { return }
            me.connect()
        }
    }

    /// Whether a refused handshake ends the call: 401 (the token) and 403 (an answer-only device) always. A 404 only
    /// while no connection of this start was ready: after that the extension exists, and the hub answers 404 while it
    /// applies it again (a setting changed, switched off and on, an upgrade), so a reconnect's 404 is one more try.
    private func refusedForGood(_ status: Int) -> Bool {
        switch status {
        case 401, 403: true
        case 404: !everReady
        default: false
        }
    }

    /// What the voice of a connection that went away still had to say is stale: drop it, so the next connection's
    /// first audio reports `.playing(true)` again.
    private func dropSpeech() {
        audio?.clear()
        playing = false
        hearing = false
        if speaking { speaking = false }
    }

    private func handle(_ frame: VoiceFrame) {
        switch frame {
        case .ready(let r):
            becomeReady(r)
        case .clear:
            audio?.clear()
        case .transcript(let t):
            caption(t)
        case .tool(let id, let label, let done):
            if done {
                tools.removeAll { $0.id == id }
            } else if let i = tools.firstIndex(where: { $0.id == id }) {
                if tools[i].label != label { tools[i].label = label }
            } else {
                tools.append(VoiceTool(id: id, label: label))
            }
        case .run(let run):
            update(run)
        case .ask(let ask):
            switch ask.state {
            case .asking: if askingID != ask.interactionID { askingID = ask.interactionID }
            case .answered, .released: if askingID == ask.interactionID { askingID = nil }
            case .other: break
            }
        case .link(let state):
            if link != state { link = state }
        case .error(let failure):
            if failure.fatal {
                end(.failed(code: failure.code, message: failure.message))
            } else {
                notice = failure
            }
        case .other:
            break
        }
    }

    /// The extension is connected upstream: the microphone's held audio goes out, at the rate it asked for.
    private func becomeReady(_ r: VoiceReady) {
        guard !ready, let socket else { return }
        // iOS has no protocol 1 client: an older extension would hand everything to the main conversation instead of
        // the one the call was opened from.
        guard r.protocolVersion >= 2, r.target.map({ $0.matches(key) }) ?? true else { return end(.incompatible) }
        // A meeting call on an extension that doesn't take them would be an ordinary call bound to the room, with its
        // tools in front of the room (#3862 phase 4).
        if meeting != nil, !r.features.contains(VoiceMeeting.feature) { return end(.incompatible) }
        timers["ready"]?.cancel()
        let wanted = VoiceAudioFormat(inputRate: r.inputRate, outputRate: r.outputRate)
        if wanted != format {
            if pausedByInterruption {
                // Paused by an interruption (#9887): set up for these rates as the audio comes back. Now it would start
                // the engine under the other audio, and a refusal would end the voice chat instead of pausing it.
                formatOnResume = wanted
            } else {
                do { try audio?.reconfigure(wanted) } catch { return end(.audioFailed) }
            }
            format = wanted
        }
        Self.formats[Self.formatKey(key.group, extensionID)] = wanted
        ready = true
        if !connected { connected = true }
        everReady = true
        attempt = 0
        features = r.features
        if let t = r.target { target = t }
        // Still on hold after a reconnect: the new connection's extension hears it again, before any audio goes up.
        if held { sendHold() }
        uplink.open(rate: r.inputRate) { @Sendable pcm in socket.send(data: pcm) }
        lastActivity = deps.clock.now
        refreshPhase()
        if connectedAt == nil {
            connectedAt = deps.clock.now
            deps.callEvents?(.ready)
        }
    }

    private func caption(_ t: VoiceTranscript) {
        let id = "\(generation):\(t.id)"
        if let i = captions.lastIndex(where: { $0.id == id }) {
            if captions[i].text != t.text || captions[i].final != t.final {
                captions[i].text = t.text
                captions[i].final = t.final
            }
        } else if !t.text.isEmpty {
            let caption = VoiceCaption(id: id, generation: generation, role: t.role, text: t.text, final: t.final)
            // In the order it was said: a caption that came after the voice began to answer it goes before that
            // answer, when this connection's line is still here; else at the end (#5673).
            if let before = t.before, let i = captions.lastIndex(where: { $0.id == "\(generation):\(before)" }) {
                captions.insert(caption, at: i)
            } else {
                captions.append(caption)
            }
            if captions.count > Self.captionLimit { captions.removeFirst(captions.count - Self.captionLimit) }
        }
        if meeting != nil, t.final, t.role == VoiceLine.assistant, let said = captions.last(where: { $0.id == id }),
           !said.text.isEmpty, lastAnswer != said {
            lastAnswer = said
        }
        lastActivity = deps.clock.now
        if t.role == VoiceLine.user {
            // Thinking once the owner's words are final (once for each line), unless the voice already began to answer
            // them (a late caption goes before that answer: `before`); not while they still come.
            if !t.final {
                think(false)
            } else if t.before == nil, !playing, !t.text.isEmpty, thoughtAbout != id {
                thoughtAbout = id
                think(true)
            }
            hearing = !t.final
            hearingTick += 1
            let tick = hearingTick
            if hearing {
                after(Self.hearingQuiet, "hearing") { me in
                    guard me.hearingTick == tick else { return }
                    me.hearing = false
                    me.refreshPhase()
                }
            }
        } else if !t.text.isEmpty {
            think(false)
        }
        refreshPhase()
    }

    /// Thinking starts or stops (`thinking`); started, it stops by itself after `thinkingAtMost`.
    private func think(_ on: Bool) {
        if thinking != on { thinking = on }
        if on {
            after(Self.thinkingAtMost, "thinking") { me in me.think(false) }
        } else {
            timers["thinking"]?.cancel()
            timers["thinking"] = nil
        }
    }

    private func update(_ run: VoiceRun) {
        runSeen[run.id] = generation
        if let i = runs.firstIndex(where: { $0.id == run.id }) {
            if runs[i] != run { runs[i] = run }
            return
        }
        runs.append(run)
        while runs.count > Self.runLimit {
            let gone = runs.remove(at: runs.firstIndex { !$0.state.isOpen } ?? 0)
            runSeen[gone.id] = nil
        }
    }

    private func refreshPhase() {
        if speaking != (playing && ready && !phase.isEnded) { speaking = playing && ready && !phase.isEnded }
        guard ready, !phase.isEnded else { return }
        let next = VoicePhase.live(playing ? .speaking : hearing ? .hearing : .listening)
        if phase != next { phase = next }
    }

    // MARK: Audio and timers

    private func audioEvent(_ event: VoiceAudioEvent, epoch: Int) {
        guard isCurrent(epoch) else { return }
        switch event {
        case .playing(let on):
            playing = on
            if on {
                lastActivity = deps.clock.now
                think(false)
            }
            refreshPhase()
        case .outputLevel(let level):
            if levelsShown { levels.set(output: level) }
        case .interrupted:
            if surface == .call {
                // A system call holds instead: its manager hears it (#9290).
                deps.callEvents?(.interrupted)
            } else if surface == .phone, meeting == nil {
                // The phone's voice chat pauses (#9887): on hold until the interruption is over.
                pausedByInterruption = true
                setHeld(true)
            } else {
                end(.interrupted)
            }
        case .interruptionEnded(let resume):
            if surface == .call {
                deps.callEvents?(.interruptionEnded(shouldResume: resume))
            } else if pausedByInterruption {
                pausedByInterruption = false
                // Only when the system says the audio may go on, and the session comes back; else it ends as before, with
                // Continue.
                guard resume else { return end(.interrupted) }
                // New rates that came meanwhile first: the audio, still interrupted, keeps them and comes back with them.
                if let wanted = formatOnResume {
                    formatOnResume = nil
                    do { try audio?.reconfigure(wanted) } catch { return end(.audioFailed) }
                }
                do {
                    try audio?.resume()
                } catch {
                    return end(.interrupted)
                }
                setHeld(false)
            }
        case .reset:
            end(.audioReset)
        case .failed:
            end(.audioFailed)
        }
    }

    /// Whether the bound conversation works or waits on the user: a call is not idle then. The app's word on it while it
    /// is kept up to date. In the background (`sessionStale`) the voice connection's own, never a state from before
    /// (#9887): the extension pauses the upstream while the conversation works on something the call waits for, and
    /// only then (docs/assistant/voice.md 4.9: paused or waking), and asks about a card by voice while one waits on the
    /// user. A finished task wakes it to say so, and the quiet minutes then count as for an idle conversation; so they
    /// do with an extension that does not pause (no `suspend`), which says neither.
    private var targetBusy: Bool {
        if sessionStale { return link == .paused || link == .waking || askingID != nil }
        guard let state = deps.session(key)?.state else { return false }
        return state == .working || state == .needsYou
    }

    private func watchIdle(_ epoch: Int) {
        timers["idle"]?.cancel()
        let clock = deps.clock
        timers["idle"] = Task { [weak self] in
            while true {
                do { try await clock.sleep(for: Self.idleCheck) } catch { return }
                guard let self, self.isCurrent(epoch) else { return }
                // On hold the quiet minutes do not count (they start again when it goes on).
                if self.held { continue }
                if VoiceIdle.expired(now: clock.now, lastActivity: self.lastActivity, openRuns: self.runs.contains { $0.state.isOpen },
                                     targetWorking: self.targetBusy, playing: self.playing) {
                    self.end(.idle)
                    return
                }
            }
        }
    }

    /// Runs `action` after `delay` on the call's clock, replacing the timer of the same name.
    private func after(_ delay: Duration, _ name: String, _ action: @escaping @MainActor (VoiceConversation) -> Void) {
        timers[name]?.cancel()
        let clock = deps.clock
        timers[name] = Task { [weak self] in
            do { try await clock.sleep(for: delay) } catch { return }
            guard !Task.isCancelled, let self else { return }
            action(self)
        }
    }

    // MARK: Model

    /// The model the call asks for: the use `plugin.<id>`'s, as Android's resolveVoiceModel decides. A request that
    /// fails here does not stop the call (the extension's default applies, and the socket will say what is wrong).
    static func resolveModel(_ api: APIClient, _ extensionID: String) async -> VoiceModelPick {
        async let choice = try? api.voiceModelChoice(extensionID)
        async let models = try? api.voiceModels(extensionID)
        let chosen: VoiceModelChoice?? = await choice
        let listed: VoiceModels? = await models
        return VoiceModelPick.resolve(choice: chosen ?? nil, models: listed)
    }
}
