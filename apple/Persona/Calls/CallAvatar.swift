#if os(iOS)
import SwiftUI
import UIKit

/// The conversation a call talks in when it is not the main one (#9887 one button), as its pages show it, taken when the
/// call is made: its title (what the call screen, the bar and the question elsewhere name), a line saying whose it is
/// (the assistant's name for another assistant conversation; the agent and its machine for an Agent session), and an
/// Agent session's mark (its runtime's, `AgentKind`). The main conversation has none: the assistant's look and name
/// stand for it.
nonisolated struct CallConversation: Equatable, Sendable {
    var title: String
    var detail: String?
    /// An Agent session's mark; nil: an assistant conversation, which wears the assistant's look.
    var agent: AgentKind?

    /// `key`'s conversation as the app has it now; nil for the main conversation, or one the app does not list (it is
    /// shown as the assistant's). Whose it is: the assistant the conversation belongs to, which is not the selected one
    /// for another assistant's conversation opened from the Inbox, a notification or a link (`assistantShownName(in:)`,
    /// as its top bar says). The system's handle stays the selected assistant's name (`CallHandle`).
    @MainActor static func make(_ key: SessionKey, model: AppModel) -> CallConversation? {
        make(session: model.session(key), main: model.assistantMain == key, assistant: model.assistantShownName(in: key),
             machine: model.machines.first { $0.id == key.machineID }?.name)
    }

    /// The same from what the app has: the session, whether it is the main conversation, the assistant's name and the
    /// session's machine's.
    static func make(session: Session?, main: Bool, assistant: String, machine: String?) -> CallConversation? {
        guard !main, let session else { return nil }
        if session.assistant {
            let title = session.title.trimmingCharacters(in: .whitespacesAndNewlines)
            return CallConversation(title: title.isEmpty ? assistant : title, detail: assistant, agent: nil)
        }
        let detail = [session.agentLabel, machine].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ") // i18n-ignore: a separator
        return CallConversation(title: session.displayTitle, detail: detail.isEmpty ? nil : detail, agent: AgentKind(session))
    }
}

/// Who a call is with, as its pages show them (#9290 follow-up): the assistant's look and its name, taken together when
/// the call is made, again as its conversation is decided (bound then to whose main conversation it is: `Owner`), and once
/// more just before the system's Start for that same assistant only (a cold launch's look and roster may have come by
/// then: `PersonaCalls.place`), then fixed for the call, as the system's handle is. The name is the one the main
/// conversation's top bar says (#9887: `AppModel.assistantShownName`, the current assistant's on a hub with several, else
/// the look's), so the call screen, its bar, the question elsewhere and the system's handle say one name. Choosing another
/// avatar, assistant or hub changes neither the face nor the name of a call that is on, nor of one still being placed
/// (#9887 review: it stays the assistant whose conversation it calls). A package's pictures are read and decoded off the
/// main actor (`prepareFrames`, `prepareFace`) and kept here for the call; until they are ready, and for a built-in, the
/// face is the built-in drawing in the look's design. The pages only arrange what is here (docs/apple/performance.md P1,
/// P4, P8).
@MainActor @Observable final class CallAssistant {
    /// Whose main conversation a call is to: the selected hub's group and the assistant picked there (`manager`: its id;
    /// nil, the hub's default or its only one), as the Persona page is (`AppModel.showsPage(of:)`).
    nonisolated struct Owner: Equatable, Sendable {
        var group: String
        var manager: String?
    }

    private(set) var persona: Persona
    /// The name as the main conversation's top bar says it when the call was made (before `CallHandle.name`).
    private(set) var shownName: String
    /// Whose main conversation the call is to, once its conversation was decided; nil before.
    @ObservationIgnored private(set) var owner: Owner?
    /// The package's dot frames for every state the call screen shows (`states`), once decoded.
    private(set) var frames: [PersonaState: PersonaFrames] = [:]
    /// The package's face for the urgent call's header and the voice chat's bar, once decoded.
    private(set) var face: UIImage?
    /// The pixel width the face was read for: a larger one is read again (the text grew).
    @ObservationIgnored private var faceWidth: CGFloat = 0
    @ObservationIgnored private var loadingFrames = false

