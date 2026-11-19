import SwiftUI

// The voice conversation's tokens (docs/assistant/voice.md「Design system」): how the orb in the voice bar moves and how large it is.
// A file of their own, away from Theme.swift (the CarPlay voice PR edits it too). Shared/, so the watch compiles them;
// it draws no voice UI.

nonisolated extension Theme.Motion {
    /// One way of the orb's breath while it listens: it swells for this long and settles for as long, eased at both
    /// ends. The same as Android's `Voice.kt` (`ListeningBreath`, 1600 ms each way).
    static let voiceBreath: TimeInterval = 1.6
    /// One way of the orb's beat while Persona speaks: quick enough to read as a voice. Android's `SpeakingBeat`
    /// (420 ms each way).
    static let voiceBeat: TimeInterval = 0.42
    /// One lap of the ring around the orb while the call connects: a spinner's pace.
    static let voiceSpin: TimeInterval = 1.1
    /// One lap of the ring around the orb while the agent works: slow, a sign that something goes on, not a wait.
    static let voiceLap: TimeInterval = 2.8
    /// The call screen sliding up over everything, and down again (its own window, `CallWindow`): a sheet's pace. The
    /// window stays this long after the last thing in it went, so the slide is seen.
    static let callScreen = Animation.smooth(duration: 0.35)
    static let callScreenOut: Duration = .milliseconds(400)
    /// How often the minimised call's bar looks again at what is over the app (a sheet, a cover, the + panel), to give it
    /// room under the bar: UIKit says nothing when a page presents another. Only while the bar shows, the app in front.
    static let callBarRoomCheck: Duration = .milliseconds(250)
    /// How often a moving orb is drawn: 30 a second, as `StatusDot` and the avatar's halo; a 1.6 s breath and a level
    /// that changes about 20 times a second gain nothing from more, and a call can run for many minutes with the screen
    /// on. A clock drives it, not a repeating animation (those leaked into the layout of rows around them, `StatusDot`).
    static let voiceFrame: TimeInterval = 1.0 / 30
    /// The call's speaking motion (#9887, `CallSpeech`): the voice's level followed as an envelope, rising within this
    /// (a syllable's onset) and falling within `callSpeechRelease` (so the bounce settles between words, not between
    /// buffers).
    static let callSpeechAttack: TimeInterval = 0.05
    static let callSpeechRelease: TimeInterval = 0.22
    /// A ripple leaving the avatar's edge on a peak of the voice: how long it takes to reach the box's edge and fade, and
    /// the least time between two (a fast talker's every syllable would be a blur).
    static let callRipple: TimeInterval = 1.1
    static let callRippleGap: TimeInterval = 0.45
}

nonisolated extension Theme.Size {
    /// The voice orb's box at the default text size (`scaledFrame` grows it with the text): the sphere and the room it
    /// swells and glows into, more than a finger's target. The voice bar's middle; a larger orb passes its own size.
    static let voiceOrb: CGFloat = 52
    /// The orb's level meter (the BeautifulUI style's five bars) at the default orb: a bar's width, the gap between
    /// two, and the tallest bar. A larger orb scales them with it.
    static let voiceLevelBar: CGFloat = 3
    static let voiceLevelGap: CGFloat = 2
    static let voiceLevelHeight: CGFloat = 16
    /// How much of the conversation a call keeps in sight over the pending cards (`PendingCards`) where the page is short
    /// (large text): about a line of the call's captions, its last row. The card the user has to answer gets the rest.
    static let voiceThreadRoom: CGFloat = 44
    /// The call screen's round controls (#9290: Mute, Speaker or Audio, Resume, End): a circle a thumb finds without
    /// looking, as the Phone app's, with its one-line label under it. Grows with the text (`scaledFrame`).
    static let callControl: CGFloat = 72
    /// The call screen's assistant (`CallAvatar`: its own look, the ring of the voice's level around it), where the
    /// Phone app shows the caller's picture: the box, ring and its swell included.
    static let callAvatar: CGFloat = 160
    /// The most of the call screen's height the assistant takes. It also gives way to the name, the line and the caption
    /// (the controls always keep their room), down to the voice bar's orb's size; where even that does not fit, it is
    /// not drawn.
    static let callAvatarShare: CGFloat = 0.22
    /// The assistant's face inside its box, as a share of the box's side; the rest is the ring's and its swell's.
    static let callAvatarFace: CGFloat = 0.78
    /// The ring around the face at rest, as a share of the box, and how far a full voice swells it (the box's edge).
    static let callAvatarRing: CGFloat = 0.88
    /// The badge on the face while the call is on hold or muted (its pause or slashed microphone), as a share of the box.
    static let callAvatarBadge: CGFloat = 0.24
    /// The call's speaking motion at a full voice (`CallSpeech`, every look): the avatar a little larger (1.00–1.04) and
    /// lifted this share of its box (about four points at the default size); never warped.
    static let callAvatarBounce: CGFloat = 0.04
    static let callAvatarLift: CGFloat = 0.025
    /// The assistant's small face in the bar back to a minimised call (`CallReturnBar`), and in the urgent call's header.
    static let callBarAvatar: CGFloat = 28
    /// An Agent session's mark on a call to it (`CallAgentFace`, #9887 one button), as a share of its round disc (the
    /// face's place on the call screen, the bar's small face).
    static let callAgentMark: CGFloat = 0.5
    /// The system call UI's template icon (`CallIcon`): Apple's 40 points for a call app's icon, drawn at this scale.
    static let callIconTemplate: CGFloat = 40
    static let callIconTemplateScale: CGFloat = 3
}

