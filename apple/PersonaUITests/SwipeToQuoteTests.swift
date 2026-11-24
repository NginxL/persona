import XCTest

/// Agents chats: a message dragged to the left is quoted, as Telegram's reply (#1058). Past the threshold it lands in
/// the quote bar over the box and the keyboard comes up; a short drag or a vertical one (scrolling) does nothing. The
/// drag no longer pages to the files: the header's ··· menu opens them, and the edge swipe back still works.
///   PORT=18797 MOCK_AGENTS=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18797 xcodebuild test … -only-testing:PersonaUITests/SwipeToQuoteTests
/// Both styles (TEST_RUNNER_PERSONA_UI_STYLE=beautiful).
final class SwipeToQuoteTests: XCTestCase {
    private var app: XCUIApplication!
    private let answer = "PR #42 看完了"
    private let question = "看一下 PR #42"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 (PERSONA_MOCK_HUB)")
        }
    }

    @MainActor private func launch() {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000006"]
        app.launchPinned()
        dropKeptQuotes()
    }

    /// Quotes are kept with the chat across launches: an earlier test's (or run's) quote is dropped first.
    @MainActor private func dropKeptQuotes() {
        let bar = app.descendants(matching: .any).matching(identifier: "composer.quotes").firstMatch
        guard bar.waitForExistence(timeout: 3) else { return }
        for _ in 0..<5 where bar.exists { bar.buttons.firstMatch.tap(); usleep(400_000) }
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "swipe \(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    @MainActor private func text(_ message: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", message)).firstMatch
    }

    /// A message's centre in the window. Rows of the flipped chat list may report flipped frames, inside the window
    /// too: the question is above the answer on screen, so when the frames say otherwise they are mirrored back.
    @MainActor private func centre(of message: String) -> XCUICoordinate {
        XCTAssertTrue(text(message).waitForExistence(timeout: 15), "\(message) is not in the chat")
        XCTAssertTrue(text(message == answer ? question : answer).waitForExistence(timeout: 15))
        // The list settles at the newest message, and the header folds into its slim bar a few seconds in, moving the
        // list: a touch on a list still moving only stops it, so wait for both.
        let slim = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "显示标题和标签")).firstMatch
        let deadline = Date.now.addingTimeInterval(10)
        while !(slim.exists && slim.isHittable) && Date.now < deadline { usleep(200_000) }
        sleep(1)
        let window = app.windows.firstMatch.frame
        var f = text(message).frame
        let flipped = text(question).frame.minY > text(answer).frame.minY || !window.contains(CGPoint(x: f.midX, y: f.midY))
        if flipped { f.origin.y = window.height - f.maxY }
        return app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.midX, dy: f.midY))
    }

    @MainActor private func drag(_ message: String, by d: CGVector, hold: TimeInterval = 0.1) {
        let from = centre(of: message)
        from.press(forDuration: 0.05, thenDragTo: from.withOffset(d), withVelocity: .default, thenHoldForDuration: hold)
    }

    @MainActor private var quotes: XCUIElement { app.descendants(matching: .any).matching(identifier: "composer.quotes").firstMatch }
    @MainActor private var files: XCUIElement { app.descendants(matching: .any).matching(identifier: "files.pane").firstMatch }

    /// Far enough to the left, the answer is quoted over the box, the keyboard is up, and the chat stays on screen.
    @MainActor func testDragLeftQuotes() {
        launch()
        drag(answer, by: CGVector(dx: -160, dy: 0), hold: 0.6) // held past the threshold a moment: the icon armed
        let quoted = quotes.waitForExistence(timeout: 3)
        XCTAssertTrue(quoted, "a drag to the left did not quote the message")
        XCTAssertTrue(quotes.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "引用 Claude")).firstMatch.exists,
                      "the quote does not name whom it quotes")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 3), "the keyboard did not come up for the reply")
        XCTAssertFalse(files.exists, "the drag still pages to the files")
        shot("quoted")
    }

    /// Your own message is quoted the same way.
    @MainActor func testDragLeftQuotesMine() {
        launch()
        drag(question, by: CGVector(dx: -160, dy: 0))
        let quoted = quotes.waitForExistence(timeout: 3)
        XCTAssertTrue(quoted, "a drag to the left on my own message did not quote it")
        XCTAssertTrue(quotes.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "引用 你的消息")).firstMatch.exists)
    }

    /// Short of the threshold, letting go does nothing.
    @MainActor func testShortDragDoesNothing() {
        launch()
        drag(answer, by: CGVector(dx: -30, dy: 0))
        sleep(1)
        XCTAssertFalse(quotes.exists, "a short drag quoted the message")
        XCTAssertFalse(files.exists)
    }

    /// Dragged up or down (scrolling the list), a message is not quoted, even with a little sideways drift.
    @MainActor func testVerticalDragDoesNotQuote() {
        launch()
        drag(answer, by: CGVector(dx: -40, dy: 220))
        sleep(1)
        drag(question, by: CGVector(dx: -30, dy: -200))
        sleep(1)
        XCTAssertFalse(quotes.exists, "scrolling quoted a message")
    }

    /// Files are an item of the header's ··· menu (#1205; no button of their own, in the folded bar either: a tap on
    /// it opens the header); from them, Back to Chat there goes back.
    @MainActor func testMoreMenuOpensFiles() {
        launch()
        let folded = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch
        XCTAssertTrue(folded.waitForExistence(timeout: 15), "the chat did not open")
        sleep(6) // past the 3 s the header waits before folding, and the fold's animation
        XCTAssertFalse(app.buttons["chat.files"].exists, "the folder button is still in the header")
        shot("folded")
        let more = app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch
        if !more.exists { folded.tap() }
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no ··· in the header")
        XCTAssertFalse(app.buttons["chat.files"].exists, "the folder button is still in the header")
        sleep(1)
        shot("header")
        more.tap()
        app.openAgentMenuMore() // Files is More's (#13396); Back to Chat stays on the top level
        let item = app.buttons.matching(NSPredicate(format: "label == %@", "文件")).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no 文件 in the ··· menu")
        sleep(1)
        shot("menu")
        item.tap()
        XCTAssertTrue(files.waitForExistence(timeout: 5), "文件 did not open the files")
        sleep(1)
        shot("files")
        if !more.exists { folded.tap() }
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no ··· on the files")
        more.tap()
        let chat = app.buttons.matching(NSPredicate(format: "label == %@", "回到对话")).firstMatch
        XCTAssertTrue(chat.waitForExistence(timeout: 5), "no 回到对话 in the ··· menu on the files")
        chat.tap()
        XCTAssertTrue(files.waitForNonExistence(timeout: 5), "回到对话 did not go back to the chat")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", answer)).firstMatch.waitForExistence(timeout: 5))
    }

    /// The edge swipe back still leaves the chat for the list.
    @MainActor func testEdgeSwipeBack() {
        launch()
        let back = app.buttons["返回"]
        XCTAssertTrue(back.waitForExistence(timeout: 15), "the chat did not open")
        sleep(2)
        // From the very edge and quick, as a thumb does it (from 1 % in, slowly, the system takes it on master either).
        app.coordinate(withNormalizedOffset: CGVector(dx: 0, dy: 0.5))
            .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)),
                   withVelocity: .fast, thenHoldForDuration: 0)
        let deadline = Date.now.addingTimeInterval(5)
        while back.isHittable && Date.now < deadline { usleep(200_000) }
        XCTAssertFalse(back.isHittable, "still in the chat after the edge swipe")
        let list = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH %@", "agents.filter.")).firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 5), "not back on the Agents list")
    }
}
