#if os(iOS)
import AVFoundation
import Foundation
import Observation
import os

/// One system call as the page sees it (#9290): who it is with, where it is, muted, on hold and why, when it
/// connected, how it ended. Each changes only when it changes, so the page draws only then.
@MainActor @Observable final class PersonaCall: Identifiable {
    let id: UUID
    let kind: CallKind
    let direction: CallDirection
    /// Who the call is with: the assistant's look and name, taken together when the call is made (again as its
    /// conversation is decided, and once more just before the system's Start for that same assistant), then fixed for the
    /// call (`CallAssistant`).
    let assistant: CallAssistant
    /// The assistant's name as the call carries it (`CallHandle.name`): what the main conversation's top bar says
    /// (`AppModel.assistantShownName`, #9887), and what the system's call UI, the Lock Screen and Recents show.
    var name: String {
        #if DEBUG
        if let sampleName { return sampleName }
        #endif
        return assistant.name
    }
    /// The conversation the call talks in when it is not the main one (#9887 one button): its title, whose it is and its
    /// mark, for the pages. Fixed when the call is made.
    var conversation: CallConversation?
    /// Who the pages say the call is with: the conversation's title, or the assistant's name for the main conversation.
    var title: String {
        #if DEBUG
        if let sampleName { return sampleName }
        #endif
        return conversation?.title ?? assistant.name
    }
    private(set) var phase: CallPhase
    private(set) var muted = false
    /// On hold, and why (the system's pause first).
    private(set) var holdReason: CallHoldReason?
    /// The audio did not come back after an interruption: the page offers Resume (`PersonaCalls.resume`).
    private(set) var resumeOffered = false
    /// Why it ended, in a line for the page; nil while it is on, and when it was simply hung up. Set by `PersonaCalls`.
    var note: String?
    /// A call to Persona's voice, once it began (its captions, what it is doing).
    fileprivate(set) var voice: VoiceConversation?
    /// An urgent call the system would not show (Focus, Do Not Disturb refused its ring): only its companion notification
    /// says it rings, so while it rings it refuses nothing (`PersonaCalls.urgentInProgress`).
    fileprivate(set) var unshown = false

    var held: Bool { holdReason != nil }
    var connectedSince: Date? { phase.connectedSince }
    var endReason: CallEndReason? { if case .ended(let reason) = phase { reason } else { nil } }
    /// An urgent call's session, once answered (`UrgentCallView`).
    var urgentSession: UrgentCallSession? { UrgentCallCenter.shared.session(for: id) }

    @ObservationIgnored fileprivate var machine: CallMachine
    @ObservationIgnored fileprivate(set) var engine: (any CallEngine)?
    @ObservationIgnored fileprivate var timers: [CallTimer: Task<Void, Never>] = [:]
    /// The call's audio category is set (`PersonaCalls.prepareAudio`): a call to Persona's before its Start, an urgent
    /// call's as it rang or as it was answered.
    @ObservationIgnored fileprivate(set) var audioPrepared = false
    /// Over: the call is ended and what follows its end has run.
    @ObservationIgnored fileprivate var finished = false
    /// A DEBUG sample: nothing reaches the system or an engine.
    @ObservationIgnored fileprivate(set) var sample = false
    #if DEBUG
    /// A sample has no voice: what one would show, its connection being picked up again and its latest caption.
    fileprivate(set) var sampleReconnecting = false
    fileprivate(set) var sampleCaption: String?
    /// A sample's name in the assistant's place (`-showCallSampleName`): a long one, for the screenshots.
    fileprivate(set) var sampleName: String?
    /// A sample's voice (once it connected): its live state and its link (`-showCallSample speaking`, `hearing`,
    /// `paused`…), and what its conversation does (`working`, `asking`, `thinking`), as a real voice's would read.
    fileprivate(set) var sampleVoice: VoiceLiveState?
    fileprivate(set) var sampleLink: VoiceLink = .live
    fileprivate(set) var sampleActivity: CallActivity?
    /// A sample's voice level: a made-up voice while it speaks or hears (`CallSampleVoice`), so the speaking motion and the
    /// ring show on the Simulator, which has no audio for a sample; read only on the avatar's clock.
    var sampleLevel: Double {
        guard let voice = sampleVoice, voice != .listening else { return 0 }
        return CallSampleVoice.level(at: Date.now.timeIntervalSinceReferenceDate)
    }
    #endif

    fileprivate init(id: UUID, kind: CallKind, direction: CallDirection, persona: Persona, name: String) {
        self.id = id
        self.kind = kind
        self.direction = direction
        assistant = CallAssistant(persona, name: name)
        machine = CallMachine(id: id, kind: kind, direction: direction)
        phase = machine.phase
    }

    /// The machine's state onto what the page reads, each only when it changed.
    fileprivate func sync() {
        if phase != machine.phase { phase = machine.phase }
        if muted != machine.muted { muted = machine.muted }
        if holdReason != machine.holdReason { holdReason = machine.holdReason }
        if resumeOffered != machine.resumeOffered { resumeOffered = machine.resumeOffered }
    }
}

/// Where a call to Persona was asked for.
nonisolated enum CallOrigin: String, Equatable, Sendable {
    /// The app's own Call (the main conversation, a DEBUG switch).
    case app
    /// A redial from the Phone app's Recents.
    case recents
    case siri
}

