import SwiftUI

// The sidebar's Pinned group (the leader directions), opened up (#4352, docs/apple/sidebar.md "Pinned directions"):
// each lead's row (#4331's, with its "N working · N need you") gets a chevron that folds open its to-dos, each with
// its state; a to-do folds open to its Agents, an Agent to its sub-Agents. All of it is the hub's direction tree
// (`AppModel.directionTree`, the one the counts come from too). Kept apart from AssistantSidebar.swift, which only
// places these rows. The secretary group has no row here: the main conversation's row stands for it.
// With the hub's `direction_sessions` (#11516) the open row also lists the live sessions filed there, then All ›,
// which opens the page of everything under it (DirectionPage.swift).

/// One direction under Pinned: `head` (the leader conversation's own row, counts included) with a chevron on the
/// right, and while open the rows of its to-dos. Folded at first; this device remembers it per direction. Without a
/// tree (an older hub, another manager's page, or before it is read) only `head`, as the sidebar drew it before.
struct AssistantSidebarDirection<Head: View>: View {
    @Environment(AppModel.self) private var model
    var direction: DirectionTree.Direction?
    /// The direction's name as its row says it, for VoiceOver.
    var name: String
    var isShell: Bool
    /// The drawer the sidebar is in: closed when an Agent opens (nil as a column).
    var drawer: AssistantDrawer?
    /// Whether the head is the open conversation, and the fill the sidebar marks that with.
    var selected: Bool
    var selectedFill: Color
    /// The head row, told whether to draw its own selected and hover shade: not while the chevron sits beside it,
    /// where this view draws one shade across both.
    var head: (Bool) -> Head
    /// The leader conversation: All › opens the page over it (#11516).
    var lead: SessionKey?
    @StoredValue private var open: Bool
    @State private var openTasks: Set<String> = []
    @State private var openAgents: Set<String> = []
    @State private var shownTodo: TodoDetailRoute?

    init(direction: DirectionTree.Direction?, id: String, name: String, isShell: Bool, drawer: AssistantDrawer?,
         selected: Bool = false, selectedFill: Color = .clear, lead: SessionKey? = nil,
         @ViewBuilder head: @escaping (_ drawsShade: Bool) -> Head) {
        self.direction = direction
        self.name = name
        self.isShell = isShell
        self.drawer = drawer
        self.selected = selected
        self.selectedFill = selectedFill
        self.lead = lead
        self.head = head
        _open = StoredValue(wrappedValue: false, Self.foldKey(id))
    }

    /// Where this device keeps whether a direction is open.
    nonisolated static func foldKey(_ id: String) -> String { "assistant.direction.open." + id }

    var body: some View {
        // The one card presenter for every to-do row below: on the head, which stays while the tree replaces the
        // to-dos (one accepted or closed leaves it) or the direction folds, so an open card is not torn down under
        // the owner.
        headRow
            .sheet(item: $shownTodo) { r in
                NavigationStack {
                    TodoDetailView(todoID: r.id).sheetDone { shownTodo = nil }
                }
            }
        if let direction, open {
            // Whether the filed rows and All › are drawn, and so whether "No open to-dos" still shows, is read by
            // `DirectionFiledRows` alone (P5): the tree's `sessions` and the moves out redraw only it.
            DirectionFiledRows(direction: direction, name: name, inset: taskInset, drawer: drawer, lead: lead) {
                Text("No open to-dos")
                    .font(Theme.Font.secondary).foregroundStyle(.secondary).lineLimit(1)
                    .padding(.leading, taskInset)
                    .padding(.vertical, Theme.Sidebar.rowPadding)
                    .scaledFrame(minHeight: Theme.Sidebar.row)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } tasks: {
                ForEach(direction.tasks) { task in
                    DirectionTaskRow(task: task, inset: taskInset, unfolded: openTasks.contains(task.id),
                                     toggle: { openTasks.formSymmetricDifference([task.id]) },
                                     open: { shownTodo = TodoDetailRoute(id: task.id) })
                    if openTasks.contains(task.id) {
                        ForEach(task.agentLines(opened: openAgents)) { line in
                            DirectionAgentRow(line: line, inset: taskInset, unfolded: openAgents.contains(line.id),
                                              toggle: { openAgents.formSymmetricDifference([line.id]) }) { key in
                                drawer?.close()
                                openTodoAgentChat(key, in: model)
                            }
                        }
                    }
                }
            }
        }
    }

