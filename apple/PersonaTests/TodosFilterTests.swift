import Foundation
import Testing
@testable import Persona
import PersonaWire

/// The To-dos list's Filter and its sections (#10465; docs/design/proposals/todos-redesign.md 3.3, 3.4; DESIGN_SYSTEM 3.4
/// "To-dos list"): its menu always lists all five choices, All first, with how many each would show; a choice that hides
/// what needs the owner says how many in the guard line and the title's subtitle; the calm rule hides it; each choice
/// draws its own sections in the hub's order.
struct TodosFilterTests {
    private let en = TodoWords(locale: Locale(identifier: "en"))

    private func counts(needsYou: Int = 0, inProgress: Int = 0, todo: Int = 0) -> TodoCounts {
        var sections = Dictionary(uniqueKeysWithValues: TodoListSection.all.map { ($0, 0) })
        sections[TodoListSection.todo] = todo
        return TodoCounts(open: needsYou + inProgress + todo, needsYou: needsYou, inProgress: inProgress, sections: sections)
    }

    @Test func everyChoiceIsListedWithWhatItWouldShow() {
        let options = TodosFilter.options(counts(needsYou: 3, inProgress: 5, todo: 2), doneToday: 2, words: en)
        #expect(options.map(\.choice) == [.all, .needsYou, .inProgress, .todo, .done])
        #expect(options.map(\.subtitle) == [nil, "3 to-dos", "5 to-dos", "2 to-dos", "2 today"])
        let empty = TodosFilter.options(counts(), doneToday: 0, words: en)
        #expect(empty.map(\.choice) == TodosChoice.allCases, "none comes and goes: an empty choice stays listed")
        #expect(empty.first { $0.choice == .done }?.subtitle == nil, "Done says nothing when nothing closed today")
        #expect(empty.first { $0.choice == .needsYou }?.subtitle == "0 to-dos")
    }

    /// A choice that leaves Needs you out says how many need the owner: the guard line and the title's subtitle.
    @Test func aChoiceThatHidesWhatNeedsYouSaysSo() {
        #expect(TodosFilter.hiddenNeeds(.all, needsYou: 3) == 0)
        #expect(TodosFilter.hiddenNeeds(.needsYou, needsYou: 3) == 0)
        for choice in [TodosChoice.inProgress, .todo, .done] {
            #expect(TodosFilter.hiddenNeeds(choice, needsYou: 3) == 3, "\(choice)")
            #expect(TodosFilter.hiddenNeeds(choice, needsYou: 0) == 0, "\(choice)")
        }
        #expect(TodosFilter.subtitle(.all, needsYou: 3, words: en) == "")
        // The choice's name is the app's own (the tests run in zh-Hans); what follows it is said in `words`.
        let progress = TodosChoice.inProgress.title
        #expect(TodosFilter.subtitle(.inProgress, needsYou: 3, words: en) == progress + " · 3 need you")
        #expect(TodosFilter.subtitle(.inProgress, needsYou: 1, words: en) == progress + " · 1 needs you")
        #expect(TodosFilter.subtitle(.inProgress, needsYou: 0, words: en) == "", "nothing hidden: the Filter names its choice")
        #expect(TodosFilter.subtitle(.needsYou, needsYou: 3, words: en) == "")
    }

