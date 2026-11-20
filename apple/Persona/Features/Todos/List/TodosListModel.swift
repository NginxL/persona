import Foundation
import Observation
import PersonaWire

/// What the To-dos list needs from the app (docs/apple/ios.md "Logic-layer choice"): the store whose rows, views and
/// numbers it draws, the hub, the clock and the projects' names. `live` is the app's (`TodoListView.swift`); tests pass
/// a store of their own and a `FakeHub`'s client.
struct TodosListDeps {
    var store: TodoStore
    var api: @MainActor () -> APIClient?
    var now: @MainActor () -> Date
    var calendar: @MainActor () -> Calendar
    var projects: @MainActor () -> [TodoProject]
}

/// The V1 To-dos list (#10465; docs/design/proposals/todos-redesign.md 3.3, 3.4; DESIGN_SYSTEM 3.4 "To-dos list"): the
/// current assistant's rows as the hub files them (`TodoStore.countsSource.filed`, the rows its numbers are counted
/// from), drawn in sections for the bar's choice, the grouping and the sort.
///
/// - Rows never move under a finger: while the list is scrolled or out of sight (`hold`, the page reports what it does)
///   a new filing waits, and so does what each row says, since a new line can wrap or a door come and go, and a row's
///   new height moves every row under it (the Agents list learned it, `ChatsView`'s `landCritical`). Once the list rests
///   it all lands at once, the rows that changed place fading in. A row that comes to need the owner lands at once,
///   alone; the numbers are the store's, never held.
/// - The structure holds keys; each row reads its own cell (`Row`), so a run's new line draws its row alone (P5).
@MainActor @Observable
final class TodosListModel {
    typealias Deps = TodosListDeps

    /// One row as the list draws it.
    @MainActor @Observable
    final class Row: Identifiable {
        /// Its key in the list: the manager and the id (`TodosListLayout.key`).
        let id: String
        let todoID: String
        fileprivate(set) var content: TodoRowContent
        /// How many re-filings put it in another group or fold.
        @ObservationIgnored fileprivate(set) var arrivals = 0
        /// Hidden until the view drawing it where a re-filing put it fades it in (proposal 3.9; `TodoListRow`). That
        /// view is a new one (another section's `ForEach`), which reads this as it is first drawn, so no frame shows the
        /// row before its fade; it clears it as it appears (`entered`). A row that landed out of sight is shown as it
        /// is at the next landing or scroll (`settleArrivals`), never faded in later under a finger.
        fileprivate(set) var entering = false
        /// What it will say once the list rests (nil: what it says is the latest).
        @ObservationIgnored fileprivate var waiting: TodoRowContent?
        /// What the content was worked out from: worked out again only when one of these or the clock changes.
        @ObservationIgnored fileprivate var inputs: Inputs

        fileprivate struct Inputs: Equatable {
            var item: TodoItem
            var view: TodoSummary?
            var filed: TodoFiled
            var grouping: TodosListGrouping
            var projects: [TodoProject]
        }

        fileprivate init(id: String, inputs: Inputs, content: TodoRowContent) {
            self.id = id
            todoID = inputs.filed.id
            self.inputs = inputs
            self.content = content
        }

        /// Its view, appearing where a re-filing put it: shown, faded in by the caller's animation.
        func entered() { entering = false }
    }

    /// The bar's choice: All each time the list is made, never stored, so a remembered choice can't hide what needs the
    /// owner (proposal 3.3).
    var choice: TodosChoice = .all {
        didSet { if choice != oldValue { relayout() } }
    }
    var grouping: TodosListGrouping = .phase {
        didSet { if grouping != oldValue { relayout(contents: true) } }
    }
    var sort: TodosListSort = .suggested {
        didSet { if sort != oldValue { relayout() } }
    }
    /// The folds the owner opened (Can wait, Someday, Done today): this list's, not kept.
    private(set) var unfolded: Set<String> = []
    private(set) var layout = TodosListLayout.Output.empty
    /// Whether the list can draw: the views are known (kept from the last run, or the first read ended). Until then it
    /// draws nothing rather than filing its rows twice (`TodoStore.viewsRead`).
    private(set) var ready = false

