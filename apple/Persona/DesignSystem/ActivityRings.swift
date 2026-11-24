#if os(iOS)
import SwiftUI
import HealthKit
import HealthKitUI

/// Apple's unmodified ring view and its values share a black surface. The values sit beside the rings and move under
/// them only when the row is too narrow for both, measured rather than read from the text size: the values are
/// interface text, capped at xxxLarge like the rest of the interface (DESIGN_SYSTEM.md, "Dynamic Type").
struct ActivityRings: View {
    var reading: HealthActivityReading

    var body: some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: Theme.Space.m) { rings; values.frame(maxWidth: .infinity, alignment: .leading) }
            VStack(alignment: .leading, spacing: Theme.Space.m) { rings; values }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(Theme.Space.m)
        .background(Theme.Health.activityBackground, in: .rect(cornerRadius: Theme.Radius.card))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(Text("Activity Rings"))
        .accessibilityValue(accessibilityReading)
        .accessibilityIdentifier("health-activity-rings")
    }

    private var rings: some View {
        ActivityRingSurface(reading: reading)
            .scaledFrame(width: Theme.Health.activitySide, height: Theme.Health.activitySide)
            .accessibilityHidden(true)
    }

    private var values: some View {
        VStack(alignment: .leading, spacing: Theme.Space.s) {
            value(String(localized: "Move [Activity Rings]"), moveValue, color: Theme.Health.move)
            if let goal = reading.exerciseGoal {
                value(String(localized: "Exercise"), minutes(reading.exercise, goal), color: Theme.Health.exercise)
            }
            if let goal = reading.standGoal {
                value(String(localized: "Stand"), hours(reading.stand, goal), color: Theme.Health.stand)
            }
            if reading.isPaused {
                Text("Activity Rings Paused").font(Theme.Font.secondary).foregroundStyle(Theme.onSolid)
            }
        }
    }

    private func value(_ title: String, _ value: String, color: Color) -> some View {
        VStack(alignment: .leading, spacing: Theme.Space.xxs) {
            Text(title).font(Theme.Font.labelStrong)
            Text(value).font(Theme.Font.secondary.monospacedDigit())
        }
        .foregroundStyle(color)
    }

    private var moveValue: String {
        if reading.moveMode == .time { return minutes(reading.move, reading.moveGoal) }
        return String(localized: "\(number(reading.move)) / \(number(reading.moveGoal)) kcal")
    }

    private func number(_ value: Double) -> String { value.formatted(.number.precision(.fractionLength(0))) }
    private func minutes(_ value: Double, _ goal: Double) -> String {
        String(localized: "\(number(value)) / \(number(goal)) min")
    }
    private func hours(_ value: Double, _ goal: Double) -> String {
        String(localized: "\(number(value)) / \(number(goal)) hr")
    }

    private var accessibilityReading: String {
        var parts = [String(localized: "Move [Activity Rings]") + ": " + moveValue]
        if let goal = reading.exerciseGoal { parts.append(String(localized: "Exercise") + ": " + minutes(reading.exercise, goal)) }
        if let goal = reading.standGoal { parts.append(String(localized: "Stand") + ": " + hours(reading.stand, goal)) }
        if reading.isPaused { parts.append(String(localized: "Activity Rings Paused")) }
        return parts.joined(separator: ", ")
    }
}

private struct ActivityRingSurface: UIViewRepresentable {
    var reading: HealthActivityReading

    final class Surface: UIView {
        let rings = HKActivityRingView()
        override init(frame: CGRect) {
            super.init(frame: frame)
            backgroundColor = UIColor(Theme.Health.activityBackground)
            isUserInteractionEnabled = false
            accessibilityElementsHidden = true
            rings.isAccessibilityElement = false
            rings.accessibilityElementsHidden = true
            addSubview(rings)
        }
        required init?(coder: NSCoder) { fatalError("init(coder:) has not been implemented") }
        override func layoutSubviews() {
            super.layoutSubviews()
            layer.cornerRadius = bounds.width / 2
            rings.frame = bounds.insetBy(dx: Theme.Health.ringMargin, dy: Theme.Health.ringMargin)
        }
    }

    func makeUIView(context: Context) -> Surface { Surface(frame: .zero) }

    func updateUIView(_ view: Surface, context: Context) {
        let summary = HKActivitySummary()
        summary.activityMoveMode = reading.moveMode == .time ? .appleMoveTime : .activeEnergy
        if reading.moveMode == .time {
            summary.appleMoveTime = HKQuantity(unit: .minute(), doubleValue: reading.move)
            summary.appleMoveTimeGoal = HKQuantity(unit: .minute(), doubleValue: reading.moveGoal)
        } else {
            summary.activeEnergyBurned = HKQuantity(unit: .kilocalorie(), doubleValue: reading.move)
            summary.activeEnergyBurnedGoal = HKQuantity(unit: .kilocalorie(), doubleValue: reading.moveGoal)
        }
        summary.appleExerciseTime = HKQuantity(unit: .minute(), doubleValue: reading.exercise)
        summary.exerciseTimeGoal = reading.exerciseGoal.map { HKQuantity(unit: .minute(), doubleValue: $0) }
        summary.appleStandHours = HKQuantity(unit: .count(), doubleValue: reading.stand)
        summary.standHoursGoal = reading.standGoal.map { HKQuantity(unit: .count(), doubleValue: $0) }
        summary.isPaused = reading.isPaused
        view.rings.setActivitySummary(summary, animated: false)
    }
}
#endif
