import Foundation
import Observation
import Testing
@testable import Persona
import PersonaWire

/// The To-dos places (#10465; docs/design/proposals/todos-redesign.md 3.3; DESIGN_SYSTEM 3.5 "To-dos places"): which
/// places the sidebar and the title menu list, what each holds and draws, its numbers and its guard line, and where a
/// pick leaves To-dos.
@MainActor
struct TodosPlacesModelTests {
    private static let calendar: Calendar = {
        var c = Calendar(identifier: .gregorian)
        c.timeZone = TimeZone(identifier: "UTC")!
        return c
    }()
    /// Wednesday 18 November 2026, 12:14 UTC (the prototype's now is 10:14 local time).
    private static let now = Date(timeIntervalSince1970: 1_795_004_040)

    private static func place(_ id: String, needsYou: Int = 0, manager: String? = nil) -> TodoPlace {
        if let slug = TodoPlaceRef(id: id).project {
            return TodoPlace(id: id, kind: TodoPlaceKind.project, title: slug.capitalized, project: slug, needsYou: needsYou, manager: manager)
        }
        return TodoPlace(id: id, kind: id, needsYou: needsYou)
    }

    private static let fixed = [place("all", needsYou: 3), place("today"), place("upcoming")]

    private static func filed(_ id: String, _ section: String = TodoListSection.todo, closed: Bool = false, project: String? = nil,
                              partOf: String? = nil, quiet: Bool = false) -> TodoFiled {
        TodoFiled(id: id, manager: nil, section: section, closed: closed, rank: 0, inactive: false, byView: true, quiet: quiet,
                  partOf: partOf, project: project, asks: section == TodoListSection.needsYou ? ["ask:" + id] : [])
    }

    private static func day(_ offset: Int) -> String {
        TodosLogic.dayKey(calendar.date(byAdding: .day, value: offset, to: now)!, calendar: calendar)
    }

    // MARK: The sidebar's and the title menu's places

    /// Today and Upcoming, then the projects by name (the hub sends them by slug), as the Finder sorts names.
    @Test func theSidebarListsTodayUpcomingAndTheProjectsByName() {
        let named = { (slug: String, title: String) in
            TodoPlace(id: "project:" + slug, kind: TodoPlaceKind.project, title: title, project: slug)
        }
        let entries = TodosPlaces.entries(Self.fixed + [named("a-home", "Home"), named("b-trip", "Kyoto trip"),
                                                       named("c-app", "app 10"), named("d-app", "app 9")])
        #expect(entries.fixed.map(\.id) == ["today", "upcoming"])
        #expect(entries.projects.map(\.title) == ["app 9", "app 10", "Home", "Kyoto trip"])
        #expect(entries.more, "All Projects… follows the projects")
    }

    /// Five projects at most; one that needs the owner is never left behind All Projects…, and the ones listed go by
    /// name.
    @Test func atMostFiveProjectsThoseThatNeedYouFirst() {
        let projects = (1...7).map { Self.place("project:p\($0)", needsYou: $0 >= 6 ? 1 : 0) }
        let entries = TodosPlaces.entries(Self.fixed + projects)
        #expect(entries.projects.map(\.id) == ["project:p1", "project:p2", "project:p3", "project:p6", "project:p7"])
        #expect(entries.more)
    }

    /// A hub without `todo_places` (or one that answered 405): the store has All alone, so there is nothing to fold open
    /// or pick, and a place picked before is All To-dos again.
    @Test func withoutTheHubsPlacesAllToDosAlone() {
        let entries = TodosPlaces.entries([Self.place("all", needsYou: 2)])
        #expect(entries.isEmpty)
        #expect(TodosPlaces.effective(.today, places: [Self.place("all")]) == .all)
        #expect(TodosPlaces.effective(.project("kyoto"), places: [Self.place("all")]) == .all)
    }

    /// No projects but the knowledge base has some: All Projects… is still offered (a project with nothing open is a place).
    @Test func allProjectsWithOnlyTheKnowledgeBasesProjects() {
        #expect(!TodosPlaces.entries(Self.fixed).more)
        #expect(TodosPlaces.entries(Self.fixed, knowsProjects: true).more)
    }

    /// A project place is drawn while the hub or the knowledge base knows it; another assistant's is All To-dos.
    @Test func aProjectNobodyKnowsIsAllToDos() {
        let places = Self.fixed + [Self.place("project:kyoto", needsYou: 1)]
        #expect(TodosPlaces.effective(.project("kyoto"), places: places) == .project("kyoto"))
        #expect(TodosPlaces.effective(.project("garden"), places: places) == .all)
        let garden = TodoProject(slug: "garden", name: "Garden", long: false)
        #expect(TodosPlaces.effective(.project("garden"), places: places, projects: [garden]) == .project("garden"))
        #expect(TodosPlaces.effective(.upcoming, places: places) == .upcoming)
    }

    @Test func titles() {
        let places = Self.fixed + [TodoPlace(id: "project:kyoto", kind: TodoPlaceKind.project, title: "Kyoto trip", project: "kyoto")]
        let kb = [TodoProject(slug: "garden", name: "The garden", long: false)]
        #expect(TodosPlaces.title(.project("kyoto"), places: places, projects: kb) == "Kyoto trip")
        #expect(TodosPlaces.title(.project("garden"), places: places, projects: kb) == "The garden")
        #expect(TodosPlaces.title(.project("shed"), places: places, projects: kb) == "shed")
        #expect(!TodosPlaces.title(.today, places: places, projects: kb).isEmpty)
        #expect(TodosPlaces.title(.today, places: places, projects: kb) != TodosPlaces.title(.upcoming, places: places, projects: kb))
    }

