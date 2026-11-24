import XCTest

/// The assistant's To Decide entry and its pager (#12831): the conversation's own card and its tasks' questions and
/// approvals are one card at a time over the composer, the entry first in the row of entries.
/// - order: the conversation's own card, then the tasks' oldest first; "1 / 5", ‹ ›, a sideways swipe;
/// - a tool approval and a question read off a terminal grey Skip and say why; the conversation's own question keeps Skip;
/// - Collapse folds the pager into the entry and the entry opens it on the same card;
/// - answering a card moves on to the next, the entry's count goes down;
/// - a tray of the action bar and the pager show one at a time.
/// Mock hub (any free port), TEST_RUNNER_PERSONA_DOCK_HUB for xcodebuild:
///   MOCK_AGENTS=1 MOCK_TASKS=1 MOCK_ASK_LONG=source MOCK_CARD_SOURCE=1 MOCK_DOCK=1
/// Screenshots: PERSONA_SHOTS=<dir>, PERSONA_SHOTS_DEVICE, PERSONA_SHOTS_EXTERNAL=1 as in QuestionCardFoldTests.
final class QuestionDockTests: XCTestCase {
    private var app: XCUIApplication!
    private static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func hub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_DOCK_HUB"] else { throw XCTSkip("needs PERSONA_DOCK_HUB (see the class comment)") }
        return hub
    }

    @MainActor private func launch(_ hub: String, scheme: String) {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-ui.scheme", scheme, "-ui.questionCardHeight", "",
                               "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", Self.main, "-app.navigation", "sidebar"]
        app.launchPinned()
    }

    @MainActor private func dismissIconAlert() {
        let ok = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons
            .matching(NSPredicate(format: "label IN %@", ["OK", "好"])).firstMatch
        if ok.waitForExistence(timeout: 3) { ok.tap() }
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone")
        let file = "qdock-\(device)-\(name).png"
        let folder = URL(fileURLWithPath: dir)
        guard env["PERSONA_SHOTS_EXTERNAL"] != nil else {
            try? app.screenshot().pngRepresentation.write(to: folder.appendingPathComponent(file))
            return
        }
        try? file.write(to: folder.appendingPathComponent("want"), atomically: true, encoding: .utf8)
        let target = folder.appendingPathComponent(file)
        let deadline = Date.now.addingTimeInterval(15)
        while Date.now < deadline, !FileManager.default.fileExists(atPath: target.path) { Thread.sleep(forTimeInterval: 0.2) }
    }

    @MainActor private func settle(_ seconds: TimeInterval = 1.2) { RunLoop.current.run(until: Date().addingTimeInterval(seconds)) }

    @MainActor private func text(_ prefix: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
    }

    @MainActor private var dock: XCUIElement { app.otherElements["question-dock"].firstMatch }
    @MainActor private var entry: XCUIElement { app.buttons["action-bar.to-decide"].firstMatch }

    /// To the first card: one that comes in after the pager showed never moves it (#12831), so a test starts from there.
    @MainActor private func goFirst() {
        let previous = app.buttons["question-dock.previous"]
        for _ in 0..<6 where previous.exists && previous.isEnabled { previous.tap(); settle(0.5) }
    }

    /// Waits for the pager to say `want` ("2 / 5").
    @MainActor private func expectPosition(_ want: String, _ step: String, file: StaticString = #filePath, line: UInt = #line) {
        let label = app.staticTexts["question-dock.position"].firstMatch
        var ok = label.waitForExistence(timeout: 5)
        let deadline = Date.now.addingTimeInterval(5)
        while ok, label.label != want, Date.now < deadline { settle(0.2) }
        ok = ok && label.label == want
        XCTAssertTrue(ok, "\(step): the pager says \(label.exists ? label.label : "nothing"), not \(want)", file: file, line: line)
    }

    /// The pager in order, light and dark: the conversation's own question (Skip available), the task's question, the
    /// task's tool approval and the question read off the blog's terminal (Skip greyed, with why), the
    /// network tests' build approval last; ‹ › and a swipe.
    @MainActor func testThePagerHoldsEveryCardInOrder() throws {
        let hub = try hub()
        continueAfterFailure = true
        for look in ["light", "dark"] {
            launch(hub, scheme: look)
            XCTAssertTrue(dock.waitForExistence(timeout: 20), "no pager (\(look))")
            dismissIconAlert()
            settle(2)
            goFirst()
            XCTAssertTrue(entry.exists, "no To Decide entry (\(look))")
            XCTAssertTrue(entry.label.contains("5"), "the entry says \(entry.label), not 5 (\(look))")
            expectPosition("1 / 5", "first (\(look))")
            XCTAssertTrue(text("构建卡在打包").exists, "the first card is not the conversation's own question (\(look))")
            XCTAssertTrue(dock.buttons["question-skip"].firstMatch.exists, "the conversation's own question lost its Skip (\(look))")
            XCTAssertFalse(app.scrollViews["pending-cards"].exists, "the old stack is still drawn under the entries (\(look))")
            XCTAssertFalse(app.buttons["question-dock.previous"].isEnabled, "‹ on the first card (\(look))")
            shot("own-\(look)")

            app.buttons["question-dock.next"].tap()
            expectPosition("2 / 5", "next (\(look))")
            XCTAssertTrue(text("网络层的测试用哪个框架").waitForExistence(timeout: 3), "the second card is not the task's question (\(look))")
            XCTAssertTrue(dock.staticTexts["补齐网络层的单元测试"].exists, "the task's question does not say its task (\(look))")
            shot("task-\(look)")

            app.buttons["question-dock.next"].tap()
            expectPosition("3 / 5", "permission (\(look))")
            XCTAssertTrue(dock.buttons["question-skip-unavailable"].exists, "the approval's Skip is not greyed (\(look))")
            XCTAssertFalse(dock.buttons["question-skip-unavailable"].isEnabled, "the approval's Skip can be tapped (\(look))")
            XCTAssertTrue(dock.staticTexts["interaction-no-skip"].exists, "the approval does not say why it can't be skipped (\(look))")
            shot("permission-\(look)")

            app.buttons["question-dock.next"].tap()
            expectPosition("4 / 5", "terminal (\(look))")
            XCTAssertTrue(text("旧文章的固定链接").waitForExistence(timeout: 3), "the last card is not the terminal's question (\(look))")
            XCTAssertTrue(dock.staticTexts["interaction-no-skip"].exists, "the terminal's question does not say why (\(look))")
            shot("terminal-\(look)")
            // The network tests' build approval (MOCK_AGENTS), the newest, last.
            app.buttons["question-dock.next"].tap()
            expectPosition("5 / 5", "last (\(look))")
            XCTAssertFalse(app.buttons["question-dock.next"].isEnabled, "› on the last card (\(look))")
            app.buttons["question-dock.previous"].tap()
            expectPosition("4 / 5", "back to the terminal (\(look))")

            // A sideways swipe goes back one.
            let card = dock.staticTexts["interaction-no-skip"].firstMatch
            // Left to right from a quarter of the card, on screen: from its left edge the phone's own edge swipe (Back, the
            // drawer) takes it.
            let width = app.windows.firstMatch.frame.maxX
            let from = card.coordinate(withNormalizedOffset: CGVector(dx: 0.25, dy: 0.5))
            let to = from.withOffset(CGVector(dx: min(card.frame.width * 0.6, width - card.frame.midX), dy: 0))
            from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .default, thenHoldForDuration: 0)
            // XCUITest's drags never reach the app on the open iPhone Duo simulator (as in QuestionCardFoldTests).
            let swiped = { self.expectPosition("3 / 5", "swiped back (\(look))") }
            if ProcessInfo.processInfo.environment["PERSONA_DRAGS_UNSUPPORTED"] != nil {
                XCTExpectFailure("XCUITest drags do not reach the app on the open iPhone Duo simulator", strict: false, failingBlock: swiped)
            } else {
                swiped()
            }
            app.terminate()
        }
    }

    /// Collapse folds the pager into the entry, which opens it on the same card; answering a card moves on to the next.
    @MainActor func testCollapseKeepsTheCardAndAnAnswerMovesOn() throws {
        let hub = try hub()
        launch(hub, scheme: "dark")
        XCTAssertTrue(dock.waitForExistence(timeout: 20), "no pager")
        dismissIconAlert()
        settle(2)
        goFirst()
        app.buttons["question-dock.next"].tap()
        expectPosition("2 / 5", "next")

        app.buttons["question-dock.collapse"].firstMatch.tap()
        settle()
        XCTAssertFalse(dock.exists, "collapsed, the pager is still drawn")
        XCTAssertTrue(entry.exists && entry.label.contains("5"), "collapsed, the entry says \(entry.label)")
        shot("collapsed-dark")
        entry.tap()
        XCTAssertTrue(dock.waitForExistence(timeout: 3), "the entry did not open the pager")
        expectPosition("2 / 5", "opened again")

        // Answer the task's question: the approval takes its place, four left.
        text("XCTest").tap()
        expectPosition("2 / 4", "answered")
        XCTAssertTrue(dock.buttons["question-skip-unavailable"].waitForExistence(timeout: 3), "after the answer, not on the approval")
        XCTAssertTrue(entry.label.contains("4"), "after the answer the entry says \(entry.label)")
        shot("answered-dark")
    }

    /// A tray of the action bar and the pager show one at a time; its row for the conversation's own card opens the
    /// pager on that card.
    @MainActor func testATrayAndThePagerShowOneAtATime() throws {
        let hub = try hub()
        launch(hub, scheme: "light")
        XCTAssertTrue(dock.waitForExistence(timeout: 20), "no pager")
        dismissIconAlert()
        settle(2)
        goFirst()
        app.buttons["question-dock.next"].tap()
        expectPosition("2 / 5", "next")
        app.buttons["action-bar.needs-you"].firstMatch.tap()
        settle()
        XCTAssertTrue(app.otherElements["action-tray.needs-you"].exists, "no Needs You tray")
        XCTAssertFalse(dock.exists, "the pager stays under the open tray")
        let ask = app.buttons["action-tray.ask"].firstMatch
        XCTAssertTrue(ask.exists, "no row for the conversation's own card")
        ask.tap()
        XCTAssertTrue(dock.waitForExistence(timeout: 3), "the row did not open the pager")
        expectPosition("1 / 5", "the row's card")
        XCTAssertFalse(app.otherElements["action-tray.needs-you"].exists, "the tray stayed open")
    }

    /// View in Chat on the conversation's own card collapses the pager and brings its message into view.
    @MainActor func testViewInChatCollapsesThePager() throws {
        let hub = try hub()
        launch(hub, scheme: "dark")
        XCTAssertTrue(dock.waitForExistence(timeout: 20), "no pager")
        dismissIconAlert()
        settle(2)
        goFirst()
        let source = dock.buttons["interaction-source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 5), "no View in Chat on the conversation's own card")
        source.tap()
        settle(1.5)
        XCTAssertFalse(dock.exists, "View in Chat did not collapse the pager")
        let message = text("看了构建记录")
        XCTAssertTrue(message.exists && message.isHittable, "View in Chat: the message is not in view")
        shot("own-source-dark")
    }

    /// Taps the pushed chat's Back: 「返回」 in the navigation bar, or on the open iPhone Duo the side rail's round one
    /// (`BackButton`, no label), by its point. False when there is none.
    @MainActor @discardableResult private func tapBack() -> Bool {
        let back = app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@ OR label == %@", "BackButton", "返回", "Back")).firstMatch
        guard back.waitForExistence(timeout: 5) else { return false }
        back.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).tap()
        return true
    }

    /// View in Chat on a task's card (#12625): the Agent's chat is pushed over the conversation at the message that asked
    /// (by its tool use id), and Back shows the pager on the same card; a card read off a terminal opens its chat too.
    @MainActor func testViewInChatOnATasksCardOpensItsAgent() throws {
        let hub = try hub()
        launch(hub, scheme: "light")
        XCTAssertTrue(dock.waitForExistence(timeout: 20), "no pager")
        dismissIconAlert()
        settle(2)
        goFirst()
        app.buttons["question-dock.next"].tap()
        expectPosition("2 / 5", "the task's question")
        let source = dock.buttons["interaction-source"].firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 3), "no View in Chat on the task's question")
        source.tap()
        let message = text("开始写测试前想确认")
        XCTAssertTrue(message.waitForExistence(timeout: 15), "the Agent's chat did not open at the message that asked")
        settle(1.5)
        XCTAssertTrue(message.isHittable, "the message that asked is not in view (\(message.frame))")
        shot("task-source-light")
        XCTAssertTrue(tapBack(), "no Back on the Agent's chat")
        XCTAssertTrue(dock.waitForExistence(timeout: 5), "Back did not come back to the pager")
        expectPosition("2 / 5", "back on the same card")

        app.buttons["question-dock.next"].tap()
        app.buttons["question-dock.next"].tap()
        expectPosition("4 / 5", "the terminal's question")
        dock.buttons["interaction-source"].firstMatch.tap()
        settle(2.5)
        XCTAssertFalse(dock.exists, "the blog's chat did not open over the pager")
        shot("terminal-source-light")
        XCTAssertTrue(tapBack(), "no Back on the blog's chat")
        XCTAssertTrue(dock.waitForExistence(timeout: 5), "Back did not come back to the pager")
        expectPosition("4 / 5", "back on the terminal's card")
    }
}
