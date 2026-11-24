import Foundation
import Observation

/// The Agents list's window is separate from the detail/session index. Filtering one list must not erase open chats.
@MainActor @Observable final class RuntimeSessionLists {
    private(set) var lists: [String: SessionListModel<Session>] = [:]
    private(set) var sections: [Scope: SessionListModel<Session>] = [:]
    private(set) var aggregates: [Scope: SessionListCounts] = [:]
    nonisolated struct Scope: Hashable, Sendable {
        var hub: String
        var query: SessionListQuery
    }
    private(set) var selected = SessionListQuery()
    private(set) var hubFilter: String?
    @ObservationIgnored private weak var model: AppModel?
    /// The query each group's list was last selected with, before the current assistant went into it (`managed`).
    @ObservationIgnored private var bases: [String: SessionListQuery] = [:]
    /// Each hub's heading counts are read again for hints at most once a window (#9487): the pending read, its window,
    /// and the latest hint's revision (nil: read every scope's).
    @ObservationIgnored private var countsReads: [String: Task<Void, Never>] = [:]
    @ObservationIgnored private var countsWindows: [String: HintWindow] = [:]
    @ObservationIgnored private var countsHints: [String: String?] = [:]
    /// The Agents lists in sight now (`AgentsCoverWatch`): while any is, hints read as they come (`HintWindow`).
    @ObservationIgnored private var inSight = Set<UUID>()
    private var hintWindowLength: Duration { inSight.isEmpty ? HintWindow.length : .zero }

    /// An Agents list came into sight or went out of it. Coming in, the reads waiting for their windows go at once: a
    /// page out of sight lands what came as soon as it is seen (docs/apple/ios-state.md "Timely").
    func listInSight(_ id: UUID, _ shown: Bool) {
        let before = inSight.isEmpty
        if shown { inSight.insert(id) } else { inSight.remove(id) }
        guard before, !inSight.isEmpty else { return }
        lists.values.forEach { $0.releaseHintWindow() }
        sections.values.forEach { $0.releaseHintWindow() }
        countsWindows.values.forEach { $0.release() }
    }

    /// `query` as `group`'s hub is asked it once the pages list one assistant's things (`AppModel.assistantScope`): with
    /// that manager, from a hub that filters by it (`sessions_manager`). A hub with bounded lists but without the
    /// filter is read as a complete copy instead (`legacy`: every row once, then what changed), filtered here by each
    /// row's `manager`: its pages, totals and counts are every manager's, and a page of fifty could hold none of this
    /// assistant's. A complete copy is never asked with a manager: it is the app's rows of that hub, the other
    /// assistants' too.
    func managed(_ query: SessionListQuery, group: String) -> SessionListQuery {
        var query = query
        query.manager = ""
        // Not before the hub answered its roster in this run: a kept pick or roster filters the rows by then, but the hub
        // is asked as before (it may be one without managers, or have one manager by now).
        guard !query.legacy, let model, let scope = model.assistantScope, scope.group == group, scope.asked else { return query }
        let features = model.features(group)
        if features.contains(SessionListQuery.sessionsManagerFeature) {
            query.manager = scope.current
        } else if !features.isEmpty {
            query.legacy = true // the hub said what it has, and not the filter; before its hello, the pages as they are
        }
        return query
    }

    private func managed(_ scope: Scope) -> Scope { Scope(hub: scope.hub, query: managed(scope.query, group: scope.hub)) }

    /// The current assistant, the roster or the hub's features changed: `group`'s list is asked again when its query
    /// is another now, and its sections and counts, which were the query's before, go.
    func rescope(_ group: String, model: AppModel) async {
        self.model = model
        guard let list = lists[group] else { return }
        let query = managed(bases[group] ?? SessionListQuery(), group: group)
        guard query != list.query else { return }
        for (scope, page) in sections where scope.hub == group {
            page.cancel()
            sections[scope] = nil
        }
        for scope in aggregates.keys where scope.hub == group { aggregates[scope] = nil }
        await list.select(query)
    }

    func bind(_ model: AppModel) { self.model = model }