    /// The states the call screen shows (`CallAvatarLook.state`): all five of a package's dot (a state it lacks shows its
    /// idle frames, as `PersonaDot` does).
    static let states: [PersonaState] = PersonaState.allCases

    /// The name the system's call UI, the call screen and the bar say (`CallHandle.name`: "Persona" when it is empty).
    var name: String { CallHandle.name(shownName) }

    /// `name`: what the main conversation's top bar says; nil: the look's own.
    init(_ persona: Persona, name: String? = nil) {
        self.persona = persona
        shownName = name ?? persona.name
    }

    /// The look and name taken once more, together; its pictures go with the old look. `owner`: whose main conversation
    /// they are; the first one given binds the call to it (as its conversation is decided), and a later one taken for
    /// another assistant or hub (picked while the call waited for the hub, the extension or the microphone) changes
    /// nothing: false. Without `owner` (a DEBUG sample's look) it is taken as it is.
    @discardableResult func retake(_ persona: Persona, name: String? = nil, owner: Owner? = nil) -> Bool {
        if let owner {
            if let bound = self.owner, bound != owner { return false }
            self.owner = owner
        }
        let name = name ?? persona.name
        if name != shownName { shownName = name }
        guard persona != self.persona else { return true }
        self.persona = persona
        frames = [:]
        face = nil
        faceWidth = 0
        return true
    }

    /// Reads and decodes the package's frames for the call's states off the main actor, once. Nothing for a built-in.
    func prepareFrames(_ store: PersonaStore) async {
        let look = persona
        guard !look.isBuiltin, frames.isEmpty, !loadingFrames else { return }
        loadingFrames = true
        let loaded = await store.decodedFrames(look, Self.states)
        loadingFrames = false
        guard persona == look else { return }
        frames = loaded
    }

    /// Reads the package's face `points` wide at `scale` off the main actor, prepared for display. Nothing for a built-in,
    /// nor when the one here is large enough.
    func prepareFace(_ store: PersonaStore, points: CGFloat, scale: CGFloat) async {
        let look = persona, width = points * scale
        guard !look.isBuiltin, width > faceWidth else { return }
        faceWidth = width
        let image = await store.decodedFace(look, points: points, scale: scale)
        guard persona == look else { return }
        if let image { face = image } else { faceWidth = 0 }
    }
}

