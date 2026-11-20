import Foundation
import PersonaWire

/// How the To-dos words are resolved: in the app's language through the bundle's cached tables (the app passes no
/// locale, docs/apple/performance.md P4), or in a given language (tests check all three).
nonisolated struct TodoWords: Sendable {
    var locale: Locale?

    func callAsFunction(_ value: String.LocalizationValue) -> String {
        guard let locale else { return String(localized: value) }
        var resource = LocalizedStringResource(value)
        resource.locale = locale
        return String(localized: resource)
    }

    /// The locale dates and times are written in.
    var formatting: Locale { locale ?? .interface }

    /// Between the parts of a spoken label, in the language.
    var listSeparator: String {
        var resource = LocalizedStringResource(", ", comment: "Separator between parts of an accessibility label")
        if let locale { resource.locale = locale }
        return String(localized: resource)
    }
}

/// A row's second line in words (#10465; docs/design/proposals/todos-redesign.md 3.4): the hub sends a key and string
/// arguments (`TodoLine`, spec/fixtures/todo-view/README.md "Line keys"), and the app says it in its language. Absolute
/// times become "since Tue" here, in the list's model, never in a view's body. A key this build does not know is not
/// said: the row says its section's word instead (`TodoRowContentLogic`). Pure.
nonisolated enum TodoLineText {
    /// What the line's lead is drawn in; the rest is always the secondary color.
    enum Tone: Equatable, Sendable {
        /// The ask's own words, in the needs-you text tone.
        case needs
        /// A failed start: both parts in danger text ("Didn't start", the shipped word for it in a line's sentence case).
        case danger
        /// What finished, in the unread green.
        case ready
        case plain
    }

    struct Said: Equatable, Sendable {
        /// Drawn in the tone: what asks, what failed, what finished, or who works on it.
        var lead: String?
        /// After a separator, in the secondary color.
        var rest: String?
        var tone: Tone = .plain

        /// One string, for the row's spoken label.
        func spoken(separator: String) -> String { [lead, rest].compactMap { $0 }.joined(separator: separator) }
    }

    /// Every key this build says: the README's table (`TodoLineTextTests` keeps the two the same).
    static let keys: Set<String> = [
        "failed_start", "agent_asks", "interrupted", "decision", "decision_default", "cannot", "awaits_reply", "working",
        "persona_working", "starting", "background", "parked", "finished", "accepted", "deployed", "pr_open", "usage_limit",
        "run_waits", "finished_seen", "completed", "canceled", "reported", "superseded", "someday",
    ]

    /// The line in words; nil for no line, or a key this build does not know. `since`: when the row came to where it is
    /// (the view's `since`), said on the lines of rows that wait. `closedReason`: the to-do's own words on how it ended.
    static func said(_ line: TodoLine?, since: Date? = nil, closedReason: String? = nil, now: Date, words: TodoWords = TodoWords(),
                     calendar: Calendar = .current) -> Said? {
        guard let line, keys.contains(line.key) else { return nil }
        let args = line.args ?? [:]
        func arg(_ name: String) -> String? { args[name].flatMap { $0.isEmpty ? nil : $0 } }
        func time(_ name: String) -> Date? { arg(name).flatMap(JSON.parseDate) }
        let day = { (date: Date) in TodoTimeWords.day(date, now: now, words: words, calendar: calendar) }
        let sinceRest = since.map { words("since \(day($0)) [To-dos Line]") }
        let check = words("check and tick [To-dos Line]")
        let run = arg("run")
        var said: Said
        switch line.key {
        case "failed_start":
            said = Said(lead: words("Didn't start [To-dos Line]"), rest: arg("error"), tone: .danger)
        case "agent_asks":
            let who = run ?? words("An Agent [To-dos Line]")
            if let text = arg("text") {
                said = Said(lead: words("\(who): \(text) [To-dos Line]"), tone: .needs)
            } else {
                said = Said(lead: words("\(who) needs you [To-dos Line]"), tone: .needs)
            }
        case "interrupted":
            said = Said(lead: words("\(run ?? words("An Agent [To-dos Line]")) stopped before it finished [To-dos Line]"), tone: .needs)
        case "decision":
            said = Said(lead: arg("text") ?? words("A decision for you [To-dos Line]"),
                        rest: arg("recommend").map { words("\($0) recommended [To-dos Line]") }, tone: .needs)
        case "decision_default":
            // "on Fri", "on Apr 3", or "at 10:00" today.
            let at = time("default_at")
            let today = at.map { calendar.isDate($0, inSameDayAs: now) } ?? false
            let when = at.map(day)
            let rest: String? = switch (arg("recommend"), when) {
            case let (pick?, when?) where today: words("Persona picks \(pick) at \(when) unless you answer [To-dos Line]")
            case let (pick?, when?): words("Persona picks \(pick) on \(when) unless you answer [To-dos Line]")
            case let (nil, when?) where today: words("Persona decides at \(when) unless you answer [To-dos Line]")
            case let (nil, when?): words("Persona decides on \(when) unless you answer [To-dos Line]")
            case let (pick?, nil): words("Persona picks \(pick) unless you answer [To-dos Line]")
            case (nil, nil): nil
            }
            said = Said(lead: arg("text"), rest: rest)
        case "cannot":
            said = Said(lead: words("Persona says it can't be done as asked [To-dos Line]"), tone: .needs)
        case "awaits_reply":
            said = Said(lead: StateName.awaitsReply.text,
                        rest: arg("text") ?? run.map { words("\($0) is waiting for your reply [To-dos Line]") })
        case "working":
            let who = run ?? words("An Agent [To-dos Line]")
            said = arg("text").map { Said(lead: who, rest: $0) } ?? Said(lead: words("\(who) is working [To-dos Line]"))
        case "persona_working":
            said = arg("text").map { Said(lead: "Persona", rest: $0) } ?? Said(lead: words("Persona is working [To-dos Line]"))
        case "starting":
            said = Said(lead: run.map { words("Starting \($0)… [To-dos Line]") } ?? words("Starting… [To-dos Line]"))
        case "background":
            said = Said(lead: words("\(run ?? words("An Agent [To-dos Line]")) is working in the background [To-dos Line]"))
        case "parked":
            let until = time("until").map { words("until \(day($0)) [To-dos Line]") }
            said = Said(lead: arg("reason") ?? words("Put aside [To-dos Line]"), rest: until ?? sinceRest)
        case "finished":
            said = Said(lead: run.map { words("\($0) finished [To-dos Line]") } ?? words("Finished [To-dos Line]"), rest: check,
                        tone: .ready)
        case "accepted":
            said = Said(lead: words("Accepted [To-dos Line]"), rest: check, tone: .ready)
        case "deployed":
            said = Said(lead: words("Live [To-dos Line]"), rest: check, tone: .ready)
        case "pr_open":
            said = Said(lead: words("Waiting for a merge [To-dos Line]"), rest: sinceRest)
        case "usage_limit":
            let lead = run.map { words("\($0) reached its usage limit [To-dos Line]") } ?? words("Usage limit reached [To-dos Line]")
            said = Said(lead: lead, rest: time("resets_at").map { words("back \(day($0)) [To-dos Line]") })
        case "run_waits":
            let who = run ?? words("An Agent [To-dos Line]")
            let lead = arg("on") == "agent" ? words("\(who) is waiting for another Agent [To-dos Line]")
                : words("\(who) is waiting for something outside [To-dos Line]")
            said = Said(lead: lead, rest: sinceRest)
        case "finished_seen":
            said = Said(lead: time("at").map { words("Finished \(day($0)) [To-dos Line]") } ?? words("Finished [To-dos Line]"))
        case "completed":
            let lead: String = switch arg("by") {
            case "auto": words("Done by Persona [To-dos Line]")
            case "review": words("Done after review [To-dos Line]")
            case nil: words("You marked it done [To-dos Line]")
            default: StateName.done.text
            }
            said = Said(lead: lead, rest: closedReason)
        case "canceled":
            said = Said(lead: words("Canceled [To-dos Line]"), rest: arg("reason"))
        case "reported":
            said = Said(lead: words("Reported back [To-dos Line]"))
        case "superseded":
            said = Said(lead: words("Finished in a related to-do [To-dos Line]"))
        case "someday":
            said = Said(lead: StateName.todoSomeday, rest: time("until").map { words("from \(day($0)) [To-dos Line]") })
        default:
            return nil
        }
        // A to-do that carries one of its steps says which: "Ryokan · Which of 3?"; not twice when the line names it
        // already (a step's run is often called as the step is).
        if let step = arg("step"), !(said.lead?.hasPrefix(step) ?? false) {
            said.lead = said.lead.map { step + TodoTimeWords.separator + $0 } ?? step
        }
        return said
    }
}

