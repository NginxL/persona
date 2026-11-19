#if os(iOS)
import BeautifulUI
import SwiftUI

/// The bar's start boundary, also allowing a unit test to fail or count a start without opening the microphone.
@MainActor protocol DictationStarting {
    var heard: String { get }
    /// Why a start now would be refused (a live call, #9290 follow-up), nil while it may.
    var callRefusal: String? { get }
    func start(hints: [String], update: @escaping (String) -> Void, failed: @escaping (String) -> Void)
    func finish(settling: Bool) async -> String
    func stop(settling: Bool)
}

extension Dictation: DictationStarting {}

extension DictationStarting {
    /// A stand-in (tests' fakes) asks the gate itself; `Dictation` has its own, which tests can replace.
    var callRefusal: String? { LiveCallGate.dictationRefusal }
}

/// The composer's mic tapped (not held): the composer's capsule turns into ChatGPT's dictation bar, [✕] the wave and
/// ■ ↑. ✕ drops what was heard; ■ puts it into the draft and sends nothing; ↑ puts it into the draft and sends. ■ is
/// where the mic was, so a second tap there stops without sending (#9619). ■ and ↑ wait for the recognizer's last words
/// ("Transcribing", a spinner on the button pressed). The draft typed before stays and what was heard goes after it
/// (`DictationHold.joined`). Held, the mic dictates while held (`ComposerHoldMic`).
@Observable
final class DictationBarModel {
    /// The bar is showing.
    private(set) var on = false
    /// ■ or ↑ was pressed and the last words are on their way.
    private(set) var ending: Ending?
    /// They are taking a while: the pressed button shows a spinner (not for a moment's wait, which only flashed it).
    private(set) var slow = false
    /// Which way the wave goes as the bar closes: into ✕ (cancel) or into ↑ / ■ (kept).
    private(set) var closing: Ending = .cancel
    /// Which start, stop or send a late answer belongs to. Lazy: the model is a `@State` value, built again with each
    /// rebuild of the composer's initializer, and keeps no work there (docs/apple/performance.md P3).
    @ObservationIgnored private lazy var run = UUID()
    /// One generator, prepared before it is needed: `Haptics.tap()` makes a new one each time, and the first impact of a
    /// cold one started the haptic engine on the main thread right as the bar moved (100–600 ms frames on a simulator).
    @ObservationIgnored private var haptic: UIImpactFeedbackGenerator?
    /// The words already joined into the draft, so a disappearing composer cannot append them twice.
    @ObservationIgnored private var wordsCommitted = false

    enum Ending: Equatable { case cancel, stop, send }

    /// The composer is on screen: the press's haptic gets ready while nothing moves.
    func warm() {
        if haptic == nil { haptic = UIImpactFeedbackGenerator(style: .light) }
        haptic?.prepare()
    }

    private func feel() {
        if haptic == nil { warm() }
        haptic?.impactOccurred()
        haptic?.prepare()
    }

    /// The mic's tap gives no haptic, as before the bar: the bar's own buttons do, and their generator gets ready once
    /// the bar is in (not on the tap's frame, which already carries the bar's spring).
    func start(_ dictation: any DictationStarting, hints: [String], reduceMotion: Bool, onFailure: (() -> Void)? = nil) {
        // A live system call: a note, and the bar never opens (dictation would cut the call's microphone).
        if let refusal = dictation.callRefusal {
            AppModel.shared.toast = Toast(refusal)
            onFailure?() // as any start that could not begin: the composer takes the keyboard back (#9619)
            return
        }
        run = UUID(); let mine = run
        Task {
            try? await Task.sleep(for: Theme.DictationBar.warmAfter)
            if run == mine { warm() }
        }
        ending = nil
        slow = false
        wordsCommitted = false
        withAnimation(reduceMotion ? Theme.Motion.state : Theme.Motion.dictationIn) { on = true }
        // A cancel while the system still asks for the microphone passes the start's turn (`Dictation.stop`): it never
        // listens.
        dictation.start(hints: hints, update: { _ in }, failed: { [weak self] problem in
            AppModel.shared.toast = Toast(problem, style: .error)
            guard let self, self.run == mine else { return }
            self.close(.cancel, reduceMotion: reduceMotion)
            onFailure?()
        })
    }

