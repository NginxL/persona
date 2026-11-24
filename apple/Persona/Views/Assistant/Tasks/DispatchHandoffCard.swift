import SwiftUI
import PersonaWire

/// One dispatch, one card per Agent (docs/design/DESIGN_SYSTEM.md §3.3; the web's `HandoffCard`): an arrow, the Agent's
/// title with how it came to the task (New, Already Active, Resumed, Continuation), when it last moved and its state, and an arrow out.
/// A plan names only its own tasks. Before an Agent is up the card stands for the task: starting, why it did not start,
/// or that it closed. Agents linked together are each a card; a later Agent is not on this hand-off. It is the task's only
/// card in the chat (#8237): a start that failed or a turn the model service cut off shows here, in place, with why.
struct DispatchHandoffCards: View {
    var tasks: [DispatchedTasks.Entry]
    private var store: TasksStore { .shared }

    private var cards: [DispatchedTasks.Entry] {
        tasks.flatMap { entry in
            DispatchedTasks.cards(of: entry, todo: entry.todoID.flatMap { store.todos[$0] })
        }
    }

    var body: some View {
        let rows = cards
        if !rows.isEmpty {
            let shared = Set(Dictionary(grouping: rows, by: { $0.todoID ?? $0.id }).filter { $0.value.count > 1 }.map(\.key))
            VStack(alignment: .leading, spacing: Theme.Space.s) {
                ForEach(rows) { DispatchHandoffCard(entry: $0, disambiguate: shared.contains($0.todoID ?? $0.id)) }
            }
        }
    }
}

/// What a hand-off card says about its task, worked out from the stores. The card reads them (which makes it re-evaluate
/// on any to-do or session change, as the stores are shared) and hands this value to `DispatchHandoffCardBody`, which is
/// `Equatable`: a change that leaves it as it was redraws nothing below (docs/apple/performance.md P5, P6).
struct DispatchHandoffCard: View {
    var entry: DispatchedTasks.Entry
    /// Two Agents of one task need different accessibility ids. One keeps `dispatch.task.<todo>`.
    var disambiguate = false
    private var store: TasksStore { .shared }

    /// What the meta line under the title says.
    enum Meta: Equatable {
        /// An Agent has the task: when it last moved (nil when nothing yet) and the task's state.
        case agent(at: Date?, state: TaskState, limit: Bool)
        /// The Agent stopped on a model service error nothing continues: when, and why in words ("" when unknown).
        case interrupted(at: Date?, reason: String)
        /// Its Agent did not start: the line that says so, in plain words (`StartFailureWords.cardLine`).
        case failed(String)
        case gone
        case closedEarly
        case starting
    }

    var body: some View {
        let todo = entry.todoID.flatMap { store.todos[$0] }
        let session = agentSession(todo)
        let meta = meta(todo: todo, session: session)
        DispatchHandoffCardBody(entry: titled(session), meta: meta, disambiguate: disambiguate).equatable()
    }

    private func titled(_ session: Session?) -> DispatchedTasks.Entry {
        var shown = entry
        shown.title = title(session: session)
        return shown
    }

    /// The session's own title, then the Agent row's, then the task's. Same order as the web, including a session
    /// whose title is the runtime's default.
    private func title(session: Session?) -> String {
        DispatchedTasks.cardTitle(sessionTitle: session?.title, agentTitle: entry.agentTitle, taskTitle: entry.title)
    }

    private func agentSession(_ todo: TodoItem?) -> Session? {
        guard let mid = entry.machineID, let sid = entry.sessionID, !mid.isEmpty, !sid.isEmpty else { return nil }
        if let todo, let found = store.sessions(todo).first(where: { $0.machineID == mid && $0.sessionID == sid }) { return found }
        let group = AppModel.shared.hub?.group ?? ""
        return AppModel.shared.sessionWithLine(SessionKey(group: group, machineID: mid, sessionID: sid))
    }

