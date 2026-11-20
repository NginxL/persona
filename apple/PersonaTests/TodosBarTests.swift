import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The To-dos list's section bar and its sections (#10465; docs/design/proposals/todos-redesign.md 3.3, 3.4): All lands
/// first, Needs you carries the blue N and always shows, In progress and To do keep their neutral counts at every
/// width, Done only with rows; the calm rule hides the bar; each choice draws its own sections in the hub's order.
struct TodosBarTests {
    private func counts(needsYou: Int = 0, inProgress: Int = 0, todo: Int = 0) -> TodoCounts {
        var sections = Dictionary(uniqueKeysWithValues: TodoListSection.all.map { ($0, 0) })
        sections[TodoListSection.todo] = todo
        return TodoCounts(open: needsYou + inProgress + todo, needsYou: needsYou, inProgress: inProgress, sections: sections)
    }

    @Test func allLandsFirstAndNeedsYouAlwaysShows() {
        let empty = TodosBar.entries(counts(), doneRows: 0, chosen: .all)
        #expect(empty.map(\.choice) == [.all, .needsYou])
        #expect(empty[0].count == nil && empty[1].count == 0 && empty[1].look == .needs, "N hides at 0, the choice stays")
        let full = TodosBar.entries(counts(needsYou: 3, inProgress: 5, todo: 2), doneRows: 1, chosen: .all)
        #expect(full.map(\.choice) == [.all, .needsYou, .inProgress, .todo, .done])
        #expect(full.map(\.count) == [nil, 3, 5, 2, nil])
        #expect(full.map(\.look) == [nil, .needs, .neutral, .neutral, nil])
    }

    /// The bar has no width rule: the counts the prototype drops in a 320-pt column stay, and the strip scrolls
    /// (DESIGN_SYSTEM 3.4, "every filter shows its whole name"; proposal 3.3, "no second pattern by width").
    @Test func everyCountIsKeptInANarrowColumn() {
        for chosen in TodosChoice.allCases {
            let entries = TodosBar.entries(counts(needsYou: 3, inProgress: 5, todo: 2), doneRows: 0, chosen: chosen)
            #expect(entries.first { $0.choice == .inProgress }?.count == 5)
            #expect(entries.first { $0.choice == .todo }?.count == 2)
            #expect(entries.first { $0.choice == .needsYou }?.count == 3)
        }
    }

    @Test func aChosenChoiceStaysWhileItEmpties() {
        let entries = TodosBar.entries(counts(), doneRows: 0, chosen: .done)
        #expect(entries.map(\.choice) == [.all, .needsYou, .done])
        #expect(TodosBar.entries(counts(), doneRows: 0, chosen: .inProgress).map(\.choice) == [.all, .needsYou, .inProgress])
    }

    @Test func theCalmRule() {
        #expect(TodosBar.calm([TodoListSection.todo]))
        #expect(TodosBar.calm([TodoListSection.todo, TodoListSection.someday, TodoListSection.done]), "folded sections never break it")
        #expect(!TodosBar.calm([TodoListSection.todo, TodoListSection.working]), "two sections with rows")
        #expect(!TodosBar.calm([TodoListSection.needsYou]))
        #expect(!TodosBar.calm([TodoListSection.canWait, TodoListSection.todo]), "something for the owner, even uncounted")
        #expect(!TodosBar.calm([]), "an empty list has its empty state")
    }

    @Test func eachChoiceDrawsItsSections() {
        #expect(TodosChoice.all.sections == TodoListSection.all)
        #expect(TodosChoice.needsYou.sections == [TodoListSection.needsYou, TodoListSection.canWait])
        #expect(TodosChoice.inProgress.sections == [TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished])
        #expect(TodosChoice.todo.sections == [TodoListSection.todo, TodoListSection.someday])
        #expect(TodosChoice.done.sections == [TodoListSection.done])
    }
}

