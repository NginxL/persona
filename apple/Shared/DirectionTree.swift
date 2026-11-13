import Foundation

// The hub's direction tree (#4352, docs/assistant/unified-entry.md, docs/protocol/protocol.md): each current leader
// conversation (a direction, shown to the owner as 「常驻」 / Pinned) with the open to-dos it supervises, each to-do with
// its one state and its Agents, each Agent with its sub-Agents; the to-dos nobody (or the main conversation) supervises
// in the secretary group, last. The sidebar, the To-dos page and the Agents page all draw this one tree: the app never
// groups to-dos by supervisor or works out their state itself. Decoded leniently: a missing field takes its empty value.

/// `GET /api/v1/assistant/directions/tree`.
nonisolated struct DirectionTree: Codable, Hashable, Sendable {
    var directions: [Direction]

    init(directions: [Direction] = []) { self.directions = directions }

    init(from decoder: Decoder) throws {
        let c = try decoder.container(keyedBy: CodingKeys.self)
        directions = try c.decodeIfPresent([Direction].self, forKey: .directions) ?? []
    }

    private enum CodingKeys: String, CodingKey { case directions }

    /// How many of a direction's listed to-dos are in each state. A direction's row shows `needsYou` (what runs under it
    /// is `Direction.running`, its activity mark, #6626).
    nonisolated struct Counts: Codable, Hashable, Sendable {
        var working: Int
        var needsYou: Int
        var stuck: Int
        var review: Int
        /// Open to-dos no Agent has ever worked on (#5860); 0 from an older hub, which omits the field.
        var planned: Int

        init(working: Int = 0, needsYou: Int = 0, stuck: Int = 0, review: Int = 0, planned: Int = 0) {
            self.working = working
            self.needsYou = needsYou
            self.stuck = stuck
            self.review = review
            self.planned = planned
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            working = try c.decodeIfPresent(Int.self, forKey: .working) ?? 0
            needsYou = try c.decodeIfPresent(Int.self, forKey: .needsYou) ?? 0
            stuck = try c.decodeIfPresent(Int.self, forKey: .stuck) ?? 0
            review = try c.decodeIfPresent(Int.self, forKey: .review) ?? 0
            planned = try c.decodeIfPresent(Int.self, forKey: .planned) ?? 0
        }

        private enum CodingKeys: String, CodingKey {
            case working, stuck, review, planned
            case needsYou = "needs_you"
        }
    }

    /// A to-do's one state, as the hub decides it (precedence needs_you, review, planned, stuck, working). A state this
    /// app does not know reads as working: the to-do is open either way.
    nonisolated enum TaskState: String, Codable, Hashable, Sendable, CaseIterable {
        case needsYou = "needs_you", review, stuck, working, planned

        init(from decoder: Decoder) throws {
            self = TaskState(rawValue: try decoder.singleValueContainer().decode(String.self)) ?? .working
        }

        /// Needs you / to verify / stuck / working / not started (`StateName`).
        var text: String {
            switch self {
            case .needsYou: StateName.taskNeedsYou
            case .review: StateName.taskToVerify
            case .stuck: StateName.taskStuck
            case .working: StateName.taskWorking
            case .planned: StateName.taskNotStarted
            }
        }
    }

    /// An Agent working on a to-do, or one of its sub-Agents (`children`, same shape).
    nonisolated struct AgentNode: Codable, Hashable, Sendable, Identifiable {
        var machineID: String
        /// A sub-Agent running inside a session is `<parent>@<id>`.
        var sessionID: String
        var title: String
        /// claude, codex…; may be empty.
        var agent: String
        /// The session's state as session rows carry it (`AgentState`'s raw value); "" when the hub did not find it.
        var state: String
        var children: [AgentNode]

        var id: String { machineID + "/" + sessionID }
        /// The state from the tree, for a node whose session the app does not list (a sub-Agent).
        var agentState: AgentState { AgentState(rawValue: state) ?? .unknown }

        init(machineID: String, sessionID: String, title: String = "", agent: String = "", state: String = "",
             children: [AgentNode] = []) {
            self.machineID = machineID
            self.sessionID = sessionID
            self.title = title
            self.agent = agent
            self.state = state
            self.children = children
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            machineID = try c.decodeIfPresent(String.self, forKey: .machineID) ?? ""
            sessionID = try c.decodeIfPresent(String.self, forKey: .sessionID) ?? ""
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            agent = try c.decodeIfPresent(String.self, forKey: .agent) ?? ""
            state = try c.decodeIfPresent(String.self, forKey: .state) ?? ""
            children = try c.decodeIfPresent([AgentNode].self, forKey: .children) ?? []
        }

        private enum CodingKeys: String, CodingKey {
            case title, agent, state, children
            case machineID = "machine_id", sessionID = "session_id"
        }

        /// The session in `group` (the hub the tree came from).
        func key(group: String) -> SessionKey { SessionKey(group: group, machineID: machineID, sessionID: sessionID) }
    }

    /// An open to-do of a direction.
    nonisolated struct TaskNode: Codable, Hashable, Sendable, Identifiable {
        var id: String
        var title: String
        var state: TaskState
        var agents: [AgentNode]

        init(id: String, title: String = "", state: TaskState = .working, agents: [AgentNode] = []) {
            self.id = id
            self.title = title
            self.state = state
            self.agents = agents
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
            title = try c.decodeIfPresent(String.self, forKey: .title) ?? ""
            state = try c.decodeIfPresent(TaskState.self, forKey: .state) ?? .working
            agents = try c.decodeIfPresent([AgentNode].self, forKey: .agents) ?? []
        }

        private enum CodingKeys: String, CodingKey { case id, title, state, agents }

        /// Its title, or Untitled To-do for one without.
        var displayTitle: String { title.isEmpty ? String(localized: "Untitled To-do") : title }
    }

    /// A direction: a current leader conversation, or (`secretary`) the group of to-dos no leader supervises, whose id is
    /// the main conversation's.
    nonisolated struct Direction: Codable, Hashable, Sendable, Identifiable {
        /// The leader conversation's session id; the main conversation's for the secretary group.
        var id: String
        /// From the same names as `GET /api/v1/assistant/leaders`; "" for the secretary group.
        var name: String
        var secretary: Bool
        var counts: Counts
        /// Something under it is at work right now: an Agent of one of its to-dos, or one under it, whose session is
        /// working, whatever the to-do's state (the hub's one rule, #6626). Every direction row's activity mark spins on
        /// it; the working count is not shown. From a hub without it: whether it counts a working to-do, as before.
        var running: Bool
        var tasks: [TaskNode]

        init(id: String, name: String = "", secretary: Bool = false, counts: Counts = Counts(), running: Bool = false,
             tasks: [TaskNode] = []) {
            self.id = id
            self.name = name
            self.secretary = secretary
            self.counts = counts
            self.running = running
            self.tasks = tasks
        }

        init(from decoder: Decoder) throws {
            let c = try decoder.container(keyedBy: CodingKeys.self)
            id = try c.decodeIfPresent(String.self, forKey: .id) ?? ""
            name = try c.decodeIfPresent(String.self, forKey: .name) ?? ""
            secretary = try c.decodeIfPresent(Bool.self, forKey: .secretary) ?? false
            counts = try c.decodeIfPresent(Counts.self, forKey: .counts) ?? Counts()
            running = try c.decodeIfPresent(Bool.self, forKey: .running) ?? (counts.working > 0)
            tasks = try c.decodeIfPresent([TaskNode].self, forKey: .tasks) ?? []
        }

        private enum CodingKeys: String, CodingKey { case id, name, secretary, counts, running, tasks }

        /// What a header calls it: its name, Secretary for the secretary group (`fallback` for a leader the hub gave no
        /// name).
        func title(fallback: String? = nil) -> String {
            if secretary { return String(localized: "Secretary") }
            return name.isEmpty ? (fallback ?? "") : name
        }
    }
}

