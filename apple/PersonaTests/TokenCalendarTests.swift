import Foundation
@testable import Persona
import PersonaWire
import Testing

/// The token calendar (#9506, docs/server/machines.md "Token usage history"): the steps by rank, where each day and
/// hour goes, the language's short numbers, and the reads against a hub in memory.
@MainActor
struct TokenCalendarTests {
    // MARK: Steps

    /// A day's step is where it ranks among the days with usage, so one very busy day does not push the others to the
    /// faintest; the largest is always the last step, equal days share one, no usage is 0.
    @Test func stepsGoByRank() {
        #expect(HeatLevels.levels([0, 10, 20, 30, 40]) == [0, 1, 2, 3, 4])
        #expect(HeatLevels.levels([10, 10, 10, 1000]) == [3, 3, 3, 4])
        #expect(HeatLevels.levels([5]) == [4])
        #expect(HeatLevels.levels([0, 0]) == [0, 0])
        #expect(HeatLevels.levels([]) == [])
        // Scaled from the busiest, eleven days of 1 to 11 with one of 1000 were all step 1; by rank they spread.
        let days: [Int64] = Array(1...11) + [1000]
        #expect(Set(HeatLevels.levels(days)) == [1, 2, 3, 4])
    }

    // MARK: Days

    private static func days(_ list: [(String, Int64)]) -> [UsageDay] {
        list.map { UsageDay(date: $0.0, tokens: UsageTokens(cacheRead: $0.1)) }
    }

    /// A column a week, Monday on top, this week last with the days to come left out, today outlined; months over the
    /// first week that starts in them; Monday, Wednesday and Friday named.
    @Test func laysOutWeeks() {
        // Tuesday 17 November 2026 is today.
        let list = Self.days([("2026-11-15", 5), ("2026-11-16", 10), ("2026-11-17", 20)])
        let l = TokenCalendarLayout(days: list, weeks: 3, locale: Locale(identifier: "en_US"))
        #expect(l.levels.count == 3 && l.levels.allSatisfy { $0.count == 7 })
        #expect(l.dates[0][0] == "2026-11-02" && l.dates[2][0] == "2026-11-16" && l.dates[2][1] == "2026-11-17")
        #expect(l.dates[2][2] == nil && l.levels[2][2] == -1 && l.levels[2][6] == -1)
        #expect(l.today == HeatGrid.Cell(column: 2, row: 1))
        // Sunday 15th is the last row of the middle week; three days with usage rank 2, 3 and 4.
        #expect(l.levels[1][6] == 2 && l.levels[2][0] == 3 && l.levels[2][1] == 4 && l.levels[0][0] == 0)
        #expect(l.months == [.init(column: 0, name: "Nov")])
        #expect(l.weekdays == ["M", "", "W", "", "F", "", ""])
        #expect(l.cell(of: "2026-11-16") == HeatGrid.Cell(column: 2, row: 0) && l.date(at: .init(column: 1, row: 6)) == "2026-11-15")
    }

    /// A month is named over the first week that starts in it; the first column's, of a month begun before it, is not.
    @Test func namesMonthsWhereTheyStart() {
        let list = Self.days([("2026-11-17", 1)])
        let l = TokenCalendarLayout(days: list, weeks: 8, locale: Locale(identifier: "en_US"))
        // Columns start on 28 Sep, 5 Oct, … 26 Oct, 2 Nov, 9 Nov, 16 Nov.
        #expect(l.dates[0][0] == "2026-09-28")
        #expect(l.months.map(\.name) == ["Oct", "Nov"] && l.months.map(\.column) == [1, 5])
        let zh = TokenCalendarLayout(days: list, weeks: 8, locale: Locale(identifier: "zh-Hans"))
        #expect(zh.months.map(\.name) == ["10月", "11月"] && zh.weekdays[0] == "一" && zh.weekdays[4] == "五")
    }

    /// As many weeks as fit: 19 across an iPhone's card, never fewer than 8 nor more than a year's 53.
    @Test func fitsWeeksToTheWidth() {
        #expect(TokenCalendarLayout.weeks(width: 301) == 19)
        #expect(TokenCalendarLayout.weeks(width: 40) == Theme.Heat.minWeeks)
        #expect(TokenCalendarLayout.weeks(width: 5000) == Theme.Heat.maxWeeks)
    }

    // MARK: Hours

    /// A row a day, a column an hour on the clock; the hours still to come today are not drawn; the hour that comes
    /// twice on the day the clocks go back adds into one square.
    @Test func laysOutHours() {
        var hours: [UsageHour] = []
        for h in 0..<24 { hours.append(UsageHour(t: Int64(h) * 3600, date: "2026-11-01", hour: h, tokens: UsageTokens(output: Int64(h)))) }
        hours.insert(UsageHour(t: 9000, date: "2026-11-01", hour: 1, tokens: UsageTokens(output: 100)), at: 2) // the second 1 a.m.
        for h in 0...9 { hours.append(UsageHour(t: 86400 + Int64(h) * 3600, date: "2026-11-02", hour: h, tokens: UsageTokens(output: 1))) }
        let l = UsageHourLayout(hours: hours, today: "Today", locale: Locale(identifier: "en_US"))
        #expect(l.dates == ["2026-11-01", "2026-11-02"] && l.rowNames.last == "Today")
        #expect(l.levels.count == 24 && l.levels[0].count == 2)
        #expect(l.tokens[1][0].output == 101 && l.max == 101 && l.levels[1][0] == 4)
        #expect(l.levels[0][0] == 0 && l.levels[10][1] == -1 && l.levels[9][1] >= 1)
        // ICU puts a narrow no-break space before AM / PM.
        let plain = { (s: String?) in s?.replacingOccurrences(of: "\u{202F}", with: " ") }
        #expect(plain(l.hourNames[0]) == "12 AM" && plain(l.hourNames[18]) == "6 PM")
    }

