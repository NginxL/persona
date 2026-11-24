import Foundation

/// Pure projection of all subagents shown in Session Overview (#27, #14322):
/// aggregates embedded `session.subagents` (`parent@agent`), same-machine folded children, and cross-machine A2A /
/// dispatch subagents into **Running · N** (Needs you pinned first, then newest start; hidden when 0) and
/// **Ended · N** (collapsed by default with up to 4 stacked harness brand marks).
/// Compiled into `Shared/` so it is watchOS 10 compatible and unit-tested without SwiftUI.
nonisolated struct SubagentOverview: Equatable, Sendable {
    /// Maximum indentation depth for nested subagents (#27); deeper descendants are flattened to this level.
    static let maxNestingDepth = 2
    /// Maximum stacked harness brand marks shown on the collapsed Ended summary row.
    static let maxEndedPreviewMarks = 4

    enum Status: Int, Equatable, Hashable, Sendable {
        case needsYou
        case running
        case failed
        case ended

        var isRunningGroup: Bool { self == .needsYou || self == .running }
    }

    /// Destination opened when tapping a subagent row in Session Overview.
    enum Target: Equatable, Hashable, Sendable {
        /// Embedded transcript `<parent>@<agent>` on the parent's computer (Claude Code / DSH).
        case embedded(parent: SessionKey, agent: Subagent)
        /// Standalone session row (same-machine folded child or cross-machine A2A / dispatch subagent).
        case session(SessionKey)

        var sessionKey: SessionKey {
            switch self {
            case .embedded(let parent, let agent):
                SessionKey(group: parent.group, machineID: parent.machineID, sessionID: agent.sessionID)
            case .session(let key):
                key
            }
        }
    }

    /// Lightweight harness metadata carried by an overview row (independent of UIKit / AgentMark).
    struct HarnessInfo: Equatable, Hashable, Sendable {
        var agent: String?
        var agentLabel: String?
        var launcher: String?
        var agentSurface: String?
        var sourceKind: String?
        var displayName: String

        init(session: Session) {
            agent = session.agent
            agentLabel = session.agentLabelHint
            launcher = session.launcher
            agentSurface = session.agentSurface
            sourceKind = session.sourceKind
            displayName = session.agentLabel
        }

        init(agent: String? = nil, agentLabel: String? = nil, launcher: String? = nil,
             agentSurface: String? = nil, sourceKind: String? = nil, displayName: String) {
            self.agent = agent
            self.agentLabel = agentLabel
            self.launcher = launcher
            self.agentSurface = agentSurface
            self.sourceKind = sourceKind
            self.displayName = displayName
        }

        /// Stable identity for deduplicating harness marks in the collapsed Ended preview stack.
        var brandIdentity: String {
            let key = displayName.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
            return key.isEmpty ? (agent ?? "claude").lowercased() : key
        }
    }

    struct Item: Identifiable, Equatable, Hashable, Sendable {
        var id: String
        var target: Target
        var title: String
        var harness: HarnessInfo
        var machineID: String
        var machineName: String
        /// Progressive disclosure origin chip: nil when same machine AND same harness as parent.
        var originChip: String?
        var status: Status
        var startedAt: Date?
        var updatedAt: Date?
        /// Second-line latest tool summary, populated ONLY for running items.
        var toolSummary: String?
        /// Indentation level capped at `SubagentOverview.maxNestingDepth`.
        var depth: Int
        var canSend: Bool
        var canInterrupt: Bool
        /// Launch call ID in the parent chat when known (enables "Show in chat").
        var toolUseID: String?
        /// Folded child session ID in the parent chat when known (enables "Show in chat").
        var childCardSessionID: String?
    }

    private struct Node {
        var item: Item
        var parentID: String?
    }

    var running: [Item] = []
    var ended: [Item] = []
    /// Up to 4 harness descriptors shown as stacked brand marks on the collapsed Ended row.
    var endedPreviewHarnesses: [HarnessInfo] = []

    var totalCount: Int { running.count + ended.count }
    var isEmpty: Bool { running.isEmpty && ended.isEmpty }
    var needsYouCount: Int { running.filter { $0.status == .needsYou }.count }

    /// Equatable change token for `SessionOverviewSheet`'s projection invalidation (P4): captures parent metadata,
    /// embedded subagent state resolved against `ChatStore.tasks` (completion/failure notices & `agentStatuses`),
    /// standalone folded/cross-machine child rows, and pending interactions so embedded task status changes refresh
    /// Session Overview immediately without rebuilding in `body`.
    struct InputStamp: Equatable, Sendable {
        struct EmbeddedEntry: Equatable, Sendable {
            var id: String
            var sessionID: String
            var shownRunning: Bool
            var agentStatus: String?
            var updatedAt: Date?
            var lastTool: String?
            var parentID: String?
            var description: String?
            var type: String?
        }

        struct ChildEntry: Equatable, Sendable {
            var key: SessionKey
            var rev: Int64?
            var state: AgentState
            var backgroundWorking: Bool
            var turnError: String?
            var failureReason: String?
            var startedAt: Date?
            var updatedAt: Date?
            var activity: String?
            var waitLine: String?
            var title: String
            var foldedUnder: String?
            var foldedUnderMachineID: String?
            var parentSessionID: String?
            var parentMachineID: String?
            var foldedAs: String?
        }

        var parentState: AgentState?
        var parentAgent: String?
        var parentMachineID: String?
        var embedded: [EmbeddedEntry]
        var agentStatuses: [String: String]
        var children: [ChildEntry]
        var pendingUIDs: [String]
    }

    static func inputStamp(session: Session?, children: [Session], pending: [Interaction], tasks: TaskIndex) -> InputStamp {
        let works = session?.state == .working || session?.state == .needsYou
        let embedded = (session?.subagents ?? []).map { agent in
            InputStamp.EmbeddedEntry(
                id: agent.id,
                sessionID: agent.sessionID,
                shownRunning: agent.running(in: tasks, sessionWorks: works),
                agentStatus: agent.toolUseID.flatMap { tasks.agentStatuses[$0] },
                updatedAt: agent.updatedAt,
                lastTool: agent.lastTool?.summary ?? agent.lastTool?.name,
                parentID: agent.parentID,
                description: agent.description,
                type: agent.type
            )
        }
        let childEntries = children.map { row in
            InputStamp.ChildEntry(
                key: row.key,
                rev: row.rev,
                state: row.state,
                backgroundWorking: row.backgroundWorking,
                turnError: row.turnError,
                failureReason: row.failureReason,
                startedAt: row.startedAt,
                updatedAt: row.updatedAt,
                activity: row.activity,
                waitLine: row.waitLine,
                title: row.displayTitle,
                foldedUnder: row.foldedUnder,
                foldedUnderMachineID: row.foldedUnderMachineID,
                parentSessionID: row.parentSessionID,
                parentMachineID: row.parentMachineID,
                foldedAs: row.foldedAs
            )
        }
        return InputStamp(
            parentState: session?.state,
            parentAgent: session?.agent,
            parentMachineID: session?.machineID,
            embedded: embedded,
            agentStatuses: tasks.agentStatuses,
            children: childEntries,
            pendingUIDs: pending.map(\.uid)
        )
    }

    /// Progressive disclosure origin chip (#27): shown ONLY when child machine or harness differs from parent,
    /// displaying only the differing part (`Machine · Harness` / `Machine` / `Harness`).
    static func originChip(childMachineID: String, childMachineTitle: String, childHarness: String,
                           parentMachineID: String, parentHarness: String) -> String? {
        let machineDiffers = !childMachineID.isEmpty && !parentMachineID.isEmpty && childMachineID != parentMachineID
        let childH = childHarness.trimmingCharacters(in: .whitespacesAndNewlines)
        let parentH = parentHarness.trimmingCharacters(in: .whitespacesAndNewlines)
        let harnessDiffers = !childH.isEmpty && !parentH.isEmpty && childH.caseInsensitiveCompare(parentH) != .orderedSame
        let machineLabel = childMachineTitle.trimmingCharacters(in: .whitespacesAndNewlines)
        let shownMachine = machineLabel.isEmpty ? childMachineID : machineLabel
        switch (machineDiffers, harnessDiffers) {
        case (true, true): return "\(shownMachine) · \(childH)"
        case (true, false): return shownMachine
        case (false, true): return childH
        case (false, false): return nil
        }
    }

    /// Aggregates embedded `session.subagents`, same-machine folded children, and cross-machine A2A / dispatch
    /// subagents for `parentKey`, producing pre-sorted **Running** and **Ended** groups ready for rendering (P4).
    static func build(parent: Session?, parentKey: SessionKey, embedded: [Subagent], foldedChildren: [Session],
                      pendingInteractions: [Interaction] = [],
                      machineName: (MachineRef) -> String? = { _ in nil },
                      agentStatuses: [String: String] = [:]) -> SubagentOverview {
        let parentHarness = parent.map(HarnessInfo.init(session:))
            ?? HarnessInfo(agent: "claude", displayName: "Claude")
        let parentMachineTitle = machineName(parentKey.machineRef) ?? parentKey.machineID
        let pendingSessionKeys = Set(pendingInteractions.map(\.sessionKey))

        let nonSideChildren = foldedChildren.filter { !SideFold.isSideRow($0) && $0.foldedAs != "worker" }
        var embeddedBySessionID: [String: Subagent] = [:]
        for agent in embedded { embeddedBySessionID[agent.sessionID] = agent }

        var nodes: [Node] = []
        var seenSessionKeys: Set<SessionKey> = []

        // 1. Standalone folded & cross-machine child sessions (same-machine + A2A / dispatch / archived-parent orphans).
        for row in nonSideChildren {
            guard row.key != parentKey, seenSessionKeys.insert(row.key).inserted else { continue }
            let embeddedMatch = row.machineID == parentKey.machineID ? embeddedBySessionID[row.sessionID] : nil
            let status = sessionStatus(row, hasPendingCard: pendingSessionKeys.contains(row.key))
            let harness = HarnessInfo(session: row)
            let mName = machineName(row.machineRef) ?? row.machineID
            let chip = originChip(childMachineID: row.machineID, childMachineTitle: mName,
                                  childHarness: harness.displayName,
                                  parentMachineID: parentKey.machineID, parentHarness: parentHarness.displayName)
            let rawTitle = row.displayTitle.trimmingCharacters(in: .whitespacesAndNewlines)
            let title = rawTitle.isEmpty ? (embeddedMatch?.title ?? String(localized: "Subagent [Session Overview]")) : rawTitle
            let toolSummary: String? = status.isRunningGroup
                ? firstNonEmpty(row.activity, embeddedMatch?.lastTool?.summary, embeddedMatch?.lastTool?.name, row.waitLine)
                : nil
            let directParent = ChildSessions.directParentKey(row)
            let immediateParentID: String? = directParent.flatMap { p in
                (p != parentKey && p != row.key) ? "session:\(p.path)" : nil
            }
            let item = Item(
                id: "session:\(row.key.path)",
                target: .session(row.key),
                title: title,
                harness: harness,
                machineID: row.machineID,
                machineName: mName,
                originChip: chip,
                status: status,
                startedAt: row.startedAt ?? embeddedMatch?.startedAt,
                updatedAt: row.updatedAt ?? embeddedMatch?.updatedAt,
                toolSummary: toolSummary,
                depth: 0,
                canSend: row.canSend,
                canInterrupt: row.canInterrupt && row.status.isActive,
                toolUseID: embeddedMatch?.toolUseID,
                childCardSessionID: row.sessionID
            )
            nodes.append(Node(item: item, parentID: immediateParentID))
        }

        // 2. Embedded `session.subagents` (`parent@agent`) not already represented by a standalone child row.
        for agent in embedded {
            let key = SessionKey(group: parentKey.group, machineID: parentKey.machineID, sessionID: agent.sessionID)
            guard seenSessionKeys.insert(key).inserted else { continue }
            let status = embeddedStatus(agent, hasPendingCard: pendingSessionKeys.contains(key), agentStatuses: agentStatuses)
            let toolSummary: String? = status.isRunningGroup
                ? firstNonEmpty(agent.lastTool?.summary, agent.lastTool?.name)
                : nil
            let immediateParentID = agent.parentID.flatMap { pid -> String? in
                guard !pid.isEmpty, pid != agent.id else { return nil }
                return "embedded:\(parentKey.path):\(pid)"
            }
            let item = Item(
                id: "embedded:\(parentKey.path):\(agent.id)",
                target: .embedded(parent: parentKey, agent: agent),
                title: agent.title,
                harness: parentHarness,
                machineID: parentKey.machineID,
                machineName: parentMachineTitle,
                originChip: nil,
                status: status,
                startedAt: agent.startedAt,
                updatedAt: agent.updatedAt,
                toolSummary: toolSummary,
                depth: 0,
                canSend: false,
                canInterrupt: false,
                toolUseID: agent.toolUseID,
                childCardSessionID: nil
            )
            nodes.append(Node(item: item, parentID: immediateParentID))
        }

        let running = layoutGroup(nodes.filter { $0.item.status.isRunningGroup }, running: true)
        let ended = layoutGroup(nodes.filter { !$0.item.status.isRunningGroup }, running: false)
        let previewHarnesses = previewMarks(from: ended)
        return SubagentOverview(running: running, ended: ended, endedPreviewHarnesses: previewHarnesses)
    }

    private static func sessionStatus(_ row: Session, hasPendingCard: Bool) -> Status {
        if row.state == .needsYou || hasPendingCard { return .needsYou }
        if row.state == .working || row.backgroundWorking { return .running }
        if let err = row.turnError, !err.isEmpty { return .failed }
        if let fail = row.failureReason, !fail.isEmpty { return .failed }
        return .ended
    }

    private static func embeddedStatus(_ agent: Subagent, hasPendingCard: Bool, agentStatuses: [String: String]) -> Status {
        if hasPendingCard { return .needsYou }
        if agent.running { return .running }
        if let callID = agent.toolUseID, let raw = agentStatuses[callID]?.lowercased(),
           raw == "failed" || raw == "error" {
            return .failed
        }
        return .ended
    }

    /// Tree-preserving sort within a group: keeps children directly beneath their in-group parent, caps indentation at
    /// `maxNestingDepth` (flattening deeper levels), pins Needs you first in Running, and orders siblings newest-first.
    private static func layoutGroup(_ nodes: [Node], running: Bool) -> [Item] {
        guard !nodes.isEmpty else { return [] }
        let listed = Set(nodes.map(\.item.id))
        var byID: [String: Node] = [:]
        for node in nodes { byID[node.item.id] = node }

        // Resolve nearest in-group ancestor so cycles break cleanly and children whose parent is in the other group promote to root.
        var childrenOf: [String: [Node]] = [:]
        var roots: [Node] = []
        for node in nodes {
            if let pid = node.parentID, pid != node.item.id, listed.contains(pid), !createsCycle(child: node.item.id, parent: pid, in: byID) {
                childrenOf[pid, default: []].append(node)
            } else {
                roots.append(node)
            }
        }

        // In Running, a root whose subtree contains a Needs-you item also rises above plain working roots.
        var subtreeNeedsYou: [String: Bool] = [:]
        if running {
            func hasNeedsYou(_ id: String, visited: inout Set<String>) -> Bool {
                if let cached = subtreeNeedsYou[id] { return cached }
                guard visited.insert(id).inserted else { return false }
                let own = byID[id]?.item.status == .needsYou
                let kids = (childrenOf[id] ?? []).contains { hasNeedsYou($0.item.id, visited: &visited) }
                let value = own || kids
                subtreeNeedsYou[id] = value
                return value
            }
            for node in nodes {
                var visited: Set<String> = []
                _ = hasNeedsYou(node.item.id, visited: &visited)
            }
        }

        func sortSiblings(_ list: [Node]) -> [Node] {
            list.sorted { a, b in
                if running {
                    let aNeeds = subtreeNeedsYou[a.item.id] == true
                    let bNeeds = subtreeNeedsYou[b.item.id] == true
                    if aNeeds != bNeeds { return aNeeds }
                    if (a.item.status == .needsYou) != (b.item.status == .needsYou) {
                        return a.item.status == .needsYou
                    }
                    let aTime = a.item.startedAt ?? a.item.updatedAt ?? .distantPast
                    let bTime = b.item.startedAt ?? b.item.updatedAt ?? .distantPast
                    if aTime != bTime { return aTime > bTime }
                } else {
                    let aTime = a.item.updatedAt ?? a.item.startedAt ?? .distantPast
                    let bTime = b.item.updatedAt ?? b.item.startedAt ?? .distantPast
                    if aTime != bTime { return aTime > bTime }
                }
                return a.item.id < b.item.id
            }
        }

        var out: [Item] = []
        var placed: Set<String> = []
        func visit(_ node: Node, depth: Int) {
            guard placed.insert(node.item.id).inserted else { return }
            var copy = node.item
            copy.depth = min(depth, maxNestingDepth)
            out.append(copy)
            for child in sortSiblings(childrenOf[node.item.id] ?? []) {
                visit(child, depth: depth + 1)
            }
        }
        for root in sortSiblings(roots) { visit(root, depth: 0) }
        for node in sortSiblings(nodes) where !placed.contains(node.item.id) { visit(node, depth: 0) }
        return out
    }

    private static func createsCycle(child: String, parent: String, in byID: [String: Node]) -> Bool {
        var cursor: String? = parent
        var seen: Set<String> = [child]
        while let current = cursor {
            if !seen.insert(current).inserted { return true }
            cursor = byID[current]?.parentID
        }
        return false
    }

    /// Up to 4 harness brand marks for the collapsed Ended row, preferring distinct harnesses first, then filling
    /// from the newest ended items so a homogeneous group of 3 ended Claude subagents shows 3 stacked marks (max 4).
    private static func previewMarks(from ended: [Item]) -> [HarnessInfo] {
        guard !ended.isEmpty else { return [] }
        var distinct: [HarnessInfo] = []
        var seen: Set<String> = []
        var rest: [HarnessInfo] = []
        for item in ended {
            if seen.insert(item.harness.brandIdentity).inserted {
                distinct.append(item.harness)
            } else {
                rest.append(item.harness)
            }
        }
        return Array((distinct + rest).prefix(maxEndedPreviewMarks))
    }

    private static func firstNonEmpty(_ candidates: String?...) -> String? {
        for candidate in candidates {
            if let trimmed = candidate?.trimmingCharacters(in: .whitespacesAndNewlines), !trimmed.isEmpty {
                return trimmed
            }
        }
        return nil
    }
}
