import Foundation
import Testing
@testable import Persona

/// The system's side faked (#9290): what `PersonaCalls` asked of it, and the actions and audio callbacks a test hands
/// back, as LiveCommunicationKit's delegate would.
@MainActor final class FakeCallSystem: CallSystem {
    weak var handler: (any CallSystemHandler)?
    private(set) var starts: [UUID] = []
    /// The handle's value of each Start (the assistant's name).
    var startNames: [String] { startHandles.map(\.value) }
    /// The handle of each Start.
    private(set) var startHandles: [CallHandle] = []
    /// Each ring: the handle's value (the assistant's name) and the call's own name (the display name only).
    private(set) var rings: [(id: UUID, name: String, title: String)] = []
    private(set) var requests: [CallRequest] = []
    private(set) var reports: [CallReport] = []
    private(set) var fulfilled: [CallSystemAction] = []
    private(set) var failed: [CallSystemAction] = []
    private(set) var activations = 0
    var known: Set<UUID> = []
    /// Started or reported calls are in its list at once; false: only once a test says so.
    var knowsAtOnce = true
    /// Thrown by the next Starts, in order.
    var startErrors: [any Error] = []
    var requestError: (any Error)?
    /// Requests it refuses (with `CallSystemStandIn.refused`), whatever `requestError` says.
    var refuses: [CallRequest] = []
    /// Thrown by the next rings (Focus or Do Not Disturb would not show the call).
    var ringError: (any Error)?
    var activates = true

    func start(_ id: UUID, handle: CallHandle) async throws {
        starts.append(id)
        startHandles.append(handle)
        if !startErrors.isEmpty { throw startErrors.removeFirst() }
        if knowsAtOnce { known.insert(id) }
    }

    func reportIncoming(_ id: UUID, name: String, title: String) async throws {
        rings.append((id, name, title))
        if let ringError { throw ringError }
        known.insert(id)
    }

    func perform(_ request: CallRequest, for id: UUID) async throws {
        requests.append(request)
        if let requestError { throw requestError }
        if refuses.contains(request) { throw CallSystemStandIn.refused }
    }

    func report(_ report: CallReport, for id: UUID) { reports.append(report) }

    func knows(_ id: UUID) -> Bool { known.contains(id) }

    func activateAudio() async -> Bool {
        activations += 1
        return activates
    }

    /// The system asks the app to carry out `action`.
    func act(_ action: CallSystemAction, _ id: UUID) {
        handler?.system(perform: action, for: id, fulfil: { self.fulfilled.append(action) }, fail: { self.failed.append(action) })
    }

    func activate() { handler?.systemActivatedAudio() }
    func deactivate() { handler?.systemDeactivatedAudio() }

    var ended: [CallEndedReport] { reports.compactMap { if case .ended(_, let why) = $0 { why } else { nil } } }
}

/// A `PersonaCalls` on the fake system, the voice on `VoiceRig`'s fakes, the clock moved by hand.
@MainActor final class CallRig {
    let system = FakeCallSystem()
    let voice = VoiceRig()
    let voiceCalls = VoiceCalls()
    /// The groups whose call to Persona ended (`PersonaCalls.callLeft`: their tunnels may go).
    private(set) var left: [String] = []
    /// How many times a call's audio category was set (`PersonaCalls.prepareAudio`).
    private(set) var categories = 0
    /// The assistant's name a new call carries (`PersonaCalls.assistant`: the persona on screen, here named so).
    var assistant = "Persona"
    /// The persona on screen, when a test gives a whole one (its look and name): else the built-in named `assistant`.
    var persona: Persona?
    /// The name the main conversation's top bar says when it is not the look's (a hub with several assistants: the current
    /// one's, `AppModel.assistantShownName`, #9887).
    var shownName: String?
    /// Whose main conversation a new call is to: the hub and the assistant picked there (`PersonaCalls.assistantOwner`).
    var owner = CallAssistant.Owner(group: "g", manager: nil)
    /// Developing Persona's Call handle (#9887).
    var handleKind: CallHandleKind = .generic
    /// What happened before the system's side was made, in order: "prepared" (`PersonaCalls.readySystem`), "made".
    private(set) var systemSteps: [String] = []
    private(set) lazy var calls: PersonaCalls = {
        let calls = PersonaCalls(system: { [weak self, system] in
            self?.systemSteps.append("made")
            return system
        }, clock: voice.clock, voiceCalls: voiceCalls,
                                 ownerAudio: { [voice] in voice.audio }, ownerDeps: { [voice] _, audio, events in
            var deps = voice.deps
            deps.audio = { audio }
            deps.callEvents = events
            return deps
        }, callCategory: { [weak self] in self?.categories += 1 }, assistant: { [weak self] in
            self?.persona ?? CallRig.persona(named: self?.assistant ?? "")
        }, assistantName: { [weak self] in
            self?.shownName ?? self?.persona?.name ?? self?.assistant ?? ""
        }, assistantOwner: { [weak self] in
            self?.owner ?? CallAssistant.Owner(group: "g", manager: nil)
        }, prepareSystem: { [weak self] in self?.systemSteps.append("prepared") }, handleKind: { [weak self] in self?.handleKind ?? .generic })
        calls.callLeft = { [weak self] in self?.left.append($0) }
        return calls
    }()

