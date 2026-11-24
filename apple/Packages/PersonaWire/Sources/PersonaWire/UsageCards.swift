import Foundation

// Usage cards by account type (#13173): the subscription accounts grouped by provider, each group's accounts ordered by
// their 7-day reset, and the reset timeline of one account. Pure, like UsageWindows.swift (the web's lib/usage-windows.ts
// and the Android usage rules carry the same rule); the views are in UsageWindowsViews.swift.

/// The accounts of one provider (account type): one card, its accounts in the order `UsageWindows.groups` gives.
public nonisolated struct UsageProviderGroup: Hashable, Sendable, Identifiable {
    public var provider: String
    public var accounts: [UsageAccount]
    /// The newest sample of the group, for the card's "updated" line.
    public var sampledAt: Date?

    public var id: String { provider }

    public init(provider: String, accounts: [UsageAccount], sampledAt: Date?) {
        self.provider = provider
        self.accounts = accounts
        self.sampledAt = sampledAt
    }
}

/// One end of a bar on the reset timeline: where it ends along the shared axis (0...1) and how long is left.
public nonisolated struct UsageTimelineCap: Hashable, Sendable {
    public var position: Double
    public var seconds: TimeInterval
    public var resetsAt: Date
}

/// One account's row of the card's reset timeline: the plan's 5h and 7d windows, each running from now to its reset.
public nonisolated struct UsageTimelineRow: Hashable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var plan: String
    public var fiveHour: UsageTimelineCap?
    public var week: UsageTimelineCap?
}

extension UsageWindows {
    /// The longest stretch a timeline shows; a window that resets later is cut at the end of the strip.
    public static let timelineMaxSpan: TimeInterval = 7 * 24 * 3600
    public static let fiveHourMinutes = 300
    public static let weekMinutes = 10080

    /// The provider's name as a card title: the known ones in their own spelling, any other capitalised.
    public static func providerTitle(_ provider: String) -> String {
        let p = provider.trimmingCharacters(in: .whitespaces)
        switch p.lowercased() {
        case "claude": return "Claude"
        case "codex": return "Codex"
        case "xai": return "xAI"
        case "": return String(localized: "usage.provider.other", defaultValue: "Other", comment: "Card title for usage accounts whose provider is not named")
        default: return p.prefix(1).uppercased() + p.dropFirst()
        }
    }

    /// The plan's 7-day window reset time, the key accounts are ordered by; nil without one or without a reset time.
    static func weekReset(_ a: UsageAccount) -> Date? {
        a.windows.first { $0.model == nil && minutes($0) == weekMinutes }?.resetsAt
    }

    /// The accounts of one card: soonest 7-day reset first; an account without a 7-day window or reset time last, then by name.
    public static func sortedByWeekReset(_ accounts: [UsageAccount]) -> [UsageAccount] {
        accounts.sorted { l, r in
            switch (weekReset(l), weekReset(r)) {
            case let (a?, b?) where a != b: return a < b
            case (.some, .none): return true
            case (.none, .some): return false
            default: break
            }
            let order = l.name.localizedCaseInsensitiveCompare(r.name)
            return order != .orderedSame ? order == .orderedAscending : l.id < r.id
        }
    }

    /// An account's windows as its card lists them: the plan's 5h, then its 7d, then every other window as given.
    public static func cardWindows(_ a: UsageAccount) -> [UsageWindow] {
        func rank(_ w: UsageWindow) -> Int {
            guard w.model == nil else { return 2 }
            switch minutes(w) {
            case fiveHourMinutes: return 0
            case weekMinutes: return 1
            default: return 2
            }
        }
        return a.windows.enumerated().sorted { l, r in
            let (x, y) = (rank(l.element), rank(r.element))
            return x != y ? x < y : l.offset < r.offset
        }.map(\.element)
    }