/// The app's one call manager (#9290): calls to Persona and Persona's urgent calls share one system (one
/// LiveCommunicationKit `ConversationManager`, made the first time a call needs it, never at launch: #9264) and one
/// state machine per call (`CallMachine`). The system's actions reach the call by its conversation id; the machine says
/// what to do, and this does it: fulfil or fail the action, report to the system, drive the call's engine
/// (`OwnerCallEngine`, `UrgentCallEngine`). A call goes on whatever the app does (another app, Home, another page, the
/// phone locked) until it is hung up, fails, or its voice ends by itself.
@MainActor @Observable final class PersonaCalls {
    /// The app's: it also answers the app's guards (`guardTheApp`: nothing in the app ends a live call by itself).
    static let shared: PersonaCalls = {
        let calls = PersonaCalls()
        calls.guardTheApp()
        return calls
    }()
    nonisolated static let log = Logger(subsystem: "persona", category: "call")

    /// The calls, oldest first: the live ones, and one that ended a moment ago (it stays `endedStays` for the page).
    private(set) var calls: [PersonaCall] = []
    /// The full-screen call page shows the call to Persona; false: minimised to the bar that returns to it. Hiding the
    /// page never ends the call.
    var presented = false
    /// Why a call to Persona could not be placed (the page's alert: title, message, Open Settings); the page clears it.
    var refusal: VoiceUnavailableNote?
    /// Where the call's sound goes (receiver, speaker, Bluetooth…), as the session has it.
    let route = CallRoute()

    /// How long an ended call stays for the page to say how it ended.
    static let endedStays: Duration = .seconds(2)

    @ObservationIgnored private var made: (any CallSystem)?
    @ObservationIgnored private let makeSystem: @MainActor () -> any CallSystem
    @ObservationIgnored let clock: any VoiceClock
    @ObservationIgnored let voiceCalls: VoiceCalls
    /// A call to Persona's audio and voice: the system's session, and `VoiceDeps.systemCall` (the tests' fakes).
    @ObservationIgnored let ownerAudio: @MainActor () -> any SystemCallAudio
    @ObservationIgnored let ownerDeps: @MainActor (String, any SystemCallAudio, @escaping @MainActor (VoiceCallEvent) -> Void) -> VoiceDeps
    /// Sets a call's audio category (`setCallCategory`; the tests count it).
    @ObservationIgnored let callCategory: @MainActor () async -> Void
    /// The interruption observer for urgent calls (a call to Persona hears its own through its audio).
    @ObservationIgnored private var interruptions: NSObjectProtocol?
    /// When the last redial came (`continueActivity`): the same tap can come twice.
    @ObservationIgnored var lastRedial: Date?
    /// A call to Persona on this group ended: the tunnels it kept open in the background go (`AppModel.callEnded`).
    @ObservationIgnored var callLeft: @MainActor (_ group: String) -> Void = { AppModel.shared.callEnded(group: $0) }
    /// The assistant a new call is with: the persona on screen (the tests' own). The call carries its look.
    @ObservationIgnored let assistant: @MainActor () -> Persona
    /// The name a new call carries (#9887): what the main conversation's top bar says, the current assistant's on a hub
    /// with several, else the look's (`AppModel.assistantShownName`; the tests' own).
    @ObservationIgnored let assistantName: @MainActor () -> String
    /// Whose main conversation a new call is to (#9887 review): the selected hub's group and the assistant picked there
    /// (`CallAssistant.Owner`; the tests' own). A call's name and look are taken again only for the same one.
    @ObservationIgnored let assistantOwner: @MainActor () -> CallAssistant.Owner
    /// What the system's side needs before it is made, off the main actor: its call UI's icon (`CallIcon.prepare`).
    @ObservationIgnored let prepareSystem: @MainActor () async -> Void
    /// The kind of handle a new call to Persona carries (Developing Persona's Call handle, #9887).
    @ObservationIgnored let handleKind: @MainActor () -> CallHandleKind
    /// A system call became live, or the last one ended (`guardsLive`): the app's media guard follows (`guardTheApp`).
    @ObservationIgnored var onLiveChange: (@MainActor (Bool) -> Void)?
    @ObservationIgnored private var wasLive = false

    init(system: @escaping @MainActor () -> any CallSystem = PersonaCalls.makeSystem, clock: any VoiceClock = SystemVoiceClock(),
         voiceCalls: VoiceCalls = .shared,
         ownerAudio: @escaping @MainActor () -> any SystemCallAudio = { PhoneVoiceAudio(session: .system) },
         ownerDeps: @escaping @MainActor (String, any SystemCallAudio, @escaping @MainActor (VoiceCallEvent) -> Void) -> VoiceDeps
             = { VoiceDeps.systemCall(group: $0, audio: $1, events: $2) },
         callCategory: @escaping @MainActor () async -> Void = { await PersonaCalls.setCallCategory() },
         assistant: @escaping @MainActor () -> Persona = { AppModel.shared.persona },
         assistantName: @escaping @MainActor () -> String = { AppModel.shared.assistantShownName },
         assistantOwner: @escaping @MainActor () -> CallAssistant.Owner = {
             CallAssistant.Owner(group: AppModel.shared.hub?.group ?? "", manager: AppModel.shared.assistantManagerID)
         },
         prepareSystem: @escaping @MainActor () async -> Void = { await CallIcon.prepare() },
         handleKind: @escaping @MainActor () -> CallHandleKind = { DevMode.shared.callHandleInUse }) {
        makeSystem = system
        self.handleKind = handleKind
        self.assistant = assistant
        self.assistantName = assistantName
        self.assistantOwner = assistantOwner
        self.prepareSystem = prepareSystem
        self.clock = clock
        self.voiceCalls = voiceCalls
        self.ownerAudio = ownerAudio
        self.ownerDeps = ownerDeps
        self.callCategory = callCategory
        // Every voice chat starts or goes on through `VoiceCalls` (a page, CarPlay, Continue): none under an urgent call,
        // and none before a call to Persona that is on let its audio go (`endForVoiceChat`).
        voiceCalls.urgentCallOn = { [weak self] in self?.urgentInProgress ?? false }
        voiceCalls.endSystemCall = { [weak self] in await self?.endForVoiceChat() }
        // A voice chat from another surface asks before it ends a call to Persona that is on, and CarPlay's is refused:
        // nothing ends the call by itself (`VoiceCalls.start(endingCall:)`).
        voiceCalls.systemCallName = { [weak self] in self?.liveOwnerCallName }
    }

