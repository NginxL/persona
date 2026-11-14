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
    /// What folding a question card gives back at least: more than a tap target's row.
    private static let foldedAtLeast: CGFloat = 44
    /// A line of a question at the largest accessibility text size (lines are about 60 pt apart on an iPhone 17 Pro).
    private static let largestQuestionLine: CGFloat = 56

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

    /// 「你已更改“Persona”的图标」: the app's icon follows the appearance a launch asks for (`-ui.scheme`), and SpringBoard's
    /// alert covered the card in the screenshots and took the taps.
    private func dismissIconAlert() {
        let ok = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons
            .matching(NSPredicate(format: "label IN %@", ["OK", "好"])).firstMatch
        if ok.waitForExistence(timeout: 3) { ok.tap() }
    }

    /// A question form's own scroll area in a card: the tallest scroll view in it that holds the question's top (the step
    /// chips scroll sideways in a strip of their own above the question).
    private func formScroll(in cards: XCUIElement, holding question: XCUIElement) -> CGRect? {
        let q = question.frame
        return cards.scrollViews.allElementsBoundByIndex.map { $0.frame }
            .filter { $0.minY <= q.minY + 1 && q.minY < $0.maxY && $0.minX <= q.midX && q.midX <= $0.maxX }
            .max { $0.height < $1.height }
    }

    private func waitGone(_ element: XCUIElement, timeout: TimeInterval = 5) -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while element.exists && Date() < deadline { RunLoop.current.run(until: Date().addingTimeInterval(0.2)) }
        return !element.exists
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

    /// A lone question card, the keyboard down (#6277): it grows past half the page until the conversation keeps a fixed
    /// minimum above it, so on a phone as tall as an iPhone 17 Pro its question, every option with its description and
    /// 「其他回答」 are whole in the form's scroll area and can be tapped without scrolling. It was held to half the page: two
    /// and a half options showed, the rest under its edge, with empty conversation above it. On a shorter phone the whole
    /// question is in view and what does not fit scrolls inside the card; at the largest text size its first line is
    /// (iPhone SE shows only that line's top, as main does). The card still folds from its header, the composer stays
    /// usable, and the card's height holds still while the page does (#1580).
    func testLoneQuestionShowsEveryOption() {
        continueAfterFailure = true // every look measured and shot, whichever fails
        for look in ["light", "dark", "largest"] {
            let extra = look == "largest" ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
                : ["-ui.scheme", look]
            launch(chat: Self.widget, ["-showSampleQuestion", "YES"] + extra)
            // Not the turn's work line: at the largest text size it is scrolled out of the short conversation.
            let cards = app.scrollViews["pending-cards"]
            XCTAssertTrue(cards.waitForExistence(timeout: 15), "the cards' area (\(look))")
            dismissIconAlert()
            sleep(2)
            shot("lone-question-\(look)")
            XCTAssertFalse(app.staticTexts["pending-cards-count"].exists, "a lone card has no count over it (\(look))")
            // What is in view is what is inside the form's own scroll area, not just inside the cards' area: under that
            // scroll area are the card's buttons and margins, where an option cut off by it still had its frame.
            let question = cards.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "一轮对话里")).firstMatch
            XCTAssertTrue(question.exists, "no question (\(look))")
            let form = formScroll(in: cards, holding: question)
            XCTAssertNotNil(form, "no scroll area holds the question (\(cards.scrollViews.allElementsBoundByIndex.map { $0.frame }), \(look))")
            let shown = (form ?? .zero).intersection(cards.frame).insetBy(dx: -1, dy: -1)
            let q = question.frame
            let tall = app.windows.firstMatch.frame.height >= 874 // as tall as an iPhone 17 Pro
            if look != "largest" {
                XCTAssertTrue(shown.minY <= q.minY && q.maxY <= shown.maxY, "the question is not whole in view (\(q) in \(shown), \(look))")
            } else {
                // At the largest text size, at least its first line whole. iPhone SE shows only that line's top, as on
                // main: the open header there leaves the cards no more than half the page (#6277, a known deviation).
                let firstLine = { XCTAssertTrue(shown.minY <= q.minY && min(q.maxY, shown.maxY) - q.minY >= min(q.height, Self.largestQuestionLine),
                                                "the question's first line is not whole in view (\(q) in \(shown))") }
                if tall { firstLine() } else { XCTExpectFailure("iPhone SE at the largest text size: the question's first line is cut, as on main", failingBlock: firstLine) }
            }
            // Every option with its description, and 「其他回答」 (a field in BeautifulUI's card: its placeholder), whole in
            // the form's scroll area and tappable, with no scrolling.
            if look != "largest", tall {
                for label in ["取最长的那段", "永远取最后一段", "永远取唤醒前那段", "其他回答"] {
                    let el = cards.descendants(matching: .any)
                        .matching(NSPredicate(format: "label BEGINSWITH %@ OR placeholderValue BEGINSWITH %@", label, label)).firstMatch
                    XCTAssertTrue(el.exists && el.isHittable && shown.minY <= el.frame.minY && el.frame.maxY <= shown.maxY,
                                  "\(label) needs scrolling (\(el.exists ? "\(el.frame)" : "not drawn") in the form's \(shown), \(look))")
                }
            }
            // The conversation keeps its minimum above the card, under the chat's header and its back button. Over the
            // longer conversation the header folds to its slim bar, and XCUITest does not call that bar's back hittable:
            // here it is checked on screen, and the tap at the end checks that it still leaves the chat.
            let back = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch
            XCTAssertTrue(back.exists && back.frame.minY >= 0 && back.frame.maxY < cards.frame.minY,
                          "the back button is not on screen over the card (\(back.frame), \(look))")
            let titles = ["chat.compactTitle", "chat.title"].map { app.buttons[$0].firstMatch }.filter { $0.exists }
            let head = (titles.map { $0.frame.maxY } + [back.frame.maxY]).max() ?? 0
            XCTAssertGreaterThanOrEqual(cards.frame.minY - head, 72, "the card leaves the conversation \(cards.frame.minY - head) pt (\(look))")
            // The composer under the card and usable.
            let field = app.descendants(matching: .any).matching(NSPredicate(format: "placeholderValue BEGINSWITH %@ OR placeholderValue BEGINSWITH %@", "给", "补充说明")).firstMatch
            XCTAssertTrue(field.exists && field.isHittable, "the composer (\(look))")
            XCTAssertLessThanOrEqual(cards.frame.maxY, field.frame.minY + 1, "the card ends above the composer (\(look))")
            // Still while the page is: its height does not move from one moment to the next (#1580's cap changed every frame).
            let heights = (0..<5).map { _ -> CGFloat in usleep(200_000); return cards.frame.height }
            XCTAssertEqual(Set(heights).count, 1, "the card's height moves on a still page: \(heights) (\(look))")
            // Folded from its header, it gives the conversation its room back.
            let title = cards.staticTexts["Claude 在问你"].firstMatch
            XCTAssertTrue(title.exists, "the card's header (\(look))")
            let open = cards.frame.height
            title.tap()
            sleep(1)
            // Its options go (at the largest text size the folded header still takes three lines).
            let option = cards.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "取最长的那段")).firstMatch
            XCTAssertFalse(option.exists && option.isHittable, "folded, the card still shows its options (\(look))")
            XCTAssertLessThan(cards.frame.height, open - Self.foldedAtLeast, "folded, the card is \(cards.frame.height) pt (was \(open), \(look))")
            shot("lone-question-folded-\(look)")
            app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: back.frame.midX, dy: back.frame.midY)).tap()
            XCTAssertTrue(waitGone(cards), "a tap on the back button did not leave the chat (\(look))")
            app.terminate()
        }
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
        let heightBefore = app.scrollViews["pending-cards"].frame.height
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
        // The keyboard down again: the card is as tall as before it came up, and the app still answers queries (#6277: a
        // lone question growing past half the page once laid itself out every frame as the composer took the focus,
        // and every query after that timed out).
        let hide = app.buttons.matching(NSPredicate(format: "label IN %@", ["收起键盘", "Hide Keyboard"])).firstMatch
        XCTAssertTrue(hide.exists, "the composer's Hide Keyboard")
        hide.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "the keyboard did not go down")
        sleep(1)
        XCTAssertEqual(cards.frame.height, heightBefore, accuracy: 1, "the card did not get its height back with the keyboard down")
        XCTAssertTrue(cards.staticTexts["Claude 在问你"].firstMatch.waitForExistence(timeout: 5), "the card's header with the keyboard down")
        shot("question-keyboard-down")
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
    /// tap within it (the option again, or Next) skipped a question, and on question 5 went past the last one. A second
    /// pick starts the pause again, so the card steps a whole pause after it; Previous / Next cancel the step. XCUITest's
    /// taps each take a second or more, longer than the 250 ms pause, so `-showQuestionStepPause` stretches it to 10 s.
    /// `MOCK_ASK_LONG=review` (six questions of one option each), as `testLongReviewActionsAreVisibleWithoutScrolling`:
    ///   PORT=18799 MOCK_AGENTS=1 MOCK_ASK_LONG=review python3 scripts/mock/mock_hub.py &   (PERSONA_REVIEW_HUB)
    func testAnotherTapWithinThePauseStepsNoFurther() throws {
        _ = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_REVIEW_HUB"])
        let pause: TimeInterval = 10
        launchMainQuestion(hubKey: "PERSONA_REVIEW_HUB", extra: ["-showQuestionStepPause", "\(Int(pause * 1000))"])
        func option(_ n: Int) -> XCUIElement {
            app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "第 \(n) 个答案")).firstMatch
        }
        func wait(until date: Date) { if date.timeIntervalSinceNow > 0 { Thread.sleep(until: date) } }
        XCTAssertTrue(option(1).waitForExistence(timeout: 10), "no question card")
        // the option, and again 4 s later: the pause starts again, so the card is still on question 1 when the first
        // pick's pause is over, and steps once, when the second pick's is
        option(1).tap()
        let firstOver = Date().addingTimeInterval(pause)
        sleep(4)
        option(1).tap()
        let secondOver = Date().addingTimeInterval(pause)
        wait(until: firstOver.addingTimeInterval(1.5)) // past the first pause and a step's animation, 2.5 s before the second's end
        XCTAssertTrue(option(1).exists && !option(2).exists, "the first pick stepped, though a second came within its pause")
        wait(until: secondOver.addingTimeInterval(2))
        shot("pause-same-option-twice")
        XCTAssertTrue(option(2).exists && !option(3).exists, "a second tap within the pause did not step exactly once")
        // an option, then Next within its pause: Next's step only
        option(2).tap()
        var pickOver = Date().addingTimeInterval(pause)
        app.buttons["下一题"].firstMatch.tap()
        XCTAssertTrue(option(3).waitForExistence(timeout: 5), "Next did not step")
        wait(until: pickOver.addingTimeInterval(2))
        shot("pause-then-next")
        XCTAssertTrue(option(3).exists && !option(4).exists, "the pick stepped again after Next and skipped question 3")
        // an option, then Previous within its pause: the card stays on the question it went back to
        option(3).tap()
        pickOver = Date().addingTimeInterval(pause)
        app.buttons["上一题"].firstMatch.tap()
        XCTAssertTrue(option(2).waitForExistence(timeout: 5), "Previous did not step back")
        wait(until: pickOver.addingTimeInterval(2))
        XCTAssertTrue(option(2).exists && !option(3).exists, "the pick stepped after Previous")
        // on to question 5: questions 2 and 3 have their answers, and question 4's pick goes on with Next at once
        for n in 2...4 {
            if n == 4 { option(4).tap() }
            app.buttons["下一题"].firstMatch.tap()
            XCTAssertTrue(option(n + 1).waitForExistence(timeout: 5), "question \(n + 1) did not come")
        }
        // the second-to-last question: never past the last one
        option(5).tap()
        option(5).tap()
        XCTAssertTrue(option(6).waitForExistence(timeout: pause + 10), "question 6 did not come")
        sleep(3)
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
