import SwiftUI

/// iPhone Duo's tab bar on the outer display in the Separate layout (#4231): the shell's switch as a tab bar in the
/// shell's rail down the system's side column (each page's symbol over its name, the chosen one's symbol white on the
/// Theme Color's circle, its count red at the symbol's corner, in one glass capsule), centred on the status bar in the
/// column's lower middle (`ShellSideRail`).
///
/// The open Duo (regular width) shows the system's tab bar on the tabs instead: iOS puts it along the foot upright and
/// down the side column on its side, in Liquid Glass, as Apple's iPhone Duo guidance has apps do.
nonisolated enum TabSwitchLayout {
    /// The side tab bar's width: an item and the capsule's padding on both sides (64 pt), within the side column's 70 pt
    /// the status bar is centred on (`Theme.Shell.sideColumnEdge`). The items keep the default text size
    /// (`Theme.Shell.switchTypeSize`), so it is the same at every text size.
    static let sideWidth = Theme.Shell.tabBarItemWidth + Theme.Space.xs * 2

    /// The side tab bar's height with `items` items: 184 pt with Messages, 128 pt without.
    static func sideHeight(items: Int) -> CGFloat { Theme.Shell.tabBarItemHeight * CGFloat(items) + Theme.Space.s * 2 }

    /// The side column's part the status bar is centred on, and the side tab bar with it: the safe-area inset less the
    /// strip at the screen's edge (`Theme.Shell.sideColumnEdge`), never narrower than the tab bar.
    static func sideColumn(inset: CGFloat) -> CGFloat { max(inset - Theme.Shell.sideColumnEdge, sideWidth) }

    /// The room under the side tab bar's foot, down to the column's (the safe area's) foot, with rail buttons `button`
    /// pt square: the face and the page's action on the outer display, their empty room on the open Duo
    /// (`ShellRailButtonRoom`), so the bar stands as high on both (`Theme.Shell.sideRailFoot`, `sideRailSpacing`,
    /// `sideTabBarGap`). 144 pt at the default text size.
    static func sideFoot(button: CGFloat) -> CGFloat {
        Theme.Shell.sideTabBarGap + Theme.Shell.sideRailSpacing * 2 + button * 2 + Theme.Shell.sideRailFoot
    }

    /// How many items the tab bar has: Assistant and Agents, and Messages when it is on (`LarkHomePreference.shows`).
    static func items(messages: Bool) -> Int { messages ? 3 : 2 }

    /// Where the system's first item stands down the side column, from the safe area's top: lower on the outer display,
    /// whose camera is over its status bar (`Theme.Shell.systemBarTopOuter`), than on the inner one. The outer display is
    /// the narrow one.
    static func systemBarTop(windowWidth: CGFloat) -> CGFloat {
        windowWidth < Theme.Shell.outerDisplayWidest ? Theme.Shell.systemBarTopOuter : Theme.Shell.systemBarTopInner
    }

    /// How many of a page's buttons stand in the side column over the tab bar (#9837), out of `count`, besides Back (when
    /// `back`) and ⋯, which always stay: the system puts them down the column from its top, Back alone and the rest in one
    /// glass (`Theme.Shell.systemBarItem`…), and that glass stays `Theme.Space.s` clear of the tab bar, `room` below the
    /// first item's top. The leading ones that fit stay, the rest are only in ⋯ (the owner's rule, 2026-11-17); the tab
    /// bar is never moved or made smaller for them. Without a tab bar in the column (`room` nil) all of them stay.
    static func pageButtons(_ count: Int, back: Bool, room: CGFloat?) -> Int {
        guard let room else { return count }
        let item = Theme.Shell.systemBarItem, gap = Theme.Shell.systemBarItemGap
        let start = back ? item + Theme.Shell.systemBarBackGap : 0
        // A glass of n items is n items and n - 1 gaps tall; ⋯ is one of them.
        let n = Int(((room - Theme.Space.s - start + gap) / (item + gap)).rounded(.down))
        return max(0, min(count, n - 1))
    }
}

/// iPhone Duo's side column (#9837): where the tab bar stands in it, for the pages under it to keep their own buttons
/// clear of it (`TabSwitchLayout.pageButtons`). Each rail with the tab bar reports its top while it is on screen
/// (`ShellSideRail`); a scope gives one to the pages that rail stands over: the Agents page in the shell (`ChatsView`). Changes only when the tab bar moves (a fold, a text size).
@MainActor @Observable final class SideColumn {
    /// The tab bar's top from the safe area's, per rail on screen.
    @ObservationIgnored private var tops: [UUID: CGFloat] = [:]
    /// From the system's first item's top down to the tab bar's (`TabSwitchLayout.pageButtons`); nil without a tab bar.
    private(set) var room: CGFloat?

    func report(_ rail: UUID, top: CGFloat?, windowWidth: CGFloat) {
        tops[rail] = top
        let room = tops.values.min().map { $0 - TabSwitchLayout.systemBarTop(windowWidth: windowWidth) }
        if room != self.room { self.room = room }
    }
}

/// A `SideColumn` of its own for what is under it, where `own`; otherwise the one around it passes through.
struct SideColumnScope: ViewModifier {
    let own: Bool
    @State private var column = SideColumn()

    func body(content: Content) -> some View {
        content.transformEnvironment(\.sideColumn) { if own { $0 = column } }
    }
}

extension EnvironmentValues {
    /// iPhone Duo's side column under a rail with the tab bar (#9837): `SideColumnScope`.
    @Entry var sideColumn: SideColumn?
}