/// How the call's avatar looks in each state of the call (pure, for the tests; #9887, docs/apple/calls.md "The avatar"):
/// which of the look's own states it shows (`PersonaState`, as the Persona tab's dot does), whether that picture holds
/// still, the ring around it, the mark on it, and the speaking layer every call shares.
///
/// - The **shared speaking layer**, the same for every look and every conversation (Mochi, a package, an uploaded photo,
///   an Agent's mark): while the assistant speaks the avatar bounces gently with the voice's level (`CallSpeech`: a
///   little larger and lifted, never warped), the ring brightens with it, and soft ripples leave its edge on the voice's
///   peaks. Reduce Motion and Low Power Mode keep none of the motion: a bright, still ring says it speaks.
/// - The **look's own pictures**, the main conversation's only: its thinking, working, needs-you and offline states (a
///   package's frames, Mochi's drawing), Mochi's mouth moving with the voice and its eyes shut on hold, a package's idle
///   frame dimmed on hold. A call to another conversation (an Agent's harness mark, another assistant conversation's look)
///   keeps the same still picture: only the shared layer, the ring and the marks move or change on it.
nonisolated struct CallAvatarLook: Equatable, Sendable {
    /// What the call is doing as the avatar shows it, the first that applies (`CallScreenState.make`).
    nonisolated enum Row: String, CaseIterable, Sendable {
        /// Calling…, Connecting…, Reconnecting….
        case connecting
        /// The call's own hold (the system's pause, an interruption): never the voice's pause.
        case held
        case ended
        /// The assistant's voice plays (muted or not: the owner still hears it).
        case speaking
        /// A card in the call's conversation waits for the owner's decision (asked by voice or not).
        case needsYou
        /// The owner stopped speaking and the voice has not begun to answer (`VoiceConversation.thinking`).
        case thinking
        /// The call's conversation works on something (its session, or what the voice handed it).
        case working
        /// The owner's microphone is off and nothing else goes on.
        case muted
        /// The owner is speaking.
        case hearing
        /// Nobody speaks.
        case listening
    }

    /// Whose picture it is: the main conversation's look (the built-in Mochi, drawn; a package's frames; a picture the owner
    /// uploaded, the hub's frames of it), or another conversation's (`other`: an Agent session's harness mark, another
    /// assistant conversation's look), which has no pictures of its own for a call's states.
    nonisolated enum Kind: String, CaseIterable, Sendable {
        case builtin, package, uploaded, other

        var isMain: Bool { self != .other }
    }

    nonisolated enum Ring: Equatable, Sendable {
        /// An arc lapping a faint track: the call connects (Calling…, Connecting…, Reconnecting…).
        case lap
        /// The persona's color, swelling with the microphone's level.
        case voice
        /// The persona's color, bright, swelling and brightening with the assistant's own voice; bright and still where
        /// motion is off (it says the assistant speaks).
        case speaking
        /// On hold: still, in the caution color.
        case held
        /// Muted: still and gray.
        case quiet
        /// Over: faint gray.
        case ended
    }

    nonisolated enum Mark: Equatable, Sendable {
        case hold, muted
    }

    /// The look's own state the picture shows (a main look's; another conversation's stays idle).
    var state: PersonaState
    /// The picture holds still (its first frame, Mochi's still drawing).
    var still: Bool
    var ring: Ring
    var mark: Mark?
    /// The shared speaking motion: the bounce and the ripples (`CallSpeech`).
    var bounces = false
    /// Mochi's mouth follows the voice (the built-in main look while it speaks).
    var mouth = false
    /// Mochi's eyes shut (the built-in main look on hold).
    var eyesShut = false
    /// The picture dimmed (a package's or an uploaded picture's idle frame on hold).
    var dimmed = false
    /// The picture greyed (the call is over).
    var greyed = false
    /// The ring runs on its own clock (a level to follow, or the lap): only while motion is allowed.
    var clock = false

    /// `row` as `kind`'s avatar shows it; `muted`: the owner's microphone is off (a mark, and the picture still, unless the
    /// assistant speaks); `motion`: neither Reduce Motion nor Low Power Mode.
    static func make(_ row: Row, muted: Bool, kind: Kind, motion: Bool) -> CallAvatarLook {
        let main = kind.isMain
        // Another conversation's picture is always the same one, still: only the main look has pictures for the states.
        var look = CallAvatarLook(state: .idle, still: !main || !motion, ring: .voice, mark: nil)
        switch row {
        case .connecting:
            look.ring = .lap
        case .listening, .hearing:
            break
        case .thinking:
            if main { look.state = .thinking }
        case .working:
            if main { look.state = .working }
        case .needsYou:
            if main { look.state = .needsYou }
        case .speaking:
            // Its idle picture under the shared motion; Mochi's mouth moves with the voice.
            look.ring = .speaking
            look.bounces = motion
            look.mouth = kind == .builtin && motion
            if muted { look.mark = .muted }
        case .muted:
            break
        case .held:
            look.still = true
            look.ring = .held
            look.mark = .hold
            look.eyesShut = kind == .builtin
            look.dimmed = kind == .package || kind == .uploaded
        case .ended:
            if main { look.state = .offline }
            look.still = true
            look.ring = .ended
            look.greyed = true
        }
        // Muted: still, the quiet ring and the mark, whatever the look says it is doing; its voice still moves it.
        if muted, [.listening, .hearing, .thinking, .working, .needsYou, .muted].contains(row) {
            look.still = true
            look.ring = .quiet
            look.mark = .muted
        }
        look.clock = motion && [.lap, .voice, .speaking].contains(look.ring)
        return look
    }

    /// The kind of the call's picture: another conversation's, or the main look's.
    static func kind(conversation: Bool, look: Persona) -> Kind {
        if conversation { return .other }
        if look.isBuiltin { return .builtin }
        return look.isUploaded ? .uploaded : .package
    }

    /// The ring follows a level (the microphone's, or the assistant's voice while it speaks).
    var followsLevel: Bool { ring == .voice || ring == .speaking }

    /// Whether the avatar's clock ticks (P9): its look runs one, and the avatar is seen (`visible`: on screen, the page in
    /// front of the call's details, the app in front). Otherwise it is paused and the speaking pose rests.
    func ticks(visible: Bool) -> Bool { clock && visible }
}

