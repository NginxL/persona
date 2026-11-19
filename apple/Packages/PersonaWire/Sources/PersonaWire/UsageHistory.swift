import Foundation

// Token usage history (docs/server/machines.md "Token usage history", #9480): GET /api/v1/usage/days and
// GET /api/v1/usage/hours, every computer's tokens summed in the user's zone, the same count as today's tokens.

/// One day of the calendar in the user's zone.
public nonisolated struct UsageDay: Decodable, Hashable, Sendable, Identifiable {
    /// 2006-01-02, the day in the hub's user zone.
    public var date: String
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case date }

    public init(date: String, tokens: UsageTokens) {
        self.date = date
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        date = try decoder.container(keyedBy: CodingKeys.self).decode(String.self, forKey: .date)
        tokens = try UsageTokens(from: decoder)
    }

    public var id: String { date }
}

/// GET /api/v1/usage/days: the last days, oldest first; the last is today.
public nonisolated struct UsageDays: Decodable, Hashable, Sendable {
    public var zone: String
    public var days: [UsageDay]

    public init(zone: String, days: [UsageDay]) {
        self.zone = zone
        self.days = days
    }

    /// The last n days' sums (today's with n 1).
    public func last(_ n: Int) -> UsageTokens { days.suffix(n).reduce(UsageTokens()) { $0 + $1.tokens } }
}

/// One hour on the clock in the user's zone (a day the clocks change has 23 or 25).
public nonisolated struct UsageHour: Decodable, Hashable, Sendable {
    public var t: Int64
    public var date: String
    public var hour: Int
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case t, date, hour }

    public init(t: Int64, date: String, hour: Int, tokens: UsageTokens) {
        self.t = t
        self.date = date
        self.hour = hour
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        t = try c.decode(Int64.self, forKey: .t)
        date = try c.decode(String.self, forKey: .date)
        hour = try c.decode(Int.self, forKey: .hour)
        tokens = try UsageTokens(from: decoder)
    }
}

/// One computer's share of the hours; an empty name is a computer no longer there.
public nonisolated struct UsageMachineShare: Decodable, Hashable, Sendable, Identifiable {
    public var id: String
    public var name: String
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case id, name }

    public init(id: String, name: String, tokens: UsageTokens) {
        self.id = id
        self.name = name
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        id = try c.decode(String.self, forKey: .id)
        name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
        tokens = try UsageTokens(from: decoder)
    }
}

/// One agent's share of the hours (#9984): `agent` is `Session.agent`'s name ("claude", "codex", "pi", …); "" for usage
/// the hub stored before it kept agents apart.
public nonisolated struct UsageAgentShare: Decodable, Hashable, Sendable, Identifiable {
    public var agent: String
    public var tokens: UsageTokens

    public enum CodingKeys: String, CodingKey { case agent }

    public init(agent: String, tokens: UsageTokens) {
        self.agent = agent
        self.tokens = tokens
    }

    public init(from decoder: Decoder) throws {
        agent = try decoder.container(keyedBy: CodingKeys.self).decodeIfPresent(String.self, forKey: .agent) ?? ""
        tokens = try UsageTokens(from: decoder)
    }

    public var id: String { agent }
}

/// GET /api/v1/usage/hours: the last days by the hour, oldest first, each computer's and each agent's share (largest
/// first) and the total. An older hub sends no agents (empty).
public nonisolated struct UsageHours: Decodable, Hashable, Sendable {
    public var zone: String
    public var hours: [UsageHour]
    public var machines: [UsageMachineShare]
    public var agents: [UsageAgentShare]
    public var total: UsageTokens

    public enum CodingKeys: String, CodingKey { case zone, hours, machines, agents, total }

    public init(zone: String, hours: [UsageHour], machines: [UsageMachineShare], agents: [UsageAgentShare] = [], total: UsageTokens) {
        self.zone = zone
        self.hours = hours
        self.machines = machines
        self.agents = agents
        self.total = total
    }

    public init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        zone = try c.decode(String.self, forKey: .zone)
        hours = try c.decode([UsageHour].self, forKey: .hours)
        machines = try c.decode([UsageMachineShare].self, forKey: .machines)
        agents = try c.decodeIfPresent([UsageAgentShare].self, forKey: .agents) ?? []
        total = try c.decode(UsageTokens.self, forKey: .total)
    }
}

/// The steps of a heat calendar (docs/server/machines.md "Token usage history"): 0 for nothing, else 1 to `count` by
/// where a value ranks among the values above 0, as GitHub does: `ceil(count × (values at or below it) / (values above
/// 0))`. The largest is always the last step, values that are equal share one, and one very large value does not push
/// the others down (a scale from the largest made every other day the faintest). Every client takes this rule.
public nonisolated enum HeatLevels {
    public static func levels(_ values: [Int64], count: Int = 4) -> [Int] {
        let sorted = values.filter { $0 > 0 }.sorted()
        guard !sorted.isEmpty, count > 0 else { return values.map { _ in 0 } }
        return values.map { v in
            guard v > 0 else { return 0 }
            // How many are at or below v: the first index past v.
            var lo = 0, hi = sorted.count
            while lo < hi {
                let mid = (lo + hi) / 2
                if sorted[mid] <= v { lo = mid + 1 } else { hi = mid }
            }
            return max(1, min(count, Int((Double(count * lo) / Double(sorted.count)).rounded(.up))))
        }
    }
}
