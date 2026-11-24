import Foundation
import HealthKit
import SwiftUI
import Testing
import WidgetKit
@testable import Persona

extension HomeWidgetTests {
    private func widgetDeps(api: @escaping () -> APIClient? = { nil }, supported: @escaping () -> Bool = { true }) -> HomeWidgetData.HealthDeps {
        .init(api: api, supportsIntake: supported, healthAllowed: { false }, activityPermission: { .unavailable },
              activity: { nil }, now: { HomeSamples.now }, changed: {}, snapshot: { nil })
    }

    @Test func aColdLockedReadKeepsTheSnapshotAndAnEmptyReadStillClearsIt() async {
        var deps = widgetDeps()
        var restores = 0
        var empty = false
        deps.snapshot = { restores += 1; return HomeSamples.busy() }
        deps.healthAllowed = { true }
        deps.activityPermission = { .unnecessary }
        deps.activity = {
            if empty { return nil }
            throw URLError(.dataNotAllowed)
        }
        let data = HomeWidgetData(healthDeps: deps)
        data.installed = [.healthToday]
        await data.readActivity()
        #expect(data.activity?.move == 320 && restores == 1)
        empty = true
        await data.readActivity()
        #expect(data.activity == nil)
        await data.restore()
        #expect(data.activity == nil && restores == 1, "a cleared value must not be revived by restoring again")
    }

    @Test func coldIntakeCacheBelongsToItsHubAndIgnoresTheSelectedManager() async {
        let first = FakeHub(), second = FakeHub(url: "https://second.fake.invalid")
        var cached = HomeSamples.busy()
        cached.intake?.scope = HomeWidgetData.scope(first.client)
        var deps = widgetDeps(api: { first.client.showing("another-manager") })
        deps.snapshot = { cached }
        let same = HomeWidgetData(healthDeps: deps)
        await same.restore()
        #expect(same.intake?.water.today == 1250)
        first.on("GET", "/api/v1/assistant/health/intake") { _ in throw URLError(.notConnectedToInternet) }
        same.installed = [.water]
        same.intakeChanged()
        await waitForWidget { first.requests.count == 1 }
        #expect(same.intake?.water.today == 1250, "a cold failed HTTP read retains this hub's cached amount")
        deps.api = { second.client }
        let other = HomeWidgetData(healthDeps: deps)
        await other.restore()
        #expect(other.intake == nil)
        #expect(other.activity?.move == 320, "phone Health readings do not belong to a hub")
    }

    private func waitForWidget(_ ready: () -> Bool) async {
        let end = Date.now.addingTimeInterval(3)
        while !ready(), Date.now < end { await Task.yield() }
        #expect(ready())
    }

    private func widgetIntake(_ amount: Double) -> Data {
        Data("""
        {"water":{"today":\(amount),"goal":2000,"entries":[]},"protein":{"today":42,"goal":60,"entries":[]},
         "days":[{"date":"2026-11-12","water":\(amount),"protein":42}],"zone":"UTC"}
        """.utf8)
    }

    @Test func widgetReaderUsesHelloAndSkipsUnplacedIntake() async {
        let hub = FakeHub()
        let data = HomeWidgetData(healthDeps: widgetDeps(api: { hub.client }, supported: { false }))
        data.intakeChanged()
        await Task.yield()
        #expect(data.intake == nil && hub.requests.isEmpty, "wait for WidgetCenter before reading intake")
        data.installed = [.health]
        data.intakeChanged()
        await Task.yield()
        #expect(data.intake == nil && hub.requests.isEmpty)
        data.installed = [.water]
        data.intakeChanged()
        await waitForWidget { data.intake?.supported == false }
        #expect(hub.requests.isEmpty, "hello gates old hubs without probing a route")
    }

