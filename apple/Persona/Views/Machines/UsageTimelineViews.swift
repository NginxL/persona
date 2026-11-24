import SwiftUI
import PersonaWire

/// The usage card's reset timeline (#13173): one chart for all of the card's accounts on a shared axis (now, 6h, 12h, 1d,
/// 3d, 5d, 7d; the first day takes 45% of the width), a row per account in the quota list's order, each with a teal 5h bar
/// over a blue 7d bar running from the now line to its reset, a hollow dot at the end and the time left. The geometry is
/// `UsageWindows.timelineRows` (unit-tested); the look is these views only.
struct UsageResetTimelineChart: View {
    var rows: [UsageTimelineRow]
    var now: Date

    var body: some View {
        VStack(alignment: .leading, spacing: Theme.Space.xs) {
            Text("Reset timeline", comment: "Usage card: the chart of when each account's windows reset")
                .font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
            HStack(alignment: .top, spacing: Theme.Space.s) {
                Color.clear.frame(width: Theme.Size.timelineName, height: 0)
                axis
            }
            VStack(spacing: 0) {
                ForEach(Array(rows.enumerated()), id: \.element.id) { i, row in
                    UsageTimelineRowView(row: row, now: now, banded: i % 2 == 0)
                }
            }
            .background { grid }
            legend
        }
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("usage.timeline")
    }

    private var axis: some View {
        GeometryReader { geo in
            // The 6h tick keeps its line but not its label: on a phone's width it would sit on top of "Now".
            ForEach(UsageWindows.timelineTickHours.filter { $0 != 6 }, id: \.self) { h in
                let x = geo.size.width * UsageWindows.timelinePosition(hours: h)
                Text(Self.tickLabel(h)).font(Theme.Font.label).lineLimit(1).fixedSize()
                    .foregroundStyle(h == 0 ? Color.primary : Color.secondary)
                    .position(x: h == 0 ? x + Theme.Space.l : min(max(x, Theme.Space.l), geo.size.width - Theme.Space.s), y: Theme.Space.s)
            }
        }
        .frame(height: Theme.Space.l)
        .accessibilityHidden(true)
    }

    /// Vertical lines at the ticks, the now line heavier and in the text colour.
    private var grid: some View {
        GeometryReader { geo in
            let left = Theme.Size.timelineName + Theme.Space.s
            let w = geo.size.width - left
            ForEach(UsageWindows.timelineTickHours, id: \.self) { h in
                Rectangle().fill(h == 0 ? Color.primary : Theme.Fill.chip)
                    .frame(width: h == 0 ? Theme.Size.rule : Theme.Size.hairline)
                    .offset(x: left + w * UsageWindows.timelinePosition(hours: h))
            }
        }
        .accessibilityHidden(true)
    }

    private var legend: some View {
        HStack(spacing: Theme.Space.m) {
            legendKey(Theme.Hue.teal, String(localized: "5h window resets", comment: "Usage timeline legend"))
            legendKey(Theme.Hue.blue, String(localized: "7d window resets", comment: "Usage timeline legend"))
        }
        .font(Theme.Font.label).foregroundStyle(Color.secondary)
        .accessibilityHidden(true)
    }

    private func legendKey(_ color: Color, _ text: String) -> some View {
        HStack(spacing: Theme.Space.xs) {
            Capsule().fill(color).frame(width: Theme.Space.l, height: Theme.Size.timelineCap)
            Text(text).lineLimit(1)
        }
    }

    static func tickLabel(_ hours: Double) -> String {
        switch hours {
        case 0: String(localized: "Now", comment: "Usage timeline axis: the left edge")
        case ..<24: String(localized: "usage.axis.hours", defaultValue: "\(Int(hours))h", comment: "Usage timeline axis tick: hours, kept short in every language")
        default: String(localized: "usage.axis.days", defaultValue: "\(Int(hours / 24))d", comment: "Usage timeline axis tick: days, kept short in every language")
        }
    }
}

private struct UsageTimelineRowView: View {
    var row: UsageTimelineRow
    var now: Date
    var banded: Bool

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            nameColumn
            plot
        }
        .frame(height: Theme.Size.timelineRow)
        .background { band }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(spoken)
    }

    private var nameColumn: some View {
        VStack(alignment: .leading, spacing: 0) {
            Text(row.name).font(Theme.Font.label).foregroundStyle(Color.secondary).lineLimit(1)
            Text(row.plan).font(Theme.Font.label).foregroundStyle(.tertiary).lineLimit(1)
        }
        .frame(width: Theme.Size.timelineName, alignment: .leading)
    }

    private var plot: some View {
        GeometryReader { geo in
            VStack(spacing: 0) {
                half(row.fiveHour, color: Theme.Hue.teal, height: Theme.Size.timelineCap, width: geo.size.width)
                half(row.week, color: Theme.Hue.blue, height: Theme.Size.timelineCapWide, width: geo.size.width)
            }
        }
    }

    /// Every other row has a faint band behind it.
    @ViewBuilder private var band: some View {
        if banded {
            RoundedRectangle(cornerRadius: Theme.Radius.badge).fill(Theme.Fill.chip.opacity(0.4))
        }
    }

    /// "Name, 5h resets in 4 hours, 7d resets in 2 days".
    private var spoken: String {
        var parts: [String] = [row.name]
        if let c = row.fiveHour { parts.append("5h " + Self.left(c)) }
        if let c = row.week { parts.append("7d " + Self.left(c)) }
        return parts.joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label"))
    }

    private static func left(_ c: UsageTimelineCap) -> String {
        UsageWindows.resetLabel(c.resetsAt, now: c.resetsAt.addingTimeInterval(-c.seconds))
    }

    /// Half a row: one window's bar, or nothing.
    @ViewBuilder private func half(_ cap: UsageTimelineCap?, color: Color, height: CGFloat, width: CGFloat) -> some View {
        Group {
            if let cap { bar(cap, color: color, height: height, width: width) } else { Color.clear }
        }
        .frame(height: Theme.Size.timelineRow / 2)
    }

    /// A bar from the now line to the cap, fading in from the left, a hollow dot at the end and the time left: beside the
    /// dot, or inside the bar in white when the end is far right.
    private func bar(_ cap: UsageTimelineCap, color: Color, height: CGFloat, width: CGFloat) -> some View {
        let dot = Theme.Size.timelineEnd
        let end = width * cap.position
        let inside = cap.position > UsageResetTimelineChart.insideLabelFrom
        let label = Text(UsageWindows.shortDuration(cap.seconds)).font(Theme.Font.labelStrong.monospacedDigit()).lineLimit(1).fixedSize()
        return ZStack(alignment: .leading) {
            Capsule().fill(LinearGradient(colors: [color.opacity(0), color], startPoint: .leading, endPoint: .trailing))
                .frame(width: max(dot, end), height: height)
            Circle().fill(Theme.Fill.chip).overlay(Circle().stroke(color, lineWidth: Theme.Size.rule))
                .frame(width: dot, height: dot)
                .offset(x: end - dot)
            if inside {
                label.foregroundStyle(Color.white).frame(width: max(0, end - dot), alignment: .trailing)
            } else {
                label.foregroundStyle(color).padding(.leading, max(dot, end) + Theme.Space.xs)
            }
        }
        .frame(width: width, alignment: .leading)
    }
}

extension UsageResetTimelineChart {
    /// Past this share of the width a bar's time left is written inside it, in white.
    static let insideLabelFrom = 0.8
}
