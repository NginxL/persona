import XCTest

/// An agent's pending cards above the composer (#926). One is laid out as it always was. Three (a plan, a question and
/// a permission: more than half the page) scroll on their own under 「3 个待处理」, the permission first and in sight,
/// while the chat's header and back button stay on screen: they were pushed off the top, out of reach.
///   PORT=18797 MOCK_AGENTS=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18797 xcodebuild test … -only-testing:PersonaUITests/PendingCardsTests
/// Both styles (TEST_RUNNER_PERSONA_UI_STYLE=beautiful).
final class PendingCardsTests: XCTestCase {
    private var app: XCUIApplication!
    /// The mock's agent with three cards waiting, and one with none.
    private static let photos = "studio/a1f3c2d4-0000-4000-8000-000000000005"
    private static let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private static let askLong = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 (PERSONA_MOCK_HUB)")
        }
    }

    private func launch(chat: String, _ extra: [String] = []) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", chat, "-tab", "dev",
                               "-openAgentsFresh", "YES", "-openChatsFresh", "YES"] + extra
        app.launchPinned()
    }

    private func launchMainQuestion(_ scheme: String? = nil, hubKey: String = "PERSONA_MOCK_HUB", largestText: Bool = false,
                                    extra: [String] = []) {
        let hub = ProcessInfo.processInfo.environment[hubKey]!
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant",
                               "-openAssistant", Self.askLong, "-openChatsFresh", "YES"]
        if let scheme { app.launchArguments += ["-ui.scheme", scheme] }
        if largestText { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchArguments += extra
        app.launchPinned()
    }

    private func resolutionCount(_ id: String, hub: String? = nil) -> Int {
        let hub = hub ?? ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/resolved")!),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rows = obj["resolved"] as? [[String: Any]] else { return 0 }
        return rows.filter { $0["id"] as? String == id }.count
    }

    private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "cards \(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        // TEST_RUNNER_PERSONA_SHOTS keeps them as files too, named by TEST_RUNNER_PERSONA_SHOT_TAG (layout, look, device).
        let env = ProcessInfo.processInfo.environment
        if let dir = env["PERSONA_SHOTS"] {
            let tag = env["PERSONA_SHOT_TAG"].map { "-" + $0 } ?? ""
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "cards-\(name)-\(UITestStyle.name)\(tag).png"))
        }
    }

    /// The chat's back button, on screen and in reach.
    private func assertBackInReach(file: StaticString = #filePath, line: UInt = #line) {
        let backs = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex
        XCTAssertTrue(backs.contains { $0.isHittable && $0.frame.minY >= 0 }, "the back button is on screen", file: file, line: line)
    }

    func testOneCardAsBefore() {
        launch(chat: Self.widget, ["-showSamplePermission", "YES"])
        XCTAssertTrue(app.buttons["work-head"].firstMatch.waitForExistence(timeout: 15), "the chat")
        sleep(2)
        XCTAssertFalse(app.staticTexts["pending-cards-count"].exists, "one card fits: no count over it")
        // Laid out whole: nothing of the card outside its area (it was cut off at the bottom once).
        let cards = app.scrollViews["pending-cards"]
        XCTAssertTrue(cards.exists, "the cards' area")
        let area = cards.frame.insetBy(dx: -1, dy: -1)
        let outside = cards.descendants(matching: .any).allElementsBoundByIndex.filter { !$0.frame.isEmpty && !area.contains($0.frame) }
        XCTAssertTrue(outside.isEmpty, "cut off: \(outside.map { $0.label }) outside \(cards.frame)")
        assertBackInReach()
        shot("one")
    }

    /// The keyboard up to answer (#1912): the question card is whole above the composer, or scrolls in all the room the
    /// conversation had, so every option and 「其他回答」 can be reached and tapped. It was held to half of the short page
    /// the keyboard leaves: its first two options, cut off.
    func testKeyboardKeepsTheQuestionInReach() {
        launch(chat: Self.widget, ["-showSampleQuestion", "YES"])
        XCTAssertTrue(app.buttons["work-head"].firstMatch.waitForExistence(timeout: 15), "the chat")
        sleep(2)
        shot("question")
        // The composer: 「给 … 发消息」, or 「补充说明（会排队发送）」 while the agent works.
        let field = app.descendants(matching: .any).matching(NSPredicate(format: "placeholderValue BEGINSWITH %@ OR placeholderValue BEGINSWITH %@", "给", "补充说明")).firstMatch
        XCTAssertTrue(field.exists, "the composer")
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the keyboard did not come up")
        sleep(1)
        shot("question-keyboard")
        let cards = app.scrollViews["pending-cards"]
        XCTAssertTrue(cards.exists, "the cards' area")
        XCTAssertLessThanOrEqual(cards.frame.maxY, field.frame.minY + 1, "the cards end above the composer")
        // The composer whole above the keyboard: its field and its buttons can be tapped (the keyboard's frame in XCUITest
        // starts under its suggestion bar, so a frame check misses a composer half under it).
        let keyboardTop = app.keyboards.firstMatch.frame.minY
        XCTAssertLessThanOrEqual(field.frame.maxY, keyboardTop + 1, "the composer is above the keyboard (\(field.frame) over \(keyboardTop))")
        let end = app.buttons.matching(NSPredicate(format: "label IN %@", ["中断", "发送"])).firstMatch
        if end.exists {
            XCTAssertTrue(end.isHittable, "the composer's \(end.label) button is under the keyboard (\(end.frame), keyboard from \(keyboardTop))")
        }
        // Whole, or scrolling in the room up to the chat's header (its back button): not half of what the keyboard left.
        let head = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex.filter { $0.isHittable }.map { $0.frame.maxY }.max() ?? 0
        let count = app.staticTexts["pending-cards-count"]
        if count.exists { // before the fix: 120 pt on both iPhones
            XCTAssertLessThan(count.frame.minY - head, 80, "the cards scroll in a strip \(cards.frame.height) pt high, under \(count.frame.minY - head) pt of conversation")
        }
        XCTAssertLessThan(cards.frame.minY - head, 80, "the card is \(cards.frame.height) pt high under \(cards.frame.minY - head) pt of conversation")
        // Down to the composer: nothing between the card and the composer but the plan's pill, there while the chat's
        // newest plan is out of view (#1092).
        var above = (name: "the card", frame: cards.frame)
        let pill = app.buttons["chat.plan-pill"]
        if pill.exists && pill.frame.minY >= cards.frame.maxY - 1 && pill.frame.maxY <= field.frame.minY + 1 {
            XCTAssertLessThan(pill.frame.minY - cards.frame.maxY, 40, "a gap between the card (\(cards.frame)) and the plan's pill (\(pill.frame))")
            above = ("the plan's pill", pill.frame)
        }
        XCTAssertLessThan(field.frame.minY - above.frame.maxY, 40, "a gap between \(above.name) (\(above.frame)) and the composer (\(field.frame))")
        // The first option shows at once (a small iPhone: before, the question alone; the card's header row steps aside).
        let first = cards.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "取最长的那段")).firstMatch
        XCTAssertTrue(first.exists && first.frame.minY < cards.frame.maxY - 16 * 3,
                      "the first option does not show (\(first.exists ? "\(first.frame)" : "not drawn") in \(cards.frame))")
        // Every option and 「其他回答」 can be brought into the area and tapped there.
        // Short drags inside the card's form, held at the end (no momentum), whichever way the option is: a swipe threw a
        // form as short as a small iPhone's past it, and one that starts on the card's header row does not scroll.
        let area = cards.frame.insetBy(dx: -1, dy: -1)
        let title = cards.staticTexts["Claude 在问你"].firstMatch
        let top = (title.exists ? title.frame.maxY : cards.frame.minY) + 16, bottom = cards.frame.maxY - 16 * 2
        let origin = app.coordinate(withNormalizedOffset: .zero)
        let start = origin.withOffset(CGVector(dx: cards.frame.midX, dy: (top + bottom) / 2))
        let step = max(24, min(48, (bottom - top) / 3))
        for label in ["取最长的那段", "永远取最后一段", "永远取唤醒前那段", "其他回答"] {
            // 「其他回答」 is a field in BeautifulUI's card (its placeholder, no label).
            let el = cards.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@ OR placeholderValue BEGINSWITH %@", label, label)).firstMatch
            for _ in 0..<20 where !(el.exists && el.isHittable && area.contains(el.frame)) {
                let dy = el.exists && el.frame.minY < top ? step : -step // not drawn yet: further down
                start.press(forDuration: 0.1, thenDragTo: start.withOffset(CGVector(dx: 0, dy: dy)), withVelocity: .slow, thenHoldForDuration: 0.3)
            }
            if !(el.exists && el.isHittable && area.contains(el.frame)) { shot("unreachable") }
            // Options further down are drawn once scrolled to: one that is not there has no frame to report.
            let at = el.exists ? "\(el.frame)" : "not drawn"
            XCTAssertTrue(el.exists && el.isHittable && area.contains(el.frame), "\(label) out of reach (\(at) in \(cards.frame))")
        }
        XCTAssertTrue(app.keyboards.firstMatch.exists, "the keyboard stays up while the card scrolls")
        shot("question-keyboard-end")
    }

    /// A long ask_user card in Persona's main conversation (#4839): its action row is usable on arrival, before any
    /// scrolling, and a drag through the options neither picks one nor advances to the next question.
    func testLongQuestionActionsAreVisibleWithoutScrolling() {
        for scheme in ["light", "dark", "largest"] {
            launchMainQuestion(scheme == "largest" ? nil : scheme, largestText: scheme == "largest")
            XCTAssertTrue(app.staticTexts["这周先处理哪件事？"].firstMatch.waitForExistence(timeout: 20), "no long question")
            let first = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "选项 1")).firstMatch
            XCTAssertTrue(first.waitForExistence(timeout: 5), "no first option")
            first.tap() // Multi-select does not advance: Submit is now enabled on this same long question.
            let before = resolutionCount("ix_ask_long")
            let cards = app.scrollViews["pending-cards"]
            XCTAssertTrue(cards.exists, "the cards' area")
            let action = app.buttons.matching(NSPredicate(format: "label IN %@", ["提交", "Submit"])).firstMatch
            XCTAssertTrue(action.waitForExistence(timeout: 5), "no Submit:\n\(app.debugDescription)")
            XCTAssertTrue(action.isEnabled, "Submit is not enabled after a deliberate pick")
            // The outer card area's last 8 pt are its bottom fade, not room for a complete action.
            let aboveFade = action.frame.maxY <= cards.frame.maxY - 8
            XCTAssertTrue(action.isHittable && cards.frame.insetBy(dx: -1, dy: -1).contains(action.frame) && aboveFade,
                          "Submit is clipped or needs scrolling first (\(action.frame) in \(cards.frame))")
            shot("long-question-actions-\(scheme)")
            action.tap()
            let deadline = Date().addingTimeInterval(10)
            while resolutionCount("ix_ask_long") == before && Date() < deadline { usleep(300_000) }
            XCTAssertGreaterThan(resolutionCount("ix_ask_long"), before, "Submit did not resolve the ask_user card")
            app.terminate()
        }
    }

    func testDraggingLongQuestionDoesNotPick() throws {
        let hub = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_SWIPE_HUB"])
        launchMainQuestion(hubKey: "PERSONA_SWIPE_HUB")
        let question = app.staticTexts["短拖动也不要替我选"].firstMatch
        XCTAssertTrue(question.waitForExistence(timeout: 20), "no single-select question")
        let first = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "选项 1")).firstMatch
        XCTAssertTrue(first.waitForExistence(timeout: 5), "no first option:\n\(app.debugDescription)")
        let before = resolutionCount("ix_ask_long", hub: hub)
        let origin = app.coordinate(withNormalizedOffset: .zero)
        let start = origin.withOffset(CGVector(dx: first.frame.midX, dy: first.frame.midY))
        let end = origin.withOffset(CGVector(dx: first.frame.midX, dy: first.frame.midY - 12))
        start.press(forDuration: 0.05, thenDragTo: end, withVelocity: .fast, thenHoldForDuration: 0.1)
        sleep(1)
        XCTAssertTrue(question.exists, "the drag replaced the question")
        XCTAssertFalse(first.isSelected || first.label.contains("已选"), "the short drag picked an option")
        XCTAssertEqual(resolutionCount("ix_ask_long", hub: hub), before, "the short drag auto-sent the single choice")
        shot("single-question-after-drag")
    }

    /// An answer on its way (#6279): the card stays, greyed, with "Sending…" on the picked row until the hub has it; a
    /// failed answer leaves the card with its pick, the error and Retry, which sends it again. Against
    /// `MOCK_ASK_LONG=swipe MOCK_RESOLVE=slow` (the test switches the mock with /mock/resolve-mode).
    ///   PORT=18798 MOCK_AGENTS=1 MOCK_ASK_LONG=swipe python3 scripts/mock/mock_hub.py &
    ///   TEST_RUNNER_PERSONA_SENDING_HUB=http://127.0.0.1:18798 xcodebuild test … -only-testing:PersonaUITests/PendingCardsTests/testQuestionShowsSendingAndFailure
    func testQuestionShowsSendingAndFailure() throws {
        let hub = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_SENDING_HUB"])
        func mode(_ m: String) { _ = try? Data(contentsOf: URL(string: hub + "/mock/resolve-mode?mode=\(m)")!) }
        defer { mode("ok") }
        // A stock simulator asks about notifications on the first launch: not allowing keeps the card in view.
        addUIInterruptionMonitor(withDescription: "notifications") { alert in
            let no = alert.buttons.matching(NSPredicate(format: "label IN %@", ["不允许", "Don’t Allow", "Don't Allow"])).firstMatch
            if no.exists { no.tap(); return true }
            return false
        }
        mode("slow")
        launchMainQuestion(hubKey: "PERSONA_SENDING_HUB")
        let question = app.staticTexts["短拖动也不要替我选"].firstMatch
        if !question.waitForExistence(timeout: 8) { app.tap() } // lets the interruption monitor see a system alert
        XCTAssertTrue(question.waitForExistence(timeout: 20), "no single-select question")
        let first = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "选项 1")).firstMatch
        XCTAssertTrue(first.waitForExistence(timeout: 5), "no first option")
        first.tap()
        let sending = app.staticTexts.matching(NSPredicate(format: "label IN %@", ["正在发送…", "Sending…"])).firstMatch
        XCTAssertTrue(sending.waitForExistence(timeout: 2), "no Sending… on the picked row:\n\(app.debugDescription)")
        if !UITestStyle.isBeautiful { // BeautifulUI's card turns into its own "Sending…" line meanwhile
            XCTAssertTrue(question.exists, "the card went while its answer was on the way")
            XCTAssertFalse(first.isEnabled, "the options take a tap while sending")
        }
        shot("question-sending")
        // The hub has it after a few seconds: the card goes.
        XCTAssertTrue(sending.waitForNonExistence(timeout: 20) && question.waitForNonExistence(timeout: 5), "the card stayed after the hub had the answer")
        app.terminate()

        mode("fail")
        launchMainQuestion(hubKey: "PERSONA_SENDING_HUB")
        XCTAssertTrue(question.waitForExistence(timeout: 20), "no question after the relaunch")
        first.tap()
        let retry = app.buttons.matching(NSPredicate(format: "label IN %@", ["重试", "Retry"])).firstMatch
        XCTAssertTrue(retry.waitForExistence(timeout: 10), "no Retry after a failed answer:\n\(app.debugDescription)")
        XCTAssertTrue(question.exists, "the card went with its failed answer")
        shot("question-failed")
        mode("ok")
        retry.tap()
        XCTAssertTrue(question.waitForNonExistence(timeout: 15), "the card stayed after Retry")
        app.terminate()
    }

    func testLongReviewActionsAreVisibleWithoutScrolling() throws {
        let hub = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_REVIEW_HUB"])
        for scheme in ["light", "dark", "largest"] {
            launchMainQuestion(scheme == "largest" ? nil : scheme, hubKey: "PERSONA_REVIEW_HUB", largestText: scheme == "largest")
            let before = resolutionCount("ix_ask_long", hub: hub)
            for n in 1...6 {
                let question = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "第 \(n) 题")).firstMatch
                XCTAssertTrue(question.waitForExistence(timeout: 10), "question \(n) did not appear")
                let option = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "第 \(n) 个答案")).firstMatch
                XCTAssertTrue(option.waitForExistence(timeout: 5), "no answer for question \(n)")
                option.tap()
            }
            let submit = app.buttons["screen-review-submit"]
            if !submit.waitForExistence(timeout: 2) { app.buttons["提交"].firstMatch.tap() }
            XCTAssertTrue(submit.waitForExistence(timeout: 5), "no long review:\n\(app.debugDescription)")
            let cards = app.scrollViews["pending-cards"]
            var reachable = false
            let hitDeadline = Date().addingTimeInterval(5)
            while Date() < hitDeadline {
                if cards.frame.insetBy(dx: -1, dy: -1).contains(submit.frame) && submit.frame.maxY <= cards.frame.maxY - 8 {
                    reachable = true
                    break
                }
                RunLoop.current.run(until: Date().addingTimeInterval(0.2))
            }
            let submitFrame = submit.frame
            XCTAssertTrue(submit.isEnabled, "review Submit is disabled")
            XCTAssertTrue(reachable, "review Submit needs scrolling first (\(submitFrame) in \(cards.frame))")
            shot("long-review-actions-\(scheme)")
            // Tap its visible centre directly: XCUIElement.tap() may first try to make a nested-scroll child hittable.
            app.coordinate(withNormalizedOffset: .zero)
                .withOffset(CGVector(dx: submitFrame.midX, dy: submitFrame.midY)).tap()
            let deadline = Date().addingTimeInterval(10)
            while resolutionCount("ix_ask_long", hub: hub) == before && Date() < deadline { usleep(300_000) }
            XCTAssertGreaterThan(resolutionCount("ix_ask_long", hub: hub), before, "review Submit did not resolve the card")
            app.terminate()
        }
    }

    /// Another tap within a single-select pick's pause steps no further (#6549): each pick stepped on its own, so a second
    /// tap within it (the option again, or Next) skipped a question, and on question 5 went past the last one. XCUITest
    /// waits between taps longer than the 250 ms pause, so `-questionStepPause` stretches it to 3 s.
    /// `MOCK_ASK_LONG=review` (six questions of one option each), as `testLongReviewActionsAreVisibleWithoutScrolling`:
    ///   PORT=18799 MOCK_AGENTS=1 MOCK_ASK_LONG=review python3 scripts/mock/mock_hub.py &   (PERSONA_REVIEW_HUB)
    func testAnotherTapWithinThePauseStepsNoFurther() throws {
        _ = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_REVIEW_HUB"])
        launchMainQuestion(hubKey: "PERSONA_REVIEW_HUB", extra: ["-questionStepPause", "3000"])
        func option(_ n: Int) -> XCUIElement {
            app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "第 \(n) 个答案")).firstMatch
        }
        XCTAssertTrue(option(1).waitForExistence(timeout: 10), "no question card")
        // the option twice within its pause: one step
        option(1).tap()
        option(1).tap()
        sleep(5) // past both pauses and the step's animation
        shot("pause-same-option-twice")
        XCTAssertTrue(option(2).exists && !option(3).exists, "a second tap within the pause skipped question 2")
        // an option, then Next within its pause: Next's step only
        option(2).tap()
        app.buttons["下一题"].firstMatch.tap()
        sleep(5)
        shot("pause-then-next")
        XCTAssertTrue(option(3).exists && !option(4).exists, "the pick stepped again after Next and skipped question 3")
        for n in 3...4 {
            option(n).tap()
            XCTAssertTrue(option(n + 1).waitForExistence(timeout: 6), "question \(n + 1) did not come")
        }
        // the second-to-last question: never past the last one
        option(5).tap()
        option(5).tap()
        sleep(5)
        shot("pause-last-question")
        XCTAssertTrue(option(6).exists, "a second tap within the pause went past the last question")
        app.terminate()
    }

    func testThreeCardsScrollUnderTheHeader() {
        launch(chat: Self.photos)
        let count = app.staticTexts["pending-cards-count"]
        XCTAssertTrue(count.waitForExistence(timeout: 15), "three cards: a count over them")
        sleep(2)
        XCTAssertEqual(count.label, "3 个待处理")
        assertBackInReach()
        // The permission, the newest and most urgent, right under the count and on screen.
        let permission = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "unittest")).firstMatch
        XCTAssertTrue(permission.exists, "the permission card")
        let screen = app.windows.firstMatch.frame
        XCTAssertGreaterThan(permission.frame.minY, count.frame.maxY, "the permission is under the count")
        XCTAssertLessThan(permission.frame.maxY, screen.maxY, "the permission is on screen")
        // At most half the page: the conversation keeps the rest.
        let cards = app.scrollViews["pending-cards"]
        XCTAssertTrue(cards.exists, "the cards' scroll area")
        XCTAssertLessThanOrEqual(cards.frame.height, screen.height / 2, "the cards take at most half the page")
        shot("three")
    }
}
