import Foundation
import Observation
import PersonaWire
#if DEBUG && os(iOS) // PersonaCore is linked into the iPhone app only (apple/project.yml)
import OSLog
import PersonaCore
#endif

/// The production Agents-list projection. Swift remains authoritative; the DEBUG shadow can only report a difference.
nonisolated struct AgentSortProjectionAdapter: Sendable {
    static func coreTimestamp(_ date: Date) -> String {
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
        return formatter.string(from: date)
    }

    static func scopeKey(_ scope: RuntimeSessionLists.Scope) -> String {
        AgentListProjection.scopeKey(scope)
    }

    #if DEBUG && os(iOS)
    static func coreInput(_ input: AgentListProjection.Input,
                          settings: AgentListProjection.Settings? = nil,
                          presentation: AgentSortPresentation = .init(), now: Date? = nil,
                          projection: AgentListProjection? = nil) throws -> String {
        try AgentSortCoreInput.encode(input, settings: settings, presentation: presentation, now: now, projection: projection)
    }
    #endif

    private let body: @Sendable (AgentListProjection.Input, Date?) -> AgentListProjection

    init(body: (@Sendable (AgentListProjection.Input, Date?) -> AgentListProjection)? = nil) {
        self.body = body ?? { input, now in AgentListProjection.build(input, now: now) }
    }

    func build(_ input: AgentListProjection.Input, now: Date? = nil) -> AgentListProjection {
        body(input, now)
    }
}

nonisolated struct AgentSortShadowMismatch: Error, Equatable, Sendable {
    let domain = "agents_list"
    let rule: String
    let sessions: Int
    let machines: Int
    let hubs: Int
    let grouping: String
    let quickFilter: String
    let hasQuery: Bool
    let kept: Int
    let folded: Int
    let expandedIdle: Int
    let expandedQuiet: Int
    let hasHeld: Bool

    init(rule: String, input: AgentListProjection.Input, presentation: AgentSortPresentation = .init()) {
        self.rule = rule
        sessions = input.sessions.count
        machines = input.machines.count
        hubs = Set(input.sessions.map(\.group)).count
        grouping = input.settings.grouping
        quickFilter = input.settings.filter.rawValue
        hasQuery = !input.settings.query.isEmpty
        kept = input.settings.kept.count
        folded = presentation.folded.count
        expandedIdle = presentation.expandedIdle.count
        expandedQuiet = presentation.expandedQuiet.count
        hasHeld = input.settings.held != nil
    }
}

/// The C ABI's typed JSON result, shared by the real shadow and its fixture tests. No Foundation dictionary bridging.
nonisolated struct AgentSortCoreProjection: Decodable, Sendable {
    struct Group: Decodable, Sendable {
        let id: String
        let rows: [String]
        let visibleRows: [String]
        let folded: Bool
        let parent: String?
        let stateCounts: [String: Int]?
        let spoken: String?
        enum CodingKeys: String, CodingKey {
            case id, rows, folded, parent, spoken
            case visibleRows = "visible_rows"
            case stateCounts = "state_counts"
        }
    }
    struct Row: Decodable, Equatable, Sendable {
        let id: String
        let foldID: String
        let rows: [String]
        let visibleRows: [String]
        let folded: Bool
        enum CodingKeys: String, CodingKey {
            case id, rows, folded
            case foldID = "fold_id"
            case visibleRows = "visible_rows"
        }
    }

    let counts: [String: Int]
    let groups: [Group]
    let unread: [String]
    let filtered: [String]?
    let unpinned: [String]?
    let rows: [Row]?
    let quietTitle: String??
    let hasQuietTitle: Bool
    enum CodingKeys: String, CodingKey {
        case counts, groups, rows, filtered, unpinned
        case unread = "unread_shown"
        case quietTitle = "quiet_title"
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        counts = try container.decode([String: Int].self, forKey: .counts)
        groups = try container.decode([Group].self, forKey: .groups)
        rows = try container.decodeIfPresent([Row].self, forKey: .rows)
        unread = try container.decode([String].self, forKey: .unread)
        filtered = try container.decodeIfPresent([String].self, forKey: .filtered)
        unpinned = try container.decodeIfPresent([String].self, forKey: .unpinned)
        hasQuietTitle = container.contains(.quietTitle)
        quietTitle = hasQuietTitle ? try container.decodeIfPresent(String.self, forKey: .quietTitle) : nil
    }
}

