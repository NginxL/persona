import Foundation
import Testing
@testable import Persona

/// Who owns the app's one call (docs/assistant/voice.md 6.3, 6.5, 6.10「VoiceCalls 的所有权」): one at a time, another
/// conversation's call ends the one before; the background and leaving the page end none on the iPhone (#9887).
@MainActor
struct VoiceCallsTests {
    @Test func oneCallAtATimeAndAnotherConversationEndsTheFirst() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let first = try #require(calls.current)
        #expect(calls.isBound(to: rig.key))

        // The same conversation again: the same call, no second socket.
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        #expect(calls.current === first && rig.sockets.count == 1)

        let other = SessionKey(group: rig.key.group, machineID: "m2", sessionID: "s9")
        await calls.start(other, extensionID: "voice", surface: .phone, deps: rig.deps)
        #expect(first.phase == .ended(.replaced))
        #expect(calls.current !== first && calls.isBound(to: other) && !calls.isBound(to: rig.key))
        #expect(rig.sockets.count == 2 && rig.sockets[0].closed)
        #expect(rig.sockets[1].start?["target"] as? [String: String] == ["machine_id": "m2", "session_id": "s9"])
    }

    /// The background ends no voice chat on the iPhone (#9887 one button: switching away or locking never stops it): the
    /// phone's goes on with its socket, only its levels stop until the app is back; the car's goes on as before.
    @Test func theBackgroundEndsNoVoiceChat() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .carPlay, deps: rig.deps)
        calls.appDidEnterBackground()
        #expect(calls.current?.phase == .connecting, "a call from the car goes on")

        let phone = VoiceRig()
        let phoneCalls = VoiceCalls()
        await phoneCalls.start(phone.key, extensionID: "voice", surface: .phone, deps: phone.deps)
        let socket = try #require(phone.socket)
        socket.ready(phone.key)
        let chat = try #require(phoneCalls.current)
        phoneCalls.appDidEnterBackground()
        #expect(chat.phase.isLive && !socket.closed, "the phone's voice chat goes on in the background")
        #expect(phone.audio.uplink?.levelsPaused == true, "no levels where nobody sees them")
        phone.audio.capture(Data(repeating: 0x7F, count: 3200))
        #expect(chat.levels.input == 0)
        phoneCalls.appWillEnterForeground()
        #expect(phone.audio.uplink?.levelsPaused == false, "the levels again in front")
        #expect(phoneCalls.isBound(to: phone.key))
    }

    @Test func aSurfaceEndsOnlyItsOwnCallButTheUserEndsAny() async {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        calls.end(.left, from: .carPlay)
        #expect(calls.current?.phase == .connecting, "the car does not stop the phone's call")
        calls.end(.user, from: .carPlay)
        #expect(calls.current == nil, "结束 tapped ends it whoever started it, and nothing stays")
    }

    /// Leaving the page leaves the phone's voice chat on, with the bar back to it (#9887 one button); coming back takes
    /// the bar away; an ended one's Continue row goes with the page.
    @Test func leavingThePageKeepsTheVoiceChatWithTheBarBack() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        calls.leave(SessionKey(machineID: "m1", sessionID: "other"))
        #expect(calls.isBound(to: rig.key) && !calls.away, "another page leaving changes nothing")
        let call = try #require(calls.current)
        calls.leave(rig.key)
        #expect(!call.phase.isEnded && calls.away && calls.awayChat === call, "the chat goes on, the bar back to it shows")
        #expect(rig.socket?.closed == false)
        calls.returned(SessionKey(machineID: "m1", sessionID: "other"))
        #expect(calls.away, "another page coming back leaves the bar")
        calls.returned(rig.key)
        #expect(!calls.away && calls.awayChat == nil && calls.current === call)

        calls.leave(rig.key)
        call.end(.idle)
        #expect(calls.awayChat == nil, "an ended chat has no bar")
        calls.leave(rig.key)
        #expect(calls.current == nil, "its Continue row goes with the page")
        #expect(rig.microphone.claim(UUID()))
    }

    /// An interruption pauses the phone's voice chat (on hold: silence up, the extension told) and it goes on when the
    /// interruption ends with `shouldResume` (the audio brought back); without it, or when the audio will not come back,
    /// it ends as before, with Continue (#9887).
    @Test func anInterruptionPausesThePhonesVoiceChatAndItGoesOn() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let socket = try #require(rig.socket)
        socket.ready(rig.key)
        let chat = try #require(calls.current)
        rig.audio.emit(.interrupted)
        #expect(chat.held && chat.phase.isLive && chat.pausedByInterruption)
        rig.audio.emit(.interruptionEnded(shouldResume: true))
        #expect(!chat.held && chat.phase.isLive && rig.audio.resumes == 1)

        rig.audio.emit(.interrupted)
        rig.audio.resumeError = VoiceAudioError.session
        rig.audio.emit(.interruptionEnded(shouldResume: true))
        #expect(chat.phase == .ended(.interrupted), "the audio would not come back")

        let other = VoiceRig()
        let otherCalls = VoiceCalls()
        await otherCalls.start(other.key, extensionID: "voice", surface: .phone, deps: other.deps)
        let otherChat = try #require(otherCalls.current)
        other.audio.emit(.interrupted)
        other.audio.emit(.interruptionEnded(shouldResume: false))
        #expect(otherChat.phase == .ended(.interrupted) && other.audio.resumes == 0)

        // The car's still ends at an interruption.
        let car = VoiceRig()
        let carCalls = VoiceCalls()
        await carCalls.start(car.key, extensionID: "voice", surface: .carPlay, deps: car.deps)
        car.audio.emit(.interrupted)
        #expect(carCalls.current?.phase == .ended(.interrupted))
    }

    /// New rates while the phone's voice chat is paused by an interruption (#9887 review): its first `ready` came after
    /// the interruption began, or a reconnect's did. The audio takes them as it comes back, before its resume, not
    /// while the other audio has the session (the fake refuses a reconfigure then, as an engine start would be refused):
    /// the voice chat stays paused instead of ending.
    @Test func newRatesDuringAnInterruptionWaitForItsEnd() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let socket = try #require(rig.socket)
        let chat = try #require(calls.current)
        rig.audio.emit(.interrupted)
        rig.audio.reconfigureError = VoiceAudioError.engine
        socket.ready(rig.key, input: 24000, output: 16000)
        #expect(chat.phase.isLive && chat.held && chat.pausedByInterruption, "the first ready under the interruption ended it")
        #expect(rig.audio.reconfigured.isEmpty)
        rig.audio.reconfigureError = nil
        rig.audio.emit(.interruptionEnded(shouldResume: true))
        #expect(rig.audio.reconfigured == [VoiceAudioFormat(inputRate: 24000, outputRate: 16000)])
        #expect(rig.audio.steps == ["reconfigure", "resume"], "the rates before the audio comes back")
        #expect(!chat.held && chat.phase.isLive)

        // A reconnect's ready at other rates during the next interruption.
        rig.audio.emit(.interrupted)
        rig.audio.reconfigureError = VoiceAudioError.engine
        socket.drop()
        await rig.advance(1, sleepers: 2)
        let second = try #require(rig.socket)
        #expect(second !== socket)
        second.ready(rig.key, input: 16000, output: 24000)
        #expect(chat.phase.isLive && chat.held, "a reconnect's ready under the interruption ended it")
        rig.audio.reconfigureError = nil
        rig.audio.emit(.interruptionEnded(shouldResume: true))
        #expect(rig.audio.reconfigured.last == VoiceAudioFormat(inputRate: 16000, outputRate: 24000))
        #expect(rig.audio.steps.suffix(2) == ["reconfigure", "resume"] && !chat.held && chat.phase.isLive)

        // Out of an interruption new rates go to the audio at once, as before.
        let count = rig.audio.reconfigured.count
        second.drop()
        await rig.advance(1, sleepers: 2)
        try #require(rig.socket).ready(rig.key, input: 24000, output: 24000)
        #expect(rig.audio.reconfigured.count == count + 1)
    }

    /// The idle rule in the background (#9887 review): the app's sessions are not updated there (its socket to the hub is
    /// closed), so a conversation it last saw working must not keep the call open for ever. While the task runs the
    /// extension has the upstream paused, which keeps the call; once the voice's socket says it is done (the run done,
    /// the upstream woken, the result said) ten quiet minutes end it, though the app's copy still says working. In front
    /// that copy counts again.
    @Test func inTheBackgroundTheIdleRuleGoesByTheVoiceNotAStaleSession() async throws {
        let working = VoiceRig.session(#"{"machine_id":"m1","session_id":"s1","status":"working","state":"working"}"#)
        let rig = VoiceRig()
        rig.session = working
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let socket = try #require(rig.socket)
        socket.ready(rig.key)
        let chat = try #require(calls.current)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑测试","state":"working","msg_ids":["r1"],"base_seq":10}"#)
        socket.deliver(#"{"type":"link","state":"paused"}"#)
        calls.appDidEnterBackground()
        #expect(chat.sessionStale)
        await rig.advance(900)
        #expect(chat.phase.isLive, "the task goes on in the background: its run is open")
        socket.deliver(#"{"type":"run","id":"r1","task":"跑测试","state":"done","msg_ids":["r1"],"base_seq":10}"#)
        await rig.advance(900)
        #expect(chat.phase.isLive, "the conversation still works on something: the upstream stays paused for it")
        socket.deliver(#"{"type":"link","state":"waking"}"#)
        socket.deliver(#"{"type":"link","state":"live"}"#)
        socket.deliver(#"{"type":"transcript","id":"a1","role":"assistant","text":"测试都过了。","final":true}"#)
        #expect(rig.session?.state == .working, "the app's copy, not updated in the background")
        await rig.advance(570)
        #expect(chat.phase.isLive)
        await rig.advance(30)
        #expect(chat.phase == .ended(.idle), "the task was done: ten quiet minutes end it")

        // In front the app's copy is kept up to date and counts: working, the call stays.
        let front = VoiceRig()
        front.session = working
        let frontCalls = VoiceCalls()
        await frontCalls.start(front.key, extensionID: "voice", surface: .phone, deps: front.deps)
        try #require(front.socket).ready(front.key)
        let frontChat = try #require(frontCalls.current)
        frontCalls.appDidEnterBackground()
        frontCalls.appWillEnterForeground()
        #expect(!frontChat.sessionStale)
        await front.advance(900)
        #expect(frontChat.phase.isLive, "the conversation works")
    }

    /// The bar back to the voice chat tapped (#9887 review): its page is asked to close what covers it (`returning`), and
    /// the bar stays until that page shows again, so a full-screen picture over the same conversation never keeps the
    /// cover up with the bar gone.
    @Test func theBarStaysUntilItsPageShows() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let chat = try #require(calls.current)
        calls.leave(rig.key) // a full-screen picture of the same conversation covers its page
        #expect(calls.awayChat === chat)
        calls.returnTo(SessionKey(machineID: "m1", sessionID: "other"))
        #expect(calls.returning == nil, "another conversation asks nothing")
        calls.returnTo(rig.key)
        #expect(calls.returning == rig.key && calls.away && calls.awayChat === chat, "the bar went at the tap")
        calls.returned(rig.key)
        #expect(calls.returning == nil && !calls.away && calls.awayChat == nil)
        // Ended while its page was asked to show: nothing stays asked.
        calls.leave(rig.key)
        calls.returnTo(rig.key)
        calls.end(.user, from: .phone)
        #expect(calls.returning == nil && !calls.away)
    }

    @Test func leavingAPageDoesNotEndTheCarsCall() async {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .carPlay, deps: rig.deps)
        calls.leave(rig.key)
        #expect(calls.current?.phase == .connecting)
    }

    /// A call to Persona (#9290) goes on in the background and off the page; only its manager ends it.
    @Test func aSystemCallGoesOnInTheBackgroundAndOffThePage() async {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        let call = rig.call(surface: .call)
        await calls.begin(call)
        calls.appDidEnterBackground()
        calls.leave(rig.key)
        calls.end(.left, from: .phone)
        #expect(calls.current === call && call.phase == .connecting)
        #expect(rig.socket?.closed == false)
    }

    /// The same conversation from another surface is a call of its own: a voice chat on the main conversation while a
    /// call to Persona is on it ends the call, not picks it up.
    @Test func anotherSurfaceOnTheSameConversationIsACallOfItsOwn() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let chat = try #require(calls.current)
        let call = rig.call(surface: .call)
        await calls.begin(call)
        #expect(chat.phase == .ended(.replaced) && calls.current === call)
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        #expect(call.phase == .ended(.replaced) && calls.current?.surface == .phone)
    }

    @Test func anEndedSystemCallIsNeverPickedUpAgain() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        let call = rig.call(surface: .call)
        await calls.begin(call)
        call.end(.lost)
        await calls.resume(rig.key)
        #expect(call.phase == .ended(.lost) && rig.sockets.count == 1)
        calls.release(call)
        #expect(calls.current == nil)
    }

    /// A system call goes on when its page goes (it is `PersonaCalls`'); once it ended by itself its line (「通话已结束 · 继续」)
    /// stays on the page and goes with it (#9290).
    @Test func aSystemCallOutlivesItsPageAndItsEndedLineGoesWithIt() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        let call = rig.call(surface: .call)
        await calls.begin(call)
        calls.leave(rig.key)
        #expect(calls.current === call && !call.phase.isEnded, "the page going leaves the system call on")
        call.end(.lost)
        #expect(calls.current === call, "its ended line stays while the page is there")
        calls.leave(rig.key, from: .carPlay)
        #expect(calls.current === call, "only the phone's page lets it go")
        calls.leave(rig.key)
        #expect(calls.current == nil)
    }

    /// A system call's voice ended before its start ran (the system ended the call at once): it never begins.
    @Test func aSystemCallsVoiceThatEndedBeforeItBeganNeverBegins() async {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        let call = rig.call(surface: .call)
        call.end(.user)
        await calls.begin(call)
        #expect(call.phase == .ended(.user) && calls.current == nil)
        #expect(rig.sockets.isEmpty && rig.audio.uplink == nil && rig.microphone.isFree)
    }

    /// While Persona's urgent call is on (#9290) nothing starts or is picked up again from any surface, and nothing is
    /// touched: the call before goes on (or stays ended), no audio opens.
    @Test func noVoiceChatUnderAnUrgentCall() async throws {
        let rig = VoiceRig()
        var urgent = false
        let calls = VoiceCalls()
        calls.urgentCallOn = { urgent }
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        let first = try #require(calls.current)
        first.end(.interrupted)
        urgent = true
        let stops = rig.audio.stops
        for surface in [VoiceSurface.phone, .carPlay] {
            let other = SessionKey(group: rig.key.group, machineID: "m2", sessionID: "s9")
            #expect(await calls.start(other, extensionID: "voice", surface: surface, deps: rig.deps) == .urgentCall)
        }
        #expect(await calls.resume(rig.key) == .urgentCall)
        #expect(calls.current === first && first.phase == .ended(.interrupted))
        #expect(rig.sockets.count == 1 && rig.audio.uplink == nil && rig.audio.stops == stops)
        urgent = false
        #expect(await calls.resume(rig.key) == nil)
        #expect(first.phase == .connecting && rig.sockets.count == 2)
    }

    @Test func continueOnAnEndedCallPicksItUp() async throws {
        let rig = VoiceRig()
        let calls = VoiceCalls()
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        rig.socket?.ready(rig.key)
        // An interruption the system says not to resume from ends it (one that may resume only pauses it, #9887).
        rig.audio.emit(.interrupted)
        rig.audio.emit(.interruptionEnded(shouldResume: false))
        let call = try #require(calls.current)
        #expect(call.phase == .ended(.interrupted))
        await calls.resume(rig.key)
        #expect(calls.current === call && call.phase == .connecting && rig.sockets.count == 2)
        // Starting the same conversation again does the same.
        call.end(.lost)
        await calls.start(rig.key, extensionID: "voice", surface: .phone, deps: rig.deps)
        #expect(calls.current === call && rig.sockets.count == 3)
    }
}
