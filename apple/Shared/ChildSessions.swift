import Foundation
import Observation

/// Child sessions the hub folds under their parent (`folded_under`, docs/protocol/protocol.md "Session hierarchy").
/// A child is a row of its own only when this list does not hold that parent. The parent's own `state` stays its own
/// on the stored row; list copies show `foldedState`.
nonisolated enum ChildSessions {
    static let feature = "sessions_hierarchy"
    /// Hub capability for cross-machine folding (`folded_under_machine_id`, `parent_machine_id`, `cross_machine_hierarchy=1`).
    static let crossMachineFeature = "sessions_cross_machine_hierarchy"
    /// The synthetic work-step id prefix. Not an Agent or Task call, so it is not a subagent grid card.
    static let callPrefix = "child:"

    /// Stable synthetic work-step id carrying the full `SessionKey` (group + machine + session) so cross-machine
    /// children sharing a `sessionID` never collide. Normal IDs stay human-readable (`child:box-b/child-1`).
    static func callID(for key: SessionKey) -> String {
        let machine = encodeCallSegment(key.machineID)
        let prefix = key.group.isEmpty ? "" : encodeCallSegment(key.group) + "|"
        return callPrefix + prefix + machine + "/" + key.sessionID
    }

    /// Extracts the full `SessionKey` from a synthetic child work-step id (`child:<encoded key>`), falling back to a
    /// same-machine key when given a legacy `child:<sessionID>` id.
    static func sessionKey(fromCallID id: String, defaultGroup: String = "", defaultMachineID: String = "") -> SessionKey? {
        guard id.hasPrefix(callPrefix) else { return nil }
        let raw = String(id.dropFirst(callPrefix.count))
        guard !raw.isEmpty else { return nil }
        var rest = Substring(raw)
        var group = ""
        if let bar = rest.firstIndex(of: "|"), rest[..<bar].firstIndex(of: "/") == nil {
            group = decodeCallSegment(String(rest[..<bar]))
            rest = rest[rest.index(after: bar)...]
        }
        if let slash = rest.firstIndex(of: "/") {
            let machine = decodeCallSegment(String(rest[..<slash]))
            let session = String(rest[rest.index(after: slash)...])
            if !machine.isEmpty && !session.isEmpty {
                return SessionKey(group: group, machineID: machine, sessionID: session)
            }
        }
        guard !defaultMachineID.isEmpty else { return nil }
        return SessionKey(group: defaultGroup, machineID: defaultMachineID, sessionID: raw)
    }

    private static func encodeCallSegment(_ segment: String) -> String {
        segment
            .replacingOccurrences(of: "%", with: "%25")
            .replacingOccurrences(of: "/", with: "%2F")
            .replacingOccurrences(of: "|", with: "%7C")
    }

    private static func decodeCallSegment(_ segment: String) -> String {
        segment
            .replacingOccurrences(of: "%7C", with: "|")
            .replacingOccurrences(of: "%2F", with: "/")
            .replacingOccurrences(of: "%25", with: "%")
    }

    /// Unified parent-row rollup for subagents (#27): Needs you > Running > quiet when all ended.
    struct Rollup: Equatable, Hashable, Sendable {
        enum Kind: Equatable, Hashable, Sendable {
            case needsYou(Int)
            case running(Int)
            case quiet
        }

        var kind: Kind
        var needsYouCount: Int
        var runningCount: Int

        var isQuiet: Bool { kind == .quiet }

        /// Localized single-line subtitle for the parent row, or nil when quiet.
        var subtitle: String? {
            switch kind {
            case .needsYou(let n):
                String(localized: "\(n) need you [Child Rollup]")
            case .running(let n):
                String(localized: "\(n) running [Child Rollup]")
            case .quiet:
                nil
            }
        }

        /// Accessible spoken label using unified terminology.
        var spoken: String? {
            switch kind {
            case .needsYou(let n):
                String(localized: "Subagents: \(n) need you [Child Rollup]")
            case .running(let n):
                String(localized: "Subagents: \(n) running [Child Rollup]")
            case .quiet:
                nil
            }
        }
    }

    struct Home: Equatable {
        var key: SessionKey
        /// Full key of the redirected child session when `key` is the parent and that parent row is in hand.
        var childKey: SessionKey?
        /// Set only when `key` is the parent and that parent row is in hand.
        var childID: String? { childKey?.sessionID }

        init(key: SessionKey, childKey: SessionKey? = nil) {
            self.key = key
            self.childKey = childKey
        }

        init(key: SessionKey, childID: String?) {
            self.key = key
            self.childKey = childID.map { SessionKey(group: key.group, machineID: key.machineID, sessionID: $0) }
        }
    }

    /// Where a request is shown. The answer still uses the interaction's own id and session.
    static func home(requested: SessionKey, row: Session?, parent: Session?) -> Home {
        guard let row, let parentKey = parentKey(row), let parent, parent.key == parentKey else {
            return Home(key: requested, childKey: nil)
        }
        return Home(key: parent.key, childKey: row.key)
    }

    /// The listed parent this row folds under, resolving `folded_under_machine_id` (defaulting to the child's own
    /// machine when nil/empty) on the same hub group. A blank or self reference folds nothing.
    static func parentKey(_ row: Session) -> SessionKey? {
        guard let under = row.foldedUnder, !under.isEmpty else { return nil }
        let targetMachine = (row.foldedUnderMachineID?.isEmpty == false) ? row.foldedUnderMachineID! : row.machineID
        if targetMachine == row.machineID && under == row.sessionID { return nil }
        return SessionKey(group: row.group, machineID: targetMachine, sessionID: under)
    }

    /// The immediate parent declared by `parent_session_id` (with `parent_machine_id` defaulting to `machineID`),
    /// falling back to `asking_parent_session_id` and `folded_under`. Used to associate archived-parent orphans and
    /// compute subagent nesting depth in Session Overview.
    static func directParentKey(_ row: Session) -> SessionKey? {
        let targetMachine = (row.parentMachineID?.isEmpty == false) ? row.parentMachineID! : row.machineID
        if let parent = row.parentSessionID, !parent.isEmpty,
           !(targetMachine == row.machineID && parent == row.sessionID) {
            return SessionKey(group: row.group, machineID: targetMachine, sessionID: parent)
        }
        if let asking = row.askingParentSessionID, !asking.isEmpty,
           !(targetMachine == row.machineID && asking == row.sessionID) {
            return SessionKey(group: row.group, machineID: targetMachine, sessionID: asking)
        }
        return parentKey(row)
    }

    /// Needs You if the row or any folded child needs the user. Working if a child is working, unless this row's own
    /// reply is unread or still waiting to be answered. Otherwise the row's own state (web `foldedState`).
    static func foldedState(_ row: Session) -> AgentState {
        if row.state == .needsYou || (row.children?.needsYou ?? 0) > 0 { return .needsYou }
        if (row.children?.working ?? 0) > 0, row.state != .unread, !(row.state == .idle && row.awaitsReply) {
            return .working
        }
        return row.state
    }

    /// Non-side, non-worker standalone child sessions belonging to `parent`, deduplicated by `SessionKey`.
    private static func subagentChildren(of parent: SessionKey, in rows: [Session]) -> [Session] {
        guard !rows.isEmpty else { return [] }
        return children(of: parent, in: rows).filter { !SideFold.isSideRow($0) && $0.foldedAs != "worker" }
    }

    /// Whether `row` came from a bounded `summary=1` response whose scalar counts (`running_subagents`, omitted => 0)
    /// are authoritative even when `preservingDetails(from:)` retained an older full row's `subagents` array.
    private static func isSummaryRow(_ row: Session) -> Bool {
        row.listWaiting != nil || row.listBackgroundTasksCount != nil
    }

    /// Subagents running on the parent row:
    /// - On `summary=1` rows, prefers the hub's authoritative scalar `listRunningSubagents` (omitted => 0) over any
    ///   stale cached `row.subagents`, combined non-additively (`max`) with folded children.
    /// - On full rows (`summary=0` / legacy) with `row.subagents`, deduplicates embedded subagents against loaded
    ///   non-side/non-worker `heldChildren` by full `SessionKey` and sums only those known distinct items, falling back
    ///   non-additively (`max`) against unloaded `children.working` counts so overlapping children are never double-counted.
    static func shownRunning(_ row: Session, heldChildren: [Session] = []) -> Int {
        let heldSubagents = subagentChildren(of: row.key, in: heldChildren)
        let hubWorking = max(0, (row.children?.working ?? 0) - (row.children?.sidesWorking ?? 0))
        let heldWorking = heldSubagents.filter { $0.state == .working || $0.backgroundWorking }.count
        let childrenWorking = max(heldWorking, hubWorking)

        if !isSummaryRow(row), let embedded = row.subagents {
            let heldKeys = Set(heldSubagents.map(\.key))
            var embeddedRunningKeys: Set<SessionKey> = []
            for agent in embedded where agent.running {
                let sid = agent.sessionID.isEmpty ? "\(row.sessionID)@\(agent.id)" : agent.sessionID
                let key = SessionKey(group: row.group, machineID: row.machineID, sessionID: sid)
                if !heldKeys.contains(key) {
                    embeddedRunningKeys.insert(key)
                }
            }
            return max(embeddedRunningKeys.count + heldWorking, hubWorking)
        }

        let reported = max(0, row.listRunningSubagents ?? 0)
        return max(reported, childrenWorking)
    }

    /// Total subagents (running + ended) on the parent row, deduplicated by full `SessionKey` across embedded
    /// `row.subagents` and loaded standalone non-side/non-worker children. Uses non-additive `max` against unloaded
    /// `children.total` / `running_subagents` counts so an overlapping child is never double-counted when child rows
    /// are not loaded.
    static func totalSubagentCount(_ row: Session, heldChildren: [Session] = []) -> Int {
        let heldSubagents = subagentChildren(of: row.key, in: heldChildren)
        let nonSideChildren = max(0, (row.children?.total ?? 0) - (row.children?.sides ?? 0))
        let reported = max(0, row.listRunningSubagents ?? 0)

        if let embedded = row.subagents {
            let heldKeys = Set(heldSubagents.map(\.key))
            var embeddedKeys: Set<SessionKey> = []
            for agent in embedded {
                let sid = agent.sessionID.isEmpty ? "\(row.sessionID)@\(agent.id)" : agent.sessionID
                let key = SessionKey(group: row.group, machineID: row.machineID, sessionID: sid)
                if !heldKeys.contains(key) {
                    embeddedKeys.insert(key)
                }
            }
            return max(embeddedKeys.count + heldSubagents.count, nonSideChildren, reported)
        }

        return max(reported, heldSubagents.count, nonSideChildren)
    }

    /// Pure unified parent-row rollup (#27): Needs you priority > Running > quiet when all ended.
    static func childRollup(_ row: Session, heldChildren: [Session] = []) -> Rollup {
        let heldSubagents = subagentChildren(of: row.key, in: heldChildren)
        let hubNeedsYou = max(0, (row.children?.needsYou ?? 0) - (row.children?.sidesNeedsYou ?? 0))
        let heldNeedsYou = heldSubagents.filter { $0.state == .needsYou }.count
        let needsYou = max(heldNeedsYou, hubNeedsYou)
        let running = max(0, shownRunning(row, heldChildren: heldSubagents))
        if needsYou > 0 {
            return Rollup(kind: .needsYou(needsYou), needsYouCount: needsYou, runningCount: running)
        }
        if running > 0 {
            return Rollup(kind: .running(running), needsYouCount: 0, runningCount: running)
        }
        return Rollup(kind: .quiet, needsYouCount: 0, runningCount: 0)
    }

    /// Drop ALL `is_subagent` rows (client-side fallback so no orphan, embedded, same-machine or cross-machine subagent
    /// leaks into the main Agents list) as well as any row whose folded parent this list holds, and show folded state
    /// on the rows that stay. `asks` is the line a waiting child puts on its parent when the parent itself is not waiting.
    static func fold(_ sessions: [Session], asks: [SessionKey: String] = [:]) -> [Session] {
        let listed = Set(sessions.map(\.key))
        return sessions.compactMap { row in
            if row.isSubagent == true { return nil }
            if let parent = parentKey(row), listed.contains(parent) { return nil }
            var copy = row
            if (row.children?.needsYou ?? 0) > 0, row.state != .needsYou, let ask = asks[row.key] {
                copy.attentionReason = ask
            }
            let folded = foldedState(row)
            if folded != row.state { copy.state = folded }
            return copy
        }
    }

    /// Children folded under `parent` (same-machine, cross-machine, and archived-parent orphans), oldest first.
    /// A later copy of the same session replaces an older `rev`.
    static func children(of parent: SessionKey, in rows: [Session]) -> [Session] {
        var best: [SessionKey: Session] = [:]
        for row in rows where row.key != parent {
            let matchesFold = parentKey(row) == parent
            let matchesOrphan = row.isSubagent == true && directParentKey(row) == parent
            guard matchesFold || matchesOrphan else { continue }
            if let old = best[row.key], (old.rev ?? -1) > (row.rev ?? -1) { continue }
            best[row.key] = row
        }
        return best.values.sorted { a, b in
            let left = a.startedAt ?? a.updatedAt ?? .distantPast
            let right = b.startedAt ?? b.updatedAt ?? .distantPast
            if left != right { return left < right }
            if a.machineID != b.machineID { return a.machineID < b.machineID }
            return a.sessionID < b.sessionID
        }
    }

    /// Whether an open parent's child list should be read again. Stop when the held rows match the rollup and nothing
    /// is working or waiting. An empty local list is missing only while the parent says it has children: `roots=1`
    /// keeps children out of the root window, so zero local rows is not "still missing" once `total` is 0.
    static func needsRead(parent: Session?, known: [Session]) -> Bool {
        guard let parent, (parent.children?.total ?? 0) > 0 || !known.isEmpty else { return false }
        if known.count != (parent.children?.total ?? 0) { return true }
        if (parent.children?.working ?? 0) > 0 || (parent.children?.needsYou ?? 0) > 0 { return true }
        return known.contains { $0.state == .working || $0.state == .needsYou || $0.backgroundWorking }
    }

    /// A baseline replaces what is held. A `since` page keeps the rest, drops removals, and applies changes.
    /// Summary rows (`summary=1`) preserve detail-only fields already fetched for an open child session.
    static func merge(held: [Session], changes: [Session], removals: [SessionKey], baseline: Bool) -> [Session] {
        var oldByKey: [SessionKey: Session] = [:]
        for row in held { oldByKey[row.key] = row }
        var by: [SessionKey: Session] = baseline ? [:] : oldByKey
        for key in removals { by[key] = nil }
        for row in changes {
            if let old = by[row.key], (old.rev ?? -1) > (row.rev ?? -1) {
                if old.listWaiting != nil && row.listWaiting == nil {
                    by[row.key] = old.preservingDetails(from: row)
                }
                continue
            }
            let existing = by[row.key] ?? oldByKey[row.key]
            by[row.key] = row.preservingDetails(from: existing)
        }
        return Array(by.values)
    }

    /// The parent's own card wins. Otherwise the first child card's words, for the row the card is shown on
    /// (respecting `folded_under_machine_id` for cross-machine subagents).
    static func asks(_ cards: [Interaction]) -> [SessionKey: String] {
        var own: [SessionKey: String] = [:]
        var child: [SessionKey: String] = [:]
        for card in cards {
            guard let text = card.askPreview else { continue }
            let home = card.requestHomeKey
            if card.sessionKey == home {
                if own[home] == nil { own[home] = text }
            } else if child[home] == nil {
                child[home] = text
            }
        }
        var out = child
        for (key, text) in own { out[key] = text }
        return out
    }

    struct Place: Equatable {
        var key: SessionKey
        var time: Date?

        var sessionID: String { key.sessionID }

        init(key: SessionKey, time: Date?) {
            self.key = key
            self.time = time
        }

        /// Backwards-compatible initializer for same-machine callers and tests.
        init(sessionID: String, time: Date?, machineID: String = "", group: String = "") {
            self.key = SessionKey(group: group, machineID: machineID, sessionID: sessionID)
            self.time = time
        }
    }

    /// `started_at` when the hub sent it. Otherwise the first `updated_at` this process saw, keyed by full
    /// `SessionKey` so cross-machine children sharing a `sessionID` stay distinct.
    static func places(_ rows: [Session], seen: inout [SessionKey: Date?]) -> [Place] {
        rows.map { row in
            if let started = row.startedAt { return Place(key: row.key, time: started) }
            if let known = seen[row.key] { return Place(key: row.key, time: known) }
            seen[row.key] = row.updatedAt
            return Place(key: row.key, time: row.updatedAt)
        }
    }

    /// Backwards-compatible overload keyed by `sessionID` for legacy same-machine callers.
    static func places(_ rows: [Session], seen: inout [String: Date?]) -> [Place] {
        rows.map { row in
            if let started = row.startedAt { return Place(key: row.key, time: started) }
            if let known = seen[row.sessionID] { return Place(key: row.key, time: known) }
            seen[row.sessionID] = row.updatedAt
            return Place(key: row.key, time: row.updatedAt)
        }
    }

    struct Exchange: Equatable {
        var id: String
        var start: Date?
    }

    /// The last exchange that started at or before the child. No time: the latest exchange. A child that started
    /// before the first loaded exchange waits until history is complete, then the first exchange (web `placeChildren`).
    static func assign(_ exchanges: [Exchange], _ children: [Place], complete: Bool) -> [String: [Place]] {
        guard !exchanges.isEmpty else { return [:] }
        var by: [String: [Place]] = [:]
        for child in children {
            let home: String?
            if child.time == nil {
                home = exchanges.last?.id
            } else {
                var found: String?
                for exchange in exchanges {
                    if let start = exchange.start, let time = child.time, start <= time { found = exchange.id }
                }
                home = found ?? (complete ? exchanges.first?.id : nil)
            }
            if let home { by[home, default: []].append(child) }
        }
        var out: [String: [Place]] = [:]
        for exchange in exchanges {
            guard var list = by[exchange.id] else { continue }
            list.sort { a, b in
                let left = a.time ?? .distantFuture
                let right = b.time ?? .distantFuture
                if left != right { return left < right }
                if a.key.machineID != b.key.machineID { return a.key.machineID < b.key.machineID }
                if a.key.sessionID != b.key.sessionID { return a.key.sessionID < b.key.sessionID }
                return a.key.group < b.key.group
            }
            out[exchange.id] = list
        }
        return out
    }

    struct Detail: Equatable {
        var prompt = ""
        var result = ""
        var lastTool: String?
        var resultID: String?
        var fullSize: Int?
    }

    /// The child's prompt (first user text), latest reply, and last tool, from the items already in hand.
    static func detail(head: [Item], latest: [Item], tail: [Item] = []) -> Detail {
        let prompt = (head + latest).first { item in
            item.role == "user" && item.kind == .text && (item.toolUseID ?? "").isEmpty
        }?.text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let reply = latest.last { $0.role == "assistant" && $0.kind == .text && ($0.text ?? "").isEmpty == false }
        let lastUse = latest.last { $0.kind == .toolUse }
        let lastFold = latest.last { $0.kind == .fold }
        var tool = lastUse?.toolName
        if let fold = lastFold, fold.seq > (lastUse?.seq ?? -1) {
            tool = tail.last { $0.kind == .toolUse }?.toolName ?? tool
        }
        return Detail(prompt: prompt, result: reply?.text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
                      lastTool: tool.flatMap { $0.isEmpty ? nil : $0 }, resultID: reply?.id, fullSize: reply?.fullSize)
    }

    /// Running while it works, waits on the user, or its background work runs. Failed on a turn error. Stopped when
    /// the hub says the session was interrupted. Completed otherwise (web `childStatus`).
    enum Status: Equatable { case running, failed, stopped, completed }

    static func status(_ row: Session) -> Status {
        if row.state == .working || row.state == .needsYou || row.backgroundWorking { return .running }
        if let error = row.turnError, !error.isEmpty { return .failed }
        if row.wasInterrupted { return .stopped }
        return .completed
    }
}