    @Test func widgetLocalConfirmationWinsOverAnOlderReadAndRefetches() async throws {
        let hub = FakeHub()
        let api = hub.client.showing("another-manager")
        let data = HomeWidgetData(healthDeps: widgetDeps(api: { api }))
        data.installed = [.protein]
        var release: CheckedContinuation<Void, Never>?
        var reads = 0
        hub.onAsync("GET", "/api/v1/assistant/health/intake") { request in
            #expect(request.query["space"] == "health" && request.query["manager"] == nil)
            #expect(request.headers[APIClient.managerHeader] == nil)
            reads += 1
            if reads == 1 {
                await withCheckedContinuation { release = $0 }
                return widgetIntake(750)
            }
            #expect(data.intake?.water.today == 1250, "an older response must not replace the local confirmed total")
            if reads >= 3 { throw URLError(.notConnectedToInternet) }
            return widgetIntake(1250)
        }
        data.intakeChanged()
        await waitForWidget { release != nil }
        let confirmed = try JSONDecoder().decode(HealthIntake.self, from: widgetIntake(1250))
        data.intakeChanged(on: api, confirmed: confirmed)
        #expect(data.intake?.water.today == 1250, "published synchronously after a successful local write")
        release?.resume()
        await waitForWidget { reads == 2 }
        #expect(data.intake?.water.today == 1250 && data.intake?.days.last?.water == 1250)
        data.intakeChanged()
        await waitForWidget { reads == 3 }
        #expect(data.intake?.water.today == 1250, "a failed read keeps the confirmed value")
    }

    @Test func intakePollWaitsFifteenMinutesButAWakeReadsImmediately() async {
        let hub = FakeHub()
        var now = HomeSamples.now
        var reads = 0
        var deps = widgetDeps(api: { hub.client })
        deps.now = { now }
        hub.on("GET", "/api/v1/assistant/health/intake") { _ in reads += 1; return widgetIntake(750) }
        let data = HomeWidgetData(healthDeps: deps)
        data.installed = [.water]
        data.refresh(now: now)
        await waitForWidget { data.intake != nil }
        now = now.addingTimeInterval(14 * 60)
        data.refresh(now: now)
        await Task.yield()
        #expect(reads == 1)
        data.refresh(forceIntake: true, now: now)
        await waitForWidget { reads == 2 }
        now = now.addingTimeInterval(16 * 60)
        data.refresh(now: now)
        await waitForWidget { reads == 3 }
        data.installed = []
        data.refresh(forceIntake: true, now: now)
        await Task.yield()
        #expect(reads == 3)
    }

    @Test func activityReaderKeepsFailedReadsClearsEmptyReadsAndNeverRequests() async {
        enum Failure: Error { case locked }
        let summary = HKActivitySummary()
        summary.activeEnergyBurned = HKQuantity(unit: .kilocalorie(), doubleValue: 320)
        summary.activeEnergyBurnedGoal = HKQuantity(unit: .kilocalorie(), doubleValue: 500)
        let reading = HealthActivityReading(summary)
        var permission = HealthActivityReader.Permission.unnecessary
        var allowed = true
        var mode = "value"
        var reads = 0
        var permissions = 0
        var deps = widgetDeps()
        deps.healthAllowed = { allowed }
        deps.activityPermission = { permissions += 1; return permission }
        deps.activity = {
            reads += 1
            if mode == "locked" { throw Failure.locked }
            return mode == "empty" ? nil : reading
        }
        let data = HomeWidgetData(healthDeps: deps)
        data.installed = [.water]
        await data.readActivity()
        #expect(reads == 0 && permissions == 0)
        data.installed = [.healthToday]
        await data.readActivity()
        #expect(data.activity?.move == 320 && data.activity?.rows.count == 1)
        mode = "locked"
        await data.readActivity()
        #expect(data.activity?.move == 320)
        mode = "empty"
        await data.readActivity()
        #expect(data.activity == nil)
        permission = .shouldRequest
        await data.readActivity()
        #expect(data.activity?.needsAccess == true && reads == 3)
        allowed = false
        await data.readActivity()
        #expect(data.activity == nil && permissions == 4)
    }

