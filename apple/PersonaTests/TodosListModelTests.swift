import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// The current assistant on a hub with two, as the app's roster and pick give it.
@MainActor @Observable
private final class TodoListPick {
    var current = "work"
    var scope: AssistantScope { AssistantScope(group: "", current: current, defaultID: "home", installed: ["home", "work"]) }
}

/// The test's clock: the list's minute moves when a test moves it.
@MainActor private final class TodoListClock {
    var now = Date(timeIntervalSince1970: 1_800_000_000)
}

/// The V1 To-dos list's model (#10465) against a `FakeHub` through a store of its own: the rows as the hub files them,
/// a 304, rows from an older manager filed by the app's rules, the hub's order, the calm rule, two managers, and rows
/// that never move under a finger while the numbers never wait.
@MainActor
struct TodosListModelTests {
    private let clock = TodoListClock()

    private func model(_ store: TodoStore, hub: FakeHub) -> TodosListModel {
        let clock = clock
        return TodosListModel(deps: TodosListDeps(store: store, api: { hub.client }, now: { clock.now },
                                                  calendar: { Calendar(identifier: .gregorian) },
                                                  projects: { [TodoProject(slug: "kyoto", name: "Kyoto trip", long: false)] }))
    }

    private static func list(_ rows: [String], needsYou: Int = 1, inProgress: Int = 1) -> String {
        TodoViewJSON.list(rows, counts: TodoViewJSON.counts(needsYou: needsYou, inProgress: inProgress, open: 3,
                                                            sections: [TodoListSection.needsYou: needsYou, TodoListSection.working: inProgress]))
    }

    private static let places = #"{"places":[{"id":"all","kind":"all","title":"","needs_you":1}],"counts":{}}"#

