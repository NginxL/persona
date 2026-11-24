#if os(iOS) || os(watchOS)
import SwiftUI
import WidgetKit

extension WatchIntakeKind {
    var title: LocalizedStringKey { self == .water ? "Water [Home Widgets]" : "Protein [Home Widgets]" }
    var symbol: String { self == .water ? WatchFaceTokens.waterSymbol : WatchFaceTokens.proteinSymbol }
    var color: Color { self == .water ? HealthIntakePalette.water : HealthIntakePalette.protein }
}

/// The actual three family views, also compiled by the app for isolated image exports in tests.
struct WatchIntakeComplicationView: View {
    var entry: WatchIntakeEntry
    var kind: WatchIntakeKind
    var family: WidgetFamily

    private var portion: WatchIntakeReading.Portion? { entry.reading?.portion(kind, at: entry.date) }
    private var amount: String { (portion?.today ?? 0).formatted(.number.precision(.fractionLength(0...1))) }
    private var goal: String { (portion?.goal ?? 0).formatted(.number.precision(.fractionLength(0...1))) }
    private var unit: String {
        kind == .water ? interfaceText("ml") : interfaceText("g") // perf-ok: bounded complication entry rendering
    }
    private var message: LocalizedStringKey? {
        if entry.needsHubUpdate { return "Update the hub [Home Widgets]" }
        if !entry.paired { return "Open Persona on iPhone to connect [Watch Widgets]" }
        if portion == nil { return "Can't load intake [Watch Widgets]" }
        return nil
    }

    var body: some View {
        Group {
            if let message {
                if family == .accessoryCircular {
                    VStack(spacing: WatchFaceTokens.intakeSpacing) {
                        WatchFaceTokens.intakeSymbol(kind.symbol)
                        Text(message).font(WatchFaceTokens.intakeCaption).multilineTextAlignment(.center)
                            .lineLimit(2).minimumScaleFactor(WatchFaceTokens.intakeMinimumScale)
                    }
                } else {
                    Label { Text(message).lineLimit(family == .accessoryInline ? 1 : 2) }
                        icon: { WatchFaceTokens.intakeSymbol(kind.symbol) }
                        .font(WatchFaceTokens.intakeCaption)
                }
            } else {
                switch family {
                case .accessoryCircular:
                    Gauge(value: portion?.fraction ?? 0) {
                        WatchFaceTokens.intakeSymbol(kind.symbol)
                    } currentValueLabel: {
                        Text(verbatim: amount).lineLimit(1).minimumScaleFactor(WatchFaceTokens.intakeMinimumScale)
                    }
                    .gaugeStyle(.accessoryCircularCapacity)
                    .tint(kind.color)
                case .accessoryInline:
                    Label { Text(kind.title) + Text(verbatim: " \(amount) \(unit)") }
                        icon: { WatchFaceTokens.intakeSymbol(kind.symbol) }
                default:
                    VStack(alignment: .leading, spacing: WatchFaceTokens.intakeSpacing) {
                        Label { Text(kind.title) + Text(verbatim: " \(amount) \(unit)") }
                            icon: { WatchFaceTokens.intakeSymbol(kind.symbol) }
                            .font(WatchFaceTokens.intakeTitle).lineLimit(1)
                            .minimumScaleFactor(WatchFaceTokens.titleMinScale)
                        Text("\(amount) of \(goal) \(unit) [Watch Widgets]")
                            .font(WatchFaceTokens.intakeCaption).lineLimit(1)
                        Gauge(value: portion?.fraction ?? 0) { EmptyView() }
                            .gaugeStyle(.accessoryLinearCapacity).tint(kind.color)
                        if entry.cached {
                            Text("Cached [Watch Widgets]").font(WatchFaceTokens.intakeCaption).lineLimit(1)
                                .foregroundStyle(.secondary)
                        }
                    }
                }
            }
        }
        .widgetAccentable()
        .dynamicTypeSize(...WatchFaceTokens.intakeLargestTypeSize)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(kind.title))
        .accessibilityValue(spoken)
        .environment(\.locale, LanguageStore.drawingLocale)
    }

    private var spoken: Text {
        if let message { return Text(message) }
        let total = Text("\(amount) of \(goal) \(unit) [Watch Widgets]")
        return entry.cached ? total + Text(verbatim: ", ") + Text("Cached [Watch Widgets]") : total
    }
}
#endif
