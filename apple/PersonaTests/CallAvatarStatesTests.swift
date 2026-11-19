import Foundation
import Observation
import SwiftUI
import Testing
import UIKit
@testable import Persona

/// The call's avatar shows each state of the call (#9887, docs/apple/calls.md "The avatar"): the row from what the call
/// screen observes (`CallScreenState.make`), its look per kind of picture (`CallAvatarLook.make`: the built-in Mochi, a
/// package, an uploaded picture, another conversation's), the shared speaking motion (`CallSpeech`), Mochi's call-only
/// drawing leaving every other use as it was, the voice's thinking; and from the coordinator: the voice's pause is not a
/// hold, one name for the main conversation, the voice chat in the app asking before it is replaced.
@MainActor
struct CallAvatarStatesTests {
    private static let since = Date(timeIntervalSinceReferenceDate: 1_000)
    private static let on = CallPhase.connected(since: since)
    typealias Row = CallAvatarLook.Row
    typealias Kind = CallAvatarLook.Kind

    private static func state(_ phase: CallPhase = on, held: Bool = false, muted: Bool = false, voice: (VoicePhase, VoiceLink)? = nil,
                              reconnecting: Bool = false, working: Bool = false, asking: Bool = false,
                              thinking: Bool = false) -> CallScreenState {
        CallScreenState.make(phase: phase, held: held, muted: muted, note: nil, voice: voice.map { (phase: $0.0, link: $0.1) },
                             reconnecting: reconnecting, activity: CallActivity(working: working, asking: asking, thinking: thinking))
    }

    // MARK: The rows, from what the call screen observes

    /// Each row of the table from the voice's phase, its link, the hold, the mute, the conversation working, a card
    /// waiting and the voice thinking.
    @Test func eachRowFromWhatTheCallScreenObserves() {
        let live = { (s: VoiceLiveState) in (VoicePhase.live(s), VoiceLink.live) }
        #expect(Self.state(.dialing).avatar == .connecting)
        #expect(Self.state(.connecting).avatar == .connecting)
        #expect(Self.state(voice: (.reconnecting(attempt: 1), .live)).avatar == .connecting)
        #expect(Self.state(voice: live(.listening)).avatar == .listening)
        #expect(Self.state(voice: live(.hearing)).avatar == .hearing)
        #expect(Self.state(voice: live(.listening), thinking: true).avatar == .thinking)
        #expect(Self.state(voice: live(.speaking)).avatar == .speaking)
        #expect(Self.state(voice: live(.listening), working: true).avatar == .working)
        #expect(Self.state(voice: live(.listening), asking: true).avatar == .needsYou)
        #expect(Self.state(held: true, voice: live(.speaking)).avatar == .held)
        #expect(Self.state(muted: true, voice: live(.listening)).avatar == .muted)
        #expect(Self.state(.ended(.hungUp)).avatar == .ended)
    }

    /// Which wins when more than one holds: the hold and the end over everything; the voice speaking over the
    /// conversation; a card over the voice thinking, which goes over the conversation working; a muted microphone keeps
    /// what the conversation does (its mark is on it); the owner speaking over nothing.
    @Test func theRowsInOrder() {
        let live = { (s: VoiceLiveState) in (VoicePhase.live(s), VoiceLink.live) }
        #expect(Self.state(held: true, voice: live(.listening), working: true, asking: true).avatar == .held)
        #expect(Self.state(voice: live(.speaking), working: true, asking: true, thinking: true).avatar == .speaking)
        #expect(Self.state(voice: live(.listening), working: true, asking: true, thinking: true).avatar == .needsYou)
        #expect(Self.state(voice: live(.listening), working: true, thinking: true).avatar == .thinking)
        #expect(Self.state(muted: true, voice: live(.listening), working: true).avatar == .working)
        #expect(Self.state(muted: true, voice: live(.speaking)).avatar == .speaking, "muted, it still speaks")
        #expect(Self.state(voice: live(.hearing), working: true).avatar == .working)
        #expect(Self.state(muted: true).avatar == .muted && Self.state(muted: true).microphoneOff, "a sample, no voice")
    }

    // MARK: Item 4: the voice's pause is not a hold

    /// The extension pausing its upstream while nobody speaks (or while the conversation works) and waking it again is
    /// the call listening (or working), not on hold: no caution ring, no pause mark, the line stays the running time.
    /// Speaking resumes it; only the call's own hold is a hold.
    @Test func theVoicesPauseIsNotAHold() {
        for link in [VoiceLink.paused, .waking] {
            let quiet = Self.state(voice: (.live(.listening), link))
            #expect(quiet.line == .duration(since: Self.since) && quiet.orb == .listening && quiet.avatar == .listening, "\(link)")
            let working = Self.state(voice: (.live(.listening), link), working: true)
            #expect(working.line == .duration(since: Self.since) && working.orb == .working && working.avatar == .working, "\(link)")
            #expect(Self.state(voice: (.live(.speaking), link)).avatar == .speaking, "\(link)")
            #expect(Self.state(muted: true, voice: (.live(.listening), link)).orb == .muted, "\(link)")
            let look = CallAvatarLook.make(quiet.avatar, muted: false, kind: .builtin, motion: true)
            #expect(look.ring != .held && look.mark == nil, "\(link)")
        }
        #expect(Self.state(held: true, voice: (.live(.listening), .paused)).line == .onHold)
    }