@MainActor extension CallAvatarLook {
    /// The kind of `call`'s picture.
    static func kind(_ call: PersonaCall) -> Kind {
        kind(conversation: call.conversation != nil, look: call.assistant.persona)
    }

    /// How opaque the picture is: greyed once over (as an offline face), dimmed on hold.
    var pictureOpacity: Double { greyed ? PersonaFace.offlineOpacity : dimmed ? Theme.Call.heldPicture : 1 }
}

/// The shared speaking motion (#9887, every look, every conversation): the voice's level followed as an envelope (up
/// within `callSpeechAttack`, down within `callSpeechRelease`), the avatar's bounce riding it (a scale of 1.00–1.04 and a
/// lift of a few points, `callAvatarBounce`, `callAvatarLift`), and a ripple leaving the avatar's edge each time the
/// envelope peaks (at most one per `callRippleGap`, each `callRipple` long). Pure: one step per frame of the ring's own
/// clock; the tests hold it to the tokens.
nonisolated struct CallSpeech: Equatable, Sendable {
    /// 0…1: the level followed.
    private(set) var envelope: Double = 0
    /// When each ripple left the edge, oldest first.
    private(set) var ripples: [TimeInterval] = []
    private var last: TimeInterval?
    /// A peak may start a ripple: the envelope came down since the last one.
    private var armed = true

    /// At rest: no level heard yet, no ripple.
    static let rest = CallSpeech()

    /// The envelope at which a ripple leaves, and the one it must come back under before the next.
    static let peak = 0.5
    static let rearm = 0.3
    static let ripplesAtMost = 3

    /// The level at `t` (seconds, any epoch). A step from long ago counts as at most a quarter second.
    mutating func follow(_ raw: Double, at t: TimeInterval) {
        let level = min(max(raw, 0), 1)
        let dt = last.map { min(max(t - $0, 0), 0.25) } ?? 0
        last = t
        let time = level > envelope ? Theme.Motion.callSpeechAttack : Theme.Motion.callSpeechRelease
        envelope += (level - envelope) * (1 - exp(-dt / time))
        ripples.removeAll { t - $0 >= Theme.Motion.callRipple || t < $0 }
        if envelope < Self.rearm {
            armed = true
        } else if armed, envelope >= Self.peak, ripples.last.map({ t - $0 >= Theme.Motion.callRippleGap }) ?? true {
            ripples.append(t)
            if ripples.count > Self.ripplesAtMost { ripples.removeFirst() }
            armed = false
        }
    }

    /// The avatar's scale and its lift (a share of its box).
    var scale: Double { 1 + Theme.Size.callAvatarBounce * envelope }
    var lift: Double { Theme.Size.callAvatarLift * envelope }
    /// Each ripple at `t`, by when it left: how far it went, 0 (at the avatar's edge) to 1 (at the box's, faded out).
    func ripples(at t: TimeInterval) -> [Ripple] {
        ripples.map { Ripple(id: $0, progress: min(1, max(0, (t - $0) / Theme.Motion.callRipple))) }
    }

    nonisolated struct Ripple: Equatable, Identifiable, Sendable {
        /// When it left the edge.
        var id: TimeInterval
        var progress: Double
    }
    /// Mochi's mouth: the envelope in eighths, so the face is drawn again only when the mouth visibly moves.
    var mouth: Double { (envelope * 8).rounded() / 8 }
}

/// The speaking motion's state between frames (`CallSpeech`), kept by the avatar and stepped on its ring's clock. Not
/// observed: nothing redraws because it changed, only the clock's frames read it.
@MainActor final class CallSpeechClock {
    private(set) var speech = CallSpeech.rest

