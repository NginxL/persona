#if os(iOS)
import Foundation

/// Four independent positions in Edit Widget. Repeated choices deliberately keep their positions.
nonisolated enum HomeHealthItem: String, CaseIterable, Codable, Hashable, Sendable {
    case water, protein, activity, steps, sleep
    static let defaults: [Self] = [.water, .protein, .activity, .sleep]
}

nonisolated enum HomeHealthAverage {
    /// The previous seven day keys, excluding today and days the store did not return.
    static func previousWeek(_ values: [String: Double], days: [String]) -> Double? {
        let known = days.suffix(8).dropLast().compactMap { values[$0] }
        return known.isEmpty ? nil : known.reduce(0, +) / Double(known.count)
    }
}

/// Gregorian day buckets in the hub's zone, including UTC and daylight-saving transitions.
nonisolated enum HomeIntakeClock {
    static func calendar(_ zone: String) -> Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: zone) ?? TimeZone(secondsFromGMT: 0)!
        return calendar
    }

    static func nextDay(after date: Date, zone: String) -> Date {
        HomeDays.nextMidnight(after: date, calendar: calendar(zone))
    }

    static func key(_ date: Date, zone: String) -> String { HomeDays.key(date, calendar: calendar(zone)) }

    #if !WIDGET_EXTENSION
    /// The response's last day is authoritative, even when the phone's date differs from the hub's.
    static func snapshot(_ value: HealthIntake, now: Date) -> HomeSnapshot.Intake {
        let calendar = calendar(value.zone)
        let day = value.days.last?.date ?? key(now, zone: value.zone)
        let start = HomeDays.date(day, calendar: calendar) ?? calendar.startOfDay(for: now)
        return .init(supported: true, day: day, zone: value.zone,
                     nextDay: HomeDays.nextMidnight(after: start, calendar: calendar).timeIntervalSince1970,
                     water: .init(today: value.water.today, goal: Double(value.water.goal)),
                     protein: .init(today: value.protein.today, goal: Double(value.protein.goal)),
                     days: value.days.suffix(7).map { .init(date: $0.date, water: $0.water, protein: $0.protein) })
    }

    /// POST/DELETE totals are authoritative immediately; their page's seven-day list may still be from before
    /// midnight. Roll that list forward first, preserving yesterday's bar, then replace only today's bucket.
    static func confirmedSnapshot(_ value: HealthIntake, now: Date) -> HomeSnapshot.Intake {
        let base = snapshot(value, now: now)
        var saved = current(base, at: now) ?? base
        saved.water.today = value.water.today
        saved.protein.today = value.protein.today
        let today = HomeSnapshot.IntakeDay(date: saved.day, water: value.water.today, protein: value.protein.today)
        if let index = saved.days.firstIndex(where: { $0.date == saved.day }) { saved.days[index] = today }
        else { saved.days = Array((saved.days + [today]).suffix(7)) }
        return saved
    }
    #endif

    /// Keep historical bars and goals, but never call yesterday's amounts today's after the stored boundary.
    static func current(_ saved: HomeSnapshot.Intake?, at date: Date) -> HomeSnapshot.Intake? {
        guard var value = saved else { return nil }
        guard value.nextDay > 0, date.timeIntervalSince1970 >= value.nextDay else { return value }
        let calendar = calendar(value.zone)
        let today = calendar.startOfDay(for: date)
        let known = Dictionary(value.days.map { ($0.date, $0) }, uniquingKeysWith: { _, b in b })
        value.days = (-6...0).compactMap { offset in
            guard let day = calendar.date(byAdding: .day, value: offset, to: today) else { return nil }
            let key = HomeDays.key(day, calendar: calendar)
            return known[key] ?? .init(date: key, water: 0, protein: 0)
        }
        value.day = HomeDays.key(today, calendar: calendar)
        value.water.today = 0
        value.protein.today = 0
        value.nextDay = nextDay(after: date, zone: value.zone).timeIntervalSince1970
        return value
    }

    static func boundary(_ snapshot: HomeSnapshot, after date: Date) -> Date? {
        guard let intake = snapshot.intake, intake.supported, intake.nextDay > 0 else { return nil }
        let stored = Date(timeIntervalSince1970: intake.nextDay)
        return stored > date ? stored : nextDay(after: date, zone: intake.zone)
    }
}

nonisolated extension HomeSnapshot.Activity {
    enum RowKind: String, Sendable { case move, exercise, stand }
    struct Row: Identifiable, Sendable {
        var id: RowKind
        var value: Double
        var goal: Double
        var fraction: Double { goal > 0 ? max(0, min(1, value / goal)) : 0 }
    }

    /// A zero goal is still a row; only an absent goal removes Exercise or Stand.
    var rows: [Row] {
        guard !needsAccess else { return [] }
        var rows = [Row(id: .move, value: move, goal: moveGoal)]
        if let goal = exerciseGoal { rows.append(Row(id: .exercise, value: exercise, goal: goal)) }
        if let goal = standGoal { rows.append(Row(id: .stand, value: stand, goal: goal)) }
        return rows
    }
}

extension HomeContext {
    var intake: HomeSnapshot.Intake? { HomeIntakeClock.current(snapshot.intake, at: date) }
    var healthDayRolled: Bool {
        let boundary = snapshot.health?.dayEnds ?? snapshot.intake?.nextDay ?? 0
        return boundary > 0 && now >= boundary
    }
    var activity: HomeSnapshot.Activity? {
        guard var value = snapshot.activity else { return nil }
        let boundary = value.dayEnds ?? snapshot.intake?.nextDay ?? 0
        if boundary > 0 && now >= boundary {
            value.move = 0; value.exercise = 0; value.stand = 0; value.isPaused = false
        }
        return value
    }
}

/// Shared with the provider so a test can exercise the exact extra timeline dates, without App Group IO.
enum HomeHealthTimeline {
    static func dates(_ kind: HomeKind, snapshot: HomeSnapshot, after now: Date) -> [Date] {
        guard [.water, .protein, .healthToday].contains(kind),
              let boundary = HomeIntakeClock.boundary(snapshot, after: now) else { return [] }
        return [boundary]
    }
}
#endif