    func cancel(_ dictation: any DictationStarting, reduceMotion: Bool) {
        guard on, ending == nil else { return }
        feel()
        run = UUID()
        dictation.stop(settling: true)
        close(.cancel, reduceMotion: reduceMotion)
    }

    /// ■ or ↑: the words heard go after the draft. ↑ then sends (`send` says whether it did) and keeps the bar up until
    /// the send has taken the draft (it clears it a moment later, in a task of its own): closed first, the composer
    /// opened into the card for that moment with the words in it, and closed again.
    func end(_ how: Ending, _ dictation: any DictationStarting, text: Binding<String>, reduceMotion: Bool, send: @escaping () -> Bool) {
        guard on, ending == nil, how != .cancel else { return }
        feel()
        run = UUID(); let mine = run
        ending = how
        Task {
            try? await Task.sleep(for: Theme.DictationBar.spinnerAfter)
            if run == mine, ending == how { withAnimation(Theme.Motion.state) { slow = true } }
        }
        Task {
            let words = await dictation.finish(settling: true)
            guard run == mine else { return }
            text.wrappedValue = DictationHold.joined(text.wrappedValue, words)
            wordsCommitted = true
            if how == .send, send() {
                let deadline = ContinuousClock.now + Theme.DictationBar.sendWait
                while !text.wrappedValue.isEmpty, ContinuousClock.now < deadline { try? await Task.sleep(for: .milliseconds(8)) }
            }
            guard run == mine else { return }
            close(how, reduceMotion: reduceMotion)
        }
    }

    /// Dictation ended by itself (the recognizer's time limit, an error, the microphone taken): keep what was heard,
    /// as ■ does.
    func listeningEnded(_ dictation: any DictationStarting, text: Binding<String>, reduceMotion: Bool) {
        guard on, ending == nil else { return }
        run = UUID()
        text.wrappedValue = DictationHold.joined(text.wrappedValue, dictation.heard)
        wordsCommitted = true
        close(.stop, reduceMotion: reduceMotion)
    }

    /// Gone from the screen, including a root-layout replacement: keep the words already heard as an unsent draft.
    /// Stop/send's asynchronous finish may already have joined them; `wordsCommitted` prevents a second append.
    func reset(_ dictation: any DictationStarting, text: Binding<String>) {
        run = UUID()
        if on, !wordsCommitted {
            text.wrappedValue = DictationHold.joined(text.wrappedValue, dictation.heard)
            wordsCommitted = true
        }
        on = false
        ending = nil
        slow = false
    }

    private func close(_ how: Ending, reduceMotion: Bool) {
        closing = how
        withAnimation(reduceMotion ? Theme.Motion.state : Theme.Motion.dictationOut) {
            on = false
            ending = nil
            slow = false
        }
    }
}

