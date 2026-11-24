import UIKit
import XCTest

/// Supported navigation is selected by platform and width, never by a saved presentation choice.
final class DefaultLayoutTests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func launch(tab: String, retiredArgument: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        if tab == "settings", let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] {
            let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
            app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", tab, "-app.tab", tab,
                                   "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN", "-probes", "YES"]
        } else {
            app.launchArguments = ["-demo.on", "YES", "-openWelcome", "YES", "-dev.openChat", "", "-tab", tab,
                                   "-app.tab", tab == "dev" ? "chats" : tab, "-AppleLanguages", "(zh-Hans)",
                                   "-AppleLocale", "zh_CN", "-probes", "YES"]
        }
        if retiredArgument {
            app.launchArguments += ["-" + ["ui.", "old", "Layout"].joined(), "YES"]
        }
        app.launchPinned()
        return app
    }

    @MainActor private func element(_ id: String, in app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    @MainActor func testRetiredArgumentStillUsesCompactShell() {
        let app = launch(tab: "assistant", retiredArgument: true)
        XCTAssertTrue(element("shell-assistant", in: app).waitForExistence(timeout: 30))
        XCTAssertTrue(element("shell-agents", in: app).exists)
        XCTAssertFalse(app.tabBars.firstMatch.exists)
        element("shell-agents", in: app).tap()
        XCTAssertTrue(element("agents.grouping", in: app).waitForExistence(timeout: 20))
    }

    @MainActor func testAppearanceHasOnlySupportedChoices() {
        let app = launch(tab: "settings")
        XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
        let appearance = app.settingsAdvancedRow("settings-appearance")
        XCTAssertTrue(appearance.exists)
        appearance.tap()
        let style = element("settings-style", in: app)
        XCTAssertTrue(style.waitForExistence(timeout: 15))
        for id in ["settings-color", "settings-scheme"] {
            XCTAssertTrue(element(id, in: app).exists, "Missing appearance choice: \(id)")
        }
        XCTAssertFalse(element("settings-layout", in: app).exists)
        element("settings-color", in: app).tap()
        XCTAssertTrue(element("color-style", in: app).waitForExistence(timeout: 15))
    }

    @MainActor func testRegularWidthKeepsSidebarNavigation() {
        let app = launch(tab: "assistant", retiredArgument: true)
        XCTAssertGreaterThan(app.frame.width, 700)
        XCTAssertTrue(element("sidebar-main", in: app).waitForExistence(timeout: 30))
        XCTAssertTrue(element("sidebar-inbox", in: app).exists)
        XCTAssertTrue(element("sidebar-settings", in: app).exists)
        // No top switch: the iPad's tabs are the system's; a phone's (the open Duo's) are the Duo's tab bar, down the side
        // column or along the foot (#4231, DuoTabSwitchTests).
        let item = element("shell-assistant", in: app)
        if UIDevice.current.userInterfaceIdiom == .phone {
            XCTAssertTrue(item.exists && (item.frame.midX > app.frame.width * 0.8 || item.frame.minY > app.frame.midY),
                          "no tab bar down the side or along the foot")
        } else {
            XCTAssertFalse(item.exists)
        }
        XCTAssertFalse(app.tabBars.buttons["收件箱"].exists)
        XCTAssertFalse(app.tabBars.buttons["设置"].exists)
        element("sidebar-settings", in: app).tap()
        let close = element("settings-close", in: app)
        XCTAssertTrue(close.waitForExistence(timeout: 20))
        close.tap()
        XCTAssertTrue(element("sidebar-main", in: app).waitForExistence(timeout: 15))
    }
}