    @Test func anActivityReadFinishingAfterMidnightKeepsItsOriginalBoundary() async {
        let summary = HKActivitySummary()
        summary.activeEnergyBurned = HKQuantity(unit: .kilocalorie(), doubleValue: 320)
        let reading = HealthActivityReading(summary)
        var now = HomeIntakeClock.nextDay(after: HomeSamples.now, zone: "UTC").addingTimeInterval(-1)
        var deps = widgetDeps()
        deps.healthAllowed = { true }
        deps.activityPermission = { .unnecessary }
        deps.now = { now }
        deps.activity = { now = now.addingTimeInterval(2); return reading }
        let data = HomeWidgetData(healthDeps: deps)
        data.installed = [.healthToday]
        await data.readActivity()
        #expect(data.activity?.move == 320)
        let context = HomeContext(snapshot: HomeSnapshot(paired: true, activity: data.activity), date: now)
        #expect(context.activity?.move == 0, "yesterday's in-flight summary cannot become today's")
    }

    @Test func intakeAndActivityAreOptionalVersionOneFields() throws {
        let sample = HomeSamples.busy()
        let data = try #require(HomeWidgetWriter.encode(sample))
        let decoded = try JSONDecoder().decode(HomeSnapshot.self, from: data)
        #expect(decoded.intake?.water.today == 1250)
        #expect(decoded.intake?.protein.goal == 60)
        #expect(decoded.activity?.rows.map(\.id) == [.move, .exercise, .stand])
        #expect(decoded.health?.sleepAverage == 7.5)
        var old = try #require(JSONSerialization.jsonObject(with: data) as? [String: Any])
        old.removeValue(forKey: "intake")
        old.removeValue(forKey: "activity")
        var health = try #require(old["health"] as? [String: Any])
        health.removeValue(forKey: "sleepAverage")
        old["health"] = health
        let restored = try JSONDecoder().decode(HomeSnapshot.self, from: JSONSerialization.data(withJSONObject: old))
        #expect(restored.version == 1 && HomeSnapshot.version == 1)
        #expect(restored.intake == nil && restored.activity == nil && restored.health?.sleepAverage == nil)
        #expect(restored.health?.steps == sample.health?.steps)
    }

    @Test func healthWidgetPartsReloadOnlyTheirOwnKinds() {
        let original = HomeSamples.busy()
        func changed(_ edit: (inout HomeSnapshot) -> Void) -> Set<HomeKind> {
            var updated = original
            edit(&updated)
            let a = HomeWidgetWriter.parts(original), b = HomeWidgetWriter.parts(updated)
            return Set(HomeKind.allCases.filter { a[$0] != b[$0] })
        }
        #expect(changed { $0.intake?.water.today += 250 } == [.water, .healthToday])
        #expect(changed { $0.intake?.days[0].water += 250 } == [.water, .healthToday])
        #expect(changed { $0.intake?.protein.today += 12.3 } == [.protein, .healthToday])
        #expect(changed { $0.intake?.days[0].protein += 12.3 } == [.protein, .healthToday])
        #expect(changed { $0.activity?.move += 10 } == [.healthToday])
        #expect(changed { $0.health?.sleepAverage = 8 } == [.health, .healthToday])
        #expect(changed { $0.intake?.supported = false } == [.water, .protein, .healthToday])
    }

    @Test func fourHealthChoicesDefaultAndKeepDuplicatesInPlace() {
        var entry = HomeEntry(date: HomeSamples.now, snapshot: HomeSamples.busy())
        #expect(entry.healthItems == [.water, .protein, .activity, .sleep])
        #expect(entry.context.healthItems == HomeHealthItem.defaults)
        entry.healthItems = [.steps, .water, .steps, .sleep]
        #expect(entry.context.healthItems == [.steps, .water, .steps, .sleep])
        entry.healthItems[0] = .protein
        #expect(entry.context.healthItems[2] == .steps)
        #expect(HomeHealthItem.allCases.count == 5)
    }

