import SwiftUI

/// A page or sheet an Agent's action opens. A list row routes to its Agent's chat with this request;
/// the same selection in the chat acts on the page already open instead of pushing another chat.
nonisolated enum AgentMenuDestination: Hashable, Sendable {
    case terminal(resume: Bool)
    case pane(SessionPane)
    case review
    case preview
    case subagents
    case sideSessions
    case space
}

/// A pending route lives only in memory and is consumed by the destination chat.
@MainActor @Observable
final class AgentMenuRouting {
    static let shared = AgentMenuRouting()
    let store = AgentGitStatusStore()
    var requests: [SessionKey: AgentMenuDestination] = [:]
    var linkingSession: Session?
    func open(_ destination: AgentMenuDestination, at key: SessionKey) { requests[key] = destination }
    func take(_ key: SessionKey) -> AgentMenuDestination? { requests.removeValue(forKey: key) }
    func gitStatus(_ session: Session) -> GitStatus? {
        store.status(for: .init(session: session.key, cwd: session.cwd))
    }
}

/// What the two Agent menus show. Its equality is deliberately limited to facts that change the menu:
/// a new reply or elapsed working time does not rebuild a menu held open over the Agents list.
nonisolated struct AgentActionFacts: Equatable, Sendable {
    var key: SessionKey
    var session: Session
    var pinned: Bool
    var muted: Bool
    var panes: [SessionPane]
    var paneBadges: [SessionPane: String]
    var currentPane: SessionPane
    var review: Bool
    var preview: Bool
    var screen: Bool
    var todoHub: Bool
    var sideCount: Int
    var hosted: Bool
    var showsSpace: Bool
    var offersTakeover: Bool
    var offersContinuation: Bool
    var terminalResume: TerminalResume
    var phone: Bool
    var paneActions: Bool

    /// From the model: only `AgentActionMenuReader` may, so what the facts read is observed by the menu alone, never
    /// by a row or a header that holds it (#10231). Elsewhere this does not compile.
    @MainActor
    fileprivate init(session: Session, model: AppModel, currentPane: SessionPane = .chat, hosted: Bool = false,
                     showsSpace: Bool = true, sides: SideSessions? = nil) {
        self.init(session: session,
                  pinned: model.isPinned(session.key), muted: model.isMuted(session.key),
                  panes: AgentMenuFacts.panes(session, git: AgentMenuRouting.shared.gitStatus(session)),
                  paneBadges: AgentMenuFacts.paneBadges(AgentMenuRouting.shared.gitStatus(session)),
                  currentPane: currentPane, review: model.features(session.group).contains("git_review") && !session.cwd.isEmpty,
                  preview: !model.isDemo && model.features(session.group).contains(PreviewLogic.feature) && !session.cwd.isEmpty,
                  screen: model.canShowScreen(session.key.machineRef),
                  todoHub: TodoStore.shared.isSupported && TodosLogic.isOnTodosHub(sessionGroup: session.group, hubGroup: model.hub?.group),
                  sideCount: (sides ?? (hosted ? SideSessions() : model.sides(of: session.key))).rows.count,
                  hosted: hosted, showsSpace: showsSpace,
                  terminalResume: session.status == .offline || session.status == .unknown
                    ? session.terminalResume(on: model.machine(session.key.machineRef)) : .hidden,
                  phone: AgentMenuFacts.isPhone)
    }

    /// Test and preview construction needs no live hub. Production uses the model initializer above.
    init(session: Session, pinned: Bool = false, muted: Bool = false, panes: [SessionPane]? = nil,
         paneBadges: [SessionPane: String] = [:], currentPane: SessionPane = .chat, review: Bool = false,
         preview: Bool = false, screen: Bool = false, todoHub: Bool = false, sideCount: Int = 0,
         hosted: Bool = false, showsSpace: Bool = true, terminalResume: TerminalResume = .hidden,
         phone: Bool = false, git: GitStatus? = nil) {
        key = session.key
        self.session = session
        self.pinned = pinned
        self.muted = muted
        self.panes = panes ?? AgentMenuFacts.panes(session, git: git)
        self.paneBadges = paneBadges.isEmpty ? AgentMenuFacts.paneBadges(git) : paneBadges
        self.currentPane = currentPane
        self.review = review
        self.preview = preview
        self.screen = screen
        self.todoHub = todoHub
        self.sideCount = sideCount
        self.hosted = hosted
        self.showsSpace = showsSpace
        offersTakeover = session.isCodex && !session.canSend && session.capabilities?.takeover?.canResume == true
        offersContinuation = session.isCodex && !session.canSend && session.capabilities?.takeover != nil
        self.terminalResume = terminalResume
        self.phone = phone
        paneActions = NodeMenu.offersPaneActions(session)
    }

    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.key == b.key && a.pinned == b.pinned && a.muted == b.muted && a.panes == b.panes
            && a.paneBadges == b.paneBadges && a.currentPane == b.currentPane && a.review == b.review
            && a.preview == b.preview && a.screen == b.screen && a.todoHub == b.todoHub && a.sideCount == b.sideCount
            && a.hosted == b.hosted && a.showsSpace == b.showsSpace && a.offersTakeover == b.offersTakeover
            && a.offersContinuation == b.offersContinuation && a.terminalResume == b.terminalResume && a.phone == b.phone
            && a.session.state == b.session.state && a.session.status == b.session.status
            && a.session.capabilities == b.session.capabilities
            && a.session.state == b.session.state && a.session.status == b.session.status
            && a.session.canTerminal == b.session.canTerminal && a.session.canRename == b.session.canRename
            && a.session.canClose == b.session.canClose && a.session.canAttach == b.session.canAttach
            && a.session.canDelete == b.session.canDelete && a.session.canInterrupt == b.session.canInterrupt
            && (a.session.loop != nil) == (b.session.loop != nil)
            && a.session.todo == b.session.todo
            && a.session.subagents?.count == b.session.subagents?.count && a.session.spaceID == b.session.spaceID
            && a.session.paneID == b.session.paneID && a.paneActions == b.paneActions
    }

    /// The complete ordered inventory, including the nested node actions as separate entries.
    /// This same sequence drives rendering, not just a source-text assertion.
    nonisolated var items: [AgentActionItem] {
        var out: [AgentActionItem] = []
        func add(_ id: AgentActionItem.ID, _ group: AgentActionItem.Group = .navigation) {
            out.append(AgentActionItem(id: id, group: group))
        }
        if currentPane != .chat { add(.backToChat) }
        for pane in panes where pane == .files || pane == .git { add(pane == .files ? .files : .git) }
        if review { add(.review) }
        if preview { add(.preview) }
        if !hosted, let n = session.subagents?.count, n > 0 { add(.subagents) }
        if sideCount > 0 { add(.sideSessions) }
        if showsSpace, session.spaceID != nil { add(.space) }
        if session.canAttach { add(.attach) }
        if session.state.isLive, session.canInterrupt { add(.interrupt) }
        if session.state == .unread { add(.markSeen) }
        if phone {
            if offersTakeover { add(.takeover) }
            if offersContinuation { add(.continueInNewSession) }
        }
        if session.canRename { add(.rename) }
        add(.pin, .session)
        add(.mute, .session)
        if todoHub {
            if session.todo != nil { add(.todo, .session); add(.moveTodo, .session); add(.unlinkTodo, .session) }
            else { add(.linkTodo, .session) }
        }
        if session.loop != nil { add(.stopLoop, .session) }
        add(.handToPersona, .session)
        if screen { add(.screen, .session) }
        add(.copyDirectory, .utilities)
        if session.canTerminal {
            add(.terminal, .utilities)
            add(.copyResume, .utilities)
        }
        if terminalResume != .hidden { add(.resume, .utilities) }
        if paneActions {
            add(.splitPane, .node)
            for direction in NodeMenu.Direction.allCases { add(direction == .right ? .splitRight : .splitDown, .node) }
            add(.nodeGit, .node)
            for operation in NodeMenu.GitOp.allCases {
                add(operation == .pull ? .gitPull : operation == .push ? .gitPush : .gitFetch, .node)
            }
            add(.closeTab, .node)
        }
        if session.state.isLive, session.canClose { add(.close, .destructive) }
        if session.canDelete { add(.delete, .destructive) }
        return out
    }
}