    /// A call to Persona the system was asked to start (it has not handed Start back yet).
    func dial() async -> PersonaCall {
        let call = calls.add(kind: .owner(group: voice.key.group), direction: .outgoing)
        attachEngine(call)
        await calls.place(call, origin: .app)
        return call
    }

    /// The call's voice engine, as `callPersona` attaches it once the hub, the extension and the microphone said yes.
    func attachEngine(_ call: PersonaCall) {
        let deps = calls.ownerDeps
        let key = voice.key
        let engine = OwnerCallEngine(key: key, extensionID: "voice", recent: [], audio: voice.audio, calls: voiceCalls,
                                     deps: { audio, events in deps(key.group, audio, events) })
        calls.attach(engine, to: call)
    }

    /// Started by the system, its audio activated and the voice ready.
    func connected() async -> (PersonaCall, FakeVoiceSocket) {
        let call = await dial()
        system.act(.start, call.id)
        await voice.waitUntil { voice.socket != nil }
        system.activate()
        let socket = voice.socket!
        socket.ready(voice.key)
        await voice.waitUntil { call.connectedSince != nil }
        return (call, socket)
    }

    func settle() async { for _ in 0..<20 { await Task.yield() } }

    /// The built-in look under another name.
    static func persona(named name: String) -> Persona {
        var persona = Persona.builtin
        persona.name = name
        return persona
    }
}

/// The app's one call manager (#9290): the system's actions reach the call by its id, the machine says what to do and
/// `PersonaCalls` does it, for a call to Persona on a real `VoiceConversation(.call)` over fakes, and for urgent calls.
@MainActor
struct PersonaCallsTests {
    @Test func aCallToPersonaStartsTheVoiceOnStartAndItsAudioOnlyOnActivation() async throws {
        let rig = CallRig()
        let call = await rig.dial()
        #expect(rig.system.starts == [call.id] && call.phase == .dialing)
        #expect(rig.calls.ownerCall === call)
        rig.system.act(.start, call.id)
        #expect(rig.system.fulfilled == [.start])
        #expect(call.phase == .connecting)
        #expect(rig.system.reports.prefix(2).map { if case .startedConnecting = $0 { "connecting" } else { "\($0)" } } == ["members", "connecting"])
        await rig.voice.waitUntil { rig.voice.socket != nil }
        let socket = try #require(rig.voice.socket)
        #expect(socket.start?["surface"] as? String == "call")
        #expect(rig.voice.audio.running && !rig.voice.audio.active, "the microphone waits for the system's activation")
        #expect(rig.voiceCalls.current === call.voice)

        rig.system.activate()
        #expect(rig.voice.audio.activations == 1)
        socket.ready(rig.voice.key)
        #expect(call.connectedSince != nil)
        #expect(rig.system.reports.contains { if case .connected = $0 { true } else { false } })
    }

    @Test func reportsWaitUntilTheSystemHasTheConversation() async {
        let rig = CallRig()
        rig.system.knowsAtOnce = false
        let call = await rig.dial()
        rig.system.act(.start, call.id)
        #expect(rig.system.reports.isEmpty, "dropped by a system that does not have it yet")
        rig.calls.system(changed: call.id, state: .idle)
        #expect(rig.system.reports == [.members, .startedConnecting(rig.voice.clock.now)])
    }

    @Test func theBackgroundAnotherPageOrTheLockNeverEndTheCall() async {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        rig.voiceCalls.appDidEnterBackground()
        rig.voiceCalls.leave(rig.voice.key)
        rig.calls.minimise()
        #expect(call.phase.isLive && call.voice?.phase.isEnded == false && !socket.closed)
        #expect(rig.system.ended.isEmpty)
    }

    @Test func hangingUpEndsBothSidesOnce() async {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        rig.calls.hangUp()
        await rig.settle()
        #expect(rig.system.requests == [.end])
        rig.system.act(.end, call.id)
        #expect(rig.system.fulfilled.last == .end)
        #expect(call.phase == .ended(.hungUp) && call.note == nil)
        #expect(call.voice?.phase == .ended(.user) && socket.closed)
        #expect(rig.system.ended.isEmpty, "the system ended it: nothing to report back")
        #expect(rig.voiceCalls.current == nil, "no Continue after a system call")
    }

    @Test func theVoiceEndingByItselfEndsTheSystemsCall() async {
        let lost = CallRig()
        let (call, _) = await lost.connected()
        call.voice?.end(.lost)
        #expect(call.phase == .ended(.failed))
        #expect(lost.system.ended == [.failed])
        #expect(call.note == VoiceEndNote.make(.lost, extensionID: "voice")?.text)
        // The ended voice stays for the main conversation's 「通话已结束 · …」 (its Continue calls Persona again), and is
        // never picked up again itself; leaving the page lets it go.
        #expect(lost.voiceCalls.current === call.voice)
        let picked = await lost.voiceCalls.resume(lost.voice.key)
        #expect(picked == nil && call.voice?.phase == .ended(.lost))
        #expect(lost.voice.sockets.count == 1, "no second socket for an ended system call")
        lost.voiceCalls.leave(lost.voice.key)
        #expect(lost.voiceCalls.current == nil)

        let idle = CallRig()
        let (quiet, _) = await idle.connected()
        quiet.voice?.end(.idle)
        #expect(idle.system.ended == [.remoteEnded])
    }