    /// The avatar's pose at `now` (nil: the clock does not tick, `CallAvatarLook.ticks`: still, off screen, under the
    /// call's details, the app away): the voice's level read (only then, and only while the ring follows one) and, while
    /// the look speaks with motion, followed one step; otherwise at rest at once, the level not read and no step taken,
    /// so the motion starts from rest when the avatar is seen again.
    func pose(_ look: CallAvatarLook, at now: TimeInterval?, level: () -> Double) -> CallAvatarPose {
        let quiet: Double? = look.mouth ? 0 : nil
        guard let now else {
            if speech != .rest { speech = .rest }
            return CallAvatarPose(mouth: quiet)
        }
        let heard = look.followsLevel ? min(1, max(0, level())) : 0
        guard look.bounces || look.mouth else {
            if speech != .rest { speech = .rest }
            return CallAvatarPose(heard: heard, mouth: quiet)
        }
        speech.follow(heard, at: now)
        let mouth = look.mouth ? speech.mouth : nil
        guard look.bounces else { return CallAvatarPose(heard: heard, mouth: mouth) }
        return CallAvatarPose(heard: heard, scale: speech.scale, lift: speech.lift, ripples: speech.ripples(at: now), mouth: mouth)
    }
}

/// The avatar in one frame of its clock: the level its ring follows, its bounce (scale and lift, a share of its box), the
/// ripples on their way out, and Mochi's mouth (nil: none drawn).
nonisolated struct CallAvatarPose: Equatable, Sendable {
    var heard: Double = 0
    var scale: Double = 1
    var lift: Double = 0
    var ripples: [CallSpeech.Ripple] = []
    var mouth: Double?
}

/// Whether the call's motion may run, as the system has it now (#9887 review): Low Power Mode and the app in front, each
/// read once and again as the system says it changed (`NSProcessInfoPowerStateDidChange`, the app becoming active or
/// resigning it), so the call's motion stops (and comes back) the moment the owner switches Low Power Mode or leaves the
/// app, not at the next change of the call. Observed: the views that read it (`CallScreenAvatar`'s look, `CallAvatar`'s
/// clock, `CallAssistantDot`'s frames) redraw then, and nothing else.
@MainActor @Observable final class CallMotionPolicy {
    static let shared = CallMotionPolicy()

    private(set) var lowPower: Bool
    private(set) var foreground: Bool
    @ObservationIgnored private let readLowPower: @MainActor () -> Bool
    @ObservationIgnored private var observers: [NSObjectProtocol] = []

    /// `center`, `lowPower` and `active`: the system's (the tests' own).
    init(center: NotificationCenter = .default,
         lowPower: @escaping @MainActor () -> Bool = { ProcessInfo.processInfo.isLowPowerModeEnabled },
         active: Bool = UIApplication.shared.applicationState == .active) {
        readLowPower = lowPower
        self.lowPower = lowPower()
        foreground = active
        // The power state is posted on a queue of the system's; delivered on the main one here.
        observers.append(center.addObserver(forName: .NSProcessInfoPowerStateDidChange, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.powerChanged() }
        })
        observers.append(center.addObserver(forName: UIApplication.didBecomeActiveNotification, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.setForeground(true) }
        })
        observers.append(center.addObserver(forName: UIApplication.willResignActiveNotification, object: nil, queue: .main) { @Sendable [weak self] _ in
            MainActor.assumeIsolated { self?.setForeground(false) }
        })
    }

    /// Motion is allowed: neither Reduce Motion (`reduceMotion`, the view's environment) nor Low Power Mode.
    func allows(reduceMotion: Bool) -> Bool { !reduceMotion && !lowPower }

    private func powerChanged() {
        let now = readLowPower()
        if now != lowPower { lowPower = now }
    }

    private func setForeground(_ now: Bool) {
        if now != foreground { foreground = now }
    }
}