nonisolated extension Theme {
    /// The words of a system call's pages (#9887 polish, docs/design/DESIGN_SYSTEM.md "Call screen"): how many lines
    /// each takes before it is shortened, and how far it may shrink first. What is shortened stays whole for VoiceOver
    /// and in the call's details (`CallDetailsSheet`), which a tap on the name or the caption opens.
    enum Call {
        /// Who the call is with, under the assistant: two lines at the regular text sizes, three from `nameLargeFrom`
        /// up (about as much of a long title as two lines hold at the default size), each shrinking to `nameShrink`
        /// before its end is cut. A long Agent title reads whole at the default size.
        static let nameLines = 2
        static let nameLinesLarge = 3
        static let nameLargeFrom = DynamicTypeSize.xLarge
        static let nameShrink: CGFloat = 0.8
        /// The latest caption's least room, in lines, kept while it is empty or short (nothing moves as words come); it
        /// takes more where the page has it (what the assistant's face leaves), then is cut at the end; the rest is in
        /// the call's details.
        static let captionLines = 4
        /// A package's or an uploaded picture's idle frame on hold (#9887 avatar states): dimmed to this, still, under the
        /// hold mark (Mochi shuts its eyes instead).
        static let heldPicture = 0.55
    }

    /// The voice conversation's cues (docs/assistant/voice.md 1.2 A / I / M, 6.6): a short sound through the voice's own player (so
    /// echo cancellation takes it out of the microphone too) and a tap on the hand. Played on a change the user did not
    /// make on the screen: connected, a card that wants a tap, the connection picked up again. Quiet beside the voice, and
    /// short enough not to cover a word of it.
    enum VoiceCue: String, CaseIterable, Sendable {
        /// Connected, Listening: two notes up.
        case ready
        /// A card wants a tap (a plan, a permission, a question): two notes up and back, a little higher.
        case attention
        /// The voice's connection dropped and is being picked up: one low note.
        case reconnecting

        /// The notes, in hertz, one after the other.
        var notes: [Double] {
            switch self {
            case .ready: [659.25, 880]
            case .attention: [880, 1174.66, 880]
            case .reconnecting: [440]
            }
        }

        /// How long each note sounds, and the quiet between two.
        static let note: TimeInterval = 0.085
        static let gap: TimeInterval = 0.035
        /// Each note's ends ramp in and out over this much, so it does not click.
        static let ramp: TimeInterval = 0.012
        /// Loudness, a share of full scale: under the voice's own level.
        static let gain: Double = 0.16

        /// What the hand feels with it.
        var haptic: VoiceHaptic {
            switch self {
            case .ready: .tap
            case .attention: .attention
            case .reconnecting: .none
            }
        }
    }

    /// The taps a voice call gives (`Haptics` in the app): with a cue, and on their own where no sound can play (the call
    /// ended by itself: its player is gone).
    enum VoiceHaptic: String, Sendable {
        case none, tap, attention, failure
    }
}
