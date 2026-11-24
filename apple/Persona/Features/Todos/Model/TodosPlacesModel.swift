import Foundation
import Observation
import PersonaWire

/// A To-dos place (#10465; docs/design/proposals/todos-redesign.md 3.3, "Places have identity"): All To-dos, Today,
/// Upcoming or a knowledge-base project, by the hub's place id (`TodoPlace.id`: all, today, upcoming, project:<slug>).
nonisolated struct TodoPlaceRef: Hashable, Identifiable, Sendable {
    var id: String

    static let all = TodoPlaceRef(id: TodoPlaceKind.allValue)
    static let today = TodoPlaceRef(id: TodoPlaceKind.today)
    static let upcoming = TodoPlaceRef(id: TodoPlaceKind.upcoming)
    static func project(_ slug: String) -> TodoPlaceRef { TodoPlaceRef(id: projectPrefix + slug) }
    private static let projectPrefix = TodoPlaceKind.project + ":"

    /// The project's slug, for a project's place.
    var project: String? { id.hasPrefix(Self.projectPrefix) ? String(id.dropFirst(Self.projectPrefix.count)) : nil }
    var isAll: Bool { id == TodoPlaceKind.allValue }
}

/// The places' rules (#10465; proposal 3.3; DESIGN_SYSTEM 3.5 "To-dos places"), pure: which rows a place holds, which
/// places the sidebar and the title menu list, and what a place says when it is empty.
nonisolated enum TodosPlaces {
    /// The projects the sidebar and the title menu list before All Projects….
    static let shownProjects = 5

    /// The places the sidebar's To-dos row folds open and the title menu lists, from the hub's (`countsSource.places`).
    struct Entries: Equatable, Sendable {
        /// Today and Upcoming, as the hub lists them.
        var fixed: [TodoPlace] = []
        /// Up to five projects, by name.
        var projects: [TodoPlace] = []
        /// All Projects… is offered: there are projects to go to, beyond the ones listed or with nothing open.
        var more = false
        /// No places from this hub (it does not list `todo_places`, or answered 404 or 405): All To-dos only.
        var isEmpty: Bool { fixed.isEmpty && projects.isEmpty && !more }
    }

    /// The hub's places as the sidebar lists them: Today and Upcoming, then up to five projects by name. Those with a
    /// part of N are chosen first when there are more than five, so a project that needs the owner is never behind All
    /// Projects…; the ones listed go by name, so rows only come and go and trade places only on a rename (the hub sends
    /// them by slug). `knowsProjects`: the knowledge base has projects, which All Projects… lists too.
    static func entries(_ places: [TodoPlace], knowsProjects: Bool = false) -> Entries {
        let fixed = places.filter { $0.kind == TodoPlaceKind.today || $0.kind == TodoPlaceKind.upcoming }
        // A hub that lists no places sends All alone (`TodoCountsSource`): nothing to fold open.
        guard !fixed.isEmpty else { return Entries() }
        let projects = byName(places.filter { $0.kind == TodoPlaceKind.project && !($0.project ?? "").isEmpty })
        var chosen = Set(projects.filter { $0.needsYou > 0 }.prefix(shownProjects).map(\.id))
        for p in projects where chosen.count < shownProjects { chosen.insert(p.id) }
        let shown = projects.filter { chosen.contains($0.id) }
        return Entries(fixed: fixed, projects: shown, more: !projects.isEmpty || knowsProjects)
    }

    /// Project places by the name they are listed under (the hub's title, else the slug), as the Finder sorts names.
    static func byName(_ projects: [TodoPlace]) -> [TodoPlace] {
        let name = { (p: TodoPlace) in p.title.isEmpty ? p.project ?? p.id : p.title }
        return projects.enumerated().sorted { a, b in
            let order = name(a.element).localizedStandardCompare(name(b.element))
            return order == .orderedSame ? a.offset < b.offset : order == .orderedAscending
        }.map(\.element)
    }

    /// The place drawn: `place` while the hub lists it, or, for a project, while the hub or the knowledge base knows it;
    /// else All To-dos. A hub that lists no places (an older one, or one that answered the route 404 or 405) and another
    /// assistant made current (its projects are others) never leave the list narrowed with no way back. A hello that
    /// stops listing `todo_places` mid-run takes none away (`TodoStore` reads them on).
    static func effective(_ place: TodoPlaceRef, places: [TodoPlace], projects: [TodoProject] = []) -> TodoPlaceRef {
        guard !place.isAll else { return place }
        // `entries` is empty exactly when the hub lists neither Today nor Upcoming; asked without sorting the projects.
        guard places.contains(where: { $0.kind == TodoPlaceKind.today || $0.kind == TodoPlaceKind.upcoming }) else { return .all }
        if let slug = place.project {
            return places.contains(where: { $0.id == place.id }) || projects.contains(where: { $0.slug == slug }) ? place : .all
        }
        return places.contains(where: { $0.id == place.id }) ? place : .all
    }

    /// The place's name: Today and Upcoming in the app's words, a project by the hub's title, else the knowledge base's
    /// name, else its slug.
    static func title(_ place: TodoPlaceRef, places: [TodoPlace], projects: [TodoProject]) -> String {
        switch place.id {
        case TodoPlaceKind.allValue: return String(localized: "All To-dos [To-dos List]")
        case TodoPlaceKind.today: return String(localized: "Today [To-dos Places]")
        case TodoPlaceKind.upcoming: return String(localized: "Upcoming [To-dos Places]")
        default:
            guard let slug = place.project else { return place.id }
            if let title = places.first(where: { $0.id == place.id })?.title, !title.isEmpty { return title }
            return TodoProjectLogic.name(slug, in: projects)
        }
    }

    // MARK: What a place draws

    /// Whether the place draws the Filter (DECISIONS 11-21 "The To-dos list"): All To-dos and a project's place. Today
    /// and Upcoming list open to-dos already narrowed by date, so Done would always be empty there and the other choices
    /// would narrow little; they land on All and stay there.
    static func filters(_ place: TodoPlaceRef) -> Bool { place.isAll || place.project != nil }

    /// Whether ⋯'s Group by Project files the place's rows under their projects' headings: All To-dos and Today. Not in
    /// Upcoming, whose days are its grouping (its ⋯ holds Show as Calendar alone), nor in a project's place, which is one
    /// project (its ⋯ greys the item with why).
    static func groupsByProject(_ place: TodoPlaceRef) -> Bool { place.isAll || place == .today }

    /// Each project's open to-dos (`TodoCounts.open`: not closed, not in Someday, a step its parent carries counted with
    /// its parent, wherever the parent is), counted on the device from the rows its place holds: All Projects… draws them
    /// neutral beside each project's part of N, which is counted by the same rule. The hub sends a place's N alone.
    static func openCounts(_ rows: [TodoFiled], projects: [String]) -> [String: Int] {
        let carried = TodoCountsFallback.carried(rows)
        var byProject: [String: [TodoFiled]] = [:]
        for row in rows where !carried.contains(row.id) {
            if let slug = row.project, !slug.isEmpty { byProject[slug, default: []].append(row) }
        }
        var out: [String: Int] = [:]
        for slug in projects { out[slug] = TodoCountsFallback.counts(byProject[slug] ?? []).open }
        return out
    }

    // MARK: What a place holds

    /// Whether `row` belongs in `place` (proposal 3.3; plan S2-4: the places filter the one list on the device).
    /// - Today: open, and due today, overdue, or reminding today.
    /// - Upcoming: open with a due date, Someday left out.
    /// - A project: its rows, closed ones too (the Filter's Done).
    static func holds(_ place: TodoPlaceRef, _ row: TodoFiled, item: TodoItem?, now: Date, calendar: Calendar) -> Bool {
        switch place.id {
        case TodoPlaceKind.allValue: return true
        case TodoPlaceKind.today:
            guard !row.closed, row.section != TodoListSection.someday, let item else { return false }
            if let by = TodosLogic.dueBy(item, calendar: calendar), by < now || calendar.isDate(dueDay(item, calendar) ?? by, inSameDayAs: now) {
                return true
            }
            return item.remindAt.map { calendar.isDate($0, inSameDayAs: now) } ?? false
        case TodoPlaceKind.upcoming:
            guard !row.closed, row.section != TodoListSection.someday, let item else { return false }
            return TodosLogic.dueBy(item, calendar: calendar) != nil
        default:
            guard let slug = place.project else { return false }
            return row.project == slug
        }
    }

    /// The day a to-do is due on: its due moment's day, or its due date.
    static func dueDay(_ item: TodoItem, _ calendar: Calendar) -> Date? {
        if let at = item.dueAt { return calendar.startOfDay(for: at) }
        return TodosLogic.dueDay(item, calendar: calendar)
    }

    /// The rows of `rows` that `place` holds, in the order given. A step whose parent the place holds too is left out,
    /// as the list leaves it out (its parent carries it); one whose parent the place leaves out (a dated step of an
    /// undated to-do, on Today or Upcoming) is a row of its own there. A project's place rolls a step up to its open
    /// parent wherever the parent is, as its part of N, the hub's and the sidebar's, counts it (another project's parent
    /// carries it there), so the place and every number about it agree (DESIGN_SYSTEM 4.8).
    static func rows(_ rows: [TodoFiled], in place: TodoPlaceRef, items: [String: TodoItem], now: Date, calendar: Calendar)
        -> [TodoFiled] {
        guard !place.isAll else { return rows }
        let held = rows.filter { holds(place, $0, item: items[$0.id], now: now, calendar: calendar) }
        let carried = TodoCountsFallback.carried(place.project != nil ? rows : held)
        return carried.isEmpty ? held : held.filter { !carried.contains($0.id) }
    }

    /// "3 coming up this week" (Today's empty state): Upcoming's rows due in the seven days after today.
    static func comingUp(_ rows: [TodoFiled], items: [String: TodoItem], now: Date, calendar: Calendar) -> Int {
        let today = calendar.startOfDay(for: now)
        guard let first = calendar.date(byAdding: .day, value: 1, to: today),
              let end = calendar.date(byAdding: .day, value: 8, to: today) else { return 0 }
        return self.rows(rows, in: .upcoming, items: items, now: now, calendar: calendar).count { row in
            guard let item = items[row.id], let day = dueDay(item, calendar) else { return false }
            return day >= first && day < end
        }
    }
}

