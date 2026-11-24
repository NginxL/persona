#if os(iOS)
import EventKit
import Foundation
import CryptoKit

/// What the widgets show that the app reads on its own rather than from the hub's live state: today's calendar events,
/// Health's numbers, the subscription windows and today's tokens by the hour (docs/apple/home-widgets.md「What each
/// number counts」). Read only for the kinds on a screen (`installed`), at most every `every` a kind, off the main
/// actor; the writer takes what is here when it builds the snapshot, and a read that brought something new asks it for
/// a write.
@MainActor
final class HomeWidgetData {
    static let shared = HomeWidgetData()

    struct HealthDeps {
        var api: () -> APIClient?
        var supportsIntake: () -> Bool
        var healthAllowed: () -> Bool
        var activityPermission: () async throws -> HealthActivityReader.Permission
        var activity: () async throws -> HealthActivityReading?
        var now: () -> Date
        var changed: () -> Void
        var snapshot: () async -> HomeSnapshot?

        static var live: Self {
            .init(api: { AppModel.shared.api }, supportsIntake: { AppModel.shared.hubFeatures.contains(HealthIntake.feature) },
                  healthAllowed: { HealthAccess.cachedStatus() == .granted },
                  activityPermission: { try await HealthActivityReader.permission() },
                  activity: { try await HealthActivityReader.today() }, now: { .now },
                  changed: { HomeWidgetWriter.shared.flush() },
                  snapshot: { await Task.detached { HomeSnapshotStore.shared.read() }.value })
        }
    }

    private let healthDeps: HealthDeps
    init(healthDeps: HealthDeps = .live) { self.healthDeps = healthDeps }
    private var restored = false
    private var restoring: Task<HomeSnapshot?, Never>?

    /// A cold widget wake must keep the last successful Health read when the phone's database is locked. Intake
    /// restores only for this hub, so changing hubs can never expose the previous hub's amounts.
    func restore() async {
        guard !restored else { return }
        let read: Task<HomeSnapshot?, Never>
        if let restoring { read = restoring }
        else {
            let source = healthDeps.snapshot
            read = Task { await source() }
            restoring = read
        }
        let saved = await read.value
        guard !restored else { return }
        restored = true
        restoring = nil
        guard let saved, saved.version == HomeSnapshot.version else { return }
        if health == nil { health = saved.health }
        if activity == nil { activity = saved.activity }
        if intake == nil, let api = healthDeps.api()?.manager(nil), saved.intake?.scope == Self.scope(api) {
            intake = saved.intake
            intakeScope = api
        }
    }

    /// The default manager's hub identity, independent of a selected manager and without tokens or other secrets.
    static func scope(_ api: APIClient) -> String {
        SHA256.hash(data: Data((api.config.url.absoluteString + "\n" + api.group).utf8))
            .map { String(format: "%02x", $0) }.joined()
    }

    /// The kinds on this device's screens (`WidgetCenter`'s configurations, also obtained on a cold wake); nil before the
    /// first read; new health reads wait until the configurations are known.
    var installed: Set<HomeKind>?

    /// The rest of today's events from the phone's calendar; nil when Persona may not read it.
    private(set) var events: [HomeSnapshot.Event]?
    /// The days around today from the same read, for the Calendar widget's week and month (`HomeDays`).
    private(set) var days: HomeSnapshot.Days?
    private(set) var health: HomeSnapshot.Health?
    private(set) var intake: HomeSnapshot.Intake?
    private(set) var activity: HomeSnapshot.Activity?
    private var intakeScope: APIClient?
    private var intakeReading = false
    private var intakePending = false
    private var intakeRevision = 0
    private var activityReading = false
    /// Today's tokens by the hour (`GET /api/v1/usage`'s `hourly`, #5721); empty from an older hub.
    private(set) var hourly: [Int] = []

    /// Today's money at the gateways that report it (`HomeUsage.spentToday`), with the day it was read on.
    private var spent: (day: String, usd: Double?)?

    private var readAt: [HomeKind: Date] = [:]
    static let every: [HomeKind: TimeInterval] = [.today: 10 * 60, .health: 15 * 60, .water: 15 * 60,
                                               .healthToday: 15 * 60, .usage: 5 * 60]
    /// The gateways' money: a read asks the hub for every gateway launcher's quota.
    static let spentEvery: TimeInterval = 15 * 60
    private var spentReadAt = Date.distantPast

    /// Today's money, nil when no gateway reports it or it was read on another day.
    func spentToday(_ now: Date) -> Double? { spent?.day == Self.dayKey(now) ? spent?.usd : nil }

