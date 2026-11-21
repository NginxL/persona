import Foundation
import Observation
import PersonaWire

/// What the To-dos list needs from the app (docs/apple/ios.md "Logic-layer choice"): the store whose rows, views and
/// numbers it draws, the writes held on the device (`TodoOutbox`), the hub, the clock, the projects' names and where it
/// says what it did (a toast). `live` is the app's (`TodoListView.swift`); tests pass a store and an outbox of their own
/// and a `FakeHub`'s client.
struct TodosListDeps {
    var store: TodoStore
    var outbox: TodoOutbox
    var api: @MainActor () -> APIClient?
    var now: @MainActor () -> Date
    var calendar: @MainActor () -> Calendar
    var projects: @MainActor () -> [TodoProject]
    var toast: @MainActor (Toast) -> Void
    /// An error from a write the list made at once (the plan of a to-do: Today, Date…, Project).
    var show: @MainActor (Error) -> Void
    /// How long a tick, Someday or a delete waits for Undo before it goes (tests make it short).
    var undoWindow: Duration = Theme.Motion.todoUndo
    /// VoiceOver or Switch Control runs: a held write waits longer, and the rows offer its Undo.
    var assistive: @MainActor () -> Bool = { false }
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
            /// The owner's write on it still on the device (`TodoOutbox`): a tick waiting out its Undo, a new to-do.
            var pending: TodoRowWrite?
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

    /// What the list reads of the store, the outbox and the app.
    private struct Snapshot: Equatable {
        var filed: [TodoFiled] = []
        var items: [String: TodoItem] = [:]
        var views: [String: TodoSummary] = [:]
        /// The owner's writes still on the device, by to-do id.
        var writes: [String: TodoOutbox.Entry] = [:]
        /// The ids the hub just gave new to-dos made here: their rows take the place of the ones drawn for them.
        var named: Set<String> = []
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

    // MARK: The owner's writes (the tick, the swipe, the menu, New To-Do)

    /// What a tap on a row's tick does now.
    enum Tick: Equatable {
        /// An open to-do closes, after its Undo window.
        case close
        /// Persona or an Agent works on it, or an Agent waits for the owner on it: "Mark it done and stop?" first.
        case askFirst
        /// Ticked a moment ago: taken back (its write, `TodoOutbox` id).
        case takeBack(UUID)
        /// A closed to-do opens again.
        case reopen
    }

    /// The row whose tick asks first ("Mark it done and stop?"), the row Delete… asks about, and Date…'s row: each
    /// presented from its row (`TodoListCell`).
    var stopping: String?
    var deleting: String?
    var dating: String?
    /// The latest tick, Someday or delete made here that still waits out its window (VoiceOver's and Switch Control's
    /// Undo on the rows, the toast being out of their reach).
    private(set) var lastHeld: (id: UUID, todo: String, title: String, write: TodoOutbox.Write)?

    func tick(of key: String) -> Tick? {
        guard let row = cells[key] else { return nil }
        // The outbox as it is now, not as last drawn: a second tap before the row redraws takes the first back.
        if let entry = deps.outbox.writes[row.todoID], entry.write == .done(true) { return .takeBack(entry.id) }
        if row.content.closed { return .reopen }
        return row.content.working || row.content.agentWaits ? .askFirst : .close
    }

    /// The tick, the full swipe and Mark Done: closes, asks first, takes a tick of a moment ago back, or reopens.
    func tapTick(_ key: String) {
        switch tick(of: key) {
        case .close?: close(key)
        case .askFirst?: stopping = key
        case .takeBack(let id)?: undo(id, of: .done(true))
        case .reopen?: reopen(key)
        case nil: break
        }
    }

    /// What "Mark it done and stop?" says for row `key`: an Agent waiting for the owner's answer, else Persona working.
    func stopTitle(_ key: String) -> String {
        if let c = cells[key]?.content, c.agentWaits, !c.working {
            return String(localized: "An Agent is waiting for your answer on this. Mark it done and stop it? [To-dos Actions]")
        }
        return String(localized: "Persona is still working on this. Mark it done and stop? [To-dos Actions]")
    }

