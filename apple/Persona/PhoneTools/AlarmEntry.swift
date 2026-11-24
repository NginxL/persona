import Foundation
#if os(iOS) && canImport(AlarmKit)
import AlarmKit
#endif

/// One alarm or timer Persona set, as Settings › Alarms and Timers draws it (#12805): what AlarmKit reports, the name
/// Persona kept for it (`AlarmsTool.Labels`) and, for a timer, its clock from the Live Activity AlarmKit runs for it. A
/// plain value, so the tests and the page's DEBUG samples build it from made-up data.
nonisolated struct AlarmEntry: Identifiable, Equatable, Sendable {
    /// AlarmKit's state, with the names `phone_alarms_list` gives it (`AlarmsTool.stateName`).
    enum State: String, Sendable { case scheduled, countdown, paused, alerting }

    /// When an alarm rings: one exact moment, or a time of day (the next one, or on these weekdays every week: mon…sun,
    /// Monday first, as `AlarmsTool.AlarmRequest.weekdays`).
    enum When: Equatable, Sendable {
        case at(Date)
        case time(hour: Int, minute: Int, weekdays: [String])
    }

    /// A timer's clock: counting down to `ends`, or paused with `left` to go.
    enum Clock: Equatable, Sendable {
        case running(ends: Date)
        case paused(left: TimeInterval)
    }

    /// How an alarm repeats.
    enum Repeat: Equatable, Sendable {
        case once, everyDay, weekdays, weekends
        /// Other weekdays, Monday first.
        case days([String])
    }

    var id: UUID
    /// The name Persona gave it; nil when it kept none.
    var label: String?
    var state: State
    /// An alarm's schedule; nil for a timer.
    var when: When?
    /// A timer's length.
    var length: TimeInterval?
    /// A timer's clock, when its Live Activity says.
    var clock: Clock?

    var isTimer: Bool { when == nil }

    /// A counting or paused timer whose clock its Live Activity has not given yet (or gave for its other state).
    var awaitsClock: Bool { isTimer && (state == .countdown || state == .paused) && face == nil }

    /// Pause and Resume are a counting or paused timer's.
    var canPause: Bool { isTimer && state == .countdown }
    var canResume: Bool { isTimer && state == .paused }

    var repeats: Repeat {
        guard case .time(_, _, let days)? = when, !days.isEmpty else { return .once }
        let set = Set(days)
        if set.count == AlarmsTool.AlarmRequest.weekdays.count { return .everyDay }
        if set == ["mon", "tue", "wed", "thu", "fri"] { return .weekdays }
        if set == ["sat", "sun"] { return .weekends }
        return .days(AlarmsTool.AlarmRequest.weekdays.filter(set.contains))
    }

    /// Its name, or "Alarm" / "Timer" when Persona kept none (the words the tools use for one set without a label).
    var title: String {
        if let label { return label }
        return isTimer ? String(localized: "Timer [Persona Alarm]") : String(localized: "Alarm [Persona Alarm]")
    }

    var stateText: String {
        switch state {
        case .scheduled: String(localized: "Scheduled [Alarms Page]")
        case .countdown: String(localized: "Counting Down [Alarms Page]")
        case .paused: String(localized: "Paused")
        case .alerting: String(localized: "Ringing [Alarms Page]")
        }
    }

    /// An alarm's time on the phone's clock ("6:40 AM", "06:40"); nil for a timer.
    func timeText(locale: Locale = .current, calendar: Calendar = .current) -> String? {
        switch when {
        case .at(let date):
            return date.formatted(Date.FormatStyle(date: .omitted, time: .shortened, locale: locale, calendar: calendar,
                                                   timeZone: calendar.timeZone))
        case .time(let hour, let minute, _):
            // A day without a clock change, so every time of day exists.
            var utc = Calendar(identifier: .gregorian)
            utc.timeZone = .gmt
            guard let date = utc.date(from: DateComponents(year: 2001, month: 1, day: 1, hour: hour, minute: minute)) else { return nil }
            return date.formatted(Date.FormatStyle(date: .omitted, time: .shortened, locale: locale, calendar: calendar, timeZone: .gmt))
        case nil:
            return nil
        }
    }

    /// The line under its name: how an alarm repeats (after its day, for one exact moment), or a timer's length.
    func detail(locale: Locale = .current, calendar: Calendar = .current) -> String {
        if isTimer {
            guard let length else { return String(localized: "Timer [Persona Alarm]") }
            return String(localized: "\(Self.lengthText(length)) timer [Alarms Page]")
        }
        let repeatText = switch repeats {
        case .once: String(localized: "Once [Alarms Page]")
        case .everyDay: String(localized: "Every Day")
        case .weekdays: String(localized: "Weekdays")
        case .weekends: String(localized: "Weekends")
        case .days(let days): days.map(AlarmsTool.weekdayName).formatted(.list(type: .and, width: .narrow).locale(locale))
        }
        guard case .at(let date)? = when else { return repeatText }
        let day = date.formatted(Date.FormatStyle(locale: locale, calendar: calendar, timeZone: calendar.timeZone)
            .weekday(.abbreviated).month(.abbreviated).day())
        return "\(day) · \(repeatText)"
    }

    /// What a timer's digits show: a live countdown, or a still time left (paused, or 0 while it rings). Nil for an
    /// alarm, and for a timer whose clock its Live Activity did not give.
    enum Face: Equatable, Sendable {
        case counting(ends: Date)
        case still(TimeInterval)
    }

    var face: Face? {
        guard isTimer else { return nil }
        if state == .alerting { return .still(0) }
        switch clock {
        case .running(let ends)?: return state == .countdown ? .counting(ends: ends) : nil
        case .paused(let left)?: return state == .paused ? .still(left) : nil
        case nil: return nil
        }
    }

    /// A still time left as a timer shows it: "12:05", "1:02:03".
    static func clockText(_ seconds: TimeInterval) -> String {
        let whole = max(0, Int(seconds.rounded(.up)))
        return Duration.seconds(whole).formatted(.time(pattern: whole >= 3600 ? .hourMinuteSecond : .minuteSecond))
    }

    /// A timer's length in words: "5 min", "1 hr, 30 min".
    static func lengthText(_ seconds: TimeInterval) -> String {
        Duration.seconds(Int(seconds.rounded())).formatted(.units(allowed: [.hours, .minutes, .seconds], width: .abbreviated))
    }

    // MARK: Order

    /// Timers first, ringing ones at the top, then by what is left; alarms after, ringing first, then by time of day as the
    /// Clock app lists them.
    static func timers(_ entries: [AlarmEntry], now: Date = Date()) -> [AlarmEntry] {
        entries.filter(\.isTimer).sorted { a, b in
            let ka = a.timerKey(now), kb = b.timerKey(now)
            return ka != kb ? ka.lexicographicallyPrecedes(kb) : a.tieBreak(b)
        }
    }

    static func alarms(_ entries: [AlarmEntry], calendar: Calendar = .current) -> [AlarmEntry] {
        entries.filter { !$0.isTimer }.sorted { a, b in
            let ka = a.alarmKey(calendar), kb = b.alarmKey(calendar)
            return ka != kb ? ka.lexicographicallyPrecedes(kb) : a.tieBreak(b)
        }
    }

    /// Ringing (0), counting or paused (1, by the time left), clock unknown (2, by length).
    private func timerKey(_ now: Date) -> [Double] {
        if state == .alerting { return [0, 0] }
        switch face {
        case .counting(let ends)?: return [1, ends.timeIntervalSince(now)]
        case .still(let left)?: return [1, left]
        case nil: return [2, length ?? 0]
        }
    }

    /// Ringing (0), then by minute of the day; a one-off moment also by its date.
    private func alarmKey(_ calendar: Calendar) -> [Double] {
        let ringing: Double = state == .alerting ? 0 : 1
        switch when {
        case .time(let hour, let minute, _)?: return [ringing, Double(hour * 60 + minute), 0]
        case .at(let date)?:
            let parts = calendar.dateComponents([.hour, .minute], from: date)
            return [ringing, Double((parts.hour ?? 0) * 60 + (parts.minute ?? 0)), date.timeIntervalSinceReferenceDate]
        case nil: return [ringing, 0, 0]
        }
    }

    private func tieBreak(_ other: AlarmEntry) -> Bool {
        let order = title.localizedStandardCompare(other.title)
        return order != .orderedSame ? order == .orderedAscending : id.uuidString < other.id.uuidString
    }
}

