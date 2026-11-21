import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The counts the app works out from filed rows (`TodoCountsFallback`, for several managers and for hubs that send none)
/// answer every case of spec/fixtures/todo-counts as the hub's projection does: each case's rows are filed in the
/// sections its `expected.order` gives, closed when a row with `done_at` is in Just finished or Done (a failed start
/// elsewhere is open), and their counts and the projects' parts of N are compared with the expected ones.
struct TodoCountsFallbackTests {
    private static let file = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .deletingLastPathComponent().appending(path: "spec/fixtures/todo-counts/todo_counts.json")

    private struct Case: Decodable {
        struct Input: Decodable { var todos: [TodoItem] }
        struct Expected: Decodable {
            var counts: TodoCounts
            var places: TodoPlaces
            var order: [String: [String]]?
        }
        var name: String
        var input: Input
        var expected: Expected
    }

    private static func cases() throws -> [Case] {
        struct Root: Decodable { var cases: [Case] }
        return try JSON.decoder.decode(Root.self, from: Data(contentsOf: file)).cases
    }

    private static func filed(_ c: Case) throws -> [TodoFiled] {
        var section: [String: String] = [:]
        for (s, ids) in c.expected.order ?? [:] { for id in ids { section[id] = s } }
        return try c.input.todos.map { row in
            let s = try #require(section[row.id], "\(c.name): \(row.id) has no section in expected.order")
            let closed = row.doneAt != nil && (s == TodoListSection.justFinished || s == TodoListSection.done)
            return TodoFiled(id: row.id, manager: row.manager, section: s, closed: closed, rank: 0, inactive: false, byView: true,
                             quiet: row.quiet, partOf: row.partOf, project: row.project, asks: [])
        }
    }

    @Test func everyCaseCountsAsTheHubDoes() throws {
        let cases = try Self.cases()
        #expect(cases.count >= 8)
        for c in cases {
            let rows = try Self.filed(c)
            #expect(TodoCountsFallback.counts(rows) == c.expected.counts, "\(c.name)")
            let parts = TodoCountsFallback.needsYouByProject(rows)
            for place in c.expected.places.places where place.kind == TodoPlaceKind.project {
                #expect(parts[place.project ?? ""] ?? 0 == place.needsYou, "\(c.name): \(place.id)")
            }
            let all = c.expected.places.places.first { $0.kind == TodoPlaceKind.allValue }
            #expect(all?.needsYou == TodoCountsFallback.counts(rows).needsYou, "\(c.name): All carries N")
        }
    }

    /// A step its open parent carries counts with its parent only; under a closed parent it counts as the row it is.
    @Test func aStepCountsOnceWithItsParent() {
        func row(_ id: String, _ section: String, closed: Bool = false, partOf: String? = nil, quiet: Bool = false) -> TodoFiled {
            TodoFiled(id: id, section: section, closed: closed, rank: 0, inactive: false, byView: true, quiet: quiet, partOf: partOf,
                      project: nil, asks: [])
        }
        let open = [row("p", TodoListSection.needsYou), row("s", TodoListSection.needsYou, partOf: "p")]
        #expect(TodoCountsFallback.counts(open).needsYou == 1 && TodoCountsFallback.carried(open) == ["s"])
        let closedParent = [row("p", TodoListSection.done, closed: true), row("s", TodoListSection.needsYou, partOf: "p")]
        #expect(TodoCountsFallback.counts(closedParent).needsYou == 1 && TodoCountsFallback.carried(closedParent).isEmpty)
    }

    /// An outside decision's association is not a parent step: neither an open nor a quiet parent can take its row or count.
    @Test func anOutsideDecisionCountsIndependentlyOfItsParent() {
        func row(_ id: String, _ section: String, partOf: String? = nil, quiet: Bool = false) -> TodoFiled {
            TodoFiled(id: id, section: section, closed: false, rank: 0, inactive: false, byView: true, quiet: quiet, partOf: partOf,
                      project: nil, asks: [])
        }
        let open = [row("p", TodoListSection.needsYou), row("todo_od_1", TodoListSection.needsYou, partOf: "p")]
        #expect(TodoCountsFallback.counts(open).needsYou == 2 && TodoCountsFallback.carried(open).isEmpty)
        let quietParent = [row("p", TodoListSection.needsYou, quiet: true), row("todo_od_1", TodoListSection.needsYou, partOf: "p")]
        #expect(TodoCountsFallback.counts(quietParent).needsYou == 1 && TodoCountsFallback.carried(quietParent).isEmpty)
    }
}