/// The comparable shape of the core's sort projection. Native fields come from the actual production projection,
/// not a second implementation of its ordering rules. Fixture expectations are never consulted in production.
nonisolated struct AgentSortComparable: Equatable, Sendable {
    struct Group: Equatable, Sendable {
        var id: String
        var rows: [String]
        var visibleRows: [String]
        var folded: Bool
        var parent: String?
        var stateCounts: [String: Int]?
        var spoken: String?
    }
    var filtered: [String]
    var matching: [String]
    var unpinned: [String]
    var pinned: [String]
    var unread: [String]
    var filterCounts: [String: Int]
    var active: [String]
    var quiet: [String]
    var groups: [Group]
    var presentationRows: [AgentSortCoreProjection.Row]?
    var quietTitle: Bool? = nil

    init(native input: AgentListProjection.Input, projection: AgentListProjection? = nil,
         settings: AgentListProjection.Settings? = nil, presentation: AgentSortPresentation = .init(), now: Date? = nil) {
        let settings = settings ?? input.settings
        let projection = projection ?? AgentListProjection.build(input, now: now)
        filtered = projection.filtered.map(\.key.path)
        matching = filtered.sorted()
        unpinned = projection.filtered.filter { !input.settings.pinned($0.key) }.map(\.key.path)
        pinned = projection.pinnedAgents.map(\.key.path)
        unread = projection.unread.map(\.path)
        filterCounts = ["needsYou", "unread", "working", "backgroundWaiting"].reduce(into: [String: Int]()) { result, name in
            result[name] = projection.base.filter { session in
                !session.isNestedSubtask && (name == "needsYou" && session.countedState == .needsYou ||
                    name == "unread" && session.countedState == .unread ||
                    name == "working" && session.countedState == .working ||
                    name == "backgroundWaiting" && session.waitsInBackground && !input.settings.muted(session))
            }.count
        }
        active = []
        quiet = projection.quiet.map(\.id)
        let native = AgentSortComparable.makeGroups(input, projection: projection, settings: settings, presentation: presentation)
        groups = native.groups
        active = settings.grouping == "space" ? projection.active.map(\.id) : groups.map(\.id)
        quietTitle = native.quietTitle
        if presentation.compact, settings.grouping == "space" {
            presentationRows = AgentSortComparable.presentationRows(input, projection: projection, settings: settings, presentation: presentation)
        }
    }

    init(core document: AgentSortCoreProjection, input: AgentListProjection.Input,
         settings: AgentListProjection.Settings? = nil, presentation: AgentSortPresentation = .init()) {
        filterCounts = document.counts.filter { ["needsYou", "unread", "working", "backgroundWaiting"].contains($0.key) }
        filtered = document.filtered ?? document.groups.flatMap(\.rows)
        matching = filtered.sorted()
        unpinned = document.unpinned ?? document.groups.filter { $0.id != "pinned" }.flatMap(\.rows)
        pinned = document.groups.first { $0.id == "pinned" }?.rows ?? []
        unread = document.unread
        active = document.groups.filter { $0.parent != "quiet" }.map(\.id)
        quiet = document.groups.filter { $0.parent == "quiet" }.map(\.id)
        groups = document.groups.map { Group(id: $0.id, rows: $0.rows, visibleRows: $0.visibleRows, folded: $0.folded,
                                             parent: $0.parent, stateCounts: $0.stateCounts, spoken: $0.spoken) }
        if document.hasQuietTitle { quietTitle = document.quietTitle.map { $0 != nil } ?? false }
        presentationRows = document.rows
    }

    static func differences(native: Self, core: Self) -> [String] {
        var differences: [String] = []
        func add(_ rule: String) { if !differences.contains(rule) { differences.append(rule) } }
        if Set(native.filtered) != Set(core.filtered) { add("quick-filter") }
        if Set(native.unpinned) != Set(core.unpinned) { add("pin-membership") }
        if native.pinned != core.pinned { add("pin-order") }
        if native.unread != core.unread { add("unread-shown") }
        if native.groups.map(\.id) != core.groups.map(\.id) { add("grouping") }
        if native.active != core.active { add("active-groups") }
        if native.quiet != core.quiet { add("quiet-groups") }
        if native.filterCounts != core.filterCounts { add("filter-counts") }
        if native.quietTitle != core.quietTitle { add("quiet-title") }
        if native.presentationRows != core.presentationRows { add("presentation-rows") }
        for (nativeGroup, coreGroup) in zip(native.groups, core.groups) {
            if nativeGroup.rows != coreGroup.rows { add("rank") }
            if nativeGroup.visibleRows != coreGroup.visibleRows { add("folding") }
            if nativeGroup.folded != coreGroup.folded { add("folded-state") }
            if nativeGroup.parent != coreGroup.parent { add("quiet-membership") }
            if nativeGroup.stateCounts != coreGroup.stateCounts { add("space-counts") }
        }
        return differences
    }

    private static func makeGroups(_ input: AgentListProjection.Input, projection: AgentListProjection,
                                   settings: AgentListProjection.Settings,
                                   presentation: AgentSortPresentation) -> (groups: [Group], quietTitle: Bool?) {
        let order = settings.order
        let live = projection.unpinned.filter { $0.state.isLive }
        var groups: [Group] = []
        if !projection.pinnedAgents.isEmpty {
            let ids = projection.pinnedAgents.map(\.key.path)
            groups.append(Group(id: "pinned", rows: ids, visibleRows: ids, folded: false, parent: nil, stateCounts: nil))
        }
        switch settings.grouping {
        case "state":
            for (index, key) in AgentListOrder.stateKeys.enumerated() {
                let members = live.filter { order.rank($0) == index }
                guard !members.isEmpty else { continue }
                let id = "state/\(key)"
                let folded = isFolded(id, input: input, presentation: presentation)
                let rows = order.ordered(members).map(\.key.path)
                let visible = folded ? [] : rows
                groups.append(Group(id: id, rows: rows, visibleRows: visible, folded: folded, parent: nil, stateCounts: nil))
            }
            return (groups, nil)
        case "machine":
            let machines = input.machines.filter { machine in live.contains { $0.machineRef == machine.ref } }
            let orderedMachines = settings.sort == "updated" ? AgentListOrder.latestFirst(machines, date: { machine in
                live.filter { $0.machineRef == machine.ref }.map(\.sortDate).max() ?? .distantPast
            }, id: \.ref.path) : machines
            for machine in orderedMachines {
                let members = live.filter { $0.machineRef == machine.ref }
                let id = "machine/\(machine.ref.path)"
                let folded = isFolded(id, input: input, presentation: presentation)
                let rows = order.ordered(members).map(\.key.path)
                let visible = folded ? [] : rows.filter { !settings.query.isEmpty || settings.filter != .all || presentation.expandedIdle.contains("machine-\(machine.ref.path)") || order.stays(projection.sessionsByKey[SessionKey(path: $0)!]!) }
                groups.append(Group(id: id, rows: rows, visibleRows: visible, folded: folded, parent: nil, stateCounts: nil))
            }
            return (groups, nil)
        case "space":
            for (originalSpace, quiet) in projection.active.map({ ($0, false) }) + projection.quiet.map({ ($0, true) }) {
                var space = originalSpace
                if let aggregate = presentation.aggregate[space.id] { space.aggregate = aggregate }
                let folded = quiet ? !presentation.expandedQuiet.contains(space.id) && settings.query.isEmpty : isFolded(space.id, input: input, presentation: presentation)
                let rows = quiet ? settings.order.ordered(space.sessions).map(\.key.path) : (projection.rows[space.id]?.all.flatMap(\.sessions) ?? order.ordered(space.sessions)).map(\.key.path)
                let busy = quiet ? rows : (projection.rows[space.id]?.busy.flatMap(\.sessions) ?? order.ordered(space.sessions)).map(\.key.path)
                let idle = quiet ? [] : (projection.rows[space.id]?.idle.flatMap(\.sessions) ?? []).map(\.key.path)
                let expanded = quiet || !settings.query.isEmpty || settings.filter != .all || presentation.expandedIdle.contains(space.id)
                let visible = folded ? [] : busy + (expanded ? idle : [])
                let counts = [AgentState.needsYou, .working, .unread, .idle, .unknown].reduce(into: [String: Int]()) { result, state in
                    let name = state == .needsYou ? "needs_you" : state.rawValue
                    result[name] = space.count(state)
                }
                groups.append(Group(id: space.id, rows: rows, visibleRows: visible, folded: folded,
                                    parent: quiet ? "quiet" : nil, stateCounts: counts, spoken: StateCounts.spoken(space)))
            }
            return (groups, !projection.quiet.isEmpty)
        default:
            return (groups, nil)
        }
    }

    private static func presentationRows(_ input: AgentListProjection.Input,
                                         projection: AgentListProjection,
                                         settings: AgentListProjection.Settings,
                                         presentation: AgentSortPresentation) -> [AgentSortCoreProjection.Row] {
        func agent(_ session: Session) -> AgentSortCoreProjection.Row {
            .init(id: session.key.path, foldID: session.key.path, rows: [session.key.path],
                  visibleRows: [session.key.path], folded: false)
        }
        func entry(_ entry: TabPanes.Entry) -> AgentSortCoreProjection.Row {
            switch entry {
            case .agent(let session): return agent(session)
            case .tab(let tab):
                let folded = settings.query.isEmpty && presentation.folded.contains(tab.foldID)
                return .init(id: entry.id, foldID: tab.foldID, rows: tab.sessions.map(\.key.path),
                             visibleRows: folded ? [] : tab.sessions.map(\.key.path), folded: folded)
            }
        }
        var rows = projection.pinnedAgents.map(agent)
        for space in projection.active {
            guard !isFolded(space.id, input: input, presentation: presentation) else { continue }
            let parts = projection.rows[space.id] ?? .init(all: [], busy: [], idle: [])
            rows.append(contentsOf: parts.busy.map(entry))
            if presentation.expandedIdle.contains(space.id) || !settings.query.isEmpty {
                rows.append(contentsOf: parts.idle.map(entry))
            }
        }
        for space in projection.quiet where presentation.expandedQuiet.contains(space.id) || !settings.query.isEmpty {
            rows.append(contentsOf: TabPanes.grouped(settings.order.ordered(space.sessions)).map(agent))
        }
        return rows
    }

    private static func isFolded(_ id: String, input: AgentListProjection.Input, presentation: AgentSortPresentation) -> Bool {
        guard input.settings.query.isEmpty else { return false }
        return id == "state/idle" ? !presentation.folded.contains("!" + id) : presentation.folded.contains(id)
    }
}

