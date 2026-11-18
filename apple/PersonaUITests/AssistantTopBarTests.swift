import XCTest

/// The Persona tab's top bar and its sidebar (docs/apple/ios.md「Persona top bar and sidebar」): the avatar's menu (Knowledge, Details, the
/// copy items), Chat History on the left opening the sidebar, a swipe to the right anywhere on the page doing the same,
/// New Chat on the right, picking a conversation in the sidebar, and the bar staying put while a long chat scrolls.
/// Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB): its main conversation is「今天要处理的事」, the long one is ASK4.
/// The iPhone shell's bar (the face is the switch's Assistant, `shell-assistant`, since #1696): the iPad's bar beside its
/// sidebar column is SidebarLayoutTests' and IPadSidebarTests'.
final class AssistantTopBarTests: XCTestCase {
    private var hub: String!
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let long = "server/b2e4d6f8-0000-4000-8000-000000000004"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else {
            throw XCTSkip("the iPhone shell's top bar: the iPad has its sidebar column and no shell switch")
        }
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(_ open: String?, _ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-probes", "YES"]
            + (open.map { ["-openAssistant", $0] } ?? ["-newAssistant", "YES"]) + extra
        app.launchPinned()
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons
            .matching(NSPredicate(format: "label IN %@", ["允许", "Allow"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let suffix = ProcessInfo.processInfo.environment["SHOT_SUFFIX"].map { "-" + $0 } ?? ""
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/persona-topbar-\(name)\(suffix).png"))
    }

    @MainActor private func avatar(_ app: XCUIApplication) -> XCUIElement {
        let a = app.buttons["shell-assistant"]
        XCTAssertTrue(a.waitForExistence(timeout: 15), "no avatar in the top bar:\n\(app.debugDescription)")
        return a
    }

    @MainActor private func sidebar(_ app: XCUIApplication) -> XCUIElement { app.descendants(matching: .any)["assistant-sidebar"] }

    /// Back to the main conversation from a page the menu opened: a place's root has ☰ and no Back (#7799), so through
    /// the conversation's row in its drawer.
    @MainActor private func backToMain(_ app: XCUIApplication, from page: String) {
        let menu = app.buttons["shell-place-sidebar"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 5), "no ☰ on \(page):\n\(app.debugDescription)")
        menu.tap()
        let row = app.buttons["sidebar-main"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5), "\(page)'s ☰ opened no drawer:\n\(app.debugDescription)")
        row.tap()
        waitGone(sidebar(app), "the drawer stays open after picking the main conversation from \(page)")
    }

    /// One of the sidebar's day headings.
    @MainActor private func waitGone(_ e: XCUIElement, _ why: String) {
        let gone = expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: e)
        XCTAssertEqual(XCTWaiter.wait(for: [gone], timeout: 5), .completed, why)
    }