/// Times as a row says them, from now (#10465): today's by the clock, the week's by its day, the rest by date.
nonisolated enum TodoTimeWords {
    /// Between the parts of a line: "Which ryokan? · Ryokan Hanami recommended".
    static let separator = " · "

    /// "10:00" today; "Tue" within six days either way; "Apr 3" further.
    static func day(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        if calendar.isDate(date, inSameDayAs: now) { return date.formatted(style.hour().minute()) }
        let days = abs(calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99)
        style = days <= 6 ? style.weekday(.abbreviated) : style.month(.abbreviated).day()
        return date.formatted(style)
    }

    /// A due day: "Today", "Thu" within the week, "Apr 3" further, and its year when not this one.
    static func due(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return words("Today [To-dos Due]") }
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        let days = calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99
        if (1...6).contains(days) { return date.formatted(style.weekday(.abbreviated)) }
        style = style.month(.abbreviated).day()
        if calendar.component(.year, from: date) != calendar.component(.year, from: now), abs(days) > 180 { style = style.year() }
        return date.formatted(style)
    }

    /// How long ago, as short as a row's end has room for: "now", "16m", "3h", then the day.
    static func age(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 60 { return words("now [To-dos Age]") }
        if seconds < 3600 { return words("\(Int(seconds / 60))m [To-dos Age]") }
        if seconds < 86_400 { return words("\(Int(seconds / 3600))h [To-dos Age]") }
        return day(date, now: now, words: words, calendar: calendar)
    }

    // Said in full for VoiceOver, where "16m" reads as a length and "Apr 4" does not say what it is.

    /// "10:00" today; "Tuesday" within six days either way; "April 3" further.
    static func spokenDay(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        if calendar.isDate(date, inSameDayAs: now) { return date.formatted(style.hour().minute()) }
        let days = abs(calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99)
        style = days <= 6 ? style.weekday(.wide) : style.month(.wide).day()
        return date.formatted(style)
    }

    /// "Due today", "Due Saturday", "Due April 3".
    static func spokenDue(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        if calendar.isDate(date, inSameDayAs: now) { return words("Due today [To-dos Spoken]") }
        var style = Date.FormatStyle(calendar: calendar, timeZone: calendar.timeZone).locale(words.formatting)
        let days = calendar.dateComponents([.day], from: calendar.startOfDay(for: now), to: calendar.startOfDay(for: date)).day ?? 99
        style = (1...6).contains(days) ? style.weekday(.wide) : style.month(.wide).day()
        return words("Due \(date.formatted(style)) [To-dos Spoken]")
    }

    /// "Just now", "16 minutes ago", "3 hours ago", then "Since Tuesday".
    static func spokenAge(_ date: Date, now: Date, words: TodoWords, calendar: Calendar) -> String {
        let seconds = now.timeIntervalSince(date)
        if seconds < 60 { return words("Just now [To-dos Spoken]") }
        if seconds < 3600 { return words("\(Int(seconds / 60)) minutes ago [To-dos Spoken]") }
        if seconds < 86_400 { return words("\(Int(seconds / 3600)) hours ago [To-dos Spoken]") }
        return words("Since \(spokenDay(date, now: now, words: words, calendar: calendar)) [To-dos Spoken]")
    }
}