    private func meta(todo: TodoItem?, session: Session?) -> Meta {
        if entry.sessionID != nil, let todo {
            let at = (session?.activityAt ?? session?.updatedAt).flatMap { $0.isMeaningful ? $0 : nil }
            let state = session.map { TaskState(todo: todo, sessions: [$0]) } ?? store.state(todo)
            let limit = session.map { Self.limited(state: state, closed: todo.doneAt != nil, lead: $0) } ?? false
            if !limit, let session, let reason = Self.interruption(session, closed: todo.doneAt != nil) {
                return .interrupted(at: at, reason: reason)
            }
            return .agent(at: at, state: state, limit: limit)
        }
        if let todo, let line = store.startFailureCard(todo, canOpen: entry.detailTodoID != nil) { return .failed(line) }
        if let failure = failure(todo) {
            return .failed(StartFailureWords.cardLine(failure, canOpen: entry.detailTodoID != nil))
        }
        if todo?.doneAt != nil { return .closedEarly }
        if todo == nil, entry.todoID != nil, !store.todos.isEmpty { return .gone }
        return .starting
    }

    /// Why the Agent's turn was cut off, in words, while it is (the tray's `interrupted`: stopped on a turn error that is
    /// not a usage limit, the task open); "" when the error says nothing readable; nil when it was not cut off.
    nonisolated static func interruption(_ session: Session, closed: Bool) -> String? {
        let error = (session.turnError ?? "").trimmingCharacters(in: .whitespacesAndNewlines)
        guard !closed, !error.isEmpty, session.turnErrorKind != "usage_limit",
              session.state == .unread || session.state == .idle else { return nil }
        return TurnErrorReason.of(error) ?? error
    }

    /// The Agent is waiting out a usage limit: not when it needs you, not once the task is closed.
    static func limited(state: TaskState, closed: Bool, lead: Session, now: Date = .now) -> Bool {
        state != .needsYou && !closed && lead.isUsageLimited(now: now)
    }

    /// Why the task's Agent did not start: the hub's word on the to-do, its plan's, the Agent's own row, or the plan task's.
    private func failure(_ todo: TodoItem?) -> String? {
        let why = todo.flatMap { store.startFailure($0) ?? $0.agents.first(where: { !($0.startError ?? "").isEmpty })?.startError } ?? entry.error
        let trimmed = why?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? nil : trimmed
    }
}

struct DispatchHandoffCardBody: View, Equatable {
    var entry: DispatchedTasks.Entry
    var meta: DispatchHandoffCard.Meta
    var disambiguate = false
    private var store: TasksStore { .shared }

    private static let justNow = String(localized: "Just now")
    private static let relative: RelativeDateTimeFormatter = {
        let f = RelativeDateTimeFormatter()
        f.unitsStyle = .short
        return f
    }()

    static func activityTime(_ at: Date, now: Date) -> String {
        // Match the web hand-off card's recent window, including timestamps ahead of the device clock.
        if now.timeIntervalSince(at) < 45 { return justNow }
        return relative.localizedString(for: at, relativeTo: now)
    }

