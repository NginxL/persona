import SwiftUI

/// iPhone Duo's tab bar in the Separate layout (#4231): the shell's switch as a tab bar, the same items on both displays
/// (each page's symbol over its name, the chosen one's symbol white on the Theme Color's circle, its count red at the
/// symbol's corner, in one glass capsule with a hairline edge and a faint shadow), after a tab bar down the side of the
/// outer display (the owner's reference).
///
/// - Where the system puts its bars down the side (the outer display, the inner one on its side) it stands in that column,
///   centred on the status bar, in its lower middle (`ShellSideRail`): on the outer display in the shell's rail, on the
///   inner one over the tabs (`TabSwitchOverlay`), in the place of the system's tab bar. The same rail on both, at the same
///   text size and the same height above the column's foot, so folding or unfolding leaves it where it was.
/// - Where the bars run across the top (the inner display upright) there is no side column: it runs along the foot of the
///   window, centred, where iOS puts a tab bar there, and the pages keep its room at their foot, as for the system's tab
///   bar (`PhoneTabBarHidden`), so it covers nothing and the composer keeps the whole width.
///
/// While the keyboard is up it fades away, and the room at the foot goes with it.
nonisolated enum TabSwitchLayout {
    /// Where the tab bar stands over a phone's tabs.
    enum Placement: Equatable {
        /// In the system's side column on that edge (the open Duo on its side).
        case rail(HorizontalEdge)
        /// Along the window's foot (the open Duo upright).
        case bottom
    }

    /// Where the tab bar stands: on a phone's tabs (regular width, the open iPhone Duo) by where the system puts its bars;
    /// nil on the iPad, which keeps the system's tab bar along the top.
    static func placement(phone: Bool, barEdge: HorizontalEdge?) -> Placement? {
        guard phone else { return nil }
        return barEdge.map { .rail($0) } ?? .bottom
    }

    /// The side tab bar's width: an item and the capsule's padding on both sides (64 pt), within the side column's 70 pt
    /// the status bar is centred on (`Theme.Shell.sideColumnEdge`). The items keep the default text size
    /// (`Theme.Shell.switchTypeSize`), so it is the same at every text size.
    static let sideWidth = Theme.Shell.tabBarItemWidth + Theme.Space.xs * 2

    /// The side tab bar's height with `items` items: 184 pt with Messages, 128 pt without.
    static func sideHeight(items: Int) -> CGFloat { Theme.Shell.tabBarItemHeight * CGFloat(items) + Theme.Space.s * 2 }

    /// The bottom tab bar's height (64 pt) and its width with `items` items (184 pt with Messages).
    static let bottomHeight = Theme.Shell.tabBarItemHeight + Theme.Space.xs * 2
    static func bottomWidth(items: Int) -> CGFloat { Theme.Shell.tabBarItemWidth * CGFloat(items) + Theme.Space.s * 2 }

    /// The bottom tab bar stands this far above the safe area's foot, as the system's floating tab bar.
    static let bottomGap = Theme.Space.s

    /// The room the pages keep at their foot under the bottom tab bar (the bar and its gap), as the system's tab bar makes
    /// them keep; none while the keyboard is up, when the bar is away, and none anywhere else.
    static func bottomRoom(bottomBar: Bool, keyboardUp: Bool) -> CGFloat {
        bottomBar && !keyboardUp ? bottomHeight + bottomGap : 0
    }

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
/// (`ShellSideRail`); a scope gives one to the pages that rail stands over: the open Duo's tabs (`TabSwitchPlaced`), the
/// Agents page in the shell (`ChatsView`). Changes only when the tab bar moves (a fold, a text size).
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
    /// The tab bar runs along the foot of the window over the tabs (the open iPhone Duo upright, the Separate layout):
    /// each tab's page keeps its room (`PhoneTabBarHidden`). Set by `TabSwitchOverlay` only.
    @Entry var tabBarAlongFoot = false
    /// iPhone Duo's side column under a rail with the tab bar (#9837): `SideColumnScope`.
    @Entry var sideColumn: SideColumn?
}

#if os(iOS)
/// The tabs on a phone (the open iPhone Duo in the Separate layout, #4231): the system's tab bar is hidden on every tab
/// (`PhoneTabBarHidden`), and the shell's switch stands in its place as the Duo's tab bar, over the tabs and outside their
/// splits and stacks, so it stays put on a push and a pop. The iPad keeps the system's tab bar.
///
/// Where the system's bars are is read here, in the same update as the tabs, not a beat later through a state: neither
/// posture is laid out first as the other.
struct TabSwitchOverlay: ViewModifier {
    private let phone = UIDevice.current.userInterfaceIdiom == .phone

    func body(content: Content) -> some View {
        if phone {
            #if compiler(>=6.4)
            if #available(iOS 27.1, *) {
                content.modifier(TabSwitchSideBars())
            } else {
                content.modifier(TabSwitchPlaced(edge: nil))
            }
            #else
            content.modifier(TabSwitchPlaced(edge: nil))
            #endif
        } else {
            content
        }
    }
}

#if compiler(>=6.4)
@available(iOS 27.1, *)
private struct TabSwitchSideBars: ViewModifier {
    @Environment(\.toolbarVerticalEdge) private var edge

