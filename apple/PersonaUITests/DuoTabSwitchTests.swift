import XCTest

/// iPhone Duo in the Separate layout (#4231): one tab bar on both displays, the pages' symbols over their names
/// (Assistant, Agents, Messages), the chosen one's symbol on the Theme Color's circle, counts red at a symbol's corner:
/// - closed, the outer display: in the shell's rail down the system's column on the right, centred on the status bar, in
///   the column's lower middle;
/// - open on its side: the same in the system's column on the right, in the system's tab bar's place;
/// - open upright: along the window's foot, centred, the pages keeping its room as for the system's tab bar, so the
///   composer stands on it at the whole width.
/// The keyboard sends it away. Settings › Appearance › Sidebar switches between Merged (the one sidebar) and Separate at
/// once, and the choice is kept.
///
/// The posture is set before the run (scripts' display tool or Xcode's Device Hub; `XCUIDevice.orientation` does not
/// turn the open Duo): the test reads it from the window. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB).
/// TEST_RUNNER_PERSONA_HOLD=<seconds> holds each state, named in an activity `duo-shot <name>`, for
/// `simctl io --display=<1 outer, 3 inner> screenshot`.
final class DuoTabSwitchTests: XCTestCase {
    private enum Posture: String { case outer, side, upright }

    private var app: XCUIApplication!
    private var posture: Posture!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("iPhone Duo only") }
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        app = XCUIApplication()
        launch([])
        let agents = app.descendants(matching: .any)["shell-agents"].firstMatch
        let assistant = app.descendants(matching: .any)["shell-assistant"].firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 20) && assistant.exists, "no switch:\n\(app.debugDescription)")
        let window = app.windows.firstMatch.frame
        if window.width > 900 { posture = .side }
        else if window.width > 600 { posture = .upright }
        else if abs(assistant.frame.midX - agents.frame.midX) < 2, agents.frame.minY > assistant.frame.maxY - 2 { posture = .outer }
        else { throw XCTSkip("not an iPhone Duo: the switch runs across the top here") }
    }

    private func launch(_ extra: [String], layoutPinned: Bool = true) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] ?? ""
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant", "-dev.openChat", "",
                               "-integrations.lark.enabled", "YES"] + extra
        app.launchPinned(navigation: layoutPinned)
        dismissSystemAlerts()
    }

    /// A fresh simulator's notification prompt can come up over the app (and its tab bar) after an install.
    private func dismissSystemAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        guard springboard.alerts.firstMatch.waitForExistence(timeout: 3) else { return }
        for label in ["允许", "Allow", "好", "OK"] {
            let button = springboard.alerts.buttons[label].firstMatch
            if button.exists { button.tap(); return }
        }
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    @MainActor private func shot(_ name: String) {
        XCTContext.runActivity(named: "duo-shot \(posture.rawValue)-\(name)") { _ in
            if let hold = ProcessInfo.processInfo.environment["PERSONA_HOLD"].flatMap(UInt32.init) { sleep(hold) }
        }
    }

    /// One tab bar, the same on both displays: three items of a symbol over a name, each more than a tap target and named
    /// for VoiceOver (not by its symbol), the chosen one marked; down the system's column centred on the status bar in its
    /// lower middle, or along the foot upright; no system tab bar under it; the Agents count at its symbol's corner.
    @MainActor func testOneTabBarOfSymbolsAndNames() {
        let ids = ["shell-assistant", "shell-agents", "shell-feishu"]
        let segments = ids.map(element)
        XCTAssertTrue(segments[2].waitForExistence(timeout: 10), "no Messages segment:\n\(app.debugDescription)")
        for (id, s) in zip(ids, segments) {
            XCTAssertEqual(s.frame.width, 56, accuracy: 1.5, "\(id) is not an item wide: \(s.frame)")
            XCTAssertEqual(s.frame.height, 56, accuracy: 1.5, "\(id) is not an item tall: \(s.frame)")
            XCTAssertFalse(s.label.isEmpty || s.label.contains("sparkles") || s.label.contains("chevron") || s.label.contains("bubble"),
                           "\(id) has no name of its own: \(s.label)")
        }
        XCTAssertTrue(segments[0].isSelected, "Assistant is not the chosen one")
        let window = app.windows.firstMatch.frame
        if posture == .upright {
            // Along the foot, centred, side by side.
            for s in segments { XCTAssertEqual(s.frame.midY, segments[0].frame.midY, accuracy: 1, "not side by side: \(s.frame)") }
            XCTAssertLessThan(segments[0].frame.maxX, segments[1].frame.minX + 1, "Agents is not after Assistant")
            XCTAssertEqual(segments[1].frame.midX, window.midX, accuracy: 2, "the bar is not centred: \(segments[1].frame) in \(window)")
            XCTAssertGreaterThan(segments[1].frame.maxY, window.maxY - 120, "the bar is not at the foot: \(segments[1].frame) in \(window)")
        } else {
            // Down the system's column: centred on the status bar (49 pt from the edge on both displays), stacked, in the
            // column's lower middle rather than at its foot.
            for s in segments {
                XCTAssertEqual(s.frame.midX, window.maxX - 49, accuracy: 3, "not centred on the status bar: \(s.frame) in \(window)")
            }
            XCTAssertLessThan(segments[0].frame.maxY, segments[1].frame.minY + 1, "Agents is not under Assistant")
            XCTAssertLessThan(segments[1].frame.maxY, segments[2].frame.minY + 1, "Messages is not under Agents")
            XCTAssertGreaterThan(segments[1].frame.midY, window.height * 0.4, "the bar is not low in the column: \(segments[1].frame)")
            XCTAssertLessThan(segments[2].frame.maxY, window.maxY - window.height * 0.15, "the bar is a block at the foot: \(segments[2].frame)")
            // As high above the screen's foot folded and open, so it stays put as the Duo folds: the home indicator's 34 pt,
            // the face and the page's action under it on the outer display (their room on the open Duo, 144.5 pt at the
            // default text size, `TabSwitchLayout.sideFoot`) and the capsule's 8 pt under its last item.
            XCTAssertEqual(window.maxY - segments[2].frame.maxY, 186.7, accuracy: 1,
                           "the bar's foot is not where it is on the other display: \(segments[2].frame) in \(window)")
        }
        if posture != .outer {
            let narrow = NSPredicate { item, _ in ((item as? XCUIElementAttributes)?.frame.width ?? .infinity) <= 60 }
            let systemTab = app.buttons.matching(NSPredicate(format: "label == %@", "Persona")).matching(narrow).firstMatch
            XCTAssertFalse(systemTab.exists, "the system's tab bar is still there:\n\(app.debugDescription)")
        }
        // The Agents count (the mock hub has Agents waiting) is on its symbol's top trailing corner, as a tab bar badges.
        let agents = segments[1], count = element("shell-agents-count")
        XCTAssertTrue(count.waitForExistence(timeout: 10), "no Agents count on the tab bar:\n\(app.debugDescription)")
        XCTAssertTrue(count.frame.intersects(agents.frame), "the count is not on its item: \(count.frame) vs \(agents.frame)")
        XCTAssertGreaterThan(count.frame.midX, agents.frame.midX, "the count is not at the trailing corner: \(count.frame) vs \(agents.frame)")
        XCTAssertLessThan(count.frame.midY, agents.frame.midY, "the count is not at the top: \(count.frame) vs \(agents.frame)")
        shot("switch")
    }

    /// Each segment goes to its page at once; the chosen one shows it. (Not Agents on the outer display in Chinese: the
    /// Agents list's header can loop its layout there, #2352, which this switch does not touch.)
    @MainActor func testEachSegmentGoesToItsPage() {
        let assistant = element("shell-assistant"), agents = element("shell-agents"), messages = element("shell-feishu")
        XCTAssertTrue(messages.waitForExistence(timeout: 10), "no Messages segment:\n\(app.debugDescription)")
        messages.tap()
        XCTAssertTrue(element("feishu-conversations").waitForExistence(timeout: 10), "Messages did not open:\n\(app.debugDescription)")
        XCTAssertTrue(messages.isSelected, "Messages is not the chosen one")
        shot("messages")
        if posture != .outer {
            agents.tap()
            let chosen = NSPredicate { _, _ in agents.isSelected }
            XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: chosen, object: NSNull())], timeout: 10), .completed,
                           "Agents is not the chosen one:\n\(app.debugDescription)")
            // On its side the Agents' column is beside the page; upright it lies over the page and starts away.
            if posture == .side {
                XCTAssertTrue(element("sidebar-agents-new").waitForExistence(timeout: 10), "Agents did not open:\n\(app.debugDescription)")
            }
            shot("agents")
        }
        assistant.tap()
        let back = NSPredicate { _, _ in assistant.isSelected }
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: back, object: NSNull())], timeout: 10), .completed,
                       "Assistant did not come back:\n\(app.debugDescription)")
    }

    /// The keyboard sends the switch away, and it comes back with the keyboard down. Upright the composer leaves it room
    /// beside it, its foot level with the switch's, and takes the whole width back while typing.
    @MainActor func testTheKeyboardSendsTheSwitchAway() {
        let field = element("shell-composer-field"), agents = element("shell-agents"), messages = element("shell-feishu")
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no composer:\n\(app.debugDescription)")
        let composerEnd = { () -> CGFloat in
            ["shell-composer-send", "shell-composer-mic", "shell-composer-voice", "shell-composer-stop"].map(self.element)
                .filter { $0.exists }.map(\.frame.maxX).max() ?? field.frame.maxX
        }
        if posture == .upright {
            // The composer stands on the bar at the whole width, as on the system's tab bar.
            XCTAssertLessThanOrEqual(field.frame.maxY, agents.frame.minY, "the composer runs under the bar: \(field.frame) vs \(agents.frame)")
            XCTAssertGreaterThan(composerEnd(), agents.frame.maxX, "the composer does not take the whole width")
        }
        shot("keyboard-down")
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 10), "no keyboard")
        let gone = NSPredicate { _, _ in !agents.exists || !agents.isHittable }
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: gone, object: NSNull())], timeout: 5), .completed,
                       "the switch stayed over the keyboard:\n\(app.debugDescription)")
        sleep(1)
        shot("keyboard-up")
        let dismiss = element("shell-composer-dismiss-keyboard")
        if dismiss.exists { dismiss.tap() } else { app.keyboards.buttons.allElementsBoundByIndex.last?.tap() }
        XCTAssertTrue(agents.waitForExistence(timeout: 10), "the switch did not come back:\n\(app.debugDescription)")
        let back = NSPredicate { _, _ in agents.isHittable }
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: back, object: NSNull())], timeout: 5), .completed,
                       "the switch came back out of reach")
    }

    /// Settings › Appearance › Sidebar: Merged takes the switch away for the one sidebar at once, Separate brings it back
    /// at once, and the choice outlives a relaunch.
    @MainActor func testTheLayoutChoiceAppliesAtOnceAndIsKept() {
        func pick(_ layout: String) {
            app.terminate()
            launch(["-tab", "settings", "-openAppearance", "navigation"], layoutPinned: false)
            let choice = element("navigation-layout-\(layout)")
            XCTAssertTrue(choice.waitForExistence(timeout: 20), "no Sidebar page:\n\(app.debugDescription)")
            choice.tap()
            let chosen = NSPredicate { _, _ in choice.exists && choice.isSelected }
            XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: chosen, object: NSNull())], timeout: 10),
                           .completed, "\(layout) is not chosen, or Settings closed:\n\(app.debugDescription)")
            let close = app.buttons["settings-close"].firstMatch, back = app.navigationBars.buttons["BackButton"].firstMatch
            for _ in 0..<3 where !close.exists && back.exists {
                back.tap()
                _ = close.waitForExistence(timeout: 2)
            }
            if close.waitForExistence(timeout: 5) { close.tap() }
        }
        let agents = element("shell-agents")
        pick("sidebar")
        XCTAssertTrue(agents.waitForNonExistence(timeout: 10), "Merged kept the switch:\n\(app.debugDescription)")
        shot("merged")
        app.terminate()
        launch([], layoutPinned: false)
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 20))
        sleep(3)
        XCTAssertFalse(agents.exists, "Merged was not kept across a relaunch")
        pick("separate")
        XCTAssertTrue(agents.waitForExistence(timeout: 10), "Separate did not bring the switch back:\n\(app.debugDescription)")
        app.terminate()
        launch([], layoutPinned: false)
        XCTAssertTrue(agents.waitForExistence(timeout: 20), "Separate was not kept across a relaunch")
    }
}