/// A lookup already running keeps the latest hint and the caller runs it once that lookup ends. A `sessions_list`
/// that arrives in between is not dropped.
struct FetchHint: Equatable {
    enum Waiting: Equatable {
        case none
        case hint(String?)
    }

    private var running = false
    private(set) var waiting = Waiting.none
    /// A lookup is out: what it answers may stand at any revision.
    var isRunning: Bool { running }

    /// Begin `hint`. False: one is already running, and `hint` replaces whatever was waiting.
    mutating func start(_ hint: String?) -> Bool {
        if running {
            waiting = .hint(hint)
            return false
        }
        running = true
        return true
    }

    /// The lookup ended. The hint that arrived during it, if any.
    mutating func finish() -> Waiting {
        running = false
        let next = waiting
        waiting = .none
        return next
    }
}

/// One open parent's folded children, beside the root list. A paged hub's `roots=1` window does not hold them.
@MainActor @Observable final class ChildSessionIndex {
    private(set) var held: [SessionKey: [Session]] = [:]
    /// The same rows, unread by views. `AppModel.childRows` seeds a per-parent cell from this, not from `held`.
    @ObservationIgnored private(set) var heldNow: [SessionKey: [Session]] = [:]
    /// First `updated_at` seen for a child that has no `started_at`. Not observed: recording it must not redraw.
    @ObservationIgnored var seen: [SessionKey: Date?] = [:]
    @ObservationIgnored private var revision: [SessionKey: String] = [:]
    @ObservationIgnored private var loaded: Set<SessionKey> = []

