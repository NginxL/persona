#if os(iOS)
import SwiftUI

// The call to Persona's own page (#9290, docs/design/DESIGN_SYSTEM.md "Call screen"): full screen over the app, as the
// Phone app's in-call screen, because a call is one task going on with its controls always under the thumb; Minimize
// leaves it for a bar at the top that returns to it, and the call goes on (`PersonaCalls` runs it, not this page).

/// What the call screen says and how the voice looks (the orb's phase), and what the avatar shows the call doing
/// (`avatar`, which `CallAvatarLook` draws in the call's look), from the call's state and what its conversation and voice
/// do (pure, for the tests).
nonisolated struct CallScreenState: Equatable, Sendable {
    nonisolated enum Line: Equatable, Sendable {
        /// Outgoing, the system asked to start it.
        case calling
        /// Started, waiting for the audio and the voice's first `ready`.
        case connecting
        /// How long it has been on (a running timer).
        case duration(since: Date)
        case onHold
        /// The voice's connection to the hub dropped and is being picked up again.
        case reconnecting
        /// Over, and why in a line (nil: hung up).
        case ended(note: String?)

        /// Over: no more words come, so the caption and its room go (why it ended gets the room).
        var isEnded: Bool { if case .ended = self { true } else { false } }
    }

    var line: Line
    var orb: VoiceOrb.Phase
    /// What the avatar shows the call doing (#9887 avatar states).
    var avatar: CallAvatarLook.Row
    /// The owner's microphone is off (the avatar's mark).
    var microphoneOff: Bool

    init(line: Line, orb: VoiceOrb.Phase, avatar: CallAvatarLook.Row? = nil, muted: Bool = false) {
        self.line = line
        self.orb = orb
        self.avatar = avatar ?? Self.row(orb)
        microphoneOff = muted
    }

    /// `voice`: the call's voice once it began, its phase and link (a sample has none: `reconnecting` stands in);
    /// `activity`: what the call's conversation and voice do beyond that (`CallActivity`: it works, a card waits, the
    /// voice thinks). In a call only the call's own hold (`held`) is a hold: the voice's pause (the extension closing the
    /// upstream while nobody speaks, or while the conversation works) and its waking are the call listening as ever, or
    /// working while its conversation works, and the line stays the running time.
    static func make(phase: CallPhase, held: Bool, muted: Bool, note: String?, voice: (phase: VoicePhase, link: VoiceLink)?,
                     reconnecting: Bool = false, activity: CallActivity = CallActivity()) -> CallScreenState {
        switch phase {
        case .dialing, .ringing:
            return CallScreenState(line: .calling, orb: .connecting, muted: muted)
        case .connecting:
            return CallScreenState(line: .connecting, orb: .connecting, muted: muted)
        case .ended:
            return CallScreenState(line: .ended(note: note), orb: .ended, muted: muted)
        case .connected(let since):
            if held { return CallScreenState(line: .onHold, orb: .paused, muted: muted) }
            let dropped: Bool = {
                guard let voice else { return reconnecting }
                if case .reconnecting = voice.phase { return true }
                return voice.link == .reconnecting
            }()
            if dropped { return CallScreenState(line: .reconnecting, orb: .connecting, muted: muted) }
            let orb = voice.map { voice in
                let link: VoiceLink = voice.link == .paused || voice.link == .waking ? .live : voice.link
                return VoiceBarStatus.make(phase: voice.phase, link: link, muted: muted, working: activity.working).orb
            } ?? (muted ? .muted : activity.working ? .working : .listening)
            let row: CallAvatarLook.Row = switch orb {
            case .connecting, .idle: .connecting
            case .paused: .held
            case .ended: .ended
            case .speaking: .speaking
            case .listening, .hearing, .working, .muted:
                activity.asking ? .needsYou : activity.thinking ? .thinking : activity.working ? .working
                    : muted ? .muted : orb == .hearing ? .hearing : .listening
            }
            return CallScreenState(line: .duration(since: since), orb: orb, avatar: row, muted: muted)
        }
    }

    /// The avatar's row for the orb alone (nothing known of the conversation).
    private static func row(_ orb: VoiceOrb.Phase) -> CallAvatarLook.Row {
        switch orb {
        case .idle, .connecting: .connecting
        case .paused: .held
        case .ended: .ended
        case .speaking: .speaking
        case .working: .working
        case .muted: .muted
        case .hearing: .hearing
        case .listening: .listening
        }
    }
}

