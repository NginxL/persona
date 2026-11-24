import XCTest

/// A question card over the composer never takes the conversation away (#9392). In the assistant's conversation the cards
/// are one pager behind To Decide since #12831 (`QuestionDockTests`): its fold collapses the pager into the entry. On iPad and the open iPhone Duo a task's
/// question in Persona's conversation took the whole page: its form had no cap, the conversation got no room (nothing
/// to scroll) and the card's top, its fold with it, sat under the page's top bar. Now:
/// - the card's title row and fold are on the page, under the top bar, with conversation over it;
/// - the conversation scrolls with the card open, and folded;
/// - the card folds to one line (a question of several says how many are left, and keeps its page) and unfolds;
/// - View in Chat brings the message the card is about into view and folds the card;
/// - Skip is in the card's title row, with no row of its own under the options (#12306).
/// Mock hubs (any free ports), each named by its environment variable (TEST_RUNNER_ prefix for xcodebuild):
///   PERSONA_CARD_HUB:  MOCK_AGENTS=1 MOCK_ASK_LONG=source  MOCK_CARD_SOURCE=1
///   PERSONA_CARD2_HUB: MOCK_AGENTS=1 MOCK_ASK_LONG=source2 MOCK_CARD_SOURCE=1
///   PERSONA_TASK_HUB:  MOCK_AGENTS=1 MOCK_TASKS=1 MOCK_CARD_SOURCE=1
///   PERSONA_BOTH_HUB:  MOCK_AGENTS=1 MOCK_TASKS=1 MOCK_ASK_LONG=source MOCK_CARD_SOURCE=1
///   PERSONA_BOTH_LONG_HUB: MOCK_AGENTS=1 MOCK_TASKS=1 MOCK_ASK_LONG=source MOCK_CARD_SOURCE=1 MOCK_TASK_ASK_LONG=1
/// The grabber (#12306): the grabber on the question's top edge
/// makes it taller and shorter by dragging, the tallest and back by a tap, the choice kept after a relaunch.
/// Screenshots: PERSONA_SHOTS=<dir> (PERSONA_SHOTS_DEVICE names the device in the file names; PERSONA_SHOTS_EXTERNAL=1
/// asks the host for them, as the open Duo's own come out black: /tmp's shot watcher, `simctl io … screenshot`).
final class QuestionCardFoldTests: XCTestCase {
    private var app: XCUIApplication!
    private static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private static let tests = "macbook/a1f3c2d4-0000-4000-8000-000000000002"
    /// The conversation a lone card leaves over itself, under the top bar (`Theme.Size.cardThreadRoom`, less a little for
    /// rounding and the list's hairline).
    private static let threadRoom: CGFloat = 60
    /// A folded card: its header row and margins, at most.
    private static let foldedAtMost: CGFloat = 110

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private func hub(_ name: String) throws -> String {
        guard let hub = ProcessInfo.processInfo.environment[name] else { throw XCTSkip("needs \(name) (see the class comment)") }
        return hub
    }

    /// `keepHeight`: the card height the user chose last stays (`CardHeight`); otherwise each launch starts from the default.
    @MainActor private func launch(_ hub: String, chat: String, scheme: String, keepHeight: Bool = false) {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-ui.scheme", scheme]
        if !keepHeight { app.launchArguments += ["-ui.questionCardHeight", ""] }
        if chat == Self.main {
            // The Sidebar layout, as the report's (iPad and the open Duo).
            app.launchArguments += ["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", chat, "-app.navigation", "sidebar"]
        } else {
            app.launchArguments += ["-app.tab", "chats", "-dev.openChat", chat, "-tab", "dev", "-openAgentsFresh", "YES"]
        }
        app.launchPinned()
    }

    /// 「你已更改“Persona”的图标」 after `-ui.scheme`: it covers the card and takes the taps.
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
        let file = "qcard-\(device)-\(name).png"
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

    /// The lowest edge of what the main conversation's top bar draws over the page (its face and buttons over the chat
    /// column); an Agents chat's header otherwise. `left`: where the chat's column starts.
    @MainActor private func barBottom(left: CGFloat) -> CGFloat {
        let ids = ["persona-avatar", "assistant-sidebar-button", "assistant-new", "chat.title", "chat.compactTitle"]
        let frames = ids.flatMap { id in app.descendants(matching: .any).matching(identifier: id).allElementsBoundByIndex.map(\.frame) }
            .filter { !$0.isEmpty && $0.minX >= left - 1 && $0.minY < app.windows.firstMatch.frame.height / 3 }
        let back = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex.map(\.frame)
            .filter { !$0.isEmpty && $0.minX >= left - 1 }
        return (frames + back).map(\.maxY).max() ?? 0
    }