    /// The tick, the full swipe and Mark Done: the row is drawn ticked and struck through at once and closes on the hub
    /// once its Undo window ends ("Done · Undo"); it leaves for where the hub files it when the list rests. On a to-do
    /// someone works on, after "Mark it done and stop?" (closing it stops its Agents' follow-ups).
    func close(_ key: String) {
        guard let row = cells[key], !row.content.saving else { return }
        Haptics.tap()
        held(.done(true), row, said: String(localized: "Done [To-dos Toast]"))
    }

    /// The tick on a closed to-do: it opens again at once.
    func reopen(_ key: String) {
        guard let row = cells[key] else { return }
        Haptics.tap()
        deps.outbox.add(.done(false), todo: row.todoID, manager: row.inputs.item.manager)
    }

    /// Someday: the row moves there at once, and the hub is told once its Undo window ends.
    func someday(_ key: String) {
        guard let row = cells[key], !row.content.saving else { return }
        Haptics.tap()
        held(.someday(true), row, said: String(localized: "Moved to Someday [To-dos Toast]"))
    }

    /// Out of Someday again: the hub files it where it belongs. Within Someday's own window it is that write taken back,
    /// and nothing goes (`TodoOutbox.add`).
    func outOfSomeday(_ key: String) {
        guard let row = cells[key] else { return }
        Haptics.tap()
        deps.outbox.add(.someday(false), todo: row.todoID, manager: row.inputs.item.manager)
    }

    /// Delete…, once the owner confirmed it: the row goes at once; the hub deletes it only once the Undo window ends,
    /// since nothing undeletes a to-do there.
    func delete(_ key: String) {
        guard let row = cells[key], !row.content.saving else { return }
        Haptics.tap()
        held(.delete, row, said: String(localized: "Deleted [To-dos Toast]"))
    }

    /// New To-Do: listed at once, first in To do ("Saving…"), and sent now; Undo deletes it again. false: nothing to add,
    /// or no hub.
    @discardableResult
    func create(title: String, notes: String?) -> Bool {
        let title = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let notes = notes?.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return false }
        let made = deps.store.manager(of: .current)
        guard let entry = deps.outbox.add(.create(title: title, notes: notes?.isEmpty == false ? notes : nil),
                                          todo: TodoOutbox.newID(), manager: made) else { return false }
        Haptics.tap()
        toast(String(localized: "Added [To-dos Toast]"), undo: entry.id, of: entry.write)
        return true
    }

    /// Try Again on a new to-do nobody knows the hub made: sent again, as the owner asks.
    func retry(_ key: String) {
        guard let row = cells[key], let entry = deps.outbox.writes[row.todoID], entry.unsure else { return }
        Haptics.tap()
        deps.outbox.retry(entry.id)
    }

    /// Remove on a new to-do nobody knows the hub made: off the list; should the hub turn out to have it, it is deleted.
    func removeUnsure(_ key: String) {
        guard let row = cells[key], let entry = deps.outbox.writes[row.todoID], entry.unsure else { return }
        Haptics.tap()
        deps.outbox.takeBack(entry.id)
    }

    /// Undo of `write` on a toast, VoiceOver's Undo, or the tick of a row ticked a moment ago. One that can't be taken
    /// back is a delete the hub has, or a write the outbox no longer remembers (many later ones, or the app ended since).
    func undo(_ id: UUID, of write: TodoOutbox.Write) {
        Haptics.tap()
        guard deps.outbox.takeBack(id) else {
            Haptics.error()
            let said = write == .delete ? String(localized: "It was deleted already, so it can't come back. [To-dos Toast]")
                                        : String(localized: "It can't be undone anymore. [To-dos Toast]")
            deps.toast(Toast(said, style: .error))
            return
        }
        if lastHeld?.id == id { lastHeld = nil }
    }

    /// The write VoiceOver's and Switch Control's Undo takes back while it waits out its window: the latest made here.
    var undoable: (id: UUID, title: String, write: TodoOutbox.Write)? {
        guard let held = lastHeld, deps.outbox.holds(held.id) else { return nil }
        return (held.id, held.title, held.write)
    }

    /// Today: due today, at the time of day it was due at if it had one.
    func setToday(_ key: String) {
        guard let item = item(key) else { return }
        let today = deps.calendar().startOfDay(for: deps.now())
        setDue(key, to: Self.due(item, on: today, calendar: deps.calendar()))
    }

    /// A due date from Date… (nil: none), shown at once and saved as the to-do's own page saves it.
    func setDue(_ key: String, to due: APIClient.TodoPlanChange.Due) {
        guard let row = cells[key], !row.content.saving else { return }
        Haptics.tap()
        deps.store.reschedule(row.todoID, to: due, api: deps.api())
    }

