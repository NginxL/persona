import Foundation
import HealthKit
import Testing
@testable import Persona

@MainActor struct HealthActivityTests {
    private func summary(moveOnly: Bool = false) -> HKActivitySummary {
        let value = HKActivitySummary()
        value.activeEnergyBurned = HKQuantity(unit: .kilocalorie(), doubleValue: 320)
        value.activeEnergyBurnedGoal = HKQuantity(unit: .kilocalorie(), doubleValue: 500)
        value.appleExerciseTime = HKQuantity(unit: .minute(), doubleValue: 25)
        value.appleStandHours = HKQuantity(unit: .count(), doubleValue: 8)
        if !moveOnly {
            value.exerciseTimeGoal = HKQuantity(unit: .minute(), doubleValue: 30)
            value.standHoursGoal = HKQuantity(unit: .count(), doubleValue: 12)
        }
        return value
    }

    @Test func convertsThreeRingsWithoutLosingTheGoals() {
        let reading = HealthActivityReading(summary())
        #expect(reading.moveMode == .energy && reading.move == 320 && reading.moveGoal == 500)
        #expect(reading.exercise == 25 && reading.exerciseGoal == 30)
        #expect(reading.stand == 8 && reading.standGoal == 12)
    }

    @Test func missingGoalsMeanMoveAloneEvenWithExerciseValues() {
        let reading = HealthActivityReading(summary(moveOnly: true))
        #expect(reading.move == 320 && reading.moveGoal == 500)
        #expect(reading.exerciseGoal == nil && reading.standGoal == nil)
        #expect(reading.exercise == 25 && reading.stand == 8)
    }

    @Test func moveTimeUsesMinutesAndKeepsPauseState() {
        let value = summary()
        value.activityMoveMode = .appleMoveTime
        value.appleMoveTime = HKQuantity(unit: .minute(), doubleValue: 40)
        value.appleMoveTimeGoal = HKQuantity(unit: .minute(), doubleValue: 60)
        value.isPaused = true
        let reading = HealthActivityReading(value)
        #expect(reading.moveMode == .time && reading.move == 40 && reading.moveGoal == 60)
        #expect(reading.isPaused)
    }

    @Test func todayHasItsCalendarAndLocalDateAtMidnight() throws {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try #require(TimeZone(identifier: "Asia/Tokyo"))
        let now = try #require(ISO8601DateFormatter().date(from: "2026-11-22T15:00:01Z"))
        let day = HealthActivityReader.dayComponents(now: now, calendar: calendar)
        #expect(day.calendar == calendar)
        #expect(day.year == 2026 && day.month == 11 && day.day == 23 && day.era == 1)
        #expect(day.hour == nil)
    }

    @Test func requestStatusDoesNotClaimReadPermission() {
        #expect(HealthActivityReader.permission(.shouldRequest) == .shouldRequest)
        #expect(HealthActivityReader.permission(.unnecessary) == .unnecessary)
        #expect(HealthActivityReader.permission(.unknown) == .unknown)
        #expect(HealthTool.readTypes.contains(HKObjectType.activitySummaryType()))
    }

    @Test func loadingNeverRequestsAndAllowOnlyAsksWhenNeeded() async {
        var permission = HealthActivityReader.Permission.shouldRequest
        var requests = 0
        var reads = 0
        let model = HealthActivityModel(deps: .init(permission: { permission }, read: { reads += 1; return nil },
                                                  request: { requests += 1; permission = .unnecessary }))
        await model.load()
        #expect(model.state == .needsAccess && requests == 0 && reads == 0)
        await model.requestAccess()
        #expect(requests == 1 && reads == 1 && model.state == .noData)
        await model.requestAccess()
        #expect(requests == 1, "unnecessary does not establish read access, and never prompts again")
    }

    @Test func unsupportedAndFailedReadsStayDistinctFromNoData() async {
        let unavailable = HealthActivityModel(deps: .init(permission: { .unavailable }, read: { Issue.record("must not query"); return nil }, request: {}))
        await unavailable.load()
        #expect(unavailable.state == .unavailable)
        let unknown = HealthActivityModel(deps: .init(permission: { .unknown }, read: { Issue.record("must not query"); return nil }, request: {}))
        await unknown.load()
        #expect(unknown.state == .failed)
        let failed = HealthActivityModel(deps: .init(permission: { .unnecessary }, read: { throw URLError(.cannotLoadFromNetwork) }, request: {}))
        await failed.load()
        #expect(failed.state == .failed)
        let reading = HealthActivityReading(summary())
        let loaded = HealthActivityModel(deps: .init(permission: { .unnecessary }, read: { reading }, request: {}))
        await loaded.load()
        #expect(loaded.state == .loaded(reading))
    }
}