    /// Reads what is due. A widget wake forces intake alone; the existing kinds keep their intervals.
    func refresh(force: Bool = false, forceIntake: Bool = false, now: Date = .now) {
        func due(_ kind: HomeKind, also: HomeKind? = nil) -> Bool {
            let shown = installed.map { $0.contains(kind) || also.map($0.contains) == true } ?? true
            return shown && (force || now.timeIntervalSince(readAt[kind] ?? .distantPast) >= Self.every[kind] ?? 600)
        }
        if due(.today, also: .record) {
            readAt[.today] = now
            Task { await readEvents() }
        }
        if due(.health, also: .healthToday) {
            readAt[.health] = now
            Task { await readHealth() }
        }
        if installed?.contains(.healthToday) == true, due(.healthToday) {
            readAt[.healthToday] = now
            Task { await readActivity() }
        }
        if intakeShown, force || forceIntake || now.timeIntervalSince(readAt[.water] ?? .distantPast) >= Self.every[.water]! {
            intakeChanged()
        }
        // Never sooner, even when the hub wakes the app: a Needs You change can wake it every few seconds.
        let usageShown = installed.map { $0.contains(.usage) } ?? true
        if usageShown, now.timeIntervalSince(spentReadAt) >= Self.spentEvery {
            spentReadAt = now
            Task { await readSpent() }
        }
        if due(.usage) {
            readAt[.usage] = now
            Task { await readUsage() }
        }
    }

    private func readEvents() async {
        let read = await Task.detached { Self.calendarEvents(.now) }.value
        guard read?.today != events || read?.days != days else { return }
        events = read?.today
        days = read?.days
        HomeWidgetWriter.shared.schedule()
    }

    /// The events the user has not declined, with a title, over the days the Calendar widget can show
    /// (`HomeDays.range`), in one read: the rest of today (`today`) and every day's (`days`); nil without full access
    /// (the widget then takes the hub's meetings, or says how to allow the calendar). EventKit reads the store: never on
    /// the main actor.
    nonisolated static func calendarEvents(_ now: Date) -> (today: [HomeSnapshot.Event], days: HomeSnapshot.Days)? {
        guard MeetingCalendar.canRead else { return nil }
        let calendar = Calendar.current
        let range = HomeDays.range(around: now, calendar: calendar)
        let store = EKEventStore()
        let found = store.events(matching: store.predicateForEvents(withStart: range.start, end: range.end, calendars: nil))
            .map(MeetingCalendar.Candidate.init)
            .filter { !$0.declined && !$0.title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty }
            .map { HomeSnapshot.Event(title: $0.title, start: $0.start.timeIntervalSince1970, end: $0.end.timeIntervalSince1970, allDay: $0.allDay) }
        return (HomeToday.rest(found, now: now, calendar: calendar), HomeDays.build(found, range: range, calendar: calendar))
    }

    /// Today's steps against the week before, last night's sleep, the resting heart rate and active energy, when the
    /// user let Persona read Health (`HealthAccess`). A query that fails (Health is locked with the phone) keeps the last.
    private func readHealth() async {
        await restore()
        guard HealthAccess.cachedStatus() == .granted else {
            if health != nil { health = nil; HomeWidgetWriter.shared.schedule() }
            return
        }
        let calendar = Calendar.current
        let readingAt = Date.now
        let days = (0..<8).compactMap { calendar.date(byAdding: .day, value: -$0, to: readingAt) }.reversed().map(Self.dayKey)
        guard let steps = try? await HealthTool.daily("steps", days: 8) else { return }
        let sleep = (try? await HealthTool.daily("sleep", days: 8)) ?? [:]
        let heart = (try? await HealthTool.daily("resting_heart_rate", days: 1)) ?? [:]
        let energy = (try? await HealthTool.daily("active_energy", days: 1)) ?? [:]
        let today = days.last ?? ""
        let before = days.dropLast().compactMap { steps[$0] }
        var h = HomeSnapshot.Health()
        h.dayEnds = HomeIntakeClock.nextDay(after: readingAt, zone: intake?.zone ?? "UTC").timeIntervalSince1970
        h.steps = steps[today].map { Int($0) } ?? 0
        h.stepsAverage = before.isEmpty ? nil : Int(before.reduce(0, +) / Double(before.count))
        h.sleepHours = sleep[today] ?? sleep[days.dropLast().last ?? ""]
        h.sleepAverage = HomeHealthAverage.previousWeek(sleep, days: Array(days))
        h.restingHeartRate = heart[today].map { Int($0.rounded()) }
        h.activeEnergy = energy[today].map { Int($0.rounded()) }
        h.week = days.suffix(7).map { Int(steps[$0] ?? 0) }
        guard h != health else { return }
        health = h
        HomeWidgetWriter.shared.schedule()
    }

    private var intakeShown: Bool {
        installed.map { !$0.isDisjoint(with: [.water, .protein, .healthToday]) } ?? false
    }