// MARK: - From AlarmKit

#if os(iOS) && canImport(AlarmKit)
@available(iOS 26.0, *)
extension AlarmEntry {
    /// What AlarmKit reports for one alarm, the name Persona kept for it and, for a timer, its Live Activity's state (a
    /// clock that disagrees with the alarm's state, an activity a step behind, is kept but not drawn: `face`).
    init(id: UUID, state: Alarm.State, schedule: Alarm.Schedule?, countdown: Alarm.CountdownDuration?, label: String?,
         activity: AlarmPresentationState.Mode?) {
        self.id = id
        self.label = label
        self.state = State(rawValue: AlarmsTool.stateName(state)) ?? .scheduled
        when = schedule.map(Self.when)
        length = countdown?.preAlert
        switch activity {
        case .countdown(let c)?: clock = .running(ends: c.fireDate)
        case .paused(let p)?: clock = .paused(left: max(0, p.totalCountdownDuration - p.previouslyElapsedDuration))
        default: clock = nil
        }
    }

    init(_ alarm: Alarm, label: String?, activity: AlarmPresentationState.Mode?) {
        self.init(id: alarm.id, state: alarm.state, schedule: alarm.schedule, countdown: alarm.countdownDuration, label: label,
                  activity: activity)
    }

    /// Every alarm with its name and its Live Activity's state.
    static func entries(_ alarms: [Alarm], labels: [String: String], activities: [UUID: AlarmPresentationState.Mode]) -> [AlarmEntry] {
        alarms.map { AlarmEntry($0, label: labels[$0.id.uuidString], activity: activities[$0.id]) }
    }

    /// AlarmKit's schedule as the page reads it; weekdays named as the tools name them, Monday first.
    static func when(_ schedule: Alarm.Schedule) -> When {
        switch schedule {
        case .fixed(let date):
            return .at(date)
        case .relative(let relative):
            var days: [String] = []
            if case .weekly(let weekdays) = relative.repeats {
                days = AlarmsTool.AlarmRequest.weekdays.filter { day in AlarmsTool.localeWeekday(day).map(weekdays.contains) ?? false }
            }
            return .time(hour: relative.time.hour, minute: relative.time.minute, weekdays: days)
        @unknown default:
            return .time(hour: 0, minute: 0, weekdays: [])
        }
    }
}
#endif
