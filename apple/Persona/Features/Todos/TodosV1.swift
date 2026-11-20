import SwiftUI

extension TodosV1 {
    /// Whether this build may show V1 on this device: once released (#10465's switch-on), on the iPhone and iPad; before
    /// that, in a DEBUG build launched with `-showTodosV1 YES`, for screenshots and tests. Either way only where the hub
    /// lists `todo_view`: the switch stands for the release, not for the hub.
    nonisolated static var allowedHere: Bool { (released || shownByLaunch) && platform }

    #if DEBUG
    nonisolated static var shownByLaunch: Bool { UserDefaults.standard.bool(forKey: "showTodosV1") }
    #else
    nonisolated static let shownByLaunch = false
    #endif
}

extension TodoStore {
    /// The To-dos row's number in the drawer and the sidebar, and the folded Pages heading's: N on the V1 path, the to-dos
    /// that need the owner, as the list's bar and Needs you header say it (`countsSource`, DESIGN_SYSTEM 4.8); today's
    /// open count otherwise (`badgeCount`), also while a To-dos page made before the gate came on still draws today's
    /// list (`TodoWorkspace.holdsToday`).
    var sidebarCount: Int { TodosV1.shared.isOn && !TodoWorkspace.shared.holdsToday ? countsSource.needsYou : badgeCount }
}

#if os(iOS)
/// The To-dos place's page: the V1 list while the gate is on, else today's list (`TodosView`, frozen). Which one is
/// decided once, when the page is made: a hello that turns V1 on while the page is up (a hub whose manager connects
/// late), or while a to-do is pushed over it, never swaps it; the next time To-dos opens it is the V1 list
/// (docs/apple/ios.md "To-dos (V1, #10465)").
struct TodosPlacePage: View {
    /// A stored value's read (P3), kept for the page's life.
    @State private var choice = TodosPageChoice()

    var body: some View {
        Group {
            if choice.v1 { TodoListView() } else { TodosView() }
        }
        .onAppear { choice.shown() }
        #if DEBUG || PERF_SIGNPOST
        // Which list the page draws, and whether the gate is on meanwhile (`TodosV1ListTests`, `-probes YES`).
        .screenProbe("todos-page", TodosPageProbe(v1: choice.v1, gate: ScreenProbe.enabled && TodosV1.shared.isOn))
        #endif
    }
}

/// Which list a To-dos page draws, decided as the page is made. While it draws today's list the workspace knows it, until
/// the page is gone (`TodoWorkspace.todayPages`).
@MainActor final class TodosPageChoice {
    let v1 = TodosV1.shared.isOn
    /// What the workspace was told, once the page appeared: SwiftUI makes and drops other choices while it builds the
    /// page, and those tell nothing. Read again only as this one goes, when nothing else holds it.
    nonisolated(unsafe) private var told: UUID?

    func shown() {
        guard !v1, told == nil else { return }
        let id = UUID()
        told = id
        TodoWorkspace.shared.todayPage(id, open: true)
    }

    deinit {
        guard let id = told else { return }
        Task { @MainActor in TodoWorkspace.shared.todayPage(id, open: false) }
    }
}

#if DEBUG || PERF_SIGNPOST
struct TodosPageProbe: Encodable, Equatable {
    var v1: Bool
    var gate: Bool
}
#endif
#endif