    /// Due on `day`: at the time of day `item` was due at when it had one (moving a moment keeps its hour), else all day.
    nonisolated static func due(_ item: TodoItem, on day: Date, calendar: Calendar) -> APIClient.TodoPlanChange.Due {
        guard let at = item.dueAt else { return .day(TodoPlanText.day(day, calendar: calendar)) }
        let time = calendar.dateComponents([.hour, .minute, .second], from: at)
        let moved = calendar.date(bySettingHour: time.hour ?? 0, minute: time.minute ?? 0, second: time.second ?? 0, of: day)
        return .at(moved ?? day)
    }

    /// Project: a knowledge-base project's slug, "" for none, shown at once.
    func setProject(_ key: String, _ slug: String) {
        guard let row = cells[key], !row.content.saving, slug != (row.inputs.item.project ?? "") else { return }
        Haptics.tap()
        deps.store.refile(row.todoID, project: slug, api: deps.api())
    }

    /// Delete… asks first, from its row.
    func askDelete(_ key: String) { deleting = key }
    /// Date… opens its sheet for row `key`.
    func pickDate(_ key: String) { dating = key }

    /// The to-do of row `key`, as the list has it (a new one's too).
    func item(_ key: String) -> TodoItem? { cells[key]?.inputs.item }

    /// What a new to-do the hub turned down said, back in New To-Do's sheet (its toast's Edit); `tookDraft` once it is.
    var draft: TodoOutbox.Draft? { deps.outbox.draft }
    func tookDraft() { deps.outbox.tookDraft() }

    /// The list's line while the hub is out of reach, with when the hub last answered a read of it.
    var offlineLine: String { TodoOfflineLine.text(readAt: deps.store.readAt, now: deps.now(), calendar: deps.calendar()) }

    /// `write` held through its Undo window, said in a toast with Undo. While VoiceOver or Switch Control runs the window
    /// is longer, and the rows offer the Undo (`undoable`): the toast's button is out of their reach (HIG-005).
    private func held(_ write: TodoOutbox.Write, _ row: Row, said: String) {
        let window = deps.assistive() ? Theme.Motion.todoUndoAssistive : deps.undoWindow
        guard let entry = deps.outbox.add(write, todo: row.todoID, manager: row.inputs.item.manager, hold: window)
        else { return }
        lastHeld = (entry.id, row.todoID, row.inputs.item.title, write)
        toast(said, undo: entry.id, of: write, window: window)
    }

    private func toast(_ said: String, undo id: UUID, of write: TodoOutbox.Write, window: Duration? = nil) {
        deps.toast(Toast(said, style: .success, action: Toast.Action(title: String(localized: "Undo")) { [weak self] in
            self?.undo(id, of: write)
        }, duration: window ?? deps.undoWindow))
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
        let writes = deps.outbox.writes
        var items = store.byID
        // A new to-do is drawn from what the owner wrote until the hub has it (`TodoCountsSource` files it).
        for entry in writes.values { if let item = entry.newItem, items[item.id] == nil { items[item.id] = item } }
        return Snapshot(filed: store.countsSource.filed, items: items, views: store.views, writes: writes,
                        named: deps.outbox.named, ready: store.viewsRead, projects: deps.projects())
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
                // A new to-do's own row in the place of the one drawn for it is the same row: it does not fade in again.
                guard let cell = cells[key], !snapshot.named.contains(cell.todoID) else { continue }
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
            let inputs = Row.Inputs(item: item, view: snapshot.views[filed.id], filed: filed, grouping: grouping, projects: projects,
                                    pending: snapshot.writes[filed.id].map {
                                        TodoRowWrite(write: $0.write, waits: $0.waits && !$0.answered, unsure: $0.unsure)
                                    })
            if let cell = cells[key], !force, cell.inputs == inputs {
                if !held, let waiting = cell.waiting {
                    cell.waiting = nil
                    if cell.content != waiting { cell.content = waiting }
                }
                continue
            }
            let content = TodoRowContentLogic.content(filed, item: item, view: inputs.view, grouping: grouping, now: now,
                                                      calendar: calendar,
                                                      projectName: { TodoProjectLogic.name($0, in: projects) },
                                                      pending: inputs.pending)
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
