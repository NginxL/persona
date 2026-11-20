import Charts
import SwiftUI

/// A chart of values over categories (a plugin's `chart` item, docs/extensions/extensions.md「UI slots」): bars (side by side or
/// stacked), lines or areas (UI version 6: overlapping, each with its line, or stacked), one to a few series, in the design system's colors. A series without a color of its own takes
/// the data hues in order (the first is the accent). Holding (iPhone) or hovering (Mac) a category shows its label and
/// every series' value above the plot. Compact (a card): no axis text, only the marks and the first and last labels.
/// Nothing here knows about plugins: the caller maps its data onto `DataChart.Series`.
struct DataChart: View {
    enum Kind: Sendable { case bar, line, area }

    struct Series: Identifiable, Sendable {
        var name: String
        /// A role color (`Theme.positive`…); nil: the next data hue.
        var color: Color?
        var values: [Double]
        /// How each value is written ("¥1.20"); nil at an index: `format` of the value.
        var displays: [String?] = []
        var id: String { name }

        func text(_ i: Int, _ format: (Double) -> String) -> String {
            if i < displays.count, let d = displays[i], !d.isEmpty { return d }
            return i < values.count ? format(values[i]) : ""
        }
    }

    var kind: Kind
    /// The categories along the x axis; every series' values line up with them.
    var labels: [String]
    var series: [Series]
    /// Bars of several series on top of each other (lines ignore it).
    var stacked = false
    /// How a value reads where the plugin gave no `display` (with its unit).
    var format: (Double) -> String = { DataChart.compact($0) }
    /// A card: a shorter plot, no axis text, no legend.
    var compact = false

    @State private var picked: String?

    private struct Point: Identifiable {
        var id: String { series + "@" + String(index) }
        var series: String
        var index: Int
        var label: String
        var value: Double
    }

    /// The data hues, in order: different enough from each other in light and dark.
    static let hues: [Color] = [Theme.accent, Theme.Hue.orange, Theme.Hue.green, Theme.Hue.purple]

    private func color(_ i: Int) -> Color { series[i].color ?? Self.hues[i % Self.hues.count] }

    /// Whether the chart is one category's shares — a stacked bar whose every series has exactly one point, all
    /// non-negative and adding above zero. Such a chart draws as a horizontal segmented bar (`SegmentedShareBar`),
    /// not a lone vertical column; two or more categories keep the columns. The same rule on every app
    /// (docs/extensions/extensions.md「New items」chart; issue #5386).
    static func isSingleCategoryShare(kind: Kind, stacked: Bool, series: [Series]) -> Bool {
        singleCategoryShares(kind: kind, stacked: stacked, series: series) != nil
    }

    /// Each series' share of one category. If adding finite values overflows, scale them before summing so large
    /// positive parts do not all become zero-width segments; ordinary values keep the original arithmetic.
    static func singleCategoryShares(kind: Kind, stacked: Bool, series: [Series]) -> [Double]? {
        guard kind == .bar, stacked, !series.isEmpty, series.allSatisfy({ $0.values.count == 1 }) else { return nil }
        let values = series.map(\.values[0])
        guard values.allSatisfy({ $0.isFinite && $0 >= 0 }) else { return nil }
        let total = values.reduce(0, +)
        guard total > 0 else { return nil }
        if total.isFinite { return values.map { $0 / total } }
        let largest = values.max() ?? 1
        let scaled = values.map { $0 / largest }
        let scaledTotal = scaled.reduce(0, +)
        return scaled.map { $0 / scaledTotal }
    }

    private var points: [Point] {
        series.flatMap { s in
            s.values.prefix(labels.count).enumerated().compactMap { i, v in
                v.isFinite ? Point(series: s.name, index: i, label: labels[i], value: v) : nil
            }
        }
    }

    /// The x labels written under the plot: all when they fit, else every few (always the first and the last), so they
    /// never overlap. A compact chart writes only the first and the last.
    private var shownLabels: [String] {
        guard labels.count > 2 else { return labels }
        if compact { return [labels[0], labels[labels.count - 1]] }
        let most = 6
        guard labels.count > most else { return labels }
        let step = Int((Double(labels.count - 1) / Double(most - 1)).rounded(.up))
        var out = stride(from: 0, to: labels.count - 1, by: step).map { labels[$0] }
        if labels.count - 1 - (out.count - 1) * step < step / 2 + 1, out.count > 1 { out.removeLast() }
        out.append(labels[labels.count - 1])
        return out
    }

