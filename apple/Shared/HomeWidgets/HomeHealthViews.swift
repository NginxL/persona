#if os(iOS)
import SwiftUI
import WidgetKit

/// One intake's words and numbers, resolved once as its timeline entry is drawn.
struct HomeIntakeValue {
    var kind: HomeKind
    var c: HomeContext
    var portion: HomeSnapshot.Portion? { kind == .water ? c.intake?.water : c.intake?.protein }
    var color: Color { kind == .water ? HomeWidgetTokens.water : HomeWidgetTokens.protein }
    var symbol: String { kind == .water ? HomeWidgetTokens.waterSymbol : HomeWidgetTokens.proteinSymbol }
    var amount: String { (portion?.today ?? 0).formatted(.number.precision(.fractionLength(0...1))) }
    var unit: String {
        kind == .water ? interfaceText("ml") : interfaceText("g") // perf-ok: drawn once per timeline entry
    }
    var remaining: String {
        let left = max(0, (portion?.goal ?? 0) - (portion?.today ?? 0)).formatted(.number.precision(.fractionLength(0...1)))
        return interfaceText("\(left) \(unit) left [Home Widgets]") // perf-ok: drawn once per timeline entry
    }
    var fraction: Double {
        guard let portion, portion.goal > 0 else { return 0 }
        return max(0, min(1, portion.today / portion.goal))
    }
    var message: LocalizedStringKey? {
        if c.intake?.supported == false { return "Update the hub [Home Widgets]" }
        if c.intake == nil { return "Open Persona to refresh [Home Widgets]" }
        return nil
    }
    var line: String {
        let name = kind == .water ? interfaceText("Water [Home Widgets]") : interfaceText("Protein [Home Widgets]") // perf-ok: drawn once per timeline entry
        return "\(name) \(amount) \(unit)"
    }
}

/// The small intake ring; medium keeps it and adds seven hub day buckets and a goal line.
struct HomeIntakeWidget: View {
    var kind: HomeKind
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    var body: some View {
        let value = HomeIntakeValue(kind: kind, c: c)
        VStack(alignment: .leading, spacing: HomeWidgetTokens.flush) {
            HomeHeader(title: kind.title, color: value.color, mood: c.mood(kind), pictures: pictures)
            if let message = value.message {
                Spacer(minLength: HomeWidgetTokens.flush)
                HomeEmpty(text: message, pictures: pictures, horizontal: family == .systemMedium)
                Spacer(minLength: HomeWidgetTokens.flush)
            } else {
                Spacer(minLength: HomeWidgetTokens.flush)
                HStack(spacing: HomeWidgetTokens.halves) {
                    VStack(spacing: HomeWidgetTokens.tight) {
                        ZStack {
                            HomeRing(fraction: value.fraction, color: value.color, showsZeroArc: false)
                            VStack(spacing: HomeWidgetTokens.flush) {
                                HomeHero(text: value.amount, font: HomeWidgetTokens.ringNumber)
                                Text(verbatim: value.unit).font(HomeWidgetTokens.caption).homeInk(.secondary)
                            }
                            .padding(HomeWidgetTokens.ringStroke)
                        }
                        .frame(width: HomeWidgetTokens.ringHero, height: HomeWidgetTokens.ringHero)
                        Text(verbatim: value.remaining).font(HomeWidgetTokens.caption).lineLimit(1)
                            .minimumScaleFactor(HomeWidgetTokens.heroMinScale).homeInk(.secondary)
                    }
                    .frame(maxWidth: family == .systemSmall ? .infinity : nil)
                    if family == .systemMedium, let intake = c.intake {
                        HomeWeekChart(values: intake.days.map { kind == .water ? $0.water : $0.protein },
                                      goal: value.portion?.goal ?? 0, now: c.date, color: value.color,
                                      calendar: HomeIntakeClock.calendar(intake.zone))
                    }
                }
                Spacer(minLength: HomeWidgetTokens.flush)
            }
        }
        .dynamicTypeSize(...HomeWidgetTokens.healthLargestTypeSize)
    }
}

struct HomeIntakeLock: View {
    var kind: HomeKind
    var c: HomeContext
    var family: WidgetFamily

    var body: some View {
        let value = HomeIntakeValue(kind: kind, c: c)
        Group {
            if let message = value.message {
                if family == .accessoryCircular {
                    VStack(spacing: HomeWidgetTokens.flush) {
                        HomeWidgetTokens.symbol(value.symbol)
                        Text(message).font(HomeWidgetTokens.lockLine).multilineTextAlignment(.center)
                    }
                } else {
                    Label { Text(message) } icon: { HomeWidgetTokens.symbol(value.symbol) }
                }
            } else {
                switch family {
                case .accessoryCircular:
                    Gauge(value: value.fraction) {
                        HomeWidgetTokens.symbol(value.symbol)
                    } currentValueLabel: {
                        Text(verbatim: value.amount).minimumScaleFactor(HomeWidgetTokens.heroMinScale)
                    }
                    .gaugeStyle(.accessoryCircularCapacity)
                    .accessibilityLabel(Text(verbatim: value.line))
                    .accessibilityValue(Text(verbatim: value.remaining))
                case .accessoryInline:
                    Label(value.line, systemImage: value.symbol)
                default:
                    VStack(alignment: .leading, spacing: HomeWidgetTokens.tight) {
                        Text(verbatim: value.line).font(HomeWidgetTokens.lockTitle).lineLimit(1)
                            .minimumScaleFactor(HomeWidgetTokens.heroMinScale)
                        Text(verbatim: value.remaining).font(HomeWidgetTokens.lockLine).lineLimit(1)
                        HomeBar(fraction: value.fraction, color: value.color, height: HomeWidgetTokens.activityBar)
                    }
                }
            }
        }
        .homeAccent()
    }
}