/// What the call's conversation and its voice do, beyond the call's own state (#9887 avatar states): the conversation
/// works (its session, or what the voice handed it and is still open), a card in it waits for the owner (asked by voice or
/// on the screen), the voice is thinking (`VoiceConversation.thinking`). Pure (`make`); the pages read it in small views of
/// their own (`read`), which redraw as these change and nothing around them.
nonisolated struct CallActivity: Equatable, Sendable {
    var working = false
    var asking = false
    var thinking = false

    /// From the conversation (`session`, whether it has pending `cards`) and the voice (its `runs`, the card it is
    /// `askingAbout`, `thinking`): as the voice bar decides working (a run sent, working, waiting outside or answered and
    /// still at work) and the Persona tab's dot decides Needs You (a pending card, the session waiting on the owner).
    static func make(session: Session?, cards: Bool, runs: [VoiceRun], askingAbout: Bool, thinking: Bool) -> CallActivity {
        let open = runs.filter(\.state.isOpen)
        let working = session?.status == .working || session?.state == .working
            || open.contains { [.sent, .working, .blocked, .held].contains($0.state) }
        let asking = cards || askingAbout || session?.state == .needsYou || open.contains { $0.state == .needsYou }
        return CallActivity(working: working, asking: asking, thinking: thinking)
    }
}

extension CallActivity {
    /// `call`'s now: its voice's, and its conversation's as the app has it. Nothing before the call connected.
    @MainActor static func read(_ call: PersonaCall, model: AppModel) -> CallActivity {
        #if DEBUG
        if let sample = call.sampleActivity { return sample }
        #endif
        guard let voice = call.voice, call.connectedSince != nil else { return CallActivity() }
        return make(session: model.session(voice.key), cards: !model.interactions(for: voice.key).isEmpty, runs: voice.runs,
                    askingAbout: voice.askingID != nil, thinking: voice.thinking)
    }
}

/// The call to Persona's page, laid out as the Phone app's in-call screen: Minimize at the top; in the top third the
/// assistant, its own look (moving with the call, a ring of the voice's level around it: `CallAvatar`), its name and
/// right under it where the call is, then its latest words; the round controls low, End centred at the bottom.
/// VoiceOver's escape minimises. When the call ends its line says why for a moment (the call stays
/// `PersonaCalls.endedStays`), then the page goes. Minimize and the controls always keep their room: the middle takes
/// what they leave (`CallScreenMiddle`). A tap on the name or the caption opens their whole text (`CallDetailsSheet`).
struct CallScreen: View {
    let call: PersonaCall
    @State private var calls = PersonaCalls.shared
    /// The page's height, for the orb's size (`orbSize`).
    @State private var height: CGFloat = 0
    /// The call's details, open, and what was tapped to open them.
    @State private var details: CallDetailsSheet.Opened?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        let state = Self.state(call)
        VStack(spacing: Theme.Space.l) {
            HStack(spacing: Theme.Space.s) {
                minimise
                Spacer(minLength: 0)
            }
            CallScreenMiddle(call: call, state: state, page: height, covered: details != nil) { details = $0 }
            CallScreenControls(call: call, route: calls.route)
        }
        .padding(.horizontal, Theme.Space.page)
        .padding(.vertical, Theme.Space.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if abs(height - h) >= 1 { height = h }
        }
        .background { Theme.pageBackground.ignoresSafeArea() }
        .animation(reduceMotion ? nil : Theme.Motion.state, value: state)
        .accessibilityElement(children: .contain)
        .accessibilityAction(.escape) { calls.minimise() }
        .accessibilityIdentifier("call-screen")
        .modifier(CallAnnouncements(state: state))
        .sheet(item: $details) { opened in CallDetailsSheet(call: call, opened: opened) }
    }

    /// How many lines the name takes at `size` before it is shortened (`Theme.Call`): more at the larger text sizes, so
    /// they show about as much of a long title as the default size does.
    static func nameLines(_ size: DynamicTypeSize) -> Int {
        size >= Theme.Call.nameLargeFrom ? Theme.Call.nameLinesLarge : Theme.Call.nameLines
    }

    /// The assistant's size at the default text size (`CallAvatar` grows it by `scale`): `callAvatar`, but at most
    /// `callAvatarShare` of the page's height and `free` (the middle's height less its words and the gap to them) once
    /// grown. Nil where not even the voice bar's orb's size fits: the words say where the call is, the face gives way.
    static func avatarSize(page: CGFloat, free: CGFloat, scale: CGFloat) -> CGFloat? {
        guard page > 0, scale > 0 else { return Theme.Size.callAvatar }
        let points = min(Theme.Size.callAvatar * scale, page * Theme.Size.callAvatarShare, free)
        guard points >= Theme.Size.voiceOrb * scale else { return nil }
        return points / scale
    }

    /// `call`'s state; `activity`: what its conversation and voice do (`CallActivity.read`), for the avatar.
    static func state(_ call: PersonaCall, activity: CallActivity = CallActivity()) -> CallScreenState {
        var reconnecting = false
        var voice = call.voice.map { (phase: $0.phase, link: $0.link) }
        #if DEBUG
        reconnecting = call.sampleReconnecting
        if voice == nil, let live = call.sampleVoice { voice = (phase: .live(live), link: call.sampleLink) }
        #endif
        return CallScreenState.make(phase: call.phase, held: call.held, muted: call.muted, note: call.note, voice: voice,
                                    reconnecting: reconnecting, activity: activity)
    }

    private var minimise: some View {
        Button {
            Haptics.tap()
            calls.minimise()
        } label: {
            ThemedIcon(Theme.Icon.callMinimise)
                .font(Theme.Font.heading)
                .foregroundStyle(Theme.Shell.ink)
                .scaledFrame(width: Theme.Size.wellL, height: Theme.Size.wellL)
                .background(Theme.Shell.roundMuted, in: .circle)
                .tapTargetFrame()
                .contentShape(.circle)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("Minimize [Call Screen]")
        .accessibilityHint("The call goes on; the bar at the top returns to it")
        .accessibilityIdentifier("call-minimise")
    }
}