/// The bar's row, laid over the composer's one-line shape (`ShellComposerBar` hides its own row under it): ✕ where
/// 「+」 is and ■ where the mic was tapped, so the buttons seem to turn into each other and a second tap in the same
/// place stops (#9619). ↑ takes the round button after the mic (voice, stop, send), or goes before ■ when the mic ended
/// its row (`stopLast`).
struct DictationBar: View {
    let model: DictationBarModel
    let meter: DictationMeter
    /// The mic ends its row: ■ goes last, ↑ before it. It follows the composer while the bar is off
    /// (`ShellComposerBar.followMicPlace`), so the tap moves nothing; a change moves the two (one view each, reordered,
    /// never built again) without animation.
    var stopLast = false
    var cancel: () -> Void
    var stop: () -> Void
    var send: () -> Void
    @Environment(\.accessibilityReduceMotion) private var reduceMotion

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("DictationBar")
        #endif
        let ending = model.ending
        let on = model.on
        // Always built, hidden while off: what a tap shows already exists (the wave's canvas and its render layer,
        // the buttons), so turning the bar on only animates scale and opacity (inserting them on the tap cost a ~40 ms
        // frame on a simulator). Off, the parts also drop their accessibility labels and identifiers: XCUITest still
        // lists built views hidden from accessibility, and the off ↑ answered UI tests' lookups of the composer's send
        // (#7684).
        let named = { (name: String) in on ? name : "" }
        HStack(spacing: Theme.Space.xs) {
            ComposerRoundButton(symbol: Theme.Icon.close, label: named(String(localized: "Cancel")), id: named("dictation-bar-cancel"),
                                pressScales: !reduceMotion, muted: true, action: cancel)
                .disabled(ending != nil)
                .modifier(BarPart(on: on, reduceMotion: reduceMotion))
            DictationWaveView(meter: meter, listening: on && ending == nil)
                .padding(.horizontal, Theme.Space.s)
                // Opens out from the right (where the mic was) and folds into the button that closed the bar.
                .scaleEffect(x: on || reduceMotion ? 1 : 0.01, y: 1,
                             anchor: on || model.closing != .cancel ? .trailing : .leading)
                .opacity(on ? 1 : Theme.DictationBar.hiddenInk)
                // VoiceOver reads the wave as where dictation stands, between ✕ and the two that end it.
                .accessibilityElement()
                .accessibilityLabel(named(ending == nil ? String(localized: "Listening…") : String(localized: "Transcribing…")))
                .accessibilityAddTraits(.updatesFrequently)
                .accessibilityHidden(!on)
            ForEach(stopLast ? Self.sendThenStop : Self.stopThenSend, id: \.self) { part in
                endButton(part, ending: ending, named: named)
                    .disabled(ending != nil)
                    .modifier(BarPart(on: on, reduceMotion: reduceMotion))
                    .animation(nil, value: stopLast)
            }
        }
        .allowsHitTesting(on)
    }

    private static let stopThenSend = ["stop", "send"]
    private static let sendThenStop = ["send", "stop"]

    /// ■ or ↑.
    private func endButton(_ part: String, ending: DictationBarModel.Ending?, named: (String) -> String) -> ComposerRoundButton {
        if part == "stop" {
            return ComposerRoundButton(symbol: Theme.Icon.stopDictating, label: named(String(localized: "Stop Dictation")), id: named("dictation-bar-stop"),
                                       pressScales: !reduceMotion, muted: true, busy: model.slow && ending == .stop, action: stop)
        }
        return ComposerRoundButton(symbol: Theme.Icon.send, label: named(String(localized: "Send")), id: named("dictation-bar-send"),
                                   pressScales: !reduceMotion, busy: model.slow && ending == .send, action: send)
    }
}

/// A bar button coming and going: it grows in from `dictationButtonFrom` a beat after the bar, and leaves quicker
/// than it came; Reduce Motion fades it only.
private struct BarPart: ViewModifier {
    var on: Bool
    var reduceMotion: Bool

    func body(content: Content) -> some View {
        content
            .scaleEffect(on || reduceMotion ? 1 : Theme.Motion.dictationButtonFrom)
            .opacity(on ? 1 : Theme.DictationBar.hiddenInk)
            .animation(reduceMotion ? Theme.Motion.state : on ? Theme.Motion.dictationButtonsIn : Theme.Motion.dictationButtonsOut,
                       value: on)
            .accessibilityHidden(!on)
    }
}

/// The wave: a canvas drawn once per sample (`TimelineView(.periodic)`, every `sampleInterval`), slid one pitch to the
/// left between samples by Core Animation, under a mask that fades the oldest columns at the left and lets the newest
/// come in at the right. Between samples the app does nothing: the render server moves the drawn strip. (Drawing the canvas
/// every frame cost more app CPU than the composer before the bar, and a SwiftUI offset animation is stepped on the main
/// thread every frame; measured 2026-11-13.) The meter is read on the ticks, so a louder voice never invalidates a view
/// (`DictationMeter`). Paused (transcribing) it holds still, dimmed. Reduce Motion: no slide, the row breathing with the
/// current level in place.
private struct DictationWaveView: View {
    let meter: DictationMeter
    var listening: Bool
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @Environment(\.buiTheme) private var theme
    @State private var wave = WaveBox()
    @State private var size: CGSize = .zero
    @State private var start = Date.now

