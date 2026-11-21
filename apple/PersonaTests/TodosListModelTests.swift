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

/// What the list said in its toasts and errors, and what its outbox said was turned down.
@MainActor private final class TodoListToasts {
    var shown: [Toast] = []
    var errors: [String] = []
    var refusals: [String] = []
}

/// The outbox the store's numbers follow (`TodoStore.V1.pending`), made after the store it writes through.
@MainActor @Observable private final class TodoListOutboxBox {
    var outbox: TodoOutbox?
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

    private func model(_ store: TodoStore, hub: FakeHub, outbox: TodoOutbox? = nil, toasts: TodoListToasts? = nil) -> TodosListModel {
        let clock = clock
        return TodosListModel(deps: TodosListDeps(store: store, outbox: outbox ?? Self.outbox(store, hub: hub), api: { hub.client },
                                                  now: { clock.now }, calendar: { Calendar(identifier: .gregorian) },
                                                  projects: { [TodoProject(slug: "kyoto", name: "Kyoto trip", long: false)] },
                                                  toast: { toasts?.shown.append($0) }, show: { toasts?.errors.append($0.localizedDescription) },
                                                  undoWindow: .milliseconds(300)))
    }

    /// An outbox of the test's own, writing through `store` to `hub` (one hub-level group, "g"), kept in memory.
    static func outbox(_ store: TodoStore, hub: FakeHub, refused: @escaping @MainActor (String) -> Void = { _ in }) -> TodoOutbox {
        TodoOutbox(deps: .init(load: { [] }, save: { _, _ in Task {} }, hub: { "g" },
                               send: { entry, answered in await store.send(entry, answered: answered, api: hub.client) },
                               refused: { _, message in refused(message) },
                               standing: { id in
                                   store.byID[id].map { .init(title: $0.title, updatedAt: $0.updatedAt, done: $0.isDone, someday: false) }
                               },
                               listed: { store.todos }))
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

/// The list's writes (#10465: the tick, the swipe, the menu, New To-Do) through a store, an outbox and a `FakeHub`: each
/// is drawn at once, waits out its Undo window on the device, and goes with its op id; Undo within it sends nothing.
@MainActor
struct TodosListWritesTests {
    private let clock = TodoListClock()
    private let toasts = TodoListToasts()

    /// A store whose numbers follow the outbox, its outbox and the list, read once.
    private func list(_ hub: FakeHub) async throws -> (TodoStore, TodoOutbox, TodosListModel) {
        let box = TodoListOutboxBox()
        var v1 = TodoStore.V1.on()
        v1.pending = { box.outbox?.pending ?? [:] }
        v1.answered = { box.outbox?.hubAnswered(read: $0) }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        let toasts = toasts
        let outbox = TodosListModelTests.outbox(store, hub: hub) { toasts.refusals.append($0) }
        await outbox.ready()
        box.outbox = outbox
        let clock = clock
        let list = TodosListModel(deps: TodosListDeps(store: store, outbox: outbox, api: { hub.client }, now: { clock.now },
                                                      calendar: { Calendar(identifier: .gregorian) }, projects: { [] },
                                                      toast: { toasts.shown.append($0) },
                                                      show: { toasts.errors.append($0.localizedDescription) },
                                                      undoWindow: .milliseconds(300)))
        list.start()
        await store.refresh(api: hub.client)
        try await waitForListTest { list.ready && !list.groups.isEmpty }
        return (store, outbox, list)
    }

    private static let places = #"{"places":[{"id":"all","kind":"all","title":"","needs_you":1}],"counts":{}}"#

    private static func hub(_ rows: [String], needsYou: Int = 1, todo: Int = 1) -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: list(rows, needsYou: needsYou, todo: todo))
        hub.on("GET", "/api/v1/todos/places", json: places)
        return hub
    }

    private static func list(_ rows: [String], needsYou: Int = 1, todo: Int = 1) -> String {
        TodoViewJSON.list(rows, counts: TodoViewJSON.counts(needsYou: needsYou, open: needsYou + todo,
                                                            sections: [TodoListSection.needsYou: needsYou, TodoListSection.todo: todo]))
    }