/// The middle of the call screen, in the height Minimize and the controls leave it, from its top: the assistant, its
/// name and the call's line right under it, then the latest caption, which takes the room left over the controls. The
/// name and the line come first; the caption has at least `Theme.Call.captionLines` lines' room (kept while it is empty
/// or short, so nothing moves as the assistant's words come); the face gives way to them (`CallScreen.avatarSize`), down
/// to the voice bar's orb's size and then altogether (a short phone at the largest text size); what the face leaves the
/// caption has too. Where the words alone do not fit, they scroll, and the controls stay. Once the call is over the
/// caption goes, and why it ended has its room. The heights it goes by (the middle's, the words', a caption line's)
/// follow the page and the text size, never what is drawn from them (docs/apple/performance.md P7).
private struct CallScreenMiddle: View {
    let call: PersonaCall
    let state: CallScreenState
    /// The page's height (the orb takes a share of it at most).
    let page: CGFloat
    /// The call's details are open over the page: the avatar's clock stops (P9).
    let covered: Bool
    /// The name or the caption tapped: their whole text.
    let open: (CallDetailsSheet.Opened) -> Void
    /// The height the middle has, what the name and the line take of it, and the caption's least room.
    @State private var room: CGFloat = 0
    @State private var who: CGFloat = 0
    @State private var least: CGFloat = 0
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let scale = Theme.sizeScale(typeSize)
        let ended = state.line.isEnded
        let caption = ended ? 0 : least + Theme.Space.section
        let free = room > 0 ? room - who - caption - Theme.Space.l : .infinity
        let size = CallScreen.avatarSize(page: page, free: free, scale: scale)
        ScrollView {
            VStack(spacing: Theme.Space.section) {
                VStack(spacing: Theme.Space.l) {
                    if let size {
                        CallScreenAvatar(call: call, size: size, covered: covered)
                    }
                    VStack(spacing: Theme.Space.s) {
                        CallScreenName(call: call, lines: CallScreen.nameLines(typeSize)) { open(.name) }
                        CallScreenStateLine(call: call, line: state.line)
                    }
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
                        if abs(who - h) >= 1 { who = h }
                    }
                }
                if !ended {
                    let face = size.map { $0 * scale + Theme.Space.l } ?? 0
                    let left = (room - face - who - Theme.Space.section).rounded(.down)
                    CallCaptionLine(call: call, height: room > 0 && least > 0 ? max(least, left) : nil,
                                    least: { h in if abs(least - h) >= 1 { least = h } }) { open(.caption) }
                }
            }
            .frame(maxWidth: .infinity, minHeight: room, alignment: .top)
        }
        .scrollBounceBehavior(.basedOnSize)
        .scrollIndicators(.hidden)
        .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { h in
            if abs(room - h) >= 1 { room = h }
        }
    }
}