    /// The call screen passes what its conversation does: working from the session or what the voice handed it and is
    /// still open; a card waiting from the conversation's cards, the card the voice asks about, the session or a run
    /// waiting on the owner.
    @Test func theRealWorkingAndAskingOfTheCallsConversation() {
        func session(_ fields: String) -> Session { VoiceRig.session(#"{"machine_id":"m1","session_id":"s1","title":"t""# + fields + "}") }
        func run(_ state: String) -> VoiceRun {
            VoiceRun(id: "voice-1", task: "x", state: VoiceRunState(wire: state), msgIDs: ["voice-1"], baseSeq: 1, startedAt: nil,
                     code: nil, message: nil, draft: nil, clearable: nil)
        }
        let idle = session(#","status":"idle","state":"idle""#)
        #expect(CallActivity.make(session: idle, cards: false, runs: [], askingAbout: false, thinking: false) == CallActivity())
        #expect(CallActivity.make(session: session(#","status":"working","state":"working""#), cards: false, runs: [], askingAbout: false,
                                  thinking: false).working)
        for state in ["sent", "working", "blocked", "held"] {
            #expect(CallActivity.make(session: idle, cards: false, runs: [run(state)], askingAbout: false, thinking: false).working, "\(state)")
        }
        for state in ["sending", "done", "failed", "refused"] {
            #expect(!CallActivity.make(session: idle, cards: false, runs: [run(state)], askingAbout: false, thinking: false).working, "\(state)")
        }
        #expect(CallActivity.make(session: idle, cards: true, runs: [], askingAbout: false, thinking: false).asking)
        #expect(CallActivity.make(session: idle, cards: false, runs: [], askingAbout: true, thinking: false).asking)
        #expect(CallActivity.make(session: session(#","state":"needs_you""#), cards: false, runs: [], askingAbout: false, thinking: false).asking)
        #expect(CallActivity.make(session: idle, cards: false, runs: [run("needs_you")], askingAbout: false, thinking: false).asking)
        #expect(CallActivity.make(session: nil, cards: false, runs: [], askingAbout: false, thinking: true).thinking)
    }

    // MARK: The look per row and kind

    /// The table, for each kind of picture: the look's own state pictures only for the main conversation's looks,
    /// another conversation's picture always the same and still; the ring and the marks for every kind.
    @Test(arguments: Kind.allCases)
    func eachRowsLook(kind: Kind) {
        let main = kind.isMain
        func look(_ row: Row, muted: Bool = false) -> CallAvatarLook { CallAvatarLook.make(row, muted: muted, kind: kind, motion: true) }

        let connecting = look(.connecting)
        #expect(connecting.state == .idle && connecting.ring == .lap && connecting.clock && connecting.still == !main)
        for row in [Row.listening, .hearing] {
            let l = look(row)
            #expect(l.state == .idle && l.ring == .voice && l.clock && l.mark == nil && l.still == !main, "\(row)")
        }
        let own: [(Row, PersonaState)] = [(.thinking, .thinking), (.working, .working), (.needsYou, .needsYou)]
        for (row, state) in own {
            let l = look(row)
            #expect(l.state == (main ? state : .idle) && l.still == !main && l.ring == .voice && !l.bounces, "\(kind) \(row)")
        }
        let held = look(.held)
        #expect(held.state == .idle && held.still && held.ring == .held && held.mark == .hold && !held.clock)
        #expect(held.eyesShut == (kind == .builtin), "Mochi shuts its eyes on hold")
        #expect(held.dimmed == (kind == .package || kind == .uploaded), "a package's idle frame dims on hold")
        let muted = look(.muted, muted: true)
        #expect(muted.state == .idle && muted.still && muted.ring == .quiet && muted.mark == .muted && !muted.clock)
        let ended = look(.ended)
        #expect(ended.state == (main ? .offline : .idle) && ended.still && ended.ring == .ended && ended.greyed && !ended.clock)
        #expect(!ended.dimmed && !ended.eyesShut && ended.mark == nil)
    }

    /// Speaking: the shared layer (the ring brightening, the bounce and the ripples) for every kind, an Agent's mark and
    /// another conversation's look included; the look's idle picture under it (a package's idle frames play); Mochi's
    /// mouth only for the built-in. Muted, it still speaks, with the muted mark.
    @Test(arguments: Kind.allCases)
    func speakingIsTheSharedLayerForEveryKind(kind: Kind) {
        let look = CallAvatarLook.make(.speaking, muted: false, kind: kind, motion: true)
        #expect(look.ring == .speaking && look.bounces && look.clock && look.followsLevel)
        #expect(look.state == .idle && look.still == !kind.isMain && look.mark == nil)
        #expect(look.mouth == (kind == .builtin), "only Mochi has a mouth to move")
        let muted = CallAvatarLook.make(.speaking, muted: true, kind: kind, motion: true)
        #expect(muted.ring == .speaking && muted.bounces && muted.mark == .muted)
    }

    /// Reduce Motion or Low Power Mode: no bounce, no ripples, no mouth, no clock, every picture still; a bright, still ring
    /// alone says it speaks, and the marks and rings still say the rest.
    @Test(arguments: Kind.allCases)
    func reduceMotionAndLowPowerHoldStill(kind: Kind) {
        for row in Row.allCases {
            let look = CallAvatarLook.make(row, muted: false, kind: kind, motion: false)
            #expect(!look.bounces && !look.mouth && !look.clock && look.still, "\(kind) \(row)")
        }
        #expect(CallAvatarLook.make(.speaking, muted: false, kind: kind, motion: false).ring == .speaking)
        #expect(CallAvatarLook.make(.held, muted: false, kind: kind, motion: false).mark == .hold)
    }

    /// Muted while the conversation works, a card waits or the voice thinks: still, the quiet ring and the muted mark,
    /// the picture's still frame saying the state.
    @Test func mutedKeepsTheStatesStillFrame() {
        for (row, state) in [(Row.working, PersonaState.working), (.needsYou, .needsYou), (.thinking, .thinking)] {
            let look = CallAvatarLook.make(row, muted: true, kind: .package, motion: true)
            #expect(look.state == state && look.still && look.ring == .quiet && look.mark == .muted && !look.clock, "\(row)")
        }
    }

    /// The kind of picture: another conversation's, else the main look's (the built-in, a package, an uploaded one).
    @Test func theKindOfPicture() throws {
        #expect(CallAvatarLook.kind(conversation: true, look: .builtin) == .other)
        #expect(CallAvatarLook.kind(conversation: false, look: .builtin) == .builtin)
        let package = try JSONDecoder().decode(PersonaManifest.self, from: Data(#"""
            {"format":1,"id":"frog","name":"奶蛙","avatar":{"sizes":{"64":"face.png"}},"dot":{"states":{"idle":{"file":"idle.png"}}}}
            """#.utf8))
        let uploaded = try JSONDecoder().decode(PersonaManifest.self, from: Data(#"""
            {"format":1,"id":"me","name":"我","source":{"kind":"upload"},"avatar":{"sizes":{"64":"face.png"}},"dot":{"states":{"idle":{"file":"idle.png"}}}}
            """#.utf8))
        let frog = Persona(id: "frog", name: "奶蛙", subtitle: nil, rev: "r", manifest: package)
        let mine = Persona(id: "me", name: "我", subtitle: nil, rev: "r", manifest: uploaded)
        #expect(CallAvatarLook.kind(conversation: false, look: frog) == .package)
        #expect(CallAvatarLook.kind(conversation: false, look: mine) == .uploaded)
        #expect(CallAvatarLook.kind(conversation: true, look: frog) == .other)
        #expect(CallAssistant.states == PersonaState.allCases, "a package's every state is decoded for the call")
    }

    /// The bar's small avatar: the same state, still (`CallBarFace` makes its look with no motion).
    @Test func theBarsAvatarIsTheSameStateStill() throws {
        for row in Row.allCases {
            let screen = CallAvatarLook.make(row, muted: false, kind: .builtin, motion: true)
            let bar = CallAvatarLook.make(row, muted: false, kind: .builtin, motion: false)
            #expect(bar.state == screen.state && bar.eyesShut == screen.eyesShut && bar.greyed == screen.greyed, "\(row)")
            #expect(bar.still && !bar.bounces && !bar.mouth && !bar.clock, "\(row)")
        }
        let code = try Self.code("Persona/Calls/CallScreen.swift")
        let face = try #require(code.range(of: "private struct CallBarFace"))
        let body = String(code[face.upperBound...].prefix(900))
        #expect(body.contains("motion: false") && body.contains("still: true") && !body.contains("TimelineView"))
        #expect(code.contains("CallBarFace(call: call, side:"), "the bar shows another face")
    }

    // MARK: The shared speaking motion

    /// The envelope rises fast and falls slower; the bounce stays within 1.00–1.04 and a lift of `callAvatarLift` of the
    /// box; a ripple leaves on a peak, at most one per `callRippleGap`, each gone after `callRipple`; a quiet voice makes
    /// none; Mochi's mouth moves in eighths.
    @Test func theSpeakingMotionFollowsTheVoice() {
        var speech = CallSpeech()
        var t = 100.0
        speech.follow(0, at: t)
        #expect(speech.envelope == 0 && speech.scale == 1 && speech.lift == 0 && speech.ripples(at: t).isEmpty)
        // A loud syllable: up within a few frames.
        for _ in 0..<6 {
            t += Theme.Motion.voiceFrame
            speech.follow(1, at: t)
        }
        #expect(speech.envelope > 0.9 && speech.scale <= 1 + Theme.Size.callAvatarBounce && speech.scale > 1.03)
        #expect(speech.lift <= Theme.Size.callAvatarLift && speech.lift > 0)
        #expect(speech.ripples(at: t).count == 1 && speech.ripples(at: t)[0].progress < 0.3, "one ripple on the peak")
        // Held loud: no second ripple until the voice came down.
        for _ in 0..<10 {
            t += Theme.Motion.voiceFrame
            speech.follow(1, at: t)
        }
        #expect(speech.ripples(at: t).count == 1)
        // Down, then up again past the gap: a second one, the first still on its way out.
        for _ in 0..<10 {
            t += Theme.Motion.voiceFrame
            speech.follow(0, at: t)
        }
        #expect(speech.envelope < CallSpeech.rearm)
        for _ in 0..<6 {
            t += Theme.Motion.voiceFrame
            speech.follow(1, at: t)
        }
        #expect(speech.ripples(at: t).count == 2)
        // The ripples fade out and go.
        for _ in 0..<60 {
            t += Theme.Motion.voiceFrame
            speech.follow(0, at: t)
        }
        #expect(speech.ripples(at: t).isEmpty && speech.envelope < 0.01)
        #expect(speech.mouth * 8 == (speech.mouth * 8).rounded())

        // A soft voice never peaks: it bounces a little and sends no ripple.
        var soft = CallSpeech()
        for i in 0..<120 { soft.follow(0.3, at: 200 + Double(i) * Theme.Motion.voiceFrame) }
        #expect(soft.ripples.isEmpty && soft.scale > 1 && soft.scale < 1.02)
        // Peaks closer than the gap: one ripple.
        var fast = CallSpeech()
        var at = 300.0
        for _ in 0..<4 {
            for _ in 0..<3 { at += Theme.Motion.voiceFrame; fast.follow(1, at: at) }
            for _ in 0..<2 { at += Theme.Motion.voiceFrame; fast.follow(0, at: at) }
        }
        #expect(fast.ripples.count <= Int((at - 300) / Theme.Motion.callRippleGap) + 1)
        #expect(fast.ripples.count <= CallSpeech.ripplesAtMost)
    }

    /// The motion runs on the ring's own clock (one `TimelineView`, paused unless its look runs a clock and the avatar is
    /// seen), moves the picture with a transform only, and the avatar is registered as one loop (`ContinuousMotionTests`).
    @Test func theMotionIsOnTheRingsOwnClock() throws {
        let code = try Self.code("Persona/Calls/CallAvatar.swift")
        #expect(code.components(separatedBy: "TimelineView(").count == 2, "one clock")
        #expect(code.contains("TimelineView(.animation(minimumInterval: Theme.Motion.voiceFrame, paused: !ticks))"))
        #expect(code.contains(".scaleEffect(step.scale)") && code.contains(".offset(y: -step.lift * box)"))
        #expect(ContinuousMotionTests.registry["Persona/Calls/CallAvatar.swift"]?.count == 1)
    }

    // MARK: The review: the clock only while the avatar is seen (P9)

    /// The clock ticks only while the look runs one and the avatar is seen: never off screen, under the call's details or
    /// with the app away, whatever the row and the picture.
    @Test(arguments: Kind.allCases)
    func theClockTicksOnlyWhileTheAvatarIsSeen(kind: Kind) {
        for row in Row.allCases {
            for muted in [false, true] {
                let look = CallAvatarLook.make(row, muted: muted, kind: kind, motion: true)
                #expect(!look.ticks(visible: false), "\(kind) \(row) muted \(muted): ticks unseen")
                #expect(look.ticks(visible: true) == look.clock, "\(kind) \(row) muted \(muted)")
            }
        }
    }

    /// While the clock ticks the speaking pose follows the voice, a step a frame; once it stops (the avatar hidden, covered
    /// or the app away) the pose is at rest at once, no step is taken and the voice's level is not read, and it starts
    /// again from rest.
    @Test func aHiddenAvatarTakesNoStep() {
        let clock = CallSpeechClock()
        let speaking = CallAvatarLook.make(.speaking, muted: false, kind: .builtin, motion: true)
        var reads = 0
        var t = 100.0
        for _ in 0..<6 {
            t += Theme.Motion.voiceFrame
            _ = clock.pose(speaking, at: t, level: { reads += 1; return 1 })
        }
        let loud = clock.pose(speaking, at: t + Theme.Motion.voiceFrame, level: { reads += 1; return 1 })
        #expect(reads == 7 && loud.heard == 1 && loud.scale > 1 && loud.lift > 0 && !loud.ripples.isEmpty && (loud.mouth ?? 0) > 0)
        #expect(clock.speech.envelope > 0.9)

        // Hidden: at rest, nothing read, nothing stepped, frame after frame.
        for _ in 0..<5 {
            let hidden = clock.pose(speaking, at: nil, level: { reads += 1; return 1 })
            #expect(hidden == CallAvatarPose(mouth: 0), "Mochi's mouth closed, no bounce, no ripple")
            #expect(clock.speech == .rest)
        }
        #expect(reads == 7, "the level was read while hidden")

        // Seen again: from rest.
        let back = clock.pose(speaking, at: t + 10, level: { reads += 1; return 1 })
        #expect(reads == 8 && back.ripples.isEmpty && clock.speech.envelope < 0.5)

        // A look that does not speak follows the level on its ring and keeps no speaking pose.
        let listening = CallAvatarLook.make(.listening, muted: false, kind: .package, motion: true)
        let ring = clock.pose(listening, at: t + 11, level: { 0.4 })
        #expect(ring == CallAvatarPose(heard: 0.4) && clock.speech == .rest)
        // A held one reads no level at all.
        let held = CallAvatarLook.make(.held, muted: false, kind: .builtin, motion: true)
        let still = clock.pose(held, at: t + 12, level: { reads += 1; return 1 })
        #expect(still == CallAvatarPose() && reads == 8)
    }

    /// The app leaving the front stops the clock and its return starts it (`CallMotionPolicy.foreground`, from the app's
    /// active notifications, the scene's phase for the avatar's visibility reader), observed so the avatar redraws then.
    @Test func theAppAwayStopsTheClock() {
        let center = NotificationCenter()
        let policy = CallMotionPolicy(center: center, lowPower: { false }, active: true)
        #expect(policy.foreground)
        nonisolated(unsafe) var changed = false // the change handler fires once, synchronously, on the write
        withObservationTracking { _ = policy.foreground } onChange: { changed = true }
        center.post(name: UIApplication.willResignActiveNotification, object: nil)
        #expect(!policy.foreground && changed)
        center.post(name: UIApplication.didBecomeActiveNotification, object: nil)
        #expect(policy.foreground)
    }

    /// The avatar reads its own visibility (`VisibleWork`: mounted, on screen in the page's scroll view, the scene active)
    /// with the app in front and the call's details closed, and the clock's pause and the pose go by that alone; the call
    /// screen says when its details cover it.
    @Test func theAvatarsVisibilityInTheSources() throws {
        let avatar = try Self.code("Persona/Calls/CallAvatar.swift")
        #expect(avatar.contains(".environment(\\.scenePhase, policy.foreground ? .active : .inactive)"))
        #expect(avatar.contains("VisibleWork { visible in"))
        #expect(avatar.contains("let ticks = look.ticks(visible: visible && !covered)"))
        #expect(avatar.contains("let now = ticks ? context.date.timeIntervalSinceReferenceDate : nil"))
        #expect(avatar.contains("let step = speech.pose(look, at: now, level: level)"))
        let screen = try Self.code("Persona/Calls/CallScreen.swift")
        #expect(screen.contains("CallScreenMiddle(call: call, state: state, page: height, covered: details != nil)"))
        #expect(screen.contains("CallScreenAvatar(call: call, size: size, covered: covered)"))
        #expect(screen.contains("size: size, covered: covered, interrupt:"))
    }

    // MARK: The review: Low Power Mode as it is switched

    /// Low Power Mode switched on and off during a call, the voice speaking all along: the policy follows the system's
    /// power-state notification at once (observed, so the avatar's look is made again), and the same row's look loses its
    /// bounce, ripples, mouth and clock, then has them back; a package's frames hold still with it.
    @Test func lowPowerModeSwitchedDuringTheCall() {
        let center = NotificationCenter()
        let power = PowerSwitch()
        let policy = CallMotionPolicy(center: center, lowPower: { power.low }, active: true)
        func look(_ kind: Kind) -> CallAvatarLook {
            CallAvatarLook.make(.speaking, muted: false, kind: kind, motion: policy.allows(reduceMotion: false))
        }
        #expect(!policy.lowPower && look(.builtin).bounces && look(.builtin).mouth && look(.package).clock)

        nonisolated(unsafe) var changed = false // the change handler fires once, synchronously, on the write
        withObservationTracking { _ = policy.allows(reduceMotion: false) } onChange: { changed = true }
        power.low = true
        center.post(name: .NSProcessInfoPowerStateDidChange, object: nil)
        #expect(policy.lowPower && changed, "the views that read it are told")
        for kind in Kind.allCases {
            let l = look(kind)
            #expect(!l.bounces && !l.mouth && !l.clock && l.still && l.ring == .speaking, "\(kind): a bright, still ring alone")
        }

        power.low = false
        center.post(name: .NSProcessInfoPowerStateDidChange, object: nil)
        #expect(!policy.lowPower && look(.builtin).bounces && look(.builtin).mouth && look(.package).clock && !look(.package).still)
        #expect(!policy.allows(reduceMotion: true), "Reduce Motion holds still whatever the power")

        // The call's views take it from the policy, never from a one-time read.
        for path in ["Persona/Calls/CallScreen.swift", "Persona/Calls/CallAvatar.swift"] {
            let code = try? Self.code(path)
            let reads = (code ?? "").components(separatedBy: "isLowPowerModeEnabled").count - 1
            #expect(reads == (path.hasSuffix("CallAvatar.swift") ? 1 : 0), "\(path): Low Power Mode read outside CallMotionPolicy")
        }
        let screen = (try? Self.code("Persona/Calls/CallScreen.swift")) ?? ""
        #expect(screen.contains("let motion = policy.allows(reduceMotion: reduceMotion)"))
        let avatar = (try? Self.code("Persona/Calls/CallAvatar.swift")) ?? ""
        #expect(avatar.contains("face(look, still: still || !policy.allows(reduceMotion: reduceMotion))"), "a package's frames")
    }

    // MARK: The review: the avatar's states for VoiceOver

    /// The avatar stays decorative; the call's line says each row in words as its value (the running time stays what it
    /// shows): the conversation thinking, working and waiting for a decision, the voice speaking, the owner speaking,
    /// nobody, muted; nothing more where the line's own words say it. The bar's face says what the conversation does.
    @Test func eachRowInWordsForVoiceOver() {
        let words: [Row: String?] = [
            .connecting: nil, .held: nil, .ended: nil,
            .listening: String(localized: "Listening [Voice Bar Line 226686]"),
            .hearing: String(localized: "Hearing you"),
            .thinking: String(localized: "Thinking [Call Screen]"),
            .speaking: String(localized: "Speaking"),
            .working: StateName.working.text,
            .needsYou: String(localized: "Waiting for your decision [Call Screen]"),
            .muted: String(localized: "Muted [Inbox Mutes View e5e468]"),
        ]
        for row in Row.allCases {
            #expect(row.words == words[row] ?? nil, "\(row)")
            #expect(row.barWords == ([.thinking, .working, .needsYou].contains(row) ? row.words : nil), "\(row)")
        }
        // From what the call screen observes: a card coming, the conversation working, the voice thinking.
        let live = (VoicePhase.live(.listening), VoiceLink.live)
        #expect(Self.state(voice: live, asking: true).avatar.words == String(localized: "Waiting for your decision [Call Screen]"))
        #expect(Self.state(voice: live, working: true).avatar.words == StateName.working.text)
        #expect(Self.state(voice: live, thinking: true).avatar.words == String(localized: "Thinking [Call Screen]"))
        #expect(Self.state(voice: live, working: true).line == .duration(since: Self.since), "the line still shows the running time")
    }

    /// The new words in each language. Working is the state's own name (`StateName.working`, #10502), not a word of the
    /// call screen's: 「工作中」, never the retired 「正在工作」.
    @Test func theRowsWordsInEachLanguage() throws {
        let keys = ["Thinking [Call Screen]", "Working [State Names]", "Waiting for your decision [Call Screen]"]
        let words: [String: [String]] = [
            "en": ["Thinking", "Working", "Waiting for your decision"],
            "zh-Hans": ["正在思考", "工作中", "等你拿主意"], // i18n-ignore: the catalog's words
            "ja": ["考え中", "作業中", "あなたの判断待ち"], // i18n-ignore: the catalog's words
        ]
        for (language, expected) in words.sorted(by: { $0.key < $1.key }) {
            let path = try #require(Bundle.main.path(forResource: language, ofType: "lproj"), "no \(language) strings")
            let bundle = try #require(Bundle(path: path))
            #expect(keys.map { bundle.localizedString(forKey: $0, value: nil, table: nil) } == expected, "\(language)")
        }
    }

    /// The call screen's line carries the row's words as its value and VoiceOver hears a decision waiting as it comes; the
    /// avatar stays hidden from VoiceOver unless it is Interrupt Voice; the bar's face says its words within the bar's.
    @Test func theWordsInTheSources() throws {
        let screen = try Self.code("Persona/Calls/CallScreen.swift")
        #expect(screen.contains("CallScreenStateLine(call: call, line: state.line)"))
        #expect(screen.contains(".accessibilityValue(row.words ?? \"\")"))
        #expect(screen.contains(".onChange(of: row == .needsYou)") && screen.contains("AccessibilityNotification.Announcement(words).post()"))
        #expect(screen.contains(".accessibilityLabel(state.avatar.barWords ?? \"\")"))
        let avatar = try Self.code("Persona/Calls/CallAvatar.swift")
        #expect(avatar.contains(".accessibilityHidden(!isButton)"))
    }

    // MARK: Mochi's call-only drawing

    /// Mochi's default drawing is unchanged: with no mouth and open eyes asked for, every state draws the same pixels as
    /// the face drawn without the call's parameters; the mouth and the shut eyes change pixels only where they are, and
    /// only when a call asks for them.
    @Test func mochisDefaultDrawingIsUnchanged() throws {
        let side: CGFloat = 125
        for scheme in [ColorScheme.light, .dark] {
            for state in PersonaState.allCases {
                let plain = try Self.pixels(PersonaFace(size: side, design: .mochi, state: state), scheme)
                let none = try Self.pixels(PersonaFace(size: side, design: .mochi, state: state, mouth: nil, eyesShut: false), scheme)
                #expect(plain == none, "\(scheme) \(state)")
            }
            let idle = try Self.pixels(PersonaFace(size: side, design: .mochi, state: .idle), scheme)
            for open in [0.0, 1.0] {
                let speaking = try Self.pixels(PersonaFace(size: side, design: .mochi, state: .idle, mouth: open), scheme)
                let changed = Self.changed(idle, speaking)
                #expect(!changed.isEmpty, "no mouth drawn (\(scheme), \(open))")
                let mouth = CGRect(x: (Mochi.mouthCenter.x - Mochi.mouthWidth) * side, y: (Mochi.mouthCenter.y - Mochi.mouthWidth) * side,
                                   width: Mochi.mouthWidth * 2 * side, height: (Mochi.mouthWidth + Mochi.mouthDepth * 2) * side)
                #expect(changed.allSatisfy { mouth.contains($0) }, "the mouth drew outside its place (\(scheme), \(open))")
            }
            let shut = try Self.pixels(PersonaFace(size: side, design: .mochi, state: .idle, eyesShut: true), scheme)
            let changed = Self.changed(idle, shut)
            #expect(!changed.isEmpty, "the eyes did not shut (\(scheme))")
            let r = Mochi.eyeRadius * 2 * side
            #expect(changed.allSatisfy { p in Mochi.eyes.contains { abs($0.x * side - p.x) <= r && abs($0.y * side - p.y) <= r } },
                    "shut eyes drew outside the eyes (\(scheme))")
        }
    }

    /// Every other use of Mochi passes neither parameter: the Live Activity's, the widgets', the dot's, the avatar's.
    @Test func onlyTheCallAsksForTheMouthAndShutEyes() throws {
        for file in ["LiveActivity/FleetPersona.swift", "Shared/HomeWidgets/HomeFace.swift", "Persona/Persona/PersonaViews.swift",
                     "Persona/DesignSystem/ShellComponents.swift"] {
            let code = try Self.code(file)
            #expect(!code.contains("mouth:") && !code.contains("eyesShut:"), "\(file)")
        }
        let calls = try Self.code("Persona/Calls/CallAvatar.swift")
        #expect(calls.contains("mouth: mouth, eyesShut: eyesShut"))
    }

    // MARK: The voice thinking

    /// From the owner's line turning final until the voice plays or says anything, the owner speaks again or
    /// `thinkingAtMost` passes; once per line; never for a late line that came after the answer began.
    @Test func theVoiceThinksBetweenTheOwnersWordsAndItsAnswer() async {
        let rig = VoiceRig()
        let call = rig.call()
        await call.begin()
        let socket = rig.socket!
        socket.ready(rig.key)
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"明天","final":false}"#)
        #expect(!call.thinking, "still speaking")
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"明天几点开会","final":true}"#)
        #expect(call.thinking)
        socket.deliver(#"{"type":"transcript","id":"a1","role":"assistant","text":"十点","final":false}"#)
        #expect(!call.thinking, "the answer began")
        socket.deliver(#"{"type":"transcript","id":"u1","role":"user","text":"明天几点开会","final":true}"#)
        #expect(!call.thinking, "once for each line")

        socket.deliver(#"{"type":"transcript","id":"u2","role":"user","text":"再提醒我一次","final":true}"#)
        #expect(call.thinking)
        rig.audio.emit(.playing(true))
        #expect(!call.thinking, "the voice plays")
        rig.audio.emit(.playing(false))

        socket.deliver(#"{"type":"transcript","id":"u3","role":"user","text":"它到哪了","final":true,"before":"a1"}"#)
        #expect(!call.thinking, "a late line: the answer had begun")

        socket.deliver(#"{"type":"transcript","id":"u4","role":"user","text":"好的","final":true}"#)
        #expect(call.thinking)
        await rig.advance(VoiceConversation.thinkingAtMost.inSeconds, sleepers: 1)
        await rig.waitUntil { !call.thinking }
        #expect(!call.thinking, "no answer came: it listens again")
        call.end(.user)
        #expect(!call.thinking)
    }

    // MARK: Item 5: one name for the main conversation

    /// The call carries the name the main conversation's top bar says (the current assistant's on a hub with several),
    /// not the look's: the page, the bar, the question elsewhere, the urgent call's handle and the system's handle; taken
    /// when the call is made and once more before the Start, then fixed; "Persona" when empty.
    @Test func oneNameForTheMainConversation() async throws {
        let rig = CallRig()
        rig.persona = CallRig.persona(named: "Persona")
        rig.shownName = "助手" // i18n-ignore: a test's assistant name
        let call = await rig.dial()
        #expect(call.name == "助手" && call.title == "助手" && call.assistant.persona.name == "Persona")
        #expect(rig.system.startNames == ["助手"] && rig.system.startHandles.first?.displayName == "助手")
        #expect(rig.calls.liveOwnerCallName == "助手", "the question elsewhere")
        rig.shownName = "工作" // i18n-ignore: a test's assistant name
        rig.system.act(.start, call.id)
        #expect(call.name == "助手", "fixed for the call")
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        #expect(rig.system.rings.last?.name == "工作", "an urgent call rings with the name as it is now")

        // Taken again just before the Start: a roster that came meanwhile counts.
        let late = CallRig()
        late.shownName = "Persona"
        let placed = late.calls.add(kind: .owner(group: late.voice.key.group), direction: .outgoing)
        late.attachEngine(placed)
        late.shownName = "助手" // i18n-ignore: a test's assistant name
        await late.calls.place(placed, origin: .app)
        #expect(late.system.startNames == ["助手"] && placed.name == "助手")

        #expect(CallAssistant(.builtin, name: "  ").name == "Persona" && CallAssistant(.builtin).name == "Persona")
        let code = try Self.code("Persona/Calls/PersonaCalls.swift")
        #expect(code.contains("assistantName: @escaping @MainActor () -> String = { AppModel.shared.assistantShownName }"))
        let bar = try Self.code("Persona/Views/Voice/VoiceReturnBar.swift")
        #expect(bar.contains("name: AppModel.shared.assistantShownName"))
        let entry = try Self.code("Persona/Views/Voice/VoiceEntry.swift")
        #expect(entry.contains("session.assistant ? model.assistantShownName(in: key) : session.agentLabel"), "the voice bar's own name")
    }

    /// A call still being placed keeps the assistant whose main conversation it calls (#9887 review): assistant A's main
    /// conversation decided (the call bound to A, as `callPersona` does once it has the conversation), the owner picks B
    /// while the call waits for the extension; at the Start the screen, the bar, the question elsewhere and the system's
    /// handle still say A's name, with A's look. The same assistant's name and look that came meanwhile (a cold launch's
    /// roster) do count; so does another hub's pick not, nor another assistant's afterwards.
    @Test func aPendingCallKeepsItsAssistantWhenAnotherIsPicked() async {
        let rig = CallRig()
        rig.persona = CallRig.persona(named: "Mochi")
        rig.shownName = "工作" // i18n-ignore: a test's assistant name
        rig.owner = CallAssistant.Owner(group: "g", manager: "work")
        let call = rig.calls.add(kind: .owner(group: rig.voice.key.group), direction: .outgoing)
        // The conversation decided: who the call is with, bound to A.
        #expect(call.assistant.retake(rig.calls.assistant(), name: rig.calls.assistantName(), owner: rig.calls.assistantOwner()))
        rig.attachEngine(call)
        // The owner picks B while the call waits; B's name and B's look.
        rig.owner = CallAssistant.Owner(group: "g", manager: "life")
        rig.shownName = "生活" // i18n-ignore: a test's assistant name
        rig.persona = CallRig.persona(named: "Frog")
        await rig.calls.place(call, origin: .app)
        #expect(rig.system.startNames == ["工作"] && rig.system.startHandles.first?.displayName == "工作")
        #expect(call.name == "工作" && call.title == "工作" && call.assistant.persona.name == "Mochi")
        #expect(rig.calls.liveOwnerCallName == "工作", "the question elsewhere")
        #expect(call.assistant.owner == CallAssistant.Owner(group: "g", manager: "work"))
        // Another hub's assistant of the same id is another assistant too.
        #expect(!call.assistant.retake(.builtin, name: "x", owner: CallAssistant.Owner(group: "h", manager: "work")))
        #expect(call.name == "工作")

        // The same assistant: what came meanwhile is taken (a cold launch's roster and look).
        let same = CallRig()
        same.shownName = "Persona"
        same.owner = CallAssistant.Owner(group: "g", manager: "work")
        let placed = same.calls.add(kind: .owner(group: same.voice.key.group), direction: .outgoing)
        placed.assistant.retake(same.calls.assistant(), name: same.calls.assistantName(), owner: same.calls.assistantOwner())
        same.attachEngine(placed)
        same.shownName = "工作" // i18n-ignore: a test's assistant name
        same.persona = CallRig.persona(named: "Mochi")
        await same.calls.place(placed, origin: .app)
        #expect(same.system.startNames == ["工作"] && placed.name == "工作" && placed.assistant.persona.name == "Mochi")

        // `callPersona` binds it as the conversation is decided, from what it read before asking for it, and before
        // every wait for the extension and the microphone.
        let code = (try? Self.code("Persona/Calls/OwnerCall.swift")) ?? ""
        let who = code.range(of: "let who = (look: assistant(), name: assistantName(), owner: assistantOwner())")
        let key = code.range(of: "let key: SessionKey")
        let bound = code.range(of: "call.assistant.retake(who.look, name: who.name, owner: who.owner)")
        let refresh = code.range(of: "await VoiceSupport.shared.refresh(group: group, api: api)")
        #expect(who != nil && key != nil && bound != nil && refresh != nil)
        if let who, let key, let bound, let refresh {
            #expect(who.lowerBound < key.lowerBound && key.lowerBound < bound.lowerBound && bound.lowerBound < refresh.lowerBound)
        }
    }

    // MARK: The review: the urgent call's header says the call's own name

    /// The urgent call's page says who is calling in its header: the call's own name, taken as it rang and fixed (as its
    /// handle), not the look's ("Mochi") nor a name picked since; the page with no call behind it (the DEBUG sample) says
    /// the name as it is now, "Persona" when empty.
    @Test func theUrgentCallsHeaderSaysTheCallsName() async throws {
        let rig = CallRig()
        rig.persona = CallRig.persona(named: "Mochi")
        rig.shownName = "助手" // i18n-ignore: a test's assistant name
        try await rig.calls.ring(UUID(), callID: "call_1", name: "Urgent")
        let call = try #require(rig.calls.urgentCall("call_1"))
        #expect(call.assistant.persona.name == "Mochi" && rig.system.rings.last?.name == "助手")
        #expect(rig.calls.urgentCallerName("call_1") == "助手")
        rig.shownName = "工作" // i18n-ignore: a test's assistant name
        #expect(rig.calls.urgentCallerName("call_1") == "助手", "fixed for the call, as its handle")
        #expect(rig.calls.urgentCallerName("call_mock") == "工作", "no call behind the page: the name as it is now")
        rig.shownName = " "
        #expect(rig.calls.urgentCallerName("call_mock") == "Persona")
        let page = try Self.code("Persona/Calls/UrgentCallView.swift")
        let header = try #require(page.range(of: "private struct UrgentCallHeader"))
        let body = String(page[header.upperBound...].prefix(1400))
        #expect(body.contains("Text(verbatim: calls.urgentCallerName(session.id))") && body.contains("Text(\"Urgent Call\")"))
    }

    // MARK: Item 6: the voice chat asks before it is replaced

    /// The voice chat in the app on in another conversation (the fallback that survives leaving its page): the phone icon
    /// here asks first, "End the voice chat with <name> and start one here?" (End and Start) where it is the voice chat,
    /// "… and call <this>?" (End and Call) on the iPhone; the yes goes ahead; a live system call's own question first.
    @Test func theVoiceChatAsksBeforeItIsReplaced() {
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: nil, voiceChat: "修登录页", endingCall: false, systemCalls: false)
                == .askVoiceChat(name: "修登录页", call: false))
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: nil, voiceChat: "修登录页", endingCall: false, systemCalls: true)
                == .askVoiceChat(name: "修登录页", call: true))
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: nil, voiceChat: "修登录页", endingCall: true, systemCalls: false) == .inApp)
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: nil, voiceChat: "修登录页", endingCall: true, systemCalls: true) == .systemCall)
        #expect(VoiceEntry.choose(urgent: false, ownCall: false, liveCall: "助手", voiceChat: "修登录页", endingCall: false, systemCalls: true)
                == .ask(name: "助手"))
        #expect(VoiceEntry.choose(urgent: true, ownCall: false, liveCall: nil, voiceChat: "修登录页", endingCall: false, systemCalls: false) == .urgentCall)

        let key = SessionKey(group: "g", machineID: "m2", sessionID: "s9")
        let here = VoiceUnavailableNote.endVoiceChatFirst("修登录页", key: key)
        #expect(here.title == String(localized: "End the voice chat with \("修登录页") and start one here?"))
        #expect(here.proceed == .replaceVoice(key) && here.proceed?.label == String(localized: "End and Start") && here.settings == nil)
        let call = VoiceUnavailableNote.endVoiceChatFirst("修登录页", key: key, call: "周末计划")
        #expect(call.title == String(localized: "End the voice chat with \("修登录页") and call \("周末计划")?"))
        #expect(call.proceed == .callConversation(key) && call.proceed?.label == String(localized: "End and Call"))
    }

    /// The question's words in each language.
    @Test func theVoiceChatsQuestionInEachLanguage() throws {
        let keys = ["End and Start", "End the voice chat with %@ and start one here?", "End the voice chat with %@ and call %@?"]
        let words: [String: [String]] = [
            "en": ["End and Start", "End the voice chat with %@ and start one here?", "End the voice chat with %1$@ and call %2$@?"],
            "zh-Hans": ["结束并开始", "结束与 %@ 的语音对话，在这里开始新的？", "结束与 %1$@ 的语音对话，改为呼叫「%2$@」？"], // i18n-ignore: the catalog's words
            "ja": ["終了して開始", "%@ との音声チャットを終了して、ここで開始しますか？", "%1$@ との音声チャットを終了して「%2$@」に発信しますか？"], // i18n-ignore: the catalog's words
        ]
        for (language, expected) in words.sorted(by: { $0.key < $1.key }) {
            let path = try #require(Bundle.main.path(forResource: language, ofType: "lproj"), "no \(language) strings")
            let bundle = try #require(Bundle(path: path))
            #expect(keys.map { bundle.localizedString(forKey: $0, value: nil, table: nil) } == expected, "\(language)")
        }
    }

    /// The phone icon passes the voice chat that is on elsewhere, and the yes ends it through the same path as a call's.
    @Test func theIconAsksAboutTheVoiceChatInTheSources() throws {
        let entry = try Self.code("Persona/Views/Voice/VoiceEntry.swift")
        #expect(entry.contains("voiceChat: voiceChatElsewhere(key, model: model), endingCall: endingCall"))
        #expect(entry.contains("case .callConversation(let key), .replaceVoice(let key): return await begin(key, model: model, endingCall: true)"))
        #expect(entry.contains("chat.surface != .call, !chat.phase.isEnded, chat.key != key"))
    }

    // MARK: Helpers

    private static func code(_ path: String) throws -> String {
        let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        return try String(contentsOf: apple.appending(path: path), encoding: .utf8).components(separatedBy: "\n")
            .map { l in l.range(of: "//").map { String(l[..<$0.lowerBound]) } ?? l }
            .joined(separator: "\n")
    }

    /// The face drawn at 1× (a pixel per point) in `scheme`: its RGBA bytes and width.
    private static func pixels(_ face: PersonaFace, _ scheme: ColorScheme) throws -> Pixels {
        let renderer = ImageRenderer(content: face.environment(\.colorScheme, scheme))
        renderer.scale = 1
        let image = try #require(renderer.cgImage)
        let width = image.width, height = image.height
        var bytes = [UInt8](repeating: 0, count: width * height * 4)
        let context = try #require(CGContext(data: &bytes, width: width, height: height, bitsPerComponent: 8, bytesPerRow: width * 4,
                                             space: CGColorSpace(name: CGColorSpace.sRGB)!,
                                             bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        context.draw(image, in: CGRect(x: 0, y: 0, width: width, height: height))
        return Pixels(width: width, bytes: bytes)
    }

    private struct Pixels: Equatable {
        var width: Int
        var bytes: [UInt8]
    }

    /// The points where two pictures of the same size differ (more than a rounding's worth), top left first.
    private static func changed(_ a: Pixels, _ b: Pixels) -> [CGPoint] {
        guard a.width == b.width, a.bytes.count == b.bytes.count else { return [CGPoint(x: -1, y: -1)] }
        var out: [CGPoint] = []
        for i in stride(from: 0, to: a.bytes.count, by: 4) {
            let d = (0..<4).map { abs(Int(a.bytes[i + $0]) - Int(b.bytes[i + $0])) }.max() ?? 0
            if d > 2 { out.append(CGPoint(x: (i / 4) % a.width, y: (i / 4) / a.width)) }
        }
        return out
    }
}

/// Low Power Mode as a test switches it (`CallMotionPolicy`'s reader).
@MainActor private final class PowerSwitch {
    var low = false
}

private extension Duration {
    var inSeconds: TimeInterval { Double(components.seconds) + Double(components.attoseconds) / 1e18 }
}
