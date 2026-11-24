import Foundation
import PersonaWire

/// The mark before a row's second line (proposal 3.4): ● needs you, ◐ working, ✓ just finished, ◷ waiting; To do and the
/// rest have none.
nonisolated enum TodoMark: Equatable, Sendable { case needs, working, ready, waiting }

/// What a row draws, worked out in the list's model when the row's facts, view or the clock change, never in a view's
/// body (docs/apple/performance.md P4).
nonisolated struct TodoRowContent: Equatable, Sendable {
    var id: String
    var title: String
    /// A high-priority to-do: "!" before its title in the caution tone, as Reminders marks it; spoken "High priority".
    var highPriority = false
    /// The tick drawn ticked: the to-do is closed.
    var closed = false
    /// At the title's end, in words that say what they are: "Due Fri", or how long ago it came to where it is or closed
    /// ("16m ago"); none when line 2 says a time.
    var trailing: String?
    var overdue = false
    /// Finished work nobody has seen: the unread dot before `trailing`.
    var unseen = false
    var mark: TodoMark?
    /// Line 2 in words; two asks or more end it with how many ("· 2 asks").
    var said: TodoLineText.Said?
    /// How many asks hold it in Needs you.
    var asks = 0
    /// Line 3, in caption: what line 2 and the grouping do not say (its steps, the Inactive mark, with its project), or
    /// nil.
    var caption: String?
    var inactive = false
    /// Ticked a moment ago (`TodoOutbox`): struck through, the tick drawn ticked, until the hub has it closed.
    var struck = false
    /// Made on this device and not on the hub yet: line 2 says so, and the row neither opens nor has actions but Try
    /// Again and Remove when `unsure`.
    var saving = false
    /// A new to-do nobody knows the hub made: "Not sure it was saved", with Try Again and Remove.
    var unsure = false
    /// Line 2 is this device's own word on the owner's write ("Saving…", "Not on your hub yet"), in the tertiary color.
    var unsaved = false
    /// Persona or an Agent works on it: the tick asks first ("Mark it done and stop?").
    var working = false
    /// An Agent's question, plan or tool waits for the owner on it: the tick asks first too.
    var agentWaits = false
    /// In Someday: its swipe and menu take it out again.
    var someday = false
    /// Finished and seen, still open ("Finished Tue"): line 2 ends in Mark Done.
    var markDone = false
    /// The row in words, for VoiceOver: the title, the state its mark draws, its line (with how many asks), line 3, then
    /// its time said in full ("Due Saturday", "16 minutes ago").
    var spoken = ""
    /// The tick's value: Done or Not done (its label is the title).
    var tickValue = ""
}

/// What a To-dos row offers besides opening it (#10465): its swipe, its long press and VoiceOver's actions.
nonisolated enum TodoRowAction: String, Equatable, Sendable {
    case done, someday, outOfSomeday, undo, reopen, tryAgain, remove
    case today, date, project, copyLink, delete
}

/// Which of them a row offers, from its content, so the swipe, the menu and VoiceOver's actions agree.
nonisolated enum TodoRowActionList {
    /// The trailing swipe, the full swipe first: Done and Someday (Remove from Someday in Someday); Undo on a row ticked a
    /// moment ago; Reopen on a closed one; Try Again and Remove on a new one nobody knows the hub made; none while a new
    /// one is being saved.
    static func swipe(_ c: TodoRowContent) -> [TodoRowAction] {
        if c.unsure { return [.tryAgain, .remove] }
        if c.saving { return [] }
        if c.struck { return [.undo] }
        if c.closed { return [.reopen] }
        return [.done, c.someday ? .outOfSomeday : .someday]
    }

    /// The long press: Undo or Reopen first where the swipe has it, else Today, Date…, Project and Someday; then Copy
    /// Link and Delete….
    static func menu(_ c: TodoRowContent) -> [TodoRowAction] {
        if c.unsure { return [.tryAgain, .remove] }
        if c.saving { return [] }
        if c.struck { return [.undo, .copyLink, .delete] }
        if c.closed { return [.reopen, .copyLink, .delete] }
        return [.today, .date, .project, c.someday ? .outOfSomeday : .someday, .copyLink, .delete]
    }

    /// VoiceOver's and Switch Control's actions on the row: the swipe's, then Delete…; the menu's other items are its
    /// "Show menu" on the same element.
    static func spoken(_ c: TodoRowContent) -> [TodoRowAction] {
        let swipe = swipe(c)
        return swipe.isEmpty || c.saving ? swipe : swipe + [.delete]
    }
}