/// Who the call is with (`PersonaCall.title`): the assistant's name for the main conversation, as its top bar, the
/// system's call UI and Recents say it; another conversation's title, and whose it is under it (#9887 one button). The name wraps to
/// `lines`, shrinking a little first, then its end is cut; whose it is stays one line, cut in the middle (an agent's and
/// a machine's names, both ends tell). VoiceOver reads both whole, and a tap opens them whole (`CallDetailsSheet`).
private struct CallScreenName: View {
    let call: PersonaCall
    let lines: Int
    let open: () -> Void

    var body: some View {
        VStack(spacing: Theme.Space.s) {
            Text(verbatim: call.title)
                .font(Theme.Font.display)
                .lineLimit(lines)
                .minimumScaleFactor(Theme.Call.nameShrink)
                .multilineTextAlignment(.center)
                .accessibilityAddTraits(.isHeader)
                .accessibilityAction(named: Text("Call Details"), open)
                .accessibilityIdentifier("call-title")
            if let detail = call.conversation?.detail {
                Text(verbatim: detail)
                    .font(Theme.Font.secondary)
                    .foregroundStyle(.secondary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                    .accessibilityIdentifier("call-detail")
            }
        }
        .contentShape(.rect)
        .onTapGesture(perform: open)
    }
}

/// The assistant at the call screen's size showing what the call does (`CallAvatarLook` from the call's state and what its
/// conversation and voice do, read here so only this view redraws as they change: docs/apple/performance.md P5), its
/// ring following the voice (its levels: the microphone's while it listens, the assistant's own while it speaks). A tap
/// while it speaks stops the voice, as the voice bar's orb does. Motion follows Reduce Motion and Low Power Mode as they
/// are switched (`CallMotionPolicy`: the look is made again the moment Low Power Mode changes, whatever the call does).
private struct CallScreenAvatar: View {
    let call: PersonaCall
    let size: CGFloat
    /// The call's details over the page (`CallAvatar.covered`).
    let covered: Bool
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var policy = CallMotionPolicy.shared

    var body: some View {
        let state = CallScreen.state(call, activity: .read(call, model: model))
        let motion = policy.allows(reduceMotion: reduceMotion)
        let look = CallAvatarLook.make(state.avatar, muted: state.microphoneOff, kind: CallAvatarLook.kind(call), motion: motion)
        let speaking = state.avatar == .speaking
        let voice = call.voice
        #if DEBUG
        let sample: () -> Double = { [call] in call.sampleLevel }
        #else
        let sample: () -> Double = { 0 }
        #endif
        CallAvatar(look: look, assistant: call.assistant, agent: call.conversation?.agent, level: { [levels = voice?.levels] in
            guard let levels else { return sample() }
            return Double(speaking ? levels.output : levels.input)
        }, size: size, covered: covered, interrupt: speaking ? voice.map { voice in { voice.interruptSpeech() } } : nil)
    }
}

/// The call's line on the call screen with what the avatar shows in words, for VoiceOver (#9887 review: the avatar is
/// decorative, and the line shows the running time whatever the call does): its value is the avatar's row
/// (`CallAvatarLook.Row.words`: listening, hearing you, thinking, speaking, working, waiting for your decision, muted),
/// and VoiceOver hears "Waiting for your decision" once as a card comes to wait for the owner. What the call's
/// conversation and voice do is read here, so only the line redraws as it changes (docs/apple/performance.md P5).
private struct CallScreenStateLine: View {
    let call: PersonaCall
    let line: CallScreenState.Line
    @Environment(AppModel.self) private var model