    /// Scrolled or out of sight: the page says which, the hold says when a new filing lands. Made at first use: the
    /// page makes this model in a `@State` initial value, which must stay cheap (P3).
    @ObservationIgnored private(set) lazy var hold = Hold("To-dos list")
    @ObservationIgnored private let deps: Deps
    @ObservationIgnored private var cells: [String: Row] = [:]
    /// What the store has now.
    @ObservationIgnored private var snapshot = Snapshot.empty
    /// What the drawn layout was laid out from: the store as it was when it last landed, and the needs landed since.
    @ObservationIgnored private var drawn = Snapshot.empty
    @ObservationIgnored private var watching = false
    @ObservationIgnored private var following = false
    /// The time the rows' times are said from: set when the list starts and each minute after.
    @ObservationIgnored private var now = Date.distantPast
    /// A minute passed while the list was held: every row's time is said again once it rests, not mid-glide.
    @ObservationIgnored private var clockWaits = false

    /// What the list reads of the store and the app.
    private struct Snapshot: Equatable {
        var filed: [TodoFiled] = []
        var items: [String: TodoItem] = [:]
        var views: [String: TodoSummary] = [:]
        var ready = false
        var projects: [TodoProject] = []
        static let empty = Snapshot()
    }

    init(deps: Deps) {
        self.deps = deps
    }

    var groups: [TodosListGroup] { layout.groups }
    var calm: Bool { layout.calm }
    /// The bar's choices with the store's counts: never held, so they change in the same update as every other place
    /// that shows them.
    var entries: [TodosBar.Entry] {
        TodosBar.entries(deps.store.countsSource.counts, doneRows: layout.doneRows, chosen: choice)
    }
    /// N, as the drawer, the bar and the Needs you header say it.
    var needsYou: Int { deps.store.countsSource.needsYou }
    /// Nothing to list under the current choice.
    var isEmpty: Bool { layout.groups.allSatisfy { $0.keys.isEmpty && $0.folds.isEmpty } }
    /// The list has rows, under any choice: the bar is drawn (unless calm), so an empty choice can be left.
    var hasRows: Bool { !layout.sections.isEmpty }

    /// A drawn row's cell; kept until the layout that drops its row lands.
    func row(_ key: String) -> Row? { cells[key] }

    /// Follows the store from now on; the first layout lands at once.
    func start() {
        guard !following else { return }
        following = true
        now = deps.now()
        hold.land = { [weak self] in self?.landAll(animated: true) }
        snapshot = current()
        ready = snapshot.ready
        landAll(force: true, animated: false)
    }

    func toggle(_ fold: String) {
        if unfolded.contains(fold) { unfolded.remove(fold) } else { unfolded.insert(fold) }
        relayout()
    }

    func setFold(_ fold: String, open: Bool) {
        guard open != unfolded.contains(fold) else { return }
        toggle(fold)
    }

    /// The page's minute: the rows' times are said again, and rows that moved by the clock (Done today) are filed
    /// again. Nothing within the minute the times were last said in.
    func tick() {
        let at = deps.now()
        guard !deps.calendar().isDate(at, equalTo: now, toGranularity: .minute) else { return }
        now = at
        changed(clock: true)
    }

    /// Pull to refresh.
    func refresh() async {
        await deps.store.refresh(api: deps.api())
    }

    // MARK: Following the store

    private func current() -> Snapshot {
        guard following else { return snapshot }
        guard !watching else { return read() }
        watching = true
        return withObservationTracking { read() } onChange: { [weak self] in
            // Told before the change: read it after.
            Task { @MainActor [weak self] in
                self?.watching = false
                self?.changed()
            }
        }
    }

    private func read() -> Snapshot {
        let store = deps.store
        return Snapshot(filed: store.countsSource.filed, items: store.byID, views: store.views, ready: store.viewsRead,
                        projects: deps.projects())
    }

    /// The store changed (or, `clock`, a minute passed): it all lands now, or once the list rests, but a row that comes
    /// to need the owner.
    private func changed(clock: Bool = false) {
        let next = current()
        guard next != snapshot || clock else { return }
        if next != snapshot {
            snapshot = next
            if ready != next.ready { ready = next.ready }
        }
        guard hold.wait() else { return landAll(force: clock, animated: true) }
        if clock { clockWaits = true }
        updateCells(force: false, held: true)
        landNeeds()
    }

    /// Everything that waited lands: what the rows say, then where they are.
    private func landAll(force: Bool = false, animated: Bool) {
        updateCells(force: force || clockWaits, held: false)
        clockWaits = false
        hold.landed()
        drawn = snapshot
        // A row gone from the store keeps its cell until now: the layout drawn until now still listed it.
        let keys = Set(snapshot.filed.map(TodosListLayout.key))
        if cells.count > keys.count || cells.keys.contains(where: { !keys.contains($0) }) {
            cells = cells.filter { keys.contains($0.key) }
        }
        show(makeLayout(drawn), animated: animated)
    }