/// The assistant on the call screen: the call's own look (`CallAssistant`, as the app's dot draws it: Mochi drawn, or a
/// package's or an uploaded picture's frames, decoded off the main actor), or an Agent session's mark, showing the call's
/// state (`CallAvatarLook`), a thin ring around it following the voice, and while the assistant speaks the shared speaking
/// layer: the avatar's bounce and the ripples leaving its edge. One clock, the ring's own, drives the ring, the ripples
/// and the avatar's transform (its scale and lift); the picture itself is not drawn again for them (Mochi's mouth alone
/// redraws Mochi as it moves), so a new level redraws the ring and moves the picture and nothing else
/// (docs/apple/performance.md P4, P5). The clock runs only while the ring has something to follow or lap, motion is
/// allowed and the avatar is seen (P9, `CallAvatarLook.ticks`): paused with Reduce Motion, in Low Power Mode (followed as
/// it is switched, `CallMotionPolicy`), when the look is still, when the avatar is off screen (scrolled away, the page gone:
/// `VisibleWork`, its own visibility reader), under the call's details (`covered`), and while the app is not in front
/// (`CallMotionPolicy.foreground`, the scene's phase for that reader);
/// the speaking pose rests then and the voice's level is not read. While the assistant speaks and `interrupt` is given it
/// is a button, Interrupt Voice, as the voice bar's orb; otherwise it is decoration (the line under the name says the
/// state in words, its value what the avatar shows: `CallScreenStateLine`).
struct CallAvatar: View {
    let look: CallAvatarLook
    /// Who the call is with: the look and name it was made with (never the one chosen since).
    let assistant: CallAssistant
    /// A call to an Agent session (#9887 one button): its mark in the assistant's place.
    var agent: AgentKind?
    /// 0…1, the voice's level, read only on the ring's clock.
    var level: () -> Double = { 0 }
    /// The box at the default text size; it grows with the text like every `Theme.Size`.
    var size: CGFloat = Theme.Size.callAvatar
    /// Something covers the page (the call's details over it): the clock stops as if it were off screen.
    var covered = false
    /// What a tap does while the assistant speaks: stop the voice. Given only then; without it this is never a button.
    var interrupt: (() -> Void)?

    @Environment(\.dynamicTypeSize) private var typeSize
    @State private var speech = CallSpeechClock()
    @State private var policy = CallMotionPolicy.shared

    private var isButton: Bool { interrupt != nil }

    var body: some View {
        let box = size * Theme.sizeScale(typeSize)
        let face = box * Theme.Size.callAvatarFace
        Button { if isButton { interrupt?() } } label: {
            VisibleWork { visible in
                let ticks = look.ticks(visible: visible && !covered)
                TimelineView(.animation(minimumInterval: Theme.Motion.voiceFrame, paused: !ticks)) { context in
                    let now = ticks ? context.date.timeIntervalSinceReferenceDate : nil
                    let step = speech.pose(look, at: now, level: level)
                    ZStack {
                        if !step.ripples.isEmpty { CallAvatarRipples(ripples: step.ripples, box: box, face: face) }
                        CallAvatarRing(look: look, lap: now.map { $0.truncatingRemainder(dividingBy: Theme.Motion.voiceSpin) / Theme.Motion.voiceSpin },
                                       level: step.heard, box: box)
                        ZStack {
                            CallAvatarPicture(look: look, assistant: assistant, agent: agent, size: face, mouth: step.mouth)
                            if let mark = look.mark {
                                CallAvatarMark(mark: mark, side: box * Theme.Size.callAvatarBadge)
                                    .offset(x: face * 0.36, y: face * 0.36)
                                    .transition(.opacity)
                            }
                        }
                        .scaleEffect(step.scale)
                        .offset(y: -step.lift * box)
                    }
                }
            }
            // The scene's phase for the avatar's visibility reader and its picture's is the app's, as the policy follows
            // it: the call's window is UIKit's own over the app's scene (`CallWindow`), where SwiftUI's own may not say it.
            .environment(\.scenePhase, policy.foreground ? .active : .inactive)
            .frame(width: box, height: box)
            .contentShape(.circle)
        }
        .buttonStyle(Press())
        .disabled(!isButton)
        .allowsHitTesting(isButton)
        .accessibilityHidden(!isButton)
        .accessibilityLabel("Interrupt Voice")
        .accessibilityHint("Stops only the voice; the work in progress carries on")
        .accessibilityIdentifier("call-avatar")
    }

    /// As it is, enabled or not (the picture says the state); pressed, it dims as a plain button does.
    private struct Press: ButtonStyle {
        func makeBody(configuration: Configuration) -> some View {
            configuration.label.opacity(configuration.isPressed ? 0.7 : 1)
        }
    }
}