    @Test func rowsComeAsTheHubFilesThem() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list([
            TodoViewJSON.row("t01", title: "Plan the Kyoto trip", extra: #""project":"kyoto""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["todo:decision@1"], line: "decision")),
            TodoViewJSON.row("t05", view: TodoViewJSON.view(TodoListSection.working)),
            TodoViewJSON.row("t11", view: TodoViewJSON.view(TodoListSection.todo, line: "rest")),
        ]))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        #expect(!list.ready, "nothing drawn before the first read")
        await store.refresh(api: hub.client)
        try await waitForListTest { list.ready && !list.groups.isEmpty }
        #expect(list.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working, TodoListSection.todo])
        #expect(list.entries.map(\.choice) == [.all, .needsYou, .inProgress])
        #expect(list.needsYou == 1)
        let kyoto = try #require(list.row("/t01")?.content)
        #expect(kyoto.title == "Plan the Kyoto trip" && kyoto.mark == .needs && kyoto.door == .review)
        #expect(list.row("/t05")?.content.mark == .working)
        #expect(!list.calm)
    }

    /// A 304 keeps the list as the tagged read had it; a row from an older manager is filed by the app's own rules.
    @Test func aListThatDidNotChangeAndRowsWithoutAView() async throws {
        var answers = [TodosPage(list: [TodoItem(id: "a", title: "A"), TodoItem(id: "old", title: "Old", notes: "Buy milk\nand eggs")],
                                 views: ["a": TodoSummary(phase: TodoPhase.working, section: TodoListSection.working,
                                                          reason: TodoReason.agentRunning, rank: 0)],
                                 counts: nil, etag: "t1"),
                       TodosPage(list: nil, etag: "t1")]
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, read: { _, _ in answers.removeFirst() }, v1: .on())
        defer { store.forget() }
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.groups.count == 2 }
        #expect(list.groups.map(\.id) == [TodoListSection.working, TodoListSection.todo])
        #expect(list.row("/old")?.content.said?.rest == "Buy milk", "a plain To do says its notes' first line")
        let before = list.layout
        await store.refresh(api: hub.client)
        #expect(answers.isEmpty && list.layout == before)
    }

    /// While only plain To do rows exist and nothing needs the owner, no bar and no headers.
    @Test func theCalmList() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list([TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.todo, line: "rest")),
                                                       TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.todo, rank: 1, line: "rest"))],
                                                      needsYou: 0, inProgress: 0))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.calm }
        #expect(list.groups.count == 1 && list.groups[0].title == nil && list.groups[0].keys == ["/a", "/b"])
    }

    /// Two managers: the list is the current assistant's rows, in the hub's order (rank, then manager, then id).
    @Test func twoManagersListTheCurrentAssistantsRows() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list([
            TodoViewJSON.row("h1", extra: #""manager":"home""#, view: TodoViewJSON.view(TodoListSection.working)),
            TodoViewJSON.row("w2", extra: #""manager":"work""#, view: TodoViewJSON.view(TodoListSection.working, rank: 1)),
            TodoViewJSON.row("w1", extra: #""manager":"work""#, view: TodoViewJSON.view(TodoListSection.working)),
        ]))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let pick = TodoListPick()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, managers: .init(scope: { pick.scope }, naming: { $0 }),
                              v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.groups.first?.keys.count == 2 }
        #expect(list.groups.first?.keys == ["work/w1", "work/w2"])
        pick.current = "home"
        try await waitForListTest { list.groups.first?.keys == ["home/h1"] }
    }

    /// Rows never move under a finger: while the list is scrolled a new filing waits, and so does what a row says (a
    /// new line can change its height); the numbers change at once. A row that comes to need the owner lands at once,
    /// alone, saying what it says now. Once the list rests the rest lands, the moved rows fading in.
    @Test func aNewFilingWaitsForTheListToRest() async throws {
        let hub = FakeHub()
        let first = Self.list([TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.working, line: "working")),
                               TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.todo, line: "rest"))], needsYou: 0)
        hub.on("GET", "/api/v1/todos", json: first)
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.groups.count == 2 }
        list.hold.set(.scrolling, true)
        // a moves to Waiting, with a line of its own: its words and its place wait for the list to rest.
        hub.on("GET", "/api/v1/todos", json: Self.list([
            TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.waiting, line: "pr_open")),
            TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.todo, line: "rest"))], needsYou: 0, inProgress: 4))
        await store.refresh(api: hub.client)
        try await waitForListTest { list.entries.first { $0.choice == .inProgress }?.count == 4 } // the numbers never wait
        // Held: a proof that something did not happen needs a moment to have happened in; the store has told the list.
        try await Task.sleep(for: .milliseconds(100))
        #expect(list.groups.map(\.id) == [TodoListSection.working, TodoListSection.todo], "held while scrolled")
        #expect(list.row("/a")?.content.mark == .working, "what it says waits too")
        // b comes to need the owner: that lands at once, alone; a's move and words still wait.
        hub.on("GET", "/api/v1/todos", json: Self.list([
            TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.waiting, line: "pr_open")),
            TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["todo:decision@1"], line: "decision"))]))
        await store.refresh(api: hub.client)
        try await waitForListTest { list.groups.first?.id == TodoListSection.needsYou }
        #expect(list.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working])
        #expect(list.row("/b")?.content.mark == .needs && list.row("/b")?.content.door == .review)
        #expect(list.row("/a")?.content.mark == .working)
        let arrived = try #require(list.row("/a")?.arrivals)
        // The list rests: the rest lands, a fading in where it now is.
        list.hold.set(.scrolling, false)
        try await waitForListTest { list.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.waiting] }
        #expect(list.row("/a")?.content.mark == .waiting)
        #expect(list.row("/a")?.arrivals == arrived + 1, "a re-filed row fades in")
        #expect(list.row("/b")?.arrivals == 1, "b faded in once, when it came to need the owner")
        // In another group the row is a new view, drawn hidden from its first frame until it fades in as it appears.
        let a = try #require(list.row("/a"))
        #expect(a.entering, "a waits for its view to fade it in where it now is")
        #expect(list.row("/b")?.entering == false, "b, landed earlier and not drawn since, is shown as it is at this landing")
        list.settleArrivals() // the page reports a scroll
        #expect(!a.entering, "a row that landed out of sight never fades in later, under a finger")
    }

    /// A row gone from the store while the list is held keeps its place and its words until the list rests: the rows
    /// under it never jump up mid-glide.
    @Test func aRowGoneWhileHeldStaysUntilItLands() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list([TodoViewJSON.row("a", view: TodoViewJSON.view(TodoListSection.working)),
                                                       TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.working, rank: 1))]))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.groups.first?.keys == ["/a", "/b"] }
        list.hold.set(.scrolling, true)
        hub.on("GET", "/api/v1/todos", json: Self.list([TodoViewJSON.row("b", view: TodoViewJSON.view(TodoListSection.working, rank: 1))]))
        await store.refresh(api: hub.client)
        try await waitForListTest { store.byID["a"] == nil }
        try await Task.sleep(for: .milliseconds(100)) // the list was told; held, nothing moved
        #expect(list.groups.first?.keys == ["/a", "/b"] && list.row("/a") != nil, "kept, with its cell, while held")
        list.hold.set(.scrolling, false)
        try await waitForListTest { list.groups.first?.keys == ["/b"] }
        #expect(list.row("/a") == nil)
    }

    /// The page's minute: the rows' times are said again each minute, and nothing within the minute they were said in.
    @Test func timesAreSaidAgainEachMinute() async throws {
        let hub = FakeHub()
        let at = clock.now.addingTimeInterval(-16 * 60).formatted(.iso8601)
        hub.on("GET", "/api/v1/todos", json: Self.list([
            TodoViewJSON.row("a", extra: #""updated_at":"\#(at)""#, view: TodoViewJSON.view(TodoListSection.working))]))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.row("/a") != nil }
        let before = try #require(list.row("/a")?.content)
        clock.now = clock.now.addingTimeInterval(20)
        list.tick()
        #expect(list.row("/a")?.content == before, "the same minute: nothing said again")
        clock.now = clock.now.addingTimeInterval(120)
        list.tick()
        let next = try #require(list.row("/a")?.content)
        #expect(next.trailing != before.trailing, "said from the new minute")
        // Held: the minute is said once the list rests, not mid-glide.
        list.hold.set(.scrolling, true)
        clock.now = clock.now.addingTimeInterval(120)
        list.tick()
        #expect(list.row("/a")?.content == next, "held while scrolled")
        list.hold.set(.scrolling, false)
        try await waitForListTest { list.row("/a")?.content.trailing != next.trailing }
    }

    /// The bar's choice, the grouping and the sort are the owner's taps: they land at once, folds too.
    @Test func choicesAndFoldsAreNeverHeld() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list([
            TodoViewJSON.row("n", view: TodoViewJSON.view(TodoListSection.needsYou, line: "decision")),
            TodoViewJSON.row("c", view: TodoViewJSON.view(TodoListSection.canWait, line: "awaits_reply")),
            TodoViewJSON.row("w", view: TodoViewJSON.view(TodoListSection.working))]))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = model(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.groups.count == 2 }
        list.hold.set(.scrolling, true)
        list.choice = .needsYou
        #expect(list.groups.map(\.id) == [TodoListSection.needsYou])
        #expect(list.groups[0].folds.first?.open == false)
        list.toggle(TodoListSection.canWait)
        #expect(list.groups[0].folds.first?.open == true && list.unfolded == [TodoListSection.canWait])
        list.choice = .all
        list.grouping = .project
        #expect(list.groups.map(\.id) == [TodoListSection.needsYou, "project:"])
    }

    /// The minute clock for rows filed by the app's own rules follows whatever made one current: another assistant made
    /// current files its rows, and the clock starts without waiting for the next read.
    @Test func switchingAssistantsStartsTheMinuteClock() async throws {
        let hub = FakeHub()
        // home's row has a view; work's is an older manager's, filed by the app's rules.
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("h1", extra: #""manager":"home""#, view: TodoViewJSON.view(TodoListSection.todo, line: "rest")),
            TodoViewJSON.row("w1", extra: #""manager":"work""#)]))
        hub.on("GET", "/api/v1/todos/places", json: Self.places)
        let pick = TodoListPick()
        pick.current = "home"
        var ticks = 0
        var v1 = TodoStore.V1.on()
        v1.now = { ticks += 1; return Date() }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, managers: .init(scope: { pick.scope }, naming: { $0 }),
                              v1: v1)
        defer { store.forget() }
        await store.refresh(api: hub.client)
        #expect(!store.countsSource.byRules && !store.rulesClockRuns, "no clock while every counted row has a view")
        pick.current = "work"
        try await waitForListTest { store.countsSource.byRules && store.rulesClockRuns }
        let started = ticks
        try await waitForListTest { ticks >= started + 3 } // the test's clock ticks each 0.1 s, counting again each time
        pick.current = "home"
        try await waitForListTest { !store.countsSource.byRules && !store.rulesClockRuns }
    }
}

/// Waits (up to 5 s) for something the store or the hold does.
@MainActor
private func waitForListTest(_ condition: @MainActor () -> Bool) async throws {
    for _ in 0..<250 where !condition() { try await Task.sleep(for: .milliseconds(20)) }
    try #require(condition(), "it did not happen in time")
}