    /// The cards: accounts grouped by provider in the order the providers first appear, each group's accounts ordered by
    /// `sortedByWeekReset`; providers in `hidden` (lower case) are left out.
    public static func groups(_ accounts: [UsageAccount], hidden: Set<String> = []) -> [UsageProviderGroup] {
        var order: [String] = []
        var byProvider: [String: [UsageAccount]] = [:]
        for a in accounts {
            let p = a.provider.lowercased()
            guard !hidden.contains(p) else { continue }
            if byProvider[p] == nil { order.append(p) }
            byProvider[p, default: []].append(a)
        }
        return order.map { p in
            let list = byProvider[p] ?? []
            return UsageProviderGroup(provider: p, accounts: sortedByWeekReset(list), sampledAt: list.compactMap(\.sampledAt).max())
        }
    }

    /// Every provider in `accounts`, for the switches (hidden ones included), in the order they first appear.
    public static func providers(_ accounts: [UsageAccount]) -> [String] {
        var seen = Set<String>()
        return accounts.map { $0.provider.lowercased() }.filter { seen.insert($0).inserted }
    }

    /// How long is left, short, for the timeline's bar ends: "45m", "1.5h", "2d 2h" (whole days without the hours).
    public static func shortDuration(_ secs: TimeInterval) -> String {
        let mins = Int((max(0, secs) / 60).rounded(.up))
        if mins < 60 { return String(localized: "usage.duration.minutes", defaultValue: "\(mins)m", comment: "Time left on the usage timeline: minutes") }
        if mins < 24 * 60 {
            let hours = (Double(mins) / 60 * 10).rounded() / 10
            let text = hours == hours.rounded() ? String(Int(hours)) : String(hours)
            return String(localized: "usage.duration.hours", defaultValue: "\(text)h", comment: "Time left on the usage timeline: hours, one decimal at most")
        }
        let (d, h) = (mins / (24 * 60), (mins / 60) % 24)
        if h == 0 { return String(localized: "usage.duration.days", defaultValue: "\(d)d", comment: "Time left on the usage timeline: whole days") }
        return String(localized: "usage.duration.daysHours", defaultValue: "\(d)d \(h)h", comment: "Time left on the usage timeline: days and hours")
    }

    /// The shared axis' ticks, in hours from now.
    public static let timelineTickHours: [Double] = [0, 6, 12, 24, 72, 120, 168]
    /// The first day takes this much of the axis, the days 1 to 7 the rest (the owner-approved design, #13173).
    public static let timelineFirstDayShare = 0.45

    /// Where `hours` from now falls along the axis, 0...1: the first 24 hours over 45%, hours 24 to 168 over the rest.
    public static func timelinePosition(hours: Double) -> Double {
        let h = max(0, hours)
        if h <= 24 { return h / 24 * timelineFirstDayShare }
        return timelineFirstDayShare + min(h - 24, 144) / 144 * (1 - timelineFirstDayShare)
    }

    /// The card's reset timeline rows, in the order of `accounts` (the quota list above it): an account's plan 5h and 7d
    /// windows that still have a reset ahead; an account with neither has no row. Other windows are not drawn here.
    public static func timelineRows(_ accounts: [UsageAccount], now: Date = .now) -> [UsageTimelineRow] {
        func cap(_ a: UsageAccount, _ minutesWanted: Int) -> UsageTimelineCap? {
            guard let at = a.windows.first(where: { $0.model == nil && minutes($0) == minutesWanted })?.resetsAt, at > now else { return nil }
            let secs = at.timeIntervalSince(now)
            return UsageTimelineCap(position: timelinePosition(hours: secs / 3600), seconds: secs, resetsAt: at)
        }
        return accounts.compactMap { a in
            let (five, week) = (cap(a, fiveHourMinutes), cap(a, weekMinutes))
            guard five != nil || week != nil else { return nil }
            return UsageTimelineRow(id: a.id, name: a.name, plan: planNote(a), fiveHour: five, week: week)
        }
    }
}