enum AgentMenuFacts {
    @MainActor static var isPhone: Bool {
        #if os(iOS)
        UIDevice.current.userInterfaceIdiom == .phone
        #else
        false
        #endif
    }

    /// Files need a known directory. Git appears only after the detail's existing load confirms a repository:
    /// `repoRoot` alone must not make the two entry points disagree while that status is still loading.
    /// The row reads the same cached status; it never starts a repository request just to draw its menu.
    nonisolated static func panes(_ session: Session, git: GitStatus?) -> [SessionPane] {
        var result: [SessionPane] = [.chat]
        if session.canTerminal { result.append(.terminal) }
        if git?.isRepo == true { result.append(.git) }
        if !session.cwd.isEmpty { result.append(.files) }
        return result
    }

    /// The same status produces the same menu label in the row and detail. The detail's own Git pane keeps its badge.
    nonisolated static func paneBadges(_ git: GitStatus?) -> [SessionPane: String] {
        guard let count = git?.files.count, let text = CountBadge.text(count) else { return [:] }
        return [.git: text]
    }
}

nonisolated struct AgentActionItem: Equatable, Identifiable, Sendable {
    enum ID: String, Sendable {
        case backToChat, files, git, review, preview, subagents, sideSessions, space
        case attach, interrupt, markSeen, takeover, continueInNewSession, rename, pin, mute
        case todo, moveTodo, unlinkTodo, linkTodo, stopLoop, handToPersona, screen
        case copyDirectory, terminal, copyResume, resume
        case splitPane, splitRight, splitDown, nodeGit, gitPull, gitPush, gitFetch, closeTab, close, delete
    }
    enum Group: String, Sendable { case navigation, session, utilities, node, destructive }
    let id: ID
    let group: Group
}