    #if DEBUG
    /// A list a unit test loaded itself, standing where the group's live list would be.
    func setForTests(_ list: SessionListModel<Session>, group: String) { lists[group] = list }
    #endif

    func list(_ link: HubLink, model: AppModel) -> SessionListModel<Session> {
        self.model = model
        if let list = lists[link.group] { return list }
        let group = link.group
        let list = SessionListModel<Session>(id: { $0.id }, removalID: { SessionListIdentity.runtime($0, group: group)?.path },
            fetch: { [weak link] query, cursor, since in
                guard let link else { throw CancellationError() }
                return try await link.api.sessionListPage(query, cursor: cursor, since: since)
            }, counts: { [weak link] query in
                guard let link else { throw CancellationError() }
                var query = query
                query.state = "" // all quick-filter counts under the same machine/search scope
                return try await link.api.sessionListCounts(query)
            }, hintWindow: HintWindow(length: { [weak self] in self?.hintWindowLength ?? .zero }))
        list.changed = { [weak model, weak list] rows in
            guard let model, let list else { return }
            if list.paged {
                model.landSessionSummaries(rows, group: group)
                model.keepSeen(in: list)
            } else {
                // The query's manager filter (empty: every manager). A held other-manager chat is kept only when this
                // landing filtered it out (#8748); an unfiltered landing still drops a missing row.
                let filter = list.query.manager.isEmpty ? nil : list.query.manager
                model.landCompleteSessionList(rows, group: group, manager: filter)
                // A complete copy from a hub that leaves hidden sessions out stands for its list like a page (nil from
                // an older hub); a card's session it does not hold is looked up.
                model.rememberListedRows(group)
                model.lookUpCardSessions()
                model.countBadges()
            }
            // Whether the list is a window or the whole collection decides where the switcher's counts come from.
            if group == model.hub?.group { model.followNeedsYou() }
        }
        list.outside = { [weak model] rows in model?.landSessionSummaries(rows, group: group) }
        lists[group] = list
        return list
    }

    /// The sessions the Agents list shows: the current assistant's, where the hub has several (`AppModel.scoped`).
    var sessions: [Session] {
        guard let model else { return [] }
        if model.links.isEmpty { return model.allSessions }
        return model.scoped(model.links.flatMap { link -> [Session] in
            guard hubFilter == nil || hubFilter == link.group else { return [] }
            guard let list = lists[link.group], list.paged else {
                return model.allSessions.filter { $0.group == link.group }
            }
            var seen = Set<SessionKey>()
            let rows = list.rows + sections.filter { $0.key.hub == link.group }.values.flatMap(\.rows)
            return rows.filter { seen.insert($0.key).inserted }
        })
    }

    func isPaged(_ group: String) -> Bool { lists[group]?.paged == true }

    /// The current windows draw their own rows. An offline mark must reach them as well as the session index.
    func markSeen(_ keys: [SessionKey], model: AppModel) {
        guard !lists.isEmpty else { return }
        for group in Set(keys.lazy.map(\.group)) {
            guard let list = lists[group], list.paged else { continue }
            model.keepSeen(in: list)
            for (scope, page) in sections where scope.hub == group { model.keepSeen(in: page) }
        }
    }

    private var visibleLists: [SessionListModel<Session>] {
        guard let model else { return [] }
        let groups = Set(model.groupIDs)
        return lists.filter { groups.contains($0.key) && (hubFilter == nil || $0.key == hubFilter) }.values.map { $0 }
    }
    var hasMore: Bool { visibleLists.contains(where: \.hasMore) }
    var loading: Bool { visibleLists.contains(where: \.loading) }
    var paged: Bool { visibleLists.contains(where: \.paged) }
    var errors: [String] { visibleLists.compactMap(\.error) }

