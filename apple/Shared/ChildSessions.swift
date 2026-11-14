import Foundation
import Observation

/// Child sessions the hub folds under their parent (`folded_under`, docs/protocol/protocol.md "Session hierarchy").
/// A child is a row of its own only when this list does not hold that parent. The parent's own `state` stays its own
/// on the stored row; list copies show `foldedState`.
nonisolated enum ChildSessions {
    static let feature = "sessions_hierarchy"
    /// The synthetic work-step id prefix. Not an Agent or Task call, so it is not a subagent grid card.
    static let callPrefix = "child:"

    struct Home: Equatable {
        var key: SessionKey
        /// Set only when `key` is the parent and that parent row is in hand.
        var childID: String?
    }

    /// Where a request is shown. The answer still uses the interaction's own id and session.
    static func home(requested: SessionKey, row: Session?, parent: Session?) -> Home {
        guard let row, let parentKey = parentKey(row), let parent, parent.key == parentKey else {
            return Home(key: requested, childID: nil)
        }
        return Home(key: parent.key, childID: row.sessionID)
    }

    /// The listed parent this row folds under, on its own machine and hub. A blank or self reference folds nothing.
    static func parentKey(_ row: Session) -> SessionKey? {
        guard let under = row.foldedUnder, !under.isEmpty, under != row.sessionID else { return nil }
        return SessionKey(group: row.group, machineID: row.machineID, sessionID: under)
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

    /// Subagents running on the parent row: the row's own reported count and its working children, once (web
    /// `runningChildren`). A lead that also lists them as background agents is not counted twice.
    static func shownRunning(_ row: Session) -> Int {
        let reported = row.listRunningSubagents ?? row.subagents?.filter(\.running).count ?? 0
        return max(reported, row.children?.working ?? 0)
    }

    /// Drop children whose parent this list holds, and show folded state on the rows that stay. `asks` is the line a
    /// waiting child puts on its parent when the parent itself is not waiting.
    static func fold(_ sessions: [Session], asks: [SessionKey: String] = [:]) -> [Session] {
        let listed = Set(sessions.map(\.key))
        return sessions.compactMap { row in
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

    /// Children folded under `parent`, oldest first. A later copy of the same session replaces an older `rev`.
    static func children(of parent: SessionKey, in rows: [Session]) -> [Session] {
        var best: [String: Session] = [:]
        for row in rows where row.group == parent.group && row.machineID == parent.machineID
            && row.foldedUnder == parent.sessionID && row.sessionID != parent.sessionID {
            if let old = best[row.sessionID], (old.rev ?? -1) > (row.rev ?? -1) { continue }
            best[row.sessionID] = row
        }
        return best.values.sorted { a, b in
            let left = a.startedAt ?? a.updatedAt ?? .distantPast
            let right = b.startedAt ?? b.updatedAt ?? .distantPast
            if left != right { return left < right }
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
    static func merge(held: [Session], changes: [Session], removals: [SessionKey], baseline: Bool) -> [Session] {
        var by: [SessionKey: Session] = [:]
        if !baseline {
            for row in held { by[row.key] = row }
        }
        for key in removals { by[key] = nil }
        for row in changes {
            if let old = by[row.key], (old.rev ?? -1) > (row.rev ?? -1) { continue }
            by[row.key] = row
        }
        return Array(by.values)
    }

    /// The parent's own card wins. Otherwise the first child card's words, for the row the card is shown on.
    static func asks(_ cards: [Interaction]) -> [SessionKey: String] {
        var own: [SessionKey: String] = [:]
        var child: [SessionKey: String] = [:]
        for card in cards {
            guard let text = card.askPreview else { continue }
            let home = SessionKey(group: card.group, machineID: card.machineID, sessionID: card.requestHome)
            if card.sessionID == card.requestHome {
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
        var sessionID: String
        var time: Date?
    }

    /// `started_at` when the hub sent it. Otherwise the first `updated_at` this process saw, so a later push does not
    /// move a card that is still running.
    static func places(_ rows: [Session], seen: inout [String: Date?]) -> [Place] {
        rows.map { row in
            if let started = row.startedAt { return Place(sessionID: row.sessionID, time: started) }
            if let known = seen[row.sessionID] { return Place(sessionID: row.sessionID, time: known) }
            seen[row.sessionID] = row.updatedAt
            return Place(sessionID: row.sessionID, time: row.updatedAt)
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
                return a.sessionID < b.sessionID
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
    @ObservationIgnored var seen: [String: Date?] = [:]
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

    func hold(_ child: Session) {
        guard let parent = ChildSessions.parentKey(child) else { return }
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
