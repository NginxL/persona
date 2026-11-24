import SwiftUI

/// A single progress ring, shared by extension tiles and Health. The caller supplies its words and color;
/// this face is decorative and the caller owns its combined accessibility label and actions.
struct GaugeRing: View {
    var title: String
    var value: String
    var unit: String?
    var subtitle: String?
    var ratio: Double
    var color: Color
    var compact: Bool
    var subtitleLineLimit: Int?
    @Environment(\.accessibilityReduceMotion) private var reduceMotion
    @State private var shown: Double

    init(title: String, value: String, unit: String? = nil, subtitle: String? = nil,
         ratio: Double, color: Color, compact: Bool = false, subtitleLineLimit: Int? = 1) {
        self.title = title
        self.value = value
        self.unit = unit
        self.subtitle = subtitle
        self.ratio = ratio
        self.color = color
        self.compact = compact
        self.subtitleLineLimit = subtitleLineLimit
        _shown = State(initialValue: Self.progress(ratio))
    }

    nonisolated static func progress(_ ratio: Double) -> Double { min(max(ratio, 0), 1) }
    private var side: CGFloat { compact ? Theme.Size.gaugeRingCompact : Theme.Size.gaugeRing }

    var body: some View {
        VStack(alignment: .center, spacing: Theme.Space.xs) {
            ZStack {
                Circle()
                    .stroke(color.opacity(0.14), lineWidth: Theme.Size.gaugeStroke)
                if shown > 0 {
                    Circle()
                        .trim(from: 0, to: shown)
                        .stroke(color, style: StrokeStyle(lineWidth: Theme.Size.gaugeStroke, lineCap: .round))
                        .rotationEffect(.degrees(-90))
                }
                VStack(alignment: .center, spacing: Theme.Space.flush) {
                    ExtensionChangingText(value)
                        .font((compact ? Theme.Font.heading : Theme.Font.title).monospacedDigit())
                        .foregroundStyle(Color.primary)
                        .lineLimit(1)
                        .minimumScaleFactor(0.6)
                    if let unit, !unit.isEmpty {
                        Text(unit).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
                    }
                }
                .frame(maxWidth: side * 0.72)
            }
            .scaledFrame(width: side, height: side)
            Text(title).font(Theme.Font.labelStrong).foregroundStyle(Color.primary).lineLimit(1)
            if let subtitle, !subtitle.isEmpty {
                Text(subtitle).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(subtitleLineLimit)
            }
        }
        .padding(Theme.Space.m)
        .onChange(of: ratio) { _, ratio in
            let next = Self.progress(ratio)
            if reduceMotion {
                shown = next
            } else {
                withAnimation(Theme.Motion.state) { shown = next }
            }
        }
    }
}