/// The owner's write on a row still on this device (`TodoOutbox`), as the row says it.
nonisolated struct TodoRowWrite: Equatable, Sendable {
    var write: TodoOutbox.Write
    /// It could not reach the hub yet, or nobody knows whether it did: it goes again when the hub next answers.
    var waits = false
    /// A new to-do nobody knows the hub made.
    var unsure = false
}

/// The order inside each section (⋯ › Sort By, kept on the device as `todos.v1.sort`): the hub's, by its rank, unless
/// the owner picks the most recently changed first (the old page's order), the earliest due first (the one way to order
/// Working and Waiting by date) or the high-priority ones first, which their rows mark. A sort never moves a section, and
/// Needs you and Can wait keep the hub's order, its deadlines first, and closed rows their newest first, under every
/// sort. A kept value this build does not offer reads as Suggested.
nonisolated enum TodosListSort: String, CaseIterable, Identifiable, Sendable {
    case suggested, activity, due, priority
    var id: String { rawValue }

    var title: String {
        switch self {
        case .suggested: String(localized: "Suggested [To-dos Sort]")
        case .activity: String(localized: "Recently Changed [To-dos Sort]")
        case .due: String(localized: "Due Date [To-dos Sort]")
        case .priority: String(localized: "Priority [To-dos Sort]")
        }
    }
}

/// One block of the list, one card: a section with its header (or, in the calm list, none), its rows and the folds at
/// its foot ("Can wait · 1"); or a tail group (Someday, Done today) whose header is its fold, its rows drawn only while
/// it is open.
nonisolated struct TodosListGroup: Identifiable, Equatable, Sendable {
    nonisolated struct Fold: Identifiable, Equatable, Sendable {
        /// The section it folds: `TodoListSection.canWait`, `someday` or `done`.
        var id: String
        var title: String
        var keys: [String]
        var open: Bool
    }

    var id: String
    /// nil: no header.
    var title: String?
    var count = 0
    /// The count in the needs-you tone (Needs you's N); the others neutral.
    var needs = false
    var keys: [String]
    var folds: [Fold] = []
    /// A tail group's fold: its header, and its rows while open.
    var tail: Fold?

    /// The keys it draws: its rows, then those of its open folds.
    var drawnKeys: [String] {
        keys + folds.filter(\.open).flatMap(\.keys) + (tail.map { $0.open ? $0.keys : [] } ?? [])
    }
}

