import Foundation

// Subscription rate-limit windows (#2432, #2595, docs/server/usage-windows.md): GET /api/v1/usage/windows lists the accounts the
// machines' agents are signed in to, and those an account-pool gateway keeps, with how much of each window (5h, 7d …)
// is used. The types and the pure helpers (pace, countdown, sample age, which accounts the bars show) are here, as the
// web's lib/usage-windows.ts has them; the views are UsageWindowsViews.swift.

public nonisolated struct UsageWindow: Decodable, Hashable, Sendable {
    public var label: String
    public var windowMinutes: Int
    public var usedPercent: Double
    public var resetsAt: Date?
    /// An allowance a provider keeps for one model next to the plan's own (#2595); nil for the plan's window.
    public var model: String?
    /// The protection line of this window (0...100, #13173): once use reaches it the account is no longer given work.
    /// Per account and per window; nil when the hub does not say (an older hub).
    public var protectPercent: Double?

    public enum CodingKeys: String, CodingKey {
        case label, model
        case windowMinutes = "window_minutes", usedPercent = "used_percent", resetsAt = "resets_at"
        case protectPercent = "protect_percent"
    }

    public init(label: String, windowMinutes: Int, usedPercent: Double, resetsAt: Date?, model: String? = nil, protectPercent: Double? = nil) {
        self.protectPercent = protectPercent
        self.label = label
        self.windowMinutes = windowMinutes
        self.usedPercent = usedPercent
        self.resetsAt = resetsAt
        self.model = model
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        label = try c.decodeIfPresent(String.self, forKey: .label) ?? ""
        windowMinutes = try c.decodeIfPresent(Int.self, forKey: .windowMinutes) ?? 0
        usedPercent = try c.decodeIfPresent(Double.self, forKey: .usedPercent) ?? 0
        resetsAt = (try? c.decodeIfPresent(String.self, forKey: .resetsAt)).flatMap { $0.flatMap(JSON.parseDate) }
        let m = try c.decodeIfPresent(String.self, forKey: .model)?.trimmingCharacters(in: .whitespaces)
        model = m?.isEmpty == false ? m : nil
        protectPercent = (try? c.decodeIfPresent(Double.self, forKey: .protectPercent)).flatMap { $0 }.flatMap { (0...100).contains($0) ? $0 : nil }
    }

    /// Unique within an account (the same length may come twice: the plan's and one model's).
    public var key: String { "\(label):\(windowMinutes):\(model ?? "")" }
}

public nonisolated struct UsageAccount: Decodable, Hashable, Sendable, Identifiable {
    public var id: String
    public var provider: String
    public var name: String
    public var plan: String?
    public var windows: [UsageWindow]
    public var sampledAt: Date?
    public var stale: Bool
    /// When it turns stale if nothing newer comes (#1905): kept numbers are judged again against it (`live`).
    public var staleAt: Date?
    /// Where the numbers came from (#2595): an agent's files on one of the computers, and/or gateways by name.
    public var local: Bool
    public var sources: [String]
    /// The computers (by name) that read it (#1907); an older hub only says `local`.
    public var machines: [String]
    /// Why a gateway's numbers are old: "error" (it could not read this account just now) or, when its own last read
    /// failed, "rejected" (the key was turned away), "unreachable" or "failed" (docs/server/usage-windows.md).
    public var problem: String?

    public enum CodingKeys: String, CodingKey {
        case id, provider, name, plan, windows, stale, local, sources, machines, problem
        case sampledAt = "sampled_at"
        case staleAt = "stale_at"
    }

    public init(id: String, provider: String, name: String, plan: String? = nil, windows: [UsageWindow], sampledAt: Date?,
         stale: Bool = false, staleAt: Date? = nil, local: Bool = false, sources: [String] = [], machines: [String] = [], problem: String? = nil) {
        self.id = id
        self.provider = provider
        self.name = name
        self.plan = plan
        self.windows = windows
        self.sampledAt = sampledAt
        self.stale = stale
        self.staleAt = staleAt
        self.local = local
        self.sources = sources
        self.machines = machines
        self.problem = problem
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
        provider = try c.decodeIfPresent(String.self, forKey: .provider) ?? ""
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        plan = try c.decodeIfPresent(String.self, forKey: .plan)
        windows = try c.decodeIfPresent([UsageWindow].self, forKey: .windows) ?? []
        sampledAt = (try? c.decodeIfPresent(String.self, forKey: .sampledAt)).flatMap { $0.flatMap(JSON.parseDate) }
        stale = try c.decodeIfPresent(Bool.self, forKey: .stale) ?? false
        staleAt = (try? c.decodeIfPresent(String.self, forKey: .staleAt)).flatMap { $0.flatMap(JSON.parseDate) }
        local = try c.decodeIfPresent(Bool.self, forKey: .local) ?? false
        sources = try c.decodeIfPresent([String].self, forKey: .sources) ?? []
        machines = try c.decodeIfPresent([String].self, forKey: .machines) ?? []
        problem = try c.decodeIfPresent(String.self, forKey: .problem)
    }
}