    /// The head, and with a tree its chevron beside it, not inside it: the head keeps its own tap (the conversation),
    /// menu, swipe and counts. The selected and hover shade then spans both, as wide as every other row's.
    @ViewBuilder private var headRow: some View {
        if let direction {
            head(false)
                .safeAreaInset(edge: .trailing, spacing: 0) { toggle(direction) }
                .background {
                    if selected {
                        RoundedRectangle(cornerRadius: Theme.Sidebar.radius, style: .continuous).fill(selectedFill)
                    }
                }
        } else {
            head(true)
        }
    }

    /// The row's right end: the chevron that folds the direction (the counts stay in the head, as #4331 drew them).
    private func toggle(_ direction: DirectionTree.Direction) -> some View {
        Button {
            withAnimation(Theme.Motion.state) { open.toggle() }
        } label: {
            ThemedIcon(open ? Theme.Icon.collapse : Theme.Icon.expand)
                .font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                .padding(.trailing, Theme.Space.s)
                .padding(.vertical, Theme.Sidebar.rowPadding)
                .scaledFrame(minWidth: Theme.Size.tapTarget, minHeight: Theme.Sidebar.row)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .layoutPriority(1)
        .accessibilityLabel([name, direction.counts.label ?? ""].filter { !$0.isEmpty }
            .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
        .accessibilityValue(open ? String(localized: "Expanded") : String(localized: "Collapsed [Assistant Sidebar e64447]"))
        .accessibilityIdentifier("sidebar-direction-toggle")
    }

    /// Set in from the direction's row as Auto Chats' sources are.
    private var taskInset: CGFloat { isShell ? Theme.Space.xl : Theme.Space.m + Theme.Sidebar.mark + Theme.Space.m }
}

/// A to-do of a direction under its sidebar row (#4352), and on the page's In Progress (#11516): its title and its
/// state; a tap opens its card. Its Agents fold out from the chevron on the right.
struct DirectionTaskRow: View {
    let task: DirectionTree.TaskNode
    /// How far its words are set in.
    var inset: CGFloat
    var unfolded: Bool
    var toggle: () -> Void
    var open: () -> Void

    var body: some View {
        let title = task.displayTitle
        return HStack(spacing: Theme.Space.xs) {
            Button(action: open) {
                HStack(spacing: Theme.Space.s) {
                    Text(title).font(Theme.Font.secondary).foregroundStyle(Color.primary).lineLimit(1)
                    Spacer(minLength: Theme.Space.xs)
                    DirectionTaskStateWord(state: task.state)
                }
                .padding(.leading, inset)
                .padding(.vertical, Theme.Sidebar.rowPadding)
                .scaledFrame(minHeight: Theme.Sidebar.row)
                .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityLabel([title, task.state.text]
                .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
            .accessibilityHint(String(localized: "View to-do details"))
            .accessibilityIdentifier("sidebar-direction-task")
            if task.agents.isEmpty {
                Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.dot).accessibilityHidden(true)
            } else {
                DirectionFoldButton(unfolded: unfolded, label: String(localized: "Agents of \(title)"),
                                    id: "sidebar-direction-task-toggle", toggle: toggle)
            }
        }
        .padding(.trailing, Theme.Space.m)
    }
}

/// An Agent of a to-do (or a sub-Agent, a level further in) under a direction's sidebar row, and on the page's In
/// Progress: its live state, its title; a tap opens its session. Its own view, so only it follows its session (P5).
struct DirectionAgentRow: View {
    @Environment(AppModel.self) private var model
    let line: DirectionTree.AgentLine
    var inset: CGFloat
    var unfolded: Bool
    var toggle: () -> Void
    var open: (SessionKey) -> Void

    var body: some View {
        let key = model.inHub(SessionKey(machineID: line.node.machineID, sessionID: line.node.sessionID))
        let session = model.session(key)
        let state = session?.state ?? line.node.agentState
        let title = !line.node.title.isEmpty ? line.node.title
            : session?.displayTitle ?? (line.node.agent.isEmpty ? String(localized: "Agent") : line.node.agent)
        return HStack(spacing: Theme.Space.xs) {
            Button {
                open(key)
            } label: {
                DirectionSessionLine(state: state, title: title, word: state.label, tone: state.tone.text)
                    .padding(.leading, inset + Theme.Space.l * CGFloat(line.depth + 1))
                    .padding(.vertical, Theme.Sidebar.rowPadding)
                    .scaledFrame(minHeight: Theme.Sidebar.row)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel([title, state.label]
                .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
            .accessibilityAddTraits(.isButton)
            .accessibilityIdentifier("sidebar-direction-agent")
            if line.hasChildren {
                DirectionFoldButton(unfolded: unfolded, label: String(localized: "Subagents of \(title)"),
                                    id: "sidebar-direction-agent-toggle", toggle: toggle)
            } else {
                Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.dot).accessibilityHidden(true)
            }
        }
        .padding(.trailing, Theme.Space.m)
    }
}

/// What a session's row under a direction says (an Agent of a to-do, a filed session): its state's dot, its title,
/// and its state in words, not the dot's color alone (HIG-040).
struct DirectionSessionLine: View {
    var state: AgentState
    var title: String
    var word: String?
    var tone: Color

    var body: some View {
        HStack(spacing: Theme.Space.s) {
            StatusDot(state: state)
            Text(title).font(Theme.Font.secondary).foregroundStyle(Color.primary).lineLimit(1)
            Spacer(minLength: Theme.Space.xs)
            if let word {
                Text(word).font(Theme.Font.caption).foregroundStyle(tone).lineLimit(1)
            }
        }
    }
}

/// A chevron that folds what is under a row, with a 44 pt target, spoken as what it folds (a to-do's Agents, an
/// Agent's subagents: not the row's bare title, which its open button already says) and expanded / collapsed.
struct DirectionFoldButton: View {
    var unfolded: Bool
    var label: String
    var id: String
    var toggle: () -> Void

    var body: some View {
        Button {
            withAnimation(Theme.Motion.state) { toggle() }
        } label: {
            ThemedIcon(unfolded ? Theme.Icon.collapse : Theme.Icon.expand)
                .font(Theme.Font.labelStrong).foregroundStyle(.tertiary)
                .scaledFrame(width: Theme.Size.tapTarget, height: Theme.Sidebar.row)
                .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .accessibilityLabel(label)
        .accessibilityValue(unfolded ? String(localized: "Expanded") : String(localized: "Collapsed [Assistant Sidebar e64447]"))
        .accessibilityIdentifier(id)
    }
}

/// What an open direction row lists under its head (#4352, #11516): its to-dos (`tasks`), then, while the hub files
/// sessions (`DirectionFilings.shows`), the live sessions filed there and All ›, which opens the page. `empty` ("No
/// open to-dos") shows only while there are neither to-dos nor filed rows. Its own view, so the tree's `sessions` and
/// the moves out are read here alone (P5), not by the row above or the sidebar.
struct DirectionFiledRows<Empty: View, Tasks: View>: View {
    @Environment(AppModel.self) private var model
    /// The phone's shell: All › first puts its Assistant page in front, which takes the ask (and is built by it).
    @Environment(\.shell) private var shell
    let direction: DirectionTree.Direction
    /// The direction's name as its row says it: All › is spoken as whose it is.
    var name: String
    var inset: CGFloat
    var drawer: AssistantDrawer?
    var lead: SessionKey?
    let empty: Empty
    let tasks: Tasks

    init(direction: DirectionTree.Direction, name: String, inset: CGFloat, drawer: AssistantDrawer?, lead: SessionKey?,
         @ViewBuilder empty: () -> Empty, @ViewBuilder tasks: () -> Tasks) {
        self.direction = direction
        self.name = name
        self.inset = inset
        self.drawer = drawer
        self.lead = lead
        self.empty = empty()
        self.tasks = tasks()
    }

    var body: some View {
        let filings = DirectionFilings.shared
        let shows = filings.shows(model)
        // The tree's filed sessions are kept apart from it (`DirectionFilings.takeFiled`): read here, per direction.
        let rows = shows ? filings.rows(filings.filedLive(direction.id), in: direction.id, group: model.hub?.group ?? "",
                                        live: true) : []
        if direction.tasks.isEmpty && rows.isEmpty { empty }
        tasks
        if shows {
            ForEach(rows) { row in
                DirectionFiledRow(row: row, inset: inset) { key in
                    drawer?.close()
                    openTodoAgentChat(key, in: model)
                }
            }
            allRow
        }
    }

    /// All ›: the page of everything under this direction, pushed over its leader conversation (#11516).
    private var allRow: some View {
        Button {
            guard let lead else { return }
            // As the drawer's other picks (`ShellView`): the Assistant page in front, built if it was not yet, so the
            // ask is taken now and not when Assistant is next shown.
            shell?.select(0, animated: false)
            DirectionPages.shared.open(direction.id, over: lead, fromSidebar: true)
        } label: {
            HStack(spacing: Theme.Space.xs) {
                Text("All").font(Theme.Font.secondary).lineLimit(1)
                ThemedIcon(Theme.Icon.forward).font(Theme.Font.caption)
                Spacer(minLength: 0)
            }
            .foregroundStyle(.secondary)
            .padding(.leading, inset)
            .padding(.vertical, Theme.Sidebar.rowPadding)
            .scaledFrame(minHeight: Theme.Sidebar.row)
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(.rect)
        }
        .buttonStyle(.plain)
        .padding(.trailing, Theme.Space.m)
        .accessibilityLabel(String(localized: "Tasks and Chats [Direction Page]"))
        .accessibilityValue(name)
        // Voice Control says what it sees: "Tap All".
        .accessibilityInputLabels([Text("All"), Text("Tasks and Chats [Direction Page]")])
        .accessibilityIdentifier("sidebar-direction-all")
    }
}

/// A live session filed under a direction, drawn as an Agent row is (its state's dot, title and state word); a tap
/// closes the drawer and opens it. While its move is out it says Moving… in place of its state.
struct DirectionFiledRow: View {
    @Environment(AppModel.self) private var model
    let row: DirectionFilings.Row
    var inset: CGFloat
    var open: (SessionKey) -> Void

    var body: some View {
        let session = model.session(row.key)
        let state = session?.state ?? row.session.ownState ?? .unknown
        let title = DirectionPageLogic.title(row.session, session: session)
        let word = row.moving ? String(localized: "Moving… [Move To]") : state.label
        HStack(spacing: Theme.Space.xs) {
            Button { open(row.key) } label: {
                DirectionSessionLine(state: state, title: title, word: word,
                                     tone: row.moving ? Color.secondary : state.tone.text)
                    .padding(.leading, inset)
                    .padding(.vertical, Theme.Sidebar.rowPadding)
                    .scaledFrame(minHeight: Theme.Sidebar.row)
                    .contentShape(.rect)
            }
            .buttonStyle(.plain)
            .accessibilityElement(children: .ignore)
            .accessibilityLabel([title, word]
                .joined(separator: String(localized: ", ", comment: "Separator between the parts of a VoiceOver label")))
            .accessibilityAddTraits(.isButton)
            .accessibilityIdentifier("sidebar-direction-filed")
            Color.clear.scaledFrame(width: Theme.Size.tapTarget, height: Theme.Size.dot).accessibilityHidden(true)
        }
        .padding(.trailing, Theme.Space.m)
    }
}