    func body(content: Content) -> some View { content.modifier(TabSwitchPlaced(edge: edge)) }
}
#endif

private struct TabSwitchPlaced: ViewModifier {
    let edge: HorizontalEdge?

    func body(content: Content) -> some View {
        let placement = TabSwitchLayout.placement(phone: true, barEdge: edge)
        content
            .environment(\.tabBarAlongFoot, placement == .bottom)
            .overlay { if let placement { TabSwitchStand(placement: placement) } }
            // The tabs' pages keep their buttons in the side column clear of the tab bar standing over them (#9837).
            .modifier(SideColumnScope(own: true))
    }
}

/// The tab bar where `placement` puts it. Its own view: only it (and the switch in it) reads the tab.
private struct TabSwitchStand: View {
    let placement: TabSwitchLayout.Placement
    @Environment(AppModel.self) private var model

    var body: some View {
        @Bindable var model = model
        switch placement {
        case .rail(let edge):
            TabSwitchSideRail(edge: edge) {
                ShellSwitch(tab: $model.selectedTab, axis: .vertical) { EmptyView() } workMenu: { EmptyView() }
                    .accessibilityIdentifier("tab-switch")
            }
        case .bottom:
            TabSwitchFootBar {
                ShellSwitch(tab: $model.selectedTab, bottomBar: true) { EmptyView() } workMenu: { EmptyView() }
                    .accessibilityIdentifier("tab-switch")
            }
        }
    }
}

/// The tab bar down the open Duo's side column: the outer display's rail (`ShellSideRail`) at the shell's text size, as the
/// pages' rails are (`shellTopBarScaled`), with its buttons' room left empty, so the bar is the same size, at the same
/// text size and as high above the column's foot as on the outer display (`DuoTabBarAlignmentTests`).
struct TabSwitchSideRail<Switcher: View>: View {
    let edge: HorizontalEdge
    @ViewBuilder var switcher: Switcher

    var body: some View {
        ShellSideRail(edge: edge) { EmptyView() } switcher: { switcher }
            .shellTopBarScaled()
            .environment(\.shellRailEdge, edge)
            .ignoresSafeArea(.keyboard)
    }
}

/// The tab bar along the open Duo's foot, upright: centred, `TabSwitchLayout.bottomGap` above the safe area's foot, at
/// the shell's text size as down the side, so its names are the same size in every posture.
struct TabSwitchFootBar<Switcher: View>: View {
    @ViewBuilder var switcher: Switcher

    var body: some View {
        switcher
            .shellTopBarScaled()
            .padding(.bottom, TabSwitchLayout.bottomGap)
            .frame(maxWidth: .infinity, maxHeight: .infinity, alignment: .bottom)
            .ignoresSafeArea(.keyboard)
    }
}

/// A tab's page on a phone (the open iPhone Duo: `TabSwitchOverlay` stands in the system's tab bar's place): the system's
/// tab bar hidden, set once on each tab's root and never toggled (a tab bar shown and hidden on a push flashed the list,
/// and one changed during a drag laid the whole page out again, docs/lessons/ios.md), and, while the Duo's tab bar runs
/// along the foot, its room kept at the page's foot as for the system's tab bar, so lists end above it and the composer
/// stands on it. The iPad keeps its bar.
struct PhoneTabBarHidden: ViewModifier {
    private let phone = UIDevice.current.userInterfaceIdiom == .phone

    func body(content: Content) -> some View {
        if phone { content.toolbarVisibility(.hidden, for: .tabBar).modifier(TabBarRoom()) } else { content }
    }
}

private struct TabBarRoom: ViewModifier {
    @Environment(\.tabBarAlongFoot) private var alongFoot

    func body(content: Content) -> some View {
        // The keyboard is read only while the bar runs along the foot: on its side this draws nothing again.
        let up = alongFoot && KeyboardState.shared.isUp
        content.background { TabBarRoomProbe(room: TabSwitchLayout.bottomRoom(bottomBar: alongFoot, keyboardUp: up)) }
    }
}

/// The room as the system's tab bar makes it: on the tab's own controller (the tab bar controller's child), as
/// `additionalSafeAreaInsets.bottom`, so it reaches every column of the tab's split and every page pushed in it. A safe
/// area padding of the tab's SwiftUI page did not reach into the split's columns: the composer stayed under the bar.
/// Only the bottom edge is this probe's; the top is `TabBarRoomOnce`'s (ChatsView, #8596).
private struct TabBarRoomProbe: UIViewControllerRepresentable {
    var room: CGFloat

    func makeUIViewController(context: Context) -> Probe { Probe() }
    func updateUIViewController(_ probe: Probe, context: Context) { probe.room = room }

    final class Probe: UIViewController {
        var room: CGFloat = 0 { didSet { if room != oldValue { apply() } } }

        override func loadView() {
            view = UIView()
            view.isUserInteractionEnabled = false
        }

        override func didMove(toParent parent: UIViewController?) {
            super.didMove(toParent: parent)
            apply()
        }

        private func apply() {
            var tab = parent
            while let c = tab, !(c.parent is UITabBarController) { tab = c.parent }
            guard let tab, tab.additionalSafeAreaInsets.bottom != room else { return }
            tab.additionalSafeAreaInsets.bottom = room
        }
    }
}
#endif