    var body: some View {
        let pts = points
        let names = series.map(\.name)
        let shown = Set(shownLabels)
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            if !compact { readout }
            Chart(pts) { p in
                switch kind {
                case .bar:
                    if stacked || series.count == 1 {
                        BarMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .opacity(picked == nil || picked == p.label ? 1 : 0.4)
                    } else {
                        BarMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .position(by: .value(String(localized: "Series"), p.series))
                            .opacity(picked == nil || picked == p.label ? 1 : 0.4)
                    }
                case .line:
                    LineMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value), series: .value(String(localized: "Series"), p.series))
                        .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                        .interpolationMethod(.monotone)
                        .lineStyle(StrokeStyle(lineWidth: Theme.Size.rule))
                    if picked == p.label {
                        PointMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                    }
                case .area:
                    // Stacked: each on the one under it; else overlapping, see-through, each with its line on top.
                    AreaMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value),
                             stacking: stacked ? .standard : .unstacked)
                        .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                        .opacity(stacked ? 0.85 : 0.25)
                        .interpolationMethod(.monotone)
                    if !stacked {
                        LineMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value), series: .value(String(localized: "Series"), p.series))
                            .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                            .interpolationMethod(.monotone)
                            .lineStyle(StrokeStyle(lineWidth: Theme.Size.rule))
                        if picked == p.label {
                            PointMark(x: .value(String(localized: "Category"), p.label), y: .value(String(localized: "Value [Data Chart cda1d5]"), p.value))
                                .foregroundStyle(by: .value(String(localized: "Series"), p.series))
                        }
                    }
                }
            }
            .chartForegroundStyleScale(domain: names, range: names.indices.map(color))
            .chartLegend(.hidden)
            .chartXAxis {
                // Every category gets a mark (a categorical axis takes no subset); only the shown ones say anything.
                AxisMarks { v in
                    AxisValueLabel {
                        let l = v.as(String.self) ?? ""
                        Text(shown.contains(l) ? l : "")
                    }
                    .font(Theme.Font.label).foregroundStyle(Color.secondary)
                }
            }
            .chartYAxis {
                AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) { v in
                    AxisGridLine(stroke: StrokeStyle(lineWidth: Theme.Size.hairline)).foregroundStyle(Theme.Fill.chip)
                    AxisValueLabel {
                        if let d = v.as(Double.self) { Text(Self.compact(d)) }
                    }
                    .font(Theme.Font.label).foregroundStyle(Color.secondary)
                }
            }
            .chartYAxis(compact ? .hidden : .automatic)
            .chartXSelection(value: $picked)
            .scaledFrame(height: compact ? Theme.Size.box80 : Theme.Size.box150)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel(accessibilitySummary)

            if series.count > 1 && !compact { legend }
        }
    }

    /// What the held category reads: its label and every series' value; nothing held, a line of the same height (the
    /// plot does not jump when a finger goes down).
    private var readout: some View {
        let i = picked.flatMap { labels.firstIndex(of: $0) }
        return HStack(spacing: Theme.Space.m) {
            if let i {
                Text(labels[i]).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                ForEach(Array(series.enumerated()), id: \.offset) { n, s in
                    HStack(spacing: Theme.Space.xs) {
                        if series.count > 1 { Circle().fill(color(n)).scaledFrame(width: Theme.Size.dotXS, height: Theme.Size.dotXS) }
                        Text(s.text(i, format)).font(Theme.Font.label.monospacedDigit())
                    }
                }
            } else {
                Text(" ").font(Theme.Font.labelStrong)
            }
        }
        .lineLimit(1)
        .minimumScaleFactor(0.7)
        .accessibilityHidden(true)
    }

    private var legend: some View {
        HStack(spacing: Theme.Space.m) {
            ForEach(Array(series.enumerated()), id: \.offset) { i, s in
                HStack(spacing: Theme.Space.xs) {
                    Circle().fill(color(i)).scaledFrame(width: Theme.Size.dot, height: Theme.Size.dot)
                    Text(s.name).font(Theme.Font.label).foregroundStyle(.secondary).lineLimit(1)
                }
            }
        }
    }

    /// VoiceOver reads each series' values by category instead of the marks one by one.
    private var accessibilitySummary: String {
        series.map { s in
            let pairs = labels.indices.prefix(s.values.count).map { "\(labels[$0]) \(s.text($0, format))" }.joined(separator: String(localized: ", ", comment: "List separator"))
            return series.count > 1 && !s.name.isEmpty ? String(localized: "\(s.name): \(pairs)", comment: "Chart series name and its values") : pairs
        }.joined(separator: String(localized: "; ", comment: "Separator between chart series"))
    }

    /// A short number: whole ones as they are, a few decimals at most, large ones as 1.2k / 3.4M / 5.6B.
    static func compact(_ v: Double) -> String {
        let a = abs(v)
        func trim(_ x: Double, _ digits: Int) -> String {
            String(format: "%.\(digits)f", x).replacingOccurrences(of: #"\.0+$|(\.\d*?)0+$"#, with: "$1", options: .regularExpression)
        }
        if a >= 1e9 { return trim(v / 1e9, 1) + "B" }
        if a >= 1e6 { return trim(v / 1e6, 1) + "M" }
        if a >= 1e4 { return trim(v / 1e3, 0) + "k" }
        if a >= 1e3 { return trim(v / 1e3, 1) + "k" }
        if v == v.rounded() { return String(Int(v)) }
        return trim(v, a < 1 ? 2 : 1)
    }
}

/// A horizontal capsule split into one category's shares (a single day's stacked parts): each share takes its part of
/// the width in its color, a rule-width gap between touching segments. It carries no text — the card's rows (or the
/// legend above it) name each part and its value. Vertical columns stay for charts with several categories to compare.
struct SegmentedShareBar: View {
    /// One segment: its fill and its share of the total (0…1, the shares add up to 1).
    struct Share: Identifiable, Sendable {
        let id: Int
        let color: Color
        let ratio: Double
    }

    var shares: [Share]

    /// Zero shares draw no segment (the same rule as the web and Android bars); ids keep the series index, so the
    /// legend above still lines up with the colors.
    private var visible: [Share] { shares.filter { $0.ratio > 0 } }

    var body: some View {
        GeometryReader { g in
            let gap = Theme.Size.rule
            let gaps = gap * CGFloat(max(visible.count - 1, 0))
            HStack(spacing: gap) {
                ForEach(visible) { share in
                    Rectangle()
                        .fill(share.color)
                        .frame(width: max(g.size.width - gaps, 0) * share.ratio)
                }
            }
        }
        .scaledFrame(height: Theme.Size.meter)
        .clipShape(.capsule)
        .accessibilityHidden(true) // the rows below say what each share is
    }
}
