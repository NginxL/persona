import XCTest
@testable import Persona
import PersonaWire

/// Replays the shared fixture spec/fixtures/needs-you/cases.json as the app reads it: the Agents tab's Needs You is one
/// row per session that waits for the user (its cards under it), the section and the filter list those rows, and the
/// badge counts the rows `expected.badge_rows` names, which leave out the muted agents, quiet tasks' agents and
/// directory-hidden sessions. A case marked as iOS's known difference (`known_differences.ios`) is asserted at today's
/// value; a mark on any client must name a decision and change what that client is asserted at. With the counts report
/// switch on (spec/fixtures/counts/README.md, "Lane reports"), each case's rows and badge also go to the iOS lane's
/// report.
@MainActor
final class AgentsNeedsYouFixtureTests: XCTestCase {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
    /// The clients that replay the fixture, the only names a known difference may carry (the fixture's README).
    private static let lanes: Set<String> = ["android", "ios"]

    private struct Fixture: Decodable {
        var version: Int
        var cases: [Case]
    }

    private struct Case: Decodable {
        var name: String
        var input: Input
        var expected: Expected
        var knownDifferences: [String: Difference]?

        enum CodingKeys: String, CodingKey {
            case name, input, expected
            case knownDifferences = "known_differences"
        }
    }

    private struct Input: Decodable {
        var sessions: [Session]
        var interactions: [Interaction]
        var mutes: Pins
        var quiet: [String]
    }

    private struct Expected: Decodable, Equatable {
        var rows: [Row]
        var badgeRows: [Row]
        var badge: Int

        enum CodingKeys: String, CodingKey {
            case rows, badge
            case badgeRows = "badge_rows"
        }
    }

    /// One client's known difference: the decision that settles it, and today's values for the keys of `expected` it
    /// replaces.
    private struct Difference: Decodable {
        var decision: String
        var why: String
        var replace: Replace
    }

    private struct Replace: Decodable {
        var rows: [Row]?
        var badgeRows: [Row]?
        var badge: Int?

        enum CodingKeys: String, CodingKey {
            case rows, badge
            case badgeRows = "badge_rows"
        }
    }

    private struct Row: Codable, Equatable {
        var id: String
        var cards: [String]
    }

    /// One line of the iOS lane's report: what a surface shows for a case, and its rows.
    private struct ReportLine: Encodable {
        var lane = "ios"
        var fixture = "needs-you"
        var name: String
        var surface: String
        var value: Int
        var keys: [Row]

        enum CodingKeys: String, CodingKey {
            case lane, fixture, surface, value, keys
            case name = "case"
        }
    }

    /// Writes the iOS lane's report, `<directory>/ios.jsonl`, when the counts report switch is on: `PERSONA_COUNTS_REPORT`
    /// in the test's environment (`TEST_RUNNER_PERSONA_COUNTS_REPORT` for xcodebuild), a directory relative to the
    /// repository root or absolute.
    private static func report(_ lines: [ReportLine]) throws {
        guard let path = ProcessInfo.processInfo.environment["PERSONA_COUNTS_REPORT"], !path.isEmpty else { return }
        let directory = path.hasPrefix("/") ? URL(fileURLWithPath: path) : repo.appending(path: path)
        try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        let text = try lines.map { String(decoding: try encoder.encode($0), as: UTF8.self) + "\n" }.joined()
        try Data(text.utf8).write(to: directory.appending(path: "ios.jsonl"), options: .atomic)
    }

