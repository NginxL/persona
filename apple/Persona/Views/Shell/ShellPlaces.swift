import SwiftUI

/// The places the sidebar opens on the Assistant page (Inbox, Machines, Knowledge…) keep their own stacks
/// (docs/design/sidebar-navigation.md): leaving one for another place or a conversation keeps its path, choosing it again
/// brings it back, and choosing the place already in front returns it to its root, as a tab bar does. Before, every
/// choice went back to the conversation first and the place's pages were lost (#6599).
///
/// Only the place's path is kept: what it pushed by value. A user who goes back out of a place to the conversation left
/// it on purpose, and it starts at its root next time.
struct ShellPlaces {
    /// The place in front, pushed over the conversation; nil on the conversation (or a page that is not a place).
    private(set) var current: ShellPage?
    private var saved: [ShellPage: NavigationPath] = [:]

    /// Pages that are places: Health opens a conversation or its own page, New Chat and the Sidebar layout's way back to
    /// the conversation (#6332) are the conversation.
    static func isPlace(_ page: ShellPage) -> Bool {
        switch page {
        case .newConversation, .health, .space, .newSpace, .conversation: false
        default: true
        }
    }

    /// The place in front is left with `path`, its stack as it is now: kept for coming back.
    mutating func leave(with path: NavigationPath) {
        if let current, !path.isEmpty { saved[current] = path }
        current = nil
    }

    /// `page` is chosen while the stack is `path`. Returns the path to show: the one the place was left with, or nil
    /// for its root (first time, or chosen again while in front). `fresh`: the page shows another of its places now (a
    /// To-dos place picked in the sidebar, #10465), so what it kept belongs to the one before and it starts at its root.
    mutating func enter(_ page: ShellPage, from path: NavigationPath, fresh: Bool = false) -> NavigationPath? {
        guard Self.isPlace(page) else {
            leave(with: path)
            return nil
        }
        if current == page {
            saved[page] = nil
            return nil
        }
        leave(with: path)
        current = page
        let kept = saved.removeValue(forKey: page)
        return fresh ? nil : kept
    }

    /// Back out of the place to the conversation by the user: nothing to keep.
    mutating func backedOut() { current = nil }

    /// Another assistant (its places read the one before): every kept path goes.
    mutating func forget() {
        current = nil
        saved = [:]
    }

    /// Whether a path is kept for `page` (tests, the probe).
    func keeps(_ page: ShellPage) -> Bool { saved[page] != nil }
}
