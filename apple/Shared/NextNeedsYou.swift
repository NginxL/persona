import Foundation

/// Picking the next conversation that needs the user, shared by the Mac's ⌃⌘N menu and the iPhone chat's entry shown
/// after its pending card was answered (#4348): the ones needing you, longest waiting first, muted or quiet
/// (#1731) sessions left out, wrapping around from the open one.
enum NextNeedsYou {
    static func next(after current: SessionKey?, in sessions: [Session], muted: (Session) -> Bool) -> SessionKey? {
        let order = sessions.filter { $0.hiddenByDirectory != true && $0.state == .needsYou && !muted($0) }
            .sorted { ($0.stateSince ?? .distantPast) < ($1.stateSince ?? .distantPast) }
            .map(\.key)
        guard !order.isEmpty else { return nil }
        guard let current, let i = order.firstIndex(of: current) else { return order[0] }
        return order[(i + 1) % order.count]
    }
}