    /// The system's side, made the first time a call needs it (after `readySystem`). On the Simulator a stand-in (it has
    /// no call service); DEBUG `-showSystemCallService YES` takes LiveCommunicationKit there too, to see what it does.
    static func makeSystem() -> any CallSystem {
        #if targetEnvironment(simulator)
        #if DEBUG
        if UserDefaults.standard.bool(forKey: "showSystemCallService") {
            log.notice("call: the Simulator on LiveCommunicationKit (-showSystemCallService)")
            return LCKCallSystem()
        }
        #endif
        return SimulatedCallSystem()
        #else
        return LCKCallSystem()
        #endif
    }

    var system: any CallSystem {
        if let made { return made }
        let s = makeSystem()
        s.handler = self
        made = s
        return s
    }

    /// Whether the system's side was made (the tests: launching makes none).
    var systemMade: Bool { made != nil }

    /// Before the system's side is first made (a call placed, an urgent call rung): what it needs is made off the main
    /// actor (the call UI's icon, whose drawing and PNG encoding would otherwise run as the manager is made, on the main
    /// actor). Nothing once it exists.
    func readySystem() async {
        guard made == nil else { return }
        await prepareSystem()
    }

    // MARK: What the page reads

    /// The call the call page shows: the call to Persona, live or just ended.
    var ownerCall: PersonaCall? { calls.last { !$0.kind.isUrgent } }
    /// The newest live call, else the one that just ended.
    var current: PersonaCall? { calls.last { $0.phase.isLive } ?? calls.last }
    /// An urgent call rings or is on: nothing else may start (voice chat, a call to Persona). One the system would not
    /// show counts only once it is answered (its notification tapped): until then nothing rings.
    var urgentInProgress: Bool {
        calls.contains { $0.kind.isUrgent && $0.phase.isLive && !($0.unshown && $0.phase == .ringing) }
    }
    /// A call to Persona is on.
    var ownerInProgress: Bool { calls.contains { !$0.kind.isUrgent && $0.phase.isLive } }
    /// The phone's voice chat on away from its page (#9887 one button: the fallback where a system call can't be used):
    /// the call's window shows the bar back to it.
    var awayVoiceChat: VoiceConversation? { voiceCalls.awayChat }

    /// The bar back to the phone's voice chat tapped: its conversation opens and closes what covers it (a full-screen
    /// picture of that same conversation: `VoiceCalls.returnTo`); the bar goes once its page shows (`VoiceCalls.returned`).
    func returnToVoiceChat(_ chat: VoiceConversation) {
        voiceCalls.returnTo(chat.key)
        AppModel.shared.open(chat.key)
    }

    func call(_ id: UUID) -> PersonaCall? { calls.first { $0.id == id } }
    /// The urgent call the hub knows as `callID` (its page's header shows who is calling).
    func urgentCall(_ callID: String) -> PersonaCall? {
        calls.last { if case .urgent(let id) = $0.kind { id == callID } else { false } }
    }

    /// Who the urgent call `callID`'s page says is calling (its header, #9887 review): the call's own name, taken as it
    /// rang and fixed for it, as its handle says it (`PersonaCall.name`: what the main conversation's top bar said then,
    /// "Persona" when that was empty); a page with no system call behind it (the DEBUG sample) says the name as it is now.
    func urgentCallerName(_ callID: String) -> String {
        urgentCall(callID)?.name ?? CallHandle.name(assistantName())
    }

    // MARK: What the page does

    /// Hang Up: the call to Persona unless another is named. Through the system, so its call UI ends too.
    func hangUp(_ id: UUID? = nil) {
        guard let call = id.flatMap(call) ?? ownerCall else { return }
        if call.sample {
            call.urgentSession?.hangUp() // an urgent call page's sample goes with its call
            return remove(call)
        }
        dispatch(call, .hangUpTapped)
    }

    /// Mute: through the system (`MuteConversationAction`), so its call UI and AirPods say the same; here if it refuses.
    func setMuted(_ muted: Bool, _ id: UUID? = nil) {
        guard let call = id.flatMap(call) ?? ownerCall else { return }
        dispatch(call, .muteTapped(muted))
    }

    /// Resume: asks the system to take the call off hold (after an interruption the audio did not come back from, or a
    /// hold the system left after the other call ended).
    func resume(_ id: UUID? = nil) {
        guard let call = id.flatMap(call) ?? ownerCall else { return }
        dispatch(call, .resumeTapped)
    }

    /// Speaker on or off (the route the session gives back: the earpiece, or what is connected).
    func setSpeaker(_ on: Bool) {
        route.setSpeaker(on)
    }

    /// The page minimised (the bar returns to it), or shown again.
    func minimise() { if presented { presented = false } }
    func present() { if ownerCall != nil, !presented { presented = true } }

