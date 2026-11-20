import Foundation
import Testing
@testable import Persona
import PersonaWire

/// Where the V1 list files each to-do (#10465, docs/apple/ios.md "Filing and counts"): by its view, else by the app's own
/// rules mapped onto the V1 sections (`TodoFiling`); and the list's order within a section, `rank`, then `manager`, then
/// `id` (docs/protocol/protocol.md, the `GET /api/v1/todos` row).
struct TodoFilingTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func ago(_ hours: Double) -> Date { now.addingTimeInterval(-hours * 3600) }

    private func session(_ id: String, state: AgentState) throws -> Session {
        let json = #"{"machine_id":"m","session_id":"\#(id)","title":"\#(id)","cwd":"/tmp","status":"idle","state":"\#(state.rawValue)"}"#
        return try JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }

    private func view(_ section: String, phase: String? = nil, rank: Int = 0, inactive: String? = nil, asks: [String] = []) -> TodoSummary {
        TodoSummary(phase: phase ?? section, section: section, reason: TodoReason.rest, rank: rank, inactive: inactive,
                    asks: asks.map { OwnerAsk(id: $0, kind: OwnerAskKind.decision, home: OwnerAskHome.todo) })
    }

    @Test func aRowIsFiledByItsView() {
        let rows = [TodoItem(id: "a", title: "A", quiet: true, manager: "work", project: "kyoto", partOf: "p"),
                    TodoItem(id: "b", title: "B", doneAt: ago(1)), TodoItem(id: "c", title: "C")]
        let views = ["a": view(TodoListSection.needsYou, rank: 3, asks: ["todo:decision@1"]),
                     "b": view(TodoListSection.justFinished, phase: TodoPhase.done, rank: 1),
                     "c": view(TodoListSection.todo, inactive: "2026-11-10T00:00:00Z")]
        let filed = TodoFiling.file(rows, views: views, now: now, lookup: { Issue.record("no row needs the app's rules"); return nil })
        let sections = filed.map { $0.section }
        #expect(sections == [TodoListSection.needsYou, TodoListSection.justFinished, TodoListSection.todo])
        #expect(filed.allSatisfy { $0.byView })
        #expect(filed[0] == TodoFiled(id: "a", manager: "work", section: TodoListSection.needsYou, closed: false, rank: 3,
                                      inactive: false, byView: true, quiet: true, partOf: "p", project: "kyoto",
                                      asks: ["todo:decision@1"]))
        #expect(filed[1].closed && !filed[2].closed && filed[2].inactive)
    }

    /// A row without a view (an older manager's), or with a section this build does not know, is filed by the app's
    /// own rules, each of their sections mapped onto V1's.
    @Test func aRowWithoutAViewThisBuildCanFileByTakesTheLegacyMap() throws {
        let working = try session("s1", state: .working)
        let rows = [
            TodoItem(id: "needs", title: "Failed start", doneAt: ago(1), updatedAt: ago(1), startError: "the computer is offline"),
            TodoItem(id: "work", title: "Working", updatedAt: ago(1), agents: [TodoItem.Agent(machineID: "m", sessionID: "s1")]),
            TodoItem(id: "pr", title: "Pull request", updatedAt: ago(1), origin: "conv", prURL: "https://example.com/pr/1"),
            TodoItem(id: "rest", title: "Errand", createdAt: ago(1)),
            TodoItem(id: "fresh", title: "Closed an hour ago", doneAt: ago(1)),
            TodoItem(id: "shelved", title: "Untouched task", createdAt: ago(72), updatedAt: ago(72), origin: "conv"),
            TodoItem(id: "old", title: "Closed days ago", doneAt: ago(72)),
            TodoItem(id: "unknown", title: "A newer section", createdAt: ago(1)),
        ]
        let views = ["unknown": view("later_maybe")]
        var looked = 0
        let filed = TodoFiling.file(rows, views: views, now: now, lookup: {
            looked += 1
            return TodosLogic.makeSessionLookup([working])
        })
        #expect(looked == 1, "the sessions are read once, only for the rows that need them")
        let by = Dictionary(uniqueKeysWithValues: filed.map { ($0.id, $0) })
        #expect(by["needs"]?.section == TodoListSection.needsYou && by["needs"]?.closed == false, "a failed start is open")
        #expect(by["work"]?.section == TodoListSection.working)
        #expect(by["pr"]?.section == TodoListSection.waiting)
        #expect(by["rest"]?.section == TodoListSection.todo && by["rest"]?.inactive == false)
        #expect(by["fresh"]?.section == TodoListSection.justFinished && by["fresh"]?.closed == true)
        #expect(by["shelved"]?.section == TodoListSection.todo && by["shelved"]?.inactive == true, "Shelved is the Inactive mark")
        #expect(by["old"]?.section == TodoListSection.done && by["old"]?.closed == true)
        #expect(by["unknown"]?.section == TodoListSection.todo)
        #expect(filed.allSatisfy { !$0.byView && $0.asks.isEmpty })
        for legacy in TodoSection.allCases {
            #expect(TodoListSection.all.contains(TodoFiling.section(legacy: legacy).section))
        }
    }

    /// A restore kept on the device, or the hub's own, counts as the row moving: an untouched task is not Inactive.
    @Test func aRestoreTakesTheInactiveMarkOff() {
        let row = TodoItem(id: "t", title: "Task", createdAt: ago(72), updatedAt: ago(72), origin: "conv")
        #expect(TodoFiling.file([row], views: [:], now: now)[0].inactive)
        #expect(!TodoFiling.file([row], views: [:], now: now, restored: ["t": ago(1)])[0].inactive)
        var restored = row
        restored.restoredAt = ago(1)
        #expect(!TodoFiling.file([restored], views: [:], now: now)[0].inactive)
    }

    /// Within a section: `rank`, then `manager`, then `id` (ranks are per manager: a merged section can hold equal ones);
    /// sections in the list's order. Rows the app's rules filed are ranked by when they last moved, newest first, among
    /// their manager's.
    @Test func theListsOrder() {
        let rows = [TodoItem(id: "z", title: "Z", manager: "work"), TodoItem(id: "y", title: "Y", manager: "work"),
                    TodoItem(id: "x", title: "X", manager: "home"), TodoItem(id: "n", title: "N", manager: "home"),
                    TodoItem(id: "old", title: "Old", createdAt: ago(5), manager: "home"),
                    TodoItem(id: "new", title: "New", createdAt: ago(2), manager: "home")]
        let views = ["z": view(TodoListSection.working, rank: 1), "y": view(TodoListSection.working, rank: 0),
                     "x": view(TodoListSection.working, rank: 0), "n": view(TodoListSection.needsYou, rank: 4)]
        let filed = TodoFiling.file(rows, views: views, now: now)
        let ranks = Dictionary(uniqueKeysWithValues: filed.map { ($0.id, $0.rank) })
        #expect(ranks["new"] == 0 && ranks["old"] == 1)
        let order = TodoFiling.ordered(filed).map { $0.id }
        #expect(order == ["n", "x", "y", "z", "new", "old"])
    }
}
