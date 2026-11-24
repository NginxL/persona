import PersonaWire
import XCTest
@testable import Persona

/// Replays spec/fixtures/triage through the same models and rules the Apple list uses (#5732).
final class TodoTriageFixtureTests: XCTestCase {
    private static let repo = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()

    private struct Fixture: Decodable {
        var version: Int
        var cases: [Case]
    }

    private struct Case: Decodable {
        var name: String
        var input: Input
        var expected: Expected
    }

    private struct Input: Decodable {
        var now: String?
        var sessions: [Session]?
        var todo: TodoItem?
        var todos: [TodoItem]?
        var restored_at: String?
        var include_quiet: Bool?
    }

    private struct Expected: Decodable {
        var section: String?
        var rule: String?
        var last: String?
        var sections: [String: [[String]]]?
        var open: Int?
        var shelved: Int?
        var finished: Int?
        var cancelled: Int?
        var keys: [String]?
    }

    func testDecisionTableFixtures() throws {
        for c in try cases("triage.json") {
            let input = c.input
            let todo = try XCTUnwrap(input.todo, c.name)
            let got = TodoTriageLogic.triage(todo, lookup: lookup(input.sessions ?? []), now: date(try XCTUnwrap(input.now)),
                                             restoredAt: input.restored_at.flatMap(date), calendar: utc)
            XCTAssertEqual(got.section.rawValue, c.expected.section, c.name)
            XCTAssertEqual(got.rule.rawValue, c.expected.rule, c.name)
            if let last = c.expected.last {
                XCTAssertEqual(got.last, try XCTUnwrap(JSON.parseDate(last)), c.name)
            }
        }
    }

    func testGroupingAndCountFixtures() throws {
        for c in try cases("groups.json") {
            let input = c.input
            let got = TodoTriageLogic.triageTodos(try XCTUnwrap(input.todos), lookup: lookup(input.sessions ?? []), now: date(try XCTUnwrap(input.now)),
                                                  quiet: input.include_quiet ?? true, calendar: utc)
            for section in TodoSection.allCases {
                let wanted = c.expected.sections?[section.rawValue] ?? []
                XCTAssertEqual(got.groups(section).map { $0.members.map(\.id) }, wanted, "\(c.name): \(section.rawValue)")
            }
            XCTAssertEqual(got.open, c.expected.open, c.name)
            XCTAssertEqual(got.shelved, c.expected.shelved, c.name)
            XCTAssertEqual(got.finished, c.expected.finished, c.name)
            XCTAssertEqual(got.cancelled, c.expected.cancelled ?? 0, c.name)
        }
    }

    func testRelatedKeyFixtures() throws {
        for c in try cases("related.json") {
            XCTAssertEqual(TodoTriageLogic.relatedKeys(try XCTUnwrap(c.input.todo)), c.expected.keys, c.name)
        }
    }

    private var utc: Calendar {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = try! XCTUnwrap(TimeZone(secondsFromGMT: 0))
        return calendar
    }

    private func date(_ value: String) -> Date {
        try! XCTUnwrap(JSON.parseDate(value))
    }

    private func lookup(_ sessions: [Session]) -> TodosLogic.SessionLookup {
        let byKey = Dictionary(sessions.map { ("\($0.machineID)/\($0.sessionID)", $0) }, uniquingKeysWith: { a, _ in a })
        return { machine, session in byKey["\(machine)/\(session)"] }
    }

    private func cases(_ file: String) throws -> [Case] {
        let data = try Data(contentsOf: Self.repo.appending(path: "spec/fixtures/triage/\(file)"))
        let fixture = try JSON.decoder.decode(Fixture.self, from: data)
        XCTAssertEqual(fixture.version, 1)
        // Native concern behavior has its own follow-up (#3582); the shared concern_* cases are hub/web-only until then.
        // Parked-rule alignment likewise remains #5929, so its hub/web-only case is not a native expectation.
        let supported = fixture.cases.filter { !$0.name.hasPrefix("concern_") && $0.name != "parked_unread_pr_open" }
        XCTAssertFalse(supported.isEmpty)
        return supported
    }
}
