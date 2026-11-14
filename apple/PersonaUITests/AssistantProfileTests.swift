import XCTest

/// Persona's profile (Details in its avatar's menu on the Persona tab) and its schedule: one list of tasks (every day, every
/// 30 minutes, once), a task's page, Run Now for a daily task and for a follow-up, and a task's permission mode (Allow Everything
/// asks first). Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB), which records what was run at /mock/schedule.
final class AssistantProfileTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(_ extra: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-newAssistant", "YES"] + extra
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let suffix = ProcessInfo.processInfo.environment["SHOT_SUFFIX"].map { "-" + $0 } ?? ""
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/persona-profile-\(name)\(suffix).png"))
    }

    /// What the mock hub was asked to run.
    private func ran() throws -> [String] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/schedule")!)
        let obj = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return obj?["ran"] as? [String] ?? []
    }

    @MainActor private func openProfile(_ app: XCUIApplication) {
        // The face in the top bar opens a menu; Details is the profile.
        let avatar = app.buttons["shell-assistant"]
        XCTAssertTrue(avatar.waitForExistence(timeout: 15), "no avatar on the Persona tab:\n\(app.debugDescription)")
        avatar.tap()
        XCTAssertTrue(app.buttons["详细信息"].waitForExistence(timeout: 3), "no 详细信息 in the avatar's menu")
        app.buttons["详细信息"].tap()
        XCTAssertTrue(app.staticTexts["你的私人助手 · 运行在 Server"].waitForExistence(timeout: 5)
                      || app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "你的私人助手")).firstMatch.waitForExistence(timeout: 5),
                      "the profile did not open:\n\(app.debugDescription)")
    }

    @MainActor func testProfileRoutineAndRunNow() throws {
        let app = launch()
        openProfile(app)
        XCTAssertTrue(app.staticTexts["接下来"].exists || app.staticTexts["接下来".uppercased()].exists, "no 接下来 section")
        for row in ["定时任务", "运行记录", "关于我", "记忆"] {
            let b = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", row)).firstMatch
            for _ in 0..<4 where !(b.exists && b.isHittable) { app.swipeUp() }
            XCTAssertTrue(b.exists, "no \(row) row:\n\(app.debugDescription)")
        }
        shot(app, "profile")
        for _ in 0..<4 { app.swipeDown() }

        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "定时任务")).firstMatch.tap()
        let morning = app.descendants(matching: .any)["task-morning"]
        XCTAssertTrue(morning.waitForExistence(timeout: 5), "no 早报 routine:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["每个工作日 09:00"].exists, "the rule is not in words")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "上次")).firstMatch.exists, "no last run")
        shot(app, "routines")

        morning.tap()
        XCTAssertTrue(app.staticTexts["什么时候"].waitForExistence(timeout: 5) || app.staticTexts["什么时候".uppercased()].exists,
                      "the routine page did not open")
        let runNow = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "立即运行")).firstMatch
        for _ in 0..<4 where !(runNow.exists && runNow.isHittable) { app.swipeUp() }
        XCTAssertTrue(runNow.exists, "no 立即运行 on the routine page:\n\(app.debugDescription)")
        let history = app.staticTexts["历史记录"]
        for _ in 0..<4 where !(history.exists && history.isHittable) { app.swipeUp() }
        XCTAssertTrue(history.exists, "no history on the routine page:\n\(app.debugDescription)")
        XCTAssertTrue(runNow.isHittable, "立即运行 scrolled away")
        shot(app, "routine")
        runNow.tap()
        let deadline = Date().addingTimeInterval(10)
        while try !ran().contains("morning"), Date() < deadline { usleep(200_000) }
        XCTAssertTrue(try ran().contains("morning"), "立即运行 did not reach the hub")
        // It opens the conversation it started: the profile is gone.
        XCTAssertTrue(app.buttons["shell-assistant"].waitForExistence(timeout: 5), "not back on the conversation")
        XCTAssertFalse(app.staticTexts["历史记录"].exists, "still on the routine page")
    }

    @MainActor func testFollowupRunNow() throws {
        let app = launch()
        openProfile(app)
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "定时任务")).firstMatch.tap()
        let ci = app.descendants(matching: .any)["task-fu_ci"].firstMatch
        XCTAssertTrue(app.staticTexts["接下来"].waitForExistence(timeout: 5), "the schedule page did not open")
        for _ in 0..<5 where !(ci.exists && ci.isHittable) { app.swipeUp() }
        XCTAssertTrue(ci.exists && ci.isHittable, "no follow-up row on the schedule page:\n\(app.debugDescription)")
        ci.press(forDuration: 1.0)
        let run = app.buttons["立即运行"].firstMatch
        XCTAssertTrue(run.waitForExistence(timeout: 5), "no 立即运行 in the menu:\n\(app.debugDescription)")
        run.tap()
        let deadline = Date().addingTimeInterval(10)
        while try !ran().contains("fu_ci"), Date() < deadline { usleep(200_000) }
        XCTAssertTrue(try ran().contains("fu_ci"), "the follow-up was not run")
    }

    /// A task with no conversation of its own to go into runs in its own one (docs/assistant/assistant.md「Scheduled tasks」): its page
    /// says Runs in the Same Chat and opens it; New Chat Next Time lets the next run start over, and that run's conversation is the
    /// task's again. Reached from the shell's drawer.
    @MainActor func testATaskOpensItsOwnConversationInTheShell() throws { try ownConversation() }

    @MainActor private func ownConversation() throws {
        let app = launch()
        let name = "shell"
        func openMorning() {
            let menu = app.buttons["assistant-sidebar-button"].firstMatch
            XCTAssertTrue(menu.waitForExistence(timeout: 15), "no drawer button:\n\(app.debugDescription)")
            menu.tapCenter()
            let routines = app.buttons.matching(NSPredicate(format: "label == '定时任务'")).firstMatch
            XCTAssertTrue(routines.waitForExistence(timeout: 5), "no Scheduled Tasks in the drawer:\n\(app.debugDescription)")
            routines.tap()
            let morning = app.descendants(matching: .any)["task-morning"]
            XCTAssertTrue(morning.waitForExistence(timeout: 5), "no 早报:\n\(app.debugDescription)")
            morning.tap()
            XCTAssertTrue(app.staticTexts["什么时候"].waitForExistence(timeout: 5), "the task's page did not open")
        }
        func reach(_ e: XCUIElement) {
            for _ in 0..<5 where !(e.exists && e.isHittable) { app.swipeUp() }
            XCTAssertTrue(e.exists && e.isHittable, "\(e) is not on the page:\n\(app.debugDescription)")
        }

        openMorning()
        let own = app.buttons["task.ownConversation"]
        reach(own)
        XCTAssertTrue(app.buttons["task.freshConversation"].exists, "no 下次开新对话")
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "查看上次结果")).firstMatch.exists,
                       "the task's own conversation is listed twice")
        shot(app, "own-\(name)")
        own.tap()
        // The conversation it runs in, on Assistant / the Persona tab, with the task's page gone.
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "3 封邮件需要回复")).firstMatch.waitForExistence(timeout: 8),
                      "the task's conversation did not open:\n\(app.debugDescription)")
        XCTAssertFalse(app.staticTexts["什么时候"].exists, "still on the task's page")

        openMorning()
        let fresh = app.buttons["task.freshConversation"]
        reach(fresh)
        fresh.tap()
        let deadline = Date().addingTimeInterval(5)
        while try tasks().first(where: { $0["id"] as? String == "morning" })?["run_session_id"] != nil, Date() < deadline { usleep(200_000) }
        XCTAssertNil(try tasks().first(where: { $0["id"] as? String == "morning" })?["run_session_id"], "下次开新对话 did not reach the hub")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "第一次运行时开一段新对话")).firstMatch.waitForExistence(timeout: 5),
                      "the page doesn't say the next run starts over:\n\(app.debugDescription)")
        XCTAssertFalse(fresh.exists, "下次开新对话 is still offered")
        XCTAssertFalse(app.buttons["task.ownConversation"].exists, "nothing to open until the next run")
        shot(app, "fresh-\(name)")

        // The next run starts a conversation, and it is the task's own again.
        let runNow = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "立即运行")).firstMatch
        reach(runNow)
        runNow.tap()
        // The shell's top bar has the Assistant | Agents switch where the old one has Persona's face.
        XCTAssertTrue(app.buttons["shell-assistant"].waitForExistence(timeout: 8), "not back on the conversation")
        XCTAssertTrue(app.staticTexts["什么时候"].waitForNonExistence(timeout: 5), "still on the task's page")
        XCTAssertNotNil(try tasks().first(where: { $0["id"] as? String == "morning" })?["run_session_id"], "the run's conversation is not the task's")
    }

    /// The scheduled tasks' conversations are on Scheduled Tasks, not in the drawer's Recent (#2838): 「早报」's own conversation is not
    /// listed there, the user's own ones are, and Scheduled Tasks opens it from under 「早报」's row.
    @MainActor func testATasksConversationsAreOnItsRowNotInRecent() throws {
        let app = launch()
        let menu = app.buttons["assistant-sidebar-button"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 15), "no ☰:\n\(app.debugDescription)")
        menu.tapCenter()
        let mine = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "帮我约周五和韩梅梅吃饭")).firstMatch
        XCTAssertTrue(mine.waitForExistence(timeout: 5), "the user's own conversation is not in 最近:\n\(app.debugDescription)")
        sleep(2) // the tasks and runs are read as the drawer comes out
        XCTAssertFalse(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "早报 · 周三")).firstMatch.exists,
                       "早报's conversation is still in 最近:\n\(app.debugDescription)")
        shot(app, "recent-without-tasks")
        let routines = app.buttons.matching(NSPredicate(format: "label == '定时任务'")).firstMatch
        XCTAssertTrue(routines.waitForExistence(timeout: 5), "no 定时任务 in the drawer")
        routines.tap()
        let chat = app.buttons["task-chat-morning-0"]
        XCTAssertTrue(chat.waitForExistence(timeout: 8), "no conversation under 早报:\n\(app.debugDescription)")
        shot(app, "routines-chats")
        chat.tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "3 封邮件需要回复")).firstMatch.waitForExistence(timeout: 8),
                      "早报's conversation did not open:\n\(app.debugDescription)")
    }

    /// The tasks the mock hub has now.
    private func tasks() throws -> [[String: Any]] {
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/assistant/tasks")!)
        return (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["tasks"] as? [[String: Any]] ?? []
    }

    /// A patrol Persona set up in a conversation is on the page like any task: every 30 minutes, its next run, what it
    /// runs with. Its page changes the permission mode; Allow Everything asks first, says so under the picker, and is saved when
    /// leaving the page.
    @MainActor func testRepeatingTaskAndItsPermissionMode() throws {
        let app = launch()
        openProfile(app)
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "定时任务")).firstMatch.tap()
        let patrol = app.descendants(matching: .any)["task-fu_patrol"].firstMatch
        XCTAssertTrue(patrol.waitForExistence(timeout: 5), "the patrol is not on the page:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["每 30 分钟"].exists, "its rule is not in words")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "自动 · 思考低")).firstMatch.exists,
                      "what it runs with is not on its row")
        shot(app, "tasks")
        let oneOffs = app.staticTexts["一次性跟进"]
        for _ in 0..<4 where !(oneOffs.exists && oneOffs.isHittable) { app.swipeUp() }
        XCTAssertTrue(oneOffs.exists, "no one-offs section:\n\(app.debugDescription)")
        shot(app, "tasks-bottom")
        for _ in 0..<3 { app.swipeDown() }

        patrol.tap()
        let mode = app.buttons["task-mode"].firstMatch
        for _ in 0..<4 where !(mode.exists && mode.isHittable) { app.swipeUp() }
        XCTAssertTrue(mode.waitForExistence(timeout: 5), "no permission mode on the task's page:\n\(app.debugDescription)")
        shot(app, "task")
        mode.tap()
        let bypass = app.buttons["全部放行"].firstMatch
        XCTAssertTrue(bypass.waitForExistence(timeout: 3), "no 全部放行 among the modes:\n\(app.debugDescription)")
        bypass.tap()
        let alert = app.alerts["全部放行？"]
        XCTAssertTrue(alert.waitForExistence(timeout: 3), "全部放行 did not ask first")
        shot(app, "bypass-alert")
        alert.buttons["全部放行"].tap()
        let warning = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "全部放行：")).firstMatch
        for _ in 0..<3 where !(warning.exists && warning.isHittable) { app.swipeUp() }
        XCTAssertTrue(warning.waitForExistence(timeout: 3), "no warning under the picker:\n\(app.debugDescription)")
        shot(app, "task-bypass")

        app.navigationBars.buttons.firstMatch.tap() // back: saved when leaving
        let deadline = Date().addingTimeInterval(10)
        func saved() throws -> Bool { try tasks().first { $0["id"] as? String == "fu_patrol" }?["permission_mode"] as? String == "bypassPermissions" }
        while try !saved(), Date() < deadline { usleep(200_000) }
        XCTAssertTrue(try saved(), "the mode did not reach the hub")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "全部放行")).firstMatch.waitForExistence(timeout: 5),
                      "the row doesn't say it now runs with 全部放行")
        shot(app, "tasks-bypass")
    }
}
