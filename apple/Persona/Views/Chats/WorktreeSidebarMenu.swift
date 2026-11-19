import SwiftUI
#if os(macOS)
import AppKit
#endif

/// Every hit region of a worktree shares these commands and the exact host-owned creation target.
struct WorktreeSidebarMenu: View, Equatable {
    nonisolated let workspace: HostWorkspace
    nonisolated let parent: HostWorkspace?
    nonisolated let sessions: [Session]
    nonisolated let agent: Session?
    #if os(iOS)
    /// Each Agent's menu reads its own facts (`AgentActionMenuReader`): built here, they made the list observe the
    /// machines and links of every Agent in it (#10231).
    nonisolated let handlers: AgentMenuHandlers
    #else
    nonisolated let menus: [AgentRowMenu]
    #endif
    var open: (HostWorkspace) -> Void
    var newSession: (HostWorkspace) -> Void
    var newChild: (HostWorkspace) -> Void
    var rename: (HostWorkspace) -> Void = { _ in }

    /// Transcript pushes and workspace polling must not replace an open native menu.
    nonisolated static func == (a: Self, b: Self) -> Bool {
        a.workspace.key == b.workspace.key && a.workspace.title == b.workspace.title && a.workspace.cwd == b.workspace.cwd
            && a.workspace.online == b.workspace.online && a.workspace.available == b.workspace.available
            && a.parent?.key == b.parent?.key && a.agent?.key == b.agent?.key && sameMenus(a, b)
            && a.sessions.map { [$0.key.path, $0.title] } == b.sessions.map { [$0.key.path, $0.title] }
    }

    #if os(iOS)
    nonisolated private static func sameMenus(_ a: Self, _ b: Self) -> Bool { a.handlers === b.handlers }
    #else
    nonisolated private static func sameMenus(_ a: Self, _ b: Self) -> Bool { a.menus == b.menus }
    #endif

    var body: some View {
        Section {
            Button { open(workspace) } label: {
                Label(String(localized: "worktree.open", defaultValue: "Open Worktree"), systemImage: Theme.Icon.openSpace)
            }
            .accessibilityIdentifier("worktree.open")
            Button { newSession(workspace) } label: {
                Label("New Session [Chats View 58e21b]", systemImage: Theme.Icon.compose)
            }
            .disabled(!workspace.online || !workspace.available || workspace.cwd.isEmpty)
            .accessibilityIdentifier("workspace.newSession")
            Button { newChild(workspace) } label: {
                Label(String(localized: "workspace.newChild", defaultValue: "New Child Worktree…"), systemImage: Theme.Icon.newWorktree)
            }
            .disabled(!workspace.online || !workspace.available)
            .accessibilityIdentifier("workspace.newChild")
            Button { rename(workspace) } label: {
                Label(String(localized: "worktree.rename", defaultValue: "Rename Worktree…"), systemImage: Theme.Icon.edit)
            }
            .disabled(!workspace.online || !workspace.available)
            .accessibilityIdentifier("worktree.rename")
        }
        Section {
            Button { UIPasteboard.general.string = workspace.cwd } label: {
                Label("Copy Path", systemImage: Theme.Icon.copy)
            }
            .disabled(workspace.cwd.isEmpty)
            .accessibilityIdentifier("worktree.copyPath")
            Button { UIPasteboard.general.string = workspace.title } label: {
                Label(String(localized: "worktree.copyName", defaultValue: "Copy Worktree Name"), systemImage: Theme.Icon.copy)
            }
            .accessibilityIdentifier("worktree.copyName")
            if let parent {
                Button { open(parent) } label: {
                    Label(String(localized: "worktree.openParent", defaultValue: "Open Parent Worktree"), systemImage: Theme.Icon.parentFolder)
                }
                .accessibilityIdentifier("worktree.openParent")
            }
        }
        if let agent {
            Section { agentMenu(agent) }
        } else if !sessions.isEmpty {
            Section {
                ForEach(sessions) { session in
                    Menu { agentMenu(session) } label: {
                        Label { Text(session.title).lineLimit(1) } icon: {
                            #if os(macOS)
                            menuIcon(session)
                            #else
                            AgentMark(session)
                            #endif
                        }
                    }
                }
            }
        }
    }

    @ViewBuilder private func agentMenu(_ session: Session) -> some View {
        #if os(iOS)
        AgentActionMenuReader(session: session, handlers: handlers)
        #else
        if let menu = menus.first(where: { $0.key == session.key }) { menu.equatable() }
        #endif
    }

    #if os(macOS)
    /// NSHostingMenu extracts an Image's native size, ignoring AgentMark's SwiftUI frame.
    private func menuIcon(_ session: Session) -> Image {
        let kind = AgentKind(session)
        guard let brand = kind.brand, let source = NSImage(named: brand) else {
            return Theme.Icon.image(Theme.Icon.commandLine)
        }
        let size = NSSize(width: Theme.Size.markS, height: Theme.Size.markS)
        let image = NSImage(size: size, flipped: false) { rect in
            source.draw(in: rect)
            return true
        }
        image.isTemplate = brand != Theme.Brand.claude
        return Image(nsImage: image)
    }
    #endif
}
