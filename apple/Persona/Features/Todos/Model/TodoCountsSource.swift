import Foundation
import Observation
import PersonaWire

/// Every To-dos number in one place (#10465, docs/apple/ios.md "Filing and counts"; DESIGN_SYSTEM 4.8, "every place
/// that collects requests shows the same number"): the To-dos row in the drawer and sidebar, the folded Pages tally, the section bar, the Needs
/// you header, the places and the to-do's page all read these, so an answer anywhere moves every number in one update.
///
/// - One manager: the hub's `counts` and places as sent.
/// - Several managers, or a hub that sends no counts: counted from the current assistant's rows as filed
///   (`TodoFiling`, `TodoCountsFallback`), since the hub adds every manager's up while the pages list the current
///   assistant's; only its own project places are shown, each with its part of N counted the same way.
/// - Less what the owner answered on this device that the hub's view does not show yet (`answered(_:of:)`): a to-do in
///   Needs you whose every ask is answered leaves N at once, and comes back if the answer is undone.
/// - With what the owner did on this device that the hub has not filed yet (`Pending`, the list's writes still in
///   `TodoOutbox`): a deleted to-do is off the list and out of every number, one put in Someday is filed there, and a
///   new one is a To do row of its own, first in its section, until the hub's view says the same.
///
/// Each number is set only when it changes, so a push that moves none redraws nothing that reads them. The rows are
/// filed here once for the list and the numbers alike (`filed`), so the list's sections and the numbers cannot disagree.
@MainActor @Observable
final class TodoCountsSource {
    private(set) var counts = TodoCounts(sections: Dictionary(uniqueKeysWithValues: TodoListSection.all.map { ($0, 0) }))
    /// All To-dos, Today, Upcoming, then the projects; All only from a hub without places.
    private(set) var places: [TodoPlace] = [TodoPlace(id: TodoPlaceKind.allValue, kind: TodoPlaceKind.allValue)]
    /// The current assistant's rows as filed, in the list's order (`TodoFiling.ordered`): the V1 list draws its sections
    /// from these, the rows the numbers were counted from.
    private(set) var filed: [TodoFiled] = []
    /// N: the to-dos that need the owner.
    var needsYou: Int { counts.needsYou }
    /// Whether a counted row is filed by the app's own rules, which read the clock: the store counts again each minute
    /// while there is one (its sessions are watched here).
    @ObservationIgnored private(set) var byRules = false {
        didSet { if byRules != oldValue { byRulesChanged?() } }
    }
    /// Told when `byRules` changes, whatever counted again (a read, the clock, a session, another assistant made
    /// current): the store starts or stops its minute clock.
    @ObservationIgnored var byRulesChanged: (@MainActor () -> Void)?

    /// A change the owner made on this device that the hub's view does not show yet (`TodoOutbox.pending`).
    nonisolated enum Pending: Equatable, Sendable {
        /// Deleted: off the list and out of every number.
        case gone
        /// Put in Someday: filed there.
        case someday
        /// Made here and not on the hub yet: a To do row of its own, first in its section.
        case added(TodoItem)
    }

    /// What the store holds: every row (each manager's), their views, and the hub's counts and places.
    struct Input {
        var rows: [TodoItem] = []
        var views: [String: TodoSummary] = [:]
        var counts: TodoCounts?
        var places: TodoPlaces?
        var now = Date.distantPast
    }

    @ObservationIgnored private var input = Input()
    /// The asks answered on this device that the hub's view still lists, by to-do id.
    @ObservationIgnored private var overlay: [String: Set<String>] = [:]
    @ObservationIgnored private let scope: @MainActor () -> AssistantScope?
    @ObservationIgnored private let lookup: @MainActor ([TodoItem]) -> TodosLogic.SessionLookup?
    @ObservationIgnored private let now: @MainActor () -> Date
    @ObservationIgnored private let pending: @MainActor () -> [String: Pending]
    @ObservationIgnored private var watchesScope = false
    @ObservationIgnored private var watchesPending = false
    /// The Agents of the rows filed by the app's own rules whose sessions are watched, and the watch's token: a session
    /// of theirs changing files them again (an older manager sends no push for it).
    @ObservationIgnored private var watched: (agents: Set<String>, token: Int)?
    @ObservationIgnored private var watchToken = 0