    /// Nothing in the app ends a live call by itself (#9290 follow-up): another chat's Voice Chat or CarPlay's Talk to
    /// Persona is refused while a call to Persona is on, touching nothing (no hang-up, no socket, no audio). With the
    /// user's yes (End Call and Start: `endingCall`) the call is hung up through the system, and the new voice's audio
    /// opens only once the system let the call's audio go (LiveCommunicationKit releases it late, and its deactivation
    /// would stop the new audio), whichever entry asked (`VoiceCalls.endSystemCall`).
    @Test func anotherVoiceAsksFirstThenEndsTheCallAndWaitsForItsAudio() async {
        for surface in [VoiceSurface.phone, .carPlay] {
            let rig = CallRig()
            rig.assistant = "Mochi"
            let (call, socket) = await rig.connected()
            let other = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s2")
            #expect(await rig.voiceCalls.start(other, extensionID: "voice", surface: surface, deps: rig.voice.deps) == .systemCallOn("Mochi"))
            await rig.settle()
            #expect(rig.system.requests.isEmpty && call.phase.isLive && !socket.closed, "\(surface): the call was touched")
            #expect(rig.voice.sockets.count == 1 && rig.voiceCalls.current === call.voice, "\(surface)")
            let starting = Task {
                await rig.voiceCalls.start(other, extensionID: "voice", surface: surface, deps: rig.voice.deps, endingCall: true)
            }
            await rig.voice.waitUntil { rig.system.requests.contains(.end) }
            try? await Task.sleep(for: .milliseconds(150))
            #expect(rig.voice.sockets.count == 1 && rig.voiceCalls.current === call.voice, "\(surface): the new voice waits")
            rig.system.act(.end, call.id)
            rig.system.deactivate()
            #expect(await starting.value == nil)
            #expect(call.phase == .ended(.hungUp) && rig.system.ended.isEmpty, "\(surface): the system ended it")
            #expect(rig.voiceCalls.current?.surface == surface && rig.voice.sockets.count == 2, "\(surface)")
        }
    }

    @Test func aPauseHoldsTheVoiceAtOnceAndTheResumeOnceTheAudioIsBack() async {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        rig.system.act(.pause(true), call.id)
        #expect(rig.system.fulfilled.last == .pause(true), "at once: a late or failed pause ends the call")
        #expect(call.held && call.holdReason == .system)
        #expect(call.voice?.held == true && rig.voice.audio.heldNow)
        rig.system.deactivate()
        #expect(rig.voice.audio.deactivations == 1 && call.phase.isLive)
        socket.deliver(data: .pcm(480))
        #expect(rig.voice.audio.played.isEmpty, "nothing plays on hold")
        rig.system.act(.pause(false), call.id)
        #expect(call.held && call.voice?.held == true, "on hold until the audio is back")
        rig.system.activate()
        #expect(!call.held && call.voice?.held == false && !rig.voice.audio.heldNow)
    }

