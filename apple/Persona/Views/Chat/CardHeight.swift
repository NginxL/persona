import SwiftUI

/// How tall the user made the question cards over the composer with their grabber (#12306). On a short page (the open
/// iPhone Duo held sideways, a small iPhone) a card's default cap left one option and the start of its description in
/// view, and nothing let the user trade conversation for card. The grabber on the card's top edge sets the height by
/// dragging; a tap on it switches between the tallest and the default. The choice is kept on the device, as a share of
/// the room between the shortest card (`Theme.Size.cardLeast`) and the tallest (`FooterRoom.tallestRoom`), so it reads
/// the same upright and sideways.
@Observable final class CardHeight {
    static let shared = CardHeight()
    private static let key = "ui.questionCardHeight"

    /// The share of the room the user chose, 0…1; nil: the default cap.
    private(set) var share: Double?
    /// The card being dragged and the height its container takes under the finger. Only that card follows it.
    private(set) var live: (id: String, height: CGFloat)?

    /// A UI test starts from the default with `-ui.questionCardHeight ""` (not a number: no share).
    private init() {
        share = (UserDefaults.standard.object(forKey: Self.key) as? Double).map { min(max($0, 0), 1) }
    }

    /// The container's cap: under the finger while its card is dragged, the user's share of `range`, or the default.
    func cap(default fallback: CGFloat, range: ClosedRange<CGFloat>, id: String) -> CGFloat {
        if let live, live.id == id { return Self.clamp(live.height, range) }
        guard let share else { return fallback }
        return range.lowerBound + CGFloat(share) * (range.upperBound - range.lowerBound)
    }

    func drag(_ id: String, to height: CGFloat) { live = (id, height) }

    /// The finger lifts: the height it left becomes the user's share, written once (never while dragging).
    func end(_ id: String, range: ClosedRange<CGFloat>) {
        guard let live, live.id == id else { return }
        let span = range.upperBound - range.lowerBound
        let chosen = span > 0 ? Double((Self.clamp(live.height, range) - range.lowerBound) / span) : 1
        withAnimation(Theme.Motion.standard) {
            self.live = nil
            set(chosen)
        }
    }

    /// A tap on the grabber: the tallest, or back to the default from there.
    func toggle() {
        withAnimation(Theme.Motion.standard) { set(share == 1 ? nil : 1) }
    }

    /// VoiceOver's swipe up and down on the grabber: a quarter of the room at a time.
    func step(by delta: Double, from height: CGFloat, range: ClosedRange<CGFloat>) {
        let span = range.upperBound - range.lowerBound
        let now = share ?? (span > 0 ? Double((Self.clamp(height, range) - range.lowerBound) / span) : 1)
        withAnimation(Theme.Motion.standard) { set(min(max(now + delta, 0), 1)) }
    }

    /// Kept off the main actor (docs/apple/performance.md P2): once per drag or tap, never while dragging.
    private func set(_ value: Double?) {
        share = value
        let key = Self.key
        Task.detached(priority: .utility) {
            if let value { UserDefaults.standard.set(value, forKey: key) } else { UserDefaults.standard.removeObject(forKey: key) }
        }
    }

    private static func clamp(_ h: CGFloat, _ r: ClosedRange<CGFloat>) -> CGFloat { min(max(h, r.lowerBound), r.upperBound) }
}

/// What a container lets its card's grabber do (`InteractionPanel`): nil, no grabber.
struct CardResize {
    /// The card it sizes (`CardHeight.live`).
    var id: String
    /// The container's height now, read when a drag starts (not in its body: the card would redraw at every height).
    var height: @MainActor () -> CGFloat
    /// The heights the grabber can give the container.
    var range: ClosedRange<CGFloat>
}

extension EnvironmentValues {
    /// The card's container lets the user size it (#12306): a lone question over the composer with the keyboard down.
    @Entry var cardResize: CardResize? = nil
}

/// The grabber on a question card's top edge (#12306): dragged, the card follows the finger between its shortest and its
/// tallest; tapped, it goes to the tallest or back to the default. A strip the card's width, so a finger finds it; the
/// mark is the sheets' grabber.
struct CardGrabber: View {
    var resize: CardResize
    private var store: CardHeight { .shared }
    @State private var start: CGFloat?

    var body: some View {
        Capsule()
            .fill(.tertiary)
            .scaledFrame(width: Theme.Size.wellXL, height: Theme.Size.grabber)
            .frame(maxWidth: .infinity)
            .frame(height: Theme.Space.l)
            // A finger's room over the card's edge without a taller card; under it is the title row, which folds.
            .contentShape(.rect.inset(by: -Theme.Space.s))
            .gesture(
                DragGesture(minimumDistance: Theme.Space.xs, coordinateSpace: .global)
                    .onChanged { g in
                        let from = start ?? resize.height()
                        if start == nil { start = from; Haptics.selection() }
                        // The card's bottom stays on the composer: up makes it taller.
                        store.drag(resize.id, to: from - g.translation.height)
                    }
                    .onEnded { _ in
                        start = nil
                        store.end(resize.id, range: resize.range)
                    }
            )
            .onTapGesture {
                Haptics.tap()
                store.toggle()
            }
            .accessibilityElement()
            .accessibilityLabel(Text("Card Height [Question Card]"))
            .accessibilityHint(Text("Swipe up or down to make the card taller or shorter. Double-tap for the tallest or the default."))
            .accessibilityAddTraits(.isButton)
            .accessibilityAction { store.toggle() }
            .accessibilityAdjustableAction { direction in
                switch direction {
                case .increment: store.step(by: 0.25, from: resize.height(), range: resize.range)
                case .decrement: store.step(by: -0.25, from: resize.height(), range: resize.range)
                @unknown default: break
                }
            }
            .accessibilityIdentifier("interaction-grabber")
    }
}
