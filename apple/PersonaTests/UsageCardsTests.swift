import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The usage cards' pure part (#13173): grouping by provider, the 7-day-reset order, the window order, the timeline geometry.
struct UsageCardsTests {
    let now = Date(timeIntervalSince1970: 1_793_000_000)

    private func win(_ label: String, _ mins: Int, used: Double = 10, left: TimeInterval?, model: String? = nil) -> UsageWindow {
        UsageWindow(label: label, windowMinutes: mins, usedPercent: used, resetsAt: left.map { now.addingTimeInterval($0) }, model: model)
    }

    private func acct(_ id: String, _ provider: String, name: String? = nil, windows: [UsageWindow], sampled: TimeInterval? = nil) -> UsageAccount {
        UsageAccount(id: id, provider: provider, name: name ?? id, windows: windows, sampledAt: sampled.map { now.addingTimeInterval($0) })
    }

    @Test func groupsByProviderInFirstSeenOrderWithTheNewestSample() {
        let list = [
            acct("c1", "claude", windows: [win("7d", 10080, left: 3600)], sampled: -300),
            acct("x1", "codex", windows: [win("7d", 10080, left: 3600)], sampled: -50),
            acct("c2", "Claude", windows: [win("7d", 10080, left: 7200)], sampled: -60),
        ]
        let groups = UsageWindows.groups(list)
        #expect(groups.map(\.provider) == ["claude", "codex"])
        #expect(groups[0].accounts.map(\.id) == ["c1", "c2"])
        #expect(groups[0].sampledAt == now.addingTimeInterval(-60))
    }

    @Test func hiddenProvidersAreLeftOut() {
        let list = [acct("c1", "claude", windows: [win("5h", 300, left: 60)]), acct("x1", "codex", windows: [win("5h", 300, left: 60)])]
        #expect(UsageWindows.groups(list, hidden: ["codex"]).map(\.provider) == ["claude"])
        #expect(UsageWindows.providers(list) == ["claude", "codex"])
    }

    @Test func accountsAreOrderedByTheirWeekResetThenName() {
        let soon = acct("a", "claude", name: "Zed", windows: [win("7d", 10080, left: 3600)])
        let later = acct("b", "claude", name: "Amy", windows: [win("5h", 300, left: 60), win("7d", 10080, left: 86400)])
        let noReset = acct("c", "claude", name: "Bob", windows: [win("7d", 10080, left: nil)])
        let noWeek = acct("d", "claude", name: "Abe", windows: [win("5h", 300, left: 60)])
        let modelOnly = acct("e", "claude", name: "Cat", windows: [win("7d", 10080, left: 10, model: "Fable")])
        let sorted = UsageWindows.sortedByWeekReset([noWeek, later, modelOnly, noReset, soon])
        // The 7d window with a reset first (soonest first); the rest by name: Abe, Bob, Cat.
        #expect(sorted.map(\.id) == ["a", "b", "d", "c", "e"])
        #expect(UsageWindows.sortedByWeekReset([later, soon]).map(\.id) == ["a", "b"])
    }

    @Test func windowsListFiveHourThenWeekThenTheRest() {
        let a = acct("a", "claude", windows: [win("7d", 10080, left: 100, model: "Fable"), win("30d", 43200, left: 100), win("7d", 10080, left: 100), win("5h", 300, left: 100)])
        #expect(UsageWindows.cardWindows(a).map(\.key) == ["5h:300:", "7d:10080:", "7d:10080:Fable", "30d:43200:"])
    }

    @Test func axisGivesTheFirstDay45PercentAndTheRestSeven() {
        #expect(UsageWindows.timelinePosition(hours: 0) == 0)
        #expect(UsageWindows.timelinePosition(hours: 12) == 0.225)
        #expect(UsageWindows.timelinePosition(hours: 24) == 0.45)
        #expect(abs(UsageWindows.timelinePosition(hours: 96) - 0.725) < 1e-9)
        #expect(UsageWindows.timelinePosition(hours: 168) == 1)
        #expect(UsageWindows.timelinePosition(hours: 500) == 1)
        #expect(UsageWindows.timelinePosition(hours: -3) == 0)
    }

    @Test func timelineRowsFollowTheListOrderWithPlanWindowsOnly() throws {
        let a = acct("a", "claude", name: "First", windows: [win("5h", 300, left: 1.5 * 3600), win("7d", 10080, left: 2 * 3600)])
        let b = acct("b", "claude", name: "Second", windows: [win("7d", 10080, left: 150 * 3600), win("7d", 10080, left: 3600, model: "Fable")])
        let none = acct("c", "claude", name: "Third", windows: [win("5h", 300, left: nil), win("30d", 43200, left: 3600)])
        let rows = UsageWindows.timelineRows([a, b, none], now: now)
        #expect(rows.map(\.id) == ["a", "b"])
        let five = try #require(rows[0].fiveHour)
        #expect(five.seconds == 1.5 * 3600 && abs(five.position - UsageWindows.timelinePosition(hours: 1.5)) < 1e-12)
        #expect(rows[0].week?.seconds == 7200.0)
        #expect(rows[1].fiveHour == nil)
        // The per-model 7d is not the plan's: the row's bar is the plan's own, 150 hours out.
        #expect(abs((rows[1].week?.position ?? 0) - UsageWindows.timelinePosition(hours: 150)) < 1e-12)
    }

    @Test func aResetPastOrOutsideTheAxisIsHandled() {
        let far = acct("a", "codex", windows: [win("7d", 10080, left: 400 * 3600)])
        #expect(UsageWindows.timelineRows([far], now: now).first?.week?.position == 1)
        let past = acct("b", "codex", windows: [win("5h", 300, left: -5)])
        #expect(UsageWindows.timelineRows([past], now: now).isEmpty)
    }

    @Test func shortDurations() {
        #expect(UsageWindows.shortDuration(45 * 60) == "45分") // the tests run in zh-Hans
        #expect(UsageWindows.shortDuration(1.5 * 3600) == "1.5小时")
        #expect(UsageWindows.shortDuration(4 * 3600) == "4小时")
        #expect(UsageWindows.shortDuration(50 * 3600) == "2天2时")
        #expect(UsageWindows.shortDuration(3 * 86400) == "3天")
    }

    @Test func protectPercentIsReadWhenPresentAndTolerated() throws {
        let json = """
        {"accounts":[{"id":"a","provider":"claude","name":"A","windows":[
          {"label":"5h","window_minutes":300,"used_percent":3,"protect_percent":98},
          {"label":"7d","window_minutes":10080,"used_percent":30},
          {"label":"7d","window_minutes":10080,"used_percent":30,"model":"M","protect_percent":140}]}]}
        """
        struct R: Decodable { var accounts: [UsageAccount] }
        let w = try JSON.decoder.decode(R.self, from: Data(json.utf8)).accounts[0].windows
        #expect(w[0].protectPercent == 98)
        #expect(w[1].protectPercent == nil)
        #expect(w[2].protectPercent == nil) // out of range: ignored
    }

    @Test func providerTitles() {
        #expect(UsageWindows.providerTitle("claude") == "Claude")
        #expect(UsageWindows.providerTitle("xai") == "xAI")
        #expect(UsageWindows.providerTitle("kimi") == "Kimi")
    }
}