    /// The app came back to the front (the Dynamic Island, the status bar's call, the Lock Screen's call, the app
    /// switcher): a call to Persona that is on shows its page again.
    func returnedToApp() {
        guard let call = ownerCall, call.phase.isLive else { return }
        present()
    }

    // MARK: Adding and ending calls

    @discardableResult func add(_ id: UUID = UUID(), kind: CallKind, direction: CallDirection, persona: Persona? = nil) -> PersonaCall {
        let call = PersonaCall(id: id, kind: kind, direction: direction, persona: persona ?? assistant(),
                               name: persona?.name ?? assistantName())
        if calls.isEmpty { route.watch() }
        calls.append(call)
        noteLive()
        return call
    }

    /// Tells `onLiveChange` when the app's guards should start or stop (`guardsLive` changed).
    private func noteLive() {
        let live = guardsLive
        guard live != wasLive else { return }
        wasLive = live
        Self.log.notice("call: the app's guards \(live ? "on" : "off", privacy: .public)")
        onLiveChange?(live)
    }

    /// `engine` runs `call` from now on. What the call already is goes with it before anything starts: muted or held while
    /// it dialled (the page's Mute works from "Calling…", before the engine exists), so its first buffer is silent.
    func attach(_ engine: any CallEngine, to call: PersonaCall) {
        call.engine = engine
        if call.machine.muted { engine.setMuted(true) }
        if call.machine.engineHeld { engine.setHeld(true) }
        engine.events = { [weak self, weak call] event in
            guard let self, let call else { return }
            self.heard(event, from: call)
        }
    }

    /// A call that never reached the system (refused before Start): it ends with `note`, the page goes, and the note
    /// is what the app shows (`refusal`). Not for an attempt that is over already (hung up while it waited for the hub or
    /// the microphone prompt): a newer call's page and alert stay as they are.
    func refuse(_ call: PersonaCall, note: VoiceUnavailableNote?) {
        guard call.phase.isLive else {
            Self.log.notice("call \(call.id.uuidString, privacy: .public): a refusal after it ended, not shown")
            return
        }
        _ = call.machine.handle(.settled(.failed), at: clock.now)
        call.note = note?.title
        call.sync()
        refusal = note
        presented = false
        finish(call)
        noteLive()
    }

    /// What follows a call's end, once: its timers stop, its engine goes, a call of ours the system held for it goes on
    /// again (the system does not resume it by itself), and after a moment it leaves the list.
    private func finish(_ call: PersonaCall) {
        guard !call.finished else { return }
        call.finished = true
        for timer in call.timers.values { timer.cancel() }
        call.timers = [:]
        call.engine?.events = nil
        Self.log.notice("call \(call.id.uuidString, privacy: .public): ended (\(String(describing: call.phase), privacy: .public))")
        if !call.sample {
            for other in calls where other !== call && other.phase.isLive && other.machine.holds.contains(.system) {
                Self.log.notice("call \(other.id.uuidString, privacy: .public): our other call ended, resuming it")
                perform(.pause(false), other)
            }
        }
        if !calls.contains(where: { $0.kind.isUrgent && $0.phase.isLive }) { stopWatchingInterruptions() }
        if !call.sample, let owner = call.engine as? OwnerCallEngine, owner.voice != nil { callLeft(owner.key.group) }
        let clock = clock
        Task { [weak self, weak call] in
            try? await clock.sleep(for: Self.endedStays)
            guard let self, let call else { return }
            self.remove(call)
        }
    }

    private func remove(_ call: PersonaCall) {
        call.engine = nil
        calls.removeAll { $0 === call }
        // The page goes with the last call to Persona.
        if ownerCall == nil, presented { presented = false }
        if calls.isEmpty { route.unwatch() }
        noteLive()
    }

    // MARK: The machine