    /// The card's title row and fold are on the page under the top bar, with the conversation's room over the card.
    /// The card's fold: an Agent's card folds to one line, the assistant's pager collapses into To Decide (#12831).
    @MainActor private func foldControl(_ card: XCUIElement) -> XCUIElement {
        let collapse = card.buttons["question-dock.collapse"].firstMatch
        return collapse.exists ? collapse : card.buttons["interaction-fold"].firstMatch
    }

    @MainActor private var dock: XCUIElement { app.otherElements["question-dock"].firstMatch }
    @MainActor private var entry: XCUIElement { app.buttons["action-bar.to-decide"].firstMatch }

    /// The pager on the task's question, its second card: the conversation's own first (#12831). A card that comes in
    /// later never moves the pager, so it goes to the first and then one on.
    @MainActor private func showTasksQuestion() {
        let previous = app.buttons["question-dock.previous"]
        for _ in 0..<5 where previous.exists && previous.isEnabled { previous.tap(); settle(0.5) }
        app.buttons["question-dock.next"].tap()
        settle()
    }

    @MainActor private func assertInPage(_ card: XCUIElement, _ step: String, file: StaticString = #filePath, line: UInt = #line) {
        let fold = foldControl(card)
        XCTAssertTrue(fold.exists && fold.isHittable, "\(step): the card's fold is not on screen", file: file, line: line)
        let bar = barBottom(left: card.frame.minX)
        XCTAssertGreaterThanOrEqual(fold.frame.minY, bar, "\(step): the fold (\(fold.frame)) is under the top bar (\(bar))", file: file, line: line)
        XCTAssertGreaterThanOrEqual(card.frame.minY - bar, Self.threadRoom,
                                    "\(step): the card (\(card.frame)) leaves the conversation \(card.frame.minY - bar) pt under the bar (\(bar))",
                                    file: file, line: line)
    }

    /// The texts drawn in the conversation between `top` and `bottom` in the card's column, with where they are.
    @MainActor private func strip(_ top: CGFloat, _ bottom: CGFloat, _ card: XCUIElement) -> [String] {
        let column = card.frame.minX - 1...card.frame.maxX + 1
        return app.staticTexts.allElementsBoundByIndex.compactMap { t -> String? in
            let f = t.frame
            guard !f.isEmpty, column.contains(f.midX), f.maxY > top, f.minY < bottom, !card.frame.contains(CGPoint(x: f.midX, y: f.midY)) else { return nil }
            return "\(t.label.prefix(12))@\(Int(f.minY))"
        }
    }

