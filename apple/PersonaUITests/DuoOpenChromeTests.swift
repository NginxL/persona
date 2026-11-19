import XCTest

/// iPhone Duo open, the inner screen, in the Separate layout's new look (#4206; it replaced #2623's switch on the page):
/// - the system's tab bar on the tabs: iOS puts it down the side column on its side and along the foot upright; no
///   switch drawn on the page's top and none over the tabs;
/// - each tab its part of the one sidebar's column: its fold at the head, New Chat, Search and Settings at the foot;
/// - Search and Settings over the window, and the iPhone's new conversation with Needs You (#3680);
/// - an Agent asked for from outside on the Agents tab.
///
/// A phone at regular width only: the Duo simulator opened in Xcode's Device Hub, on its side
/// (TEST_RUNNER_PERSONA_ORIENTATION=landscape) or upright. The iPad keeps its tab bar (IPadSidebarTests).
/// Taps and long presses only (upright, synthetic drags miss on the open Duo). Against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB).
/// TEST_RUNNER_PERSONA_HOLD=<seconds> holds each state, named in an activity `duo-shot <name>`, for
/// `simctl io --display=3 screenshot`; XCUITest's own screenshot captures the dark outer screen.
final class DuoOpenChromeTests: XCTestCase {
    /// A tab item is at most about 100 pt wide (wider along the foot upright, where it has its name beside it room);
    /// a heading or a row is wider.
    private static let tabItemWidth: CGFloat = 120

    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("a phone's tabs only (iPhone Duo open)") }
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        XCUIDevice.shared.orientation = ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" ? .landscapeLeft : .portrait
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant"]
        app.launchPinned()
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("regular width only (iPhone Duo open)") }
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    /// The system tab bar's item: a button of that name a tab item wide, not the column's heading of the same name. The
    /// bar itself is not a `TabBar` to XCUITest there.
    @MainActor private func systemTab(_ label: String) -> XCUIElement {
        let narrow = NSPredicate { item, _ in ((item as? XCUIElementAttributes)?.frame.width ?? .infinity) <= Self.tabItemWidth }
        return app.buttons.matching(NSPredicate(format: "label == %@", label)).matching(narrow).firstMatch
    }

    /// The tab bar's item for a tab: the system's, by the tab's name.
    @MainActor private func tab(_ id: String) -> XCUIElement { systemTab(id == "shell-agents" ? "Agents" : "Persona") }

    @MainActor private func shot(_ name: String) {
        XCTContext.runActivity(named: "duo-shot \(name)") { _ in
            if let hold = ProcessInfo.processInfo.environment["PERSONA_HOLD"].flatMap(UInt32.init) { sleep(hold) }
        }
    }

    /// The system's tab bar on the tabs, no switch drawn on the page's top (#4206, the new look of the Separate layout)
    /// and none over the tabs: the column's head has its fold, its foot Search; the page has its menu (the name pill beside the
    /// column, whose head shows the face, #9973), and its sidebar button only while the column is away.
    @MainActor func testTheTabSwitchAndTheColumnsFold() {
        let main = element("sidebar-main"), fold = element("sidebar-fold-column"), search = element("sidebar-search")
        XCTAssertTrue(main.waitForExistence(timeout: 20), "no column:\n\(app.debugDescription)")
        XCTAssertTrue(tab("shell-assistant").exists && tab("shell-agents").exists, "no system tab bar:\n\(app.debugDescription)")
        XCTAssertFalse(element("tab-switch").exists, "a tab bar drawn over the system's:\n\(app.debugDescription)")
        XCTAssertFalse(element("page-tab-switch").exists, "a switch drawn on the page")
        XCTAssertGreaterThan(tab("shell-agents").frame.minY, app.windows.firstMatch.frame.midY, "the tab bar is not low in the window")
        XCTAssertTrue(fold.exists && search.exists, "no fold or Search:\n\(app.debugDescription)")
        XCTAssertLessThan(fold.frame.maxY, main.frame.minY, "the fold is not at the column's head")
        let window = app.windows.firstMatch.frame
        XCTAssertGreaterThan(search.frame.minY, window.maxY - 160, "Search is not at the column's foot")
        XCTAssertTrue(element("persona-avatar").exists, "no menu on the page")
        let back = element("assistant-sidebar-button")
        XCTAssertFalse(back.exists && back.isHittable, "the page's sidebar button shows beside the column")
        shot("chrome")

        fold.tap()
        XCTAssertTrue(fold.waitForNonExistence(timeout: 5), "the column did not fold")
        XCTAssertTrue(back.waitForExistence(timeout: 5) && back.isHittable, "nothing on the page brings the column back")
        shot("folded")
        back.tap()
        XCTAssertTrue(fold.waitForExistence(timeout: 5), "the column did not come back")
    }

    /// Search covers the window, as ChatGPT's (#3705), from the column's foot (#4206): its field at the foot over the
    /// keyboard with × beside it, Chats and Agents over the results; what is typed narrows them, a row opens its
    /// conversation, × closes.
    @MainActor func testSearchCoversTheWindowWithItsFieldAtTheFoot() {
        let search = element("sidebar-search")
        XCTAssertTrue(search.waitForExistence(timeout: 20), "no Search at the column's foot:\n\(app.debugDescription)")
        search.tap()
        let field = element("full-search-field"), close = app.buttons["full-search-close"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "Search opened no field:\n\(app.debugDescription)")
        XCTAssertTrue(element("full-search-scope").exists, "no Chats | Agents")
        let keyboard = app.keyboards.firstMatch
        XCTAssertTrue(keyboard.waitForExistence(timeout: 5), "the field did not take the keyboard")
        sleep(1)
        let window = app.windows.firstMatch.frame
        XCTAssertGreaterThan(field.frame.minY, window.midY - 100, "the field is not at the foot: \(field.frame) in \(window)")
        XCTAssertLessThanOrEqual(field.frame.maxY, keyboard.frame.minY + 1, "the field is under the keyboard: \(field.frame), \(keyboard.frame)")
        XCTAssertTrue(close.exists && close.frame.minX > field.frame.maxX - 1, "no × beside the field")
        let rows = app.buttons.matching(identifier: "full-search-row")
        XCTAssertGreaterThan(rows.count, 1, "no conversations above the field:\n\(app.debugDescription)")
        shot("search")
        field.typeText("韩梅梅")
        let hit = rows.firstMatch
        XCTAssertTrue(hit.waitForExistence(timeout: 5) && hit.label.contains("韩梅梅"), "typing did not narrow the list: \(hit.label)")
        hit.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5), "a row did not close the search")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "韩梅梅")).firstMatch.waitForExistence(timeout: 10),
                      "the row did not open its conversation:\n\(app.debugDescription)")
        search.tap()
        XCTAssertTrue(close.waitForExistence(timeout: 5), "Search did not open again")
        close.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5), "× did not close the search")
    }

    /// A new conversation is the iPhone's (#3680): what needs the user over the composer (Agents waiting, the Inbox's
    /// Needs You), as `ShellHome` shows it on every iPhone.
    @MainActor func testTheNewConversationShowsWhatNeedsYou() {
        let new = app.drawerNewChatButton
        XCTAssertTrue(new.waitForExistence(timeout: 20), "no New Chat in the column:\n\(app.debugDescription)")
        new.tap()
        XCTAssertTrue(element("shell-needs-you").waitForExistence(timeout: 10), "no Needs You over the composer:\n\(app.debugDescription)")
        XCTAssertTrue(element("assistant-welcome").exists, "no welcome on the new conversation")
        shot("new-conversation")
    }

    /// Settings covers the window, as ChatGPT's account page does there, not a sheet over half of it (#3701): one column
    /// across, its × at the top left (#9653), which closes it.
    @MainActor func testSettingsCoversTheWindowAndCloses() {
        let settings = element("sidebar-settings")
        XCTAssertTrue(settings.waitForExistence(timeout: 20), "no Settings at the column's foot:\n\(app.debugDescription)")
        settings.tap()
        let close = app.buttons["settings-close"].firstMatch
        XCTAssertTrue(close.waitForExistence(timeout: 10), "Settings has no ×:\n\(app.debugDescription)")
        shot("settings")
        let window = app.windows.firstMatch.frame, list = element("settings-list")
        XCTAssertTrue(list.exists, "no settings list:\n\(app.debugDescription)")
        XCTAssertGreaterThan(list.frame.width, window.width * 0.8, "Settings does not cover the window: \(list.frame) in \(window)")
        XCTAssertLessThan(close.frame.midX, window.width * 0.15, "the × is not at the window's left: \(close.frame) in \(window)")
        XCTAssertLessThan(close.frame.minY, window.minY + window.height / 4, "the × is not at the top: \(close.frame) in \(window)")
        close.tap()
        XCTAssertTrue(close.waitForNonExistence(timeout: 5), "the × did not close Settings")
        XCTAssertTrue(settings.waitForExistence(timeout: 5) && settings.isHittable, "the page did not come back")
    }

    /// The switch's Agents: the same column with the Agents' part beside the Agents' page, and back to Persona's.
    @MainActor func testTheTabsGoToAgentsAndBack() {
        XCTAssertTrue(element("sidebar-main").waitForExistence(timeout: 20), "no column:\n\(app.debugDescription)")
        tab("shell-agents").tap()
        XCTAssertTrue(element("sidebar-agents-new").waitForExistence(timeout: 10), "no Agents in the column:\n\(app.debugDescription)")
        XCTAssertFalse(element("sidebar-main").exists, "the Agents tab's column holds Persona's rows")
        shot("agents")
        tab("shell-assistant").tap()
        XCTAssertTrue(element("sidebar-main").waitForExistence(timeout: 10), "the switch did not go back to Persona")
    }

    /// An agent asked for from outside opens on the Agents tab, beside its column, and the switch goes back to Persona.
    @MainActor func testAnOpenedAgentIsOnTheAgentsTab() {
        app.terminate()
        app.launchArguments += ["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000002"]
        app.launchPinned()
        let title = element("chat.title")
        XCTAssertTrue(title.waitForExistence(timeout: 20), "the agent did not open:\n\(app.debugDescription)")
        XCTAssertTrue(tab("shell-assistant").exists && tab("shell-agents").isSelected, "no switch over the opened agent")
        shot("agent-open")
        tab("shell-assistant").tap()
        XCTAssertTrue(element("sidebar-main").waitForExistence(timeout: 10), "the switch did not go back to Persona")
    }
}