    /// `scope`: the current assistant's, nil with one manager (`TodoStore.Managers.scope`). `lookup`: the sessions of the
    /// given rows' Agents, read only to file a row without a view, one session at a time so only theirs are watched.
    /// `now`: the time a row is filed at again when the clock or a session moves it (`tick()`). `pending`: the list's
    /// writes the hub has not filed yet, by to-do id, followed as `scope` is.
    init(scope: @escaping @MainActor () -> AssistantScope?,
         lookup: @escaping @MainActor ([TodoItem]) -> TodosLogic.SessionLookup? = { _ in nil },
         now: @escaping @MainActor () -> Date = { Date() },
         pending: @escaping @MainActor () -> [String: Pending] = { [:] }) {
        self.scope = scope
        self.lookup = lookup
        self.now = now
        self.pending = pending
    }

    /// The store's rows, views, counts or places changed: answers the hub now shows are let go, and the numbers counted
    /// again.
    func update(_ input: Input) {
        self.input = input
        for (todo, asks) in overlay {
            let listed = Set(input.views[todo]?.asks?.map(\.id) ?? [])
            let kept = asks.intersection(listed)
            overlay[todo] = kept.isEmpty ? nil : kept
        }
        recount()
    }

    /// The owner answered ask `ask` of to-do `todo` here; it counts as answered until the hub's view stops listing it.
    func answered(_ ask: String, of todo: String) {
        guard !(overlay[todo]?.contains(ask) ?? false) else { return }
        overlay[todo, default: []].insert(ask)
        recount()
    }

    /// The answer was undone, or the hub refused it: the ask counts again.
    func unanswered(_ ask: String, of todo: String) {
        guard overlay[todo]?.remove(ask) != nil else { return }
        if overlay[todo]?.isEmpty == true { overlay[todo] = nil }
        recount()
    }

    /// What the last run showed, kept with the views (`TodoViewCache`), until the first count of this one: the first
    /// frame draws the numbers it will keep.
    func seed(_ counts: TodoCounts?, places: [TodoPlace]?) {
        if let counts, counts != self.counts { self.counts = counts }
        if let places, !places.isEmpty, places != self.places { self.places = places }
    }

    /// Another hub, or none: nothing kept.
    func reset() {
        overlay = [:]
        watched = nil
        update(Input())
    }

    /// Counts again now: the rows filed by the app's own rules move with the clock and their sessions.
    func tick() {
        input.now = now()
        recount()
    }

    private func recount() {
        let scope = watchedScope()
        let pending = watchedPending()
        let rows = scope.map { s in input.rows.filter { s.has($0.manager) } } ?? input.rows
        let hubCounts = scope == nil ? input.counts : nil
        let ruled = rows.filter { row in input.views[row.id].map(TodoFiling.knows) != true }
        byRules = !ruled.isEmpty
        if ruled.isEmpty { watched = nil }
        let hubFiled = TodoFiling.file(rows, views: input.views, now: input.now, lookup: { sessions(of: ruled) })
        let filed = pending.isEmpty ? hubFiled : Self.applying(pending, to: hubFiled, scope: scope)
        let ordered = TodoFiling.ordered(filed)
        if ordered != self.filed { self.filed = ordered }
        var next: TodoCounts
        var awayByProject: [String: Int] = [:]
        if let hubCounts {
            // The hub's numbers, moved by what the owner's writes still here move: the same count the rows give, before
            // and after them.
            next = pending.isEmpty ? hubCounts : Self.shifted(hubCounts, from: TodoCountsFallback.counts(hubFiled),
                                                              to: TodoCountsFallback.counts(filed))
            if !pending.isEmpty {
                let before = TodoCountsFallback.needsYouByProject(hubFiled), after = TodoCountsFallback.needsYouByProject(filed)
                for (project, n) in before where n > (after[project] ?? 0) { awayByProject[project] = n - (after[project] ?? 0) }
            }
        } else {
            next = TodoCountsFallback.counts(filed)
        }
        let carried = overlay.isEmpty ? [] : TodoCountsFallback.carried(filed)
        for row in filed where row.section == TodoListSection.needsYou && !carried.contains(row.id) && !row.asks.isEmpty
            && Set(row.asks).isSubset(of: overlay[row.id] ?? []) {
            next.sections[TodoListSection.needsYou] = max(0, (next.sections[TodoListSection.needsYou] ?? 0) - 1)
            guard !row.quiet else { continue }
            next.needsYou = max(0, next.needsYou - 1)
            if let project = row.project { awayByProject[project, default: 0] += 1 }
        }
        if next != counts { counts = next }
        let nextPlaces = places(scope: scope, filed: filed, awayByProject: awayByProject)
        if nextPlaces != places { places = nextPlaces }
    }

