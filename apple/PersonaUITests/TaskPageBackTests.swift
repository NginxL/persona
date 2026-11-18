import XCTest

/// A task's page opened from its row in In Progress has one way back (#2625; its receipt's View until #8237). Beside the sidebar (regular width) it is
/// pushed: under iPhone Duo's side bars its buttons are the system's, in the side column, and the page hid the system's
/// bar with its Back in it; with its own header it has that header's ‹ alone, no system bar over it. On a phone it is a
/// sheet with its header's ✕ alone. `-sideBars YES` lays the chat out as under the Duo's side bars on any simulator.
/// Against scripts/mock/mock_hub.py with MOCK_TASKS=1 (PERSONA_MOCK_HUB): the widget fix, working, its row in the main
/// chat's In Progress. PERSONA_SHOTS keeps the screens.
final class TaskPageBackTests: XCTestCase {
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait // another test may have left the iPad on its side
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    /// Pushed, under side bars: one Back (the system's bar's, from the page's items); it leads back, the row again opens.
    @MainActor func testUnderSideBarsThePushedPageHasOneBack() throws {
        try pushed(["-sideBars", "YES"], name: "side-bars")
    }

    /// Pushed, with the page's own header: its ‹ alone, no system bar with a second one over it.
    @MainActor func testWithItsHeaderThePushedPageHasOneBack() throws {
        try pushed([], name: "header")
    }

    /// A phone's sheet: its header's ✕ alone, no system bar.
    @MainActor func testOnAPhoneTheSheetHasOneClose() throws {
        let app = launch([])
        guard app.windows.firstMatch.frame.width < 700 else { throw XCTSkip("a sheet in compact width only") }
        openView(app)
        let close = app.buttons.matching(NSPredicate(format: "label IN %@", ["关闭", "Close"]))
        XCTAssertTrue(close.firstMatch.waitForExistence(timeout: 10), "no ✕ on the sheet:\n\(app.debugDescription)")
        XCTAssertEqual(close.count, 1, "one ✕ on the sheet")
        XCTAssertEqual(backs(app).count, 0, "no system bar's back over the sheet's header")
        shot(app, "task-sheet-phone")
        close.firstMatch.tap()
        XCTAssertTrue(chat(app).waitForExistence(timeout: 10), "✕ did not lead back to the main chat")
    }

    // MARK: -

    @MainActor private func pushed(_ extra: [String], name: String) throws {
        let app = launch(extra)
        guard app.windows.firstMatch.frame.width > 700 else { throw XCTSkip("pushed beside the sidebar in regular width only (iPad, iPhone Duo open)") }
        openView(app)
        let back = backs(app)
        XCTAssertTrue(back.firstMatch.waitForExistence(timeout: 10), "no way back from the task's page:\n\(app.debugDescription)")
        sleep(1)
        XCTAssertEqual(back.count, 1, "one way back, not two:\n\(app.debugDescription)")
        shot(app, "task-page-\(name)")
        back.firstMatch.tap()
        XCTAssertTrue(chat(app).waitForExistence(timeout: 10), "back did not lead to the main chat:\n\(app.debugDescription)")
        openView(app)
        XCTAssertTrue(back.firstMatch.waitForExistence(timeout: 10), "the row did not open the page again")
    }

    @MainActor private func launch(_ extra: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // The default manager's Persona page: the mock's hub has more than one, and its tasks are the default's.
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant", "-openAssistantManager", "assistant"] + extra
        app.launchPinned()
        return app
    }

    /// The widget fix's row in In Progress.
    @MainActor private func view(_ app: XCUIApplication) -> XCUIElement {
        app.buttons.matching(identifier: "action-tray.task").matching(NSPredicate(format: "label CONTAINS %@", "修小组件横屏崩溃")).firstMatch
    }

    /// The main chat's In Progress entry: the chat is on screen.
    @MainActor private func chat(_ app: XCUIApplication) -> XCUIElement { app.buttons["action-bar.running"].firstMatch }

    @MainActor private func backs(_ app: XCUIApplication) -> XCUIElementQuery {
        // Under side bars a pushed page's Back is the system's (#9971), named by the page under it: its identifier says Back.
        app.buttons.matching(NSPredicate(format: "identifier == 'BackButton' OR label IN %@", ["返回", "Back"]))
    }

    /// The widget fix's row: In Progress opened, the row tapped (it closes the tray and opens the task).
    @MainActor private func openView(_ app: XCUIApplication) {
        let entry = chat(app)
        XCTAssertTrue(entry.waitForExistence(timeout: 20), "no In Progress entry in the main chat:\n\(app.debugDescription)")
        let view = view(app)
        if !view.exists { entry.tap() }
        XCTAssertTrue(view.waitForExistence(timeout: 10), "no widget fix row in In Progress:\n\(app.debugDescription)")
        view.tap()
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + style + ".png"))
        }
    }
}
