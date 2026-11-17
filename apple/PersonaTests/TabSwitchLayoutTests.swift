#if os(iOS)
import Foundation
import SwiftUI
import Testing
@testable import Persona

/// iPhone Duo's tab bar in the Separate layout (#4231, `TabSwitchLayout`): the same items on both displays (the page's
/// symbol over its name), down the system's side column, centred on the status bar and in its lower middle, or along the
/// foot of the inner display upright, where the pages keep its room as for the system's tab bar. Settings › Appearance ›
/// Sidebar (Merged / Separate) chooses it, kept on this device.
@MainActor
struct TabSwitchLayoutTests {
    /// Where the tab bar stands in each posture of the open Duo, and nowhere on the iPad, which keeps the system's tab bar.
    @Test func itStandsWhereTheSystemsBarsAre() {
        #expect(TabSwitchLayout.placement(phone: true, barEdge: nil) == .bottom) // open, upright: bars across the top
        #expect(TabSwitchLayout.placement(phone: true, barEdge: .trailing) == .rail(.trailing)) // open, on its side
        #expect(TabSwitchLayout.placement(phone: true, barEdge: .leading) == .rail(.leading))
        #expect(TabSwitchLayout.placement(phone: false, barEdge: nil) == nil) // the iPad
        #expect(TabSwitchLayout.placement(phone: false, barEdge: .trailing) == nil)
    }

    /// An item is a tap target and more (its symbol over its name), at every text size (a bar of fixed items,
    /// DESIGN_SYSTEM.md 6.2); the side bar fits the side column the status bar is centred on.
    @Test func theItemsAndTheBarsSizes() {
        #expect(Theme.Shell.tabBarItemWidth >= Theme.Size.tapTarget)
        #expect(Theme.Shell.tabBarItemHeight >= Theme.Size.tapTarget)
        #expect(Theme.Shell.switchTypeSize == .large) // the default text size, as the system's tab bar keeps
        #expect(TabSwitchLayout.sideWidth == 64)
        #expect(TabSwitchLayout.sideHeight(items: 3) == 184)
        #expect(TabSwitchLayout.sideHeight(items: 2) == 128)
        #expect(TabSwitchLayout.bottomHeight == 64)
        #expect(TabSwitchLayout.bottomWidth(items: 3) == 184)
        #expect(TabSwitchLayout.items(messages: true) == 3)
        #expect(TabSwitchLayout.items(messages: false) == 2)
        // The Duo's 84 pt inset (both displays) less the screen edge's strip: 70 pt, the bar within it.
        #expect(TabSwitchLayout.sideColumn(inset: 84) == 70)
        #expect(TabSwitchLayout.sideColumn(inset: 84) >= TabSwitchLayout.sideWidth)
        #expect(TabSwitchLayout.sideColumn(inset: 0) == TabSwitchLayout.sideWidth) // never narrower than the bar
    }

    /// Down the side it is centred on the status bar: measured on both displays, the clock 49 pt from the screen's edge.
    @Test func theSideBarIsCentredOnTheStatusBar() {
        let inset: CGFloat = 84
        let column = TabSwitchLayout.sideColumn(inset: inset)
        let centreFromEdge = (inset - column) + column / 2
        #expect(centreFromEdge == 49)
    }

    /// In the lower middle of the column, not a small block at its foot: on both displays the room of the outer
    /// display's face and page's action under it (144 pt at the default text size), so it stands as high when the Duo
    /// folds or unfolds (`DuoTabBarAlignmentTests` lays both out).
    @Test func theSideBarStandsInTheLowerMiddle() {
        let button = Theme.Size.tapTarget * Theme.sizeScale(.xLarge) // the rail's buttons, a size up at the default size
        let foot = TabSwitchLayout.sideFoot(button: button)
        #expect(abs(foot - 144) < 1)
        for height: CGFloat in [644, 635] { // the outer display's 678 pt and the inner one's 669 pt, less their 34 pt foot
            let top = height - foot - TabSwitchLayout.sideHeight(items: 3)
            #expect(top > height * 0.4 && height - foot < height * 0.8) // its middle in the column's lower half
        }
    }