/// Four positions, never grouped by value: duplicates remain distinct cells. No Lock Screen family is registered.
struct HomeHealthTodayWidget: View {
    var c: HomeContext
    var family: WidgetFamily
    var pictures: HomeFacePictures?

    var body: some View {
        VStack(alignment: .leading, spacing: HomeWidgetTokens.flush) {
            HomeHeader(title: HomeKind.healthToday.title, color: HomeWidgetTokens.water, mood: c.mood(.healthToday), pictures: pictures)
            Spacer(minLength: HomeWidgetTokens.tight)
            if family == .systemSmall {
                Grid(horizontalSpacing: HomeWidgetTokens.gap, verticalSpacing: HomeWidgetTokens.tight) {
                    GridRow { cell(0); cell(1) }
                    GridRow { cell(2); cell(3) }
                }
                .frame(maxWidth: .infinity)
            } else {
                HStack(alignment: .top, spacing: HomeWidgetTokens.tight) {
                    ForEach(0..<4, id: \.self) { cell($0).frame(maxWidth: .infinity) }
                }
            }
            Spacer(minLength: HomeWidgetTokens.flush)
        }
        .dynamicTypeSize(...HomeWidgetTokens.healthLargestTypeSize)
    }

    @ViewBuilder private func cell(_ position: Int) -> some View {
        let item = c.healthItems.indices.contains(position) ? c.healthItems[position] : HomeHealthItem.defaults[position]
        if item == .activity {
            HomeActivityCell(activity: c.activity, named: family == .systemMedium)
        } else {
            HomeHealthRingCell(item: item, c: c, named: family == .systemMedium)
        }
    }
}

private struct HomeHealthRingCell: View {
    var item: HomeHealthItem
    var c: HomeContext
    var named: Bool

    private var side: CGFloat { named ? HomeWidgetTokens.ringMedium : HomeWidgetTokens.ringSmall }
    private var kind: HomeKind { item == .protein ? .protein : .water }
    private var intake: HomeIntakeValue { .init(kind: kind, c: c) }
    private var isIntake: Bool { item == .water || item == .protein }
    private var value: Double? {
        switch item {
        case .water, .protein: intake.portion?.today
        case .steps: c.healthDayRolled ? 0 : c.snapshot.health?.steps.map(Double.init)
        case .sleep: c.healthDayRolled ? nil : c.snapshot.health?.sleepHours
        case .activity: nil
        }
    }
    private var reference: Double? {
        switch item {
        case .water, .protein: intake.portion?.goal
        case .steps: c.snapshot.health?.stepsAverage.map(Double.init)
        case .sleep: c.snapshot.health?.sleepAverage
        case .activity: nil
        }
    }
    private var words: String {
        guard let value else { return "–" }
        return value.formatted(.number.precision(.fractionLength(0...1)))
    }
    private var title: LocalizedStringKey {
        switch item {
        case .water: "Water [Home Widgets]"
        case .protein: "Protein [Home Widgets]"
        case .steps: "Steps [Home Widgets]"
        case .sleep: "Asleep [Home Widgets]"
        case .activity: "Activity [Home Widgets]"
        }
    }
    private var color: Color {
        switch item {
        case .water, .protein: intake.color
        case .steps: HomeWidgetTokens.healthTodaySteps
        case .sleep: HomeWidgetTokens.sleep
        case .activity: HomeWidgetTokens.activity
        }
    }
    private var symbol: String {
        switch item {
        case .water, .protein: intake.symbol
        case .steps: HomeWidgetTokens.stepsSymbol
        case .sleep: HomeWidgetTokens.sleepSymbol
        case .activity: HomeWidgetTokens.fitnessSymbol
        }
    }