    var body: some View {
        let row = CallScreen.state(call, activity: .read(call, model: model)).avatar
        CallStateLine(line: line)
            .accessibilityValue(row.words ?? "")
            .onChange(of: row == .needsYou) { _, waiting in
                if waiting, let words = row.words { AccessibilityNotification.Announcement(words).post() }
            }
    }
}

extension CallAvatarLook.Row {
    /// What the avatar shows the call doing, in words (VoiceOver's value of the call's line, #9887 review); nil where the
    /// line's own words say it (Calling…, Connecting…, Reconnecting…, On Hold, Call ended).
    var words: String? {
        switch self {
        case .connecting, .held, .ended: nil
        case .listening: String(localized: "Listening [Voice Bar Line 226686]")
        case .hearing: String(localized: "Hearing you")
        case .thinking: String(localized: "Thinking [Call Screen]")
        case .speaking: String(localized: "Speaking")
        case .working: StateName.working.text // the state's own name, as every screen says it (StateNamesTests)
        case .needsYou: String(localized: "Waiting for your decision [Call Screen]")
        case .muted: String(localized: "Muted [Inbox Mutes View e5e468]")
        }
    }

    /// What the minimised bar's face says to VoiceOver: what the conversation does (the bar's own words and marks say the
    /// rest: the running time, muted, on hold).
    var barWords: String? { [.thinking, .working, .needsYou].contains(self) ? words : nil }
}

/// The line under the name: Calling…, Connecting…, the running time, On Hold, Reconnecting…, or how it ended. It
/// wraps, whole: why a call ended can be a long sentence.
struct CallStateLine: View {
    let line: CallScreenState.Line

    var body: some View {
        CallStateWords(line: line)
            .font(Theme.Font.body)
            .foregroundStyle(.secondary)
            .multilineTextAlignment(.center)
            .fixedSize(horizontal: false, vertical: true)
            .accessibilityIdentifier(Self.isDuration(line) ? "call-duration" : "call-state")
    }

    private static func isDuration(_ line: CallScreenState.Line) -> Bool {
        if case .duration = line { true } else { false }
    }
}

/// The words of the call's line, for the page and the call's details.
struct CallStateWords: View {
    let line: CallScreenState.Line

    var body: some View {
        switch line {
        case .calling: Text("Calling… [Call Screen]")
        case .connecting: Text("Connecting…")
        case .duration(let since):
            Text(since, style: .timer)
                .monospacedDigit()
        case .onHold: Text("On Hold [Call Screen]")
        case .reconnecting: Text("Reconnecting…")
        case .ended(let note):
            if let note, !note.isEmpty { Text("Call ended · \(note)") } else { Text("Call ended") }
        }
    }
}

/// Persona's latest words (or the owner's, as they come), in the room the middle gives them: at least
/// `Theme.Call.captionLines` lines, as many more as the page has, cut at the end. The call stays useful when looked at.
/// Its own view: captions change with every word, and only this redraws. VoiceOver does not hear them as they come
/// (they would talk over the voice); they read whole when touched. A tap opens every caption of the call
/// (`CallDetailsSheet`).
private struct CallCaptionLine: View {
    let call: PersonaCall
    /// The room it has; nil until the middle has measured itself (then `captionLines` lines).
    let height: CGFloat?
    /// Its least room, `captionLines` lines of its font at this text size, measured for the middle.
    let least: (CGFloat) -> Void
    let open: () -> Void

    var body: some View {
        let text = caption
        Text(text ?? " ")
            .font(Theme.Font.secondary)
            .foregroundStyle(.secondary)
            .lineLimit(height == nil ? Theme.Call.captionLines : nil)
            .multilineTextAlignment(.center)
            .frame(maxWidth: Theme.Size.readable)
            .frame(height: height, alignment: .top)
            .overlay(alignment: .top) {
                Text(verbatim: " ")
                    .font(Theme.Font.secondary)
                    .lineLimit(Theme.Call.captionLines, reservesSpace: true)
                    .fixedSize(horizontal: false, vertical: true)
                    .hidden()
                    .onGeometryChange(for: CGFloat.self) { $0.size.height } action: { least($0) }
            }
            .opacity(text == nil ? 0 : 1)
            .contentShape(.rect)
            .onTapGesture { if text != nil { open() } }
            .accessibilityHidden(text == nil)
            .accessibilityAction(named: Text("Call Details"), open)
            .accessibilityIdentifier("call-caption")
    }

