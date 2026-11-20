import Foundation
import PersonaWire

/// The mark before a row's second line (proposal 3.4): ● needs you, ◐ working, ✓ just finished, ◷ waiting; To do and the
/// rest have none.
nonisolated enum TodoMark: Equatable, Sendable { case needs, working, ready, waiting }

/// A row's door to where its ask is answered, until the rows answer in place: one door, whatever it asks.
nonisolated enum TodoDoor: Equatable, Sendable {
    case review
    /// A run's plan, which always opens its home.
    case reviewPlan
}

/// What a row draws, worked out in the list's model when the row's facts, view or the clock change, never in a view's
/// body (docs/apple/performance.md P4).
nonisolated struct TodoRowContent: Equatable, Sendable {
    var id: String
    var title: String
    /// The tick drawn ticked: the to-do is closed.
    var closed = false
    /// At the title's end: the due day, or how long ago it came to where it is, or when it closed.
    var trailing: String?
    var overdue = false
    /// Finished work nobody has seen: the unread dot before `trailing`.
    var unseen = false
    var mark: TodoMark?
    var said: TodoLineText.Said?
    var door: TodoDoor?
    /// How many asks hold it in Needs you: two or more say so on the door.
    var asks = 0
    /// Line 3, in caption: what the grouping does not already show, or nil.
    var caption: String?
    var inactive = false
    /// The row in words, for VoiceOver: the title, the state its mark draws, its line, the door and how many asks, line
    /// 3, then its time said in full ("Due Saturday", "16 minutes ago").
    var spoken = ""
}

/// How the list groups its rows (the bar's trailing menu): by phase, the sections; by project; by due date. In every
/// grouping Needs you stays a section at the top (proposal 3.3).
nonisolated enum TodosListGrouping: String, CaseIterable, Identifiable, Sendable {
    case phase, project, due
    var id: String { rawValue }

    var title: String {
        switch self {
        case .phase: String(localized: "Phase [To-dos Grouping]")
        case .project: String(localized: "Project [To-dos Grouping]")
        case .due: String(localized: "Due Date [To-dos Grouping]")
        }
    }
}

/// The order inside each group (the list's ⋯ menu): the hub's, by its rank, unless the owner picks another. A sort never
/// moves a section.
nonisolated enum TodosListSort: String, CaseIterable, Identifiable, Sendable {
    case suggested, activity, due, priority
    var id: String { rawValue }

    var title: String {
        switch self {
        case .suggested: String(localized: "Suggested [To-dos Sort]")
        case .activity: String(localized: "Recent Activity [To-dos Sort]")
        case .due: String(localized: "Due Date [To-dos Sort]")
        case .priority: String(localized: "Priority [To-dos Sort]")
        }
    }
}

/// One block of the list: a section with its header (or, in the calm list, none) and its rows, and the folds at its
/// foot ("Can wait · 1 ›", "Someday · 1 ›").
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
}