/// The presentation inputs needed only by the DEBUG shadow. They are deliberately outside the production cache key.
nonisolated struct AgentSortPresentation: Equatable, Sendable {
    var folded = Set<String>()
    var expandedIdle = Set<String>()
    var expandedQuiet = Set<String>()
    var compact = false
    var aggregate: [String: SessionListCounts] = [:]
    var bounded = Set<String>()
}

nonisolated struct ShadowToken: Equatable, Sendable {
    var revision: UInt64
    var settings: AgentListProjection.Settings
    var presentation: AgentSortPresentation
}

#if DEBUG && os(iOS)
/// A cheap launch-owned switch. The view submits immutable snapshots; this object never publishes view state.
@MainActor final class AgentSortShadowObserver {
    private var worker: AgentSortShadow?
    var enabled: Bool { worker != nil }
    init() {}
    func enable() {
        guard worker == nil, ProcessInfo.processInfo.arguments.contains("-showAgentSortShadow") else { return }
        worker = AgentSortShadow(report: Self.report)
    }
    init(testWorker: AgentSortShadow?) { worker = testWorker }

    func submit(feed: AgentsFeed, settings: AgentListProjection.Settings,
                presentation: AgentSortPresentation) {
        guard let worker, let revision = feed.projection.publishedRevision, revision == feed.revision,
              let snapshot = feed.projection.shadowValue else { return }
        let token = ShadowToken(revision: revision, settings: settings, presentation: presentation)
        worker.submit(snapshot.input, projection: snapshot.projection, settings: settings, presentation: presentation,
                      now: snapshot.now, token: token)
    }

