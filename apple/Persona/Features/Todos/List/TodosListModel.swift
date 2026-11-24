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

/// Where a to-do made with New To-Do starts (#10465; proposal 3.3, "Capture default"; DESIGN_SYSTEM 3.5 "To-dos
/// places"): the place it was made in holds it from the tap on.
nonisolated struct TodoCapture: Equatable, Identifiable, Sendable {
    /// The day it is due on, all day unless the owner gives it a time in the sheet; nil asks for no date.
    var day: Date?
    /// The moment on `day` it is due at: the sheet starts with All Day off (an hour tapped on Upcoming's calendar, a
    /// refused to-do that had one).
    var at: Date?
    /// The knowledge-base project it is made in, by slug.
    var project: String?

    var id: String { "\(day?.timeIntervalSince1970 ?? -1)|\(at?.timeIntervalSince1970 ?? -1)|\(project ?? "")" }
}

/// The V1 To-dos list (#10465; docs/design/proposals/todos-redesign.md 3.3, 3.4; DESIGN_SYSTEM 3.4 "To-dos list"): the
/// current assistant's rows as the hub files them (`TodoStore.countsSource.filed`, the rows its numbers are counted
/// from), drawn in sections for the place shown (DESIGN_SYSTEM 3.5 "To-dos places"), the Filter's choice, the search,
/// the sort and ⋯'s Group by Project.
///
/// - Rows never move under a finger: while the list is scrolled or out of sight (`hold`, the page reports what it does)
///   a new filing waits, and so does what each row says, since a new line can wrap or a line 3 come and go, and a row's
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
            /// The list draws project headings (`TodosListModel.grouped`): line 3 leaves the project out of a row filed
            /// under one (`TodosListLayout.underProjects`).
            var grouped: Bool
            /// The place shown is a project, whose title names it: line 3 leaves the project out of every row.
            var inProject: Bool
            /// The place shown is Upcoming, whose day headings name each row's day: a row says only its time.
            var underDay: Bool
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

    /// The Filter's choice: All each time the list is made, never stored, so a remembered choice can't hide what needs
    /// the owner (proposal 3.3). Back to All when the Filter goes (the calm list, no rows), so no choice comes back on
    /// its own with it.
    var choice: TodosChoice = .all {
        didSet { if choice != oldValue { relayout() } }
    }
    /// ⋯'s Group by Project, kept on the device by the page (`todos.v1.byProject`): line 3 drops the project its heading
    /// says where the list draws the headings (`grouped`).
    var byProject = false {
        didSet { if byProject != oldValue { relayout() } }
    }
    /// The order inside each section (⋯ › Sort By): kept on the device by the page (`todos.v1.sort`).
    var sort: TodosListSort = .suggested {
        didSet { if sort != oldValue { relayout() } }
    }
    /// The search field's words (`TodosListLayout.Input.query`): this list's, never kept. A search looks through every
    /// open to-do, whatever the place (places pick where, search finds any to-do).
    var query = "" {
        didSet { if query != oldValue { relayout() } }
    }
    /// The place the list shows (`TodosPlaces.holds`): another one lands on All, at once, as the owner's own pick.
    var place: TodoPlaceRef = .all {
        didSet {
            guard place != oldValue else { return }
            recountPlace()
            if choice != .all { choice = .all } else { relayout() }
        }
    }
    /// A place's own numbers (its Filter's counts, its Needs you header), counted from its rows; nil for All To-dos,
    /// which shows the store's. Never held, as the store's are not; set only when they change (P5).
    private(set) var placeCounts: TodoCounts?
    /// What needs the owner outside the place shown ("2 more need you, not in Today"); 0 in All To-dos. Kept, set only
    /// when it changes, so the store's other numbers moving never redraw what reads it (P5).
    private(set) var outsideNeeds = 0
    /// Today's "3 coming up this week".
    private(set) var comingUp = 0
    /// Upcoming's to-dos as its list holds them, for its calendar (⋯ › Show as Calendar): worked out with the place's
    /// numbers, once per change, and set only when they change (P4, P5).
    private(set) var upcomingItems: [TodoItem] = []
    /// The folds the owner opened (Can wait, Someday, Done today): this list's, not kept.
    private(set) var unfolded: Set<String> = []
    private(set) var layout = TodosListLayout.Output.empty
    /// The layout's flags, kept apart from it so the bars and menus that read them redraw when one changes, not at each
    /// landing (P5): the calm rule holds; the search field has words (the list is what they match); the list has rows
    /// under some choice.
    private(set) var calm = false
    private(set) var searching = false
    private(set) var hasRows = false
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
    /// The place's rows as last worked out, and what from: the place's numbers and its layout share one pass (P4).
    @ObservationIgnored private var placeRowsMemo: (place: TodoPlaceRef, now: Date, filed: [TodoFiled], items: [String: TodoItem],
                                                    rows: [TodoFiled])?
    #if DEBUG
    /// `-openTodosChoice <choice>`: the Filter's choice the list takes once it first draws the Filter (measurements).
    @ObservationIgnored private var launchChoice: TodosChoice?
    #endif

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
        /// The store's numbers: an answer given here moves a place's too.
        var counts = TodoCounts()
        static let empty = Snapshot()

        /// The same rows, saying the same, whatever the numbers: nothing to lay out again.
        func sameRows(_ other: Snapshot) -> Bool {
            var numbered = other
            numbered.counts = counts
            return numbered == self
        }
    }

    init(deps: Deps) {
        self.deps = deps
    }

    var groups: [TodosListGroup] { layout.groups }
    /// Whether the Filter is drawn: in All To-dos and a project's place (never in Today or Upcoming, which list open
    /// to-dos already narrowed by date), not before the list can draw, in the calm list, or while nothing is listed under
    /// any choice.
    var filters: Bool { ready && !calm && hasRows && TodosPlaces.filters(place) }
    /// The Filter's choices with the counts of the place shown: never held, so they change in the same update as every
    /// other place that shows them.
    var filterOptions: [TodosFilter.Option] {
        TodosFilter.options(placeCounts ?? deps.store.countsSource.counts, doneToday: layout.doneToday)
    }
    /// N, as the drawer, the guard line and the Needs you header say it; in a place, its part of N.
    var needsYou: Int { placeCounts?.needsYou ?? deps.store.countsSource.needsYou }
    /// What needs the owner that the list does not show (the guard line's "3 more need you"): what the choice hides,
    /// and in a place what is outside it as well, one line for both; none while searching, which sets the place and the
    /// choice aside.
    var hiddenNeeds: Int {
        guard !searching else { return 0 }
        guard let placed = placeCounts else { return calm ? 0 : TodosFilter.hiddenNeeds(choice, needsYou: needsYou) }
        return outsideNeeds + TodosFilter.hiddenNeeds(choice, needsYou: placed.needsYou)
    }
    /// What `hiddenNeeds` counts is all outside the place shown, the choice hiding none of its own: the line names the
    /// place ("2 more need you, not in Today").
    var hiddenElsewhere: Bool {
        guard let placed = placeCounts, !searching else { return false }
        return TodosFilter.hiddenNeeds(choice, needsYou: placed.needsYou) == 0
    }
    /// The title's subtitle (iOS 26 and later) once the guard line has scrolled away: what that line said, so what needs
    /// the owner stays in sight. A choice hiding it says its name and how many ("In Progress · 3 need you"); in a place,
    /// what needs the owner outside it says the line's own words ("2 more need you, not in Today"), since the choice
    /// does not hide those. Nothing while nothing is hidden or a search sets the place and the choice aside.
    func subtitle(place name: String) -> String {
        guard !searching else { return "" }
        guard let placed = placeCounts else { return calm ? "" : TodosFilter.subtitle(choice, needsYou: hiddenNeeds) }
        guard outsideNeeds > 0 else { return TodosFilter.subtitle(choice, needsYou: placed.needsYou) }
        return TodosFilter.guardWords(hiddenNeeds, elsewhere: hiddenElsewhere, place: name)
    }
    /// Nothing to list under the current choice or search.
    var isEmpty: Bool { layout.groups.allSatisfy { $0.keys.isEmpty && $0.folds.isEmpty && $0.tail == nil } }
    /// A place other than All To-dos with nothing in it says so in its own words (`TodoPlaceEmptyState`); under another
    /// choice, that choice's empty state, and while searching, the search's.
    var placeIsEmpty: Bool { !place.isAll && isEmpty && !searching && (!hasRows || calm || choice == .all) }

    /// The list draws project headings: Group by Project, where the list draws them (not the calm list's one card, nor
    /// under Needs You or Done, which draw one section or the days). Needs you's rows and the tail folds' sit under none.
    nonisolated static func grouped(_ byProject: Bool, calm: Bool, choice: TodosChoice, searching: Bool) -> Bool {
        byProject && !calm && (searching || TodosFilter.groupingNote(choice) == nil)
    }

    /// A drawn row's cell; kept until the layout that drops its row lands.
    func row(_ key: String) -> Row? { cells[key] }

    /// Whether `key`'s row is drawn now: in a group, or in a fold that is open (the place shown may leave it out).
    func draws(_ key: String) -> Bool { layout.groups.contains { $0.drawnKeys.contains(key) } }

    /// Follows the store from now on; the first layout lands at once.
    func start() {
        guard !following else { return }
        following = true
        #if DEBUG
        launchChoice = UserDefaults.standard.string(forKey: "openTodosChoice").flatMap(TodosChoice.init(rawValue:))
        #endif
        now = deps.now()
        hold.land = { [weak self] in self?.landAll(animated: true) }
        snapshot = current()
        ready = snapshot.ready
        recountPlace()
        landAll(force: true, animated: false)
    }

    /// A fold opened or closed; nothing while searching, which draws every fold open.
    func toggle(_ fold: String) {
        guard !searching else { return }
        if unfolded.contains(fold) { unfolded.remove(fold) } else { unfolded.insert(fold) }
        relayout()
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

    /// Where New To-Do starts in the place shown (proposal 3.3, "Capture default"): in All To-dos, nowhere in
    /// particular; on Today, due today; on Upcoming, on a day its sheet asks for, tomorrow unless a day heading's + gave
    /// `day` (or an empty slot of its calendar `day` and, for an hour, `time`); in a project, in it.
    func capture(on day: Date? = nil, at time: Date? = nil) -> TodoCapture {
        let calendar = deps.calendar()
        if let day { return TodoCapture(day: calendar.startOfDay(for: day), at: time, project: place.project) }
        let today = calendar.startOfDay(for: deps.now())
        switch place.id {
        case TodoPlaceKind.allValue: return TodoCapture()
        case TodoPlaceKind.today: return TodoCapture(day: today)
        case TodoPlaceKind.upcoming: return TodoCapture(day: calendar.date(byAdding: .day, value: 1, to: today) ?? today)
        default: return TodoCapture(project: place.project)
        }
    }

    /// New To-Do: listed at once ("Saving…"), first in To do and in the place it was made in (due `due`, in `project`,
    /// as its `capture` said), and sent now with them in the one write; Undo deletes it again. false: nothing to add, or
    /// no hub.
    @discardableResult
    func create(title: String, notes: String?, due: APIClient.TodoPlanChange.Due? = nil, project: String? = nil) -> Bool {
        let title = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let notes = notes?.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !title.isEmpty else { return false }
        let made = deps.store.manager(of: .current)
        var dueAt: Date?, dueDate: String?
        switch due {
        case .some(.at(let at)): dueAt = at
        case .some(.day(let day)): dueDate = day
        case .some(.none), nil: break
        }
        let write = TodoOutbox.Write.create(title: title, notes: notes?.isEmpty == false ? notes : nil, dueAt: dueAt, dueDate: dueDate,
                                            project: project?.isEmpty == false ? project : nil)
        guard let entry = deps.outbox.add(write, todo: TodoOutbox.newID(), manager: made) else { return false }
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

    /// Where a new to-do the hub turned down starts again (its toast's Edit): due when it was due and in its project,
    /// unless the project is what the hub turned down, wherever the list is now (`TodoOutbox.refusal`).
    func capture(_ draft: TodoOutbox.Draft) -> TodoCapture {
        let due = TodoItem(id: "", title: draft.title, dueAt: draft.dueAt, dueDate: draft.dueDate)
        return TodoCapture(day: TodosPlaces.dueDay(due, deps.calendar()), at: draft.dueAt, project: draft.project)
    }

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
                        named: deps.outbox.named, ready: store.viewsRead, projects: deps.projects(),
                        counts: store.countsSource.counts)
    }

    /// The store changed (or, `clock`, a minute passed): it all lands now, or once the list rests, but a row that comes
    /// to need the owner.
    private func changed(clock: Bool = false) {
        let next = current()
        guard next != snapshot || clock else { return }
        if next != snapshot {
            // Only the store's numbers moved (an answer given here, the hub's counts): a place's numbers follow, and
            // nothing is laid out again, since no row moved or says anything else (P5).
            let numbersOnly = !clock && next.sameRows(snapshot)
            snapshot = next
            if ready != next.ready { ready = next.ready }
            if numbersOnly { return recountPlace() }
        }
        recountPlace()
        guard hold.wait() else { return landAll(force: clock, animated: true) }
        if clock { clockWaits = true }
        updateCells(force: false, held: true, grouped: groupsByProject(calm: calm, searching: searching))
        landNeeds()
    }

    /// Everything that waited lands: where the rows are, then what they say (line 3 follows the grouping drawn), then
    /// both are drawn.
    private func landAll(force: Bool = false, animated: Bool) {
        let laid = makeLayout(snapshot)
        updateCells(force: force || clockWaits, held: false, grouped: groupsByProject(calm: laid.calm, searching: laid.searching))
        clockWaits = false
        hold.landed()
        drawn = snapshot
        // A row gone from the store keeps its cell until now: the layout drawn until now still listed it.
        let keys = Set(snapshot.filed.map(TodosListLayout.key))
        if cells.count > keys.count || cells.keys.contains(where: { !keys.contains($0) }) {
            cells = cells.filter { keys.contains($0.key) }
        }
        show(laid, animated: animated)
    }

    /// While held, the rows that come to need the owner land at once, alone (DESIGN_SYSTEM 3.4; `Hold`: critical changes
    /// land "at once, alone, and the rest goes on waiting"): onto the list as drawn, saying what they say now.
    private func landNeeds() {
        // The place's rows only (all of them while searching): one outside it is never drawn here, so it never lands.
        let searching = !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let rows = searching ? snapshot.filed : placeRows(snapshot.filed, items: snapshot.items, at: now)
        let needs = rows.filter { row in
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

    /// A choice, the search, the sort, the grouping or a fold changed: the owner's own doing, never held, never faded.
    private func relayout() {
        landAll(animated: false)
    }

    /// Draws `laid`, not animated: rows it puts in another group fade in where they now are (proposal 3.9, "Hub
    /// re-files a row"), so nothing slides across the list.
    private func show(_ laid: TodosListLayout.Output, animated: Bool) {
        if calm != laid.calm { calm = laid.calm }
        if searching != laid.searching { searching = laid.searching }
        let rows = !laid.sections.isEmpty
        if hasRows != rows { hasRows = rows }
        // The Filter is gone: what it had chosen goes with it, the list laid out and drawn again as All.
        if laid.calm || !rows, choice != .all {
            choice = .all
            return
        }
        #if DEBUG
        if filters, let chosen = launchChoice {
            launchChoice = nil
            if choice != chosen {
                choice = chosen
                return
            }
        }
        #endif
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
            for fold in group.folds + [group.tail].compactMap({ $0 }) where fold.open {
                for key in fold.keys { at[key] = group.id + "/" + fold.id }
            }
        }
        return at
    }

    /// The rows the place shows of `filed` at `at`: worked out once per place, rows and minute.
    private func placeRows(_ filed: [TodoFiled], items: [String: TodoItem], at: Date) -> [TodoFiled] {
        if let m = placeRowsMemo, m.place == place, m.now == at, m.filed == filed, m.items == items { return m.rows }
        let rows = TodosPlaces.rows(filed, in: place, items: items, now: at, calendar: deps.calendar())
        placeRowsMemo = (place, at, filed, items, rows)
        return rows
    }

    /// A place's numbers, Today's "coming up" and Upcoming's calendar's to-dos, from the store's rows as they are now.
    private func recountPlace() {
        let calendar = deps.calendar()
        let at = now == .distantPast ? deps.now() : now
        let rows = place.isAll ? nil : placeRows(snapshot.filed, items: snapshot.items, at: at)
        let counts = rows.map { deps.store.countsSource.counts(of: $0) }
        if counts != placeCounts { placeCounts = counts }
        let outside = counts.map { max(0, snapshot.counts.needsYou - $0.needsYou) } ?? 0
        if outside != outsideNeeds { outsideNeeds = outside }
        let coming = place == .today ? TodosPlaces.comingUp(snapshot.filed, items: snapshot.items, now: at, calendar: calendar) : 0
        if coming != comingUp { comingUp = coming }
        let upcoming = place == .upcoming ? (rows ?? []).compactMap { snapshot.items[$0.id] } : []
        if upcoming != upcomingItems { upcomingItems = upcoming }
    }

    /// Whether the rows are filed under their projects' headings (line 3 then leaves the project out): ⋯'s Group by
    /// Project where the list draws them, never in Upcoming's days or a project's place, which is one project; a search
    /// sets the place aside.
    private func groupsByProject(calm: Bool, searching: Bool) -> Bool {
        let here = searching || TodosPlaces.groupsByProject(place)
        return here && Self.grouped(byProject, calm: calm, choice: choice, searching: searching)
    }

    /// The list for the place, or, while the search field has words, every open to-do that matches them, whatever the
    /// place and the choice.
    private func makeLayout(_ from: Snapshot) -> TodosListLayout.Output {
        let projects = from.projects
        let searching = !query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let rows = searching ? from.filed : placeRows(from.filed, items: from.items, at: now)
        if place == .upcoming && !searching {
            return TodoUpcomingLayout.layout(rows, items: from.items, now: now, calendar: deps.calendar())
        }
        return TodosListLayout.layout(.init(
            rows: rows, items: from.items, choice: TodosPlaces.filters(place) ? choice : .all,
            byProject: byProject && (searching || TodosPlaces.groupsByProject(place)), sort: sort, unfolded: unfolded,
            query: query, now: now, calendar: deps.calendar(), projectName: { TodoProjectLogic.name($0, in: projects) }))
    }

    /// Each row's content, worked out again only where its facts, its view, its filing, the grouping or the clock
    /// (`force`) changed. `held`: a drawn row keeps what it says until the list rests (`waiting`); otherwise what waited
    /// lands. `grouped`: the rows sit under their project's heading.
    private func updateCells(force: Bool, held: Bool, grouped: Bool) {
        let calendar = deps.calendar()
        let projects = snapshot.projects
        // A project's place names its project in its title: line 3 leaves it out; Upcoming's headings name the days
        // (neither while searching, which lists any to-do under the sections).
        let placed = query.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
        let inProject = place.project != nil && placed
        let underDay = place == .upcoming && placed
        for filed in snapshot.filed {
            guard let item = snapshot.items[filed.id] else { continue }
            let key = TodosListLayout.key(filed)
            let inputs = Row.Inputs(item: item, view: snapshot.views[filed.id], filed: filed, grouped: grouped, inProject: inProject,
                                    underDay: underDay, projects: projects,
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
            let content = TodoRowContentLogic.content(filed, item: item, view: inputs.view, byProject: grouped,
                                                      inProject: inProject, underItsDay: underDay, now: now, calendar: calendar,
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
