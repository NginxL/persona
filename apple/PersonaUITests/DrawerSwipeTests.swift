import XCTest

/// The Persona tab's sidebar drag (docs/apple/ios.md「Persona top bar and sidebar」): it opens from wherever a finger starts a drag to the
/// right — the middle of the chat at a slant, on a message bubble, right after the list stopped scrolling — while a
/// code block wider than the phone still scrolls sideways under the finger.
/// Against scripts/mock/mock_hub.py started with MOCK_DRAWER=1 (PERSONA_MOCK_HUB): the main conversation ends with a wide
/// code block; ASK4 is a long conversation.
final class DrawerSwipeTests: XCTestCase {
    private var hub: String!
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let long = "server/b2e4d6f8-0000-4000-8000-000000000004"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_DRAWER=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(_ open: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistant", open, "-showDrawerProbe", "YES", "-probes", "YES"]
        app.launchPinned()
        XCTAssertTrue(app.buttons["shell-assistant"].waitForExistence(timeout: 15), "the Persona tab did not open")
        return app
    }

    @MainActor private func sidebar(_ app: XCUIApplication) -> XCUIElement { app.descendants(matching: .any)["assistant-sidebar"] }

    @MainActor private func isOpen(_ app: XCUIApplication, within seconds: TimeInterval = 3) -> Bool {
        sidebar(app).waitForExistence(timeout: seconds)
    }