/// What the sidebar's To-dos row folds open and the list's title menu lists (#10465; proposal 3.3): the places from the
/// store (`TodoCountsSource.places`: the hub's with one manager, the current assistant's projects with several, each
/// with its part of N), the place shown (`TodoWorkspace.place`) and its name. A pick in the sidebar opens To-dos on that
/// place at its root; a pick in the title menu changes the list in place.
@MainActor @Observable
final class TodosPlacesModel {
    struct Deps {
        var store: TodoStore
        var workspace: TodoWorkspace
        /// The knowledge base's projects (`TodoProjects`): All Projects… lists those with nothing open too.
        var projects: @MainActor () -> [TodoProject]
    }

    @ObservationIgnored private let deps: Deps
    /// The last entries worked out and what from: the sidebar's rows, the title menu and the page's follower each read
    /// them in their bodies, and the projects are sorted by name only when the places or the projects change (P4).
    @ObservationIgnored private var entriesMemo: (places: [TodoPlace], knowsProjects: Bool, entries: TodosPlaces.Entries)?

    init(deps: Deps) {
        self.deps = deps
    }

    /// The hub's places as the store counts them.
    var places: [TodoPlace] { deps.store.countsSource.places }
    var entries: TodosPlaces.Entries {
        let places = places
        let knows = !deps.projects().isEmpty
        if let m = entriesMemo, m.knowsProjects == knows, m.places == places { return m.entries }
        let entries = TodosPlaces.entries(places, knowsProjects: knows)
        entriesMemo = (places, knows, entries)
        return entries
    }
    /// The place shown: the owner's pick, or All To-dos once the hub lists no places.
    var current: TodoPlaceRef {
        TodosPlaces.effective(TodoPlaceRef(id: deps.workspace.place), places: places, projects: deps.projects())
    }