    /// Group by Project is greyed, with why, under a choice that draws one section or the days.
    @Test func groupingIsGreyedWhereItCannotGroup() {
        #expect(TodosFilter.groupingNote(.needsYou, words: en) == "Needs you is one section")
        #expect(TodosFilter.groupingNote(.done, words: en) == "Done is listed by day")
        for choice in [TodosChoice.all, .inProgress, .todo] { #expect(TodosFilter.groupingNote(choice) == nil) }
    }

    @Test(arguments: ["en", "zh-Hans", "ja"]) func theFiltersWordsInEveryLanguage(_ language: String) {
        let words = TodoWords(locale: Locale(identifier: language))
        let said = TodosFilter.options(counts(needsYou: 3, inProgress: 5, todo: 2), doneToday: 2, words: words).compactMap(\.subtitle)
            + [TodosFilter.subtitle(.todo, needsYou: 2, words: words).replacingOccurrences(of: TodosChoice.todo.title, with: ""),
               TodosFilter.groupingNote(.needsYou, words: words) ?? "", TodosFilter.groupingNote(.done, words: words) ?? ""]
        for text in said {
            #expect(!text.isEmpty && !text.contains("["), "\(language): \(text)")
            if language != "en" { #expect(text.unicodeScalars.contains { $0.value >= 0x3040 }, "\(language): \(text)") }
        }
    }

    @Test func theCalmRule() {
        #expect(TodosFilter.calm([TodoListSection.todo]))
        #expect(TodosFilter.calm([TodoListSection.todo, TodoListSection.someday, TodoListSection.done]), "folded sections never break it")
        #expect(!TodosFilter.calm([TodoListSection.todo, TodoListSection.working]), "two sections with rows")
        #expect(!TodosFilter.calm([TodoListSection.needsYou]))
        #expect(!TodosFilter.calm([TodoListSection.canWait, TodoListSection.todo]), "something for the owner, even uncounted")
        #expect(!TodosFilter.calm([]), "an empty list has its empty state")
    }

    @Test func eachChoiceDrawsItsSections() {
        #expect(TodosChoice.all.sections == TodoListSection.all)
        #expect(TodosChoice.needsYou.sections == [TodoListSection.needsYou, TodoListSection.canWait])
        #expect(TodosChoice.inProgress.sections == [TodoListSection.working, TodoListSection.waiting, TodoListSection.justFinished])
        #expect(TodosChoice.todo.sections == [TodoListSection.todo, TodoListSection.someday])
        #expect(TodosChoice.done.sections == [TodoListSection.done])
    }
}

/// The list's layout (`TodosListLayout`): sections in the hub's order, one card each, folds where proposal 3.4 says (Can
/// wait inside Needs you's card, Someday and Done today folds of their own after the last), the sort inside sections
/// only and never inside Needs you, Group by Project with Needs you on top, the steps a parent carries never rows of
/// their own, and the search.
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
    /// Someday folded after them, a group of its own; carried steps are no rows.
    @Test func allDrawsTheSectionsInTheHubsOrder() throws {
        let rows = [filed("t01", TodoListSection.needsYou), filed("t01-s1", TodoListSection.done, partOf: "t01"),
                    filed("t02", TodoListSection.needsYou, rank: 1), filed("t13", TodoListSection.canWait),
                    filed("t05", TodoListSection.working), filed("t04", TodoListSection.waiting),
                    filed("t11", TodoListSection.todo), filed("t07", TodoListSection.justFinished),
                    filed("t14", TodoListSection.someday)]
        let out = TodosListLayout.layout(input(rows))
        #expect(!out.calm)
        #expect(out.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working, TodoListSection.waiting,
                                         TodoListSection.todo, TodoListSection.justFinished, "tail:someday"])
        #expect(out.groups[0].keys == ["/t01", "/t02"] && out.groups[0].needs)
        #expect(out.groups[0].folds.map(\.id) == [TodoListSection.canWait] && !out.groups[0].folds[0].open)
        #expect(out.groups[4].folds.isEmpty, "the last card keeps no fold of the tail's")
        let someday = try #require(out.groups.last?.tail)
        #expect(someday.id == TodoListSection.someday && someday.keys == ["/t14"] && !someday.open)
        #expect(out.groups.last?.keys == [] && out.groups.last?.title == nil, "its rows only while open, under the fold itself")
        #expect(out.sections["/t01-s1"] == nil, "a step its parent carries is no row")
        #expect(out.doneToday == 0)
    }

    @Test func anOutsideDecisionKeepsItsOwnRowWithAnOpenParent() {
        let rows = [filed("p", TodoListSection.needsYou), filed("todo_od_1", TodoListSection.needsYou, partOf: "p")]
        let out = TodosListLayout.layout(input(rows))
        #expect(out.groups.map(\.id) == [TodoListSection.needsYou])
        #expect(out.groups[0].keys == ["/p", "/todo_od_1"] && out.groups[0].count == 2)
        #expect(out.sections["/todo_od_1"] == TodoListSection.needsYou, "the request is associated with the parent, not carried as its step")
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
        #expect(todo.map(\.id) == [TodoListSection.todo, "tail:someday"] && todo[0].folds.isEmpty, "Someday folded after To do")
    }

    /// While only plain To do rows exist, no headers: one plain card, Someday folded after it.
    @Test func theCalmListHasNoHeaders() {
        let out = TodosListLayout.layout(input([filed("a", TodoListSection.todo), filed("b", TodoListSection.todo, rank: 1),
                                                filed("s", TodoListSection.someday)]))
        #expect(out.calm)
        #expect(out.groups.count == 2 && out.groups[0].title == nil && out.groups[0].keys == ["/a", "/b"])
        #expect(out.groups[1].tail?.id == TodoListSection.someday)
    }

    /// An open tail fold draws its rows; its group's keys stay empty, its rows are the fold's.
    @Test func aTailFoldOpensInPlace() {
        var i = input([filed("w", TodoListSection.working), filed("s", TodoListSection.someday)])
        i.unfolded = [TodoListSection.someday]
        let out = TodosListLayout.layout(i)
        #expect(out.groups.last?.tail?.open == true && out.groups.last?.drawnKeys == ["/s"])
    }

    /// The list's order: rank, then manager, then id (`TodoFiling.ordered`); a sort orders inside a section and never
    /// moves one.
    @Test func ranksThenManagersThenIdsAndTheSortStaysInside() {
        let soon = Calendar(identifier: .gregorian).date(byAdding: .day, value: 1, to: now)!
        let rows = [filed("z", TodoListSection.working, rank: 0, manager: "work"), filed("y", TodoListSection.working, rank: 0, manager: "home"),
                    filed("x", TodoListSection.working, rank: 1, manager: "home"), filed("n", TodoListSection.needsYou, rank: 5)]
        var i = input(rows, items: [TodoItem(id: "x", title: "x", dueAt: soon), TodoItem(id: "z", title: "z", priority: "high"),
                                    TodoItem(id: "y", title: "y", priority: "low")])
        let out = TodosListLayout.layout(i)
        #expect(out.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.working])
        #expect(out.groups[1].keys == ["home/y", "work/z", "home/x"])
        i.sort = .due
        let sorted = TodosListLayout.layout(i)
        #expect(sorted.groups[1].keys == ["home/x", "home/y", "work/z"], "the dated first, the rest in the hub's order")
        #expect(sorted.groups.map(\.id) == out.groups.map(\.id))
        i.sort = .priority
        #expect(TodosListLayout.layout(i).groups[1].keys == ["work/z", "home/x", "home/y"], "high, then normal, then low")
    }

    /// Needs you and Can wait keep the hub's order (its deadlines first) under every sort.
    @Test func noSortReordersWhatNeedsYou() {
        let rows = [filed("a", TodoListSection.needsYou, rank: 0), filed("b", TodoListSection.needsYou, rank: 1),
                    filed("c", TodoListSection.canWait, rank: 0), filed("d", TodoListSection.canWait, rank: 1)]
        var i = input(rows, items: [TodoItem(id: "b", title: "b", dueAt: now, priority: "high"),
                                    TodoItem(id: "d", title: "d", dueAt: now, priority: "high")])
        i.unfolded = [TodoListSection.canWait]
        for sort in TodosListSort.allCases {
            i.sort = sort
            let needs = TodosListLayout.layout(i).groups[0]
            #expect(needs.keys == ["/a", "/b"] && needs.folds.first?.keys == ["/c", "/d"], "\(sort)")
        }
    }

    /// Group by Project: Needs you stays a section at the top, then a group per project where its first row comes, then
    /// the rows of none; a sort never regroups; the calm list and Done are never grouped.
    @Test func groupByProjectKeepsNeedsYouOnTop() {
        let rows = [filed("n", TodoListSection.needsYou, project: "kyoto"), filed("a", TodoListSection.working, project: "home"),
                    filed("b", TodoListSection.todo), filed("c", TodoListSection.waiting, project: "kyoto")]
        var i = input(rows)
        for sort in TodosListSort.allCases {
            i.sort = sort
            #expect(TodosListLayout.layout(i).groups.map(\.id)
                == [TodoListSection.needsYou, TodoListSection.working, TodoListSection.waiting, TodoListSection.todo], "\(sort)")
        }
        i.sort = .suggested
        i.byProject = true
        i.projectName = { $0 == "kyoto" ? "Kyoto trip" : $0 }
        let grouped = TodosListLayout.layout(i)
        #expect(grouped.groups.map(\.id) == [TodoListSection.needsYou, "project:home", "project:kyoto", "project:"])
        #expect(grouped.groups[0].keys == ["/n"], "Needs you keeps its rows")
        #expect(grouped.groups[2].title == "Kyoto trip" && grouped.groups[2].keys == ["/c"] && grouped.groups[2].count == 1)
        var calm = input([filed("t", TodoListSection.todo, project: "home")])
        calm.byProject = true
        #expect(TodosListLayout.layout(calm).groups.map(\.id) == ["plain"], "the calm list is never grouped")
    }

    /// Closed rows show neither a due day nor a priority, so no sort reorders them: the newest first in Done today and in
    /// Done's days, whatever the sort.
    @Test func closedRowsKeepTheirNewestFirst() {
        let rows = [filed("old", TodoListSection.done), filed("new", TodoListSection.done), filed("t", TodoListSection.todo),
                    filed("w", TodoListSection.working)] // not the calm list, which has no Filter
        var i = input(rows, items: [TodoItem(id: "old", title: "old", doneAt: now.addingTimeInterval(-3600), dueAt: now, priority: "high"),
                                    TodoItem(id: "new", title: "new", doneAt: now.addingTimeInterval(-60), priority: "low")])
        for sort in TodosListSort.allCases {
            i.sort = sort
            i.choice = .all
            #expect(TodosListLayout.layout(i).groups.last?.tail?.keys == ["/new", "/old"], "Done today, \(sort)")
            i.choice = .done
            #expect(TodosListLayout.layout(i).groups.first?.keys == ["/new", "/old"], "Done, \(sort)")
        }
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
        #expect(all.doneToday == 1)
        #expect(all.groups.last?.tail?.id == TodoListSection.done && all.groups.last?.tail?.keys == ["/today"])
        i.choice = .done
        #expect(TodosListLayout.layout(i).groups.map(\.id) == ["done:today", "done:earlier"])
    }
}