    // MARK: What a place holds

    @Test func todayHoldsWhatIsDueTodayOverdueOrRemindingToday() {
        let items: [String: TodoItem] = [
            "due": TodoItem(id: "due", title: "due", dueDate: Self.day(0)),
            "late": TodoItem(id: "late", title: "late", dueDate: Self.day(-2)),
            "earlier": TodoItem(id: "earlier", title: "earlier", dueAt: Self.now.addingTimeInterval(-3600)),
            "remind": TodoItem(id: "remind", title: "remind", remindAt: Self.now.addingTimeInterval(3600)),
            "later": TodoItem(id: "later", title: "later", dueDate: Self.day(1)),
            "plain": TodoItem(id: "plain", title: "plain"),
            "closed": TodoItem(id: "closed", title: "closed", dueDate: Self.day(0)),
            "someday": TodoItem(id: "someday", title: "someday", dueDate: Self.day(0)),
        ]
        let rows = ["due", "late", "earlier", "remind", "later", "plain"].map { Self.filed($0) }
            + [Self.filed("closed", TodoListSection.done, closed: true), Self.filed("someday", TodoListSection.someday)]
        let today = TodosPlaces.rows(rows, in: .today, items: items, now: Self.now, calendar: Self.calendar).map(\.id)
        #expect(today == ["due", "late", "earlier", "remind"])
        let upcoming = TodosPlaces.rows(rows, in: .upcoming, items: items, now: Self.now, calendar: Self.calendar).map(\.id)
        #expect(upcoming == ["due", "late", "earlier", "later"])
    }

    /// A project holds its rows, closed ones too (the Filter's Done); a step its parent carries is never a row of its own.
    @Test func aProjectHoldsItsRowsLessTheStepsTheirParentsCarry() {
        let rows = [Self.filed("t01", TodoListSection.needsYou, project: "kyoto"),
                    Self.filed("t01-s1", project: "kyoto", partOf: "t01"),
                    Self.filed("t08", TodoListSection.done, closed: true, project: "kyoto"),
                    Self.filed("t02", project: "weather")]
        let kyoto = TodosPlaces.rows(rows, in: .project("kyoto"), items: [:], now: Self.now, calendar: Self.calendar).map(\.id)
        #expect(kyoto == ["t01", "t08"])
        #expect(TodosPlaces.rows(rows, in: .all, items: [:], now: Self.now, calendar: Self.calendar).count == 4)
    }

    /// A step filed in another project than its open parent goes with its parent, wherever the parent is: the project's
    /// place, its part of N (the sidebar's and the title menu's, as the hub rolls it up) and All Projects…' open count
    /// say the same.
    @Test func aStepInAnotherProjectCountsWithItsParent() {
        let rows = [Self.filed("plan"),
                    Self.filed("ryokan", TodoListSection.needsYou, project: "kyoto", partOf: "plan"),
                    Self.filed("app", project: "weather"),
                    Self.filed("icon", TodoListSection.needsYou, project: "kyoto", partOf: "app"),
                    Self.filed("visa", TodoListSection.needsYou, project: "kyoto")]
        let kyoto = TodosPlaces.rows(rows, in: .project("kyoto"), items: [:], now: Self.now, calendar: Self.calendar)
        #expect(kyoto.map(\.id) == ["visa"], "the steps go with their parents")
        #expect(TodoCountsFallback.counts(kyoto).needsYou == 1, "the place's Needs you")
        #expect(TodoCountsFallback.needsYouByProject(rows)["kyoto"] == 1, "its part of N")
        #expect(TodosPlaces.openCounts(rows, projects: ["kyoto", "weather"]) == ["kyoto": 1, "weather": 1])
    }

    /// "3 coming up this week": open to-dos due in the seven days after today, whatever day of the week today is.
    @Test func comingUpIsTheNextSevenDays() {
        let items = Dictionary(uniqueKeysWithValues: [(-1), 0, 1, 2, 7, 8].map { n in
            ("d\(n)", TodoItem(id: "d\(n)", title: "d", dueDate: Self.day(n)))
        })
        let rows = items.keys.sorted().map { Self.filed($0) }
        #expect(TodosPlaces.comingUp(rows, items: items, now: Self.now, calendar: Self.calendar) == 3)
    }

    /// A step its parent carries is a row of its own where the place leaves the parent out: a dated step of an undated
    /// to-do is on Today and Upcoming; under a dated parent it is not.
    @Test func aStepIsARowWhereItsParentIsNot() {
        let items: [String: TodoItem] = [
            "trip": TodoItem(id: "trip", title: "Plan the trip"),
            "ryokan": TodoItem(id: "ryokan", title: "Book the ryokan", dueDate: Self.day(0)),
            "visa": TodoItem(id: "visa", title: "Visa", dueDate: Self.day(1)),
            "photo": TodoItem(id: "photo", title: "Photo", dueDate: Self.day(1)),
        ]
        let rows = [Self.filed("trip", project: "kyoto"), Self.filed("ryokan", project: "kyoto", partOf: "trip"),
                    Self.filed("visa"), Self.filed("photo", partOf: "visa")]
        let rowsIn = { (place: TodoPlaceRef) in
            TodosPlaces.rows(rows, in: place, items: items, now: Self.now, calendar: Self.calendar).map(\.id)
        }
        #expect(rowsIn(.today) == ["ryokan"])
        #expect(rowsIn(.upcoming) == ["ryokan", "visa"])
        #expect(rowsIn(.project("kyoto")) == ["trip"])
        #expect(TodosPlaces.comingUp(rows, items: items, now: Self.now, calendar: Self.calendar) == 1, "the visa, its photo with it")
    }