/// The avatar's picture in the call's look: an Agent session's mark (still, the same picture whatever the call does), or
/// the assistant's (`CallAssistantDot`) in the look's state, with Mochi's mouth and shut eyes where the look has them,
/// greyed once over and dimmed on hold. Its inputs change only with the call's state (and Mochi's mouth), so the ring's
/// clock moves it without drawing it again.
private struct CallAvatarPicture: View {
    let look: CallAvatarLook
    let assistant: CallAssistant
    let agent: AgentKind?
    let size: CGFloat
    let mouth: Double?

    var body: some View {
        Group {
            if let agent {
                CallAgentFace(agent: agent, size: size)
            } else {
                CallAssistantDot(assistant: assistant, size: size, state: look.state, still: look.still, mouth: mouth,
                                 eyesShut: look.eyesShut)
            }
        }
        .opacity(look.pictureOpacity)
    }
}

/// The ripples leaving the avatar's edge on the voice's peaks: circles from the picture's edge out to the box's, fading as
/// they go (`CallSpeech.ripples(at:)`).
private struct CallAvatarRipples: View {
    let ripples: [CallSpeech.Ripple]
    let box: CGFloat
    let face: CGFloat

    var body: some View {
        ZStack {
            ForEach(ripples) { ripple in
                let p = ripple.progress
                Circle()
                    .stroke(Theme.persona.opacity(0.5 * (1 - p)), lineWidth: Theme.Size.ruleWide * (1 - 0.5 * p))
                    .frame(width: face + (box - face) * p, height: face + (box - face) * p)
            }
        }
        .frame(width: box, height: box)
        .accessibilityHidden(true)
    }
}

/// The ring around the assistant: the part that follows the voice's level, drawn from what the avatar's clock gives it.
private struct CallAvatarRing: View {
    let look: CallAvatarLook
    /// How far round the lap is (nil: still).
    let lap: Double?
    let level: Double
    let box: CGFloat

    var body: some View {
        ring
            .frame(width: box, height: box)
            .accessibilityHidden(true)
    }

    @ViewBuilder private var ring: some View {
        let rest = box * Theme.Size.callAvatarRing
        // A full voice swells the ring to the box's edge, its widest stroke inside it (the page's scroll view clips what
        // reaches past the box).
        let swell = box / (rest + Theme.Size.ruleWide * 2) - 1
        switch look.ring {
        case .lap:
            ZStack {
                Circle().stroke(Theme.persona.opacity(0.2), lineWidth: Theme.Size.ruleWide)
                Circle()
                    .trim(from: 0, to: VoiceOrb.arc)
                    .stroke(Theme.persona, style: StrokeStyle(lineWidth: Theme.Size.ruleWide, lineCap: .round))
                    .rotationEffect(.degrees((lap ?? 0) * 360 - 90))
            }
            .frame(width: rest, height: rest)
        case .voice:
            // At rest a faint ring; a full voice swells it to the box's edge, brighter and a little wider.
            Circle()
                .stroke(Theme.persona.opacity(0.35 + 0.65 * level), lineWidth: Theme.Size.ruleWide * (1 + level))
                .frame(width: rest, height: rest)
                .scaleEffect(1 + level * swell)
        case .speaking:
            // Brighter than a listening ring at rest, and brightening with the voice; bright and still without a clock.
            Circle()
                .stroke(Theme.persona.opacity(look.clock ? 0.6 + 0.4 * level : 1), lineWidth: Theme.Size.ruleWide * (1 + level))
                .frame(width: rest, height: rest)
                .scaleEffect(1 + level * swell)
        case .held:
            Circle().stroke(Theme.caution, lineWidth: Theme.Size.ruleWide).frame(width: rest, height: rest)
        case .quiet:
            Circle().stroke(Theme.Hue.gray.opacity(0.6), lineWidth: Theme.Size.ruleWide).frame(width: rest, height: rest)
        case .ended:
            Circle().stroke(Theme.Hue.gray.opacity(0.3), lineWidth: Theme.Size.ruleWide).frame(width: rest, height: rest)
        }
    }
}