extension TodosListLayoutTests {
    /// The search looks through the open rows' titles and notes, case and accents aside, whatever the choice: the
    /// sections stay, every fold opens, the counts are what matches, and closed to-dos wait for the search's next step.
    @Test func theSearchKeepsTheSectionsAndOpensTheFolds() {
        let rows = [filed("k", TodoListSection.needsYou), filed("c", TodoListSection.canWait), filed("w", TodoListSection.working),
                    filed("t", TodoListSection.todo), filed("s", TodoListSection.someday), filed("d", TodoListSection.done)]
        var i = input(rows, items: [TodoItem(id: "k", title: "Plan the Kyoto trip"), TodoItem(id: "c", title: "Pick a ryokan in KYOTO"),
                                    TodoItem(id: "w", title: "Draft the newsletter"),
                                    TodoItem(id: "t", title: "Renew passport", notes: "Kyōto office closes at 5"),
                                    TodoItem(id: "s", title: "Kyoto temples, someday"),
                                    TodoItem(id: "d", title: "Kyoto hotel booked", doneAt: now)])
        i.choice = .inProgress
        i.query = "  kyoto "
        let out = TodosListLayout.layout(i)
        #expect(out.searching)
        #expect(out.groups.map(\.id) == [TodoListSection.needsYou, TodoListSection.todo, "tail:someday"], "the choice set aside")
        #expect(out.groups[0].count == 1 && out.groups[0].folds.first?.open == true && out.groups[0].folds.first?.keys == ["/c"])
        #expect(out.groups[1].keys == ["/t"], "a match in the notes, its accent aside")
        #expect(out.groups[2].tail?.open == true)
        #expect(!out.groups.flatMap(\.drawnKeys).contains("/d"), "closed to-dos come with the search's next step")
        i.query = "nothing like it"
        #expect(TodosListLayout.layout(i).groups.isEmpty)
    }
}

