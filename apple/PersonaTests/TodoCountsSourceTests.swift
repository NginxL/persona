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

/// The list's writes still on the device, as `TodoOutbox.pending` gives them: watched, as the outbox's entries are.
@MainActor @Observable
private final class TodoPendingWrites {
    var value: [String: TodoCountsSource.Pending] = [:]
}

/// One number per place (#10465, docs/apple/ios.md "Filing and counts"): with one manager the hub's counts and places as
/// sent, each project's part counted from the rows as its place counts them; with several, counted from the current
/// assistant's rows, with only its own project places; less the answers given on this device that the hub's view does
/// not show yet.
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

    /// One manager: the numbers are the hub's as sent, its places too, All carrying N; a project's part is its rows',
    /// as its place's Filter and Needs you header say it.
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
        #expect(store.countsSource.places.last?.needsYou == 0, "the hub says 2, but no row of it needs the owner")
        await store.refresh(api: Self.hub().client)
        #expect(store.countsSource.places.last?.needsYou == 2, "the release's two rows in Needs you")
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

    /// The owner's writes still on the device (`TodoOutbox`) move the rows and every number at once, with the hub's
    /// counts as with counts from the rows: a deleted to-do leaves, one put in Someday is filed there, a new one is a To
    /// do row first in its section; and back when the writes go.
    @Test func writesStillOnTheDeviceMoveTheNumbers() async throws {
        let pending = TodoPendingWrites()
        let source = TodoCountsSource(scope: { nil }, pending: { pending.value })
        let rows = [TodoItem(id: "n", title: "N"), TodoItem(id: "a", title: "A"), TodoItem(id: "w", title: "W")]
        let views = ["n": TodoSummary(phase: TodoPhase.needsYou, section: TodoListSection.needsYou, reason: "decision"),
                     "a": TodoSummary(phase: TodoPhase.todo, section: TodoListSection.todo, reason: "rest"),
                     "w": TodoSummary(phase: TodoPhase.working, section: TodoListSection.working, reason: "persona_working")]
        let hubCounts = TodoCounts(open: 3, needsYou: 1, inProgress: 1,
                                   sections: [TodoListSection.needsYou: 1, TodoListSection.todo: 1, TodoListSection.working: 1])
        for counts in [hubCounts, nil] {
            pending.value = [:]
            source.update(.init(rows: rows, views: views, counts: counts, now: Date()))
            try await waitForCount { source.counts.open == 3 }
            #expect(source.needsYou == 1 && source.counts.inProgress == 1)
            pending.value = ["n": .someday, "w": .gone, "x": .added(TodoItem(id: "x", title: "X", createdAt: Date()))]
            try await waitForCount { source.needsYou == 0 }
            #expect(source.counts.inProgress == 0, "the deleted one is out of every number")
            #expect(source.counts.sections[TodoListSection.someday] == 1 && source.counts.sections[TodoListSection.todo] == 2)
            #expect(source.counts.open == 2, "Someday is not open; the new one is")
            #expect(source.filed.map(\.id) == ["x", "a", "n"], "the new one first in To do, then Someday")
            pending.value = [:]
            try await waitForCount { source.needsYou == 1 && source.counts.open == 3 }
        }
    }

    /// A write still on the device on a Needs-you row of a project (deleted, or put in Someday) comes off that project's
    /// part and off All's N once each, with the hub's counts and places (one manager): the places count each part from
    /// the rows with the writes applied, so nothing takes it off a second time. Both come back when the write goes.
    @Test func aWriteInAProjectComesOffItsPartOnce() async throws {
        let pending = TodoPendingWrites()
        let source = TodoCountsSource(scope: { nil }, pending: { pending.value })
        let rows = [TodoItem(id: "k1", title: "K1", project: "kyoto"), TodoItem(id: "k2", title: "K2", project: "kyoto"),
                    TodoItem(id: "r1", title: "R1", project: "release")]
        let needs = TodoSummary(phase: TodoPhase.needsYou, section: TodoListSection.needsYou, reason: "decision")
        let hubCounts = TodoCounts(open: 3, needsYou: 3, sections: [TodoListSection.needsYou: 3])
        let places = try JSON.decoder.decode(TodoPlaces.self, from: Data(Self.places.utf8))
        source.update(.init(rows: rows, views: ["k1": needs, "k2": needs, "r1": needs], counts: hubCounts, places: places, now: Date()))
        let part = { (slug: String) in source.places.first { $0.id == "project:" + slug }?.needsYou }
        try await waitForCount { source.needsYou == 3 && part("kyoto") == 2 }
        for write in [TodoCountsSource.Pending.gone, .someday] {
            pending.value = ["k1": write]
            try await waitForCount { source.needsYou == 2 }
            #expect(part("kyoto") == 1 && part("release") == 1, "\(write): off its project's part once")
            #expect(source.places.first?.needsYou == 2, "\(write): and off All's N once")
            pending.value = [:]
            try await waitForCount { source.needsYou == 3 }
            #expect(part("kyoto") == 2 && source.places.first?.needsYou == 3)
        }
    }
}