/// The pure part of the subscription windows: what to show and how it reads (the web's lib/usage-windows.ts).
public nonisolated enum UsageWindows {
    /// Samples older than this are dropped (a source that went quiet for days says nothing about now).
    public static let sampleMaxAge: TimeInterval = 3 * 24 * 3600
    /// From this used percentage a bar turns to the caution colour.
    public static let warnPercent: Double = 80
    /// The pace is not judged in the first part of a window: a few minutes in, any use is "ahead".
    public static let paceQuietFraction: Double = 0.05
    /// How far (percentage points) the actual use may be from the expected before it is over / under.
    public static let paceTolerance: Double = 5
    /// How often the bars ask the hub again while they are on screen.
    public static let pollInterval: Duration = .seconds(60)

    /// The window's length in minutes from its label ("5h", "7d", "45m"); the given minutes when the label is not one.
    public static func minutes(_ w: UsageWindow) -> Int {
        let s = w.label.trimmingCharacters(in: .whitespaces).lowercased()
        if let unit = s.last, let mult = ["m": 1.0, "h": 60, "d": 1440, "w": 10080][String(unit)],
           let n = Double(s.dropLast().trimmingCharacters(in: .whitespaces)), n > 0 {
            return Int((n * mult).rounded())
        }
        return max(0, w.windowMinutes)
    }

    public enum PaceState: Equatable, Sendable { case over, under, on }

    public struct Pace: Equatable, Sendable {
        /// Where use would be if it were spread evenly over the window, 0-100.
        public var expected: Double
        public var state: PaceState

        public init(expected: Double, state: PaceState) {
            self.expected = expected
            self.state = state
        }
    }

    /// How use compares with an even spread: expected% = elapsed / length; over when use is more than 5 points above it,
    /// under when more than 5 below. nil with no reset time or length, and in the first 5% of the window.
    public static func pace(_ w: UsageWindow, now: Date = .now) -> Pace? {
        let total = Double(minutes(w)) * 60
        guard total > 0, let resets = w.resetsAt else { return nil }
        let elapsed = total - resets.timeIntervalSince(now)
        guard elapsed >= 0, elapsed <= total, elapsed / total >= paceQuietFraction else { return nil }
        let expected = elapsed / total * 100
        let diff = w.usedPercent - expected
        return Pace(expected: expected, state: diff > paceTolerance ? .over : diff < -paceTolerance ? .under : .on)
    }

    /// The pace of one of an account's windows, as the cards show it. An out-of-date reading says nothing about how fast it
    /// is being used now, so a stale account has none (#12411): its numbers and reset stay, faded and dated.
    public static func pace(_ w: UsageWindow, stale: Bool, now: Date = .now) -> Pace? {
        stale ? nil : pace(w, now: now)
    }

    public static func paceLabel(_ s: PaceState) -> String {
        switch s {
        case .over: String(localized: "Ahead of pace")
        case .under: String(localized: "Behind pace")
        case .on: String(localized: "On pace")
        }
    }

    /// When a window starts over, as a phrase: "resets in 45m" within the hour, "resets 13:05" when it is today, otherwise
    /// "resets in 2d 3h"; "resets soon" when it is due, "" without a time.
    public static func resetLabel(_ resetsAt: Date?, now: Date = .now, calendar: Calendar = .current) -> String {
        guard let at = resetsAt else { return "" }
        let secs = at.timeIntervalSince(now)
        if secs <= 0 { return String(localized: "resets soon") }
        let mins = Int((secs / 60).rounded(.up))
        if mins < 60 { return String(localized: "resets in \(mins)m") }
        if calendar.isDate(at, inSameDayAs: now) {
            let time = at.formatted(Date.FormatStyle(date: .omitted, time: .shortened, calendar: calendar, timeZone: calendar.timeZone))
            return String(localized: "Resets \(time)")
        }
        let hours = mins / 60
        let d = hours / 24
        return d > 0 ? String(localized: "resets in \(d)d \(hours % 24)h") : String(localized: "resets in \(hours)h")
    }

    /// How long ago a sample was taken ("12 s ago"), coarser past a minute; "" without a time.
    public static func sampleAge(_ at: Date?, now: Date = .now) -> String {
        guard let at, at.isMeaningful else { return "" }
        let s = max(0, Int(now.timeIntervalSince(at).rounded()))
        if s < 60 { return String(localized: "\(s) seconds ago") }
        if s < 3600 { return String(localized: "\(s / 60) min ago") }
        if s < 86400 { return String(localized: "\(s / 3600)h ago") }
        return String(localized: "\(s / 86400)d ago")
    }

    /// The age as the account's header says it: "Outdated · 3 hr ago" when stale.
    public static func ageLabel(_ a: UsageAccount, now: Date = .now) -> String {
        a.stale ? staleLabel(a.sampledAt, now: now) : sampleAge(a.sampledAt, now: now)
    }

    /// "Outdated · 3 hr ago" (just "Outdated" without a time): numbers the hub marked out of date, wherever they are shown.
    public static func staleLabel(_ sampledAt: Date?, now: Date = .now) -> String {
        let age = sampleAge(sampledAt, now: now)
        return age.isEmpty ? String(localized: "Outdated [Usage Windows 2fe0e3]") : String(localized: "Outdated · \(age)")
    }

    /// The accounts worth showing: samples within three days, windows that have not reset, accounts that still have one;
    /// each stale once its `staleAt` has passed, however long ago the hub said it (#1905: a group whose refresh fails
    /// keeps its last answer, and that answer's own stale mark was the hub's at that moment).
    public static func live(_ accounts: [UsageAccount], now: Date = .now) -> [UsageAccount] {
        accounts.compactMap { a in
            if let t = a.sampledAt, t.isMeaningful, now.timeIntervalSince(t) > sampleMaxAge { return nil }
            var a = a
            a.windows = a.windows.filter { $0.resetsAt.map { $0 > now } ?? true }
            if let at = a.staleAt, now >= at { a.stale = true }
            return a.windows.isEmpty ? nil : a
        }
    }

    /// The used percentage of an account's fullest window. A per-model allowance does not count while the plan's own
    /// windows are there: one model running out is not the account running out.
    public static func fullest(_ a: UsageAccount) -> Double {
        let plan = a.windows.filter { $0.model == nil }
        return (plan.isEmpty ? a.windows : plan).map(\.usedPercent).max() ?? 0
    }

    /// Where an account was seen, for people: the computers that read it by name (an older hub only says "a computer"),
    /// then the gateways'. Never "this device": the numbers were read on a node, not on the device in hand (#1907).
    public static func sourceNames(_ a: UsageAccount) -> [String] {
        let machines = !a.machines.isEmpty ? a.machines
            : a.local ? [String(localized: "usage.source.local", defaultValue: "Computer", comment: "Usage numbers read from an agent's own files on one of the user's computers, when the hub doesn't say which")] : []
        return machines + a.sources
    }

    /// The plan to write beside the name, unless the name already says it ("Codex Pro" and plan "pro").
    public static func planNote(_ a: UsageAccount) -> String {
        let plan = (a.plan ?? "").trimmingCharacters(in: .whitespaces)
        return !plan.isEmpty && !a.name.lowercased().contains(plan.lowercased()) ? plan : ""
    }

    /// What the gateway could not do, in words; "" when nothing went wrong or the hub sends a reason this build doesn't
    /// know. A key the gateway turned away says where to fix it: it is not a network problem (#1902).
    public static func problemText(_ a: UsageAccount) -> String {
        switch a.problem {
        case "rejected": String(localized: "The gateway turned the key away; update it in Admin → Usage sources. Showing the last numbers")
        case "unreachable": String(localized: "Can't reach the gateway right now; showing the last numbers")
        case "failed": String(localized: "Couldn't read the gateway; showing the last numbers")
        case "error": String(localized: "The gateway couldn't read this account; showing the numbers it saved")
        default: ""
        }
    }

    /// The accounts the bars are drawn for: the `n` with the fullest window, stale ones last. Stable for equal ones.
    public static func top(_ accounts: [UsageAccount], n: Int = 2) -> [UsageAccount] {
        accounts.enumerated()
            .sorted { l, r in
                if l.element.stale != r.element.stale { return !l.element.stale }
                let (fl, fr) = (fullest(l.element), fullest(r.element))
                return fl != fr ? fl > fr : l.offset < r.offset
            }
            .prefix(n)
            .map(\.element)
    }

    public static func isWarn(_ percent: Double) -> Bool { percent >= warnPercent }

    /// "86%".
    public static func percentText(_ p: Double) -> String { "\(Int(p.rounded()))%" }

    /// 0…1 for a bar.
    public static func fraction(_ p: Double) -> Double { min(100, max(0, p)) / 100 }
}