    // MARK: Upcoming

    /// Overdue first, then each day that has a to-do, soonest first; inside a day by the due moment, then the hub's order.
    @Test func upcomingIsByDay() {
        let items: [String: TodoItem] = [
            "late": TodoItem(id: "late", title: "late", dueDate: Self.day(-1)),
            "fri2": TodoItem(id: "fri2", title: "fri2", dueDate: Self.day(2)),
            "fri1": TodoItem(id: "fri1", title: "fri1", dueAt: Self.calendar.date(byAdding: .hour, value: 49, to: Self.now)),
            "thu": TodoItem(id: "thu", title: "thu", dueDate: Self.day(1)),
        ]
        let rows = ["late", "fri2", "fri1", "thu"].map { Self.filed($0) }
        let out = TodoUpcomingLayout.layout(rows, items: items, now: Self.now, calendar: Self.calendar)
        #expect(out.groups.map(\.id) == [TodoUpcomingLayout.overdue, "day:" + Self.day(1), "day:" + Self.day(2)])
        #expect(out.groups[2].keys == ["/fri1", "/fri2"])
        #expect(TodoUpcomingLayout.day(out.groups[1].id, calendar: Self.calendar) == Self.calendar.date(byAdding: .day, value: 1,
                                                                                                         to: Self.calendar.startOfDay(for: Self.now)))
        #expect(TodoUpcomingLayout.day(TodoUpcomingLayout.overdue, calendar: Self.calendar) == nil)
        #expect(!out.calm && out.sections.count == 4, "no Filter to hide: the days are drawn whatever the rows")
    }

    /// Only All To-dos and a project's place draw the Filter (DECISIONS 11-21 "The To-dos list"): Upcoming lists every
    /// dated row by day whatever its section, and Today lands on All. Group by Project files rows in All To-dos and Today
    /// alone: Upcoming's days are its grouping, and a project's place is one project.
    @Test func onlyAllAndAProjectDrawTheFilter() {
        let items: [String: TodoItem] = [
            "ask": TodoItem(id: "ask", title: "ask", dueDate: Self.day(1)),
            "plain": TodoItem(id: "plain", title: "plain", dueDate: Self.day(1)),
            "run": TodoItem(id: "run", title: "run", dueDate: Self.day(2)),
        ]
        let rows = [Self.filed("ask", TodoListSection.needsYou), Self.filed("plain"), Self.filed("run", TodoListSection.working)]
        let out = TodoUpcomingLayout.layout(rows, items: items, now: Self.now, calendar: Self.calendar)
        #expect(!out.calm && out.groups.map(\.id) == ["day:" + Self.day(1), "day:" + Self.day(2)])
        #expect(out.groups.flatMap(\.keys) == ["/ask", "/plain", "/run"])
        #expect(TodosPlaces.filters(.all) && TodosPlaces.filters(.project("kyoto")))
        #expect(!TodosPlaces.filters(.today) && !TodosPlaces.filters(.upcoming))
        #expect(TodosPlaces.groupsByProject(.all) && TodosPlaces.groupsByProject(.today))
        #expect(!TodosPlaces.groupsByProject(.upcoming) && !TodosPlaces.groupsByProject(.project("kyoto")))
    }

    /// All Projects… counts each project's open to-dos from its rows: not closed, not in Someday, a step its parent
    /// carries with its parent; a project with no rows has none.
    @Test func allProjectsCountsEachProjectsOpenToDos() {
        let rows = [Self.filed("t01", TodoListSection.needsYou, project: "kyoto"),
                    Self.filed("t01-s1", project: "kyoto", partOf: "t01"),
                    Self.filed("t08", TodoListSection.done, closed: true, project: "kyoto"),
                    Self.filed("t09", TodoListSection.someday, project: "kyoto"),
                    Self.filed("t02", TodoListSection.working, project: "weather"),
                    Self.filed("t03", project: "weather"),
                    Self.filed("t04")]
        #expect(TodosPlaces.openCounts(rows, projects: ["kyoto", "weather", "garden"]) == ["kyoto": 1, "weather": 2, "garden": 0])
    }

    /// A project's place says its project in its title: line 3 leaves it out of every row there, Needs you's included,
    /// where All To-dos keeps it beside another fact.
    @Test func aProjectsPlaceLeavesItsNameOffLine3() {
        let view = TodoSummary(phase: TodoPhase.needsYou, section: TodoListSection.needsYou, reason: "rest",
                               steps: TodoStepCount(done: 1, total: 3))
        let item = TodoItem(id: "t01", title: "Plan the Kyoto trip", project: "kyoto")
        let row = Self.filed("t01", TodoListSection.needsYou, project: "kyoto")
        let name = { (_: String) in "Kyoto trip" }
        let all = TodoRowContentLogic.content(row, item: item, view: view, now: Self.now, projectName: name)
        #expect(all.caption?.contains("Kyoto trip") == true, "\(all.caption ?? "")")
        let here = TodoRowContentLogic.content(row, item: item, view: view, inProject: true, now: Self.now, projectName: name)
        #expect(here.caption != nil && here.caption?.contains("Kyoto trip") == false, "\(here.caption ?? "")")
    }

    /// Upcoming's calendar colours each to-do by its view's phase, not by its sessions.
    @Test func theCalendarReadsTheView() {
        let view = { (phase: String, reason: String) in TodoSummary(phase: phase, section: phase, reason: reason, rank: 0) }
        #expect(TodoUpcomingLayout.calendarState(view(TodoPhase.needsYou, TodoReason.agentAsks), fallback: .notStarted) == .needsYou)
        #expect(TodoUpcomingLayout.calendarState(view(TodoPhase.needsYou, TodoReason.failedStart), fallback: .notStarted) == .failed)
        #expect(TodoUpcomingLayout.calendarState(view(TodoPhase.ready, TodoReason.agentRunning), fallback: .notStarted) == .unread)
        #expect(TodoUpcomingLayout.calendarState(nil, fallback: .done) == .done)
    }

    // MARK: Picks

    /// The sidebar's pick opens To-dos on the place at its root, once; the title menu's changes the list in place; a
    /// page built for the app switcher's snapshot moves nothing.
    @Test func aSidebarPickOpensThePlaceAtItsRoot() {
        let scene = PlacesTestScene()
        let workspace = TodoWorkspace(active: { scene.active })
        workspace.open(TodoPlaceKind.today)
        #expect(workspace.place == TodoPlaceKind.today)
        #expect(workspace.takePick())
        #expect(!workspace.takePick(), "taken once")
        workspace.open(TodoPlaceKind.today)
        #expect(!workspace.takePick(), "the place in front picked again: its stack's own rule")
        workspace.pick(TodoPlaceKind.upcoming)
        #expect(workspace.place == TodoPlaceKind.upcoming && !workspace.takePick(), "the title menu changes the list where it is")
        scene.active = false
        workspace.open(TodoPlaceKind.allValue)
        #expect(workspace.place == TodoPlaceKind.upcoming)
    }

    /// The sidebar's To-dos row: from elsewhere To-dos comes back on its last place, with what it kept; where To-dos is
    /// the place in front (beside the sidebar or under the drawer, whatever is pushed on it: the host says) it is All
    /// To-dos at its root. A place picked after the row is that place.
    @Test func theToDosRowReopensOnTheLastPlace() {
        let workspace = TodoWorkspace(active: { true })
        let hub = FakeHub()
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on(api: hub.client))
        defer { store.forget() }
        let places = TodosPlacesModel(deps: .init(store: store, workspace: workspace, projects: { [] }))
        places.open(.today)
        #expect(workspace.opening(inFront: false), "the pick: Today at its root")
        places.pickRow()
        #expect(!workspace.opening(inFront: false) && workspace.place == TodoPlaceKind.today, "from elsewhere: as it was left")
        places.pickRow()
        #expect(workspace.opening(inFront: true) && workspace.place == TodoPlaceKind.allValue, "in front: All To-dos, its root")
        places.pickRow()
        #expect(!workspace.opening(inFront: true), "All To-dos in front: the tab-bar rule alone")
        places.pickRow()
        places.open(.upcoming)
        #expect(workspace.opening(inFront: true) && workspace.place == TodoPlaceKind.upcoming, "the place picked after the row")
        #expect(!workspace.opening(inFront: true), "nothing left over for the next opening")
    }

    // MARK: Against a hub

    private static let rows = [
        TodoViewJSON.row("t01", extra: #""project":"kyoto""#, view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["a1"])),
        TodoViewJSON.row("t02", extra: #""project":"weather""#, view: TodoViewJSON.view(TodoListSection.needsYou, rank: 1, asks: ["a2"])),
        TodoViewJSON.row("t03", extra: #""project":"weather""#, view: TodoViewJSON.view(TodoListSection.working)),
        TodoViewJSON.row("t11", extra: #""due_date":"2026-11-20""#, view: TodoViewJSON.view(TodoListSection.todo, line: "rest")),
    ]

    private static let list = TodoViewJSON.list(rows, counts: TodoViewJSON.counts(needsYou: 2, inProgress: 1, open: 4,
        sections: [TodoListSection.needsYou: 2, TodoListSection.working: 1, TodoListSection.todo: 1]))

    private static let placesJSON = #"""
    {"places":[{"id":"all","kind":"all","title":"","needs_you":2},{"id":"today","kind":"today","title":"","needs_you":0},
    {"id":"upcoming","kind":"upcoming","title":"","needs_you":0},
    {"id":"project:kyoto","kind":"project","title":"Kyoto trip","project":"kyoto","needs_you":1},
    {"id":"project:weather","kind":"project","title":"Weather App","project":"weather","needs_you":1}],"counts":{}}
    """#

    private func store(_ hub: FakeHub, places: Bool = true) -> TodoStore {
        TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {},
                  v1: .on(features: TodoFeatures(view: true, places: places), api: hub.client))
    }

    private func placesModel(_ store: TodoStore, _ workspace: TodoWorkspace) -> TodosPlacesModel {
        TodosPlacesModel(deps: .init(store: store, workspace: workspace, projects: { [] }))
    }

    private func listModel(_ store: TodoStore, hub: FakeHub, outbox: TodoOutbox? = nil) -> TodosListModel {
        TodosListModel(deps: TodosListDeps(store: store, outbox: outbox ?? TodosListModelTests.outbox(store, hub: hub),
                                           api: { hub.client }, now: { Self.now }, calendar: { Self.calendar }, projects: { [] },
                                           toast: { _ in }, show: { _ in }, undoWindow: .seconds(60)))
    }

    /// A store whose rows and numbers follow an outbox of the test's own (`TodoStore.V1.pending`), as the app's follow
    /// `TodoOutbox.shared`.
    private func storeWithOutbox(_ hub: FakeHub) async -> (TodoStore, TodoOutbox) {
        let box = PlacesTestOutbox()
        var v1 = TodoStore.V1.on(api: hub.client)
        v1.pending = { box.outbox?.pending ?? [:] }
        v1.answered = { box.outbox?.hubAnswered(read: $0) }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        let outbox = TodosListModelTests.outbox(store, hub: hub)
        await outbox.ready()
        box.outbox = outbox
        return (store, outbox)
    }

    /// The body of the latest `POST /api/v1/todos`.
    private func created(_ hub: FakeHub) throws -> [String: Any] {
        let body = try #require(hub.sent("POST", "/api/v1/todos").last?.body)
        return try #require(JSONSerialization.jsonObject(with: body) as? [String: Any])
    }

    @Test func aHubWithPlacesListsThem() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let store = store(hub)
        defer { store.forget() }
        let places = placesModel(store, TodoWorkspace(active: { true }))
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { !places.entries.isEmpty }
        #expect(places.entries.projects.map(\.title) == ["Kyoto trip", "Weather App"])
        #expect(places.entries.projects.map(\.needsYou) == [1, 1])
        #expect(places.title(.project("weather")) == "Weather App")
    }

    /// A hub whose hello does not list `todo_places`, and one that answers the route 405: All To-dos only.
    @Test func withoutTodoPlacesOrA405AllToDosOnly() async throws {
        for listed in [false, true] {
            let hub = FakeHub()
            hub.on("GET", "/api/v1/todos", json: Self.list)
            hub.on("GET", "/api/v1/todos/places") { _ in throw APIError(status: 405, message: "method not allowed") }
            let store = store(hub, places: listed)
            defer { store.forget() }
            let workspace = TodoWorkspace(active: { true })
            workspace.pick(TodoPlaceKind.today)
            let places = placesModel(store, workspace)
            await store.refresh(api: hub.client)
            try await waitForPlacesTest { !store.todos.isEmpty }
            #expect(places.entries.isEmpty)
            #expect(places.current == .all, "a place picked before is All To-dos")
        }
    }

    /// In a place the Filter and the Needs you header count its rows; the guard line counts what needs the owner
    /// elsewhere and names the place; under a choice the one line counts both, what the choice hides and what is outside
    /// the place. Show All goes back to All To-dos, where nothing is hidden.
    @Test func aPlacesNumbersAndWhatNeedsYouElsewhere() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let store = store(hub)
        defer { store.forget() }
        let workspace = TodoWorkspace(active: { true })
        let places = placesModel(store, workspace)
        let list = listModel(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && !places.entries.isEmpty }
        #expect(list.needsYou == 2 && list.hiddenNeeds == 0)

        places.show(.project("weather"))
        list.place = places.current
        #expect(list.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working])
        #expect(list.needsYou == 1, "the place's part of N")
        #expect(list.placeCounts?.inProgress == 1 && list.filters, "the Filter counts the place's rows")
        #expect(list.hiddenNeeds == 1 && list.hiddenElsewhere, "Kyoto trip's to-do needs the owner too")
        // The title's subtitle, once the line has scrolled away, says what the line said.
        #expect(list.subtitle(place: "Weather App") == TodosFilter.guardWords(1, elsewhere: true, place: "Weather App"))
        list.choice = .inProgress
        #expect(list.hiddenNeeds == 2 && !list.hiddenElsewhere, "one line: the place's own and Kyoto trip's")
        #expect(list.subtitle(place: "Weather App") == TodosFilter.guardWords(2, elsewhere: false, place: "Weather App"),
                "not the choice's: it does not hide Kyoto trip's")

        list.choice = .needsYou
        places.show(.today)
        list.place = places.current
        #expect(list.choice == .all, "another place lands on All")
        #expect(list.isEmpty && list.hiddenNeeds == 2 && list.hiddenElsewhere)
        #expect(!list.filters, "Today draws no Filter")
        #expect(list.subtitle(place: "Today") == TodosFilter.guardWords(2, elsewhere: true, place: "Today"))

        places.show(.all) // Show All
        list.place = places.current
        #expect(list.hiddenNeeds == 0 && list.groups.count == 3 && list.subtitle(place: "All To-dos").isEmpty)
    }

    /// A hello that stops listing `todo_places` (a manager reconnecting after the hub restarted) takes nothing away: the
    /// places and the place picked stay, and are read on.
    @Test func aHelloWithoutPlacesKeepsThem() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let listed = PlacesTestFlag()
        var v1 = TodoStore.V1.on(api: hub.client)
        v1.features = { TodoFeatures(view: true, places: listed.on) }
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: v1)
        defer { store.forget() }
        let workspace = TodoWorkspace(active: { true })
        let places = placesModel(store, workspace)
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { !places.entries.isEmpty }
        workspace.pick("project:kyoto")
        listed.on = false
        hub.on("GET", "/api/v1/todos", json: Self.list.replacingOccurrences(of: "\"t11\"", with: "\"t12\""))
        await store.refresh(api: hub.client)
        #expect(hub.sent("GET", "/api/v1/todos/places").count == 2, "read on")
        #expect(!places.entries.isEmpty && places.current == .project("kyoto"))
    }

    /// A project whose to-dos are all closed (none today) is a place with nothing open: its own empty state (the foot's +
    /// starts a to-do in it), not All To-dos'.
    @Test func aProjectWithOnlyClosedToDosSaysNothingIsOpenInIt() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("t08", extra: #""project":"kyoto","done":true,"done_at":"2026-11-10T09:00:00Z""#,
                             view: TodoViewJSON.view(TodoListSection.done, phase: TodoPhase.done)),
            TodoViewJSON.row("t02", extra: #""project":"weather""#, view: TodoViewJSON.view(TodoListSection.todo)),
        ]))
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let store = store(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && list.hasRows }
        list.place = .project("kyoto")
        #expect(list.hasRows && list.isEmpty && list.placeIsEmpty)
        list.place = .project("weather")
        #expect(!list.isEmpty && !list.placeIsEmpty)
        list.place = .all
        #expect(!list.placeIsEmpty)
    }

    /// Where New To-Do starts in each place (proposal 3.3, "Capture default"): nowhere in particular in All To-dos, due
    /// today on Today, on Upcoming tomorrow or the day whose heading's + was tapped, in a project in it.
    @Test func eachPlaceSaysWhereANewToDoStarts() {
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, v1: .on())
        defer { store.forget() }
        let list = listModel(store, hub: FakeHub())
        let today = Self.calendar.startOfDay(for: Self.now)
        let friday = Self.calendar.date(byAdding: .day, value: 2, to: today)!
        #expect(list.capture() == TodoCapture())
        list.place = .today
        #expect(list.capture() == TodoCapture(day: today))
        list.place = .upcoming
        #expect(list.capture() == TodoCapture(day: Self.calendar.date(byAdding: .day, value: 1, to: today)))
        #expect(list.capture(on: friday.addingTimeInterval(3600)) == TodoCapture(day: friday), "the heading's day")
        let ten = Self.calendar.date(bySettingHour: 10, minute: 0, second: 0, of: friday)!
        #expect(list.capture(on: friday, at: ten) == TodoCapture(day: friday, at: ten), "an hour on its calendar")
        list.place = .project("kyoto")
        #expect(list.capture() == TodoCapture(project: "kyoto"))
        // One the hub turned down starts again due when it was due and in its project (unless the hub turned that
        // down), wherever the list is now (its toast's Edit).
        #expect(list.capture(TodoOutbox.Draft(title: "Visa", dueAt: ten)) == TodoCapture(day: friday, at: ten))
        #expect(list.capture(TodoOutbox.Draft(title: "Visa", dueDate: Self.day(2))) == TodoCapture(day: friday))
        #expect(list.capture(TodoOutbox.Draft(title: "Visa")) == TodoCapture())
        #expect(list.capture(TodoOutbox.Draft(title: "Visa", project: "weather")) == TodoCapture(project: "weather"))
    }

    /// A to-do made in Today or in a project is listed there at once ("Saving…"), sent with its due date or its project
    /// in the one write that makes it, and stays there once the hub has it.
    @Test func aToDoMadeInAPlaceShowsThereAtOnceAndStays() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let (store, outbox) = await storeWithOutbox(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub, outbox: outbox)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && list.hasRows }
        let today = Self.day(0)
        let made: [(place: TodoPlaceRef, id: String, title: String, due: String?, project: String?)] = [
            (.today, "n1", "Renew passport", today, nil),
            (.project("kyoto"), "n2", "Book the ryokan", nil, "kyoto"),
        ]
        let listed = PlacesTestRows()
        for (n, new) in made.enumerated() {
            list.place = new.place
            let release = PlacesTestRelease()
            hub.onAsync("POST", "/api/v1/todos") { _ in
                await release.wait()
                let extra = new.due.map { #""due_date":"\#($0)""# } ?? #""project":"\#(new.project ?? "")""#
                listed.rows.append(TodoViewJSON.row(new.id, title: new.title, extra: extra,
                                                    view: TodoViewJSON.view(TodoListSection.todo, line: "rest")))
                hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list(listed.rows + Self.rows))
                return Data(#"{"todo":{"id":"\#(new.id)","title":"\#(new.title)",\#(extra)}}"#.utf8)
            }
            let before = list.groups.flatMap(\.keys)
            #expect(list.create(title: new.title, notes: nil, due: new.due.map { .day($0) }, project: new.project))
            try await waitForPlacesTest { list.groups.flatMap(\.keys).count == before.count + 1 }
            let drawn = try #require(list.groups.flatMap(\.keys).first { !before.contains($0) })
            #expect(list.row(drawn)?.content.saving == true && list.row(drawn)?.content.title == new.title, "\(new.place.id): at once")
            try await waitForPlacesTest { hub.sent("POST", "/api/v1/todos").count == n + 1 }
            let body = try created(hub)
            #expect(body["due_date"] as? String == new.due && body["project"] as? String == new.project, "\(new.place.id): \(body)")
            release.go()
            try await waitForPlacesTest { outbox.entries.isEmpty }
            #expect(list.groups.flatMap(\.keys).contains("/" + new.id), "\(new.place.id): the hub's row, still there")
            #expect(!list.groups.flatMap(\.keys).contains(drawn))
        }
    }

    /// A to-do made on Upcoming at an hour (All Day off, or an hour tapped on its calendar) is sent due at that moment
    /// (`due_at`, RFC 3339, with no `due_date`) and listed under its day at once, saying its time, where an all-day row
    /// under its day says none.
    @Test func aToDoMadeAtAnHourIsSentDueThenAndListedUnderItsDay() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let (store, outbox) = await storeWithOutbox(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub, outbox: outbox)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && list.hasRows }
        list.place = .upcoming
        #expect(list.row("/t11")?.content.trailing == nil, "all day, under its day's heading")
        let release = PlacesTestRelease()
        defer { release.go() }
        hub.onAsync("POST", "/api/v1/todos") { _ in
            await release.wait()
            return Data(#"{"todo":{"id":"n1","title":"Call the plumber","due_at":"2026-11-19T10:00:00Z"}}"#.utf8)
        }
        let tomorrow = Self.calendar.date(byAdding: .day, value: 1, to: Self.calendar.startOfDay(for: Self.now))!
        let ten = Self.calendar.date(bySettingHour: 10, minute: 0, second: 0, of: tomorrow)!
        #expect(list.create(title: "Call the plumber", notes: nil, due: .at(ten)))
        let day = "day:" + Self.day(1)
        try await waitForPlacesTest { list.groups.first { $0.id == day }?.keys.count == 1 }
        let key = try #require(list.groups.first { $0.id == day }?.keys.first)
        #expect(list.row(key)?.content.saving == true && list.row(key)?.content.trailing != nil, "at once, saying its time")
        try await waitForPlacesTest { hub.sent("POST", "/api/v1/todos").count == 1 }
        let body = try created(hub)
        #expect(body["due_at"] as? String == "2026-11-19T10:00:00Z" && body["due_date"] == nil, "\(body)")
    }

    /// An answer given here to a to-do outside the place moves only the numbers: the place's guard line follows at once,
    /// and the list drawn stays as it is.
    @Test func anAnswerElsewhereMovesThePlacesLineAlone() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("t01", extra: #""project":"kyoto""#, view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["a1"])),
            TodoViewJSON.row("t02", extra: #""project":"weather","due_date":"\#(Self.day(0))""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, rank: 1, asks: ["a2"])),
        ], counts: TodoViewJSON.counts(needsYou: 2, inProgress: 0, open: 2, sections: [TodoListSection.needsYou: 2])))
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let store = store(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && list.hasRows }
        list.place = .today
        #expect(list.groups.flatMap(\.keys) == ["/t02"] && list.hiddenNeeds == 1)
        let drawn = list.groups
        store.countsSource.answered("a1", of: "t01")
        try await waitForPlacesTest { list.hiddenNeeds == 0 }
        #expect(list.groups == drawn && list.needsYou == 1)
    }

    /// The list's writes inside a place move its rows, its numbers, the sidebar's part of N and the notice at once, each
    /// by one: Someday on a Today row that needs the owner, a delete in a project; and back when they are undone.
    @Test func writesInAPlaceMoveEveryNumberOnce() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("t01", extra: #""project":"kyoto""#, view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["a1"])),
            TodoViewJSON.row("t02", extra: #""project":"weather","due_date":"\#(Self.day(0))""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, rank: 1, asks: ["a2"])),
            TodoViewJSON.row("t03", extra: #""project":"weather""#, view: TodoViewJSON.view(TodoListSection.working)),
        ], counts: TodoViewJSON.counts(needsYou: 2, inProgress: 1, open: 3,
                                       sections: [TodoListSection.needsYou: 2, TodoListSection.working: 1])))
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let (store, outbox) = await storeWithOutbox(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub, outbox: outbox)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && store.countsSource.places.count == 5 }
        let part = { (slug: String) in store.countsSource.places.first { $0.id == "project:" + slug }?.needsYou }

        list.place = .today
        #expect(list.groups.flatMap(\.keys) == ["/t02"] && list.needsYou == 1 && list.hiddenNeeds == 1)
        list.someday("/t02")
        try await waitForPlacesTest { list.isEmpty }
        #expect(store.countsSource.needsYou == 1 && part("weather") == 0 && part("kyoto") == 1, "off N and its project once")
        #expect(list.needsYou == 0 && list.hiddenNeeds == 1, "Kyoto trip's still needs the owner elsewhere")
        list.undo(try #require(outbox.entries.last?.id), of: .someday(true))
        try await waitForPlacesTest { !list.isEmpty }
        #expect(store.countsSource.needsYou == 2 && part("weather") == 1 && list.hiddenNeeds == 1)

        list.place = .project("kyoto")
        #expect(list.needsYou == 1 && list.hiddenNeeds == 1)
        list.delete("/t01")
        try await waitForPlacesTest { list.isEmpty }
        #expect(store.countsSource.needsYou == 1 && part("kyoto") == 0 && part("weather") == 1, "its bar and its part together")
        #expect(list.needsYou == 0 && list.hiddenNeeds == 1)
        list.undo(try #require(outbox.entries.last?.id), of: .delete)
        try await waitForPlacesTest { !list.isEmpty }
        #expect(store.countsSource.needsYou == 2 && part("kyoto") == 1 && list.needsYou == 1)
        #expect(hub.sent("DELETE", "/api/v1/todos/t01").isEmpty && hub.sent("PUT", "/api/v1/todos/t02/someday").isEmpty)
    }

    /// A search finds any to-do, whatever the place: it sets the place aside as it sets the choice aside, with no guard
    /// line and no place's empty state, and the place is back once the field is cleared.
    @Test func searchFindsAnyToDoWhateverThePlace() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let store = store(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub)
        list.start()
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { list.ready && list.hasRows }
        list.place = .project("kyoto")
        #expect(!list.groups.flatMap(\.drawnKeys).contains("/t03"))
        list.query = "t03"
        #expect(list.searching && list.groups.flatMap(\.drawnKeys) == ["/t03"], "Weather App's, from Kyoto trip")
        #expect(list.hiddenNeeds == 0 && !list.placeIsEmpty)
        list.query = "nothing like it"
        #expect(list.isEmpty && !list.placeIsEmpty, "the search's own empty state")
        list.query = ""
        #expect(list.groups.flatMap(\.drawnKeys) == ["/t01"] && list.hiddenNeeds == 1)
    }

    /// A list made on Upcoming counts what needs the owner elsewhere once it starts, whatever the page draws first: its
    /// calendar says it too.
    @Test func upcomingCountsWhatNeedsYouElsewhereOnceStarted() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: Self.list)
        hub.on("GET", "/api/v1/todos/places", json: Self.placesJSON)
        let store = store(hub)
        defer { store.forget() }
        let list = listModel(store, hub: hub)
        list.place = .upcoming
        await store.refresh(api: hub.client)
        #expect(list.hiddenNeeds == 0, "not started: nothing read")
        list.start()
        #expect(list.hiddenNeeds == 2, "both are outside Upcoming")
    }

    /// Two assistants: the places' projects are the current one's, each part counted from its own rows.
    @Test func twoManagersListTheCurrentAssistantsProjects() async throws {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/todos", json: TodoViewJSON.list([
            TodoViewJSON.row("h1", extra: #""manager":"home","project":"garden""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["a"])),
            TodoViewJSON.row("w1", extra: #""manager":"work","project":"weather""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, asks: ["b"])),
            TodoViewJSON.row("w2", extra: #""manager":"work","project":"weather""#,
                             view: TodoViewJSON.view(TodoListSection.needsYou, rank: 1, asks: ["c"])),
        ]))
        hub.on("GET", "/api/v1/todos/places", json: #"""
        {"places":[{"id":"all","kind":"all","title":"","needs_you":3},{"id":"today","kind":"today","title":"","needs_you":0},
        {"id":"upcoming","kind":"upcoming","title":"","needs_you":0},
        {"id":"project:garden","kind":"project","title":"Garden","project":"garden","needs_you":1,"manager":"home"},
        {"id":"project:weather","kind":"project","title":"Weather App","project":"weather","needs_you":2,"manager":"work"}],
        "counts":{}}
        """#)
        let scope = AssistantScope(group: "", current: "work", defaultID: "home", installed: ["home", "work"])
        let store = TodoStore(cachedTodos: [], cacheWriter: nil, hubListChanged: {}, managers: .init(scope: { scope }, naming: { $0 }),
                              v1: .on(api: hub.client))
        defer { store.forget() }
        let places = placesModel(store, TodoWorkspace(active: { true }))
        await store.refresh(api: hub.client)
        try await waitForPlacesTest { !places.entries.isEmpty }
        #expect(places.entries.projects.map(\.id) == ["project:weather"])
        #expect(places.entries.projects.first?.needsYou == 2)
    }
}

/// Whether the test's scene is active.
@MainActor private final class PlacesTestScene {
    var active = true
}

/// Whether the test's hub lists its places.
@MainActor private final class PlacesTestFlag {
    var on = true
}

/// The rows the test's hub lists besides its fixed ones: the to-dos it made.
@MainActor private final class PlacesTestRows {
    var rows: [String] = []
}

/// The outbox the store's numbers follow, made after the store it writes through.
@MainActor @Observable private final class PlacesTestOutbox {
    var outbox: TodoOutbox?
}

/// Holds a fake hub's answer until the test lets it go.
@MainActor private final class PlacesTestRelease {
    private var released = false
    func go() { released = true }
    func wait() async {
        for _ in 0..<500 where !released { try? await Task.sleep(for: .milliseconds(10)) }
    }
}

/// Waits (up to 5 s) for something the store does.
@MainActor
private func waitForPlacesTest(_ condition: @MainActor () -> Bool) async throws {
    for _ in 0..<250 where !condition() { try await Task.sleep(for: .milliseconds(20)) }
    try #require(condition(), "it did not happen in time")
}