    private func places(scope: AssistantScope?, filed: [TodoFiled], awayByProject: [String: Int]) -> [TodoPlace] {
        guard let sent = input.places?.places, !sent.isEmpty else {
            return [TodoPlace(id: TodoPlaceKind.allValue, kind: TodoPlaceKind.allValue, needsYou: counts.needsYou)]
        }
        let byProject = scope == nil ? [:] : TodoCountsFallback.needsYouByProject(filed)
        var out: [TodoPlace] = []
        for var place in sent {
            switch place.kind {
            case TodoPlaceKind.allValue:
                place.needsYou = counts.needsYou
            case TodoPlaceKind.project:
                let slug = place.project ?? ""
                if let scope {
                    guard scope.has(place.manager) else { continue }
                    place.needsYou = byProject[slug] ?? 0
                }
                place.needsYou = max(0, place.needsYou - (awayByProject[slug] ?? 0))
            default:
                break // Today and Upcoming draw no count; a kind this build does not know is kept as sent
            }
            out.append(place)
        }
        return out
    }

    /// `rows` with the owner's writes still on this device (`Pending`): the deleted gone, those put in Someday filed there
    /// first, the new ones first in To do. A new one is the current assistant's (`scope`) as the list is.
    nonisolated static func applying(_ pending: [String: Pending], to rows: [TodoFiled], scope: AssistantScope?) -> [TodoFiled] {
        var out: [TodoFiled] = []
        out.reserveCapacity(rows.count)
        for var row in rows {
            switch pending[row.id] {
            case .gone?:
                continue
            case .someday?:
                (row.section, row.rank, row.closed, row.asks) = (TodoListSection.someday, -1, false, [])
            default:
                break
            }
            out.append(row)
        }
        let listed = Set(rows.map(\.id))
        let added = pending.compactMap { id, change -> TodoItem? in
            guard case .added(let item) = change, !listed.contains(id), scope?.has(item.manager) ?? true else { return nil }
            return item
        }
        // Before the hub's own rows (ranks from 0), the newest first, as the hub puts a new row first in its section.
        let newest = added.sorted { ($0.createdAt ?? .distantPast, $0.id) > ($1.createdAt ?? .distantPast, $1.id) }
        for (n, item) in newest.enumerated() {
            out.append(TodoFiled(id: item.id, manager: item.manager, section: TodoListSection.todo, closed: false,
                                 rank: n - newest.count, inactive: false, byView: true, quiet: false, partOf: nil,
                                 project: item.project, asks: []))
        }
        return out
    }

    /// `counts` moved by the difference between two countings of the same rows.
    nonisolated static func shifted(_ counts: TodoCounts, from before: TodoCounts, to after: TodoCounts) -> TodoCounts {
        var out = counts
        out.open = max(0, out.open + after.open - before.open)
        out.needsYou = max(0, out.needsYou + after.needsYou - before.needsYou)
        out.inProgress = max(0, out.inProgress + after.inProgress - before.inProgress)
        for section in Set(before.sections.keys).union(after.sections.keys) {
            let moved = (after.sections[section] ?? 0) - (before.sections[section] ?? 0)
            guard moved != 0 else { continue }
            out.sections[section] = max(0, (out.sections[section] ?? 0) + moved)
        }
        return out
    }

    /// The sessions of `rows`' Agents, watched: when one changes the rows are filed again. Watched anew only when the
    /// Agents change or the last watch fired, so a read that changes nothing adds no watch.
    private func sessions(of rows: [TodoItem]) -> TodosLogic.SessionLookup? {
        let agents = Set(rows.flatMap { $0.agents.map { "\($0.machineID)/\($0.sessionID)" } })
        if watched?.agents == agents { return lookup(rows) }
        watchToken += 1
        let token = watchToken
        watched = (agents, token)
        return withObservationTracking { lookup(rows) } onChange: { [weak self] in
            // Called once, before the change lands: filed again once it has.
            Task { @MainActor [weak self] in
                guard let self, watched?.token == token else { return }
                watched = nil
                tick()
            }
        }
    }

    /// The owner's writes still on this device, followed: a tick, a delete or a new to-do moves the numbers at once.
    private func watchedPending() -> [String: Pending] {
        guard !watchesPending else { return pending() }
        watchesPending = true
        return withObservationTracking { pending() } onChange: { [weak self] in
            // Called once, before the change lands: counted again once it has.
            Task { @MainActor [weak self] in
                self?.watchesPending = false
                self?.recount()
            }
        }
    }

    /// The current assistant, followed: when the owner switches assistants the numbers are counted for the new one.
    private func watchedScope() -> AssistantScope? {
        guard !watchesScope else { return scope() }
        watchesScope = true
        return withObservationTracking { scope() } onChange: { [weak self] in
            // Called once, before the change lands: counted again once it has.
            Task { @MainActor [weak self] in
                self?.watchesScope = false
                self?.recount()
            }
        }
    }
}