/// Where the owner is in To-dos (#10465): each place's list is put back where it last rested, written only from an
/// active scene; a page that still draws today's list is known until it is gone, so the drawer's number follows it.
@MainActor
struct TodoWorkspaceTests {
    @Test func whereAListRestedIsKeptFromAnActiveSceneOnly() {
        let scene = TodoSceneFlag()
        let workspace = TodoWorkspace(active: { scene.active })
        workspace.rested("all", at: "/t05", shown: true)
        #expect(workspace.scrolled(in: "all") == "/t05")
        workspace.rested("all", at: "/t11", shown: false)
        scene.active = false
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

/// Whether the test's scene is active, as the workspace asks it.
@MainActor private final class TodoSceneFlag {
    var active = true
}

/// The list's container (#10465; DESIGN_SYSTEM 3.4 "To-dos list"): section cards through the design system's one
/// modifier, and no row background of the list's own, which would erase the cards in Clay, Island and Lark (as
/// `themedRow()` did) and lose the system's swipe shape. Reads the list's sources next to this file.
struct TodoListContainerTests {
    private static let list = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "Persona/Features/Todos/List")

    private static func source(_ name: String) throws -> String {
        try String(contentsOf: list.appending(path: name), encoding: .utf8)
    }

    @Test func theListWearsSectionCardsAndTheSystemsCells() throws {
        let view = try Self.source("TodoListView.swift")
        let rows = try Self.source("TodoListActions.swift") + Self.source("TodoListRow.swift")
        #expect(view.contains(".sectionCards()"), "the container comes from the design system")
        #expect(!view.contains(".listStyle("), "one place chooses the container: `sectionCards()`")
        for (name, text) in [("TodoListView", view), ("rows", rows)] {
            #expect(!text.contains(".themedRow()"), "\(name): a clear row background erases the cards in Clay, Island and Lark")
        }
        #expect(!rows.contains("listRowBackground"), "rows keep the system's cells")
    }
}