    /// While held, the rows that come to need the owner land at once, alone (DESIGN_SYSTEM 3.4; `Hold`: critical changes
    /// land "at once, alone, and the rest goes on waiting"): onto the list as drawn, saying what they say now.
    private func landNeeds() {
        let needs = snapshot.filed.filter { row in
            row.section == TodoListSection.needsYou && layout.sections[TodosListLayout.key(row)] != TodoListSection.needsYou
        }
        guard !needs.isEmpty else { return }
        let keys = Set(needs.map(TodosListLayout.key))
        var next = drawn
        next.filed = TodoFiling.ordered(drawn.filed.filter { !keys.contains(TodosListLayout.key($0)) } + needs)
        for row in needs {
            next.items[row.id] = snapshot.items[row.id]
            if let cell = cells[TodosListLayout.key(row)], let waiting = cell.waiting {
                cell.waiting = nil
                cell.content = waiting
            }
        }
        drawn = next
        show(makeLayout(drawn), animated: true)
    }

    /// A choice, the grouping, the sort or a fold changed: the owner's own tap, never held, never faded.
    private func relayout(contents: Bool = false) {
        landAll(force: contents, animated: false)
    }

    /// Draws `laid`, not animated: rows it puts in another group fade in where they now are (proposal 3.9, "Hub
    /// re-files a row"), so nothing slides across the list.
    private func show(_ laid: TodosListLayout.Output, animated: Bool) {
        guard laid != layout else { return }
        settleArrivals()
        if animated && !layout.groups.isEmpty {
            let fades = !Theme.Motion.reduced // with Reduce Motion it is there at once
            for key in Self.moved(from: layout, to: laid) {
                guard let cell = cells[key] else { continue }
                cell.arrivals += 1
                if fades { cell.entering = true }
            }
        }
        layout = laid
    }

    /// Rows still waiting to fade in are shown as they are: a new layout lands, or the page reports a scroll. A row that
    /// landed out of sight never fades in later, as a scroll brings it in.
    func settleArrivals() {
        for cell in cells.values where cell.entering { cell.entering = false }
    }

    /// The drawn rows of `to` that `from` drew elsewhere or not at all.
    nonisolated static func moved(from: TodosListLayout.Output, to: TodosListLayout.Output) -> [String] {
        let before = places(from)
        return places(to).compactMap { key, at in before[key] == at ? nil : key }
    }

    /// Where each drawn row is: its group, and the fold it is in (an open one; a closed fold's rows are not drawn).
    private nonisolated static func places(_ out: TodosListLayout.Output) -> [String: String] {
        var at: [String: String] = [:]
        for group in out.groups {
            for key in group.keys { at[key] = group.id }
            for fold in group.folds where fold.open {
                for key in fold.keys { at[key] = group.id + "/" + fold.id }
            }
        }
        return at
    }

    private func makeLayout(_ from: Snapshot) -> TodosListLayout.Output {
        let projects = from.projects
        return TodosListLayout.layout(.init(
            rows: from.filed, items: from.items, choice: choice, grouping: grouping, sort: sort, unfolded: unfolded,
            now: now, calendar: deps.calendar(),
            projectName: { TodoProjectLogic.name($0, in: projects) }))
    }

    /// Each row's content, worked out again only where its facts, its view, its filing or the clock (`force`) changed.
    /// `held`: a drawn row keeps what it says until the list rests (`waiting`); otherwise what waited lands.
    private func updateCells(force: Bool, held: Bool) {
        let calendar = deps.calendar()
        let projects = snapshot.projects
        for filed in snapshot.filed {
            guard let item = snapshot.items[filed.id] else { continue }
            let key = TodosListLayout.key(filed)
            let inputs = Row.Inputs(item: item, view: snapshot.views[filed.id], filed: filed, grouping: grouping, projects: projects)
            if let cell = cells[key], !force, cell.inputs == inputs {
                if !held, let waiting = cell.waiting {
                    cell.waiting = nil
                    if cell.content != waiting { cell.content = waiting }
                }
                continue
            }
            let content = TodoRowContentLogic.content(filed, item: item, view: inputs.view, grouping: grouping, now: now,
                                                      calendar: calendar,
                                                      projectName: { TodoProjectLogic.name($0, in: projects) })
            if let cell = cells[key] {
                cell.inputs = inputs
                if held {
                    cell.waiting = content == cell.content ? nil : content
                } else {
                    cell.waiting = nil
                    if cell.content != content { cell.content = content }
                }
            } else {
                cells[key] = Row(id: key, inputs: inputs, content: content)
            }
        }
    }
}