    /// A drag over the conversation, between the top bar and the card, moves its messages: the list scrolls.
    @MainActor private func assertConversationScrolls(above card: XCUIElement, _ step: String,
                                                       file: StaticString = #filePath, line: UInt = #line) {
        let bar = barBottom(left: card.frame.minX)
        let x = card.frame.midX
        let top = bar + 6, bottom = card.frame.minY - 6
        guard bottom - top > 20 else { return XCTFail("\(step): no conversation over the card (\(top)…\(bottom))", file: file, line: line) }
        let before = strip(top, bottom, card)
        let start = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: x, dy: top))
        let end = app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: x, dy: bottom))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.3)
        settle(0.8)
        var after = strip(top, bottom, card)
        // Already at its start (a tall iPad, the card short: an earlier drag got there), the list moves the other way.
        if after == before {
            end.press(forDuration: 0.05, thenDragTo: start, withVelocity: .slow, thenHoldForDuration: 0.3)
            settle(0.8)
            after = strip(top, bottom, card)
        }
        let moved = { XCTAssertNotEqual(before, after, "\(step): a drag over the conversation (\(Int(top))…\(Int(bottom))) did not move it",
                                        file: file, line: line) }
        // XCUITest's drags never reach the chat's list on the open iPhone Duo simulator, card or not (seen before #9392,
        // the app responsive): there the room over the card is checked (`assertInPage`) and the drag on iPad and iPhone.
        if ProcessInfo.processInfo.environment["PERSONA_DRAGS_UNSUPPORTED"] != nil {
            XCTExpectFailure("XCUITest drags do not scroll the chat on the open iPhone Duo simulator", strict: false, failingBlock: moved)
        } else {
            moved()
        }
    }

    /// The report's case: a task's question in Persona's conversation, light and dark. It is capped under the top bar with
    /// conversation over it, the conversation scrolls, and it collapses into To Decide and back (#12831).
    @MainActor func testATasksQuestionKeepsTheConversation() throws {
        let hub = try hub("PERSONA_TASK_HUB")
        continueAfterFailure = true
        for look in ["light", "dark"] {
            launch(hub, chat: Self.main, scheme: look)
            let card = dock
            XCTAssertTrue(card.waitForExistence(timeout: 20), "no task question (\(look))")
            dismissIconAlert()
            settle(2)
            shot("task-open-\(look)")
            assertInPage(card, "task, open (\(look))")
            assertConversationScrolls(above: card, "task, open (\(look))")
            foldControl(card).tap()
            settle()
            XCTAssertFalse(card.exists, "collapsed, the pager is still drawn (\(look))")
            XCTAssertFalse(text("Swift Testing").isHittable, "collapsed, its options still show (\(look))")
            shot("task-folded-\(look)")
            entry.tap()
            settle()
            XCTAssertTrue(text("Swift Testing").waitForExistence(timeout: 3) && text("Swift Testing").isHittable, "opened, no options (\(look))")
            app.terminate()
        }
    }

    /// Persona's own question alone, light and dark: on the page under the top bar, the conversation scrolls, it collapses
    /// into To Decide and opens again, and View in Chat brings the message it is about into view and collapses it.
    @MainActor func testTheMainQuestionFoldsAndLeadsBackToItsMessage() throws {
        let hub = try hub("PERSONA_CARD_HUB")
        continueAfterFailure = true
        for look in ["light", "dark"] {
            launch(hub, chat: Self.main, scheme: look)
            let cards = dock
            XCTAssertTrue(cards.waitForExistence(timeout: 20), "no card (\(look))")
            dismissIconAlert()
            settle(2)
            shot("main-open-\(look)")
            assertInPage(cards, "main, open (\(look))")
            assertSkipInTitleRow(cards, "main, open (\(look))")
            assertConversationScrolls(above: cards, "main, open (\(look))")

            foldControl(cards).tap()
            settle()
            XCTAssertFalse(cards.exists, "collapsed, the pager is still drawn (\(look))")
            XCTAssertFalse(text("拿到任务编号再查").isHittable, "collapsed, its options still show (\(look))")
            shot("main-folded-\(look)")

            entry.tap()
            settle()
            XCTAssertTrue(text("拿到任务编号再查").isHittable, "opened, no options (\(look))")

            // View in Chat: the message the card is about, in view over the entries.
            let source = cards.buttons["interaction-source"].firstMatch
            XCTAssertTrue(source.exists && source.isHittable, "no View in Chat (\(look))")
            // Away from it first, so the jump has somewhere to go.
            assertConversationScrolls(above: cards, "main, before View in Chat (\(look))")
            source.tap()
            settle(1.5)
            XCTAssertFalse(cards.exists, "View in Chat did not collapse the pager (\(look))")
            let message = text("看了构建记录")
            XCTAssertTrue(message.exists, "View in Chat: the message is not drawn (\(look))")
            XCTAssertTrue(message.frame.minY >= barBottom(left: entry.frame.minX) - 1 && message.frame.minY < entry.frame.minY,
                          "View in Chat: the message (\(message.frame)) is not in view over the entries (\(entry.frame)) (\(look))")
            shot("main-source-\(look)")
            app.terminate()
        }
    }

    /// A question of several in the assistant's pager: collapsing it keeps the card's page and picks (#12831, as a folded
    /// card does, #9392).
    @MainActor func testAQuestionOfSeveralKeepsItsPageWhenCollapsed() throws {
        let hub = try hub("PERSONA_CARD2_HUB")
        launch(hub, chat: Self.main, scheme: "dark")
        let cards = dock
        XCTAssertTrue(cards.waitForExistence(timeout: 20), "no card")
        dismissIconAlert()
        settle(2)
        assertInPage(cards, "several, open")
        text("拿到任务编号再查").tap()
        XCTAssertTrue(text("查完把结论发到哪里").waitForExistence(timeout: 5), "a pick did not go on to the second question")
        foldControl(cards).tap()
        settle()
        XCTAssertFalse(cards.exists, "collapsed, the pager is still drawn")
        shot("several-folded-dark")
        entry.tap()
        settle()
        XCTAssertTrue(text("查完把结论发到哪里").isHittable, "opened again, it went back to the first question")
    }

    /// An Agent's question in its own chat: on the page, folds, and View in Chat brings its message into view.
    @MainActor func testAnAgentsQuestionLeadsBackToItsMessage() throws {
        let hub = try hub("PERSONA_CARD_HUB")
        launch(hub, chat: Self.tests, scheme: "dark")
        let cards = app.scrollViews["pending-cards"]
        XCTAssertTrue(cards.waitForExistence(timeout: 20), "no card")
        dismissIconAlert()
        settle(2)
        shot("agent-open-dark")
        let fold = cards.buttons["interaction-fold"].firstMatch
        XCTAssertTrue(fold.exists && fold.isHittable, "the card's fold is not on screen")
        let source = cards.buttons["interaction-source"].firstMatch
        XCTAssertTrue(source.exists && source.isHittable, "no View in Chat")
        source.tap()
        settle(1.5)
        XCTAssertFalse(text("Swift Testing").isHittable, "View in Chat did not fold the card")
        let message = text("开始写测试前想确认")
        XCTAssertTrue(message.exists && message.frame.maxY <= cards.frame.minY + 1 && message.frame.minY >= 0,
                      "View in Chat: the message (\(message.frame)) is not in view over the card (\(cards.frame))")
        shot("agent-source-dark")
    }

    /// Persona's own question and its tasks' at once: one pager, the conversation's own first (#12831), on the page under
    /// the top bar with the conversation's room over it.
    @MainActor func testATasksQuestionAndTheConversationsShareThePager() throws {
        let hub = try hub("PERSONA_BOTH_HUB")
        launch(hub, chat: Self.main, scheme: "dark")
        XCTAssertTrue(dock.waitForExistence(timeout: 20), "no pager")
        dismissIconAlert()
        settle(2)
        shot("both-dark")
        // The conversation's own question first, then the tasks' (the network tests' question and its build approval).
        XCTAssertTrue(app.staticTexts["question-dock.position"].firstMatch.label.hasSuffix("/ 3"), "not one pager of three")
        XCTAssertFalse(app.scrollViews["pending-cards"].exists, "the conversation's card is still in the stack")
        assertInPage(dock, "both: the pager")
        showTasksQuestion()
        XCTAssertTrue(text("Swift Testing").waitForExistence(timeout: 3), "the second page is not the task's question")
        assertInPage(dock, "both: the task's")
    }

    /// The grabber (#12306), light and dark, beside the conversation's folded card as in the report: down makes the task's
    /// question shorter, up taller (past its default where the default cuts it), a tap gives it the tallest (the conversation keeps a sliver, the fold on the page)
    /// and another the default; option descriptions wrap whole; a height chosen by hand stays after a relaunch.
    @MainActor func testTheGrabberSizesTheQuestion() throws {
        let hub = try hub("PERSONA_BOTH_LONG_HUB")
        continueAfterFailure = true
        for look in ["light", "dark"] {
            launch(hub, chat: Self.main, scheme: look)
            let task = dock
            XCTAssertTrue(task.waitForExistence(timeout: 20), "no pager (\(look))")
            dismissIconAlert()
            settle(2)
            showTasksQuestion()
            let grabber = task.descendants(matching: .any)["interaction-grabber"].firstMatch
            XCTAssertTrue(grabber.exists && grabber.isHittable, "no grabber on the task's question (\(look))")
            let start = task.frame.height
            // On a tall page (iPad) the question shows whole: taller has nothing more to show, shorter still works.
            let whole = showsWhole(task)
            shot("grab-default-\(look)")

            drag(grabber, by: 100)
            let shorter = task.frame.height
            XCTAssertLessThan(shorter, start - 30, "dragged down, the question is \(shorter) pt (was \(start)) (\(look))")

            drag(grabber, by: -180)
            let taller = task.frame.height
            XCTAssertGreaterThan(taller, shorter + 40, "dragged up, the question is \(taller) pt (was \(shorter)) (\(look))")
            if !whole { XCTAssertGreaterThan(taller, start + 20, "dragged up, the question is no taller than its default \(start) pt (\(look))") }
            assertOnPage(task, "dragged up (\(look))")

            // Off the tallest first: from there a tap goes back to the default, not to the tallest.
            drag(grabber, by: 60)
            grabber.tap()
            settle()
            let tallest = task.frame.height
            XCTAssertGreaterThanOrEqual(tallest, taller - 1, "tapped, the question is \(tallest) pt, not the tallest (\(look))")
            assertOnPage(task, "the tallest (\(look))")
            let description = text("新代码统一用 @Test")
            XCTAssertTrue(description.exists && description.label.hasSuffix("再看"), "the first description is cut: \(description.label) (\(look))")
            XCTAssertTrue(task.frame.contains(description.frame), "at the tallest the first description (\(description.frame)) is not whole in the card (\(task.frame)) (\(look))")
            XCTAssertGreaterThan(description.frame.height, 24, "the first description is one line (\(description.frame)) (\(look))")
            shot("grab-tallest-\(look)")

            grabber.tap()
            settle()
            XCTAssertEqual(task.frame.height, start, accuracy: 3, "tapped again, the question did not go back to the default (\(look))")

            // A height by hand stays after a relaunch.
            drag(grabber, by: 90)
            let chosen = task.frame.height
            app.terminate()
            launch(hub, chat: Self.main, scheme: look, keepHeight: true)
            XCTAssertTrue(task.waitForExistence(timeout: 20), "no pager, relaunched (\(look))")
            dismissIconAlert()
            settle(2)
            showTasksQuestion()
            XCTAssertEqual(task.frame.height, chosen, accuracy: 4, "relaunched, the question is not the height chosen (\(look))")
            app.terminate()
        }
    }

    /// The task's question shows its last option whole: nothing of it is cut.
    @MainActor private func showsWhole(_ task: XCUIElement) -> Bool {
        let last = text("等这一版发布以后再补")
        return last.exists && task.frame.contains(last.frame)
    }

    /// Skip is a word in the card's title row, beside its fold (#12306), still a finger's size, and nothing is left under
    /// the options but the card's margin: Skip had a row of its own there, about 60 pt for one word.
    @MainActor private func assertSkipInTitleRow(_ card: XCUIElement, _ step: String, file: StaticString = #filePath, line: UInt = #line) {
        let skip = card.buttons["question-skip"].firstMatch
        let fold = foldControl(card)
        XCTAssertTrue(skip.exists && skip.isHittable, "\(step): no Skip", file: file, line: line)
        XCTAssertEqual(skip.frame.midY, fold.frame.midY, accuracy: 4, "\(step): Skip (\(skip.frame)) is not in the title row (\(fold.frame))",
                       file: file, line: line)
        XCTAssertGreaterThanOrEqual(skip.frame.height, 44, "\(step): Skip is \(skip.frame.height) pt tall to a finger", file: file, line: line)
        let other = card.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "其他回答")).firstMatch
        XCTAssertTrue(other.exists, "\(step): no Other row", file: file, line: line)
        XCTAssertLessThan(card.frame.maxY - other.frame.maxY, 44,
                          "\(step): \(card.frame.maxY - other.frame.maxY) pt under the Other row (\(other.frame)) to the card's end (\(card.frame))",
                          file: file, line: line)
    }

    /// Drags the grabber by `dy` (up is negative) and lets the card settle.
    @MainActor private func drag(_ grabber: XCUIElement, by dy: CGFloat) {
        let from = grabber.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
        from.press(forDuration: 0.1, thenDragTo: from.withOffset(CGVector(dx: 0, dy: dy)), withVelocity: .slow, thenHoldForDuration: 0.2)
        settle()
    }

    /// The card's grabber and fold are on the page under the top bar: made taller by hand the conversation may keep only a
    /// sliver over it, never the card's top under the bar.
    @MainActor private func assertOnPage(_ card: XCUIElement, _ step: String, file: StaticString = #filePath, line: UInt = #line) {
        let fold = foldControl(card)
        let bar = barBottom(left: card.frame.minX)
        XCTAssertTrue(fold.exists && fold.isHittable, "\(step): the card's fold is not on screen", file: file, line: line)
        XCTAssertGreaterThanOrEqual(card.frame.minY, bar, "\(step): the card (\(card.frame)) is under the top bar (\(bar))", file: file, line: line)
    }
}
