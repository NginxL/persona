import Foundation
import Testing
@testable import Persona

/// One call's logic with a fake socket, audio and clock (docs/assistant/voice.md 6.3–6.5, 6.10): the start frame, what is held
/// before `ready`, captions, runs, reconnects with `resume`, the ways a call ends, the screen lock.
@MainActor
struct VoiceConversationTests {
    private func started(_ rig: VoiceRig, surface: VoiceSurface = .phone, recent: [VoiceLine] = []) async -> (VoiceConversation, FakeVoiceSocket) {
        let call = rig.call(surface: surface, recent: recent)
        await call.begin()
        return (call, rig.socket!)
    }

    private func live(_ rig: VoiceRig) async -> (VoiceConversation, FakeVoiceSocket) {
        let (call, socket) = await started(rig)
        socket.ready(rig.key)
        return (call, socket)
    }

    // MARK: Start

    @Test func startIsProtocolTwoBoundToTheConversation() async throws {
        let rig = VoiceRig()
        let (call, socket) = await started(rig, recent: [VoiceLine(role: "user", text: "修一下登录页")])
        #expect(call.phase == .connecting)
        #expect(socket.request.url?.absoluteString == "wss://fake.invalid/api/v1/extensions/voice/ws")
        #expect(socket.request.value(forHTTPHeaderField: "Authorization") == "Bearer t")
        let start = try #require(socket.start)
        #expect(start["protocol"] as? Int == 2)
        #expect(start["target"] as? [String: String] == ["machine_id": "m1", "session_id": "s1"])
        #expect(start["recent"] as? [[String: String]] == [["role": "user", "text": "修一下登录页"]])
        #expect(start["voice"] as? String == "marin")
        #expect(start["model"] == nil, "no model configuration: the extension's default")
        #expect(start["history"] == nil && start["resume"] == nil, "nothing to pick up on the first connection")
        #expect(start["echo_cancellation"] as? Bool == true)
        #expect(rig.audio.running, "the microphone records from the start")
        #expect(rig.audio.format == .provisional)

        socket.ready(rig.key)
        #expect(call.phase == .live(.listening))
        #expect(call.target?.agentLabel == "Claude Code")
        #expect(call.features.contains("resume"))
    }

    @Test func startSaysTheMicrophoneHasNoEchoCancellation() async throws {
        let rig = VoiceRig()
        rig.audio.cancelsEcho = false
        let (_, socket) = await started(rig)
        #expect(try #require(socket.start)["echo_cancellation"] as? Bool == false)
    }

    /// #5904: Only Send What I Just Said turned off on this device (its preference, per group and extension) goes in
    /// `start` as speech_gate false; on, the default, as true: always said, it tells the extension this app has Send
    /// Anyway (an app from before it says nothing, and the hub then offers none; an older extension ignores it).
    @Test func startSaysWhetherTheSpeechGateIsOn() async throws {
        let rig = VoiceRig()
        let key = "voice.speechGate." + rig.key.group + ".voice"
        UserDefaults.standard.set(false, forKey: key)
        defer { UserDefaults.standard.removeObject(forKey: key) }
        let (_, socket) = await started(rig)
        #expect(try #require(socket.start)["speech_gate"] as? Bool == false)
        let plain = VoiceRig()
        let (_, other) = await started(plain)
        #expect(try #require(other.start)["speech_gate"] as? Bool == true)
    }

