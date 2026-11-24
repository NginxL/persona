#if DEBUG && os(iOS)
import SwiftUI
import HealthKit

/// Local examples only: no hub routes, capabilities, demo hub or real Health data are used by these switches.
enum HealthHomeSamples {
    static func activityModel() -> HealthActivityModel {
        guard let mode = UserDefaults.standard.string(forKey: "showHealthActivityExample") else { return HealthActivityModel() }
        return HealthActivityModel(deps: .init(
            permission: { mode == "allow" ? .shouldRequest : .unnecessary },
            read: { mode == "unreadable" ? nil : reading(moveOnly: mode == "move") },
            request: {}))
    }

    static func reading(moveOnly: Bool = false) -> HealthActivityReading {
        let summary = HKActivitySummary()
        summary.activeEnergyBurned = HKQuantity(unit: .kilocalorie(), doubleValue: 320)
        summary.activeEnergyBurnedGoal = HKQuantity(unit: .kilocalorie(), doubleValue: 500)
        summary.appleExerciseTime = HKQuantity(unit: .minute(), doubleValue: 25)
        summary.appleStandHours = HKQuantity(unit: .count(), doubleValue: 8)
        if !moveOnly {
            summary.exerciseTimeGoal = HKQuantity(unit: .minute(), doubleValue: 30)
            summary.standHoursGoal = HKQuantity(unit: .count(), doubleValue: 12)
        }
        return HealthActivityReading(summary)
    }

    static func trendsModel() -> HealthTrendsModel {
        guard let mode = UserDefaults.standard.string(forKey: "showHealthHomeTrendsExample") else { return HealthTrendsModel() }
        return HealthTrendsModel(deps: .init(access: { .granted }, requestAccess: { true }, daily: { metric, days in
            guard mode != "empty" else { return [:] }
            var values: [String: Double] = [:]
            for (i, day) in HealthTrends.dayKeys(days, now: Date()).enumerated() {
                switch metric {
                case .steps: values[day] = Double(5200 + (i * 379) % 4800)
                case .sleep: values[day] = 6.2 + Double(i % 5) * 0.3
                case .weight: values[day] = 68.4 - Double(i % 7) * 0.1
                }
            }
            return values
        }, now: { Date() }))
    }
}
#endif
