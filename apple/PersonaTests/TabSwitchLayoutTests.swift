import Foundation
import SwiftUI
import Testing
@testable import Persona

/// iPhone Duo's tab bar in the Separate layout (#4231, `TabSwitchLayout`): on the outer display the shell's switch as a
/// tab bar (the page's symbol over its name) down the system's side column, centred on the status bar and in its lower
/// middle; the open Duo shows the system's tab bar on the tabs. Settings › Appearance › Sidebar (Merged / Separate)
/// chooses the layout, kept on this device.
@MainActor
struct TabSwitchLayoutTests {
    /// An item is a tap target and more (its symbol over its name), at every text size (a bar of fixed items,
    /// DESIGN_SYSTEM.md 6.2); the side bar fits the side column the status bar is centred on.
    @Test func theItemsAndTheBarsSizes() {
        #expect(Theme.Shell.tabBarItemWidth >= Theme.Size.tapTarget)
        #expect(Theme.Shell.tabBarItemHeight >= Theme.Size.tapTarget)
        #expect(Theme.Shell.switchTypeSize == .large) // the default text size, as the system's tab bar keeps
        #expect(TabSwitchLayout.sideWidth == 64)
        #expect(TabSwitchLayout.sideHeight(items: 3) == 184)
        #expect(TabSwitchLayout.sideHeight(items: 2) == 128)
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
    /// folds or unfolds.
    @Test func theSideBarStandsInTheLowerMiddle() {
        let button = Theme.Size.tapTarget * Theme.sizeScale(.xLarge) // the rail's buttons, a size up at the default size
        let foot = TabSwitchLayout.sideFoot(button: button)
        #expect(abs(foot - 144) < 1)
        for height: CGFloat in [644, 635] { // the outer display's 678 pt and the inner one's 669 pt, less their 34 pt foot
            let top = height - foot - TabSwitchLayout.sideHeight(items: 3)
            #expect(top > height * 0.4 && height - foot < height * 0.8) // its middle in the column's lower half
        }
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

    /// The lens never jumps (#10270): the page a choice brings in is swapped in under the bar in the same frames. Frames
    /// held up by that before it sets off keep it where it starts, and it sets off on the first frame on time; one held up
    /// once it moves moves it on by `longestStep` only, not by the time it took. Every bar drawing the same frame gets the
    /// same lens, and a choice made midway starts from where the lens was drawn.
    @Test func aHeldUpFrameDoesNotMakeTheLensJump() {
        let glide = TabBarGlide()
        glide.begin(from: 0, to: 2)
        let tap = Date(timeIntervalSinceReferenceDate: 1000)
        let frame = 1.0 / 60
        func lens(_ g: TabBarGlide, _ at: Date, is t: TimeInterval) -> Bool {
            let a = g.frame(at: at), b = TabBarGlide.frame(from: g.from, to: g.to, at: t)
            return abs(a.at - b.at) < 1e-6 && abs(a.swell - b.swell) < 1e-6 && abs(a.opacity - b.opacity) < 1e-6
        }
        #expect(lens(glide, tap, is: 0)) // drawn first where it starts
        // The tab's page swapped in, then drawn for the first time: frames 140 ms and 40 ms apart. It waits.
        #expect(lens(glide, tap + 0.14, is: 0))
        #expect(lens(glide, tap + 0.145, is: 0)) // another bar, the same frame
        #expect(lens(glide, tap + 0.18, is: 0))
        // Sets off on time.
        var at = tap + 0.18 + frame
        #expect(lens(glide, at, is: frame))
        // Held up on the way: on by `longestStep`.
        at += 0.1
        #expect(lens(glide, at, is: frame + TabBarGlide.longestStep))
        // On time, frame after frame, it goes on to its end and is gone there.
        while glide.elapsed < TabBarGlide.length {
            at += frame
            _ = glide.frame(at: at)
        }
        #expect(glide.frame(at: at) == TabBarGlide.frame(from: 0, to: 2, at: TabBarGlide.length))
        // Frames slow from the start (a busy phone): it waits `longestWait` at most, then goes on `longestStep` a frame.
        let slow = TabBarGlide()
        slow.begin(from: 0, to: 1)
        _ = slow.frame(at: tap)
        var late = tap
        for _ in 0..<20 where slow.elapsed == 0 {
            late += 0.05
            _ = slow.frame(at: late)
        }
        #expect(abs(slow.elapsed - TabBarGlide.longestStep) < 1e-9)
        #expect(late.timeIntervalSince(tap) < TabBarGlide.longestWait + 0.1 + 0.001)
        // Chosen again midway: from where it was drawn, the clock from 0.
        let other = TabBarGlide()
        other.begin(from: 0, to: 2)
        _ = other.frame(at: tap)
        let midway = other.frame(at: tap + frame).at
        other.begin(from: 2, to: 1)
        #expect(other.from == midway && other.to == 1)
        #expect(lens(other, tap + 2 * frame, is: 0))
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

    /// The open Duo's tabs keep the system's tab bar (Apple's iPhone Duo guidance: iOS places it along the foot upright
    /// and down the side column on its side): no tab hides it and nothing stands in its place.
    @Test func theTabsKeepTheSystemsBar() throws {
        let source = try String(contentsOf: Self.apple.appending(path: "Persona/Views/RootView.swift"), encoding: .utf8)
        #expect(!source.contains("TabSwitchOverlay"))
        #expect(!source.contains("PhoneTabBarHidden"))
        #expect(!source.contains("toolbarVisibility(.hidden, for: .tabBar)"))
    }
}