/// A row or detail page supplies its own alerts and navigation. `update` keeps the same callback
/// object when the list redraws; menu actions read the latest session at tap time.
@MainActor
final class AgentMenuHandlers {
    var navigate: (Session, AgentMenuDestination) -> Void = { _, _ in }
    var rename: (Session) -> Void = { _ in }
    var close: (Session) -> Void = { _ in }
    var delete: (Session) -> Void = { _ in }
    var linkTodo: (Session) -> Void = { _ in }
    var openTodo: (Session) -> Void = { _ in }
    var takeover: (Session) -> Void = { _ in }
    var continueInNewSession: (Session) -> Void = { _ in }

    func update(rename: @escaping (Session) -> Void = { _ in }, close: @escaping (Session) -> Void = { _ in },
                linkTodo: @escaping (Session) -> Void = { _ in }, delete: @escaping (Session) -> Void = { _ in },
                takeover: @escaping (Session) -> Void = { _ in }, continueInNewSession: @escaping (Session) -> Void = { _ in }) {
        self.rename = rename
        self.close = close
        self.linkTodo = linkTodo
        self.delete = delete
        self.takeover = takeover
        self.continueInNewSession = continueInNewSession
    }
}

/// `AgentActionMenu` with its facts read in its own `body`. A menu's content is built with the view that holds it (a
/// row's `.contextMenu`, the chat header's `Menu`), so facts built there made that view observe what they read of the
/// model: the machines (whose heartbeat changes on every push of a busy hub), the hub links, pins and side sessions.
/// Every row of the Agents list was drawn again on each push (#10231). Read here, only this view observes them.
struct AgentActionMenuReader: View {
    @Environment(AppModel.self) private var model
    let session: Session
    var currentPane: SessionPane = .chat
    var hosted = false
    var showsSpace = true
    var sides: SideSessions?
    let handlers: AgentMenuHandlers

    var body: some View {
        AgentActionMenu(facts: AgentActionFacts(session: session, model: model, currentPane: currentPane, hosted: hosted,
                                                showsSpace: showsSpace, sides: sides),
                        handlers: handlers)
            .equatable()
    }

    #if DEBUG
    /// The facts this menu shows, for tests. Not for views: built in a body, they make that view observe the model.
    static func factsForTesting(session: Session, model: AppModel) -> AgentActionFacts {
        AgentActionFacts(session: session, model: model)
    }
    #endif
}