    var body: some View {
        let card = HStack(spacing: Theme.Space.m) {
            ThemedIcon(Theme.Icon.followOn).font(Theme.Font.bodyStrong)
                .scaledFrame(width: Theme.Size.well, height: Theme.Size.well)
                .background(Theme.Fill.well, in: .circle)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: Theme.Space.xs) {
                HStack(spacing: Theme.Space.s) {
                    Text(entry.title).font(Theme.Font.bodyStrong).lineLimit(1)
                    badge
                }
                metaLine
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            if entry.detailTodoID != nil {
                ThemedIcon(Theme.Icon.open).font(Theme.Font.labelStrong).foregroundStyle(.secondary)
                    .accessibilityHidden(true)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .card(padding: Theme.Space.m)
        if let id = entry.detailTodoID {
            let ident = (disambiguate && entry.sessionID != nil) ? "dispatch.task.\(id).\(entry.sessionID!)" : "dispatch.task.\(id)"
            Button { open(id) } label: { card.contentShape(.rect) }
                .buttonStyle(.plain)
                .accessibilityElement(children: .combine)
                .accessibilityAddTraits(.isButton)
                .accessibilityIdentifier(ident)
        } else {
            card
                .accessibilityElement(children: .combine)
                .accessibilityIdentifier("dispatch.unstarted")
        }
    }

    /// Filled for New, Resumed and Continuation, outlined for an Agent that was running already.
    @ViewBuilder private var badge: some View {
        let active = entry.wake == "active"
        Text(badgeTitle)
            .font(Theme.Font.labelHeavy)
            .textCase(.uppercase)
            .lineLimit(1)
            .fixedSize()
            .foregroundStyle(active ? AnyShapeStyle(.primary) : AnyShapeStyle(Theme.pageBackground))
            .padding(.horizontal, Theme.Space.s)
            .padding(.vertical, Theme.Space.xxs)
            .background { if !active { Capsule().fill(.primary) } }
            .overlay { if active { Capsule().strokeBorder(.primary, lineWidth: Theme.Size.hairline) } }
            .accessibilityIdentifier("dispatch.badge")
    }

    private var badgeTitle: String {
        switch entry.wake {
        case "active": String(localized: "Already Active")
        case "resumed": String(localized: "Resumed")
        case "continuation": String(localized: "Continuation [Task Handoff Badge]")
        default: String(localized: "New [Task Handoff Badge]")
        }
    }

    @ViewBuilder private var metaLine: some View {
        switch meta {
        case .agent(let at, let state, let limit):
            HStack(spacing: Theme.Space.xs) {
                if let at {
                    ThemedIcon(Theme.Icon.recent).font(Theme.Font.label).foregroundStyle(.secondary)
                    VisibleTimelineView(interval: 60) { now in
                        Text(Self.activityTime(at, now: now)).lineLimit(1)
                    }
                } else {
                    Text("No activity yet").lineLimit(1).foregroundStyle(.tertiary)
                }
                Text(verbatim: "·").foregroundStyle(.tertiary)
                if limit {
                    ThemedIcon(Theme.Icon.backgroundWaiting).foregroundStyle(Theme.caution).scaledFrame(width: Theme.Size.markM)
                        .accessibilityIdentifier("dispatch.limit-mark")
                    Text("Usage Limit").lineLimit(1).foregroundStyle(Theme.cautionText)
                } else {
                    TaskMark(state: state).scaledFrame(width: Theme.Size.markM)
                    Text(state.label).lineLimit(1).foregroundStyle(state == .needsYou ? AnyShapeStyle(state.labelColor) : AnyShapeStyle(.secondary))
                }
            }
            .font(Theme.Font.caption)
            .foregroundStyle(.secondary)
        case .interrupted(let at, let reason):
            HStack(alignment: .firstTextBaseline, spacing: Theme.Space.xs) {
                if let at {
                    ThemedIcon(Theme.Icon.recent).font(Theme.Font.label).foregroundStyle(.secondary)
                    VisibleTimelineView(interval: 60) { now in
                        Text(Self.activityTime(at, now: now)).lineLimit(1).foregroundStyle(.secondary)
                    }
                    Text(verbatim: "·").foregroundStyle(.tertiary)
                }
                ThemedIcon(Theme.Icon.infoFilled).foregroundStyle(Theme.danger).scaledFrame(width: Theme.Size.markM)
                    .accessibilityHidden(true)
                Text(Self.interruptedLine(reason)).foregroundStyle(Theme.danger).lineLimit(2)
            }
            .font(Theme.Font.caption)
            .accessibilityElement(children: .combine)
            .accessibilityIdentifier("dispatch.interrupted")
        case .failed(let line):
            // the raw error is the task's details' (TaskDetailSheet), never the card's
            Text(line).font(Theme.Font.caption).foregroundStyle(Theme.danger).fixedSize(horizontal: false, vertical: true)
                .accessibilityIdentifier("dispatch.failure")
        case .gone:
            Text("This task no longer exists").font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
        case .closedEarly:
            Text("Closed before an Agent started").font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
        case .starting:
            Text("Starting the agent").font(Theme.Font.caption).foregroundStyle(.tertiary).lineLimit(1)
        }
    }

    /// "Interrupted · <why>", or the word alone when nothing readable says why (the tray row's words, #8237).
    nonisolated static func interruptedLine(_ reason: String) -> String {
        let word = String(localized: "Interrupted [Chat Layout]")
        return reason.isEmpty ? word : word + " · " + reason
    }

    private func open(_ id: String) {
        Haptics.tap()
        let agent: SessionKey? = {
            if let mid = entry.machineID, let sid = entry.sessionID, !mid.isEmpty, !sid.isEmpty {
                return SessionKey(group: AppModel.shared.hub?.group ?? "", machineID: mid, sessionID: sid)
            }
            return store.todos[id].flatMap { store.leadSession($0)?.key }
        }()
        #if os(iOS)
        store.openDispatchedTask(id, agent: agent, pushAgent: true)
        #else
        store.openDispatchedTask(id, agent: agent, pushAgent: false)
        #endif
    }
}