/// The list as drawn: its groups for the Filter's choice, the search, the sort and ⋯'s Group by Project, worked out once
/// per change of the filing (`TodoCountsSource.filed`) or of a choice, in the model (P4). Pure.
nonisolated enum TodosListLayout {
    struct Input {
        /// The current assistant's rows as filed, in the list's order.
        var rows: [TodoFiled]
        var items: [String: TodoItem]
        var choice: TodosChoice = .all
        /// ⋯'s Group by Project: below Needs you, a group per project in place of the other sections (never in the calm
        /// list, nor under Needs You or Done, which draw one section or the days).
        var byProject = false
        var sort: TodosListSort = .suggested
        /// The folds the owner opened.
        var unfolded: Set<String> = []
        /// The search field's words: while there are any, the list is All's open rows whose title or notes hold them,
        /// every fold open, and the choice waits until the field is cleared.
        var query = ""
        var now = Date()
        var calendar = Calendar.current
        var projectName: (String) -> String = { $0 }
    }

    struct Output: Equatable {
        var groups: [TodosListGroup] = []
        /// The calm rule holds: no Filter, no headers.
        var calm = false
        /// The ones closed today (the Filter's Done says how many).
        var doneToday = 0
        /// The search field has words: the rows are what they match.
        var searching = false
        /// Every row's section, by key: what the hold compares to tell a new need apart.
        var sections: [String: String] = [:]
        static let empty = Output()
    }

    /// A row's identity in the list: its manager and its id, unique in a merged list (and the list's tie-breakers after
    /// the rank, `TodoFiling.ordered`).
    static func key(_ row: TodoFiled) -> String { (row.manager ?? "") + "/" + row.id }

    /// The sections below Needs you, in the list's order: the ones Group by Project files under their projects'
    /// headings. Needs you heads the list and Someday and Done today fold after the last card, so no project heading
    /// names their rows' projects.
    static let underProjects = [TodoListSection.working, TodoListSection.waiting, TodoListSection.todo,
                                TodoListSection.justFinished]

    static func layout(_ input: Input) -> Output {
        let carried = TodoCountsFallback.carried(input.rows)
        var rows = input.rows.filter { !carried.contains($0.id) }
        var out = Output()
        for row in rows { out.sections[key(row)] = row.section }
        let closedToday = { (row: TodoFiled) in
            input.items[row.id]?.doneAt.map { input.calendar.isDate($0, inSameDayAs: input.now) } ?? false
        }
        out.doneToday = rows.count(where: { $0.section == TodoListSection.done && closedToday($0) })
        let present = Set(rows.map(\.section))
        out.calm = TodosFilter.calm(Array(present))
        // A search looks through every open row whatever the choice, its folds open; closed to-dos and the hub's own
        // matches come with the search's next step in #10465.
        let query = input.query.trimmingCharacters(in: .whitespacesAndNewlines)
        out.searching = !query.isEmpty
        if out.searching {
            rows = rows.filter { $0.section != TodoListSection.done && input.items[$0.id].map { matches($0, query) } == true }
        }
        let choice: TodosChoice = out.calm || out.searching ? .all : input.choice
        let opened = { (section: String) in out.searching || input.unfolded.contains(section) }
        let shown = Set(choice.sections)
        var bySection: [String: [TodoFiled]] = [:]
        for row in rows where shown.contains(row.section) { bySection[row.section, default: []].append(row) }
        // What needs the owner keeps the hub's order (its deadlines first) under every sort, and what closed its newest
        // first, since a closed row shows neither a due day nor its priority; the sort orders the rest.
        let hubsOrder: Set<String> = [TodoListSection.needsYou, TodoListSection.canWait]
        func sorted(_ list: [TodoFiled]) -> [String] {
            if list.allSatisfy({ hubsOrder.contains($0.section) }) { return list.map(key) }
            if list.allSatisfy({ $0.section == TodoListSection.done }) { return newestFirst(list, input).map(key) }
            return order(list, input).map(key)
        }
        func fold(_ section: String, title: String, _ list: [TodoFiled]) -> TodosListGroup.Fold? {
            list.isEmpty ? nil : .init(id: section, title: title, keys: sorted(list), open: opened(section))
        }
        func tail(_ fold: TodosListGroup.Fold?) -> TodosListGroup? {
            fold.map { TodosListGroup(id: "tail:" + $0.id, keys: [], tail: $0) }
        }
        let canWait = fold(TodoListSection.canWait, title: String(localized: "Can wait [To-dos List]"),
                           bySection[TodoListSection.canWait] ?? [])
        let someday = fold(TodoListSection.someday, title: StateName.todoSomeday, bySection[TodoListSection.someday] ?? [])
        let doneToday = fold(TodoListSection.done, title: String(localized: "Done today [To-dos List]"),
                             (bySection[TodoListSection.done] ?? []).filter(closedToday))

        if out.calm {
            // One headerless card; Someday and Done today close the list as their own folds.
            out.groups = [TodosListGroup(id: "plain", keys: sorted(bySection[TodoListSection.todo] ?? []))]
                + [tail(someday), tail(doneToday)].compactMap { $0 }
            return out
        }
        if choice == .done {
            out.groups = byDay(bySection[TodoListSection.done] ?? [], input)
            return out
        }

        var groups: [TodosListGroup] = []
        // Needs you heads the list in either grouping, Can wait folded at its foot, inside its card.
        let needs = bySection[TodoListSection.needsYou] ?? []
        if !needs.isEmpty || canWait != nil {
            // Its header says N, as the drawer does (the page reads it from the store, never held); while searching, what
            // matches.
            groups.append(TodosListGroup(id: TodoListSection.needsYou, title: StateName.todoNeedsYou, count: needs.count,
                                         needs: true, keys: sorted(needs), folds: [canWait].compactMap { $0 }))
        }
        let rest = underProjects.filter(shown.contains)
        if input.byProject {
            groups += byProject(rest.flatMap { bySection[$0] ?? [] }, input)
        } else {
            for section in rest {
                let list = bySection[section] ?? []
                if !list.isEmpty { groups.append(TodosListGroup(id: section, title: title(section), count: list.count, keys: sorted(list))) }
            }
        }
        // Someday, then (under All) Done today: folds of their own after the last card.
        groups += [tail(someday), choice == .all ? tail(doneToday) : nil].compactMap { $0 }
        out.groups = groups
        return out
    }

    /// Whether a to-do's title or notes hold `query`, as the system's search matches: case, accents and width aside.
    static func matches(_ item: TodoItem, _ query: String) -> Bool {
        item.title.localizedStandardContains(query) || (item.notes?.localizedStandardContains(query) ?? false)
    }

    static func title(_ section: String) -> String {
        switch section {
        case TodoListSection.needsYou: StateName.todoNeedsYou
        case TodoListSection.canWait: String(localized: "Can wait [To-dos List]")
        case TodoListSection.working: StateName.taskWorking
        case TodoListSection.waiting: StateName.taskWaiting.text
        case TodoListSection.todo: StateName.todoToDo
        case TodoListSection.justFinished: StateName.justFinished.text
        case TodoListSection.someday: StateName.todoSomeday
        default: StateName.done.text
        }
    }

    /// `list` in the sort's order; the hub's (as filed) for Suggested and for every tie.
    static func order(_ list: [TodoFiled], _ input: Input) -> [TodoFiled] {
        let item = { (row: TodoFiled) in input.items[row.id] }
        switch input.sort {
        case .suggested:
            return list
        case .activity:
            return stable(list) { a, b in
                let x = item(a).flatMap { $0.updatedAt ?? $0.createdAt } ?? .distantPast
                let y = item(b).flatMap { $0.updatedAt ?? $0.createdAt } ?? .distantPast
                return x == y ? nil : x > y
            }
        case .due:
            return stable(list) { a, b in
                let x = item(a).flatMap { TodosLogic.dueBy($0, calendar: input.calendar) } ?? .distantFuture
                let y = item(b).flatMap { TodosLogic.dueBy($0, calendar: input.calendar) } ?? .distantFuture
                return x == y ? nil : x < y
            }
        case .priority:
            // High, then normal (none said), then low; the hub's order inside each.
            let weight = { (row: TodoFiled) in ["high": 0, "low": 2][item(row)?.priority ?? ""] ?? 1 }
            return stable(list) { a, b in weight(a) == weight(b) ? nil : weight(a) < weight(b) }
        }
    }

    /// Sorted by `before` (nil: a tie), ties kept in the order given.
    private static func stable(_ list: [TodoFiled], _ before: (TodoFiled, TodoFiled) -> Bool?) -> [TodoFiled] {
        list.enumerated().sorted { a, b in before(a.element, b.element) ?? (a.offset < b.offset) }.map(\.element)
    }

    /// A group per project, where its first row comes in the list, then the rows of none.
    private static func byProject(_ list: [TodoFiled], _ input: Input) -> [TodosListGroup] {
        var slugs: [String] = []
        var rows: [String: [TodoFiled]] = [:]
        for row in list {
            let slug = row.project ?? ""
            if rows[slug] == nil { slugs.append(slug) }
            rows[slug, default: []].append(row)
        }
        slugs = slugs.filter { !$0.isEmpty } + (rows[""] == nil ? [] : [""])
        return slugs.map { slug in
            let members = order(rows[slug] ?? [], input).map(key)
            return TodosListGroup(id: "project:" + slug,
                                  title: slug.isEmpty ? String(localized: "No project [To-dos List]") : input.projectName(slug),
                                  count: members.count, keys: members)
        }
    }

    /// Closed rows by when they closed, the newest first; ties in the order given.
    private static func newestFirst(_ list: [TodoFiled], _ input: Input) -> [TodoFiled] {
        stable(list) { a, b in
            let x = input.items[a.id]?.doneAt ?? .distantPast, y = input.items[b.id]?.doneAt ?? .distantPast
            return x == y ? nil : x > y
        }
    }

    /// Done by when each closed: today, yesterday, this week, earlier; in each, the newest first under every sort.
    private static func byDay(_ list: [TodoFiled], _ input: Input) -> [TodosListGroup] {
        let cal = input.calendar
        let today = cal.startOfDay(for: input.now)
        let days: [(id: String, title: String)] = [
            ("today", String(localized: "Today [To-dos Done]")), ("yesterday", String(localized: "Yesterday [To-dos Done]")),
            ("week", String(localized: "This week [To-dos Done]")), ("earlier", String(localized: "Earlier [To-dos Done]")),
        ]
        var rows: [String: [TodoFiled]] = [:]
        for row in list {
            let at = input.items[row.id]?.doneAt ?? .distantPast
            let ago = cal.dateComponents([.day], from: cal.startOfDay(for: at), to: today).day ?? 99
            let id = ago <= 0 ? "today" : ago == 1 ? "yesterday" : ago <= 6 ? "week" : "earlier"
            rows[id, default: []].append(row)
        }
        return days.compactMap { d in
            guard let members = rows[d.id], !members.isEmpty else { return nil }
            let ordered = newestFirst(members, input)
            return TodosListGroup(id: "done:" + d.id, title: d.title, count: ordered.count, keys: ordered.map(key))
        }
    }
}