    // MARK: Words

    @Test func shortNumbersInTheLanguage() {
        #expect(TokenFormat.short(3_200_000_000, locale: Locale(identifier: "zh-Hans")) == "32亿")
        #expect(TokenFormat.short(72_900_000, locale: Locale(identifier: "zh-Hans")) == "7290万")
        #expect(TokenFormat.short(3_200_000_000, locale: Locale(identifier: "en_US")) == "3.2B")
        #expect(TokenFormat.short(56_700_000_000, locale: Locale(identifier: "ja")) == "567億")
        #expect(TokenFormat.short(950, locale: Locale(identifier: "en_US")) == "950")
    }

    // MARK: Reads

    /// The days decode, today and the last 30 days add up, a hub with no usage shows no card, an older hub (404) none
    /// either, and a read that fails later keeps what was there.
    @Test func readsTheDays() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/usage/days", json: #"{"usage":{"zone":"Asia/Shanghai","stored":true,"days":[{"date":"2026-11-16","in":1,"cache_read":99},{"date":"2026-11-17","out":5,"cache_write":5,"req":2}]}}"#)
        let api = hub.client
        let usage = TokenUsageModel(api: { api })
        await usage.loadDays(TokenUsageModel.phoneDays)
        #expect(usage.hasUsage && usage.days?.days.count == 2)
        #expect(usage.days?.last(1).total == 10 && usage.days?.last(30).total == 110)
        #expect(hub.sent("GET", "/api/v1/usage/days").first?.query["days"] == "140")

        hub.on("GET", "/api/v1/usage/days") { _ in throw APIError(status: 502, message: "down") }
        await usage.loadDays(TokenUsageModel.phoneDays)
        #expect(usage.hasUsage && !usage.failed)

        let older = TokenUsageModel(api: { FakeHub().client })
        await older.loadDays(TokenUsageModel.phoneDays)
        #expect(!older.hasUsage && older.failed)

        let quiet = FakeHub()
        quiet.on("GET", "/api/v1/usage/days", json: #"{"usage":{"zone":"UTC","stored":true,"days":[{"date":"2026-11-17"}]}}"#)
        let none = TokenUsageModel(api: { quiet.client })
        await none.loadDays(7)
        #expect(none.days != nil && !none.hasUsage)
    }

    @Test func readsTheHours() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/usage/hours", json: #"{"usage":{"zone":"UTC","stored":true,"hours":[{"t":0,"date":"2026-11-17","hour":0,"out":3}],"machines":[{"id":"a","name":"Studio","out":3},{"id":"gone","name":"","out":1}],"total":{"out":4}}}"#)
        let api = hub.client
        let usage = TokenUsageModel(api: { api })
        await usage.loadHours()
        #expect(usage.hours?.hours.first?.tokens.output == 3 && usage.hours?.machines.map(\.name) == ["Studio", ""])
        #expect(usage.hours?.total.output == 4 && hub.sent("GET", "/api/v1/usage/hours").first?.query["days"] == "7")
        #expect(usage.hours?.agents.isEmpty == true) // an older hub sends no agents: no By Agent section
    }

    /// Each agent's share (#9984), as the page names it: its product's name and mark; "" (usage kept before the hub
    /// counted agents apart) has none.
    @Test func readsTheAgents() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/usage/hours", json: #"{"usage":{"zone":"UTC","stored":true,"hours":[],"machines":[],"agents":[{"agent":"claude","cache_read":90,"out":10},{"agent":"","out":5},{"agent":"opencode","in":1}],"total":{"out":15}}}"#)
        let api = hub.client
        let usage = TokenUsageModel(api: { api })
        await usage.loadHours()
        let agents = try #require(usage.hours?.agents)
        #expect(agents.map(\.agent) == ["claude", "", "opencode"] && agents.first?.tokens.total == 100)
        #expect(AgentKind(usageAgent: "claude")?.name == "Claude Code" && AgentKind(usageAgent: "claude")?.brand == Theme.Brand.claude)
        #expect(AgentKind(usageAgent: "codex")?.brand == Theme.Brand.openAI && AgentKind(usageAgent: "omp")?.name == "Oh My Pi")
        #expect(AgentKind(usageAgent: "opencode")?.name == "opencode" && AgentKind(usageAgent: "opencode")?.brand == Theme.Brand.opencode)
        #expect(AgentKind(usageAgent: "") == nil)
    }

    /// What every client calls each agent (spec/fixtures/agent-names, #10042): the web checks the same file.
    @Test func namesTheAgentsAsEveryClientDoes() throws {
        let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        let data = try Data(contentsOf: repo.appending(path: "spec/fixtures/agent-names/names.json"))
        let fixture = try #require(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        let names = try #require(fixture["names"] as? [String: String])
        #expect(!names.isEmpty)
        for (id, name) in names {
            #expect(AgentKind(usageAgent: id)?.name == name, "\(id)")
        }
    }
}
