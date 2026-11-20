import AVFoundation
import Foundation
import ImageIO
import SwiftUI
import Testing
import UIKit
import UniformTypeIdentifiers
import WebKit
@testable import Persona

/// The call is the assistant's, and nothing in the app ends it (#9290 follow-up, docs/apple/calls.md): the system call
/// and the pages carry the assistant's name, the call screen shows its look, the call's window shows the bar and the
/// screen over everything, the system call UI gets a template icon, and every in-app action that would take the call's
/// microphone or audio asks first or is refused, the call untouched.
@MainActor
struct CallFollowUpTests {
    // MARK: The assistant's name

    /// The handle's value (what the system's call UI, the Lock Screen and Recents show) is the assistant's name, for a
    /// call to Persona and for an urgent call (whose own name stays the display name only); "Persona" when it has none.
    @Test func theSystemCallCarriesTheAssistantsName() async throws {
        let rig = CallRig()
        rig.assistant = "奶蛙"
        let call = await rig.dial()
        #expect(call.name == "奶蛙" && rig.system.startNames == ["奶蛙"])
        let urgent = UUID()
        try await rig.calls.ring(urgent, callID: "call_1", name: "Urgent: the build broke")
        let ring = try #require(rig.system.rings.last)
        #expect(ring.name == "奶蛙" && ring.title == "Urgent: the build broke")

        let unnamed = CallRig()
        unnamed.assistant = "  "
        _ = await unnamed.dial()
        #expect(unnamed.system.startNames == ["Persona"])
        #expect(CallHandle.name(nil) == "Persona" && CallHandle.name(" Mochi ") == "Mochi")
    }

    /// A redial with any value calls Persona: the activity is a call whatever its handle says.
    @Test func aRedialWithAnyNameIsACall() {
        let activity = NSUserActivity(activityType: "INStartCallIntent")
        #expect(PersonaCalls.isCall(activity))
        #expect(PersonaCalls.handle(activity) == nil)
    }

    // MARK: The guards

    /// The app's guards follow the live call: from "Calling…" to its end (not a DEBUG sample), the assistant's name for
    /// the questions, and the call's own conversation (its handset shows the call screen).
    @Test func theGuardsFollowTheLiveCall() async {
        let rig = CallRig()
        rig.assistant = "Mochi"
        var changes: [Bool] = []
        rig.calls.onLiveChange = { changes.append($0) }
        #expect(!rig.calls.guardsLive && rig.calls.liveOwnerCallName == nil)
        let (call, _) = await rig.connected()
        #expect(rig.calls.guardsLive && rig.calls.liveOwnerCallName == "Mochi" && changes == [true])
        #expect(rig.calls.isOwnerCallConversation(rig.voice.key))
        #expect(!rig.calls.isOwnerCallConversation(SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s2")))
        rig.calls.hangUp()
        rig.system.act(.end, call.id)
        #expect(call.phase.isEnded && !rig.calls.guardsLive && rig.calls.liveOwnerCallName == nil && changes == [true, false])
        #expect(!rig.calls.isOwnerCallConversation(rig.voice.key))
    }

    /// An urgent call ringing counts once the system shows it; one Focus refused counts only once answered.
    @Test func anUrgentCallGuardsOnceItRings() async throws {
        let rig = CallRig()
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        #expect(rig.calls.guardsLive && rig.calls.liveOwnerCallName == nil, "an urgent call asks no voice chat question: it refuses")
        let quiet = CallRig()
        quiet.system.ringError = CallSystemStandIn.refused
        await #expect(throws: CallSystemStandIn.self) { try await quiet.calls.ring(UUID(), callID: "call_2", name: "Urgent") }
        #expect(!quiet.calls.guardsLive)
    }