    @Test func newKindsKeepTheirNamesFamiliesAndPrivacy() {
        for kind in [HomeKind.water, .protein] {
            #expect(kind.widgetKind == "persona.home.\(kind.rawValue)")
            #expect(kind.families == [.systemSmall, .systemMedium, .accessoryCircular, .accessoryRectangular, .accessoryInline])
            #expect(!kind.isPrivate)
        }
        #expect(HomeKind.healthToday.widgetKind == "persona.home.healthToday")
        #expect(HomeKind.healthToday.families == [.systemSmall, .systemMedium])
        #expect(HomeKind.healthToday.isPrivate && HomeKind.health.isPrivate)
        #expect(HomeKind.health.widgetKind == "persona.home.health")
    }

    @Test func hubDayBoundaryClearsTheNextEntryAndKeepsHistory() throws {
        let now = try #require(ISO8601DateFormatter().date(from: "2026-11-23T06:00:00Z"))
        var snapshot = HomeSamples.busy(now: now)
        snapshot.intake?.zone = "America/Los_Angeles"
        snapshot.intake?.day = "2026-11-22"
        snapshot.intake?.nextDay = try #require(ISO8601DateFormatter().date(from: "2026-11-23T08:00:00Z")).timeIntervalSince1970
        snapshot.intake?.days = [.init(date: "2026-11-22", water: 1250, protein: 42)]
        let boundary = try #require(HomeIntakeClock.boundary(snapshot, after: now))
        #expect(boundary.timeIntervalSince(now) == 2 * 3600)
        for kind in [HomeKind.water, .protein, .healthToday] {
            #expect(HomeHealthTimeline.dates(kind, snapshot: snapshot, after: now) == [boundary])
        }
        #expect(HomeHealthTimeline.dates(.health, snapshot: snapshot, after: now).isEmpty)
        let before = HomeEntry(date: boundary.addingTimeInterval(-1), snapshot: snapshot)
        let after = HomeEntry(date: boundary, snapshot: snapshot)
        #expect(before.context.intake?.water.today == 1250)
        #expect(after.context.intake?.water.today == 0 && after.context.intake?.protein.today == 0)
        #expect(after.context.intake?.water.goal == 2000)
        #expect(after.context.intake?.days.last == .init(date: "2026-11-23", water: 0, protein: 0))
        #expect(after.context.intake?.days.dropLast().last?.water == 1250)
        #expect(after.context.activity?.rows.allSatisfy { $0.value == 0 } == true)
        #expect(after.snapshot.health?.steps == 6842, "the existing Health widget keeps its own snapshot")
    }

    @Test func hubClockHandlesUTCAndTheLongDayWhenDSTEnds() throws {
        let now = try #require(ISO8601DateFormatter().date(from: "2026-11-01T08:30:00Z"))
        #expect(HomeIntakeClock.nextDay(after: now, zone: "UTC") == ISO8601DateFormatter().date(from: "2026-11-02T00:00:00Z"))
        #expect(HomeIntakeClock.nextDay(after: now, zone: "America/Los_Angeles") == ISO8601DateFormatter().date(from: "2026-11-02T08:00:00Z"))
        #expect(HomeIntakeClock.key(now, zone: "Pacific/Honolulu") == "2026-10-31")
        #expect(HomeIntakeClock.calendar("UTC").timeZone.secondsFromGMT() == 0)
    }