    func reload() async {
        sections.values.forEach { $0.cancel() }
        sections = [:]
        aggregates = [:]
        await withTaskGroup(of: Void.self) { tasks in
            for (group, page) in lists where hubFilter == nil || hubFilter == group { tasks.addTask { await page.reload() } }
        }
    }
    private func legacyBase(_ group: String) -> [Session] {
        guard let model else { return [] }
        let query = selected
        return model.scoped(model.allSessions).filter { row in
            row.group == group && !row.assistant && (query.includeHidden || row.hiddenByDirectory != true) && (query.machine.isEmpty || row.machineID == query.machine) &&
                (query.text.isEmpty || [row.title, row.cwd, row.lastText ?? "", row.spaceName ?? "", row.slotName ?? ""]
                    .contains { $0.localizedCaseInsensitiveContains(query.text) })
        }
    }

    var total: Int {
        guard let model else { return 0 }
        return model.links.filter { hubFilter == nil || $0.group == hubFilter }.reduce(0) { sum, link in
            if let list = lists[link.group], list.paged { return sum + list.total }
            let rows = legacyBase(link.group).filter { selected.state.isEmpty ||
                ($0.state.rawValue == selected.state && (selected.state == "needs_you" || !model.isMuted($0))) }
            return sum + rows.count
        }
    }

    var stateCounts: [AgentFilter: Int]? {
        guard paged, let model else { return nil }
        var result: [AgentFilter: Int] = [.all: 0]
        for link in model.links where hubFilter == nil || link.group == hubFilter {
            if let list = lists[link.group], list.paged {
                result[.all, default: 0] += list.counts?.total ?? list.total
                for filter in [AgentFilter.needsYou, .working, .unread] {
                    if let counts = list.counts { result[filter, default: 0] += counts.state[filter.state!.rawValue] ?? 0 }
                }
            } else {
                let rows = legacyBase(link.group)
                result[.all, default: 0] += rows.count
                for filter in [AgentFilter.needsYou, .working, .unread] {
                    result[filter, default: 0] += rows.filter { filter.matches($0, muted: model.isMuted($0)) }.count
                }
            }
        }
        return result
    }

    func select(_ query: SessionListQuery, hub: String?, model: AppModel) async {
        if selected != query || hubFilter != hub {
            sections.values.forEach { $0.cancel() }
            sections = [:]
            aggregates = [:]
        }
        selected = query
        hubFilter = hub
        self.model = model
        await withTaskGroup(of: Void.self) { tasks in
            for link in model.links where hub == nil || hub == link.group {
                let list = list(link, model: model)
                bases[link.group] = query
                let asked = managed(query, group: link.group)
                tasks.addTask { await list.select(asked) }
            }
        }
    }

    func expand(_ scope: Scope) async {
        let scope = managed(scope)
        guard let model, let link = model.link(scope.hub), lists[scope.hub]?.paged == true else { return }
        if let page = sections[scope] { await page.load(); return }
        // Bounded independently of the hub's cursor retention.
        if sections.count >= 24 { return }
        let page = SessionListModel<Session>(id: { $0.id }, removalID: { SessionListIdentity.runtime($0, group: scope.hub)?.path },
            fetch: { [weak link] query, cursor, since in
                guard let link else { throw CancellationError() }
                return try await link.api.sessionListPage(query, cursor: cursor, since: since)
            }, counts: { [weak link] query in
                guard let link else { throw CancellationError() }
                return try await link.api.sessionListCounts(query)
            }, hintWindow: HintWindow(length: { [weak self] in self?.hintWindowLength ?? .zero }))
        page.changed = { [weak model, weak page] rows in
            guard let model, let page else { return }
            model.landSessionSummaries(rows, group: scope.hub)
            model.keepSeen(in: page)
        }
        page.outside = { [weak model] rows in model?.landSessionSummaries(rows, group: scope.hub) }
        sections[scope] = page
        await page.select(scope.query)
    }

    func aggregate(_ scope: Scope) -> SessionListCounts? {
        let scope = managed(scope)
        return sections[scope]?.counts ?? aggregates[scope]
    }

    func loadCounts(_ scope: Scope) async {
        let scope = managed(scope)
        guard aggregate(scope) == nil, let model, let link = model.link(scope.hub), lists[scope.hub]?.paged == true else { return }
        let query = selected
        let counts = try? await link.api.sessionListCounts(scope.query)
        guard selected == query else { return }
        aggregates[scope] = counts
    }