    private var caption: String? {
        if let last = call.voice?.captions.last(where: { !$0.text.isEmpty }) { return last.text }
        #if DEBUG
        return call.sampleCaption
        #else
        return nil
        #endif
    }
}

/// Mute, Speaker (or Audio, the system's route picker, while Bluetooth, a car or headphones are there) and Resume when
/// the call offers it in a row, End under them; where the three do not fit one row (a narrow window at a large text
/// size: iPad's Split View or Slide Over), Mute and Speaker over Resume and End, as the urgent call's page does. Each
/// goes through `PersonaCalls`.
private struct CallScreenControls: View {
    let call: PersonaCall
    let route: CallRoute
    @State private var calls = PersonaCalls.shared

    var body: some View {
        let live = call.phase.isLive
        ViewThatFits(in: .horizontal) {
            VStack(spacing: Theme.Space.xl) {
                HStack(alignment: .top, spacing: Theme.Space.xl) {
                    mute
                    speaker
                    resume
                }
                end
            }
            VStack(spacing: Theme.Space.l) {
                HStack(alignment: .top, spacing: Theme.Space.xl) {
                    mute
                    speaker
                }
                HStack(alignment: .top, spacing: Theme.Space.xl) {
                    resume
                    end
                }
            }
        }
        .disabled(!live)
    }

    private var end: some View {
        CallControl(symbol: Theme.Icon.voiceEnd, label: String(localized: "End"), id: "call-end", kind: .end,
                    hint: String(localized: "Hangs up the call")) {
            calls.hangUp(call.id)
        }
    }

    private var mute: some View {
        CallControl(symbol: call.muted ? Theme.Icon.voiceUnmute : Theme.Icon.voiceMute, label: String(localized: "Mute [Voice Bar 7db2e1]"),
                    id: "call-mute", kind: .toggle(on: call.muted)) {
            calls.setMuted(!call.muted, call.id)
        }
    }

    @ViewBuilder private var speaker: some View {
        if route.external {
            CallAudioRouteControl(route: route)
        } else {
            CallControl(symbol: Theme.Icon.callSpeaker, label: String(localized: "Speaker [Call Control]"), id: "call-speaker",
                        kind: .toggle(on: route.speakerOn)) {
                calls.setSpeaker(!route.speakerOn)
            }
        }
    }

    @ViewBuilder private var resume: some View {
        if call.resumeOffered {
            CallControl(symbol: Theme.Icon.resume, label: String(localized: "Resume [Call Control]"), id: "call-resume",
                        hint: String(localized: "Takes the call off hold")) {
                calls.resume(call.id)
            }
        }
    }
}

/// VoiceOver hears where the call went when it changes by itself: on hold, picked up again, ended.
private struct CallAnnouncements: ViewModifier {
    let state: CallScreenState

    func body(content: Content) -> some View {
        content.onChange(of: Self.word(state.line)) { _, word in
            if let word { AccessibilityNotification.Announcement(word).post() }
        }
    }

    private static func word(_ line: CallScreenState.Line) -> String? {
        switch line {
        case .onHold: String(localized: "On Hold [Call Screen]")
        case .reconnecting: String(localized: "Reconnecting…")
        case .ended(let note):
            if let note, !note.isEmpty { String(localized: "Call ended · \(note)") } else { String(localized: "Call ended") }
        case .calling, .connecting, .duration: nil
        }
    }
}

// MARK: The bar back to a minimised call

/// While the call to Persona is on and its page minimised: a pill at the top of every page, under the status bar, in
/// the positive role's tint (a call that is on), the assistant's small face, "<name> · 03:12" (the name cut in the
/// middle where it has to be, the time never: `CallBarWords`) and a mark for muted or on hold. A tap opens the call
/// screen again. Nothing else: hanging up is on the page, the voice bar and the system's call UI. It lives in the call's
/// own window (`CallWindow`), over every page and sheet, and the pages under it get its room.
struct CallReturnPill: View {
    let call: PersonaCall
    let open: () -> Void
    @Environment(\.dynamicTypeSize) private var typeSize

