import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// The current assistant on a hub with two, as the app's roster and pick give it.
@MainActor @Observable
private final class TodoAssistantPick {
    var current = "work"
    var scope: AssistantScope { AssistantScope(group: "", current: current, defaultID: "home", installed: ["home", "work"]) }
}

/// An Agent's session as the app holds it: watched, as `AppModel.session(_:)`'s cell is.
@MainActor @Observable
private final class TodoSessionCell {
    var state: AgentState = .working
    func session(_ id: String) -> Session? {
        let json = #"{"machine_id":"m","session_id":"\#(id)","title":"\#(id)","cwd":"/tmp","status":"idle","state":"\#(state.rawValue)"}"#
        return try? JSON.decoder.decode(Session.self, from: Data(json.utf8))
    }
}

/// One number per place (#10465, docs/apple/ios.md "Filing and counts"): with one manager the hub's counts and places as
/// sent; with several, counted from the current assistant's rows, with only its own project places; less the answers
/// given on this device that the hub's view does not show yet.
@MainActor
struct TodoCountsSourceTests {
    @MainActor private final class Flag { var on = false }

    private static let places = #"""
    {"places":[{"id":"all","kind":"all","title":"","needs_you":3},{"id":"today","kind":"today","title":"","needs_you":0},
    {"id":"upcoming","kind":"upcoming","title":"","needs_you":0},
    {"id":"project:kyoto","kind":"project","title":"Kyoto trip","project":"kyoto","needs_you":1,"manager":"home"},
    {"id":"project:release","kind":"project","title":"Release","project":"release","needs_you":2,"manager":"work"}],
    "counts":{"needs_you":3}}
    """#

    /// Two managers' rows, as the hub merges them: home has one to-do in Needs you, work two (one carries an ask the
    /// tests answer) and one Working; the hub's counts add both up.
    private static func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("h1", extra: #""manager":"home","project":"kyoto""#, view: TodoViewJSON.view(TodoListSection.needsYou)),
            TodoViewJSON.row("w1", extra: #""manager":"work","project":"release""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["todo:decision@1"])),
            TodoViewJSON.row("w2", extra: #""manager":"work","project":"release""#, view: TodoViewJSON.view(TodoListSection.needsYou, rank: 1)),
            TodoViewJSON.row("w3", extra: #""manager":"work""#, view: TodoViewJSON.view(TodoListSection.working)),
        ], counts: TodoViewJSON.counts(needsYou: 3, inProgress: 1, open: 4,
                                       sections: [TodoListSection.needsYou: 3, TodoListSection.working: 1])))
        hub.on("GET", "/api/v1/todos/places", json: places)
        return hub
    }

    /// One manager: the numbers are the hub's as sent, its places too, All carrying N.
    @Test func oneManagerShowsTheHubsNumbers() async throws {
        let hub = Self.hub()
        // The hub's counts differ from what the rows would give: they are the ones shown.
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.todo))],
                                                              counts: TodoViewJSON.counts(needsYou: 5, open: 9)))
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        await store.refresh(api: hub.client)
        #expect(store.countsSource.needsYou == 5 && store.countsSource.counts.open == 9)
        #expect(store.countsSource.places.map(\.id) == ["all", "today", "upcoming", "project:kyoto", "project:release"])
        #expect(store.countsSource.places.first?.needsYou == 5, "All carries N")
        #expect(store.countsSource.places.last?.needsYou == 2)
    }

    /// Several managers: the hub adds both up, the pages list the current assistant's: N is counted from its rows, and
    /// only its own project places are shown, each with its part of N.
    @Test func twoManagersCountTheCurrentAssistantsRows() async throws {
        let hub = Self.hub()
        let pick = TodoAssistantPick()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {},
                              managers: .init(scope: { pick.scope }, naming: { $0 }), v1: .on())
        defer { store.forget() }
        await store.refresh(api: hub.client)
        let source = store.countsSource
        #expect(source.needsYou == 2 && source.counts.inProgress == 1 && source.counts.open == 3)
        #expect(source.places.map(\.id) == ["all", "today", "upcoming", "project:release"])
        #expect(source.places.first?.needsYou == 2 && source.places.last?.needsYou == 2)

        pick.current = "home"
        try await waitForCount { source.needsYou == 1 }
        #expect(source.places.map(\.id) == ["all", "today", "upcoming", "project:kyoto"], "switched: the other assistant's")
    }

    /// An answer given here takes the to-do out of N at once, everywhere; undone, it is back; once the hub's view no
    /// longer lists the ask, the hub's number stands.
    @Test func anAnswerHereMovesEveryNumberAtOnce() async throws {
        let hub = Self.hub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        await store.refresh(api: hub.client)
        let source = store.countsSource
        #expect(source.needsYou == 3)
        source.answered("todo:decision@1", of: "w1")
        #expect(source.needsYou == 2 && source.counts.sections[TodoListSection.needsYou] == 2)
        #expect(source.places.first?.needsYou == 2 && source.places.last?.needsYou == 1, "All and the project with it")
        source.unanswered("todo:decision@1", of: "w1")
        #expect(source.needsYou == 3)
        source.answered("todo:decision@1", of: "w1")
        source.answered("an ask it does not have", of: "w2")
        #expect(source.needsYou == 2, "a to-do counts as answered only when every ask of its is")
        // The hub took the answer: its view lists the ask no more, and its counts say so.
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("w1", extra: #""project":"release""#, view: TodoViewJSON.view(TodoListSection.working)),
            TodoViewJSON.row("w2", extra: #""project":"release""#, view: TodoViewJSON.view(TodoListSection.needsYou)),
        ], counts: TodoViewJSON.counts(needsYou: 1, inProgress: 1, open: 2)))
        await store.refresh(api: hub.client)
        #expect(source.needsYou == 1, "the hub's number, nothing taken off twice")
    }

    /// Each number is set only when it changes: a read that moves none draws nothing that reads them.
    @Test func aReadThatMovesNoNumberChangesNothing() async throws {
        let hub = Self.hub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        await store.refresh(api: hub.client)
        let changed = Flag()
        withObservationTracking { _ = store.countsSource.needsYou; _ = store.countsSource.places } onChange: {
            MainActor.assumeIsolated { changed.on = true }
        }
        await store.refresh(api: hub.client)
        #expect(!changed.on)
    }

    /// The rows are filed once, for the list and the numbers alike: the current assistant's, in the list's order.
    @Test func theListAndTheNumbersShareOneFiling() async throws {
        let hub = Self.hub()
        let pick = TodoAssistantPick()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {},
                              managers: .init(scope: { pick.scope }, naming: { $0 }), v1: .on())
        defer { store.forget() }
        await store.refresh(api: hub.client)
        let source = store.countsSource
        #expect(source.filed.map(\.id) == ["w1", "w2", "w3"], "work's, Needs you by rank, then Working")
        #expect(source.filed.filter { $0.section == TodoListSection.needsYou }.count == source.needsYou)
        #expect(!source.byRules)

        let one = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { one.forget() }
        await one.refresh(api: hub.client)
        #expect(one.countsSource.filed.map(\.id) == ["h1", "w1", "w2", "w3"], "one manager, the hub's counts: filed all the same")
    }

    /// A row filed by the app's own rules (no view this build can read) is filed again when its Agent's session changes,
    /// without a read: an older manager sends no push for it.
    @Test func aRowFiledByTheRulesFollowsItsSessions() async throws {
        let cell = TodoSessionCell()
        let source = TodoCountsSource(scope: { nil }, lookup: { rows in
            TodosLogic.makeSessionLookup(rows.flatMap { $0.agents.compactMap { cell.session($0.sessionID) } })
        })
        let row = TodoItem(id: "t", title: "Older manager's", createdAt: Date(), updatedAt: Date(),
                           agents: [TodoItem.Agent(machineID: "m", sessionID: "s1")])
        source.update(.init(rows: [row], now: Date()))
        #expect(source.byRules && source.filed.first?.section == TodoListSection.working && source.needsYou == 0)
        cell.state = .needsYou
        try await waitForCount { source.needsYou == 1 }
        #expect(source.filed.first?.section == TodoListSection.needsYou)
        cell.state = .working
        try await waitForCount { source.needsYou == 0 }
    }

    private func waitForCount(_ condition: @MainActor () -> Bool) async throws {
        for _ in 0..<200 where !condition() { try await Task.sleep(for: .milliseconds(10)) }
        try #require(condition(), "not counted again")
    }
}