    private static nonisolated func report(_ mismatch: AgentSortShadowMismatch) {
        os_log(.error, log: OSLog(subsystem: "Persona.Diagnostics", category: "AgentSortShadow"),
               "domain=%{public}@ rule=%{public}@ sessions=%d machines=%d hubs=%d grouping=%{public}@ quick=%{public}@ query=%{public}@ kept=%d folded=%d expanded_idle=%d expanded_quiet=%d held=%{public}@",
               mismatch.domain, mismatch.rule, mismatch.sessions, mismatch.machines, mismatch.hubs,
               mismatch.grouping, mismatch.quickFilter, mismatch.hasQuery ? "yes" : "no", mismatch.kept,
               mismatch.folded, mismatch.expandedIdle, mismatch.expandedQuiet, mismatch.hasHeld ? "yes" : "no")
    }
}
#endif

#if DEBUG && os(iOS)
/// A latest-only serial queue. The worker converts and calls Rust off the main actor; production never waits for it.
nonisolated final class AgentSortShadow: @unchecked Sendable {
    typealias Report = @Sendable (AgentSortShadowMismatch) -> Void
    private let lock = NSLock()
    private struct Pending {
        var input: AgentListProjection.Input
        var projection: AgentListProjection?
        var settings: AgentListProjection.Settings
        var presentation: AgentSortPresentation
        var now: Date?
    }
    private var queued: Pending?
    private var lastToken: ShadowToken?
    private var running = false
    private var compared = 0
    private var mismatches = 0
    private var reportedCompared = -1
    private let report: Report
    private let project: @Sendable (String) throws -> String

    init(report: @escaping Report, project: (@Sendable (String) throws -> String)? = nil) {
        self.report = report
        self.project = project ?? PersonaCoreProjections.sort
    }

    var isIdle: Bool {
        lock.lock()
        defer { lock.unlock() }
        return !running && queued == nil
    }

    func submit(_ input: AgentListProjection.Input, projection: AgentListProjection? = nil,
                settings: AgentListProjection.Settings? = nil, presentation: AgentSortPresentation = .init(), now: Date? = nil,
                token: ShadowToken? = nil) {
        let pending = Pending(input: input, projection: projection, settings: settings ?? input.settings, presentation: presentation, now: now)
        let token = token ?? ShadowToken(revision: 0, settings: pending.settings, presentation: presentation)
        lock.lock()
        if lastToken == token {
            lock.unlock()
            return
        }
        lastToken = token
        queued = pending
        guard !running else { lock.unlock(); return }
        running = true
        lock.unlock()
        DispatchQueue.global(qos: .utility).async { self.drain() }
    }

    private func drain() {
        lock.lock()
        guard let snapshot = queued else {
            running = false
            if compared > reportedCompared {
                reportedCompared = compared
                report(.init(rule: "summary-compared-\(compared)-mismatches-\(mismatches)", input: .init(sessions: [], machines: [], settings: .init())))
            }
            lock.unlock()
            return
        }
        queued = nil
        lock.unlock()
        compare(snapshot.input, projection: snapshot.projection, settings: snapshot.settings, presentation: snapshot.presentation, now: snapshot.now)
        drain()
    }

    private func compare(_ input: AgentListProjection.Input, projection: AgentListProjection?,
                         settings: AgentListProjection.Settings, presentation: AgentSortPresentation, now: Date?) {
        guard ["state", "space", "machine"].contains(settings.grouping) else {
            report(.init(rule: "grouping-scope", input: input, presentation: presentation))
            return
        }
        guard [AgentFilter.all, .needsYou, .unread, .working, .backgroundWaiting].contains(settings.filter) else {
            report(.init(rule: "quick-filter-scope", input: input, presentation: presentation))
            return
        }
        lock.lock()
        compared += 1
        lock.unlock()
        let json: String
        do { json = try AgentSortCoreInput.encode(input, settings: settings, presentation: presentation, now: now, projection: projection) }
        catch {
            report(.init(rule: "input-encode-error", input: input, presentation: presentation))
            return
        }
        let output: String
        do { output = try project(json) }
        catch {
            report(.init(rule: "core-call-error", input: input, presentation: presentation))
            return
        }
        let core: AgentSortCoreProjection
        do { core = try JSONDecoder().decode(AgentSortCoreProjection.self, from: Data(output.utf8)) }
        catch {
            report(.init(rule: "core-decode-error", input: input, presentation: presentation))
            return
        }
        let native = AgentSortComparable(native: input, projection: projection, settings: settings, presentation: presentation, now: now)
        let coreComparable = AgentSortComparable(core: core, input: input, settings: settings, presentation: presentation)
        if native.groups.contains(where: { $0.spoken != nil }), coreComparable.groups.contains(where: { $0.spoken != nil }),
           zip(native.groups, coreComparable.groups).contains(where: { $0.0.spoken != $0.1.spoken }) {
            report(.init(rule: "space-spoken-scope", input: input, presentation: presentation))
        }
        for rule in AgentSortComparable.differences(native: native, core: coreComparable) {
            lock.lock()
            mismatches += 1
            lock.unlock()
            report(.init(rule: rule, input: input, presentation: presentation))
        }
    }
}