/// The list as drawn: its groups for the bar's choice, the grouping and the sort, worked out once per change of the
/// filing (`TodoCountsSource.filed`) or of a choice, in the model (P4). Pure.
nonisolated enum TodosListLayout {
    struct Input {
        /// The current assistant's rows as filed, in the list's order.
        var rows: [TodoFiled]
        var items: [String: TodoItem]
        var choice: TodosChoice = .all
        var grouping: TodosListGrouping = .phase
        var sort: TodosListSort = .suggested
        /// The folds the owner opened.
        var unfolded: Set<String> = []
        var now = Date()
        var calendar = Calendar.current
        var projectName: (String) -> String = { $0 }
    }

    struct Output: Equatable {
        var groups: [TodosListGroup] = []
        /// The calm rule holds: no bar, no headers.
        var calm = false
        /// Rows in Done (the bar shows Done only with them).
        var doneRows = 0
        /// Every row's section, by key: what the hold compares to tell a new need apart.
        var sections: [String: String] = [:]
        static let empty = Output()
    }

    /// A row's identity in the list: its manager and its id, unique in a merged list (and the list's tie-breakers after
    /// the rank, `TodoFiling.ordered`).
    static func key(_ row: TodoFiled) -> String { (row.manager ?? "") + "/" + row.id }

    static func layout(_ input: Input) -> Output {
        let carried = TodoCountsFallback.carried(input.rows)
        let rows = input.rows.filter { !carried.contains($0.id) }
        var out = Output()
        for row in rows { out.sections[key(row)] = row.section }
        out.doneRows = rows.count(where: { $0.section == TodoListSection.done })
        let present = Set(rows.map(\.section))
        out.calm = TodosBar.calm(Array(present))
        let choice: TodosChoice = out.calm ? .all : input.choice
        let shown = Set(choice.sections)
        var bySection: [String: [TodoFiled]] = [:]
        for row in rows where shown.contains(row.section) { bySection[row.section, default: []].append(row) }
        func sorted(_ list: [TodoFiled]) -> [String] { order(list, input).map(key) }
        func fold(_ section: String, title: String, _ list: [TodoFiled]) -> TodosListGroup.Fold? {
            list.isEmpty ? nil : .init(id: section, title: title, keys: sorted(list), open: input.unfolded.contains(section))
        }
        let canWait = fold(TodoListSection.canWait, title: String(localized: "Can wait [To-dos List]"),
                           bySection[TodoListSection.canWait] ?? [])
        let someday = fold(TodoListSection.someday, title: StateName.todoSomeday, bySection[TodoListSection.someday] ?? [])
        let doneToday = (bySection[TodoListSection.done] ?? []).filter { row in
            input.items[row.id]?.doneAt.map { input.calendar.isDate($0, inSameDayAs: input.now) } ?? false
        }

        if out.calm {
            var plain = TodosListGroup(id: "plain", keys: sorted(bySection[TodoListSection.todo] ?? []))
            plain.folds = [someday, fold(TodoListSection.done, title: String(localized: "Done today [To-dos List]"), doneToday)].compactMap { $0 }
            out.groups = [plain]
            return out
        }
        if choice == .done {
            out.groups = byDay(bySection[TodoListSection.done] ?? [], input)
            return out
        }

        var groups: [TodosListGroup] = []
        // Needs you heads every grouping, Can wait folded at its foot.
        let needs = bySection[TodoListSection.needsYou] ?? []
        if !needs.isEmpty || canWait != nil {
            // Its header says N, as the bar and the drawer do (the page reads it from the store, never held).
            groups.append(TodosListGroup(id: TodoListSection.needsYou, title: StateName.todoNeedsYou, count: needs.count,
                                         needs: true, keys: sorted(needs), folds: [canWait].compactMap { $0 }))
        }
        let rest = [TodoListSection.working, TodoListSection.waiting, TodoListSection.todo, TodoListSection.justFinished]
            .filter(shown.contains)
        switch input.grouping {
        case .phase:
            for section in rest {
                let list = bySection[section] ?? []
                var group = TodosListGroup(id: section, title: title(section), count: list.count, keys: sorted(list))
                // To do alone keeps Someday at its foot.
                if section == TodoListSection.todo, choice == .todo, let someday { group.folds.append(someday) }
                if !group.keys.isEmpty || !group.folds.isEmpty { groups.append(group) }
            }
        case .project:
            groups += byProject(rest.flatMap { bySection[$0] ?? [] }, input)
        case .due:
            groups += byDue(rest.flatMap { bySection[$0] ?? [] }, input)
        }
        // Someday folded at the foot of To do alone, else at the list's end; Done today at the end of All.
        var tail: [TodosListGroup.Fold] = []
        if choice != .todo || input.grouping != .phase || !groups.contains(where: { $0.id == TodoListSection.todo }), let someday {
            tail.append(someday)
        }
        if choice == .all, let done = fold(TodoListSection.done, title: String(localized: "Done today [To-dos List]"), doneToday) {
            tail.append(done)
        }
        if !tail.isEmpty {
            if groups.isEmpty { groups.append(TodosListGroup(id: "tail", keys: [])) }
            groups[groups.count - 1].folds += tail
        }
        out.groups = groups
        return out
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

    /// Groups by when the rows are due: overdue, today, tomorrow, this week, later, then no date.
    private static func byDue(_ list: [TodoFiled], _ input: Input) -> [TodosListGroup] {
        let cal = input.calendar
        let today = cal.startOfDay(for: input.now)
        let buckets: [(id: String, title: String)] = [
            ("overdue", String(localized: "Overdue [To-dos Due]")), ("today", String(localized: "Today [To-dos Due]")),
            ("tomorrow", String(localized: "Tomorrow [To-dos Due]")), ("week", String(localized: "This week [To-dos Due]")),
            ("later", String(localized: "After this week [To-dos Due]")), ("none", String(localized: "No date [To-dos Due]")),
        ]
        var rows: [String: [TodoFiled]] = [:]
        for row in list {
            let bucket: String
            if let item = input.items[row.id], let by = TodosLogic.dueBy(item, calendar: cal) {
                let days = cal.dateComponents([.day], from: today, to: cal.startOfDay(for: by)).day ?? 0
                bucket = by < input.now ? "overdue" : days <= 0 ? "today" : days == 1 ? "tomorrow" : days <= 6 ? "week" : "later"
            } else {
                bucket = "none"
            }
            rows[bucket, default: []].append(row)
        }
        return buckets.compactMap { b in
            guard let members = rows[b.id], !members.isEmpty else { return nil }
            let keys = order(members, input).map(key)
            return TodosListGroup(id: "due:" + b.id, title: b.title, count: keys.count, keys: keys)
        }
    }

    /// Done by when each closed: today, yesterday, this week, earlier.
    private static func byDay(_ list: [TodoFiled], _ input: Input) -> [TodosListGroup] {
        let cal = input.calendar
        let today = cal.startOfDay(for: input.now)
        let days: [(id: String, title: String)] = [
            ("today", String(localized: "Today [To-dos Done]")), ("yesterday", String(localized: "Yesterday [To-dos Done]")),
            ("week", String(localized: "This week [To-dos Done]")), ("earlier", String(localized: "Earlier [To-dos Done]")),
        ]
        var rows: [String: [(row: TodoFiled, at: Date)]] = [:]
        for row in list {
            let at = input.items[row.id]?.doneAt ?? .distantPast
            let ago = cal.dateComponents([.day], from: cal.startOfDay(for: at), to: today).day ?? 99
            let id = ago <= 0 ? "today" : ago == 1 ? "yesterday" : ago <= 6 ? "week" : "earlier"
            rows[id, default: []].append((row, at))
        }
        return days.compactMap { d in
            guard let members = rows[d.id], !members.isEmpty else { return nil }
            // Newest first, unless the owner picked another order.
            let ordered = input.sort == .suggested
                ? members.enumerated().sorted { $0.element.at != $1.element.at ? $0.element.at > $1.element.at : $0.offset < $1.offset }
                    .map(\.element.row)
                : order(members.map(\.row), input)
            return TodosListGroup(id: "done:" + d.id, title: d.title, count: ordered.count, keys: ordered.map(key))
        }
    }
}

/// A row's content from its facts and its view (#10465): the trailing time, the mark, the line in words, the door,
/// line 3 and the row in words. Pure.
nonisolated enum TodoRowContentLogic {
    static func content(_ filed: TodoFiled, item: TodoItem, view: TodoSummary?, grouping: TodosListGrouping, now: Date,
                        words: TodoWords = TodoWords(), calendar: Calendar = .current,
                        projectName: (String) -> String = { $0 }) -> TodoRowContent {
        var c = TodoRowContent(id: filed.id, title: item.title, closed: filed.closed, inactive: filed.inactive)
        let since = view?.since.flatMap(JSON.parseDate)
        // The time as drawn at the title's end, and as VoiceOver says it.
        var heard: [String] = []
        if let due = TodosLogic.dueBy(item, calendar: calendar), !filed.closed {
            let day = item.dueAt ?? TodosLogic.dueDay(item, calendar: calendar) ?? due
            c.trailing = TodoTimeWords.due(day, now: now, words: words, calendar: calendar)
            c.overdue = due < now
            if c.overdue { heard.append(words("Overdue [To-dos Spoken]")) }
            heard.append(TodoTimeWords.spokenDue(day, now: now, words: words, calendar: calendar))
        } else if filed.closed, let done = item.doneAt {
            c.trailing = TodoTimeWords.day(done, now: now, words: words, calendar: calendar)
            heard.append(words("Done \(TodoTimeWords.spokenDay(done, now: now, words: words, calendar: calendar)) [To-dos Spoken]"))
        } else if ![TodoListSection.todo, TodoListSection.someday].contains(filed.section),
                  let at = since ?? item.updatedAt ?? item.createdAt {
            c.trailing = TodoTimeWords.age(at, now: now, words: words, calendar: calendar)
            heard.append(TodoTimeWords.spokenAge(at, now: now, words: words, calendar: calendar))
        }
        c.unseen = filed.section == TodoListSection.justFinished && view?.seen != true
        c.mark = mark(filed, view: view)
        if let view {
            c.said = TodoLineText.said(view.line, since: since, closedReason: item.doneReason.flatMap(firstLine), now: now,
                                       words: words, calendar: calendar)
            let asks = view.asks ?? []
            c.asks = asks.count
            if [TodoListSection.needsYou, TodoListSection.canWait].contains(filed.section), let first = asks.first {
                c.door = first.kind == OwnerAskKind.plan ? .reviewPlan : .review
            }
        }
        // A row filed by the app's own rules (an older manager's), and one whose line this build cannot say (a newer
        // manager's key), say their section's word: the mark is drawn on line 2, and Working must not lose its ◐.
        if c.said == nil { c.said = legacy(filed) }
        // A plain To do says the first line of its notes, which every client has (the hub sends no line for it).
        if c.said == nil, [TodoListSection.todo, TodoListSection.someday].contains(filed.section),
           let notes = item.notes.flatMap(firstLine) {
            c.said = TodoLineText.Said(rest: notes)
        }
        var facts: [String] = []
        if filed.inactive { facts.append(StateName.todoInactive) }
        if let steps = view?.steps, steps.total > 0 { facts.append(words("Steps \(steps.done)/\(steps.total) [To-dos List]")) }
        // The project only beside another fact: alone it is not worth a line, and By Project already says it.
        if !facts.isEmpty, grouping != .project, let project = item.project, !project.isEmpty {
            facts.insert(projectName(project), at: 0)
        }
        c.caption = facts.isEmpty ? nil : facts.joined(separator: TodoTimeWords.separator)
        c.spoken = spoken(c, heard: heard, words: words)
        return c
    }

    /// The row in words: the title; the state its mark draws (its line may already say it); the line; the door and
    /// how many asks; line 3; the time.
    private static func spoken(_ c: TodoRowContent, heard: [String], words: TodoWords) -> String {
        let separator = words.listSeparator
        var parts = [c.title]
        let state: String? = switch c.mark {
        case .needs: StateName.todoNeedsYou
        case .working: StateName.taskWorking
        case .ready: StateName.justFinished.text
        case .waiting: StateName.taskWaiting.text
        case nil: c.closed && heard.isEmpty ? StateName.done.text : nil
        }
        if let state, c.said?.lead != state { parts.append(state) }
        if let said = c.said { parts.append(said.spoken(separator: separator)) }
        if let door = c.door {
            parts.append(door == .reviewPlan ? words("Review Plan [To-dos Door]") : words("Review [To-dos Door]"))
            if c.asks > 1 { parts.append(words("\(c.asks) asks [To-dos Door]")) }
        }
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