nonisolated extension DirectionTree {
    /// The leader direction for a leader conversation's session id (never the secretary group).
    func leader(_ id: String) -> Direction? {
        leaders.first { $0.id == id }
    }

    /// The current directions in the tree's order, the secretary group left out: the sidebar's Pinned rows (the
    /// secretary's to-dos stay in the To-dos and Agents groupings; the main conversation's own row stands for it there).
    var leaders: [Direction] { directions.filter { !$0.secretary && !$0.id.isEmpty } }

    /// The directions' names by leader conversation id, those with one.
    var leaderNames: [String: String] {
        var out: [String: String] = [:]
        for d in leaders {
            let name = d.name.trimmingCharacters(in: .whitespacesAndNewlines)
            if !name.isEmpty { out[d.id] = name }
        }
        return out
    }

    /// The directions with a to-do, in the tree's order: the To-dos page's and the Agents page's sections.
    var withTasks: [Direction] { directions.filter { !$0.tasks.isEmpty } }
}

nonisolated extension DirectionTree {
    /// An Agent's row under a to-do: `depth` 0 for the to-do's own Agents, one more per sub-Agent level.
    nonisolated struct AgentLine: Hashable, Sendable, Identifiable {
        var node: AgentNode
        var depth: Int
        var id: String { node.id }
        var hasChildren: Bool { !node.children.isEmpty }
    }
}

nonisolated extension DirectionTree.TaskNode {
    /// The to-do's Agents top to bottom, each sub-Agent under its parent: a node's children only while its id is in
    /// `opened` (nil: every node open, as the Agents page lists them). An Agent the hub listed twice shows once.
    func agentLines(opened: Set<String>? = nil) -> [DirectionTree.AgentLine] {
        var out: [DirectionTree.AgentLine] = []
        var seen: Set<String> = []
        func walk(_ nodes: [DirectionTree.AgentNode], depth: Int) {
            for n in nodes where !seen.contains(n.id) {
                seen.insert(n.id)
                out.append(DirectionTree.AgentLine(node: n, depth: depth))
                if opened?.contains(n.id) ?? true { walk(n.children, depth: depth + 1) }
            }
        }
        walk(agents, depth: 0)
        return out
    }
}