/// A row's content from its facts and its view (#10465): the trailing time, the mark, the line in words, line 3 and the
/// row in words. Pure.
nonisolated enum TodoRowContentLogic {
    /// `pending`: the owner's write on it still on the device (`TodoOutbox`): a tick is drawn at once, struck through; a
    /// new to-do says it is being saved; one that could not reach the hub says so.
    /// `inProject`: the place shown is the row's project, whose title names it, so line 3 leaves it out.
    /// `underItsDay`: the row is under a heading that names its due day (Upcoming), so it says only its time, if any;
    /// an overdue one, under Overdue, keeps its day. VoiceOver still says the day, the row being heard on its own.
    static func content(_ filed: TodoFiled, item: TodoItem, view: TodoSummary?, byProject: Bool = false, inProject: Bool = false,
                        underItsDay: Bool = false, now: Date, words: TodoWords = TodoWords(), calendar: Calendar = .current,
                        projectName: (String) -> String = { $0 }, pending: TodoRowWrite? = nil) -> TodoRowContent {
        var c = TodoRowContent(id: filed.id, title: item.title, closed: filed.closed, inactive: filed.inactive)
        c.highPriority = item.priority == "high"
        let since = view?.since.flatMap(JSON.parseDate)
        // The time as drawn at the title's end, and as VoiceOver says it; `aged`: it says how long ago, which a line that
        // says a time ("since Tue") already does.
        var heard: [String] = []
        var aged = false
        if let due = TodosLogic.dueBy(item, calendar: calendar), !filed.closed {
            let day = item.dueAt ?? TodosLogic.dueDay(item, calendar: calendar) ?? due
            c.overdue = due < now
            if underItsDay && !c.overdue {
                c.trailing = item.dueAt.map { TodoTimeWords.dueTime($0, words: words, calendar: calendar) }
            } else {
                c.trailing = TodoTimeWords.due(day, now: now, words: words, calendar: calendar)
            }
            if c.overdue { heard.append(words("Overdue [To-dos Spoken]")) }
            heard.append(TodoTimeWords.spokenDue(day, now: now, words: words, calendar: calendar))
        } else if filed.closed, let done = item.doneAt {
            c.trailing = TodoTimeWords.age(done, now: now, words: words, calendar: calendar)
            heard.append(now.timeIntervalSince(done) < 60 ? words("Done just now [To-dos Spoken]")
                : words("Done \(TodoTimeWords.spokenAge(done, now: now, words: words, calendar: calendar)) [To-dos Spoken]"))
            aged = true
        } else if ![TodoListSection.todo, TodoListSection.someday].contains(filed.section),
                  let at = since ?? item.updatedAt ?? item.createdAt {
            c.trailing = TodoTimeWords.age(at, now: now, words: words, calendar: calendar)
            heard.append(TodoTimeWords.spokenAge(at, now: now, words: words, calendar: calendar))
            aged = true
        }
        c.unseen = filed.section == TodoListSection.justFinished && view?.seen != true
        c.mark = mark(filed, view: view)
        if let view {
            c.said = TodoLineText.said(view.line, since: since, closedReason: item.doneReason.flatMap(firstLine), now: now,
                                       words: words, calendar: calendar)
            c.asks = view.asks?.count ?? 0
        }
        // A row filed by the app's own rules (an older manager's), and one whose line this build cannot say (a newer
        // manager's key), say their section's word: the mark is drawn on line 2, and Working must not lose its ◐.
        if c.said == nil { c.said = legacy(filed) }
        // The row is its own door to its asks (no Review button): two or more say how many at line 2's end.
        if c.asks > 1, [TodoListSection.needsYou, TodoListSection.canWait].contains(filed.section), var said = c.said {
            said.rest = [said.rest, words("\(c.asks) asks [To-dos List]")].compactMap { $0 }.joined(separator: TodoTimeWords.separator)
            c.said = said
        }
        if aged, c.said?.timed == true {
            c.trailing = nil
            heard.removeAll()
        }
        // A plain To do says the first line of its notes, which every client has (the hub sends no line for it).
        if c.said == nil, [TodoListSection.todo, TodoListSection.someday].contains(filed.section),
           let notes = item.notes.flatMap(firstLine) {
            c.said = TodoLineText.Said(rest: notes)
        }
        var facts: [String] = []
        if filed.inactive { facts.append(StateName.todoInactive) }
        if let steps = view?.steps, steps.total > 0 { facts.append(words("Steps \(steps.done)/\(steps.total) [To-dos List]")) }
        // The project only beside another fact: alone it is not worth a line, and Group by Project's heading says it for
        // the rows filed under it, never Needs you's at the top nor those of the Someday and Done today folds; a
        // project's place says it in its title for every row.
        let underItsProject = inProject || byProject && TodosListLayout.underProjects.contains(filed.section)
        if !facts.isEmpty, !underItsProject, let project = item.project, !project.isEmpty {
            facts.insert(projectName(project), at: 0)
        }
        c.caption = facts.isEmpty ? nil : facts.joined(separator: TodoTimeWords.separator)
        c.working = filed.section == TodoListSection.working && !filed.closed
        c.agentWaits = !filed.closed && (view?.asks ?? []).contains { $0.run != nil }
        c.someday = filed.section == TodoListSection.someday
        c.markDone = view?.line?.key == TodoLineKey.finishedSeen && !filed.closed
        switch pending?.write {
        case .done(true)?:
            (c.closed, c.struck, c.markDone, c.mark) = (true, true, false, nil)
        case .done(false)?:
            c.closed = false
        case .create?:
            let said = pending?.unsure == true ? words("Not sure it was saved [To-dos List]")
                : pending?.waits == true ? words("Not on your hub yet [To-dos List]") : words("Saving… [To-dos List]")
            (c.saving, c.unsure, c.said, c.mark, c.caption) = (true, pending?.unsure == true, TodoLineText.Said(rest: said), nil, nil)
            c.unsaved = true
        default:
            break
        }
        // A change that could not reach the hub yet says so where the hub's line was, until it does.
        if let pending, pending.waits, pending.write.edits {
            (c.said, c.mark, c.markDone, c.unsaved) = (TodoLineText.Said(rest: words("Not on your hub yet [To-dos List]")), nil, false, true)
        }
        if c.closed { c.highPriority = false } // closed, or ticked a moment ago: nothing to put first
        c.spoken = spoken(c, heard: heard, words: words)
        c.tickValue = c.closed ? StateName.done.text : words("Not done [To-dos Actions]")
        return c
    }

    /// The row in words: the title; High priority, where it is; the state its mark draws (its line may already say it);
    /// the line, with how many asks; line 3; the time.
    private static func spoken(_ c: TodoRowContent, heard: [String], words: TodoWords) -> String {
        let separator = words.listSeparator
        var parts = [c.title]
        if c.highPriority { parts.append(words("High priority [To-dos Spoken]")) }
        let state: String? = switch c.mark {
        case .needs: StateName.todoNeedsYou
        case .working: StateName.taskWorking
        case .ready: StateName.justFinished.text
        case .waiting: StateName.taskWaiting.text
        case nil: c.struck || (c.closed && heard.isEmpty) ? StateName.done.text : nil
        }
        if let state, c.said?.lead != state { parts.append(state) }
        if let said = c.said { parts.append(said.spoken(separator: separator)) }
        if let caption = c.caption { parts.append(caption) }
        return (parts + heard).joined(separator: separator)
    }

    /// Where it is, when no line can be said (no view, or a key this build does not know).
    private static func legacy(_ filed: TodoFiled) -> TodoLineText.Said? {
        switch filed.section {
        case TodoListSection.needsYou: TodoLineText.Said(lead: StateName.todoNeedsYou, tone: .needs)
        case TodoListSection.working: TodoLineText.Said(lead: StateName.taskWorking)
        case TodoListSection.waiting: TodoLineText.Said(lead: StateName.taskWaiting.text)
        case TodoListSection.justFinished: TodoLineText.Said(lead: StateName.justFinished.text, tone: .ready)
        default: nil
        }
    }

    static func mark(_ filed: TodoFiled, view: TodoSummary?) -> TodoMark? {
        switch filed.section {
        case TodoListSection.needsYou: view?.reason == TodoReason.failedStart ? nil : .needs
        case TodoListSection.working: .working
        case TodoListSection.waiting: .waiting
        case TodoListSection.justFinished: filed.closed ? nil : .ready
        default: nil
        }
    }

    private static func firstLine(_ text: String) -> String? {
        let line = text.split(whereSeparator: \.isNewline).first.map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
        return line.isEmpty ? nil : line
    }
}