    func title(_ place: TodoPlaceRef) -> String { TodosPlaces.title(place, places: places, projects: deps.projects()) }

    /// Every project to go to (All Projects…): the hub's project places, then the knowledge base's projects with
    /// nothing open, each by name.
    var allProjects: [TodoPlace] {
        let listed = TodosPlaces.byName(places.filter { $0.kind == TodoPlaceKind.project && !($0.project ?? "").isEmpty })
        let slugs = Set(listed.compactMap(\.project))
        let quiet = TodoProjectLogic.choices(deps.projects(), current: nil).filter { !slugs.contains($0.slug) }
            .map { TodoPlace(id: TodoPlaceRef.project($0.slug).id, kind: TodoPlaceKind.project, title: $0.name, project: $0.slug) }
        return listed + quiet
    }

    /// Each of `projects`' open to-dos, by slug (All Projects…, `TodosPlaces.openCounts`): from the current assistant's
    /// rows as filed, the ones the list draws. Worked out by the sheet when it opens and when the rows change.
    func openCounts(_ projects: [TodoPlace]) -> [String: Int] {
        TodosPlaces.openCounts(deps.store.countsSource.filed, projects: projects.compactMap(\.project))
    }

    /// The title menu's pick: the list shows `place` at once, where it is.
    func show(_ place: TodoPlaceRef) {
        deps.workspace.pick(place.id)
    }

    /// The sidebar's pick: To-dos opens on `place`, at its root (`TodoWorkspace.open`).
    func open(_ place: TodoPlaceRef) {
        deps.workspace.open(place.id)
    }

    /// The sidebar's To-dos row, which then opens To-dos: All To-dos where To-dos is in front, its root by the tab-bar
    /// rule; otherwise To-dos as it was left, on its last place and with what it kept (proposal 3.3: places keep their
    /// stacks, option 2 on phones and option 3 at regular widths). The host that opens it knows which
    /// (`TodoWorkspace.opening`).
    func pickRow() {
        deps.workspace.pickRow()
    }
}

extension TodosPlacesModel.Deps {
    /// The app's: the shared store and workspace, and the knowledge base's projects as To-dos last read them.
    @MainActor static var live: Self {
        Self(store: TodoStore.shared, workspace: TodoWorkspace.shared, projects: { TodoProjects.shared.list })
    }
}