/// One menu definition and one renderer shared by an Agents row's long press and its detail's More button.
struct AgentActionMenu: View, Equatable {
    @Environment(AppModel.self) private var model
    nonisolated var facts: AgentActionFacts
    nonisolated var key: SessionKey { facts.key }
    nonisolated let handlers: AgentMenuHandlers

    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.facts == b.facts && a.handlers === b.handlers
    }

    var body: some View {
        Group {
            #if DEBUG || PERF_SIGNPOST
            let _ = PerfTrace.body("RowMenu.content")
            #endif
            let now = { model.latest(facts.key) }
            let items = facts.items
            // Type-erased boundaries keep the many conditional actions out of the chat page's large Debug stack frames.
            // The sequence and contents are unchanged: every group still comes from the shared `facts.items` inventory.
            AnyView(section(.navigation, in: items, now: now))
            AnyView(section(.session, in: items, now: now))
            AnyView(section(.utilities, in: items, now: now))
            AnyView(section(.node, in: items, now: now))
            AnyView(section(.destructive, in: items, now: now))
        }
    }

    @ViewBuilder private func section(_ group: AgentActionItem.Group, in items: [AgentActionItem],
                                      now: @escaping () -> Session?) -> some View {
        let own = items.filter { $0.group == group }
        if !own.isEmpty {
            Section { ForEach(own) { item in AnyView(row(item, now: now)) } }
        }
    }

    @ViewBuilder private func row(_ item: AgentActionItem, now: @escaping () -> Session?) -> some View {
        let s = facts.session
        switch item.id {
        case .backToChat:
            Button { handlers.navigate(s, .pane(.chat)) } label: { Label("Back to Chat", systemImage: SessionPane.chat.symbol) }
        case .files, .git:
            let pane: SessionPane = item.id == .files ? .files : .git
            Button { handlers.navigate(s, .pane(pane)) } label: {
                Label(facts.paneBadges[pane].map { String(localized: "\(pane.title) (\($0))") } ?? pane.title, systemImage: pane.symbol)
            }
        case .review:
            Button { handlers.navigate(s, .review) } label: { Label("Review Changes", systemImage: Theme.Icon.reviewChanges).lineLimit(1) }
        case .preview:
            Button { handlers.navigate(s, .preview) } label: { Label("Preview", systemImage: Theme.Icon.servedPage).lineLimit(1) }
                .accessibilityIdentifier("chat-preview")
        case .subagents:
            Button { handlers.navigate(s, .subagents) } label: {
                Label(String(localized: "All Subagents (\(s.subagents?.count ?? 0))"), systemImage: Theme.Icon.list).lineLimit(1)
            }
        case .sideSessions:
            Button { handlers.navigate(s, .sideSessions) } label: {
                Label(SideSessionsSheet.menuTitle(facts.sideCount), systemImage: Theme.Icon.sideSessions).lineLimit(1)
            }
            .accessibilityIdentifier("side-sessions-menu")
        case .space:
            Button { handlers.navigate(s, .space) } label: { Label("All Agents in Space", systemImage: Theme.Icon.space) }
        case .attach:
            Button { Task { await model.attach(facts.key) } } label: { Label("Attach Live", systemImage: Theme.Icon.link).lineLimit(1) }
        case .interrupt:
            Button { Task { await model.interrupt(facts.key) } } label: { Label("Interrupt [Composer Buttons f9d193]", systemImage: Theme.Icon.interrupt) }
        case .markSeen:
            Button { model.markSeen(facts.key) } label: { Label("Mark as Seen", systemImage: Theme.Icon.view) }
        case .takeover:
            Button { if let s = now() { handlers.takeover(s) } } label: { Label("Take over", systemImage: Theme.Icon.terminal) }
        case .continueInNewSession:
            Button { if let s = now() { handlers.continueInNewSession(s) } } label: {
                Label("Continue in a new session", systemImage: Theme.Icon.compose)
            }
        case .rename:
            Button { if let s = now() { handlers.rename(s) } } label: {
                Label(String(localized: "agent.rename", defaultValue: "Rename Agent…"), systemImage: Theme.Icon.edit)
            }
        case .pin:
            Button { model.togglePin(facts.key) } label: {
                Label(facts.pinned ? String(localized: "Unpin [Personal Agent List View c92179]") : String(localized: "Pin [Personal Agent List View 173f88]"),
                      systemImage: facts.pinned ? Theme.Icon.unpin : Theme.Icon.pin)
            }
        case .mute:
            Button { model.toggleMute(facts.key) } label: {
                Label(facts.muted ? String(localized: "Unmute [Chat View 3834f9]") : String(localized: "Mute"),
                      systemImage: facts.muted ? Theme.Icon.unmute : Theme.Icon.mute)
            }
        case .todo:
            Button { if let s = now() { handlers.openTodo(s) } } label: { Label(s.todo?.title ?? "", systemImage: Theme.Icon.todo) }
        case .moveTodo, .linkTodo:
            Button { if let s = now() { handlers.linkTodo(s) } } label: {
                Label(item.id == .moveTodo ? "Move to Another To-do…" : "Link to To-do…", systemImage: Theme.Icon.todo)
            }
        case .unlinkTodo:
            Button {
                guard let todo = s.todo else { return }
                Task {
                    do { try await TodoStore.shared.unlinkAgent(todoID: todo.id, machineID: s.machineID, sessionID: s.sessionID) }
                    catch { model.show(error) }
                }
            } label: { Label("Unlink", systemImage: Theme.Icon.link) }
        case .stopLoop:
            Button(role: .destructive) { model.stopLoop(facts.key) } label: { Label("Stop Loop", systemImage: Theme.Icon.stopLoop) }
        case .handToPersona:
            Button {
                guard let s = now() else { return }
                Task { await model.askPersona(AgentHandoff.prompt(s, machine: model.machine(s.machineRef)?.name)) }
            } label: { Label("Hand to Persona", systemImage: Theme.Icon.ai) }
        case .screen:
            ScreenMenu(machine: facts.key.machineRef, from: facts.key)
        case .copyDirectory:
            Button { UIPasteboard.general.string = now()?.cwd } label: { Label("Copy Directory", systemImage: Theme.Icon.folder) }
        case .terminal:
            Button { handlers.navigate(s, .terminal(resume: false)) } label: { Label("Terminal", systemImage: Theme.Icon.terminal).lineLimit(1) }
        case .copyResume:
            Button {
                guard let s = now(), s.canTerminal else { return }
                UIPasteboard.general.string = "\(s.resumeCommand) \(s.sourceThreadID ?? s.sessionID)"
            } label: { Label("Copy Resume Command", systemImage: Theme.Icon.copy).lineLimit(1) }
        case .resume:
            switch facts.terminalResume {
            case .available:
                Button { handlers.navigate(s, .terminal(resume: true)) } label: {
                    Label("Resume in Terminal", systemImage: Theme.Icon.resume).lineLimit(1)
                }
                .accessibilityIdentifier("chat-resume-terminal")
            case .noEngine:
                Button {} label: {
                    Text("Resume in Terminal")
                    Text("No terminal engine on \(model.machine(facts.key.machineRef)?.name ?? facts.key.machineID)",
                         comment: "Why Resume in Terminal is off: the computer enables no terminal engine")
                    Theme.Icon.image(Theme.Icon.resume)
                }
                .disabled(true)
                .accessibilityIdentifier("chat-resume-terminal")
            case .hidden:
                EmptyView()
            }
        case .splitPane:
            Menu {
                ForEach(NodeMenu.Direction.allCases) { direction in
                    Button { if let s = now() { NodeActions.shared.split(s, direction) } } label: {
                        Label(direction.title, systemImage: direction.symbol)
                    }
                }
            } label: { Label("Split Pane", systemImage: Theme.Icon.splitRight).lineLimit(1) }
        case .splitRight, .splitDown, .gitPull, .gitPush, .gitFetch:
            // These entries describe the nested inventory; their parent submenu renders them above/below.
            EmptyView()
        case .nodeGit:
            Menu {
                ForEach(NodeMenu.GitOp.allCases) { operation in
                    Button { if let s = now() { NodeActions.shared.git(operation, session: s) } } label: {
                        Label(operation.title, systemImage: operation.symbol)
                    }
                }
            } label: { Label("Git", systemImage: Theme.Icon.gitBranch) }
        case .closeTab:
            Button(role: .destructive) { if let s = now() { NodeActions.shared.askCloseTab(s) } } label: {
                Label("Close Tab…", systemImage: Theme.Icon.closeTab).lineLimit(1)
            }
        case .close:
            Button(role: .destructive) { if let s = now() { handlers.close(s) } } label: {
                Label("Close Agent…", systemImage: Theme.Icon.end)
            }
        case .delete:
            Button(role: .destructive) { if let s = now(), s.canDelete { handlers.delete(s) } } label: {
                Label("Delete Chat…", systemImage: Theme.Icon.delete).lineLimit(1)
            }
        }
    }
}