    private static let needs = TodoViewJSON.row("n", title: "Pick a ryokan", view: TodoViewJSON.view(TodoListSection.needsYou, line: "decision"))
    private static let plain = TodoViewJSON.row("a", title: "Renew passport", view: TodoViewJSON.view(TodoListSection.todo, line: "rest"))

    /// The tick: drawn ticked and struck through at once, nothing sent until its Undo window ends, then `PATCH done` with
    /// its op id; the strike goes once the hub's view has it closed.
    @Test func aTickIsDrawnAtOnceAndGoesAfterItsUndo() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        hub.on("PATCH", "/api/v1/todos/a") { _ in
            hub.on("GET", "/api/v1/todos", json: Self.list([Self.needs, TodoViewJSON.row("a", title: "Renew passport",
                view: TodoViewJSON.view(TodoListSection.justFinished, phase: TodoPhase.done, line: "completed"))], todo: 0))
            return Data(#"{"todo":{"id":"a","title":"Renew passport","done_at":"2026-11-18T10:14:00Z"}}"#.utf8)
        }
        #expect(list.tick(of: "/a") == .close)
        list.close("/a")
        try await waitForListTest { list.row("/a")?.content.struck == true } // the next turn: what the outbox holds is drawn
        let row = try #require(list.row("/a"))
        #expect(row.content.struck && row.content.closed, "ticked")
        #expect(toasts.shown.last?.action != nil, "Done · Undo")
        #expect(list.groups.first { $0.id == TodoListSection.todo }?.keys == ["/a"], "it stays where it is meanwhile")
        guard case .takeBack? = list.tick(of: "/a") else { Issue.record("a second tap takes it back"); return }
        #expect(hub.sent("PATCH", "/api/v1/todos/a").isEmpty, "nothing goes within the window")
        try await waitForListTest { !hub.sent("PATCH", "/api/v1/todos/a").isEmpty }
        let patch = try #require(hub.sent("PATCH", "/api/v1/todos/a").first)
        #expect(patch.headers[OpRetry.header] != nil, "with its op id")
        #expect(String(decoding: patch.body ?? Data(), as: UTF8.self).contains(#""done":true"#))
        try await waitForListTest { outbox.entries.isEmpty }
        try await waitForListTest { list.row("/a")?.content.struck == false }
        #expect(list.row("/a")?.content.closed == true, "closed, as the hub's view says")
        #expect(list.groups.contains { $0.id == TodoListSection.justFinished }, "filed where the hub put it")
    }

