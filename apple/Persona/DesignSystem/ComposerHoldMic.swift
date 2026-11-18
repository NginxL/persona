#if os(iOS)
import BeautifulUI
import SwiftUI

/// What a held mic is doing, shared by the mic (which takes the finger) and the composer's box (which draws the strip
/// and the circle under the finger over itself, `ComposerHoldOverlay`).
@Observable
final class ComposerHold {
    /// The finger is holding the mic and dictation is on; what is heard waits here, not in the draft.
    private(set) var holding = false
    /// Where the finger is from where it went down.
    var translation: CGSize = .zero
    private(set) var heard = ""
    private(set) var since = Date.now
    /// The mic's frame in the box (`ComposerHold.space`).
    var micFrame: CGRect = .zero
    /// The draft when the hold began; what is heard goes after it.
    @ObservationIgnored private var draft = ""
    @ObservationIgnored private var run = UUID()

    static let space = "composer-hold"

    /// The finger has held long enough: dictation starts, what it hears kept aside until the finger decides. `hints`
    /// tell it the language spoken (`Dictation.start`), after the draft.
    func begin(_ dictation: Dictation, text: Binding<String>, hints: [String] = [], reduceMotion: Bool) {
        // A live system call: a note, and no hold begins (dictation would cut the call's microphone).
        if let refusal = dictation.callRefusal {
            AppModel.shared.toast = Toast(refusal)
            return
        }
        draft = text.wrappedValue
        heard = ""
        translation = .zero
        since = .now
        run = UUID(); let mine = run
        Haptics.tap()
        withAnimation(reduceMotion ? nil : Theme.Motion.holdAppear) { holding = true }
        // A finger let go while the microphone is still asked for has stopped dictation (`finish`), which ends this
        // start before it listens; it also takes over from a tap's start that is waiting or already listening (#3407).
        dictation.start(hints: [draft] + hints, update: { [weak self] words in
            guard let self, self.run == mine else { return }
            self.heard = words
        }, failed: { [weak self] problem in
            AppModel.shared.toast = Toast(problem, style: .error)
            if let self, self.run == mine { self.end(reduceMotion: reduceMotion) }
        })
    }

    func finish(_ outcome: DictationHold.Outcome, _ dictation: Dictation, text: Binding<String>, reduceMotion: Bool) {
        guard holding else { return }
        dictation.stop()
        if outcome == .keep { text.wrappedValue = DictationHold.joined(draft, heard) }
        run = UUID()
        end(reduceMotion: reduceMotion)
    }

    #if DEBUG
    /// `-showDictationHold <points>` (screenshots): the hold as it looks a few seconds in, with the sample's words
    /// (`-showDictationSample`) heard and the finger that far left. Nothing listens.
    func showSample(heard words: String, left: CGFloat) {
        heard = words
        translation = CGSize(width: -left, height: 0)
        since = Date.now.addingTimeInterval(-4)
        holding = true
    }
    #endif

    private func end(reduceMotion: Bool) {
        withAnimation(reduceMotion ? nil : Theme.Motion.holdLeave) {
            holding = false
            translation = .zero
        }
    }
}

/// The shell composer's mic on the iPhone and iPad (#9619): a tap opens the dictation bar, ■ where the mic was;
/// held, it dictates while held (`DictationHold`, `MicPress`), Telegram's voice button without its lock.
struct ComposerHoldMic: View {
    let dictation: Dictation
    @Binding var text: String
    let hold: ComposerHold
    var id: String
    /// What tells dictation the language spoken besides the draft: the owner's recent messages in this chat.
    var hints: () -> [String] = { [] }
    /// What a tap does; nil: dictation into the draft on or off (`Dictation.toggle`).
    var tap: (() -> Void)? = nil
    /// The dictation bar is over it and shows the listening itself: the mic under it does not pulse (a hidden pulse still
    /// stepped the window's animations every frame).
    var quiet = false
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        ComposerMicFace(listening: dictation.isListening && !hold.holding && !quiet)
            // The mic gone under the finger (the page went away, or the box closed to a line of voice and stop), or the
            // touch taken away (a call, a system alert): what was heard goes into the draft (`MicPress`).
            .modifier(MicPress(holds: !dictation.isListening, tap: tapped,
                               began: { hold.begin(dictation, text: $text, hints: hints(), reduceMotion: reduceMotion) },
                               moved: { hold.translation = $0 },
                               ended: { hold.finish($0, dictation, text: $text, reduceMotion: reduceMotion) }))
            .onGeometryChange(for: CGRect.self) { $0.frame(in: .named(ComposerHold.space)) } action: { hold.micFrame = $0 }
            .micAccessibility(listening: dictation.isListening, id: id, tap: tapped)
            .shellScaled()
            #if DEBUG
            .task {
                guard let shown = UserDefaults.standard.string(forKey: "showDictationHold"), let left = Double(shown) else { return }
                try? await Task.sleep(for: .seconds(1)) // once the composer has settled
                hold.showSample(heard: UserDefaults.standard.string(forKey: "showDictationSample") ?? "", left: left)
            }
            #endif
    }

    private func tapped() {
        if let tap { tap() } else { dictation.toggle(into: $text, hints: hints) }
    }
}