    @Test func theModelComesFromTheUseAndUnverifiedIsUsable() async throws {
        let rig = VoiceRig()
        rig.hub.on("GET", "/api/v1/model-config/purposes/plugin.voice", json: #"{"purpose":{"id":"plugin.voice","name":"实时语音","group":"语音","kind":"plugin","override":{"model":"custom/gpt-live-1"},"effective":{"model":"custom/gpt-live-1"},"revision":1,"status":"未验证"},"providers":[]}"#)
        rig.hub.on("GET", "/api/v1/extensions/voice/models", json: #"{"models":[{"id":"custom/gpt-live-1"},{"id":"custom/gpt-live-mini"}],"default":"custom/gpt-live-mini"}"#)
        let (call, socket) = await started(rig)
        #expect(call.model == "custom/gpt-live-1")
        #expect(socket.start?["model"] as? String == "custom/gpt-live-1")
    }

    @Test func aModelNoLongerOfferedKeepsTheCallFromStarting() async {
        let rig = VoiceRig()
        rig.hub.on("GET", "/api/v1/model-config/purposes/plugin.voice", json: #"{"purpose":{"id":"plugin.voice","name":"实时语音","group":"语音","kind":"plugin","override":{},"effective":{"model":"custom/gone"},"revision":1},"providers":[]}"#)
        rig.hub.on("GET", "/api/v1/extensions/voice/models", json: #"{"models":[{"id":"custom/gpt-live-1"}]}"#)
        let call = rig.call()
        await call.begin()
        #expect(call.phase == .ended(.modelUnavailable("custom/gone")))
        #expect(rig.sockets.isEmpty)
        #expect(rig.audio.stops == 1 && !rig.audio.running)
    }

    // MARK: Audio before ready

    @Test func audioHeldBeforeReadyGoesOutAfterIt() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig)
        rig.audio.capture(.pcm(800, 1))
        rig.audio.capture(.pcm(800, 2))
        #expect(socket.audio.isEmpty, "nothing goes before the extension is ready")
        socket.ready(rig.key)
        #expect(call.phase == .live(.listening))
        let sent = VoicePCM.samples(socket.audio.reduce(Data(), +))
        #expect(sent == [Int16](repeating: 1, count: 800) + [Int16](repeating: 2, count: 800))
        rig.audio.capture(.pcm(160, 3))
        #expect(socket.audio.last == .pcm(160, 3))
    }

    @Test func readyAtOtherRatesSetsTheAudioUpAgain() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig)
        rig.audio.capture(.pcm(1600))
        socket.ready(rig.key, input: 24000, output: 24000)
        #expect(rig.audio.reconfigured == [VoiceAudioFormat(inputRate: 24000, outputRate: 24000)])
        #expect(socket.audio.reduce(0) { $0 + $1.count } == 2400 * 2, "the tenth of a second held, at the new rate")
        #expect(call.phase == .live(.listening))

        // The next call of the group records at those rates from the start.
        let next = rig.call()
        call.end(.user)
        await next.begin()
        #expect(rig.audio.format == VoiceAudioFormat(inputRate: 24000, outputRate: 24000))
        next.end(.user)
    }