    @Test func muteGoesThroughTheSystemAndItsEchoChangesNothing() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.voice.clock.advance(1) // past the engine's start
        rig.calls.setMuted(true)
        await rig.settle()
        #expect(rig.system.requests == [.mute(true)] && !call.muted)
        rig.system.act(.mute(true), call.id)
        #expect(call.muted && call.voice?.muted == true)
        rig.system.act(.mute(true), call.id)
        #expect(rig.system.fulfilled.filter { $0 == .mute(true) }.count == 2 && call.muted)
        // The bar's own Mute on the voice: the system hears it.
        call.voice?.muted = false
        await rig.settle()
        #expect(rig.system.requests.last == .mute(false))
    }

    @Test func aMuteTheSystemRefusesAppliesHere() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.system.requestError = CallSystemStandIn.refused
        rig.calls.setMuted(true)
        await rig.voice.waitUntil { call.muted }
        #expect(call.muted && call.voice?.muted == true)
    }

    /// Mute on "Calling…", before the call's engine was attached and before the system had the conversation (it
    /// refuses the mute then): it applies here; the engine attached after starts muted, so the microphone sends silence
    /// from its first buffer, and the system hears the mute once it has the conversation.
    @Test func aMuteWhileItDialsKeepsTheMicrophoneMuted() async throws {
        let rig = CallRig()
        let call = rig.calls.add(kind: .owner(group: rig.voice.key.group), direction: .outgoing)
        rig.system.requestError = CallSystemStandIn.refused // no conversation yet: nothing to mute
        rig.calls.setMuted(true)
        await rig.voice.waitUntil { call.muted }
        rig.system.requestError = nil
        rig.attachEngine(call)
        await rig.calls.place(call, origin: .app)
        rig.system.act(.start, call.id)
        await rig.voice.waitUntil { rig.voice.socket != nil }
        rig.system.activate()
        let socket = try #require(rig.voice.socket)
        socket.ready(rig.voice.key)
        await rig.voice.waitUntil { call.connectedSince != nil }
        #expect(call.muted && call.voice?.muted == true, "the page says Mute and the voice is muted")
        rig.voice.audio.capture(.pcm(320, 5000))
        #expect(socket.audio.last == Data(count: 640), "the microphone sends silence")
        await rig.settle()
        #expect(rig.system.requests == [.mute(true), .mute(true)], "the system hears the mute once it has the conversation")
        rig.system.act(.mute(true), call.id)
        #expect(call.muted && rig.system.fulfilled.last == .mute(true), "its echo changes nothing")
    }

    @Test func aSystemMuteAsTheEngineStartsIsIgnored() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.system.act(.mute(true), call.id)
        #expect(!call.muted && rig.system.fulfilled.last == .mute(true))
        await rig.settle()
        #expect(rig.system.requests == [.mute(false)], "the owner's mute asserted again, once")
    }

    /// A route change the engine could not be wired again for builds a new one, voice processing on again: its startup
    /// mute is the engine's too, ignored and corrected, and a mute half a second after it runs is the owner's.
    @Test func anEngineARouteChangeRebuiltHasItsStartupMuteCorrected() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.system.act(.mute(true), call.id)
        await rig.settle()
        rig.voice.clock.advance(1)
        rig.voice.audio.rebuildEngine()
        rig.system.act(.mute(true), call.id)
        #expect(!call.muted && call.voice?.muted == false, "the new engine's, not the owner's")
        await rig.settle()
        #expect(rig.system.requests == [.mute(false), .mute(false)], "corrected again for the new engine")
        rig.voice.clock.advance(1)
        rig.system.act(.mute(true), call.id)
        #expect(call.muted && call.voice?.muted == true)
    }

    @Test func noActivationActivatesOnceThenTheCallFails() async {
        let rig = CallRig()
        let call = await rig.dial()
        rig.system.act(.start, call.id)
        await rig.voice.waitUntil { rig.voice.socket != nil }
        rig.system.activates = false
        await rig.voice.advance(5, sleepers: 2)
        await rig.voice.waitUntil { call.phase.isEnded }
        #expect(rig.system.activations == 1)
        #expect(call.phase == .ended(.failed) && rig.system.ended == [.failed])
        #expect(call.voice?.phase.isEnded == true)
    }

    @Test func anInterruptionHoldsAndTheCallStaysWhenTheAudioDoesNotComeBack() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.voice.audio.emit(.interrupted)
        #expect(call.held && call.holdReason == .interruption && call.phase.isLive)
        #expect(call.voice?.phase.isEnded == false)
        rig.system.deactivate()
        rig.system.activates = false
        // Each wait starts only once its timer sleeps (the clock moves only for sleepers already there).
        let sleeping = rig.voice.clock.sleeping
        rig.voice.audio.emit(.interruptionEnded(shouldResume: true))
        await rig.voice.advance(1, sleepers: sleeping + 2)
        await rig.voice.waitUntil { rig.system.requests.contains(.pause(false)) }
        #expect(rig.system.requests.contains(.pause(false)), "the system asked to resume")
        await rig.voice.advance(0.5, sleepers: sleeping + 2)
        await rig.voice.waitUntil { rig.system.activations == 1 }
        #expect(rig.system.activations == 1, "then the session activated once")
        await rig.voice.advance(4, sleepers: sleeping + 1)
        await rig.voice.waitUntil { call.resumeOffered }
        #expect(call.resumeOffered && call.held && call.phase.isLive, "never ended silently")
        rig.system.activate()
        #expect(!call.held && !call.resumeOffered)
    }

    /// The interruption's notifications alone (no `didDeactivate`): the engine went with the session, and the system's
    /// next activation brings it back; the call is never left connected and silent.
    @Test func anInterruptionWithoutADeactivationComesBackOnTheNextActivation() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        #expect(rig.voice.audio.activations == 1)
        rig.voice.audio.interruptSystemSession()
        #expect(call.held && call.holdReason == .interruption && rig.voice.audio.deactivations == 1)
        rig.voice.audio.emit(.interruptionEnded(shouldResume: true))
        rig.system.activate()
        #expect(rig.voice.audio.activations == 2 && rig.voice.audio.active, "the engine starts again")
        #expect(!call.held && call.voice?.held == false && !rig.voice.audio.heldNow)
    }

    @Test func anInterruptionWithoutADeactivationStillRecoversAndOffersResume() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.voice.audio.interruptSystemSession()
        rig.system.activates = false
        let sleeping = rig.voice.clock.sleeping
        rig.voice.audio.emit(.interruptionEnded(shouldResume: true))
        await rig.voice.advance(1, sleepers: sleeping + 2)
        await rig.voice.waitUntil { rig.system.requests.contains(.pause(false)) }
        #expect(rig.system.requests.contains(.pause(false)), "the system asked to resume")
        await rig.voice.advance(0.5, sleepers: sleeping + 2)
        await rig.voice.waitUntil { rig.system.activations == 1 }
        #expect(rig.system.activations == 1, "then the session activated once")
        await rig.voice.advance(4, sleepers: sleeping + 1)
        await rig.voice.waitUntil { call.resumeOffered }
        #expect(call.resumeOffered && call.held && call.phase.isLive)
        rig.system.activate()
        #expect(!call.held && rig.voice.audio.activations == 2)
    }

    /// Start and End (or a reset) one after the other, before the voice's start ran: it never begins, so nothing claims
    /// the microphone or opens a socket after the system call is over.
    @Test func aStartThenAnEndBeforeTheVoiceBeganNeverStartsIt() async {
        for reset in [false, true] {
            let rig = CallRig()
            let call = await rig.dial()
            rig.system.act(.start, call.id)
            if reset { rig.calls.systemReset() } else { rig.system.act(.end, call.id) }
            await rig.settle()
            try? await Task.sleep(for: .milliseconds(50))
            #expect(call.phase.isEnded && call.voice?.phase.isEnded == true, "reset \(reset)")
            #expect(rig.voice.sockets.isEmpty && !rig.voice.audio.running, "reset \(reset): no socket, no audio")
            #expect(rig.voice.microphone.isFree && rig.voiceCalls.current == nil, "reset \(reset)")
        }
    }

    /// Calls to Persona end: their group's tunnels may go (the background kept them for the call).
    @Test func aCallToPersonaEndingLetsItsTunnelsGo() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        #expect(rig.left.isEmpty)
        call.voice?.end(.lost)
        #expect(rig.left == [rig.voice.key.group])
    }

    @Test func aStartTooEarlyIsTriedOnceMoreAndAnotherRefusalFallsBack() async {
        let early = NSError(domain: CallSystemError.requestTransaction, code: 6)
        let rig = CallRig()
        rig.system.startErrors = [early]
        let call = rig.calls.add(kind: .owner(group: "g"), direction: .outgoing)
        let placing = Task { await rig.calls.place(call, origin: .app) }
        await rig.voice.advance(0.5)
        await placing.value
        #expect(rig.system.starts.count == 2 && call.phase.isLive)

        let full = CallRig()
        full.system.startErrors = [NSError(domain: "wrapper", code: 1, userInfo: [NSUnderlyingErrorKey: NSError(domain: CallSystemError.requestTransaction, code: 7)])]
        let refused = full.calls.add(kind: .owner(group: "g"), direction: .outgoing)
        var instead: CallSystemError?
        await full.calls.place(refused, origin: .app) { instead = $0 }
        #expect(instead?.isAtCapacity == true && full.system.starts.count == 1)
        #expect(refused.phase == .ended(.failed) && refused.note == PersonaCalls.insteadLine(instead!))
    }

    @Test func aResetEndsEveryCall() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.calls.systemReset()
        #expect(call.phase == .ended(.failed) && call.voice?.phase.isEnded == true)
    }

    // MARK: Urgent calls

    /// The ring's handle is the assistant's name (what the system shows and Recents keeps); the call's own name only its
    /// display name.
    @Test func anUrgentCallRingsWithTheAssistantsHandleAndIsRefusedNothing() async throws {
        let rig = CallRig()
        rig.assistant = "Mochi"
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Urgent: billing")
        #expect(rig.system.rings.map(\.name) == ["Mochi"] && rig.system.rings.map(\.title) == ["Urgent: billing"])
        #expect(rig.calls.urgentInProgress && rig.calls.call(id)?.phase == .ringing)
        rig.system.act(.join, id)
        #expect(rig.system.fulfilled == [.join] && rig.calls.call(id)?.connectedSince != nil)
        rig.calls.urgentEnded(id)
        #expect(rig.system.ended == [.remoteEnded] && !rig.calls.urgentInProgress)
    }

    /// Under an in-app voice chat the ring touches nothing of its audio (its category, so its loudspeaker, stays: the
    /// owner may decline); answering ends the chat first, then sets the call's category, then Join is fulfilled.
    @Test func answeringAnUrgentCallEndsAVoiceChatFirst() async throws {
        let rig = CallRig()
        await rig.voiceCalls.start(rig.voice.key, extensionID: "voice", surface: .phone, deps: rig.voice.deps)
        let chat = try #require(rig.voiceCalls.current)
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call")
        #expect(!chat.phase.isEnded, "a ring alone ends nothing")
        #expect(rig.categories == 0, "nor moves the chat's sound to the earpiece")
        rig.system.act(.join, id)
        await rig.voice.waitUntil { rig.system.fulfilled.contains(.join) }
        #expect(chat.phase == .ended(.urgentCall) && rig.categories == 1)
        #expect(rig.calls.call(id)?.connectedSince != nil)
        #expect(VoiceEndNote.make(.urgentCall, extensionID: "voice")?.again == .resume, "Continue offered")

        // Declined while it rang: the chat goes on, its category untouched.
        let declined = CallRig()
        await declined.voiceCalls.start(declined.voice.key, extensionID: "voice", surface: .phone, deps: declined.voice.deps)
        let other = UUID()
        try await declined.calls.ring(other, callID: "call_2", name: "Persona Urgent Call")
        declined.system.act(.end, other)
        #expect(declined.voiceCalls.current?.phase.isEnded == false && declined.categories == 0)
    }

    /// The urgent call's category as it rings, unless a call to Persona set it already: one still being placed (waiting
    /// for the hub or the microphone prompt, refused once the urgent call rings) has set nothing yet.
    @Test func anUrgentCallSetsItsCategoryUnlessACallToPersonaDid() async throws {
        let rig = CallRig()
        let placing = rig.calls.add(kind: .owner(group: "g"), direction: .outgoing)
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Persona Urgent Call")
        #expect(rig.categories == 1 && placing.phase == .dialing)

        let set = CallRig()
        let (call, _) = await set.connected()
        await set.calls.prepareAudio(call)
        let id = UUID()
        try await set.calls.ring(id, callID: "call_2", name: "Persona Urgent Call")
        #expect(set.categories == 1, "the call to Persona's is the same")
        set.system.act(.pause(true), call.id)
        set.system.act(.join, id)
        #expect(set.system.fulfilled.last == .join && set.categories == 1)
    }

    /// A call hung up while it waited (for the main conversation, the microphone prompt) and a newer one placed: the
    /// first one's refusal coming late does not take the newer call's page away or show the old failure.
    @Test func aLateRefusalOfAnEndedAttemptLeavesTheNewerCall() async {
        let rig = CallRig()
        rig.system.handler = rig.calls
        let first = rig.calls.add(kind: .owner(group: "g"), direction: .outgoing)
        rig.calls.presented = true
        rig.system.act(.end, first.id)
        #expect(first.phase.isEnded)
        let second = rig.calls.add(kind: .owner(group: "g"), direction: .outgoing)
        rig.calls.presented = true
        rig.calls.refuse(first, note: PersonaCalls.unreachableNote)
        #expect(rig.calls.presented && rig.calls.refusal == nil && second.phase.isLive)
        #expect(first.note == nil)
        rig.calls.refuse(second, note: PersonaCalls.unreachableNote)
        #expect(!rig.calls.presented && rig.calls.refusal == PersonaCalls.unreachableNote && second.phase.isEnded)
    }

    /// While an urgent call rings, and once it is answered: no voice chat starts and none is picked up again (Continue,
    /// CarPlay's too), and nothing of the audio is touched; once it is over, Continue works.
    @Test func noVoiceChatStartsOrContinuesUnderAnUrgentCall() async throws {
        let rig = CallRig()
        await rig.voiceCalls.start(rig.voice.key, extensionID: "voice", surface: .phone, deps: rig.voice.deps)
        let chat = try #require(rig.voiceCalls.current)
        chat.end(.interrupted)
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call")
        let stops = rig.voice.audio.stops
        #expect(await rig.voiceCalls.resume(rig.voice.key) == .urgentCall, "ringing: Continue is refused")
        let other = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s2")
        #expect(await rig.voiceCalls.start(other, extensionID: "voice", surface: .phone, deps: rig.voice.deps) == .urgentCall)
        #expect(rig.voiceCalls.current === chat && chat.phase == .ended(.interrupted))
        #expect(rig.voice.sockets.count == 1 && !rig.voice.audio.running && rig.voice.audio.stops == stops, "no audio touched")
        rig.system.act(.join, id)
        #expect(await rig.voiceCalls.start(other, extensionID: "voice", surface: .carPlay, deps: rig.voice.deps) == .urgentCall,
                "answered: CarPlay's start is refused")
        rig.calls.urgentEnded(id)
        #expect(await rig.voiceCalls.resume(rig.voice.key) == nil)
        #expect(chat.phase == .connecting && rig.voice.sockets.count == 2)
    }

    /// CarPlay's chat ended for the urgent call offers Continue; it is refused until the urgent call is over.
    @Test func carPlaysContinueAfterTheUrgentCallWaitsForItsEnd() async throws {
        let rig = CallRig()
        await rig.voiceCalls.start(rig.voice.key, extensionID: "voice", surface: .carPlay, deps: rig.voice.deps)
        let chat = try #require(rig.voiceCalls.current)
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call")
        rig.system.act(.join, id)
        await rig.voice.waitUntil { chat.phase.isEnded }
        #expect(chat.phase == .ended(.urgentCall))
        #expect(await rig.voiceCalls.resume(rig.voice.key) == .urgentCall)
        #expect(chat.phase == .ended(.urgentCall) && rig.voice.sockets.count == 1 && !rig.voice.audio.running)
        rig.calls.urgentEnded(id)
        #expect(await rig.voiceCalls.resume(rig.voice.key) == nil && chat.phase == .connecting)
    }

    /// An urgent call that rang while the call to Persona was being prepared (the hub asked, the microphone prompt):
    /// asked again just before the system's Start, and refused with the urgent call's note.
    @Test func anUrgentCallThatRangMeanwhileRefusesTheStart() async throws {
        let rig = CallRig()
        let call = rig.calls.add(kind: .owner(group: "g"), direction: .outgoing)
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Persona Urgent Call")
        await rig.calls.place(call, origin: .app)
        #expect(rig.system.starts.isEmpty)
        #expect(call.phase == .ended(.failed) && rig.calls.refusal == PersonaCalls.urgentNote && !rig.calls.presented)
    }

    /// Hold & Accept, the urgent call ended, the call to Persona resumed by the system, and no activation for it after
    /// (the urgent call had the session active): it stays on hold until its audio is back, and the recovery brings it
    /// back, its engine and its voice off hold.
    @Test func holdAndAcceptThenTheUrgentCallEndsAndTheCallToPersonaResumes() async throws {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call")
        // Hold & Accept: Pause for the call to Persona and Join for the urgent call, together.
        rig.system.act(.pause(true), call.id)
        rig.system.act(.join, id)
        rig.system.deactivate()
        #expect(call.held && call.voice?.held == true && call.phase.isLive)
        rig.system.activate()
        #expect(call.held, "the activation was the urgent call's")
        rig.system.act(.end, id)
        await rig.settle()
        #expect(rig.system.requests == [.pause(false)], "the system does not resume our own held call by itself")
        let sleeping = rig.voice.clock.sleeping
        rig.system.act(.pause(false), call.id)
        #expect(call.held && call.voice?.held == true && rig.voice.audio.heldNow, "on hold until its audio is back")
        await rig.voice.advance(1, sleepers: sleeping + 2)
        await rig.voice.waitUntil { rig.system.requests.count == 2 }
        await rig.voice.advance(0.5, sleepers: sleeping + 2)
        await rig.voice.waitUntil { !call.held }
        #expect(rig.system.activations == 1, "the session activated by the app, once")
        #expect(!call.held && call.voice?.held == false && !rig.voice.audio.heldNow && rig.voice.audio.active)
        rig.voice.audio.capture(.pcm(320, 5000))
        #expect(socket.audio.last.map { VoicePCM.samples($0).contains { $0 != 0 } } == true, "the owner is heard again")
    }

    /// The same, and the session would not activate either: on hold with Resume, which works.
    @Test func aResumeTheAudioDidNotFollowOffersAResumeThatWorks() async throws {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call")
        rig.system.act(.pause(true), call.id)
        rig.system.act(.join, id)
        rig.system.deactivate()
        rig.system.activate()
        rig.system.act(.end, id)
        await rig.settle()
        rig.system.activates = false
        let sleeping = rig.voice.clock.sleeping
        rig.system.act(.pause(false), call.id)
        await rig.voice.advance(1, sleepers: sleeping + 2)
        await rig.voice.advance(0.5, sleepers: sleeping + 2)
        await rig.voice.waitUntil { rig.system.activations == 1 }
        await rig.voice.advance(4, sleepers: sleeping + 1)
        await rig.voice.waitUntil { call.resumeOffered }
        #expect(call.resumeOffered && call.held && call.phase.isLive, "never ended, never connected and silent")
        rig.system.activates = true
        let waiting = rig.voice.clock.sleeping
        rig.calls.resume()
        await rig.settle()
        #expect(rig.system.requests.last == .pause(false))
        await rig.voice.advance(0.5, sleepers: waiting + 2)
        await rig.voice.waitUntil { !call.held }
        #expect(!call.held && !call.resumeOffered && call.voice?.held == false && rig.system.activations == 2)
    }

    /// The urgent call's notification tapped while a call to Persona is on, and the system would not answer it: the
    /// call to Persona goes on hold through the system first, and the urgent call talks only once the system let that
    /// call's audio go; its voice hears none of the urgent call, and resumes after it. Not answered here when the
    /// system would not hold the call to Persona.
    @Test func anUrgentCallAnsweredHereHoldsTheCallToPersonaFirst() async throws {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        let id = UUID()
        try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call")
        rig.system.refuses = [.join]
        let answering = Task {
            if await rig.calls.answer(id) { return true }
            return await rig.calls.answeredHere(id)
        }
        await rig.voice.waitUntil { rig.system.requests.contains(.pause(true)) }
        #expect(rig.calls.call(id)?.phase == .ringing, "not before the call to Persona is on hold")
        rig.system.act(.pause(true), call.id)
        rig.system.deactivate()
        #expect(await answering.value)
        #expect(rig.calls.call(id)?.connectedSince != nil)
        #expect(call.held && call.voice?.held == true && rig.voice.audio.heldNow && !rig.voice.audio.active)
        rig.voice.audio.capture(.pcm(320, 5000))
        #expect(socket.audio.last.map { VoicePCM.samples($0).allSatisfy { $0 == 0 } } == true, "the call's voice hears nothing")
        rig.calls.urgentEnded(id)
        await rig.settle()
        #expect(rig.system.requests.last == .pause(false), "it resumes after the urgent call")

        let refused = CallRig()
        let (other, _) = await refused.connected()
        let urgent = UUID()
        try await refused.calls.ring(urgent, callID: "call_2", name: "Persona Urgent Call")
        refused.system.refuses = [.join, .pause(true)]
        #expect(await !refused.calls.answer(urgent))
        #expect(await !refused.calls.answeredHere(urgent) && refused.calls.call(urgent)?.phase == .ringing)
        #expect(!other.held && other.voice?.held == false && refused.voice.audio.active, "the call to Persona goes on")
    }

    /// An urgent call the system would not show (Focus or Do Not Disturb refused the ring): nothing rings, so nothing
    /// is refused for it (a voice chat, a call to Persona) until it is answered from its notification.
    @Test func aRingTheSystemDidNotShowRefusesNothingUntilAnswered() async throws {
        let rig = CallRig()
        rig.system.ringError = CallSystemStandIn.refused
        let id = UUID()
        await #expect(throws: CallSystemStandIn.self) { try await rig.calls.ring(id, callID: "call_1", name: "Persona Urgent Call") }
        #expect(rig.calls.call(id)?.phase == .ringing && !rig.calls.urgentInProgress)
        #expect(await rig.voiceCalls.start(rig.voice.key, extensionID: "voice", surface: .phone, deps: rig.voice.deps) == nil)
        let chat = try #require(rig.voiceCalls.current)
        #expect(!chat.phase.isEnded)
        let categories = rig.categories
        #expect(await rig.calls.answeredHere(id))
        #expect(rig.calls.urgentInProgress && chat.phase == .ended(.urgentCall))
        #expect(rig.categories == categories + 1, "the call's category again: the voice chat set its own meanwhile")
    }

    @Test func anEndForACallItDoesNotHaveIsHarmless() {
        let rig = CallRig()
        rig.system.handler = rig.calls
        rig.system.act(.end, UUID())
        rig.system.act(.start, UUID())
        #expect(rig.system.fulfilled == [.end] && rig.system.failed == [.start])
    }

    @Test func whichVoiceEndingsAreFailures() {
        #expect(!VoiceEnd.idle.failsCall && !VoiceEnd.user.failsCall && !VoiceEnd.microphoneTaken.failsCall && !VoiceEnd.replaced.failsCall)
        #expect(VoiceEnd.lost.failsCall && VoiceEnd.audioFailed.failsCall && VoiceEnd.rejected(status: 401).failsCall)
        #expect(VoiceEnd.failed(code: nil, message: "x").failsCall && VoiceEnd.noHub.failsCall)
    }

    @Test func theSystemsErrorsAreReadByTheirDomain() {
        let wrapped = NSError(domain: "LCK", code: 1, userInfo: [NSUnderlyingErrorKey: NSError(domain: CallSystemError.requestTransaction, code: 6)])
        #expect(CallSystemError(wrapped).isTooEarly)
        #expect(!CallSystemError(NSError(domain: NSURLErrorDomain, code: 6)).isTooEarly)
    }

    @Test func aRedialFromRecentsIsACall() {
        #expect(PersonaCalls.isCall(NSUserActivity(activityType: "INStartCallIntent")))
        #expect(PersonaCalls.isCall(NSUserActivity(activityType: "INStartAudioCallIntent")))
        #expect(!PersonaCalls.isCall(NSUserActivity(activityType: "com.apple.corespotlightitem")))
    }

    #if DEBUG
    @Test func everySampleShowsWithoutTheSystem() {
        let rig = CallRig()
        for sample in PersonaCalls.Sample.allCases {
            rig.calls.showSample(sample)
            #expect(rig.calls.presented)
        }
        rig.calls.showSample(.resume)
        #expect(rig.calls.ownerCall?.resumeOffered == true)
        rig.calls.showSample(.held)
        #expect(rig.calls.ownerCall?.holdReason == .system)
        rig.calls.showSample(.connected)
        #expect(rig.calls.ownerCall?.connectedSince == rig.voice.clock.now.addingTimeInterval(-42))
        #expect(!rig.calls.systemMade && rig.system.reports.isEmpty)
    }

    /// `-showUrgentCallSample held`: the urgent call behind the page on hold with Resume, no page of a call to Persona, and
    /// nothing to the system, Hang Up included.
    @Test func theHeldUrgentSampleOffersResumeWithoutTheSystem() throws {
        let rig = CallRig()
        let id = UUID()
        rig.calls.showUrgentSample(id, callID: "call_mock")
        let call = try #require(rig.calls.call(id))
        #expect(call.kind == .urgent(callID: "call_mock") && call.held && call.resumeOffered)
        #expect(rig.calls.ownerCall == nil && !rig.calls.presented)
        rig.calls.hangUp(id)
        #expect(rig.calls.call(id) == nil)
        #expect(!rig.calls.systemMade && rig.system.reports.isEmpty && rig.system.requests.isEmpty && rig.system.activations == 0)
    }
    #endif
}

nonisolated enum CallSystemStandIn: Error {
    case refused
}
