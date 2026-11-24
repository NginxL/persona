import Foundation
import HealthKit

/// A day's Activity values, independent of UIKit and safe to share with widgets. Missing Exercise and Stand goals
/// mean Move alone; zero progress with a goal still means a ring exists.
nonisolated struct HealthActivityReading: Equatable, Sendable {
    enum MoveMode: Sendable { case energy, time }
    var date: Date
    var moveMode: MoveMode
    var move: Double
    var moveGoal: Double
    var exercise: Double
    var exerciseGoal: Double?
    var stand: Double
    var standGoal: Double?
    var isPaused: Bool

    init(_ summary: HKActivitySummary, calendar: Calendar = .current) {
        date = calendar.date(from: summary.dateComponents(for: calendar)) ?? calendar.startOfDay(for: Date())
        moveMode = summary.activityMoveMode == .appleMoveTime ? .time : .energy
        let unit: HKUnit = moveMode == .time ? .minute() : .kilocalorie()
        move = (moveMode == .time ? summary.appleMoveTime : summary.activeEnergyBurned).doubleValue(for: unit)
        moveGoal = (moveMode == .time ? summary.appleMoveTimeGoal : summary.activeEnergyBurnedGoal).doubleValue(for: unit)
        exercise = summary.appleExerciseTime.doubleValue(for: .minute())
        exerciseGoal = summary.exerciseTimeGoal?.doubleValue(for: .minute())
        stand = summary.appleStandHours.doubleValue(for: .count())
        standGoal = summary.standHoursGoal?.doubleValue(for: .count())
        if #available(iOS 18.0, watchOS 11.0, macOS 15.0, visionOS 2.0, *) {
            isPaused = summary.isPaused
        } else {
            isPaused = false
        }
    }
}

nonisolated enum HealthActivityReader {
    enum Permission: Equatable, Sendable { case shouldRequest, unnecessary, unavailable, unknown }

    /// Queries exactly one local calendar day, including across midnight and daylight-saving changes.
    static func dayComponents(now: Date, calendar: Calendar) -> DateComponents {
        var day = calendar.dateComponents([.era, .year, .month, .day], from: now)
        day.calendar = calendar
        return day
    }

    static func permission(_ status: HKAuthorizationRequestStatus) -> Permission {
        switch status {
        case .shouldRequest: .shouldRequest
        case .unnecessary: .unnecessary
        default: .unknown
        }
    }

    /// A status about whether to ask, never a claim that read access was granted.
    @concurrent static func permission() async throws -> Permission {
        guard HKHealthStore.isHealthDataAvailable() else { return .unavailable }
        let status = try await HKHealthStore().statusForAuthorizationRequest(toShare: [], read: [HKObjectType.activitySummaryType()])
        return permission(status)
    }

    /// Read-only: neither widgets nor background callers can cause a permission prompt here.
    @concurrent static func today(now: Date = Date(), calendar: Calendar = .current) async throws -> HealthActivityReading? {
        guard HKHealthStore.isHealthDataAvailable() else { return nil }
        let day = dayComponents(now: now, calendar: calendar)
        let predicate = HKQuery.predicate(forActivitySummariesBetweenStart: day, end: day)
        let summaries = try await HKActivitySummaryQueryDescriptor(predicate: predicate).result(for: HKHealthStore())
        return summaries.first.map { HealthActivityReading($0, calendar: calendar) }
    }
}
