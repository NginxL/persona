import SwiftUI

/// What the page beside the Sidebar layout's column shows at its root (#8359, docs/design/sidebar-navigation.md option 3):
/// the main conversation, or one thing the sidebar opened (a place, `ShellRoute`; Health or Messages, `SidebarRoute`; an
/// Agent's chat, `SessionKey`; or another page the Agents list opens: a space, Personal Agents, a machine…). Each is a
/// root of its own, as a system sidebar's items are: choosing one replaces the page, it is not pushed over the conversation.
enum SidebarPageRoot: Hashable {
    case conversation
    /// The value the page draws at its root, the same value its destination would be pushed by.
    case page(AnyHashable)

    /// The value drawn at the root; nil for the conversation.
    var value: AnyHashable? {
        if case .page(let v) = self { return v }
        return nil
    }

    /// The place the sidebar shades for this root (a page it lists), nil for the conversation or something the Agents
    /// list opened.
    var place: ShellPage? {
        switch value?.base {
        case let route as ShellRoute:
            switch route {
            case .inbox: .inbox
            case .todos: .todos
            case .machines: .machines
            case .profile: .profile
            case .routines: .routines
            case .knowledge: .knowledge
            #if os(iOS)
            case .starred: .starred
            #endif
            case .recordings: .recordings
            case .extensionPage(let r): .extensionPage(r)
            }
        case let route as SidebarRoute:
            switch route {
            case .health: .health
            case .messages: .messages
            }
        default: nil
        }
    }
}

/// The roots of the Sidebar layout's page and the drill-down path each was left with (#8359). Pure: the page asks it what a
/// pick means and applies the answer (`Step`).
///
/// - **Switching root** (a different item in the sidebar, or a route to one): the page's root is replaced at once, with no
///   push or pop, as a system sidebar replaces its detail. The root left keeps its drill-down path; coming back to it
///   brings that path back (HIG, Tab bars: preserve each section's navigation state).
/// - **The root already in front** picked again: back to its root (the tab-bar rule), a pop the user can see if anything
///   was pushed. The main conversation picked again with another conversation in it is a switch: that conversation
///   starts at its root, with no pop of the one before.
/// - **Drilling down** (something opened inside the page: an Inbox item, a recording, a machine's details): a push with
///   Back, as before. It is the path's, not a root.
struct SidebarPageRoots {
    /// How the page moves for a step.
    enum Move: Equatable {
        /// Nothing changes (the root in front picked again, nothing pushed on it).
        case none
        /// The root in front picked again with pages pushed on it: back to it.
        case popToRoot
        /// Another root: the page is replaced.
        case switchRoot
        /// A page pushed on the root in front.
        case drillDown
    }

    /// What the page does for a pick: the root to show, the path over it, how it moves, and whether that move is animated.
    struct Step: Equatable {
        var root: SidebarPageRoot
        var path: NavigationPath
        var move: Move
        /// Only a drill-down's push and a re-pick's pop animate; a switch of root never does (the open Duo upright: the
        /// sidebar sliding away is the only motion).
        var animated: Bool { move == .popToRoot || move == .drillDown }
        /// A pick in the sidebar (any but a drill-down): a sidebar laid over the page slides away (#4852).
        var closesOverlay: Bool { move != .drillDown }
    }

    /// The main conversation in front, nothing saved: where every sidebar starts (`UnifiedSidebar.roots`, P3).
    static let start = SidebarPageRoots()

    /// The root in front.
    private(set) var current: SidebarPageRoot = .conversation
    private var saved: [SidebarPageRoot: NavigationPath] = [:]

    /// `root` is picked while the page shows `path` over the current root. `contentChanged`: the root is the one in front
    /// but shows something else now (another conversation picked while the conversation is in front).
    mutating func select(_ root: SidebarPageRoot, path: NavigationPath, contentChanged: Bool = false) -> Step {
        if root == current {
            saved[root] = nil
            if contentChanged { return Step(root: root, path: NavigationPath(), move: .switchRoot) }
            return Step(root: root, path: NavigationPath(), move: path.isEmpty ? .none : .popToRoot)
        }
        saved[current] = path.isEmpty ? nil : path
        current = root
        return Step(root: root, path: saved.removeValue(forKey: root) ?? NavigationPath(), move: .switchRoot)
    }

    /// `value` opened from inside the page in front, over `path`: a push on its root.
    func drill(_ value: some Hashable, over path: NavigationPath) -> Step {
        var next = path
        next.append(value)
        return Step(root: current, path: next, move: .drillDown)
    }

    /// Another assistant or another hub: what every root showed was the one before's. Back to the conversation, nothing
    /// kept.
    mutating func forget() -> Step {
        current = .conversation
        saved = [:]
        return Step(root: .conversation, path: NavigationPath(), move: .switchRoot)
    }

    /// Whether a drill-down path is kept for `root` (tests).
    func keeps(_ root: SidebarPageRoot) -> Bool { saved[root] != nil }

    /// Nothing for `forget` to do: the conversation in front, nothing kept.
    var forgotten: Bool { current == .conversation && saved.isEmpty }
}

extension SidebarPageRoot {
    /// The root a sidebar page opens as (#8359); nil for New Chat and Health, which open the conversation or a page of their
    /// own (`AssistantView.showRoot`).
    static func of(_ page: ShellPage) -> SidebarPageRoot? {
        switch page {
        case .conversation: .conversation
        case .inbox: .page(ShellRoute.inbox)
        case .todos: .page(ShellRoute.todos)
        case .machines: .page(ShellRoute.machines)
        case .profile: .page(ShellRoute.profile)
        case .routines: .page(ShellRoute.routines)
        case .knowledge: .page(ShellRoute.knowledge)
        #if os(iOS)
        case .starred: .page(ShellRoute.starred)
        #endif
        case .recordings: .page(ShellRoute.recordings)
        case .extensionPage(let r): .page(ShellRoute.extensionPage(r))
        case .messages: .page(SidebarRoute.messages)
        case .newConversation, .health: nil
        }
    }
}

/// What the Agents list opens, drawn as the Sidebar layout's root (#8359): the pages its values are pushed as elsewhere
/// (`AgentsDestinations`, whose list of values this follows).
struct SidebarAgentsRootPage: View {
    let value: AnyHashable

    var body: some View {
        switch value.base {
        case let key as SessionKey: ChatView(key: key).id(key)
        case let r as SpaceRoute: SpaceView(spaceKey: r.id)
        case let r as MachineRoute: MachineSpacesView(ref: r.ref)
        case is MachineListRoute: MachinesView()
        case let r as TerminalsRoute: TerminalsView(only: r.machine)
        case let r as MachineDetailRoute: MachineDetailView(ref: r.ref)
        case is PersonalAgentsRoute: PersonalAgentListView()
        case let s as PersonalAgentSession: PersonalAgentSessionView(session: s).id(s.id)
        case let r as TodoDetailRoute: TodoDetailView(todoID: r.id)
        default: EmptyView()
        }
    }
}

extension View {
    /// A root of the Sidebar layout's page (#8359): the first page, whatever is pushed on it (its drawer button on a
    /// phone, no Back), as a page the list opens is drawn (`splitDetailPage`).
    func sidebarRootPage(ownsBack: Bool) -> some View {
        splitDetailPage(ownsBack: ownsBack)
            .transformEnvironment(\.splitDetail) { $0?.isFirstPage = true }
    }
}

extension EnvironmentValues {
    /// The inbox's accounts `ShellDestinations` shares with its pages, for the Inbox drawn as the page's root (#8359).
    @Entry var shellInboxAccounts: InboxAccountState? = nil
}