    var body: some View {
        let state = CallScreen.state(call)
        Button {
            Haptics.tap()
            open()
        } label: {
            HStack(spacing: Theme.Space.s) {
                CallBarFace(call: call, side: Theme.Size.callBarAvatar * Theme.sizeScale(typeSize))
                CallBarWords(title: call.title) {
                    CallBarLine(line: state.line)
                }
                if call.held {
                    ThemedIcon(Theme.Icon.pause).font(Theme.Font.captionStrong).foregroundStyle(Theme.caution)
                        .accessibilityHidden(true)
                } else if call.muted {
                    ThemedIcon(Theme.Icon.voiceMute).font(Theme.Font.captionStrong).foregroundStyle(.secondary)
                        .accessibilityLabel("Muted [Inbox Mutes View e5e468]")
                }
            }
            .padding(.horizontal, Theme.Space.l)
            .tapTargetHeight()
            .surface(tint: Theme.softTint(Theme.positive), interactive: true, in: .capsule)
            .contentShape(.capsule)
        }
        .buttonStyle(.plain)
        .accessibilityElement(children: .combine)
        .accessibilityHint("Returns to the call")
        .accessibilityAddTraits(.isButton)
        .accessibilityIdentifier("call-return-bar")
    }
}

/// The bar's small avatar (#9887 avatar states): the call screen's, in the same state but still. The assistant's look shows
/// its state's still picture (Mochi's thinking bubble, working arc, needs-you ring, its eyes shut on hold, asleep once
/// over; a package's first frame of the state, dimmed on hold), greyed once over; an Agent session's mark, or another
/// conversation's look, stays the same picture. No motion and no clock: the bar sits over every page for the whole call,
/// where a moving face would pull the eye from what the owner reads and keep a clock running on every page, and at its
/// size a bounce or a mouth would not read; the call screen moves. What its conversation does is read here, so only this
/// redraws as it changes, and said to VoiceOver within the bar's words (thinking, working, waiting for your decision:
/// `CallAvatarLook.Row.barWords`).
private struct CallBarFace: View {
    let call: PersonaCall
    let side: CGFloat
    @Environment(AppModel.self) private var model

    var body: some View {
        let state = CallScreen.state(call, activity: .read(call, model: model))
        let look = CallAvatarLook.make(state.avatar, muted: state.microphoneOff, kind: CallAvatarLook.kind(call), motion: false)
        Group {
            if let agent = call.conversation?.agent {
                CallAgentFace(agent: agent, size: side)
            } else {
                CallAssistantDot(assistant: call.assistant, size: side, state: look.state, still: true, eyesShut: look.eyesShut)
            }
        }
        .opacity(look.pictureOpacity)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(state.avatar.barWords ?? "")
        .accessibilityHidden(state.avatar.barWords == nil)
    }
}

/// A call bar's words (#9887 polish): who the call is with, then "·" and the running time (or where the call is). The
/// time is never cut; the name takes what is left, cut in the middle where it has to be (both ends of a long title tell
/// more than its start alone). VoiceOver reads the name whole. The minimised call's bar and the voice chat's.
struct CallBarWords<Line: View>: View {
    let title: String
    @ViewBuilder let line: Line

    var body: some View {
        HStack(spacing: Theme.Space.xs) {
            Text(verbatim: title)
                .truncationMode(.middle)
                .accessibilityIdentifier("call-bar-title")
            Group {
                Text(verbatim: "·") // i18n-ignore: a separator
                    .accessibilityHidden(true)
                line
            }
            .fixedSize()
        }
        .font(Theme.Font.secondaryStrong)
        .foregroundStyle(Color.primary)
        .lineLimit(1)
    }
}

/// The bar's short form of the call's line: the running time, Calling…, Connecting…, On Hold, Reconnecting….
private struct CallBarLine: View {
    let line: CallScreenState.Line

    var body: some View {
        switch line {
        case .calling: Text("Calling… [Call Screen]")
        case .connecting: Text("Connecting…")
        case .duration(let since): Text(since, style: .timer).monospacedDigit()
        case .onHold: Text("On Hold [Call Screen]")
        case .reconnecting: Text("Reconnecting…")
        case .ended: Text("Call ended")
        }
    }
}

#endif