    func appeared(_ session: Session) async {
        for (scope, page) in sections where scope.hub == session.group && page.rows.suffix(5).contains(where: { $0.key == session.key }) {
            await page.next()
        }
        guard let page = lists[session.group], page.rows.suffix(5).contains(where: { $0.key == session.key }) else { return }
        await page.next()
    }

    func next() async {
        await withTaskGroup(of: Void.self) { tasks in
            for (group, list) in lists where hubFilter == nil || hubFilter == group { tasks.addTask { await list.next() } }
        }
    }

    /// A `sessions_list` hint: `revision` is the hub's list revision it carries; what already holds it reads nothing.
    func invalidate(_ group: String, revision: String? = nil) {
        lists[group]?.invalidate(revision: revision)
        for (scope, page) in sections where scope.hub == group { page.invalidate(revision: revision) }
        // A hint without a revision asks for every scope; one with a revision only for the counts not at it.
        if let held = countsHints[group] { countsHints[group] = held == nil || revision == nil ? .some(nil) : revision }
        else { countsHints[group] = .some(revision) }
        guard countsReads[group] == nil else { return }
        let window = countsWindows[group] ?? HintWindow(length: { [weak self] in self?.hintWindowLength ?? .zero })
        countsWindows[group] = window
        countsReads[group] = Task { [weak self] in
            do { try await window.enter() } catch { return }
            guard let self, !Task.isCancelled else { return }
            self.countsReads[group] = nil
            let revision = self.countsHints.removeValue(forKey: group) ?? nil
            let scopes = self.aggregates.keys.filter { $0.hub == group && (revision == nil || self.aggregates[$0]?.revision != revision) }
            for scope in scopes {
                self.aggregates[scope] = nil
                Task { await self.loadCounts(scope) }
            }
        }
    }
    func forget() {
        countsReads.values.forEach { $0.cancel() }
        countsReads = [:]
        countsWindows = [:]
        countsHints = [:]
        lists.values.forEach { $0.cancel() }
        sections.values.forEach { $0.cancel() }
        lists = [:]
        sections = [:]
        aggregates = [:]
        bases = [:]
        model?.forgetListedRows() // an older pairing's or query's rows are no hub's list any more
        model = nil
    }
}

extension AppModel {
    /// Only summaries land here; complete fields already fetched for a chat stay intact.
    /// Rows keep their places and new ones go at the end: a read that brings the same rows again leaves every list
    /// equal, so nothing that reads the sessions redraws (#4683).
    func landSessionSummaries(_ rows: [Session], group: String) {
        let old = allSessions.filter { $0.group == group }
        let index = Dictionary(old.map { ($0.key, $0) }, uniquingKeysWith: { a, _ in a })
        let incoming = rows.map { $0.preservingDetails(from: index[$0.key]) }
        if heldRetryRestoration != nil || !heldRetryReceipts.isEmpty {
            reconcileHeldRetries(Dictionary(grouping: rows.filter { $0.undelivered != nil }, by: \.machineRef))
        }
        applySessions(Dictionary(grouping: Self.merging(incoming, into: old), by: \.machineRef), complete: false)
        // The lists' rows changed: a pending card's session they no longer hold is looked up (hidden, or off the page),
        // and the counts read the cards again.
        rememberListedRows(group)
        lookUpCardSessions()
        countBadges()
    }

    /// `old` with each row `incoming` has replaced where it is (one copy), then the rows it did not have, in their order.
    nonisolated static func merging(_ incoming: [Session], into old: [Session]) -> [Session] {
        var pending = Dictionary(incoming.map { ($0.key, $0) }, uniquingKeysWith: { first, _ in first })
        var placed = Set<SessionKey>()
        var out: [Session] = []
        out.reserveCapacity(old.count + incoming.count)
        for row in old {
            let key = row.key
            if let new = pending[key] {
                if placed.insert(key).inserted { out.append(new) }
            } else {
                out.append(row)
            }
        }
        for row in incoming where placed.insert(row.key).inserted {
            out.append(pending.removeValue(forKey: row.key) ?? row)
        }
        return out
    }
}