    /// What iOS is asserted at for `c`: its `expected`, with iOS's known difference applied. Fails a mark that names no
    /// decision, names a client that does not replay the fixture, or changes nothing.
    private static func wanted(_ c: Case) -> Expected {
        var want = c.expected
        for (lane, mark) in c.knownDifferences ?? [:] {
            XCTAssertTrue(lanes.contains(lane), "\(c.name): known difference for \(lane), which does not replay the fixture")
            let number = mark.decision.dropFirst()
            XCTAssertTrue(mark.decision.hasPrefix("D") && !number.isEmpty && number.allSatisfy(\.isASCII) && number.allSatisfy(\.isNumber),
                          "\(c.name): \(lane)'s known difference names no decision")
            XCTAssertFalse(mark.why.isEmpty, "\(c.name): \(lane)'s known difference does not say why")
            var today = c.expected
            if let rows = mark.replace.rows { today.rows = rows }
            if let rows = mark.replace.badgeRows { today.badgeRows = rows }
            if let badge = mark.replace.badge { today.badge = badge }
            XCTAssertNotEqual(today, c.expected, "\(c.name): \(lane)'s known difference changes nothing")
            if lane == "ios" { want = today }
        }
        return want
    }

    func testEveryCaseCountsSessionsNotCards() throws {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/needs-you/cases.json"))
        let fixture = try JSON.decoder.decode(Fixture.self, from: data)
        XCTAssertEqual(fixture.version, 1)
        XCTAssertGreaterThanOrEqual(fixture.cases.count, 14)
        var lines: [ReportLine] = []
        for c in fixture.cases {
            let i = c.input
            let want = Self.wanted(c)
            // As AppModel holds them: each session's own row for the cards and their places (`sessionIndex`), and the
            // lists' rows with side sessions folded under their parents (`allSessions`, from a hub that folds them).
            let byKey = Dictionary(i.sessions.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
            let listed = SideFold.fold(i.sessions, groups: [""]).listed
            let pending = i.interactions.sorted { ($0.createdAt ?? .distantPast) < ($1.createdAt ?? .distantPast) }
            let waiting = AgentsNeedsYou.waiting(listed)
            // The Needs You filter matches the same sessions in the list, whose rows leave the directory-hidden ones out
            // and fold sides and children under their parents (`AgentListProjection.preparedSessions`).
            let shown = ChildSessions.fold(SideFold.fold(i.sessions.filter { $0.hiddenByDirectory != true }, groups: [""]).listed)
            XCTAssertEqual(waiting.map(\.key), shown.filter { !$0.assistant && AgentFilter.needsYou.matches($0) }.map(\.key),
                           "\(c.name): the Needs You filter's sessions")
            let rows = AgentsNeedsYou.rows(cards: AgentsNeedsYou.cards(pending, session: { byKey[$0] }), waiting: waiting)
            let sectionRows = rows.map { Row(id: $0.id, cards: $0.cards.map(\.id)) }
            XCTAssertEqual(sectionRows, want.rows, c.name)
            // As countBadges counts it: every pending card but a directory-hidden session's (`directoryVisibleInteractions`;
            // the Persona tab's cards are its own place), every waiting session.
            func place(_ key: SessionKey) -> TabBadges.Place {
                TabBadges.place(key, session: byKey[key], quiet: i.quiet.contains(AppModel.pinKey(session: key)), mutes: i.mutes)
            }
            let visible = pending.filter { byKey[$0.sessionKey]?.hiddenByDirectory != true }
            let counted = AgentsNeedsYou.rows(cards: visible, waiting: waiting)
            // The rows `AgentsNeedsYou.badge` counts.
            let badgeRows = counted.filter { place($0.key) == .agents }.map { Row(id: $0.id, cards: $0.cards.map(\.id)) }
            let badge = AgentsNeedsYou.badge(counted, place: place)
            XCTAssertEqual(badgeRows, want.badgeRows, "\(c.name): badge rows")
            XCTAssertEqual(badge, want.badge, "\(c.name): badge")
            lines.append(ReportLine(name: c.name, surface: "agents_tab", value: badge, keys: badgeRows))
            lines.append(ReportLine(name: c.name, surface: "agents_section", value: sectionRows.count, keys: sectionRows))
        }
        try Self.report(lines)
    }
}