    var body: some View {
        #if DEBUG || PERF_SIGNPOST
        let _ = PerfTrace.body("DictationWave")
        #endif
        let ink = AppStyle.current.usesBeautifulUI ? theme.colors.ink : Theme.Shell.ink
        // Measured on a view the strip cannot size (the strip is a pitch wider than the wave).
        Color.clear
            .frame(maxWidth: .infinity, maxHeight: .infinity)
            .onGeometryChange(for: CGSize.self) { $0.size } action: { size = $0 }
            .overlay(alignment: .leading) {
                // One timeline, ticking once an hour while not listening: switching between a timeline and a bare strip
                // built the strip's hosting controller again on each tap, a long frame as the bar opened and closed.
                TimelineView(.periodic(from: start, by: listening ? Theme.DictationBar.sampleInterval : Theme.DictationBar.idleTick)) { context in
                    strip(at: listening ? context.date : nil, ink: ink)
                }
                // The strip's ticks never animate in SwiftUI (Core Animation slides it): a tick that came in a transaction
                // with the bar's spring had SwiftUI animate the strip again after every sample.
                .transaction { $0.animation = nil }
            }
            .mask { edges }
            .opacity(listening ? 1 : Theme.DictationBar.quietInk)
            .scaledFrame(height: Theme.Size.wellL)
            .frame(maxWidth: .infinity)
            .onChange(of: listening) { _, now in
                guard now else { return }
                wave.reset() // a new take starts from silence
                var quiet = Transaction(animation: nil)
                quiet.disablesAnimations = true
                withTransaction(quiet) { start = .now }
            }
            .accessibilityIdentifier("dictation-bar-wave")
    }

    /// The strip as of `date`'s sample (nil: as it is), one pitch wider than the wave so the newest waits past its edge.
    private func strip(at date: Date?, ink: Color) -> some View {
        let pitch = Theme.DictationBar.pitch
        if let date {
            wave.advance(width: size.width + pitch, time: date.timeIntervalSinceReferenceDate) { meter.level(at: date) }
        }
        return WaveStrip(columns: wave.state.columns(width: size.width + pitch, height: size.height, still: reduceMotion),
                         taken: wave.state.taken, ink: ink, slides: !reduceMotion,
                         size: CGSize(width: size.width + pitch, height: size.height))
    }

    /// Opaque in the middle; the oldest columns fade to `oldest` toward the left, the newest comes in through the last
    /// pitch at the right.
    private var edges: some View {
        let w = max(size.width, 1)
        let pitch = Theme.DictationBar.pitch
        let fade = min(0.45, CGFloat(Theme.DictationBar.fadeColumns) * pitch / w)
        let enter = min(0.2, pitch / w)
        return LinearGradient(stops: [
            .init(color: .black.opacity(Theme.DictationBar.oldest), location: 0),
            .init(color: .black, location: fade),
            .init(color: .black, location: 1 - enter),
            .init(color: .black.opacity(Theme.DictationBar.newest), location: 1),
        ], startPoint: .leading, endPoint: .trailing)
    }
}

/// The drawn strip in a view of its own, whose layer Core Animation slides: SwiftUI animates on the main thread every
/// frame, a layer animation runs in the render server. Each new sample redraws the canvas shifted one pitch and starts
/// the slide again from where the strip is on screen, so a late or early tick never makes it jump.
private struct WaveStrip: UIViewRepresentable {
    var columns: [DictationWave.Column]
    var taken: Int
    var ink: Color
    var slides: Bool
    var size: CGSize

    func makeUIView(context: Context) -> WaveStripView { WaveStripView() }

    func updateUIView(_ view: WaveStripView, context: Context) {
        view.update(columns: columns, taken: taken, ink: ink, slides: slides, size: size)
    }

    func sizeThatFits(_ proposal: ProposedViewSize, uiView: WaveStripView, context: Context) -> CGSize? { size }
}

final class WaveStripView: UIView {
    private let host = UIHostingController(rootView: WaveCanvas(columns: [], ink: .clear))
    private var shown = 0

    init() {
        super.init(frame: .zero)
        host.safeAreaRegions = []
        host.sizingOptions = []
        host.view.backgroundColor = .clear
        host.view.isUserInteractionEnabled = false
        isUserInteractionEnabled = false
        accessibilityElementsHidden = true
        addSubview(host.view)
    }