    @Test func undoWithinTheWindowSendsNothing() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        list.close("/a")
        try await waitForListTest { list.row("/a")?.content.struck == true }
        guard case .takeBack(let id)? = list.tick(of: "/a") else { Issue.record("no tick to take back"); return }
        list.undo(id, of: .done(true))
        try await waitForListTest { list.row("/a")?.content.struck == false }
        #expect(list.row("/a")?.content.closed == false)
        try await Task.sleep(for: .milliseconds(500)) // past the window: nothing may have gone
        #expect(hub.sent("PATCH", "/api/v1/todos/a").isEmpty && outbox.entries.isEmpty)
    }

    /// Persona or an Agent works on it: the tick asks first.
    @Test func aToDoSomeoneWorksOnAsksFirst() async throws {
        let hub = Self.hub([TodoViewJSON.row("w", view: TodoViewJSON.view(TodoListSection.working)), Self.plain], needsYou: 0)
        let (store, _, list) = try await list(hub)
        defer { store.forget() }
        #expect(list.tick(of: "/w") == .askFirst)
        #expect(list.tick(of: "/a") == .close)
    }

    /// An Agent's question on a to-do waits for the owner: its tick asks first too, in words saying so.
    @Test func aToDoAnAgentWaitsOnAsksFirst() async throws {
        let asks = #"{"phase":"needs_you","section":"needs_you","reason":"agent_asks","line":{"key":"agent_asks"},"rank":0,"#
            + #""asks":[{"id":"run:m/s","kind":"question","home":"run","run":{"machine_id":"m","session_id":"s"}}]}"#
        let hub = Self.hub([TodoViewJSON.row("q", title: "Release the app", view: asks), Self.plain])
        let (store, _, list) = try await list(hub)
        defer { store.forget() }
        #expect(list.tick(of: "/q") == .askFirst)
        #expect(list.stopTitle("/q") != list.stopTitle("/a"), "said as an Agent waiting, not Persona working")
        list.tapTick("/q")
        #expect(list.stopping == "/q", "asked from its row")
        #expect(list.tick(of: "/a") == .close)
    }

    /// Someday, then Remove from Someday inside its window: the row is back where it was and nothing reaches the hub.
    @Test func somedayTakenBackWithinItsWindowSendsNothing() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        list.someday("/a")
        try await waitForListTest { list.row("/a")?.content.someday == true }
        list.outOfSomeday("/a")
        try await waitForListTest { list.groups.first { $0.id == TodoListSection.todo }?.keys == ["/a"] }
        try await Task.sleep(for: .milliseconds(500)) // past the window: nothing may have gone
        #expect(hub.sent("PUT", "/api/v1/todos/a/someday").isEmpty && hub.sent("DELETE", "/api/v1/todos/a/someday").isEmpty)
        #expect(outbox.entries.isEmpty)
    }

    /// A delete of a to-do already gone (deleted on another device, or a repeat whose answer was lost) is what it asked
    /// for: the manager's 404 says no error.
    @Test func aDeleteOfAToDoAlreadyGoneSaysNoError() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        hub.on("DELETE", "/api/v1/todos/a") { _ in
            hub.on("GET", "/api/v1/todos", json: Self.list([Self.needs], todo: 0))
            throw APIError(status: 404, message: "todo not found", fields: ["code": "not_found"])
        }
        list.delete("/a")
        try await waitForListTest { !hub.sent("DELETE", "/api/v1/todos/a").isEmpty }
        try await waitForListTest { outbox.entries.isEmpty }
        #expect(toasts.refusals.isEmpty && toasts.errors.isEmpty, "no error: \(toasts.refusals)")
        #expect(!list.groups.contains { $0.keys.contains("/a") })
    }

    /// Today and Date… keep a timed due's time of day; a day-only due stays a day.
    @Test func movingADueKeepsItsTimeOfDay() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        let at = Date(timeIntervalSince1970: 1_800_000_000) // 2027-01-15 08:00 UTC
        let day = calendar.date(byAdding: .day, value: 3, to: calendar.startOfDay(for: at))!
        let timed = TodoItem(id: "a", title: "Call", dueAt: at)
        #expect(TodosListModel.due(timed, on: day, calendar: calendar) == .at(day.addingTimeInterval(8 * 3600)))
        let allDay = TodoItem(id: "b", title: "Pay", dueDate: "2027-01-15")
        #expect(TodosListModel.due(allDay, on: day, calendar: calendar) == .day("2027-01-18"))
    }

    /// With V1 off (an old hub's pages, and Release until the switch-on) the store never tells the outbox a read landed,
    /// so it is never made there.
    @Test func aStoreWithV1OffLeavesTheOutboxAlone() async throws {
        let hub = Self.hub([Self.plain], needsYou: 0)
        let told = TodoListTold()
        var v1 = TodoStore.V1.on()
        v1.isOn = { false }
        v1.answered = { _ in told.n += 1 }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        defer { store.forget() }
        await store.refresh(api: hub.client)
        #expect(store.byID["a"] != nil && told.n == 0)
        v1.isOn = { true }
        let on = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        defer { on.forget() }
        await on.refresh(api: hub.client)
        #expect(told.n == 1, "told with V1 on")
    }

    /// The rows' swipe, menu and VoiceOver offer the same actions for each state.
    @Test func theRowsActionsAgree() {
        var open = TodoRowContent(id: "a", title: "A")
        #expect(TodoRowActionList.swipe(open) == [.done, .someday])
        #expect(TodoRowActionList.spoken(open) == [.done, .someday, .delete])
        #expect(TodoRowActionList.menu(open) == [.today, .date, .project, .someday, .copyLink, .delete])
        open.someday = true
        #expect(TodoRowActionList.swipe(open) == [.done, .outOfSomeday])
        var struck = TodoRowContent(id: "a", title: "A", closed: true)
        struck.struck = true
        #expect(TodoRowActionList.swipe(struck) == [.undo] && TodoRowActionList.menu(struck).first == .undo)
        #expect(TodoRowActionList.swipe(TodoRowContent(id: "a", title: "A", closed: true)) == [.reopen])
        var saving = TodoRowContent(id: "a", title: "A")
        saving.saving = true
        #expect(TodoRowActionList.swipe(saving).isEmpty && TodoRowActionList.menu(saving).isEmpty && TodoRowActionList.spoken(saving).isEmpty)
        saving.unsure = true
        #expect(TodoRowActionList.swipe(saving) == [.tryAgain, .remove] && TodoRowActionList.spoken(saving) == [.tryAgain, .remove])
    }

    /// Someday: the row moves there at once and every number with it; the hub is told once the window ends.
    @Test func somedayMovesTheRowAndTheNumbersAtOnce() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        hub.on("PUT", "/api/v1/todos/n/someday", json: #"{"todo":{"id":"n","title":"Pick a ryokan"}}"#)
        #expect(list.needsYou == 1)
        list.someday("/n")
        try await waitForListTest { store.countsSource.needsYou == 0 }
        try await waitForListTest { list.groups.last?.folds.contains { $0.id == TodoListSection.someday } == true }
        #expect(list.needsYou == 0, "N moves at once, everywhere")
        #expect(!list.groups.contains { $0.id == TodoListSection.needsYou })
        #expect(list.groups.last?.folds.first { $0.id == TodoListSection.someday }?.keys == ["/n"], "folded under Someday")
        #expect(hub.sent("PUT", "/api/v1/todos/n/someday").isEmpty)
        try await waitForListTest { !hub.sent("PUT", "/api/v1/todos/n/someday").isEmpty }
        let put = try #require(hub.sent("PUT", "/api/v1/todos/n/someday").first)
        #expect(put.headers[OpRetry.header] != nil)
        try await waitForListTest { outbox.entries.isEmpty }
    }

    /// Delete…: the row goes at once and the numbers with it; Undo brings it back and nothing is sent; left alone, the
    /// DELETE goes once the window ends.
    @Test func aDeleteIsHeldAndUndone() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        hub.on("DELETE", "/api/v1/todos/a", json: "{}")
        list.delete("/a")
        try await waitForListTest { !list.groups.contains { $0.keys.contains("/a") } }
        #expect(store.countsSource.counts.sections[TodoListSection.todo] == 0 && store.countsSource.counts.open == 1)
        let undo = try #require(toasts.shown.last?.action)
        undo.run()
        try await waitForListTest { list.groups.contains { $0.keys.contains("/a") } }
        #expect(store.countsSource.counts.sections[TodoListSection.todo] == 1)
        try await Task.sleep(for: .milliseconds(500)) // past the window: nothing may have gone
        #expect(hub.sent("DELETE", "/api/v1/todos/a").isEmpty, "Undo sent nothing")
        hub.on("GET", "/api/v1/todos", json: Self.list([Self.needs], todo: 0))
        list.delete("/a")
        try await waitForListTest { !hub.sent("DELETE", "/api/v1/todos/a").isEmpty }
        try await waitForListTest { outbox.entries.isEmpty }
        #expect(store.byID["a"] == nil && !list.groups.contains { $0.keys.contains("/a") })
    }

    /// An Undo the outbox can no longer do says why by its kind: a delete the hub has is gone for good, any other write
    /// it no longer remembers is only too late to undo.
    @Test func anUndoTooLateSaysWhyByItsKind() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, _, list) = try await list(hub)
        defer { store.forget() }
        list.undo(UUID(), of: .delete)
        let deleted = try #require(toasts.shown.last)
        list.undo(UUID(), of: .done(true))
        let late = try #require(toasts.shown.last)
        #expect(deleted.style == .error && late.style == .error)
        #expect(deleted.message != late.message, "a tick the outbox forgot is not said to be deleted")
    }

    /// New To-Do: listed first in To do at once, saying it is being saved, and sent with its op id; the hub's row takes
    /// its place in one step.
    @Test func aNewToDoIsListedWhileItIsSaved() async throws {
        let hub = Self.hub([Self.needs, Self.plain])
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        let release = TodoListRelease()
        hub.onAsync("POST", "/api/v1/todos") { _ in
            await release.wait()
            hub.on("GET", "/api/v1/todos", json: Self.list([TodoViewJSON.row("t9", title: "Buy stamps",
                view: TodoViewJSON.view(TodoListSection.todo, line: "rest")), Self.needs, Self.plain], todo: 2))
            return Data(#"{"todo":{"id":"t9","title":"Buy stamps"}}"#.utf8)
        }
        #expect(list.create(title: "  Buy stamps ", notes: ""))
        try await waitForListTest { list.groups.first { $0.id == TodoListSection.todo }?.keys.count == 2 }
        let todo = try #require(list.groups.first { $0.id == TodoListSection.todo })
        let new = try #require(todo.keys.first)
        #expect(new != "/a" && list.row(new)?.content.saving == true && list.row(new)?.content.title == "Buy stamps")
        #expect(store.countsSource.counts.sections[TodoListSection.todo] == 2, "counted at once")
        try await waitForListTest { !hub.sent("POST", "/api/v1/todos").isEmpty }
        let post = try #require(hub.sent("POST", "/api/v1/todos").first)
        #expect(post.headers[OpRetry.header] != nil)
        release.go()
        try await waitForListTest { outbox.entries.isEmpty }
        let keys = list.groups.first { $0.id == TodoListSection.todo }?.keys ?? []
        #expect(keys.contains("/t9") && !keys.contains(new), "the hub's row in its place: \(keys)")
    }

    @Test func nothingToAddAddsNothing() async throws {
        let hub = Self.hub([Self.plain], needsYou: 0)
        let (store, outbox, list) = try await list(hub)
        defer { store.forget() }
        #expect(!list.create(title: "   ", notes: "x"))
        #expect(outbox.entries.isEmpty)
    }

    /// The offline line says when the hub last answered a read of the list.
    @Test func theOfflineLineSaysWhenTheListWasRead() {
        var calendar = Calendar(identifier: .gregorian)
        calendar.timeZone = TimeZone(identifier: "UTC")!
        let now = Date(timeIntervalSince1970: 1_800_000_000)
        let words = TodoWords(locale: Locale(identifier: "en_US"))
        #expect(TodoOfflineLine.text(readAt: nil, now: now, words: words, calendar: calendar)
            == "Can't reach your hub · showing what was saved")
        let today = TodoOfflineLine.text(readAt: now.addingTimeInterval(-600), now: now, words: words, calendar: calendar)
        #expect(today.hasPrefix("Can't reach your hub · showing what was saved ") && today.contains(":"))
        let earlier = TodoOfflineLine.text(readAt: now.addingTimeInterval(-2 * 86_400), now: now, words: words, calendar: calendar)
        #expect(earlier != today && earlier.count > today.count, "another day says its day")
    }

    /// Copy Link: the to-do's page on the hub's address, its id escaped.
    @Test func aToDosLink() {
        #expect(TodoLink.web("t01", on: URL(string: "https://hub.example.com/")!)?.absoluteString == "https://hub.example.com/todos/t01")
        #expect(TodoLink.web("a/b", on: URL(string: "https://hub.example.com/persona")!)?.absoluteString
            == "https://hub.example.com/persona/todos/a%2Fb")
    }
}

/// How many times the store told the outbox.
@MainActor private final class TodoListTold {
    var n = 0
}

/// Holds a fake hub's answer until the test lets it go.
@MainActor private final class TodoListRelease {
    private var released = false
    func go() { released = true }
    func wait() async {
        for _ in 0..<500 where !released { try? await Task.sleep(for: .milliseconds(10)) }
    }
}

/// Waits (up to 5 s) for something the store or the hold does.
@MainActor
private func waitForListTest(_ condition: @MainActor () -> Bool) async throws {
    for _ in 0..<250 where !condition() { try await Task.sleep(for: .milliseconds(20)) }
    try #require(condition(), "it did not happen in time")
}