    /// A voice chat elsewhere is refused while a call to Persona is on, touching nothing, until the user says to end the
    /// call (`endingCall`); Continue the same. CarPlay's never says it: it is told the call is on the phone.
    @Test func noVoiceChatOverACallToPersonaUnlessTheUserEndsIt() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        var name: String? = nil
        var ended = 0
        calls.systemCallName = { name }
        calls.endSystemCall = { ended += 1; name = nil }
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let first = try #require(calls.current)
        first.end(.interrupted)
        name = "Mochi"
        ended = 0 // the first start asked for no call to end: there was none
        let other = SessionKey(group: rig.key.group, machineID: "m2", sessionID: "s9")
        for surface in [VoiceSurface.phone, .carPlay] {
            #expect(await calls.start(other, extensionID: "voice", surface: surface, deps: rig.deps) == .systemCallOn("Mochi"))
        }
        #expect(await calls.resume(rig.key) == .systemCallOn("Mochi"))
        #expect(ended == 0 && calls.current === first && rig.sockets.count == 1, "nothing was touched")
        #expect(await calls.resume(rig.key, endingCall: true) == nil)
        #expect(ended == 1 && first.phase == .connecting && rig.sockets.count == 2)
    }

    /// The question's words and what its yes does.
    @Test func theQuestionNamesTheAssistantAndWhatItsYesDoes() {
        let key = SessionKey(group: "g", machineID: "m", sessionID: "s")
        let start = VoiceUnavailableNote.endCallFirst("Mochi", key: key)
        #expect(start.title.contains("Mochi") && start.proceed == .startVoice(key) && start.settings == nil)
        #expect(VoiceUnavailableNote.endCallFirst("Mochi", key: key, again: true).proceed == .resumeVoice(key))
        #expect(VoiceUnavailableNote.systemCallOn("Mochi").proceed == nil)
        #expect(VoiceUnavailableNote.urgentCall.proceed == nil, "a refusal stays a refusal")
    }

    /// Dictation is refused at once while a call is on: it says why, and nothing opens (the tap's bar, the hold, the
    /// microphone); the call manager's answer is what the gate asks.
    @Test func dictationIsRefusedOnACall() async {
        let note = "On a call"
        let dictation = Dictation()
        dictation.callGate = { note }
        var failures: [String] = []
        dictation.start(update: { _ in }, failed: { failures.append($0) })
        #expect(failures == [note] && !dictation.isListening)

        let bar = DictationBarModel()
        bar.start(dictation, hints: [], reduceMotion: true)
        #expect(!bar.on && AppModel.shared.toast?.message == note, "the bar opened")
        let hold = ComposerHold()
        var text = "draft"
        hold.begin(dictation, text: Binding(get: { text }, set: { text = $0 }), reduceMotion: true)
        #expect(!hold.holding && text == "draft")

        let free = Dictation()
        free.callGate = { nil }
        #expect(free.callRefusal == nil)
    }

    /// A recording is refused while a call is on, before anything is asked or opened; the call manager answers.
    @Test func aRecordingIsRefusedOnACall() async {
        let capture = RecordingCapture.shared
        guard capture.state == .idle else { return }
        let gate = capture.callGate
        capture.callGate = { "End the call first" }
        defer { capture.callGate = gate }
        await capture.start(locale: "en-US")
        #expect(capture.state == .idle && capture.error == "End the call first")
        let outcome = await MeetingRecorder.shared.startMeeting(inForeground: false, openPage: false)
        #expect(outcome == .failed("End the call first") && capture.state == .idle)
        capture.error = nil
    }

    /// A replay is refused while a call is on, before its tape, recording state or audio opens. Without a call the same
    /// tape really plays its fixture audio and End lets the session and the replayed recording go (#10693).
    @Test func aMeetingReplayRefusesOnACallAndPlaysWithoutOne() async throws {
        let capture = RecordingCapture.shared
        let replay = MeetingReplay.shared
        guard capture.state == .idle, !replay.running else {
            print("MeetingReplayCallGate: SKIPPED because the shared recorder is occupied")
            return
        }
        print("MeetingReplayCallGate: precondition held")
        let gate = capture.callGate
        var restored = false
        defer {
            if !restored { capture.callGate = gate; capture.error = nil }
        }
        let restoreGate = {
            capture.callGate = gate
            capture.error = nil
            restored = true
        }
        var tape = MeetingTape(title: "Replay call gate", startedAt: .now)
        tape.endedAfter = 30
        let audio = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appending(path: "Fixtures/meeting-tape/audio.m4a")

        capture.callGate = { "End the call first" }
        let startedOnCall = replay.play(tape, audioFile: audio)
        let refused = !startedOnCall && replay.audio == nil && capture.state == .idle && capture.recordingID == nil
        if startedOnCall {
            _ = capture.stop()
            restoreGate()
            await Self.waitUntil { !replay.running }
            replay.clearEnded()
        }
        try #require(refused, "a live call did not refuse the replay cleanly")
        print("MeetingReplayCallGate: live-call refusal passed")

        capture.callGate = { nil }
        let started = replay.play(tape, audioFile: audio)
        var played = false
        var stopped = false
        if started {
            await Self.waitUntil { replay.audio?.isPlaying == true }
            played = replay.audio?.isPlaying == true
            _ = capture.stop()
            // stopWithoutMicrophone resets the replay's recorder synchronously; another suite may use it after the await.
            stopped = capture.state == .idle && capture.recordingID == nil
            restoreGate()
            await Self.waitUntil { !replay.running }
        }
        let cleaned = stopped && !replay.running && replay.audio == nil && capture.recordingID != MeetingReplay.id
        replay.clearEnded()
        try #require(started, "ordinary replay did not start")
        try #require(played, "ordinary replay audio did not play")
        try #require(stopped, "ordinary replay did not stop its recording")
        try #require(cleaned, "ordinary replay did not finish cleanly")
        print("MeetingReplayCallGate: ordinary playback and cleanup passed")
    }

    /// The gate's words, while the call manager says a call is on.
    @Test func theGateSaysWhyWhileACallIsOn() {
        let live = LiveCallGate.isLive
        LiveCallGate.isLive = { true }
        let dictation = LiveCallGate.dictationRefusal, recording = LiveCallGate.recordingRefusal, on = LiveCallGate.callIsLive
        LiveCallGate.isLive = live
        #expect(on && dictation == String(localized: "On a call — dictation is off until it ends"))
        #expect(recording == String(localized: "End the call first"))
    }

    /// Web pages' media is suspended while a call is live, also in a page made during it, and let go after.
    @Test func webMediaWaitsForTheCall() {
        let guardian = CallMediaGuard()
        let before = WKWebView()
        guardian.register(before)
        guardian.callLive(true)
        #expect(guardian.suspended)
        guardian.register(WKWebView())
        guardian.callLive(false)
        #expect(!guardian.suspended)
    }

    /// A system call keeps its microphone: a recording or dictation that asks while it is on is refused and the call goes
    /// on (an in-app voice chat still gives way: `VoiceConversationTests.aRecordingTakesTheMicrophoneFromTheCall`).
    @Test func aSystemCallKeepsItsMicrophone() async {
        let rig = CallRig()
        let (call, socket) = await rig.connected()
        #expect(!rig.voice.microphone.claim(UUID()), "something took the call's microphone")
        #expect(call.phase.isLive && !socket.closed && call.voice?.phase.isEnded == false)
    }

    // MARK: The call screen and its window

    /// The assistant's look follows the call (each row in `CallAvatarStatesTests`): the shared speaking motion while it
    /// speaks, its working picture while its conversation works, idling while it listens, still on hold, muted and over,
    /// the ring and a mark saying which.
    @Test func theAssistantsLookFollowsTheCall() {
        func look(_ row: CallAvatarLook.Row, muted: Bool = false) -> CallAvatarLook {
            CallAvatarLook.make(row, muted: muted, kind: .builtin, motion: true)
        }
        #expect(look(.speaking).state == .idle && look(.speaking).bounces && look(.speaking).ring == .speaking)
        #expect(look(.working).state == .working)
        #expect(look(.listening) == CallAvatarLook(state: .idle, still: false, ring: .voice, mark: nil, clock: true))
        #expect(look(.hearing).followsLevel)
        #expect(look(.connecting) == CallAvatarLook(state: .idle, still: false, ring: .lap, mark: nil, clock: true))
        #expect(look(.held) == CallAvatarLook(state: .idle, still: true, ring: .held, mark: .hold, eyesShut: true))
        #expect(look(.muted, muted: true) == CallAvatarLook(state: .idle, still: true, ring: .quiet, mark: .muted))
        #expect(look(.ended) == CallAvatarLook(state: .offline, still: true, ring: .ended, mark: nil, greyed: true))
        for row in CallAvatarLook.Row.allCases where look(row).still {
            #expect(!look(row).clock, "\(row): a still look's ring moves")
        }
    }

    /// The window shows the call screen while it is presented, the bar while the call is live and minimised, a refusal's
    /// alert, and nothing under an answered urgent call's page or with no call.
    @Test func theCallWindowShowsWhatTheCallIs() async {
        let rig = CallRig()
        #expect(CallWindowMode.make(presented: false, owner: nil, refusal: false, urgentUp: false) == .none)
        #expect(CallWindowMode.make(presented: false, owner: nil, refusal: true, urgentUp: false) == .refusal)
        let call = await rig.dial()
        #expect(CallWindowMode.make(presented: true, owner: call, refusal: false, urgentUp: false) == .screen)
        #expect(CallWindowMode.make(presented: false, owner: call, refusal: false, urgentUp: false) == .bar)
        #expect(CallWindowMode.make(presented: true, owner: call, refusal: false, urgentUp: true) == .none)
        #expect(CallWindowMode.make(presented: false, owner: call, refusal: false, urgentUp: true) == .none)
        rig.calls.hangUp()
        rig.system.act(.end, call.id)
        #expect(CallWindowMode.make(presented: false, owner: call, refusal: false, urgentUp: false) == .none, "no bar for an ended call")
    }

    /// The room a page or sheet gets under the bar: what reaches past its content's top, with a small gap; none for a
    /// sheet lower down.
    @Test func theBarsRoomIsWhatThePageUnderItNeeds() {
        let status: CGFloat = 62, bar: CGFloat = 48
        #expect(CallWindow.room(contentTop: status, barBottom: status + bar) == bar + Theme.Space.xs)
        #expect(CallWindow.room(contentTop: status + 10, barBottom: status + bar) == bar - 10 + Theme.Space.xs, "a page sheet")
        #expect(CallWindow.room(contentTop: 300, barBottom: status + bar) == 0, "a sheet at half height")
    }

    /// An alert or an action sheet gets no room under the bar: its presentation covers the whole screen, but its box
    /// floats clear of the bar, and room at its top pushed the voice chat's question down under its own buttons.
    @Test func anAlertGetsNoRoomUnderTheBar() {
        #expect(CallWindow.takesRoom(UIViewController()))
        #expect(CallWindow.takesRoom(UIHostingController(rootView: Text(verbatim: "sheet"))))
        #expect(!CallWindow.takesRoom(UIAlertController(title: "t", message: "m", preferredStyle: .alert)))
        #expect(!CallWindow.takesRoom(UIAlertController(title: "t", message: "m", preferredStyle: .actionSheet)))
        let bar = CGRect(x: 100, y: 62, width: 200, height: 48)
        #expect(CallWindow.room(for: UIAlertController(title: "t", message: "m", preferredStyle: .alert), under: bar) == 0)
    }

    /// The pages' windows under the call's get the bar's room, and so does the toasts' above it (a toast sits just below
    /// the top bars, which the room moved down: the dictation refusal's note lay on an Agent conversation's title); a
    /// system window above it (the keyboard's, an alert's) gets none.
    @Test func theToastsGetTheBarsRoomAndTheSystemsWindowsNone() {
        final class Toasts: UIWindow, TakesCallBarRoom {}
        let shell = UIWindow(frame: .zero)
        let panel = UIWindow(frame: .zero)
        panel.windowLevel = .normal + 0.25
        let toasts = Toasts(frame: .zero)
        toasts.windowLevel = .normal + 1
        let keyboard = UIWindow(frame: .zero)
        keyboard.windowLevel = .normal + 1
        let alert = UIWindow(frame: .zero)
        alert.windowLevel = .alert
        #expect(CallWindow.givesRoom(to: shell) && CallWindow.givesRoom(to: panel))
        #expect(CallWindow.givesRoom(to: toasts))
        #expect(!CallWindow.givesRoom(to: keyboard) && !CallWindow.givesRoom(to: alert))
    }

    /// The call screen's middle sizes the assistant as it sized the orb: its share of the page, then what the words
    /// leave, down to the voice bar's orb's size.
    @Test func theAssistantGivesWayToTheWords() throws {
        #expect(CallScreen.avatarSize(page: 900, free: .infinity, scale: 1) == Theme.Size.callAvatar)
        #expect(CallScreen.avatarSize(page: 900, free: 90, scale: 1) == 90)
        #expect(CallScreen.avatarSize(page: 900, free: Theme.Size.voiceOrb - 1, scale: 1) == nil)
    }

    // MARK: The system call UI's icon

    /// A valid PNG with an alpha channel, 40 pt at 3×: clear around Mochi and in its eyes, solid in its body.
    @Test func theTemplateIconIsAnAlphaSilhouette() throws {
        let data = try #require(CallIcon.templatePNG())
        #expect(data.prefix(8) == Data([0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A]), "not a PNG")
        let source = try #require(CGImageSourceCreateWithData(data as CFData, nil))
        let image = try #require(CGImageSourceCreateImageAtIndex(source, 0, nil))
        #expect(image.width == 120 && image.height == 120)
        #expect(![.none, .noneSkipFirst, .noneSkipLast].contains(image.alphaInfo), "no alpha")
        let alpha = try Self.alpha(image)
        func at(_ x: Double, _ y: Double) -> UInt8 { alpha[Int(y * 119) * 120 + Int(x * 119)] }
        #expect(at(0, 0) == 0 && at(1, 1) == 0, "the corners are not clear")
        #expect(at(0.5, 0.62) == 255, "Mochi's body is not solid")
        let eye = Mochi.eyes[0]
        #expect(at(eye.x, eye.y) == 0, "the eye is not cut out")
        #expect(alpha.contains(0) && alpha.contains(255))
    }

    /// Drawn off the main actor, once, before the system's side is made with it: the manager gets finished data and
    /// draws nothing on the main actor (#9290 follow-up, second review).
    @Test func theIconIsReadyBeforeTheManagerAndDrawnOffTheMainActor() async throws {
        await CallIcon.prepare()
        #expect(CallIcon.drawnOnMainThread == false, "drawn on the main thread")
        let data = try #require(CallIcon.ready)
        #expect(data.prefix(4) == Data([0x89, 0x50, 0x4E, 0x47]))
        let rig = CallRig()
        _ = await rig.dial()
        #expect(rig.systemSteps == ["prepared", "made"], "the system's side was made before its icon was ready")
        let ringing = CallRig()
        try await ringing.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        #expect(ringing.systemSteps == ["prepared", "made"])
    }

    // MARK: The second review's findings

    /// Disconnecting a hub a live call talks through is refused with the call's name, touching nothing; another group's
    /// goes ahead; with the call over, it may (#1).
    @Test func aDisconnectWaitsForTheCallThroughItsHub() async throws {
        let rig = CallRig()
        rig.assistant = "Mochi"
        let (call, socket) = await rig.connected()
        let group = rig.voice.key.group
        let refusal = try #require(rig.calls.disconnectRefusal(groups: [group]))
        #expect(refusal.contains("Mochi"))
        #expect(rig.calls.disconnectRefusal(groups: nil) == refusal, "Disconnect All")
        #expect(rig.calls.disconnectRefusal(groups: ["another"]) == nil, "another group's hub")
        #expect(call.phase.isLive && !socket.closed)
        // An urgent call whose group is not known yet counts for every group.
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        #expect(rig.calls.disconnectRefusal(groups: ["another"]) != nil)

        let over = CallRig()
        let (ended, _) = await over.connected()
        over.calls.hangUp()
        over.system.act(.end, ended.id)
        #expect(over.calls.disconnectRefusal(groups: nil) == nil)
    }

    /// Settings' Disconnect asks before it revokes or forgets anything: refused, it says why and starts nothing.
    @Test func settingsDisconnectIsRefusedDuringACall() async {
        let model = AppModel()
        let gate = LiveCallGate.disconnectRefusal
        var asked: [Set<String>?] = []
        LiveCallGate.disconnectRefusal = { groups in
            asked.append(groups)
            return "On a call"
        }
        defer { LiveCallGate.disconnectRefusal = gate }
        await model.disconnectAll()
        #expect(model.toast?.message == "On a call" && model.hubSignOut == nil)
        await model.disconnectGroup("g")
        #expect(asked == [nil, ["g"]], "Disconnect All asks for every group, a group's for its own")
        LiveCallGate.disconnectRefusal = { _ in nil }
        #expect(model.disconnectRefusal([], scope: .all) == nil)
    }

    /// The call's look and name go together: taken when the call is made and again just before the system's Start,
    /// then fixed; choosing another assistant meanwhile changes neither (#10).
    @Test func theCallKeepsTheAssistantItWasMadeWith() async throws {
        let rig = CallRig()
        rig.persona = CallRig.persona(named: "Persona")
        let call = rig.calls.add(kind: .owner(group: rig.voice.key.group), direction: .outgoing)
        rig.attachEngine(call)
        // The look came (a cold launch's download) while the call was still being placed: taken before the Start.
        rig.persona = CallRig.persona(named: "奶蛙")
        await rig.calls.place(call, origin: .app)
        #expect(rig.system.startNames == ["奶蛙"] && call.name == "奶蛙" && call.assistant.persona.name == "奶蛙")
        // Another assistant chosen during the call: the call's face and name stay the ones the system has.
        var other = CallRig.persona(named: "Mochi")
        other.id = "another"
        rig.persona = other
        rig.system.act(.start, call.id)
        #expect(call.assistant.persona.name == "奶蛙" && call.assistant.persona.id == Persona.builtinID)
        #expect(call.name == "奶蛙")
    }

    /// A package's frames and face are read and decoded off the main actor into the call's assistant, each file once;
    /// a look taken again drops the old pictures (#12).
    @Test func theCallsPicturesAreDecodedOffTheMainActor() async throws {
        let root = FileManager.default.temporaryDirectory.appending(path: "call-look-\(UUID().uuidString)")
        defer { try? FileManager.default.removeItem(at: root) }
        let suite = "call-look-\(UUID().uuidString)"
        let defaults = try #require(UserDefaults(suiteName: suite))
        defer { defaults.removePersistentDomain(forName: suite) }
        let store = PersonaStore(root: root, defaults: defaults)
        let manifest = try JSONDecoder().decode(PersonaManifest.self, from: Data(#"""
            {"format":1,"id":"frog","name":"奶蛙","avatar":{"sizes":{"64":"face.png"}},"dot":{"states":{"idle":{"file":"idle.png"}}}}
            """#.utf8))
        let frog = Persona(id: "frog", name: "奶蛙", subtitle: nil, rev: "r1", manifest: manifest)
        let folder = store.directory(frog)
        try FileManager.default.createDirectory(at: folder, withIntermediateDirectories: true)
        try Self.png(side: 16).write(to: folder.appending(path: "idle.png"))
        try Self.png(side: 64).write(to: folder.appending(path: "face.png"))

        let frames = await store.decodedFrames(frog, CallAssistant.states)
        #expect(Set(frames.keys) == Set(CallAssistant.states), "a state the package lacks shows idle's")
        #expect(frames[.idle] === frames[.working], "one file decoded once")
        let face = await store.decodedFace(frog, points: 32, scale: 2)
        #expect(face?.size.width == 64)
        #expect(await store.decodedFrames(.builtin, CallAssistant.states).isEmpty)

        let assistant = CallAssistant(frog)
        await assistant.prepareFrames(store)
        await assistant.prepareFace(store, points: 32, scale: 2)
        #expect(assistant.frames[.offline] != nil && assistant.face != nil)
        assistant.retake(.builtin)
        #expect(assistant.frames.isEmpty && assistant.face == nil && assistant.name == "Persona")
        await assistant.prepareFrames(store)
        #expect(assistant.frames.isEmpty, "a built-in is drawn, not read")
    }

    /// The question's yes when the call already ended by itself (AirPods, the system's call UI) while it was up: no
    /// hang-up is asked, but the voice chat still waits for the system to let the ended call's audio go (#5).
    @Test func anEndedCallsAudioIsStillWaitedForBeforeTheVoiceChat() async {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        rig.system.act(.end, call.id)
        #expect(call.phase.isEnded)
        let other = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s2")
        let sockets = rig.voice.sockets.count
        let starting = Task {
            await rig.voiceCalls.start(other, extensionID: "voice", surface: .phone, deps: rig.voice.deps, endingCall: true)
        }
        try? await Task.sleep(for: .milliseconds(150))
        #expect(rig.voice.sockets.count == sockets, "the voice chat opened its audio before the ended call's went")
        #expect(!rig.system.requests.contains(.end), "an ended call is not hung up again")
        rig.system.deactivate()
        #expect(await starting.value == nil)
        #expect(rig.voice.sockets.count == sockets + 1 && rig.voiceCalls.current?.surface == .phone)
    }

    /// End Call and Start, then an urgent call rings while the call's audio goes: the voice chat does not start, and
    /// the refusal comes back to whoever asked (the question's yes awaits it: `VoiceEntry.proceed`) (#6, #11).
    @Test func anUrgentCallDuringTheHandOffIsSaidToTheQuestion() async throws {
        let rig = CallRig()
        let (call, _) = await rig.connected()
        let other = SessionKey(group: rig.voice.key.group, machineID: "m2", sessionID: "s2")
        let starting = Task {
            await rig.voiceCalls.start(other, extensionID: "voice", surface: .phone, deps: rig.voice.deps, endingCall: true)
        }
        await rig.voice.waitUntil { rig.system.requests.contains(.end) }
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        rig.system.act(.end, call.id)
        rig.system.deactivate()
        #expect(await starting.value == .urgentCall)
        #expect(rig.voice.sockets.count == 1, "the voice chat started under the urgent call")
    }

    /// Dictation waiting for its permission when a call goes live is refused then, before the microphone or the
    /// session: the call manager's voice does not claim `RecordingMicrophone`, so only the gate can say no (#4, #9).
    @Test func dictationAsksTheCallAgainAfterItsPermission() async {
        let dictation = Dictation()
        dictation.sample = "one two"
        let live = LiveFlag()
        dictation.callGate = { live.on ? "On a call" : nil }
        let permission = HeldPermission()
        dictation.permission = { await permission.ask() }
        var failures: [String] = []
        var heard = 0
        dictation.start(update: { _ in heard += 1 }, failed: { failures.append($0) })
        await Self.waitUntil { permission.asked }
        live.on = true // an urgent call answered meanwhile
        permission.answer()
        await Self.waitUntil { !failures.isEmpty }
        try? await Task.sleep(for: .milliseconds(400)) // a sample's first word would come by now
        #expect(failures == ["On a call"] && heard == 0 && !dictation.isListening)
        #expect(Self.microphoneIsFree())
    }

    /// A recording paused before an urgent call was answered: Persona speaking or not, Continue, and Stop on its Live
    /// Activity all leave the call's session as it is (no category, no deactivation, no preferred input); the recording
    /// still stops and is saved, and its microphone goes back (#2, #8). No suspension while it records: other suites'
    /// tests use the one recorder too.
    @Test func aPausedRecordingStopsUnderACallLeavingItsSession() async throws {
        let capture = RecordingCapture.shared
        guard capture.state == .idle else { return }
        let rig = try RecorderRig(capture)
        defer { rig.restore() }
        rig.useFile()
        await capture.start(locale: "en-US") // no wait with a file: it records when this returns
        try #require(capture.state == .recording, "\(capture.error ?? "")")
        capture.togglePause()
        #expect(capture.state == .paused)
        rig.log.calls = []
        rig.live.on = true // an urgent call answered: the recording stays paused
        capture.setPlaysAloud(true) // Persona told to speak (the page's switch): its category waits for the resume
        capture.togglePause() // Continue: refused
        #expect(capture.state == .paused && capture.error == "End the call first")
        let id = try #require(capture.stop()) // Stop on its Live Activity
        #expect(capture.state == .idle && rig.saved == [id])
        #expect(rig.log.calls.isEmpty, "the call's session was touched: \(rig.log.calls)")
        #expect(Self.microphoneIsFree())
    }

    /// A start with a Bluetooth microphone still being looked for when an urgent call is answered: it stops there,
    /// sets the phone's category on nothing and opens no microphone, and leaves the call's session as it is (#3).
    @Test func aCallDuringTheBluetoothSearchStopsTheStart() async throws {
        let capture = RecordingCapture.shared
        guard capture.state == .idle else { return }
        let rig = try RecorderRig(capture)
        defer { rig.restore() }
        rig.useFile()
        rig.log.onActivate = { rig.live.on = true }
        await capture.start(locale: "en-US", input: .bluetooth)
        #expect(capture.state == .idle && capture.recordingID == nil)
        #expect(capture.error == "End the call first")
        #expect(rig.log.calls.filter { $0.hasPrefix("category") }.count == 1, "the phone's category under the call: \(rig.log.calls)")
        #expect(!rig.log.calls.contains("deactivate") && !rig.log.calls.contains { $0.hasPrefix("input") }, "\(rig.log.calls)")
        #expect(Self.microphoneIsFree())
    }

    /// A call answered while the microphone was asked for: the start is refused then, nothing set up (#3).
    @Test func aCallDuringTheMicrophonePromptStopsTheStart() async throws {
        let capture = RecordingCapture.shared
        guard capture.state == .idle else { return }
        let rig = try RecorderRig(capture)
        defer { rig.restore() }
        capture.permission = { [live = rig.live] in
            live.on = true
            return true
        }
        await capture.start(locale: "en-US")
        #expect(capture.state == .idle && capture.error == "End the call first")
        #expect(rig.log.calls.isEmpty && Self.microphoneIsFree())
    }

    /// The room under the bar is watched only while the bar shows and the app is in front (#13).
    @Test func theBarsRoomIsWatchedOnlyInFront() {
        #expect(CallWindow.watchesRoom(bar: true, active: true))
        #expect(!CallWindow.watchesRoom(bar: true, active: false), "the watch runs in the background")
        #expect(!CallWindow.watchesRoom(bar: false, active: true))
    }

    // MARK: Helpers

    private static func waitUntil(_ condition: () -> Bool) async {
        for _ in 0..<300 where !condition() { try? await Task.sleep(for: .milliseconds(10)) }
    }

    private static func microphoneIsFree() -> Bool {
        let probe = UUID()
        defer { RecordingMicrophone.shared.release(probe) }
        return RecordingMicrophone.shared.claim(probe)
    }

    /// A gray square PNG `side` pixels wide.
    static func png(side: Int) throws -> Data {
        let ctx = try #require(CGContext(data: nil, width: side, height: side, bitsPerComponent: 8, bytesPerRow: 0,
                                         space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        ctx.setFillColor(CGColor(gray: 0.5, alpha: 1))
        ctx.fill(CGRect(x: 0, y: 0, width: side, height: side))
        let image = try #require(ctx.makeImage())
        let data = NSMutableData()
        let out = try #require(CGImageDestinationCreateWithData(data, UTType.png.identifier as CFString, 1, nil))
        CGImageDestinationAddImage(out, image, nil)
        #expect(CGImageDestinationFinalize(out))
        return data as Data
    }

    /// The image's alpha, row by row from the top.
    private static func alpha(_ image: CGImage) throws -> [UInt8] {
        var out = [UInt8](repeating: 0, count: image.width * image.height)
        let ctx = try #require(CGContext(data: &out, width: image.width, height: image.height, bitsPerComponent: 8,
                                         bytesPerRow: image.width, space: CGColorSpaceCreateDeviceGray(),
                                         bitmapInfo: CGImageAlphaInfo.alphaOnly.rawValue))
        ctx.draw(image, in: CGRect(x: 0, y: 0, width: image.width, height: image.height))
        return out
    }
}

/// Whether a call is live, as a test's gate says it.
@MainActor final class LiveFlag {
    var on = false
}

/// The system's question for the microphone, held open until the test answers it.
@MainActor final class HeldPermission {
    private var waiting: CheckedContinuation<Bool, Never>?
    private(set) var asked = false

    func ask() async -> Bool {
        asked = true
        return await withCheckedContinuation { waiting = $0 }
    }

    func answer(_ granted: Bool = true) {
        waiting?.resume(returning: granted)
        waiting = nil
    }
}

/// `RecordingCapture.shared` on a test's gate, session and permission, with a file in place of the microphone when
/// asked; `restore` puts the app's back.
@MainActor final class RecorderRig {
    let capture: RecordingCapture
    let live = LiveFlag()
    let log = SessionLog()
    private(set) var saved: [UUID] = []
    private let gate: () -> String?
    private let session: RecordingSession
    private let onSaved: ((UUID) -> Void)?
    private let onStopping: ((UUID) -> Void)?
    private let file: URL

    init(_ capture: RecordingCapture) throws {
        self.capture = capture
        gate = capture.callGate
        session = capture.session
        onSaved = capture.onSaved
        onStopping = capture.onStopping
        file = FileManager.default.temporaryDirectory.appending(path: "call-recording-\(UUID().uuidString).caf")
        try Self.silence(file)
        capture.callGate = { [live] in live.on ? "End the call first" : nil }
        capture.session = log.session
        capture.onSaved = { [weak self] in self?.saved.append($0) }
        capture.onStopping = { _ in }
    }

    /// A file in place of the microphone (DEBUG `-openMeetingAudio`): no permission, no input.
    func useFile() {
        UserDefaults.standard.set(file.path, forKey: RecordingFileInput.key)
    }

    func restore() {
        UserDefaults.standard.removeObject(forKey: RecordingFileInput.key)
        if capture.state != .idle { capture.stop() }
        capture.callGate = gate
        capture.session = session
        capture.onSaved = onSaved
        capture.onStopping = onStopping
        capture.permission = nil
        capture.error = nil
        for id in saved { try? RecordingLibrary.shared.remove(id) }
        try? FileManager.default.removeItem(at: file)
    }

    /// Two seconds of silence, 16 kHz mono.
    private static func silence(_ url: URL) throws {
        let format = try #require(AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: 16000, channels: 1, interleaved: false))
        let file = try AVAudioFile(forWriting: url, settings: format.settings)
        let buffer = try #require(AVAudioPCMBuffer(pcmFormat: format, frameCapacity: 32000))
        buffer.frameLength = 32000
        try file.write(from: buffer)
    }
}

/// The audio session calls a recording made (`RecordingSession`), in order.
@MainActor final class SessionLog {
    var calls: [String] = []
    var onActivate: () -> Void = {}

    var session: RecordingSession {
        RecordingSession(setCategory: { [weak self] _, _, options in self?.calls.append("category \(options.rawValue)") },
                         activate: { [weak self] in
                             self?.calls.append("activate")
                             self?.onActivate()
                         },
                         deactivate: { [weak self] in self?.calls.append("deactivate") },
                         availableInputs: { [] },
                         setPreferredInput: { [weak self] port in self?.calls.append(port == nil ? "input none" : "input") })
    }
}