/// Over the box while the mic is held: a strip with the recording dot, the time, what is heard so far and "Slide to
/// cancel" following the finger at half its pace; the circle under the finger.
struct ComposerHoldOverlay: View {
    let hold: ComposerHold
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.buiTheme) private var theme

    var body: some View {
        // Each part comes and goes with its own transition: one around them all would hide theirs.
        ZStack(alignment: .topLeading) {
            if hold.holding { strip.transition(.opacity) }
            if hold.holding { circle }
        }
        .allowsHitTesting(false)
        .accessibilityHidden(true)
    }

    private var surface: Color { AppStyle.current.usesBeautifulUI ? theme.colors.surface : Theme.Shell.composer }

    private var strip: some View {
        HStack(spacing: Theme.Space.s) {
            RecordingDot(blinks: !reduceMotion)
            Text(timerInterval: hold.since...Date.distantFuture, countsDown: false)
                .font(Theme.Font.body.monospacedDigit())
                .fixedSize()
            VStack(alignment: .leading, spacing: 0) {
                if !hold.heard.isEmpty {
                    Text(verbatim: hold.heard)
                        .font(Theme.Font.secondary)
                        .lineLimit(1)
                        .truncationMode(.head)
                }
                slideLabel
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .clipped() // the label slides away under the time
            Spacer(minLength: hold.micFrame.width)
        }
        .padding(.leading, Theme.Space.l)
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(surface, in: .rect(cornerRadius: Theme.Shell.composerRadius, style: .continuous))
    }

    /// Telegram's: it follows the finger at half its pace and fades over the last points before the drop.
    private var slideLabel: some View {
        HStack(spacing: Theme.Space.xs) {
            ThemedIcon(Theme.Icon.slideToCancel)
            Text("Slide to cancel").lineLimit(1)
        }
        .font(Theme.Font.secondary)
        .foregroundStyle(.secondary)
        .offset(x: -DictationHold.labelShift(hold.translation))
        .opacity(DictationHold.labelOpacity(hold.translation))
    }

    private var circle: some View {
        let mic = hold.micFrame
        let left = DictationHold.travel(hold.translation)
        return ThemedIcon(Theme.Icon.dictating)
            .font(Theme.Font.title)
            .foregroundStyle(AppStyle.current.usesBeautifulUI ? theme.colors.canvas : Theme.Shell.inkText)
            .frame(width: Theme.Hold.circle, height: Theme.Hold.circle)
            .background(AppStyle.current.usesBeautifulUI ? theme.colors.ink : Theme.Shell.ink, in: .circle)
            .scaleEffect(DictationHold.circleScale(hold.translation))
            .position(x: mic.midX - left, y: mic.midY)
            .transition(.scale(scale: Theme.Motion.holdAppearScale).combined(with: .opacity))
    }
}

/// The red dot of a recording: it blinks on a one-second beat (Telegram's), or stays lit with Reduce Motion.
private struct RecordingDot: View {
    var blinks: Bool
    @State private var dim = false

    var body: some View {
        Circle()
            .fill(Theme.danger)
            .scaledFrame(width: Theme.Size.dotXL, height: Theme.Size.dotXL)
            .opacity(dim ? 0 : 1)
            .onAppear {
                guard blinks else { return }
                withAnimation(Theme.Motion.holdBlink.repeatForever(autoreverses: true)) { dim = true }
            }
    }
}
#endif
