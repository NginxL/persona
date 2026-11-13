import XCTest

/// The conversations Persona opened by itself (#3264: Inbox's AI twin, scheduled tasks, Lark /agent…) in Persona's sidebar:
/// since #4331 there is no Auto Chats row; Recent (#4873, #5195) lists the newest chats whoever opened them, each one
/// Persona opened with its source's mark, and All Chats lists every one, opening on All, with a filter for each source.
/// Against scripts/mock/mock_hub.py with MOCK_AUTO_CONVERSATIONS=1 (PERSONA_AUTO_HUB; MOCK_EXTERNAL=1 too or not).
/// SHOT_DIR keeps the screenshots (the sidebar, All Chats as it opens, the list filtered to Inbox).
final class AutoConversationsTests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_AUTO_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AUTO_CONVERSATIONS=1 (PERSONA_AUTO_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch() {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-newAssistant", "YES", "-dev.openChat", ""]
        app.launchPinned()
    }

    /// The drawer's button on the phone; the iPad's column is there already. Returns the All Chats row.
    @MainActor private func openSidebar(timeout: TimeInterval = 20) -> XCUIElement {
        let menu = app.buttons["assistant-sidebar-button"].firstMatch
        let all = app.buttons["sidebar-all-chats"].firstMatch
        let end = Date().addingTimeInterval(timeout)
        while Date() < end && !menu.exists && !all.exists { usleep(250_000) }
        if all.exists && all.isHittable { return all }
        XCTAssertTrue(menu.exists, "no sidebar button:\n\(app.debugDescription)")
        sleep(1)
        menu.tapCenter()
        XCTAssertTrue(all.waitForExistence(timeout: 10), "no 全部对话 row:\n\(app.debugDescription)")
        return all
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["SHOT_DIR"] {
            let suffix = ProcessInfo.processInfo.environment["SHOT_SUFFIX"].map { "-" + $0 } ?? ""
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/auto-\(name)\(suffix).png"))
        }
    }

    @MainActor private func conversation(_ prefix: String) -> XCUIElement {
        app.buttons.matching(identifier: "sidebar-conversation").matching(NSPredicate(format: "label BEGINSWITH %@", prefix)).firstMatch
    }

    /// The sidebar: Recent has the newest five whoever opened them except the scheduled tasks' chats (#6228: those have
    /// their own Scheduled Tasks entry and stay in All Chats), each one Persona opened carries its source's mark; the
    /// heading folds Recent and opens it again.
    @MainActor func testRecentListsEveryChatWithItsSourceMark() {
        launch()
        let all = openSidebar()
        // The mock's newest after the main chat: three Inbox ones (6, 12, 40 minutes), Lark /agent's (2 hours); the
        // scheduled task's (3 hours) now stays out of Recent.
        XCTAssertTrue(conversation("1105 | 收件箱 | 回复韩梅梅").waitForExistence(timeout: 10), "the newest, an Inbox one, is not in 最近:\n\(app.debugDescription)")
        let task = conversation("1105 | 定时 | 晨间简报")
        XCTAssertFalse(task.exists, "a scheduled task's conversation is still in 最近 (#6228)")
        let lark = app.buttons.matching(identifier: "sidebar-conversation").matching(NSPredicate(format: "label CONTAINS %@", "来自 飞书 /agent")).firstMatch
        XCTAssertTrue(lark.exists, "Lark /agent's conversation is not in 最近 with its mark")
        XCTAssertEqual(app.buttons.matching(identifier: "sidebar-conversation").count, 5, "最近 lists five")
        XCTAssertFalse(conversation("帮我约周五").exists, "an older conversation of the user's is in 最近")
        XCTAssertFalse(app.buttons["sidebar-auto"].exists, "the Auto Chats row is back")
        XCTAssertTrue(all.exists)
        // The heading folds Recent and keeps working while folded (#6228).
        let fold = app.buttons["sidebar-recent-fold"].firstMatch
        XCTAssertTrue(fold.waitForExistence(timeout: 5), "no foldable Recent heading")
        fold.tap()
        XCTAssertTrue(app.buttons.matching(identifier: "sidebar-conversation").firstMatch.waitForNonExistence(timeout: 5), "最近 did not fold")
        fold.tap()
        XCTAssertTrue(conversation("1105 | 收件箱 | 回复韩梅梅").waitForExistence(timeout: 5), "最近 did not open again")
        sleep(1)
        shot("sidebar")
    }

    /// All Chats: it opens on All, every time (the last pick is not kept), with a filter for each source; a row opens
    /// its conversation.
    @MainActor func testAllChatsFiltersBySource() {
        launch()
        openSidebar().tap()
        let inbox = app.buttons["auto-filter-inbox"].firstMatch
        XCTAssertTrue(inbox.waitForExistence(timeout: 5), "no 收件箱 filter:\n\(app.debugDescription)")
        for id in ["task", "lark_agent", "spawned", "mine", "all"] {
            XCTAssertTrue(app.buttons["auto-filter-" + id].exists, "no \(id) filter")
        }
        XCTAssertTrue(app.buttons["auto-filter-all"].isSelected, "全部 is not the filter All Chats opens on")
        // The scheduled tasks' chats left Recent, not the app: the task filter still lists them (#6228).
        app.buttons["auto-filter-task"].firstMatch.tap()
        XCTAssertTrue(rows.matching(NSPredicate(format: "label BEGINSWITH %@", "1105 | 定时 | 晨间简报")).firstMatch.waitForExistence(timeout: 5), "定时任务 filter lacks the task's chat")
        app.buttons["auto-filter-all"].firstMatch.tap()
        sleep(1)
        shot("list-opens-on-all")
        inbox.tap()
        XCTAssertTrue(inbox.isSelected, "收件箱 is not the chosen filter")
        let rows = app.buttons.matching(identifier: "auto-conversation")
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 5), "收件箱's list is empty")
        XCTAssertEqual(rows.count, 6, "收件箱's list")
        sleep(1)
        shot("list-inbox")

        app.buttons["auto-filter-mine"].firstMatch.tap()
        XCTAssertTrue(app.buttons["auto-filter-mine"].isSelected, "我的 is not the chosen filter")
        XCTAssertTrue(rows.matching(NSPredicate(format: "label BEGINSWITH %@", "帮我约周五")).firstMatch.waitForExistence(timeout: 3), "我的 lacks the user's own")
        XCTAssertFalse(rows.matching(NSPredicate(format: "label CONTAINS %@", "收件箱")).firstMatch.exists, "我的 has an inbox one")
        app.buttons["auto-filter-all"].firstMatch.tap()
        XCTAssertTrue(app.buttons["auto-filter-all"].isSelected, "全部 is not the chosen filter")
        // A list draws only the rows on screen: more than Inbox's 6 is all of them here.
        XCTAssertGreaterThan(rows.count, 6, "全部's list")
        shot("list-all")

        // A row opens its conversation (the sheet and the drawer go); All Chats opens on All again, not on the pick.
        inbox.tap()
        rows.matching(NSPredicate(format: "label BEGINSWITH %@", "1105 | 收件箱 | 周会纪要确认")).firstMatch.tap()
        XCTAssertTrue(rows.firstMatch.waitForNonExistence(timeout: 5), "the list stayed")
        openSidebar(timeout: 5).tap()
        XCTAssertTrue(app.buttons["auto-filter-all"].firstMatch.waitForExistence(timeout: 5), "All Chats did not open again")
        XCTAssertTrue(app.buttons["auto-filter-all"].isSelected, "All Chats kept the last pick (收件箱)")
    }
}