/// The call's assistant in a state, as `PersonaDot` draws one but from what `CallAssistant` has ready: a package's frames
/// once decoded (in a UIImageView, as the dot's; a state it has no frames for shows its idle ones), else the built-in
/// drawing in the look's design, with the call's own expressions (`mouth`, `eyesShut`) where the call asks for them.
/// Nothing is read or decoded here: the frames are asked for in a task, off the main actor. Holds still with Reduce
/// Motion and in Low Power Mode (as it is switched: `CallMotionPolicy`), and when the call says so.
struct CallAssistantDot: View {
    let assistant: CallAssistant
    let size: CGFloat
    let state: PersonaState
    var still = false
    /// Mochi's mouth, open by the voice (`PersonaFace.mouth`); nil: none.
    var mouth: Double? = nil
    /// Mochi's eyes shut (on hold).
    var eyesShut = false
    @Environment(AppModel.self) private var model
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var policy = CallMotionPolicy.shared

    var body: some View {
        let look = assistant.persona
        face(look, still: still || !policy.allows(reduceMotion: reduceMotion))
            .frame(width: size, height: size)
            .accessibilityHidden(true)
            .task(id: look) { await assistant.prepareFrames(model.personas) }
    }

    @ViewBuilder private func face(_ look: Persona, still: Bool) -> some View {
        if !look.isBuiltin, let frames = assistant.frames[state] {
            VisibleWork { eligible in
                PersonaFramesView(frames: frames, animating: !still, eligible: eligible)
            }
        } else {
            AssistantAvatar(size: size, design: look.avatarDesign, state: state, still: still, mouth: mouth, eyesShut: eyesShut)
        }
    }
}

/// The call's assistant's small face (the return bar, the urgent call's header), as `PersonaAvatar` draws one but from
/// what `CallAssistant` has ready: a package's picture once read off the main actor, else the built-in drawing.
struct CallAssistantFace: View {
    let assistant: CallAssistant
    let size: CGFloat
    @Environment(AppModel.self) private var model
    @Environment(\.displayScale) private var scale

    var body: some View {
        let look = assistant.persona
        face(look)
            .frame(width: size, height: size)
            .accessibilityHidden(true)
            .task(id: CallFaceRequest(look: look, side: size * scale)) {
                await assistant.prepareFace(model.personas, points: size, scale: scale)
            }
    }

    @ViewBuilder private func face(_ look: Persona) -> some View {
        if !look.isBuiltin, let image = assistant.face {
            Image(uiImage: image)
                .resizable()
                .interpolation(.high)
                .scaledToFill()
                .frame(width: size, height: size)
                .clipShape(.circle)
        } else {
            AssistantAvatar(size: size, design: look.avatarDesign, state: .idle)
        }
    }
}

/// An Agent session's face on a call to it (#9887 one button): its runtime's mark (Claude's, Codex's, TRAE's; else the
/// agent's letter), as the Agents page draws it (`AgentMark`), on the well's round disc, `size` points across (already
/// scaled with the text). Holds still: the ring around it says what the call does.
struct CallAgentFace: View {
    let agent: AgentKind
    let size: CGFloat

    var body: some View {
        ZStack {
            if let brand = agent.brand {
                BrandMark(brand, side: size * Theme.Size.callAgentMark, scaled: false).foregroundStyle(.secondary)
            } else {
                Text(verbatim: agent.letter)
                    .font(.system(size: size * Theme.Size.callAgentMark * 0.7, weight: .semibold, design: .rounded))
                    .foregroundStyle(.secondary)
            }
        }
        .frame(width: size, height: size)
        .background(Theme.Fill.well, in: .circle)
        .accessibilityHidden(true)
    }
}

/// What a face is read for: the look and its side in pixels.
private nonisolated struct CallFaceRequest: Hashable, Sendable {
    var look: Persona
    var side: CGFloat
}

/// The mark on a still face: a pause on hold (the caution color), a slashed microphone while muted (gray), as the
/// voice bar's orb wears them.
private struct CallAvatarMark: View {
    let mark: CallAvatarLook.Mark
    let side: CGFloat

    var body: some View {
        ThemedIcon(mark == .hold ? Theme.Icon.pause : Theme.Icon.voiceMute)
            .font(.system(size: side * 0.5, weight: .semibold))
            .foregroundStyle(Theme.onSolid)
            .frame(width: side, height: side)
            .background(mark == .hold ? Theme.caution : Theme.Hue.gray, in: .circle)
            .overlay { Circle().strokeBorder(Theme.pageBackground, lineWidth: Theme.Size.rule) }
            .accessibilityHidden(true)
    }
}
#endif