    var body: some View {
        VStack(spacing: HomeWidgetTokens.tight) {
            if isIntake, let message = intake.message {
                VStack(spacing: HomeWidgetTokens.hair) {
                    HomeWidgetTokens.symbol(symbol).homeTone(color)
                    Text(message).font(HomeWidgetTokens.healthCellCaption).multilineTextAlignment(.center)
                        .minimumScaleFactor(HomeWidgetTokens.heroMinScale)
                }
                .frame(width: side, height: side)
            } else {
                ZStack {
                    HomeRing(fraction: (reference ?? 0) > 0 ? (value ?? 0) / reference! : 0,
                             color: color, width: HomeWidgetTokens.ringStrokeSmall, showsZeroArc: false)
                    VStack(spacing: HomeWidgetTokens.flush) {
                        HomeWidgetTokens.symbol(symbol).font(HomeWidgetTokens.healthCellCaption).homeTone(color)
                        Text(verbatim: words).font(HomeWidgetTokens.activityNumber).lineLimit(1)
                            .minimumScaleFactor(HomeWidgetTokens.heroMinScale).homeInk(.primary)
                    }
                    .padding(HomeWidgetTokens.ringStrokeSmall)
                }
                .frame(width: side, height: side)
            }
            if named {
                Text(title).font(HomeWidgetTokens.captionStrong).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.heroMinScale)
                if isIntake { Text(verbatim: intake.unit).font(HomeWidgetTokens.healthCellCaption).homeInk(.secondary) }
                if item == .sleep { Text("Hours [Home Widgets]").font(HomeWidgetTokens.healthCellCaption).homeInk(.secondary) }
            }
        }
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text(title))
        .accessibilityValue(accessibilityValue)
    }

    private var accessibilityValue: Text {
        if isIntake, let message = intake.message { return Text(message) }
        if isIntake { return Text(verbatim: "\(intake.amount) \(intake.unit), \(intake.remaining)") }
        let current = item == .sleep && value != nil
            ? Duration.seconds(value! * 3600).formatted(.units(allowed: [.hours, .minutes], width: .narrow)) : words
        if let reference {
            let average = item == .sleep
                ? Duration.seconds(reference * 3600).formatted(.units(allowed: [.hours, .minutes], width: .narrow))
                : reference.formatted(.number.precision(.fractionLength(0...1)))
            return Text("\(current), 7-day average \(average) [Home Widgets]")
        }
        return Text(verbatim: current)
    }
}

/// HIG Activity rings cannot be replicated in a widget. These neutral, straight bars follow the summary's rows.
struct HomeActivityCell: View {
    var activity: HomeSnapshot.Activity?
    var named: Bool

    var body: some View {
        VStack(spacing: HomeWidgetTokens.hair) {
            HomeWidgetTokens.symbol(HomeWidgetTokens.fitnessSymbol).font(HomeWidgetTokens.healthCellCaption)
            if let activity, !activity.needsAccess {
                ForEach(activity.rows) { row in
                    HStack(spacing: HomeWidgetTokens.hair) {
                        HomeBar(fraction: row.fraction, color: HomeWidgetTokens.activity, height: HomeWidgetTokens.activityBar)
                            .frame(width: HomeWidgetTokens.activityBarWidth)
                        Text(verbatim: named ? line(row, activity) : row.value.formatted(.number.precision(.fractionLength(0))))
                            .font(HomeWidgetTokens.activityNumber).lineLimit(1).minimumScaleFactor(HomeWidgetTokens.heroMinScale)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                if activity.isPaused { Text("Paused [Home Widgets]").font(HomeWidgetTokens.healthCellCaption).lineLimit(1) }
            } else {
                Text("Allow on Health home [Home Widgets]").font(HomeWidgetTokens.healthCellCaption)
                    .multilineTextAlignment(.center).minimumScaleFactor(HomeWidgetTokens.heroMinScale)
            }
        }
        .homeInk(.secondary)
        .frame(width: named ? nil : HomeWidgetTokens.ringSmall, height: named ? HomeWidgetTokens.ringMedium : HomeWidgetTokens.ringSmall)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text("Activity [Home Widgets]"))
        .accessibilityValue(Text(verbatim: spoken))
    }

    private func line(_ row: HomeSnapshot.Activity.Row, _ activity: HomeSnapshot.Activity) -> String {
        let name: String
        let unit: String
        switch row.id {
        case .move:
            name = interfaceText("Move [Home Widgets]") // perf-ok: drawn once per timeline entry
            unit = activity.moveMode == .energy ? interfaceText("kcal") : interfaceText("min") // perf-ok: drawn once per timeline entry
        case .exercise:
            name = interfaceText("Exercise [Home Widgets]"); unit = interfaceText("min") // perf-ok: drawn once per timeline entry
        case .stand:
            name = interfaceText("Stand [Home Widgets]"); unit = interfaceText("hr") // perf-ok: drawn once per timeline entry
        }
        return "\(name) \(row.value.formatted(.number.precision(.fractionLength(0))))/\(row.goal.formatted(.number.precision(.fractionLength(0)))) \(unit)"
    }

    private var spoken: String {
        guard let activity, !activity.needsAccess else { return interfaceText("Allow on Health home [Home Widgets]") } // perf-ok: drawn once per timeline entry
        let rows = activity.rows.map { line($0, activity) }.joined(separator: ", ")
        return activity.isPaused ? rows + ", " + interfaceText("Paused [Home Widgets]") : rows // perf-ok: drawn once per timeline entry
    }
}
#endif