    @Test func intakeConversionUsesTheHubDateAndPreservesDecimalAmounts() throws {
        let json = #"{"water":{"today":1250.5,"goal":2000,"entries":[]},"protein":{"today":42.3,"goal":60,"entries":[]},"days":[{"date":"2026-11-22","water":1250.5,"protein":42.3}],"zone":"America/Los_Angeles"}"#
        let read = try JSONDecoder().decode(HealthIntake.self, from: Data(json.utf8))
        let saved = HomeIntakeClock.snapshot(read, now: try #require(ISO8601DateFormatter().date(from: "2026-11-23T06:00:00Z")))
        #expect(saved.day == "2026-11-22")
        #expect(saved.water.today == 1250.5 && saved.days.last?.protein == 42.3)
        #expect(Date(timeIntervalSince1970: saved.nextDay) == ISO8601DateFormatter().date(from: "2026-11-23T08:00:00Z"))
    }

    @Test func aLocalWriteAfterMidnightKeepsYesterdayAndShowsTheNewTotalImmediately() throws {
        var value = try JSONDecoder().decode(HealthIntake.self, from: widgetIntake(750))
        value.water.today = 250
        let now = HomeIntakeClock.nextDay(after: HomeSamples.now, zone: "UTC").addingTimeInterval(1)
        let saved = HomeIntakeClock.confirmedSnapshot(value, now: now)
        #expect(saved.day == "2026-11-13" && saved.water.today == 250)
        #expect(saved.days.last?.water == 250)
        #expect(saved.days.dropLast().last?.water == 750)
        #expect(HomeIntakeClock.current(saved, at: now)?.water.today == 250)
    }

    @Test func activityRowsFollowPresenceOfGoalsIncludingZero() {
        var activity = HomeSnapshot.Activity(date: 0, move: 320, moveGoal: 500)
        #expect(activity.rows.map(\.id) == [.move])
        activity.exerciseGoal = 0
        activity.standGoal = 12
        #expect(activity.rows.map(\.id) == [.move, .exercise, .stand])
        #expect(activity.rows[0].fraction == 0.64)
        #expect(activity.rows[1].fraction == 0)
        activity.isPaused = true
        #expect(activity.rows.count == 3)
        activity.needsAccess = true
        #expect(activity.rows.isEmpty)
    }

    @Test func aNewIntakeDayCannotReviveYesterdaysAppleValues() {
        let now = HomeSamples.now
        var saved = HomeSamples.busy(now: now)
        saved.health?.dayEnds = now.timeIntervalSince1970 - 1
        saved.activity?.dayEnds = now.timeIntervalSince1970 - 1
        #expect(saved.intake!.nextDay > now.timeIntervalSince1970, "intake has already refreshed for the new day")
        let context = HomeEntry(date: now, snapshot: saved).context
        #expect(context.healthDayRolled)
        #expect(context.activity?.move == 0)
        #expect(context.intake?.water.today == 1250)
    }

    @Test func sleepAverageExcludesTodayAndMissingDays() {
        let days = (0..<9).map { "day-\($0)" }
        let sleep = ["day-0": 100.0, "day-1": 7, "day-3": 8, "day-7": 6, "day-8": 20]
        #expect(HomeHealthAverage.previousWeek(sleep, days: days) == 7)
        #expect(HomeHealthAverage.previousWeek(["day-8": 20], days: days) == nil)
    }

    @Test func oldHubShowsTheUpdateHintInsteadOfAZeroTotal() {
        var snapshot = HomeSnapshot(paired: true)
        snapshot.intake = .init(supported: false)
        for kind in [HomeKind.water, .protein] {
            let value = HomeIntakeValue(kind: kind, c: HomeContext(snapshot: snapshot, date: .now))
            #expect(value.message != nil)
        }
        #expect(String(localized: "Update the hub [Home Widgets]", locale: Locale(identifier: "zh-Hans")) == "需要更新 hub")
    }

    /// Optional screenshot export of the actual gallery views. No app state, credentials or network is involved.
    @Test func exportHealthWidgetGallery() throws {
        guard let path = ProcessInfo.processInfo.environment["PERSONA_WIDGET_SHOTS"] else { return }
        let directory = URL(fileURLWithPath: path, isDirectory: true)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let entry = HomeEntry(date: HomeSamples.now, snapshot: HomeSamples.busy())
        for page in ["water", "water-mono", "protein", "protein-mono", "lock", "default", "move", "access", "mono", "vibrant", "choices", "largest"] {
            let view = HomeGallerySheet(title: "Persona · \(page)", note: "Synthetic data · gallery rendering") {
                HomeHealthGallery(page: page, entry: entry)
            }
            .fixedSize()
            .environment(\.locale, Locale(identifier: "zh-Hans"))
            let renderer = ImageRenderer(content: view)
            renderer.scale = 2
            let data = try #require(renderer.uiImage?.pngData())
            try data.write(to: directory.appending(path: "health-\(page).png"))
            print("HOME_WIDGET_SHOT \(page) \(data.count) bytes")
        }
    }
}