    /// Along the foot upright the pages keep the bar's room, as the system's tab bar makes them keep; with the keyboard up
    /// the bar is away and the room goes; on its side there is none.
    @Test func theRoomUnderTheBottomBar() {
        #expect(TabSwitchLayout.bottomRoom(bottomBar: true, keyboardUp: false) == TabSwitchLayout.bottomHeight + TabSwitchLayout.bottomGap)
        #expect(TabSwitchLayout.bottomRoom(bottomBar: true, keyboardUp: true) == 0)
        #expect(TabSwitchLayout.bottomRoom(bottomBar: false, keyboardUp: false) == 0)
    }

    /// A page's buttons in the side column stand clear of the tab bar (#9837): the leading ones that fit stay, Back and ⋯
    /// always, and without a tab bar in the column all of them. Measured on iOS 27.2: on the open Duo on its side the tab
    /// bar's top was 160 pt under the system's first item (an Agent's chat: terminal, Git and ⋯ fit, Files goes to ⋯); on
    /// the outer display's rail 141 pt under Back (only ⋯ fits beside it).
    @Test func aPagesButtonsStandClearOfTheTabBar() {
        #expect(TabSwitchLayout.pageButtons(3, back: false, room: nil) == 3)
        #expect(TabSwitchLayout.pageButtons(3, back: false, room: 160.3) == 2)
        #expect(TabSwitchLayout.pageButtons(3, back: true, room: 141.3) == 0)
        #expect(TabSwitchLayout.pageButtons(3, back: false, room: 1000) == 3)
        #expect(TabSwitchLayout.pageButtons(2, back: true, room: 0) == 0)
        let item = Theme.Shell.systemBarItem, gap = Theme.Shell.systemBarItemGap
        for room in stride(from: CGFloat(0), through: 700, by: 0.5) {
            for back in [false, true] {
                let kept = TabSwitchLayout.pageButtons(9, back: back, room: room)
                let start = back ? item + Theme.Shell.systemBarBackGap : 0
                // The glass of the kept ones and ⋯ ends `Space.s` over the tab bar, and one more would not.
                let glass = CGFloat(kept + 1) * item + CGFloat(kept) * gap
                if kept > 0 { #expect(start + glass + Theme.Space.s <= room, "\(kept) kept in \(room)") }
                if kept < 9 { #expect(start + glass + item + gap + Theme.Space.s > room, "\(kept) kept in \(room): room for one more") }
            }
        }
    }

    /// The system's first item is lower on the outer display (its camera is over the status bar) than on the inner one.
    @Test func theSystemsItemsStartLowerOnTheOuterDisplay() {
        #expect(TabSwitchLayout.systemBarTop(windowWidth: 466) == Theme.Shell.systemBarTopOuter)
        #expect(TabSwitchLayout.systemBarTop(windowWidth: 669) == Theme.Shell.systemBarTopInner)
        #expect(TabSwitchLayout.systemBarTop(windowWidth: 951) == Theme.Shell.systemBarTopInner)
        #expect(Theme.Shell.systemBarTopOuter > Theme.Shell.systemBarTopInner)
    }

    /// The column's room follows the rails with a tab bar on screen: the highest one's, none once they are gone.
    @Test func theColumnFollowsTheRailsOnScreen() {
        let column = SideColumn(), a = UUID(), b = UUID()
        #expect(column.room == nil)
        column.report(a, top: 300, windowWidth: 951)
        #expect(column.room == 300 - Theme.Shell.systemBarTopInner)
        column.report(b, top: 280, windowWidth: 951)
        #expect(column.room == 280 - Theme.Shell.systemBarTopInner)
        column.report(b, top: nil, windowWidth: 951)
        #expect(column.room == 300 - Theme.Shell.systemBarTopInner)
        column.report(a, top: nil, windowWidth: 951)
        #expect(column.room == nil)
        column.report(a, top: 315, windowWidth: 466)
        #expect(column.room == 315 - Theme.Shell.systemBarTopOuter)
    }

    /// The lens over a newly chosen item (#9837): it starts on the item that was chosen at full strength, swells on the
    /// way, passes the new one with a bounce, and is gone, back to its size, on the new one once the glide ends.
    @Test func theLensGlidesFromTheOldItemToTheNew() {
        let start = TabBarGlide.frame(from: 0, to: 2, at: 0)
        #expect(start.at == 0 && start.opacity == 1 && start.swell == 1)
        let mid = TabBarGlide.frame(from: 0, to: 2, at: Theme.Motion.lensSwellTime)
        #expect(mid.swell == Theme.Motion.lensSwellScale && mid.opacity == 1 && mid.at > 0)
        let path = stride(from: 0.0, through: Theme.Motion.lensGlideTime, by: 0.01).map { TabBarGlide.frame(from: 0, to: 2, at: $0).at }
        #expect(path.max()! > 2) // a little bounce past the new item
        let end = TabBarGlide.frame(from: 0, to: 2, at: TabBarGlide.length)
        #expect(end.at == 2 && end.opacity == 0 && end.swell == 1)
        let back = TabBarGlide.frame(from: 1.5, to: 0, at: TabBarGlide.length + 1)
        #expect(back.at == 0 && back.opacity == 0)
    }

    /// The choice is Settings › Appearance › Sidebar's, this device's own: picking Separate is written at once and read
    /// back by a new reader of the same defaults, as the app's root after a relaunch; nothing picked stays Merged on the
    /// open Duo and Separate on its outer display (#4206's default, unchanged).
    @Test func theChoiceIsKept() throws {
        let suite = "persona.tests.tab-switch.\(UUID().uuidString)"
        let store = try #require(UserDefaults(suiteName: suite))
        defer { store.removePersistentDomain(forName: suite) }
        @StoredValue(NavigationLayout.key, store: store) var picked = ""
        #expect(NavigationLayout.effective(picked: picked, regular: true) == .sidebar) // the open Duo: Merged
        #expect(NavigationLayout.effective(picked: picked, regular: false) == .separate) // its outer display
        picked = NavigationLayout.separate.rawValue
        #expect(store.string(forKey: NavigationLayout.key) == "separate")
        let relaunched = try #require(UserDefaults(suiteName: suite))
        @StoredValue(NavigationLayout.key, store: relaunched) var again = ""
        #expect(again == "separate")
        // A pick holds on both displays: folding and unfolding keep the tabs.
        #expect(NavigationLayout.effective(picked: again, regular: true) == .separate)
        #expect(NavigationLayout.effective(picked: again, regular: false) == .separate)
        picked = NavigationLayout.sidebar.rawValue
        #expect(store.string(forKey: NavigationLayout.key) == "sidebar")
    }

    private static let apple = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()

    /// The tabs hide the system's tab bar on a phone (the open Duo) and the Duo's stands in its place; every tab's page
    /// hides it and keeps the bottom bar's room (`PhoneTabBarHidden`), or the system's bar comes back on that tab, or its
    /// composer goes under the bar.
    @Test func everyTabHidesTheSystemsBarOnAPhone() throws {
        let source = try String(contentsOf: Self.apple.appending(path: "Persona/Views/RootView.swift"), encoding: .utf8)
        #expect(source.contains(".modifier(TabSwitchOverlay())"))
        for tab in ["struct AssistantRootTab", "struct AgentsRootTab", "struct FeishuRootTab"] {
            let start = try #require(source.range(of: tab), "\(tab) not in RootView.swift")
            let rest = source[start.upperBound...]
            let body = rest[..<(rest.range(of: "\n}\n")?.upperBound ?? rest.endIndex)]
            #expect(body.contains("PhoneTabBarHidden()"), "\(tab) shows the system's tab bar under the Duo's")
        }
    }
}
#endif