    /// Local writes, default-manager events and widget wakes bypass the interval. Concurrent invalidations get one
    /// more read, so an answer that started before a write cannot be the last answer shown.
    func intakeChanged(on captured: APIClient? = nil, confirmed: HealthIntake? = nil) {
        guard intakeShown else { return }
        if let captured, !captured.manager(nil).sameSettingsScope(as: healthDeps.api()?.manager(nil)) { return }
        intakeRevision += 1
        if let confirmed, let captured {
            intakeScope = captured.manager(nil)
            intake = HomeIntakeClock.confirmedSnapshot(confirmed, now: healthDeps.now())
            intake?.scope = Self.scope(captured)
            healthDeps.changed()
        }
        readAt[.water] = healthDeps.now()
        if intakeReading { intakePending = true; return }
        intakeReading = true
        Task {
            repeat {
                intakePending = false
                await readIntake()
            } while intakePending
            intakeReading = false
        }
    }

    private func readIntake() async {
        await restore()
        guard let api = healthDeps.api()?.manager(nil) else { return }
        if !api.sameSettingsScope(as: intakeScope) {
            intakeScope = api
            intake = nil
            healthDeps.changed()
        }
        guard healthDeps.supportsIntake() else {
            intake = .init(supported: false)
            healthDeps.changed()
            return
        }
        do {
            let revision = intakeRevision
            let value = try await api.healthIntake()
            guard revision == intakeRevision, api.sameSettingsScope(as: healthDeps.api()?.manager(nil)),
                  healthDeps.supportsIntake() else { return }
            var saved = HomeIntakeClock.snapshot(value, now: healthDeps.now())
            saved.scope = Self.scope(api)
            guard saved != intake else { return }
            let zoneChanged = saved.zone != intake?.zone
            intake = saved
            healthDeps.changed()
            if zoneChanged, installed?.contains(.healthToday) == true {
                readAt[.health] = nil
                readAt[.healthToday] = nil
                refresh()
            }
        } catch {
            // A failed or locked read preserves the last successful snapshot and its original day boundary.
        }
    }

    /// Only an installed Today's Health widget reads Activity, and only after Health was enabled in the app. This
    /// read-only reader never requests authorization; failure (including a locked database) preserves the last value.
    func readActivity() async {
        await restore()
        guard !activityReading else { return }
        guard installed?.contains(.healthToday) == true, healthDeps.healthAllowed() else {
            if activity != nil { activity = nil; healthDeps.changed() }
            return
        }
        activityReading = true
        defer { activityReading = false }
        do {
            let permission = try await healthDeps.activityPermission()
            let readingAt = healthDeps.now()
            let saved: HomeSnapshot.Activity?
            switch permission {
            case .shouldRequest:
                saved = .init(date: readingAt.timeIntervalSince1970, needsAccess: true)
            case .unnecessary:
                saved = try await healthDeps.activity().map { reading in
                    .init(date: reading.date.timeIntervalSince1970,
                          dayEnds: HomeIntakeClock.nextDay(after: readingAt, zone: intake?.zone ?? "UTC").timeIntervalSince1970,
                          moveMode: reading.moveMode == .time ? .time : .energy,
                          move: reading.move, moveGoal: reading.moveGoal, exercise: reading.exercise,
                          exerciseGoal: reading.exerciseGoal, stand: reading.stand, standGoal: reading.standGoal,
                          isPaused: reading.isPaused)
                }
            case .unavailable: saved = nil
            case .unknown: return
            }
            guard installed?.contains(.healthToday) == true, healthDeps.healthAllowed(), saved != activity else { return }
            activity = saved
            healthDeps.changed()
        } catch {
            // Keep the last values; in particular, do not turn a locked Health store into an empty summary.
        }
    }

    /// The day keys `HealthTool.daily` answers with ("yyyy-MM-dd" in the phone's zone).
    static func dayKey(_ date: Date) -> String { ToolDate.day(date) }

    /// The subscription windows (as the Machines page reads them) and today's tokens by the hour.
    private func readUsage() async {
        let model = AppModel.shared
        await UsageWindowsStore.shared.loadIfOld(model)
        if let api = model.api, let hours = try? await api.usageHourly() { hourly = hours }
        HomeWidgetWriter.shared.schedule()
    }

    /// Today's money: every launcher with a gateway, read through the hub (which keeps an answer a minute), the reads at
    /// once. When the list or every read fails, the last stays.
    private func readSpent() async {
        guard let api = AppModel.shared.api, let launchers = try? await api.allLaunchers() else { return }
        let names = HomeUsage.gatewayLaunchers(launchers)
        let answers = await withTaskGroup(of: LauncherQuotaAnswer?.self) { group in
            for name in names { group.addTask { try? await api.launcherQuota(name) } }
            var all: [LauncherQuotaAnswer] = []
            for await answer in group { if let answer { all.append(answer) } }
            return all
        }
        if !names.isEmpty, answers.isEmpty { return }
        let now = Date.now
        let read = (day: Self.dayKey(now), usd: HomeUsage.spentToday(answers))
        guard read.day != spent?.day || read.usd != spent?.usd else { return }
        spent = read
        HomeWidgetWriter.shared.schedule()
    }
}
#endif