    /// Feeds `event` to the call's machine and does what it says. `fulfil` / `fail`: the system's action being handled.
    func dispatch(_ call: PersonaCall, _ event: CallEvent, fulfil: (@MainActor () -> Void)? = nil,
                  fail: (@MainActor () -> Void)? = nil) {
        let wasLive = call.phase.isLive, waiting = call.machine.pending.count
        let effects = call.machine.handle(event, at: clock.now)
        call.sync()
        if call.sample { return }
        // Reports the system would drop now wait for it to have the conversation (`CallEvent.known`).
        for queued in call.machine.pending.dropFirst(waiting) {
            Self.log.notice("call \(call.id.uuidString, privacy: .public): report \(queued.logName, privacy: .public) queued until the system has the conversation")
        }
        for effect in effects { run(effect, for: call, fulfil: fulfil, fail: fail) }
        if wasLive, call.phase.isEnded {
            let reported = effects.contains { if case .report(.ended) = $0 { true } else { false } }
            Self.log.notice("call \(call.id.uuidString, privacy: .public): the end, by \(String(describing: event), privacy: .public) (\(reported ? "conversationEnded reported" : "the system's own end, nothing reported", privacy: .public))")
        }
        if call.phase.isEnded { finish(call) }
        noteLive()
    }

    private func run(_ effect: CallEffect, for call: PersonaCall, fulfil: (@MainActor () -> Void)?, fail: (@MainActor () -> Void)?) {
        let id = call.id
        switch effect {
        case .fulfil:
            fulfil?()
        case .fail:
            fail?()
        case .report(let report):
            system.report(report, for: id)
        case .begin:
            if call.kind.isUrgent { endVoiceChat(for: .urgentCall) }
            call.engine?.begin()
            if let owner = call.engine as? OwnerCallEngine, call.voice !== owner.voice { call.voice = owner.voice }
        case .audioOn:
            call.engine?.audioActivated()
        case .audioOff:
            call.engine?.audioDeactivated()
        case .mute(let on):
            call.engine?.setMuted(on)
        case .hold(let on):
            Self.log.notice("call \(id.uuidString, privacy: .public): \(on ? "on hold" : "off hold", privacy: .public) (\(String(describing: call.machine.holds), privacy: .public))")
            call.engine?.setHeld(on)
        case .performMute(let on):
            let system = system
            Task { [weak self, weak call] in
                do {
                    try await system.perform(.mute(on), for: id)
                } catch {
                    Self.log.notice("call \(id.uuidString, privacy: .public): the system refused mute: \(CallSystemError(error).description, privacy: .public)")
                    guard let self, let call else { return }
                    self.dispatch(call, .muteRefused(on))
                }
            }
        case .performUnpause:
            perform(.pause(false), call)
        case .performEnd:
            let system = system
            Task { [weak self, weak call] in
                do {
                    try await system.perform(.end, for: id)
                } catch {
                    Self.log.notice("call \(id.uuidString, privacy: .public): the system refused the hang-up: \(CallSystemError(error).description, privacy: .public)")
                    guard let self, let call else { return }
                    self.dispatch(call, .hangUpRefused)
                }
            }
        case .activateSession(let attempt):
            let system = system
            Task { [weak self, weak call] in
                let ok = await system.activateAudio()
                guard let self, let call else { return }
                self.dispatch(call, ok ? .sessionActivated(attempt) : .sessionActivationFailed(attempt))
            }
        case .endEngine:
            call.engine?.hangUp()
        case .schedule(let timer):
            call.timers[timer]?.cancel()
            let clock = clock
            call.timers[timer] = Task { [weak self, weak call] in
                do { try await clock.sleep(for: timer.delay) } catch { return }
                guard !Task.isCancelled, let self, let call else { return }
                call.timers[timer] = nil
                self.dispatch(call, .timer(timer))
            }
        case .log(let line):
            Self.log.notice("call \(id.uuidString, privacy: .public): \(line, privacy: .public)")
        }
    }

    private func perform(_ request: CallRequest, _ call: PersonaCall) {
        let id = call.id
        let system = system
        Task {
            do {
                try await system.perform(request, for: id)
            } catch {
                Self.log.notice("call \(id.uuidString, privacy: .public): the system refused \(String(describing: request), privacy: .public): \(CallSystemError(error).description, privacy: .public)")
            }
        }
    }

    private func heard(_ event: CallEngineEvent, from call: PersonaCall) {
        switch event {
        case .ready:
            if call.phase == .connecting { Self.log.notice("call \(call.id.uuidString, privacy: .public): connected (the voice's first ready)") }
            dispatch(call, .voiceReady)
        case .running: dispatch(call, .engineRunning)
        case .starting: dispatch(call, .engineStarting)
        case .ended(let failed, let note):
            if call.phase.isLive, let note { call.note = note }
            dispatch(call, .engineEnded(failed: failed))
        case .interrupted: dispatch(call, .interruptionBegan)
        case .interruptionEnded: dispatch(call, .interruptionEnded)
        case .muted(let on): dispatch(call, .muteTapped(on))
        }
    }

    /// Another chat's voice (a page's Voice Chat, CarPlay's Talk to Persona, Continue: `VoiceCalls.endSystemCall`), or
    /// End and Call to another conversation (`callConversation(endingCall:)`): the call to Persona ends (one call at a
    /// time) and its audio goes before the next opens its own: the system's deactivation would otherwise interrupt the
    /// new session. The hang-up's end is waited for whether or not the call ever had its audio (#9887: one still
    /// dialling, or held with its audio gone, ends only when the system's End comes back, a moment later), so the next
    /// call or voice chat never finds it still on. A call that ended already (hung up from AirPods or the system's call
    /// UI while the question was up) is not hung up again, but the system may still hold its audio until its
    /// deactivation comes: that is waited for all the same, as for any call of ours whose audio the system has not let
    /// go yet. At most `handOffWait`; a call still on after it is shown instead (`callConversation`).
    func endForVoiceChat() async {
        let owner = ownerCall.flatMap { $0.sample ? nil : $0 }
        if let owner, owner.phase.isLive { hangUp(owner.id) }
        let ours = calls.filter { !$0.sample }
        func waiting() -> Bool { owner?.phase.isLive == true || ours.contains { $0.machine.audioActive } }
        let steps = Int(Self.handOffWait / Self.handOffStep)
        for _ in 0..<steps where waiting() {
            try? await Task.sleep(for: Self.handOffStep)
        }
        if let owner, owner.phase.isLive {
            Self.log.notice("call \(owner.id.uuidString, privacy: .public): still on after the hang-up's wait (\(String(describing: owner.phase), privacy: .public))")
        }
    }

    /// How long `endForVoiceChat` waits at most for the call to end and the system to let its audio go, looking again
    /// every `handOffStep`.
    static let handOffWait: Duration = .seconds(2)
    static let handOffStep: Duration = .milliseconds(50)

    /// An in-app voice chat (the phone's, the car's) ends before a system call takes the audio: its `stop` would
    /// deactivate the session under the call.
    func endVoiceChat(for reason: VoiceEnd) {
        guard let chat = voiceCalls.current, chat.surface != .call, !chat.phase.isEnded else { return }
        Self.log.notice("call: the \(chat.surface.rawValue, privacy: .public) voice chat ends for the call")
        voiceCalls.end(reason, from: chat.surface)
    }

    // MARK: Urgent calls (UrgentCallCenter)

    /// An urgent call rings: reported to the system at once (iOS ends an app that does not report a VoIP push's call).
    /// Throws when the system would not show it (Focus, Do Not Disturb): the companion notification is what shows then.
    func ring(_ id: UUID, callID: String, name: String) async throws {
        let call = add(id, kind: .urgent(callID: callID), direction: .incoming)
        attach(UrgentCallEngine(id: id), to: call)
        watchInterruptions()
        await readySystem()
        // Its category before the system can activate the session for it. Not while a call to Persona set it already (the
        // same), nor under an in-app voice chat, whose loudspeaker stays while it rings: answering ends the chat, and
        // sets the category then (`readyToAnswer`).
        if !audioReady(call), !voiceChatOn { await prepareAudio(call) }
        let system = system
        do {
            // The handle is the assistant's name, as for a call to Persona; the call's own name only its display name.
            try await system.reportIncoming(id, name: call.name, title: name)
        } catch {
            call.unshown = true
            noteLive()
            throw error
        }
        if system.knows(id) { known(call, by: "as it rang") }
    }

    /// The system has the call's conversation: the reports that waited for it go now. `path` says how it was found out
    /// (right after Start's fulfil, a `conversationChanged`, the ring's report), for the log.
    func known(_ call: PersonaCall, by path: String) {
        guard !call.machine.known else { return }
        Self.log.notice("call \(call.id.uuidString, privacy: .public): the system has the conversation (\(path, privacy: .public))")
        dispatch(call, .known)
    }

    /// The urgent call's notification was tapped while it rang: answered through the system; false when it refused (the
    /// caller answers here then).
    func answer(_ id: UUID) async -> Bool {
        let system = system
        do {
            try await system.perform(.join, for: id)
            return true
        } catch {
            Self.log.notice("call \(id.uuidString, privacy: .public): the system refused to answer: \(CallSystemError(error).description, privacy: .public)")
            return false
        }
    }

    /// Answered without the system (its notification tapped, and the system refused the ring or Join): the page and the
    /// turns start here. A call to Persona that is on goes on hold through the system first, as Hold & Accept does, and
    /// the urgent call talks once the system let that call's audio go (at most a second); the call to Persona resumes
    /// when the urgent call ends (`finish`). False when the call to Persona could not be held: the urgent call is not
    /// answered here then (the call's voice would hear the owner's answers too).
    func answeredHere(_ id: UUID) async -> Bool {
        guard let call = call(id), call.phase == .ringing else { return false }
        if let owner = ownerCall, owner.phase.isLive, !owner.machine.holds.contains(.system) {
            do {
                try await system.perform(.pause(true), for: owner.id)
            } catch {
                Self.log.notice("call \(id.uuidString, privacy: .public): not answered here, the call to Persona could not be held: \(CallSystemError(error).description, privacy: .public)")
                return false
            }
            for _ in 0..<20 where owner.phase.isLive && (!owner.machine.holds.contains(.system) || owner.machine.audioActive) {
                try? await Task.sleep(for: .milliseconds(50))
            }
            if owner.phase.isLive, !owner.machine.holds.contains(.system) {
                Self.log.notice("call \(id.uuidString, privacy: .public): not answered here, the system did not hold the call to Persona")
                // Its pause may still come: the call to Persona goes on all the same.
                perform(.pause(false), owner)
                return false
            }
        }
        guard call.phase == .ringing else { return false }
        await readyToAnswer(call)
        guard call.phase == .ringing else { return false }
        dispatch(call, .answeredHere)
        return true
    }

    /// The urgent call stopped ringing for good (missed, answered or declined elsewhere).
    func settle(_ id: UUID, _ reason: CallEndReason) {
        guard let call = call(id) else { return }
        dispatch(call, .settled(reason))
    }

    /// The urgent call's conversation was over by itself.
    func urgentEnded(_ id: UUID) {
        guard let call = call(id) else { return }
        dispatch(call, .engineEnded(failed: false))
    }

    // MARK: The call's audio category

    /// A call's audio category, for both kinds, before the system activates the session for it (the app never activates
    /// it): the earpiece when nothing is connected, as a phone call; the page's Speaker and the system's call UI move it
    /// (owner decision 2026-11-17).
    func prepareAudio(_ call: PersonaCall) async {
        await callCategory()
        call.audioPrepared = true
    }

    /// Off the main actor (setting the category blocks for a moment), on the audio session's queue: a dictation block
    /// queued before the call went live runs first, and one queued after sees the call and touches nothing
    /// (`LiveCallGate.sessionQueue`).
    static func setCallCategory() async {
        let options = PhoneVoiceAudio.callSessionOptions
        await withCheckedContinuation { (done: CheckedContinuation<Void, Never>) in
            LiveCallGate.sessionQueue.async {
                do {
                    try AVAudioSession.sharedInstance().setCategory(.playAndRecord, mode: .voiceChat, options: options)
                } catch {
                    PersonaCalls.log.notice("call: the audio category: \(error.localizedDescription, privacy: .public)")
                }
                done.resume()
            }
        }
    }

    /// `call`'s category is set: its own, or that of a call to Persona that is on and set it (the same). A call to Persona
    /// still being placed (waiting for the hub or the microphone prompt) has set nothing yet.
    private func audioReady(_ call: PersonaCall) -> Bool {
        call.audioPrepared || calls.contains { !$0.kind.isUrgent && $0.phase.isLive && $0.audioPrepared }
    }

    /// An in-app voice chat (the phone's, the car's) is on: its own session and loudspeaker.
    private var voiceChatOn: Bool {
        guard let chat = voiceCalls.current else { return false }
        return chat.surface != .call && !chat.phase.isEnded
    }

    /// Answering an urgent call whose category is not set yet (it rang under an in-app voice chat): the chat ends first
    /// (its `stop` deactivates the session), then the category, before the system activates the session. One the system
    /// did not show refused nothing while it rang: a voice chat may have set its own category since, so it is set again.
    private func readyToAnswer(_ call: PersonaCall) async {
        endVoiceChat(for: .urgentCall)
        if call.unshown || !audioReady(call) { await prepareAudio(call) }
    }

    /// An urgent call's audio has no observer of its own: the interruption reaches its machine from here (a call to
    /// Persona hears its own through `PhoneVoiceAudio`).
    private func watchInterruptions() {
        guard interruptions == nil else { return }
        interruptions = NotificationCenter.default.addObserver(forName: AVAudioSession.interruptionNotification, object: nil,
                                                               queue: .main) { @Sendable [weak self] note in
            let type = (note.userInfo?[AVAudioSessionInterruptionTypeKey] as? UInt).flatMap(AVAudioSession.InterruptionType.init)
            MainActor.assumeIsolated {
                guard let self else { return }
                for call in self.calls where call.kind.isUrgent && call.phase.isLive {
                    if type == .began { self.dispatch(call, .interruptionBegan) } else if type == .ended { self.dispatch(call, .interruptionEnded) }
                }
            }
        }
    }

    private func stopWatchingInterruptions() {
        if let interruptions { NotificationCenter.default.removeObserver(interruptions) }
        interruptions = nil
    }

    // MARK: DEBUG

    #if DEBUG
    nonisolated enum Sample: String, CaseIterable, Sendable {
        case dialing, connecting, connected, muted, held, interrupted, resume, speaker, ended
        /// Muted on the loudspeaker; the voice's connection being picked up again; AirPods connected (Audio in
        /// Speaker's place); the assistant speaking.
        case mutedSpeaker, reconnecting, audioRoute, speaking
        /// Each of the avatar's rows (#9887 avatar states): the owner speaking; the voice thinking before its answer; the
        /// conversation working; a card waiting for the owner; the assistant speaking while the owner is muted; the
        /// voice's upstream paused (the call listens, it is not on hold).
        case hearing, thinking, working, asking, mutedSpeaking, paused

        /// The sample's voice once connected: its live state.
        var voice: VoiceLiveState {
            switch self {
            case .speaking, .mutedSpeaking: .speaking
            case .hearing: .hearing
            default: .listening
            }
        }
    }

    /// A call to Persona's page in each of its states, with no system, no audio and no hub (`-showCallSample <state>`).
    /// `name`, `caption` and `reason` stand in for the assistant's name (on the page and the bar), its latest words and
    /// why the call ended (`-showCallSampleName`, `-showCallSampleCaption`, `-showCallSampleReason`): long texts for the
    /// screenshots. `look`: the assistant's look instead of the one on screen (`-showCallSampleLook`, a package's or an
    /// uploaded one); `agent`: a call to an Agent session instead of the main conversation (`-showCallSampleAgent`).
    func showSample(_ sample: Sample, name: String? = nil, caption: String? = nil, reason: String? = nil, look: Persona? = nil,
                    agent: Bool = false) {
        for old in calls where old.sample { remove(old) }
        let call = add(kind: .owner(group: AppModel.shared.hub?.group ?? ""), direction: .outgoing)
        if let look { call.assistant.retake(look, name: assistantName()) }
        if agent {
            call.conversation = CallConversation(title: "修登录页的跳转", detail: "Claude Code · MacBook", // i18n-ignore: a DEBUG sample's conversation
                                                 agent: AgentKind(brand: Theme.Brand.claude, name: "Claude Code", surface: nil))
        }
        call.sample = true
        call.sampleName = name
        let now = clock.now
        var events: [CallEvent] = []
        if sample != .dialing { events += [.known, .startAction] }
        if ![.dialing, .connecting].contains(sample) { events += [.audioActivated, .engineRunning] }
        switch sample {
        case .dialing, .connecting: break
        case .connected, .speaker, .reconnecting, .audioRoute, .speaking, .hearing, .thinking, .working, .asking, .paused:
            events.append(.voiceReady)
        case .muted, .mutedSpeaker, .mutedSpeaking: events += [.voiceReady, .muteTapped(true), .muteAction(true)]
        case .held: events += [.voiceReady, .pauseAction(true)]
        case .interrupted: events += [.voiceReady, .interruptionBegan]
        case .resume: events += [.voiceReady, .interruptionBegan, .audioDeactivated, .interruptionEnded, .timer(.recoveryUnpause),
                                 .timer(.recoveryActivate), .timer(.recoveryGiveUp)]
        case .ended: events += [.voiceReady, .engineEnded(failed: true)]
        }
        for event in events {
            // Connected 42 seconds ago, as the urgent call's sample.
            _ = call.machine.handle(event, at: event == .voiceReady ? now.addingTimeInterval(-42) : now)
        }
        if sample == .ended { call.note = reason ?? String(localized: "Can't reach the hub") }
        switch sample {
        case .speaker, .mutedSpeaker: route.show(.speaker)
        case .audioRoute: route.show(.bluetooth("AirPods Pro")) // i18n-ignore: a device's name in a DEBUG sample
        default: route.show(.receiver)
        }
        call.sampleReconnecting = sample == .reconnecting
        if call.machine.phase.connectedSince != nil, sample != .reconnecting {
            call.sampleVoice = sample.voice
            call.sampleLink = sample == .paused ? .paused : .live
            call.sampleActivity = CallActivity(working: sample == .working, asking: sample == .asking, thinking: sample == .thinking)
        }
        if call.machine.phase.connectedSince != nil {
            call.sampleCaption = caption ?? "好的，明早九点的会我帮你改到十点了，也跟大家说了一声。" // i18n-ignore: a DEBUG sample's caption
        }
        call.sync()
        presented = true
    }

    /// The look a sample shows (`-showCallSampleLook`): `package` (the look on screen when it is a package's, else the
    /// first package on the phone that the owner did not upload), `uploaded` (the first uploaded one on the phone), or a
    /// look's id; nil until it is on the phone.
    static func sampleLook(_ wanted: String, model: AppModel) -> Persona? {
        let store = model.personas
        let onPhone = ([model.persona] + store.offered).filter { !$0.isBuiltin && store.isOnPhone($0) }
        switch wanted {
        case "package": return onPhone.first { !$0.isUploaded }
        case "uploaded": return onPhone.first(where: \.isUploaded)
        default: return onPhone.first { $0.id == wanted }
        }
    }

    /// The urgent call page's sample on hold (`-showUrgentCallSample held`, `UrgentCallCenter.showSample`): the system call
    /// `id` behind it answered, then interrupted with no audio back, so its page offers Resume. No system, no audio.
    func showUrgentSample(_ id: UUID, callID: String) {
        let call = add(id, kind: .urgent(callID: callID), direction: .incoming)
        call.sample = true
        let events: [CallEvent] = [.known, .joinAction, .audioActivated, .engineRunning, .interruptionBegan, .audioDeactivated,
                                   .interruptionEnded, .timer(.recoveryUnpause), .timer(.recoveryActivate), .timer(.recoveryGiveUp)]
        for event in events { _ = call.machine.handle(event, at: clock.now) }
        call.sync()
        route.show(.receiver)
    }
    #endif
}

#if DEBUG
/// A made-up voice for the samples (#9887 avatar states): phrases of syllables with a breath between them, 0…1, so the
/// speaking motion (the bounce, the ripples on its peaks, Mochi's mouth) and the ring show on the Simulator.
nonisolated enum CallSampleVoice {
    static func level(at t: TimeInterval) -> Double {
        // A phrase of about three seconds, then most of a second of quiet.
        let phrase = t.truncatingRemainder(dividingBy: 4.2)
        guard phrase < 3.2 else { return 0.02 }
        let syllable = abs(sin(t * .pi * 2.4))
        let stress = 0.65 + 0.35 * sin(t * 1.9)
        return min(1, 0.08 + 0.92 * syllable * syllable * stress)
    }
}
#endif

// MARK: The system's callbacks

extension PersonaCalls: CallSystemHandler {
    func system(perform action: CallSystemAction, for id: UUID, fulfil: @escaping @MainActor () -> Void,
                fail: @escaping @MainActor () -> Void) {
        guard let call = call(id) else {
            // Not one of ours (any more): an End or a mute does no harm, anything else is refused.
            Self.log.notice("call \(id.uuidString, privacy: .public): \(String(describing: action), privacy: .public) for no call of ours")
            switch action {
            case .end, .mute: fulfil()
            default: fail()
            }
            return
        }
        let event: CallEvent = switch action {
        case .start: .startAction
        case .join: .joinAction
        case .end: .endAction
        case .mute(let on): .muteAction(on)
        case .pause(let on): .pauseAction(on)
        case .other: .otherAction
        }
        // An urgent call answered whose category is not set yet (it rang under an in-app voice chat): the chat ends and the
        // category is set before Join is fulfilled, after which the system activates the session.
        if action == .join, call.kind.isUrgent, call.phase == .ringing, !audioReady(call) {
            Task { [weak self, weak call] in
                guard let self, let call else { return fail() }
                await self.readyToAnswer(call)
                self.dispatch(call, .joinAction, fulfil: fulfil, fail: fail)
            }
            return
        }
        dispatch(call, event, fulfil: fulfil, fail: fail)
        // The conversation can be in the system's list only once Start is fulfilled: what waited for it goes now.
        if action == .start, !call.machine.known, system.knows(id) { known(call, by: "right after the Start's fulfil") }
    }

    func system(changed id: UUID, state: CallSystemState) {
        guard let call = call(id) else { return }
        if state == .left {
            dispatch(call, .systemEnded)
        } else if !call.machine.known {
            known(call, by: "conversationChanged, \(state)")
        }
    }

    func system(timedOut action: CallSystemAction, for id: UUID) {
        guard let call = call(id) else { return }
        dispatch(call, .actionTimedOut)
    }

    /// The session is the joined call's: not one the system holds, nor one still ringing. A call still dialling takes
    /// it too (its Start not handled yet): the engine starts once the voice has begun.
    func systemActivatedAudio() {
        for call in calls where call.phase.isLive && call.phase != .ringing && !call.machine.holds.contains(.system) {
            dispatch(call, .audioActivated)
        }
    }

    func systemDeactivatedAudio() {
        for call in calls where call.machine.audioActive { dispatch(call, .audioDeactivated) }
    }

    func systemReset() {
        for call in calls where call.phase.isLive { dispatch(call, .reset) }
    }
}
#endif