    @available(*, unavailable) required init?(coder: NSCoder) { fatalError() }

    fileprivate func update(columns: [DictationWave.Column], taken: Int, ink: Color, slides: Bool, size: CGSize) {
        let pitch = Theme.DictationBar.pitch
        let layer = host.view.layer
        if taken < shown { shown = taken } // a new take
        let steps = taken - shown
        shown = taken
        // The new drawing and the slide's restart go to the screen in the same transaction.
        CATransaction.begin()
        CATransaction.setDisableActions(true)
        host.view.bounds = CGRect(origin: .zero, size: size)
        host.view.center = CGPoint(x: size.width / 2, y: size.height / 2)
        host.rootView = WaveCanvas(columns: columns, ink: ink)
        host.view.layoutIfNeeded()
        // At rest the strip sits a pitch left of its frame (the newest at the wave's right edge).
        let rest = -pitch
        if !slides {
            layer.removeAnimation(forKey: "slide")
            layer.setValue(rest, forKeyPath: "transform.translation.x")
        } else if steps > 0 {
            let now = (layer.presentation()?.value(forKeyPath: "transform.translation.x") as? CGFloat) ?? rest
            let from = min(rest + 2 * pitch, now + pitch * CGFloat(steps)) // after a long stall, no more than two pitches
            layer.setValue(rest, forKeyPath: "transform.translation.x")
            let slide = CABasicAnimation(keyPath: "transform.translation.x")
            slide.fromValue = from
            slide.toValue = rest
            slide.duration = Double((from - rest) / pitch) * Theme.DictationBar.sampleInterval
            slide.timingFunction = CAMediaTimingFunction(name: .linear)
            layer.add(slide, forKey: "slide")
        }
        CATransaction.commit()
    }
}

/// The strip's drawing: the columns as rounded bars, in a handful of fills.
private struct WaveCanvas: View {
    var columns: [DictationWave.Column]
    var ink: Color

    var body: some View {
        Canvas { gc, size in Self.draw(columns, in: &gc, size: size, ink: ink) }
    }

    nonisolated static func draw(_ columns: [DictationWave.Column], in gc: inout GraphicsContext, size: CGSize, ink: Color) {
        let mid = size.height / 2
        let dot = Theme.DictationBar.dot
        // One path per step of ink: a handful of fills a draw instead of one per column.
        let steps = Theme.DictationBar.inkSteps
        var paths = Array(repeating: Path(), count: steps + 1)
        for c in columns {
            let rect = CGRect(x: size.width - c.fromRight - dot / 2, y: mid - c.height / 2, width: dot, height: c.height)
            paths[min(steps, Int((c.opacity * Double(steps)).rounded()))]
                .addRoundedRect(in: rect, cornerSize: CGSize(width: dot / 2, height: dot / 2))
        }
        let shading = gc.resolve(GraphicsContext.Shading.color(ink))
        for (i, path) in paths.enumerated() where i > 0 && !path.isEmpty {
            var layer = gc
            layer.opacity = Double(i) / Double(steps)
            layer.fill(path, with: shading)
        }
    }
}

/// The wave's samples across ticks, outside SwiftUI's state: taking one must not invalidate anything.
private final class WaveBox {
    /// Lazy for the same reason as `DictationBarModel.run`: the box is a `@State` value (P3).
    lazy var state = DictationWave(capacity: 1)
    /// When the last sample was due.
    private var last: TimeInterval?

    func reset() {
        state = DictationWave(capacity: state.capacity)
        last = nil
    }

    /// Takes the samples due by `time`, one every `sampleInterval` (after a long gap, at most a screenful).
    func advance(width: CGFloat, time: TimeInterval, level: () -> Float) {
        let fit = Int(width / Theme.DictationBar.pitch) + 2
        if state.capacity != fit { state = DictationWave(capacity: fit, taken: state.taken) }
        let interval = Theme.DictationBar.sampleInterval
        guard var due = last else {
            last = time
            return
        }
        if time - due > interval * Double(fit) { due = time - interval * Double(fit) }
        let now = level()
        while time - due >= interval {
            due += interval
            state.advance(to: due, level: now)
        }
        last = due
    }

}
#endif