    /// Paste back into the owning app: the UI-test runner has a separate protected pasteboard.
    @MainActor private func copied(_ app: XCUIApplication, _ text: String) {
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10), "no composer for the paste check")
        field.tapCenter()
        let existing = field.value as? String ?? ""
        if !existing.isEmpty, existing != field.placeholderValue {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: existing.count + 2))
        }
        field.press(forDuration: 1)
        let pasteMatches = app.descendants(matching: .any).matching(NSPredicate(format: "label IN %@", ["粘贴", "Paste"]))
        XCTAssertTrue(pasteMatches.firstMatch.waitForExistence(timeout: 5), "the copied payload has no Paste action")
        guard let paste = pasteMatches.allElementsBoundByIndex.first(where: { $0.isHittable && $0.frame.width > 0 && $0.frame.height > 0 }) else {
            return XCTFail("no hittable Paste action:\n\(app.debugDescription)")
        }
        paste.tapCenter()
        let value = field.value as? String ?? ""
        if value.isEmpty || value == field.placeholderValue {
            let attachment = XCTAttachment(screenshot: app.screenshot())
            attachment.name = "copy-paste-failure"
            attachment.lifetime = .keepAlways
            add(attachment)
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "copy-paste-tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        XCTAssertFalse(value.isEmpty || value == field.placeholderValue, "the menu copied no text")
        let session = String(main.split(separator: "/").last!)
        if text == "已拷贝会话 ID" {
            XCTAssertEqual(value, session, "the menu did not copy the session ID")
        } else if text == "已拷贝对话信息" {
            XCTAssertTrue(value.contains(session), "the chat info does not identify this conversation")
        } else {
            XCTAssertTrue(value.hasPrefix("/") || value.hasPrefix("~/"), "the directory copy is not a path")
        }
        field.press(forDuration: 1)
        let selectAll = app.descendants(matching: .any).matching(NSPredicate(format: "label IN %@", ["全选", "Select All"])).firstMatch
        if selectAll.waitForExistence(timeout: 2) {
            selectAll.tapCenter()
            field.typeText(XCUIKeyboardKey.delete.rawValue)
        } else {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: value.count + 2))
        }
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap()
    }

    /// The selected Assistant menu exposes copies, Details and Knowledge; copies are verified by pasting their payload.
    @MainActor func testAvatarMenu() throws {
        let app = launch(main)
        XCTAssertTrue(app.staticTexts["今天有 3 件事需要你："].waitForExistence(timeout: 15)
                      || app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "今天有")).firstMatch.waitForExistence(timeout: 10),
                      "the main conversation did not open:\n\(app.debugDescription)")
        shot(app, "chat")
        avatar(app).tap()
        for item in ["知识库", "详细信息", "拷贝目录", "拷贝信息", "拷贝会话 ID", "会话信息"] {
            XCTAssertTrue(app.buttons[item].waitForExistence(timeout: 3), "no \(item) in the avatar's menu:\n\(app.debugDescription)")
        }
        XCTAssertFalse(app.buttons["回到主对话"].exists, "already on the main conversation")
        shot(app, "menu")
        app.buttons["拷贝目录"].tap()
        copied(app, "已拷贝工作目录")
        shot(app, "copied")
        avatar(app).tap()
        app.buttons["拷贝信息"].tap()
        copied(app, "已拷贝对话信息")
        avatar(app).tap()
        app.buttons["拷贝会话 ID"].tap()
        copied(app, "已拷贝会话 ID")

        avatar(app).tap()
        app.buttons["详细信息"].tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "你的私人助手")).firstMatch.waitForExistence(timeout: 5),
                      "详细信息 did not open the profile:\n\(app.debugDescription)")
        backToMain(app, from: "详细信息")
        avatar(app).tap()
        app.buttons["知识库"].tap()
        XCTAssertTrue(app.navigationBars["知识库"].waitForExistence(timeout: 5), "知识库 did not open:\n\(app.debugDescription)")
        backToMain(app, from: "知识库")
        XCTAssertTrue(avatar(app).waitForExistence(timeout: 5), "not back on the chat")
    }

    /// New Chat on the right; Chat History on the left opens the sidebar, a conversation there closes it and switches.
    @MainActor func testButtonsAndPickingAConversation() throws {
        let app = launch(main)
        _ = avatar(app)
        let new = app.buttons["新对话"].firstMatch
        XCTAssertTrue(new.exists && new.isHittable, "no 新对话 on the right:\n\(app.debugDescription)")
        new.tapCenter()
        // The conversation's own composer says Ask Persona… too: the page changed when its messages are gone and New Chat
        // (nothing to start anew from) is off.
        XCTAssertTrue(app.staticTexts["今天有什么需要我处理的？"].waitForNonExistence(timeout: 5) && !new.isEnabled,
                      "新对话 did not open the new-conversation page:\n\(app.debugDescription)")
        XCTAssertTrue(app.descendants(matching: .any)["shell-composer-field"].firstMatch.exists, "no composer on the new-conversation page")
        shot(app, "home")

        let history = app.buttons["历史对话"]
        XCTAssertTrue(history.isHittable, "no 历史对话 on the left")
        history.tapCenter()
        XCTAssertTrue(sidebar(app).waitForExistence(timeout: 3), "历史对话 did not open the sidebar:\n\(app.debugDescription)")
        // Doubao's layout (2026-10-31): the main conversation first, what to start, Recent, Persona and Settings at the foot.
        for row in ["定时任务", "知识库", "最近"] {
            XCTAssertTrue(app.staticTexts[row].firstMatch.exists, "no \(row) in the sidebar:\n\(app.debugDescription)")
        }
        XCTAssertTrue(app.drawerNewChatButton.exists, "the drawer has no New Chat action")
        // The main conversation is the first row, once: not under Pin or Recent (#2004, every layout). It is "Main Chat"
        // under the assistant's head (#9973).
        let mainRow = app.buttons["sidebar-main"].firstMatch
        XCTAssertTrue(mainRow.exists && mainRow.label.hasPrefix("主对话"), "no 主对话 row:\n\(app.debugDescription)")
        XCTAssertLessThan(mainRow.frame.minY, app.buttons["sidebar-inbox"].firstMatch.frame.minY, "the main conversation is not the first row")
        XCTAssertEqual(app.buttons.matching(identifier: "sidebar-main").count, 1, "the main conversation shows more than once")
        XCTAssertTrue(app.drawerSettingsButton.exists, "no Settings at the drawer's foot")
        XCTAssertTrue(app.buttons["搜索对话"].exists, "no search button")
        shot(app, "sidebar")
        // A conversation the user opened, in Recent: the morning brief's is that scheduled task's own, under Scheduled Tasks (#3566, #5197).
        let pick = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "帮我约周五")).firstMatch
        XCTAssertTrue(pick.isHittable, "no 帮我约周五 among the conversations")
        pick.tap()
        waitGone(sidebar(app), "the sidebar stays open after picking a conversation")
        XCTAssertTrue(app.staticTexts["已经在日历里加好了。"].waitForExistence(timeout: 10),
                      "did not switch to 帮我约周五:\n\(app.debugDescription)")

        // Off the main conversation, the avatar's menu leads back to it.
        avatar(app).tap()
        XCTAssertTrue(app.buttons["回到主对话"].waitForExistence(timeout: 3), "no 回到主对话 on a side conversation")
        app.buttons["回到主对话"].tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "今天有")).firstMatch.waitForExistence(timeout: 10),
                      "回到主对话 did not switch")
    }

    /// A swipe to the right from anywhere on the page (not just the edge) opens the same sidebar; a tap on the page
    /// beside it, or a swipe back, closes it.
    @MainActor func testSwipeOpensTheSidebar() throws {
        let app = launch(main)
        _ = avatar(app)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "今天有")).firstMatch.waitForExistence(timeout: 10))
        let window = app.windows.firstMatch
        func swipe(from: CGFloat, to: CGFloat, y: CGFloat = 0.45) {
            window.coordinate(withNormalizedOffset: CGVector(dx: from, dy: y))
                .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: to, dy: y)),
                       withVelocity: .default, thenHoldForDuration: 0)
        }
        swipe(from: 0.35, to: 0.95) // from the middle of the chat
        XCTAssertTrue(sidebar(app).waitForExistence(timeout: 3), "a swipe to the right did not open the sidebar:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["sidebar-main"].exists && app.staticTexts["最近"].exists, "not the same sidebar as the button's")

        let close = app.descendants(matching: .any)["assistant-drawer-close"]
        XCTAssertTrue(close.exists, "no page edge to tap")
        close.tap()
        waitGone(sidebar(app), "a tap beside the sidebar did not close it")

        swipe(from: 0.2, to: 0.9, y: 0.6)
        XCTAssertTrue(sidebar(app).waitForExistence(timeout: 3), "the second swipe did not open the sidebar")
        // A conversation row owns its left swipe (Archive). Start on the visible pushed-aside page,
        // not at a fixed screen point that can land on a row after sidebar sizing changes.
        let visiblePage = close.frame.intersection(window.frame)
        XCTAssertFalse(visiblePage.isEmpty, "no visible page edge to swipe")
        let origin = window.coordinate(withNormalizedOffset: .zero)
        origin.withOffset(CGVector(dx: visiblePage.midX, dy: visiblePage.midY))
            .press(forDuration: 0.05,
                   thenDragTo: origin.withOffset(CGVector(dx: window.frame.width * 0.05, dy: visiblePage.midY)),
                   withVelocity: .default, thenHoldForDuration: 0)
        waitGone(sidebar(app), "a swipe to the left did not close the sidebar")

        // A left swipe pages to Agents in the Shell, rather than opening the drawer.
        swipe(from: 0.8, to: 0.2)
        XCTAssertFalse(sidebar(app).exists, "a swipe to the left opened the sidebar")
        XCTAssertTrue(app.waitAgentsDrawn(true), "a left swipe did not page to Agents: \(app.shellTrace)")
        ShellFinger(app).switchTo(agents: false, "Assistant before reopening the drawer")

        swipe(from: 0.3, to: 0.95)
        XCTAssertTrue(sidebar(app).waitForExistence(timeout: 3))
        let pick = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "帮我约周五和韩梅梅吃饭")).firstMatch
        XCTAssertTrue(pick.isHittable, "no conversation to pick")
        pick.tap()
        waitGone(sidebar(app), "the sidebar stays open after picking")
        XCTAssertTrue(app.staticTexts["已经在日历里加好了。"].waitForExistence(timeout: 10), "did not switch conversations")

        // The Shell switch still reaches Agents.
        ShellFinger(app).switchTo(agents: true, "Agents after picking a conversation")
        XCTAssertTrue(app.buttons["agents-new"].firstMatch.waitForExistence(timeout: 5), "the switch did not reach Agents")
    }

    /// Scrolling a long conversation leaves the top bar where it was.
    @MainActor func testTopBarStaysWhileScrolling() throws {
        let app = launch(long)
        let a = avatar(app)
        XCTAssertTrue(app.staticTexts["第 30 本也整理一下"].waitForExistence(timeout: 15), "the long conversation did not open:\n\(app.debugDescription)")
        sleep(1)
        let history = app.buttons["历史对话"], new = app.buttons["新对话"].firstMatch
        let before = [a.frame, history.frame, new.frame]
        // The chat list is flipped (XCUITest reports its rows' frames wrong): which messages exist says where it is.
        func shown() -> [String] {
            app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "本也整理一下")).allElementsBoundByIndex.map(\.label)
        }
        let top = shown()
        for _ in 0..<4 { app.swipeDown(velocity: .fast) } // back in history (older pages load as it goes)
        shot(app, "scrolled")
        XCTAssertNotEqual(shown(), top, "the list did not scroll:\n\(app.debugDescription)")
        XCTAssertEqual([a.frame, history.frame, new.frame], before, "the top bar moved while scrolling back")
        for _ in 0..<2 { app.swipeUp(velocity: .fast) }
        XCTAssertEqual([a.frame, history.frame, new.frame], before, "the top bar moved while scrolling towards the latest")
        XCTAssertFalse(sidebar(app).exists, "vertical scrolling opened the sidebar")
    }

    /// Out of reach of the hub, the name under the avatar turns into the spinner and the state.
    @MainActor func testConnectionStateInTheNamePill() throws {
        let app = launch(main, ["-showConnection", "connecting"])
        _ = avatar(app)
        XCTAssertTrue(app.staticTexts["连接中…"].waitForExistence(timeout: 5), "no 连接中… under the avatar:\n\(app.debugDescription)")
        shot(app, "connecting")
        avatar(app).tap()
        XCTAssertTrue(app.buttons["连接详情"].waitForExistence(timeout: 3), "no 连接详情 in the menu while connecting")
        app.buttons["连接详情"].tap()
        XCTAssertTrue(app.buttons["立即重试"].waitForExistence(timeout: 3), "连接详情 did not open")
    }
}