    @MainActor private func waitClosed(_ app: XCUIApplication, _ why: String) {
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: sidebar(app))
        XCTAssertEqual(XCTWaiter.wait(for: [gone], timeout: 5), .completed, why)
    }

    /// Closes the sidebar the way a user does: a tap on the page beside it.
    @MainActor private func close(_ app: XCUIApplication) {
        app.descendants(matching: .any)["assistant-drawer-close"].tap()
        waitClosed(app, "a tap beside the sidebar did not close it")
    }

    /// A finger's drag from `start` (a point in the window) by `dx`, `dy` points.
    @MainActor private func drag(_ app: XCUIApplication, from start: CGPoint, dx: CGFloat, dy: CGFloat,
                                 velocity: XCUIGestureVelocity = .default, hold: TimeInterval = 0) {
        let origin = app.windows.firstMatch.coordinate(withNormalizedOffset: .zero)
        let a = origin.withOffset(CGVector(dx: start.x, dy: start.y))
        a.press(forDuration: 0.05, thenDragTo: a.withOffset(CGVector(dx: dx, dy: dy)), withVelocity: velocity, thenHoldForDuration: hold)
    }

    @MainActor private func point(_ app: XCUIApplication, _ x: CGFloat, _ y: CGFloat) -> CGPoint {
        let f = app.windows.firstMatch.frame
        return CGPoint(x: f.width * x, y: f.height * y)
    }

    /// From the middle of the chat at a slant — up or down, as a thumb moves — the sidebar comes out.
    @MainActor func testSlantedDragFromTheMiddleOpens() throws {
        let app = launch(main)
        XCTAssertTrue(app.staticTexts["同一秒拍的会自动加上 -1、-2。"].waitForExistence(timeout: 10), "the conversation did not load")
        for (angle, x, y) in [(30.0, 0.3, 0.45), (-30.0, 0.35, 0.6), (40.0, 0.4, 0.35), (-40.0, 0.25, 0.55)] {
            let dx: CGFloat = 200, dy = dx * CGFloat(tan(angle * .pi / 180))
            drag(app, from: point(app, x, y), dx: dx, dy: dy)
            XCTAssertTrue(isOpen(app), "a drag at \(angle)° from the middle did not open the sidebar")
            close(app)
        }
    }

    /// Starting on a message — the user's bubble or the reply's text — opens it too.
    @MainActor func testDragOnABubbleOpens() throws {
        let app = launch(main)
        for text in ["给我一条命令，把今天的照片按拍摄时间改名", "同一秒拍的会自动加上 -1、-2。"] {
            let bubble = app.staticTexts[text]
            XCTAssertTrue(bubble.waitForExistence(timeout: 10), "no \(text)")
            let f = bubble.frame
            drag(app, from: CGPoint(x: f.minX + min(40, f.width / 2), y: f.midY), dx: 200, dy: 40)
            XCTAssertTrue(isOpen(app), "a drag starting on 「\(text)」 did not open the sidebar")
            close(app)
        }
    }

    /// The list was just flicked and has come to rest: the next drag to the right opens the sidebar, not a scroll.
    @MainActor func testDragRightAfterScrollingOpens() throws {
        let app = launch(long)
        XCTAssertTrue(app.staticTexts["第 30 本也整理一下"].waitForExistence(timeout: 10), "the long conversation did not load")
        for flick in [CGFloat(400), -300] {
            drag(app, from: point(app, 0.5, 0.4), dx: 0, dy: flick, velocity: .fast)
            drag(app, from: point(app, 0.3, 0.5), dx: 200, dy: 30)
            XCTAssertTrue(isOpen(app), "after a flick of \(flick) the drag did not open the sidebar")
            close(app)
        }
    }

    /// A drag on a code block wider than the phone scrolls the code, in both directions, and leaves the sidebar alone;
    /// once the code is back at its left edge, a drag to the right on it opens the sidebar.
    @MainActor func testCodeBlockScrollsSideways() throws {
        let app = launch(main)
        let code = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "exiftool")).firstMatch
        XCTAssertTrue(code.waitForExistence(timeout: 10), "no code block:\n\(app.debugDescription)")
        let x0 = code.frame.minX
        let y = code.frame.midY
        let w = app.windows.firstMatch.frame.width
        drag(app, from: CGPoint(x: w * 0.8, y: y), dx: -200, dy: 0)
        XCTAssertFalse(sidebar(app).exists, "a drag to the left on the code opened the sidebar")
        XCTAssertLessThan(code.frame.minX, x0 - 50, "a drag to the left did not scroll the code")
        drag(app, from: CGPoint(x: w * 0.3, y: y), dx: 120, dy: 0)
        XCTAssertFalse(isOpen(app, within: 1), "a drag to the right on scrolled code opened the sidebar instead of scrolling it")
        XCTAssertGreaterThan(code.frame.minX, x0 - 200 + 60, "a drag to the right did not scroll the code back")
        // Back at its left edge.
        drag(app, from: CGPoint(x: w * 0.3, y: y), dx: 300, dy: 0)
        XCTAssertEqual(code.frame.minX, x0, accuracy: 2, "the code is not back at its left edge")
        if !sidebar(app).exists { drag(app, from: CGPoint(x: w * 0.3, y: y), dx: 200, dy: 0) }
        XCTAssertTrue(isOpen(app), "a drag to the right on code at its left edge did not open the sidebar")
    }

    /// Settings from the drawer opens over it (a task, docs/design/sidebar-navigation.md, #6599); closing Settings comes back to
    /// the open drawer, and once that closes the chat and both page switches are as before.
    @MainActor func testDrawerSettingsReturnsToTheDrawerThenPagesSwitch() throws {
        let app = launch(main)
        let agents = app.buttons["shell-agents"].firstMatch
        // XCUITest calls the switch not hittable under the shell's status window (it takes no touches): the switch is
        // tapped by its coordinates, as a finger does (`ShellFinger`), and checked through the probe.
        XCTAssertTrue(agents.waitForExistence(timeout: 5), "no Agents switch")
        drag(app, from: point(app, 0.3, 0.5), dx: 220, dy: 20)
        XCTAssertTrue(isOpen(app))
        XCTAssertFalse(agents.isHittable, "the page switch is reachable under the drawer")
        let settings = app.drawerSettingsButton
        XCTAssertTrue(settings.waitForExistence(timeout: 5) && settings.isHittable, "the drawer's Settings is covered:\n\(app.debugDescription)")
        settings.tapCenter()
        XCTAssertTrue(app.navigationBars["设置"].waitForExistence(timeout: 5), "Settings did not open")
        let closeSettings = app.buttons["settings-close"].firstMatch
        XCTAssertTrue(closeSettings.waitForExistence(timeout: 5), "Settings has no close button")
        closeSettings.tapCenter()
        XCTAssertTrue(closeSettings.waitForNonExistence(timeout: 5), "Settings did not close")
        XCTAssertTrue(isOpen(app), "closing Settings did not come back to the drawer")
        close(app)
        ShellFinger(app).switchTo(agents: true, "Agents after closing Settings")
        ShellFinger(app).switchTo(agents: false, "Assistant after closing Settings")
        XCTAssertTrue(app.buttons["shell-assistant"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertFalse(sidebar(app).exists, "the drawer is still open after returning to Assistant")
    }

    /// Not a check: drags whose frames `DrawerProbe` measures (docs/operations/testing.md「Sidebar dragging」) — a slow drag open, a drag
    /// back, half way and let go (it springs back), and a quick flick open — in the long conversation.
    @MainActor func testMeasureDrags() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_DRAWER_MEASURE"] != nil else { throw XCTSkip("set PERSONA_DRAWER_MEASURE") }
        let app = launch(long)
        XCTAssertTrue(app.staticTexts["第 30 本也整理一下"].waitForExistence(timeout: 10))
        sleep(2)
        let rounds = Int(ProcessInfo.processInfo.environment["PERSONA_DRAWER_MEASURE"] ?? "") ?? 3
        for _ in 0..<rounds {
            drag(app, from: point(app, 0.2, 0.5), dx: 260, dy: 0, velocity: XCUIGestureVelocity(400), hold: 0.2)
            XCTAssertTrue(isOpen(app), "the slow drag did not open")
            sleep(1)
            drag(app, from: point(app, 0.9, 0.5), dx: -280, dy: 0, velocity: XCUIGestureVelocity(500), hold: 0.2)
            waitClosed(app, "the drag back did not close")
            sleep(1)
            drag(app, from: point(app, 0.3, 0.5), dx: 100, dy: 0, velocity: XCUIGestureVelocity(300), hold: 0.3)
            waitClosed(app, "half way did not spring back")
            sleep(1)
            drag(app, from: point(app, 0.3, 0.5), dx: 90, dy: 0, velocity: .fast)
            XCTAssertTrue(isOpen(app), "the flick did not open")
            sleep(1)
            close(app)
            sleep(1)
        }
    }
}