    func known(_ parent: SessionKey) -> [Session] { held[parent] ?? [] }
    func knownNow(_ parent: SessionKey) -> [Session] { heldNow[parent] ?? [] }

    private func put(_ parent: SessionKey, _ rows: [Session]) {
        heldNow[parent] = rows
        held[parent] = rows
    }
    func checkpoint(_ parent: SessionKey) -> String? { revision[parent] }
    func isLoaded(_ parent: SessionKey) -> Bool { loaded.contains(parent) }

    func row(_ key: SessionKey) -> Session? {
        for rows in held.values {
            if let found = rows.first(where: { $0.key == key }) { return found }
        }
        return nil
    }

    /// Unobserved lookup across all held children (safe from actions and non-UI code, P5).
    func rowNow(_ key: SessionKey) -> Session? {
        for rows in heldNow.values {
            if let found = rows.first(where: { $0.key == key }) { return found }
        }
        return nil
    }

    func hold(_ child: Session) {
        guard let parent = ChildSessions.parentKey(child) ?? (child.isSubagent == true ? ChildSessions.directParentKey(child) : nil) else { return }
        put(parent, ChildSessions.merge(held: heldNow[parent] ?? [], changes: [child], removals: [], baseline: false))
    }

    /// One read of the parent's children. `since` follows the checkpoint. A 409 `revision_expired` reads once more
    /// from the start. Any other failure keeps the checkpoint. No timer: the caller asks on `sessions_list`.
    func refresh(parent: SessionKey, api: APIClient) async {
        await refresh(parent: parent, api: api, retry: true)
    }

    private func refresh(parent: SessionKey, api: APIClient, retry: Bool) async {
        let since = revision[parent]
        do {
            var cursor: String?
            var changes: [Session] = []
            var removals: [SessionKey] = []
            var next: String?
            for _ in 0..<40 {
                let page = try await api.childSessionPage(machine: parent.machineID, parent: parent.sessionID,
                                                           cursor: cursor, since: cursor == nil ? since : nil)
                changes += page.sessions
                removals += (page.remove ?? []).compactMap { SessionListIdentity.runtime($0, group: parent.group) }
                next = page.revision ?? next
                guard page.hasMore == true, let more = page.nextCursor, !more.isEmpty else { break }
                cursor = more
            }
            put(parent, ChildSessions.merge(held: heldNow[parent] ?? [], changes: changes, removals: removals,
                                             baseline: since == nil))
            if let next { revision[parent] = next }
            loaded.insert(parent)
        } catch let error as APIError where retry && error.status == 409 && error.code == "revision_expired" {
            revision[parent] = nil
            loaded.remove(parent)
            await refresh(parent: parent, api: api, retry: false)
        } catch {
            // Keep the checkpoint. The next sessions_list hint tries again.
        }
    }
}