    @Test func mutedSendsSilence() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        call.muted = true
        rig.audio.capture(.pcm(320, 5000))
        #expect(socket.audio.last == Data(count: 640))
        call.muted = false
        rig.audio.capture(.pcm(320, 5000))
        #expect(socket.audio.last == .pcm(320, 5000))
    }

    // MARK: Frames

    @Test func captionsMergeByIDAndConnectionsStayApart() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"你","final":false}"#)
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"你好","final":true}"#)
        socket.deliver(#"{"type":"transcript","id":"a1","role":"assistant","text":"好，交给 Claude 了","final":true}"#)
        #expect(call.captions.map(\.text) == ["你好", "好，交给 Claude 了"])
        #expect(call.captions.map(\.final) == [true, true])

        socket.drop()
        await rig.advance(1, sleepers: 2)
        let second = rig.socket!
        #expect(second !== socket)
        second.ready(rig.key)
        // A new connection numbers its lines from the start again: its u1 is a new line.
        second.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"再说一句","final":true}"#)
        #expect(call.captions.map(\.text) == ["你好", "好，交给 Claude 了", "再说一句"])
        #expect(call.captions.map(\.id) == ["1:u1", "1:a1", "2:u1"])
    }

    /// A caption the extension says goes before another line (#5673: the user's caption came after the voice began to
    /// answer it) is put in front of that line, and stays there as both grow; one whose line this connection does not
    /// have (an older connection's, or none) goes at the end, as does one without it.
    @Test func aLateCaptionGoesBeforeTheAnswerItCameIn() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"你好","final":true}"#)
        socket.deliver(#"{"type":"transcript","id":"a1","role":"assistant","text":"它还停在原地","final":false}"#)
        socket.deliver(#"{"type":"transcript","id":"u2","role":"user","text":"它现在到哪了？","final":false,"before":"a1"}"#)
        socket.deliver(#"{"type":"transcript","id":"a1","role":"assistant","text":"它还停在原地，还没往下做。","final":true}"#)
        socket.deliver(#"{"type":"transcript","id":"u2","role":"user","text":"它现在到哪了？","final":true,"before":"a1"}"#)
        #expect(call.captions.map(\.id) == ["1:u1", "1:u2", "1:a1"])
        #expect(call.captions.map(\.text) == ["你好", "它现在到哪了？", "它还停在原地，还没往下做。"])
        #expect(call.captions.map(\.final) == [true, true, true])
        #expect(call.history.map(\.role) == ["user", "user", "assistant"], "a pick-up hands them back in the order said")

        socket.deliver(#"{"type":"transcript","id":"u3","role":"user","text":"再说一句","final":true,"before":"a9"}"#)
        socket.deliver(#"{"type":"transcript","id":"a2","role":"assistant","text":"好。","final":true}"#)
        #expect(call.captions.map(\.id) == ["1:u1", "1:u2", "1:a1", "1:u3", "1:a2"])

        socket.drop()
        await rig.advance(1, sleepers: 2)
        let second = rig.socket!
        second.ready(rig.key)
        // The new connection numbers its lines from the start again: its a2 is not the old a2.
        second.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"新的一句","final":true,"before":"a2"}"#)
        #expect(call.captions.map(\.id) == ["1:u1", "1:u2", "1:a1", "1:u3", "1:a2", "2:u1"])
    }

    @Test func atMostFortyCaptions() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        for i in 0..<45 { socket.deliver(#"{"type":"transcript","id":"a\#(i)","role":"assistant","text":"第 \#(i) 句","final":true}"#) }
        #expect(call.captions.count == 40)
        #expect(call.captions.first?.text == "第 5 句")
        #expect(call.history.count == 40)
    }

    @Test func runsAskLinkToolAndNotice() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑测试","state":"sending","msg_ids":["r1"],"base_seq":10}"#)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑测试","state":"working","msg_ids":["r1"],"base_seq":10}"#)
        #expect(call.runs.map(\.state) == [.working])
        socket.deliver(#"{"type":"ask","interaction_id":"i1","state":"asking"}"#)
        #expect(call.askingID == "i1")
        socket.deliver(#"{"type":"ask","interaction_id":"i2","state":"released","by":"elsewhere"}"#)
        #expect(call.askingID == "i1", "another card let go does not clear this one")
        socket.deliver(#"{"type":"ask","interaction_id":"i1","state":"answered","by":"voice"}"#)
        #expect(call.askingID == nil)
        socket.deliver(#"{"type":"link","state":"paused"}"#)
        #expect(call.link == .paused)
        socket.deliver(#"{"type":"tool","id":"t1","label":"查知识库"}"#)
        #expect(call.tools.map(\.label) == ["查知识库"])
        socket.deliver(#"{"type":"tool","id":"t1","done":true}"#)
        #expect(call.tools.isEmpty)
        socket.deliver(#"{"type":"error","message":"offline","code":"machine_offline"}"#)
        #expect(call.notice?.code == .machineOffline && call.phase == .live(.listening))
        socket.deliver(#"{"type":"something_new"}"#)
        #expect(call.phase == .live(.listening))
    }

    @Test func playbackClearAndInterrupt() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(data: .pcm(240))
        #expect(rig.audio.played == [.pcm(240)])
        socket.deliver(#"{"type":"clear"}"#)
        #expect(rig.audio.clears == 1)
        call.interruptSpeech()
        #expect(socket.frames.last?["type"] as? String == "interrupt")
        #expect(rig.audio.clears == 2, "what is queued is dropped here at once")
        call.say("  顺便把 lint 也修了 ")
        #expect(socket.frames.last?["type"] as? String == "text")
        #expect(socket.frames.last?["text"] as? String == "顺便把 lint 也修了")
    }

    @Test func phaseFollowsTheVoiceAndTheUsersCaption() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        rig.audio.emit(.playing(true))
        #expect(call.phase == .live(.speaking))
        rig.audio.emit(.outputLevel(0.5))
        #expect(call.levels.output == 0.5)
        rig.audio.emit(.playing(false))
        #expect(call.phase == .live(.listening))
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"我想","final":false}"#)
        #expect(call.phase == .live(.hearing))
        await rig.advance(2, sleepers: 2) // the idle watch and the caption's
        #expect(call.phase == .live(.listening), "a caption that stopped changing is no longer being spoken")
    }

    /// Clear and Send hands back the hub's `clearable` text, not the whole `draft` line (an IDE selection marker is in `draft`
    /// only), and is refused for a run with nothing the node can clear.
    @Test func resendCarriesTheClearableText() async throws {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"run","id":"voice-1","task":"跑测试","state":"refused","msg_ids":["voice-1"],"base_seq":3,"code":"input_draft","draft":"[⧉ Selected 3 lines from foo.go] 没发的字","clearable":"没发的字"}"#)
        #expect(call.resend("voice-1", clearingDraft: true))
        let frame = try #require(socket.frames.last)
        #expect(frame["type"] as? String == "resend" && frame["id"] as? String == "voice-1" && frame["clear_draft"] as? String == "没发的字")
        #expect(call.resend("voice-1"))
        #expect(socket.frames.last?["clear_draft"] == nil)
        #expect(!call.resend("nope"))
        socket.deliver(#"{"type":"run","id":"voice-3","task":"看日志","state":"refused","msg_ids":["voice-3"],"base_seq":5,"code":"input_draft","draft":"两行\n的字"}"#)
        let sent = socket.frames.count
        #expect(!call.resend("voice-3", clearingDraft: true), "nothing the node can clear")
        #expect(socket.frames.count == sent)
        #expect(call.resend("voice-3"))
        socket.deliver(#"{"type":"run","id":"voice-2","task":"看日程","state":"working","msg_ids":["voice-2"],"base_seq":4}"#)
        #expect(!call.canResend("voice-2") && !call.resend("voice-2"), "only a refused run is sent again")
    }

    /// A reconnect rebuilds only the open runs: the new connection does not know a refused one, so nothing offers to
    /// send it again; one the new connection refuses can be.
    @Test func aRunRefusedBeforeAReconnectIsNotSentAgain() async throws {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"run","id":"voice-1","task":"跑测试","state":"refused","msg_ids":["voice-1"],"base_seq":3,"code":"input_draft","draft":"没发的字","clearable":"没发的字"}"#)
        #expect(call.canResend("voice-1"))
        socket.drop()
        #expect(!call.canResend("voice-1"), "nothing goes while it reconnects")
        await rig.advance(1, sleepers: 2)
        let second = try #require(rig.sockets.count == 2 ? rig.socket : nil)
        #expect(second.start?["resume"] == nil, "a refused run is over for the extension")
        second.ready(rig.key)
        let sent = second.texts.count
        #expect(!call.canResend("voice-1") && !call.resend("voice-1"))
        #expect(second.texts.count == sent, "no resend for an id this connection never saw")
        #expect(call.runs.map(\.id) == ["voice-1"], "still shown, with its reason")

        second.deliver(#"{"type":"run","id":"voice-2","task":"跑测试","state":"refused","msg_ids":["voice-2"],"base_seq":3,"code":"dialog_open"}"#)
        #expect(call.resend("voice-2"))
        #expect(second.frames.last?["id"] as? String == "voice-2")
    }

    // MARK: Reconnect and resume

    @Test func reconnectsWithHistoryAndOnlyTheOpenRunsAsLastReported() async throws {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"跑一下测试","final":true}"#)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑一下测试","state":"sending","msg_ids":["r1"],"base_seq":10,"started_at":"2026-11-06T10:00:00Z"}"#)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑一下测试","state":"held","msg_ids":["r1"],"base_seq":14,"started_at":"2026-11-06T10:00:00Z"}"#)
        socket.deliver(#"{"type":"run","id":"r2","task":"看日程","state":"done","msg_ids":["r2"],"base_seq":20}"#)
        rig.audio.emit(.playing(true))

        socket.drop()
        #expect(call.phase == .reconnecting(attempt: 1))
        #expect(rig.audio.clears == 1, "what the old connection still had to say is dropped")
        rig.audio.capture(.pcm(160, 7)) // said while it reconnects: held
        await rig.advance(0.9, sleepers: 2)
        #expect(rig.sockets.count == 1, "the first try waits a second")
        await rig.advance(0.1, sleepers: 2)
        let second = try #require(rig.sockets.count == 2 ? rig.socket : nil)
        let start = try #require(second.start)
        #expect(start["history"] as? [[String: String]] == [["role": "user", "text": "跑一下测试"]])
        let runs = try #require((start["resume"] as? [String: Any])?["runs"] as? [[String: Any]])
        #expect(runs.map { $0["id"] as? String } == ["r1"], "only what is not over")
        #expect(runs.first?["base_seq"] as? Int == 14, "the latest base_seq")
        #expect(runs.first?["state"] as? String == "held")
        #expect(runs.first?["started_at"] as? String == "2026-11-06T10:00:00Z")

        second.ready(rig.key)
        #expect(call.phase == .live(.listening), "a dropped socket's playback is not the new one's")
        #expect(call.attempt == 0)
        #expect(second.audio.first == .pcm(160, 7))
    }

    @Test func threeFailedReconnectsEndTheCall() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.drop()
        #expect(call.phase == .reconnecting(attempt: 1))
        await rig.advance(1, sleepers: 2)
        rig.socket!.drop()
        #expect(call.phase == .reconnecting(attempt: 2))
        await rig.advance(3.9, sleepers: 2)
        #expect(rig.sockets.count == 2, "the second try waits four seconds")
        await rig.advance(0.1, sleepers: 2)
        rig.socket!.drop()
        #expect(call.phase == .reconnecting(attempt: 3))
        await rig.advance(10, sleepers: 2)
        #expect(rig.sockets.count == 4)
        rig.socket!.drop()
        #expect(call.phase == .ended(.lost))
        #expect(!call.phase.isLive && rig.audio.stops == 1)
    }

    @Test func aReadyThatNeverComesCountsAsADrop() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig)
        await rig.advance(15, sleepers: 2) // the idle watch and the ready timeout
        #expect(socket.closed)
        #expect(call.phase == .reconnecting(attempt: 1))
    }

    @Test(arguments: [401, 403, 404])
    func aRefusedFirstHandshakeIsNotTriedAgain(status: Int) async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig)
        socket.drop(status: status)
        #expect(call.phase == .ended(.rejected(status: status)))
        #expect(rig.sockets.count == 1)
    }

    /// The hub answers 404 while it applies the extension again (a setting changed, an upgrade): once the call was
    /// live, that is one more failed try, not the end.
    @Test func aReconnectsNotFoundIsOneMoreTry() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.drop()
        await rig.advance(1, sleepers: 2)
        rig.socket!.drop(status: 404)
        #expect(call.phase == .reconnecting(attempt: 2))
        await rig.advance(4, sleepers: 2)
        rig.socket!.ready(rig.key)
        #expect(call.phase == .live(.listening))
        #expect(rig.sockets.count == 3)

        // A revoked token or an answer-only device ends it even on a reconnect.
        rig.socket!.drop()
        await rig.advance(1, sleepers: 2)
        rig.socket!.drop(status: 401)
        #expect(call.phase == .ended(.rejected(status: 401)))
    }

    /// A new connection is a new conversation with the extension: the last one's link state, the card it asked about
    /// and its notices go; what the new one reports, even before `ready`, stays.
    @Test func aNewConnectionStartsFromWhatItSays() async throws {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"link","state":"paused"}"#)
        socket.deliver(#"{"type":"ask","interaction_id":"i1","state":"asking"}"#)
        socket.deliver(#"{"type":"error","message":"offline","code":"machine_offline"}"#)
        #expect(call.link == .paused && call.askingID == "i1" && call.notice != nil)
        socket.drop()
        await rig.advance(1, sleepers: 2)
        let second = try #require(rig.sockets.count == 2 ? rig.socket : nil)
        second.deliver(#"{"type":"error","message":"fell back","code":"model_unavailable"}"#)
        second.ready(rig.key)
        #expect(call.link == .live)
        #expect(call.askingID == nil)
        #expect(call.notice?.code == .modelUnavailable, "the new connection's own notice")
        second.deliver(#"{"type":"ask","interaction_id":"i1","state":"asking"}"#)
        #expect(call.askingID == "i1")
    }

    @Test func aFatalErrorEndsWithoutReconnecting() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig)
        socket.deliver(#"{"type":"error","message":"401 from upstream","fatal":true,"code":"upstream_forbidden"}"#)
        #expect(call.phase == .ended(.failed(code: .upstreamForbidden, message: "401 from upstream")))
        #expect(socket.closed)
        socket.drop()
        #expect(rig.sockets.count == 1)
    }

    /// iOS has no protocol 1 client: an older extension would send everything to the main conversation instead.
    @Test func anOlderExtensionOrAnotherTargetIsRefused() async {
        let rig = VoiceRig()
        let (old, socket) = await started(rig)
        socket.ready(rig.key, protocolVersion: 1)
        #expect(old.phase == .ended(.incompatible))

        let other = VoiceRig()
        let (call, second) = await started(other)
        second.ready(SessionKey(machineID: "m1", sessionID: "elsewhere"))
        #expect(call.phase == .ended(.incompatible))
    }

    @Test func continueAfterAnEndingPicksTheCallUp() async throws {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑测试","state":"working","msg_ids":["r1"],"base_seq":10}"#)
        rig.audio.emit(.interrupted)
        rig.audio.emit(.interruptionEnded(shouldResume: false))
        #expect(call.phase == .ended(.interrupted))
        await call.restart()
        let resumed = try #require(rig.socket)
        #expect(resumed !== socket)
        #expect(((resumed.start?["resume"] as? [String: Any])?["runs"] as? [[String: Any]])?.count == 1)
        resumed.ready(rig.key)
        #expect(call.phase == .live(.listening))
    }

    @Test func anotherVoiceIsANewConnectionThatPicksUp() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        socket.deliver(#"{"type":"transcript","id":"a1","role":"assistant","text":"在呢","final":true}"#)
        call.change(voice: "cedar")
        #expect(socket.closed)
        #expect(rig.socket?.start?["voice"] as? String == "cedar")
        #expect((rig.socket?.start?["history"] as? [Any])?.count == 1)
        #expect(call.phase == .connecting)
    }

    // MARK: Endings

    @Test func theScreenStaysOnDuringAPhoneCallOnly() async {
        let rig = VoiceRig()
        let (call, _) = await live(rig)
        #expect(rig.idleTimer.isDisabled)
        call.end(.user)
        #expect(!rig.idleTimer.isDisabled)
        #expect(rig.idleTimer.changes == [true, false])

        let car = VoiceRig()
        let carCall = car.call(surface: .carPlay)
        await carCall.begin()
        carCall.end(.user)
        #expect(car.idleTimer.changes.isEmpty)
    }

    /// A phone call pauses the phone's voice chat (#9887: on hold, the socket and the microphone kept); one the system
    /// says not to resume from ends it and frees the microphone.
    @Test func aPhoneCallPausesItAndOneThatMayNotResumeEndsIt() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        rig.audio.emit(.interrupted)
        #expect(call.held && !call.phase.isEnded && !socket.closed && rig.audio.stops == 0)
        #expect(!socket.texts.contains { $0.contains(#""type":"hold""#) }, "an extension without `hold` hears nothing of it")
        rig.audio.emit(.interruptionEnded(shouldResume: false))
        #expect(call.phase == .ended(.interrupted))
        #expect(socket.closed && rig.audio.stops == 1)
        #expect(rig.microphone.claim(UUID()))
    }

    @Test func aRecordingTakesTheMicrophoneFromTheCall() async {
        let rig = VoiceRig()
        let (call, socket) = await live(rig)
        #expect(rig.microphone.claim(UUID()), "a recording or dictation starts")
        #expect(call.phase == .ended(.microphoneTaken))
        #expect(socket.closed && !rig.audio.running)
    }

    @Test func aRecordingInProgressKeepsTheCallFromStarting() async {
        let rig = VoiceRig()
        #expect(rig.microphone.claim(UUID()))
        let call = rig.call()
        await call.begin()
        #expect(call.phase == .ended(.microphoneBusy))
        #expect(rig.sockets.isEmpty && !rig.audio.running)
    }

    @Test func noMicrophonePermission() async {
        let rig = VoiceRig()
        rig.allowed = false
        let call = rig.call()
        await call.begin()
        #expect(call.phase == .ended(.permissionDenied))
        #expect(rig.sockets.isEmpty && rig.audio.format == nil)
        #expect(rig.microphone.claim(UUID()))
    }

    @Test func aMicrophoneThatWillNotOpen() async {
        let rig = VoiceRig()
        rig.audio.failStart = true
        let call = rig.call()
        await call.begin()
        #expect(call.phase == .ended(.audioFailed))
        #expect(rig.sockets.isEmpty && !rig.idleTimer.isDisabled)
        #expect(rig.microphone.claim(UUID()))
    }

    @Test func noInputEndsAsAudioFailedAndARefusedRecordingAsPermission() async {
        let none = VoiceRig()
        none.audio.failStart = true
        none.audio.startError = .noInput
        let call = none.call()
        await call.begin()
        #expect(call.phase == .ended(.audioFailed), "no input, or the simulator's would not start (#5029)")
        #expect(none.sockets.isEmpty)

        let refused = VoiceRig()
        refused.audio.failStart = true
        refused.audio.startError = .permission
        let other = refused.call()
        await other.begin()
        #expect(other.phase == .ended(.permissionDenied))
        #expect(refused.sockets.isEmpty && refused.microphone.claim(UUID()))
    }

    @Test func tenIdleMinutesEndTheCallUnlessSomethingIsOpen() async {
        let busy = VoiceRig()
        busy.session = VoiceRig.session(#"{"machine_id":"m1","session_id":"s1","status":"working","state":"working"}"#)
        let (working, _) = await live(busy)
        await busy.advance(600)
        #expect(working.phase == .live(.listening), "the conversation works: not idle")

        let open = VoiceRig()
        let (waiting, socket) = await live(open)
        socket.deliver(#"{"type":"run","id":"r1","task":"跑测试","state":"sent","msg_ids":["r1"],"base_seq":1}"#)
        await open.advance(600)
        #expect(waiting.phase == .live(.listening), "a run is open: not idle")

        let quiet = VoiceRig()
        let (idle, _) = await live(quiet)
        await quiet.advance(570)
        #expect(idle.phase == .live(.listening))
        await quiet.advance(30)
        #expect(idle.phase == .ended(.idle))
    }

    @Test func idleRule() {
        let start = Date(timeIntervalSince1970: 0)
        let later = start.addingTimeInterval(600)
        #expect(VoiceIdle.expired(now: later, lastActivity: start, openRuns: false, targetWorking: false, playing: false))
        #expect(!VoiceIdle.expired(now: later.addingTimeInterval(-1), lastActivity: start, openRuns: false, targetWorking: false, playing: false))
        #expect(!VoiceIdle.expired(now: later, lastActivity: start, openRuns: true, targetWorking: false, playing: false))
        #expect(!VoiceIdle.expired(now: later, lastActivity: start, openRuns: false, targetWorking: true, playing: false))
        #expect(!VoiceIdle.expired(now: later, lastActivity: start, openRuns: false, targetWorking: false, playing: true))
    }

    // MARK: A system call (#9290)

    @Test func aSystemCallSaysItsSurfaceAndOtherCallsDoNot() async throws {
        let rig = VoiceRig()
        let (_, socket) = await started(rig, surface: .call)
        #expect(try #require(socket.start)["surface"] as? String == "call")
        let phone = VoiceRig()
        let (_, other) = await started(phone)
        #expect(try #require(other.start)["surface"] == nil)
    }

    @Test func anInterruptionHoldsASystemCallInsteadOfEndingIt() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig, surface: .call)
        socket.ready(rig.key)
        rig.audio.emit(.interrupted)
        #expect(call.phase.isLive && !socket.closed)
        rig.audio.emit(.interruptionEnded(shouldResume: true))
        #expect(rig.heard == [.ready, .interrupted, .interruptionEnded(shouldResume: true)])
    }

    @Test func onHoldSilenceGoesUpAndNothingPlays() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig, surface: .call)
        socket.ready(rig.key)
        let clears = rig.audio.clears
        call.setHeld(true)
        #expect(call.held && rig.audio.clears == clears + 1, "what the voice was saying is dropped")
        rig.audio.capture(.pcm(1600))
        socket.deliver(data: .pcm(480))
        #expect(rig.audio.played.isEmpty)
        #expect(socket.audio.last.map { VoicePCM.samples($0).allSatisfy { $0 == 0 } } == true)
        call.setHeld(false)
        rig.audio.capture(.pcm(1600))
        #expect(socket.audio.last.map { VoicePCM.samples($0).contains { $0 != 0 } } == true)
        socket.deliver(data: .pcm(480))
        #expect(rig.audio.played.count == 1)
    }

    /// Speech the microphone caught while the connection waited for `ready` (the first one, or a reconnect's), then the
    /// call went on hold: none of it goes up when `ready` comes while it is held, with the hold feature or without.
    @Test func onHoldWhatWasCaughtBeforeReadyNeverGoesUp() async {
        for hold in [false, true] {
            let rig = VoiceRig()
            let (call, socket) = await started(rig, surface: .call)
            rig.audio.capture(.pcm(1600, 5000))
            call.setHeld(true)
            rig.audio.capture(.pcm(1600, 5000))
            socket.ready(rig.key, hold: hold)
            #expect(!socket.audio.isEmpty)
            #expect(socket.audio.allSatisfy { VoicePCM.samples($0).allSatisfy { $0 == 0 } }, "hold feature \(hold): silence only")
            if hold { #expect(socket.frames.last?["type"] as? String == "hold" && socket.frames.last?["on"] as? Bool == true) }

            // A reconnect: what was caught while the socket was away, then the hold, then the new `ready`.
            call.setHeld(false)
            socket.drop()
            rig.audio.capture(.pcm(1600, 5000))
            call.setHeld(true)
            rig.audio.capture(.pcm(1600, 5000))
            await rig.advance(1)
            let again = rig.socket!
            again.ready(rig.key, hold: hold)
            #expect(again !== socket && !again.audio.isEmpty)
            #expect(again.audio.allSatisfy { VoicePCM.samples($0).allSatisfy { $0 == 0 } }, "hold feature \(hold): after a reconnect")
            call.setHeld(false)
            rig.audio.capture(.pcm(1600, 5000))
            #expect(again.audio.last.map { VoicePCM.samples($0).contains { $0 != 0 } } == true, "off hold it is heard again")
        }
    }

    @Test func holdFramesGoOnlyToAnExtensionThatTakesThemAndAgainAfterAReconnect() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig, surface: .call)
        socket.ready(rig.key)
        call.setHeld(true)
        #expect(!socket.texts.contains { $0.contains(#""type":"hold""#) }, "an extension without the feature hears nothing")
        call.setHeld(false)

        let held = VoiceRig()
        let (other, first) = await started(held, surface: .call)
        first.ready(held.key, hold: true)
        other.setHeld(true)
        #expect(first.frames.last?["type"] as? String == "hold" && first.frames.last?["on"] as? Bool == true)
        first.drop()
        await held.advance(1)
        let second = held.socket!
        second.ready(held.key, hold: true)
        #expect(second.frames.last?["type"] as? String == "hold" && second.frames.last?["on"] as? Bool == true,
                "still on hold: the new connection hears it")
        other.setHeld(false)
        #expect(second.frames.last?["on"] as? Bool == false)
    }

    @Test func onHoldTheQuietMinutesStopAndStartAgainAfter() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig, surface: .call)
        socket.ready(rig.key)
        call.setHeld(true)
        await rig.advance(900)
        #expect(call.phase.isLive, "frozen while held")
        call.setHeld(false)
        await rig.advance(570)
        #expect(call.phase.isLive)
        await rig.advance(30)
        #expect(call.phase == .ended(.idle), "the idle rule still ends a system call")
    }

    @Test func connectedAtIsTheFirstReadyAndSaidOnce() async {
        let rig = VoiceRig()
        let (call, socket) = await started(rig, surface: .call)
        #expect(call.connectedAt == nil)
        socket.ready(rig.key)
        let at = rig.clock.now
        #expect(call.connectedAt == at)
        socket.drop()
        await rig.advance(1)
        rig.socket?.ready(rig.key)
        #expect(call.connectedAt == at && rig.heard.filter { $0 == .ready }.count == 1)
    }

    @Test func aSystemCallsMuteOnThePageIsToldAndItsManagersIsNot() async {
        let rig = VoiceRig()
        let (call, _) = await live(rig)
        call.muted = true
        #expect(!rig.heard.contains(.muted(true)), "an in-app call has no system to tell")
        let system = VoiceRig()
        let (other, socket) = await started(system, surface: .call)
        socket.ready(system.key)
        other.muted = true
        other.setCallMuted(false)
        #expect(system.heard == [.ready, .muted(true)])
        #expect(!other.muted)
    }

    @Test func theEndIsTold() async {
        let rig = VoiceRig()
        let (call, _) = await started(rig, surface: .call)
        call.end(.lost)
        call.end(.user)
        #expect(rig.heard == [.ended(.lost)])
    }

    @Test func endedForAnUrgentCallOffersContinue() {
        let note = VoiceEndNote.make(.urgentCall, extensionID: "voice")
        #expect(note?.again == .resume && note?.text.isEmpty == false)
        #expect(!VoiceEnd.urgentCall.byUser)
    }

    @Test func endingTwiceKeepsTheFirstReason() async {
        let rig = VoiceRig()
        let (call, _) = await live(rig)
        call.end(.lost)
        call.end(.user)
        #expect(call.phase == .ended(.lost))
        #expect(rig.audio.stops == 1)
    }
}