nonisolated private enum AgentSortCoreInput {
    static func prepareSessions(_ input: AgentListProjection.Input) -> [(root: Session, descendants: [Session])] {
        // The production projection is the reference: fold a raw window exactly once, and leave already folded feeds
        // unchanged. Descendants supply independent search evidence; only roots become list rows and counts.
        let runs: ((Session) -> Bool)? = input.settings.grouping == "recent" || input.settings.grouping == "app" ? nil
            : { $0.state.isLive || input.settings.pinned($0.key) }
        let forest = CodexTasks.build(AgentListProjection.preparedSessions(input),
                                      ownRows: input.settings.filter == .all ? [] : input.settings.kept, runs: runs)
        return forest.roots.map { root in
            (root: root, descendants: forest.flattened(under: root.key).map(\.session))
        }
    }

    static func encode(_ input: AgentListProjection.Input,
                       settings: AgentListProjection.Settings? = nil,
                       presentation: AgentSortPresentation = .init(),
                       now: Date? = nil, projection: AgentListProjection? = nil) throws -> String {
        let settings = settings ?? input.settings
        let nativeProjection = projection ?? AgentListProjection.build(input, now: now)
        func encoded(_ session: Session) throws -> [String: Any] {
            var object = try JSONSerialization.jsonObject(with: JSON.encoder.encode(session)) as? [String: Any] ?? [:]
            // The core's held key means Unread. Preserve the other held rank in its presentation input.
            if settings.heldJustFinished, settings.held == session.key, session.state == .idle {
                var work = object["work"] as? [String: Any] ?? [:]
                work["just_finished"] = true
                object["work"] = work
            }
            object["state"] = ChildSessions.foldedState(session).rawValue
            object["agent_label"] = session.agentLabel
            if session.listWaiting != nil {
                let running = session.runningWorkflows
                object["workflows"] = running.isEmpty ? NSNull() : [NSNull()]
                object["workflow_progress"] = try running.first.map { try JSONSerialization.jsonObject(with: JSON.encoder.encode($0)) }
            }
            object["state_since"] = session.stateSince.map(AgentSortProjectionAdapter.coreTimestamp)
            object["updated_at"] = session.updatedAt.map(AgentSortProjectionAdapter.coreTimestamp)
            object["seen_at"] = session.seenAt.map(AgentSortProjectionAdapter.coreTimestamp)
            object["background_waiting"] = session.waitsInBackground
            object["nested_subtask"] = session.isNestedSubtask
            object["nested_match"] = nativeProjection.taskMatches[session.key]?.isEmpty == false
            if let children = session.children {
                object["children"] = ["working": children.working, "needs_you": children.needsYou,
                                      "sides": children.sides, "sides_working": children.sidesWorking,
                                      "sides_needs_you": children.sidesNeedsYou]
            } else {
                object["children"] = NSNull()
            }
            return object
        }
        let sessions = try prepareSessions(input).map { prepared in
            var object = try encoded(prepared.root)
            let descendants = try prepared.descendants.map(encoded)
            if !descendants.isEmpty { object["search_subtasks"] = descendants }
            return object
        }
        let groups = Array(Set(settings.preferences.keys).union(input.sessions.map(\.group)).union(input.machines.map(\.group))).sorted()
        let preferences = Dictionary(uniqueKeysWithValues: groups.map { group in
            let value = settings.preferences[group] ?? .init()
            return (group, ["pins": ["spaces": value.pins.spaces, "sessions": value.pins.sessions],
                            "mutes": ["spaces": value.mutes.spaces, "sessions": value.mutes.sessions, "allowed": value.mutes.allowed]])
        })
        let referenceTime = now
        let object: [String: Any] = [
            "sessions": sessions,
            "machines": input.machines.map { ["id": $0.id, "name": $0.title, "group": $0.group, "manager": $0.manager as Any] },
            "pins": ["spaces": [], "sessions": []],
            "mutes": ["spaces": [], "sessions": [], "allowed": []],
            "machines_by_ref": input.machines.map { $0.ref.path },
            "hub_preferences": preferences,
            "settings": [
                "bounded_spaces": presentation.bounded.sorted(),
                "aggregate": Dictionary(uniqueKeysWithValues: presentation.aggregate.map { (key, value) in
                    (key, ["state": value.state])
                }),
                "grouping": settings.grouping, "quick": settings.filter.rawValue, "sort": settings.sort,
                "query": settings.query, "machine_filter": settings.machine, "search_scope": settings.searchScope.rawValue,
                "search_as_code": settings.searchAsCode,
                "machine_title_filter": settings.machine,
                "harness_filter": settings.harness, "project_filter": settings.project, "model_filter": settings.model,
                "pinned_only": settings.pinnedOnly, "hub_filter": settings.hub, "multi_hub": settings.multiHub,
                "kept": settings.kept.map(\.path).sorted(), "folded": presentation.folded.sorted(),
                "expanded_idle": presentation.expandedIdle.sorted(), "expanded_quiet": presentation.expandedQuiet.sorted(),
                "compact": presentation.compact, "presentation_rows": presentation.compact,
                "inventory": true,
                "held": (settings.heldJustFinished ? nil : settings.held?.path) as Any, "now": referenceTime.map(AgentSortProjectionAdapter.coreTimestamp) as Any,
            ],
        ]
        let data = try JSONSerialization.data(withJSONObject: object)
        return String(decoding: data, as: UTF8.self)
    }
}
#endif