/// The list's layout (`TodosListLayout`): sections in the hub's order, folds where proposal 3.4 says, groupings with Needs
/// you on top, the sort inside groups only, and the steps a parent carries never rows of their own.
struct TodosListLayoutTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    private func filed(_ id: String, _ section: String, rank: Int = 0, manager: String? = nil, project: String? = nil,
                       partOf: String? = nil, closed: Bool = false) -> TodoFiled {
        TodoFiled(id: id, manager: manager, section: section, closed: closed || section == TodoListSection.done, rank: rank,
                  inactive: false, byView: true, quiet: false, partOf: partOf, project: project, asks: [])
    }

    private func input(_ rows: [TodoFiled], items: [TodoItem] = []) -> TodosListLayout.Input {
        var all = Dictionary(uniqueKeysWithValues: rows.map { ($0.id, TodoItem(id: $0.id, title: $0.id)) })
        for item in items { all[item.id] = item }
        return TodosListLayout.Input(rows: TodoFiling.ordered(rows), items: all, now: now, calendar: Calendar(identifier: .gregorian))
    }

    /// The prototype's list: Needs you (Can wait folded at its foot), Working, Waiting, To do, Just finished, then
    /// Someday folded at the end; carried steps are no rows.
    @Test func allDrawsTheSectionsInTheHubsOrder() {
        let rows = [filed("t01", TodoListSection.needsYou), filed("t01-s1", TodoListSection.done, partOf: "t01"),
                    filed("t02", TodoListSection.needsYou, rank: 1), filed("t13", TodoListSection.canWait),
                    filed("t05", TodoListSection.working), filed("t04", TodoListSection.waiting),
                    filed("t11", TodoListSection.todo), filed("t07", TodoListSection.justFinished),
                    filed("t14", TodoListSection.someday)]
        let out = TodosListLayout.layout(input(rows))
        #expect(!out.calm)
        #expect(out.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working, TodoListSection.waiting,
                                         TodoListSection.todo, TodoListSection.justFinished])
        #expect(out.groups[0].keys == ["/t01", "/t02"] && out.groups[0].needs)
        #expect(out.groups[0].folds.map(\.id) == [TodoListSection.canWait] && !out.groups[0].folds[0].open)
        #expect(out.groups.last?.folds.map(\.id) == [TodoListSection.someday])
        #expect(out.sections["/t01-s1"] == nil, "a step its parent carries is no row")
        #expect(out.doneRows == 0)
    }

    @Test func choicesDrawTheirOwnSections() {
        let rows = [filed("a", TodoListSection.needsYou), filed("b", TodoListSection.canWait), filed("c", TodoListSection.working),
                    filed("d", TodoListSection.waiting), filed("e", TodoListSection.justFinished), filed("f", TodoListSection.todo),
                    filed("g", TodoListSection.someday)]
        var i = input(rows)
        i.choice = .needsYou
        #expect(TodosListLayout.layout(i).groups.map(\.id) == [TodoListSection.needsYou])
        i.choice = .inProgress
        #expect(TodosListLayout.layout(i).groups.map(\.id) == [TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished])
        i.choice = .todo
        let todo = TodosListLayout.layout(i).groups
        #expect(todo.map(\.id) == [TodoListSection.todo] && todo[0].folds.map(\.id) == [TodoListSection.someday],
                "Someday folded at To do's foot")
    }

    /// While only plain To do rows exist, no headers: one plain group, Someday folded under it.
    @Test func theCalmListHasNoHeaders() {
        let out = TodosListLayout.layout(input([filed("a", TodoListSection.todo), filed("b", TodoListSection.todo, rank: 1),
                                                filed("s", TodoListSection.someday)]))
        #expect(out.calm)
        #expect(out.groups.count == 1 && out.groups[0].title == nil && out.groups[0].keys == ["/a", "/b"])
        #expect(out.groups[0].folds.map(\.id) == [TodoListSection.someday])
    }

    /// The list's order: rank, then manager, then id (`TodoFiling.ordered`); a sort orders inside a section and never
    /// moves one.
    @Test func ranksThenManagersThenIdsAndTheSortStaysInside() {
        let rows = [filed("z", TodoListSection.working, rank: 0, manager: "work"), filed("y", TodoListSection.working, rank: 0, manager: "home"),
                    filed("x", TodoListSection.working, rank: 1, manager: "home"), filed("n", TodoListSection.needsYou, rank: 5)]
        var i = input(rows, items: [TodoItem(id: "x", title: "x", priority: "high")])
        let out = TodosListLayout.layout(i)
        #expect(out.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working])
        #expect(out.groups[1].keys == ["home/y", "work/z", "home/x"])
        i.sort = .priority
        let sorted = TodosListLayout.layout(i)
        #expect(sorted.groups[1].keys == ["home/x", "home/y", "work/z"])
        #expect(sorted.groups.map(\.id) == out.groups.map(\.id))
    }

    @Test func byProjectAndByDueKeepNeedsYouOnTop() {
        let soon = Calendar(identifier: .gregorian).date(byAdding: .day, value: 1, to: now)!
        let rows = [filed("n", TodoListSection.needsYou, project: "kyoto"), filed("a", TodoListSection.working, project: "home"),
                    filed("b", TodoListSection.todo), filed("c", TodoListSection.waiting, project: "kyoto")]
        var i = input(rows, items: [TodoItem(id: "a", title: "a", dueAt: soon)])
        i.grouping = .project
        i.projectName = { $0 == "kyoto" ? "Kyoto trip" : $0 }
        let byProject = TodosListLayout.layout(i)
        #expect(byProject.groups.map(\.id) == [TodoListSection.needsYou, "project:home", "project:kyoto", "project:"])
        #expect(byProject.groups[2].title == "Kyoto trip" && byProject.groups[2].keys == ["/c"])
        i.grouping = .due
        let byDue = TodosListLayout.layout(i)
        #expect(byDue.groups.map(\.id) == [TodoListSection.needsYou, "due:tomorrow", "due:none"])
    }

    /// Done today folds at the end of All; Done alone lists the closed rows by day, newest first.
    @Test func doneIsByDay() {
        let cal = Calendar(identifier: .gregorian)
        let rows = [filed("today", TodoListSection.done), filed("old", TodoListSection.done), filed("t", TodoListSection.todo),
                    filed("w", TodoListSection.working)]
        let items = [TodoItem(id: "today", title: "today", doneAt: now.addingTimeInterval(-60)),
                     TodoItem(id: "old", title: "old", doneAt: cal.date(byAdding: .day, value: -20, to: now))]
        var i = input(rows, items: items)
        let all = TodosListLayout.layout(i)
        #expect(all.doneRows == 2)
        #expect(all.groups.last?.folds.map(\.id) == [TodoListSection.done] && all.groups.last?.folds.last?.keys == ["/today"])
        i.choice = .done
        #expect(TodosListLayout.layout(i).groups.map(\.id) == ["done:today", "done:earlier"])
    }
}

/// Where the owner is in To-dos (#10465): each place's list is put back where it last rested, written only from an
/// active scene; a page that still draws today's list is known until it is gone, so the drawer's number follows it.
@MainActor
struct TodoWorkspaceTests {
    @Test func whereAListRestedIsKeptFromAnActiveSceneOnly() {
        var active = true
        let workspace = TodoWorkspace(active: { active })
        workspace.rested("all", at: "/t05", shown: true)
        #expect(workspace.scrolled(in: "all") == "/t05")
        workspace.rested("all", at: "/t11", shown: false)
        active = false
        workspace.rested("all", at: "/t11", shown: true)
        #expect(workspace.scrolled(in: "all") == "/t05", "a tree on its way out, or a scene going away, moves nothing")
    }

    @Test func aPageOnTodaysListIsKnownUntilItIsGone() {
        let workspace = TodoWorkspace(active: { true })
        let a = UUID(), b = UUID()
        workspace.todayPage(a, open: true)
        workspace.todayPage(b, open: true)
        workspace.todayPage(a, open: false)
        #expect(workspace.holdsToday)
        workspace.todayPage(b, open: false)
        #expect(!workspace.holdsToday)
    }
}
