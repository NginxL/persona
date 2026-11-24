import XCTest

/// Synthetic main conversation only: MOCK_TASKS=1 MOCK_SUPERVISION=1, never the user's hub or chat.
final class SupervisionRoundUITests: XCTestCase {
    override func setUpWithError() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs mock_hub.py with MOCK_TASKS=1 MOCK_SUPERVISION=1")
        }
        continueAfterFailure = false
    }

    @MainActor private func launch(_ extra: [String] = [], tab: String = "assistant") -> XCUIApplication {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", tab, "-openChatsFresh", "YES"]
        if tab == "assistant" {
            app.launchArguments += ["-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
        }
        app.launchArguments += extra
        app.launchPinned()
        return app
    }

    private func has(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    private func mock(_ path: String) -> [String: Any] {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/" + path)!),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return object
    }

    @MainActor private func openActivity(_ app: XCUIApplication, inbox: Bool = false) {
        if inbox {
            XCTAssertTrue(app.buttons["inbox.more"].firstMatch.waitForExistence(timeout: 20), app.debugDescription)
            app.buttons["inbox.more"].firstMatch.tap()
            app.buttons["inbox-background-activity"].firstMatch.tap()
        } else {
            XCTAssertTrue(app.buttons["persona-avatar"].firstMatch.waitForExistence(timeout: 20), app.debugDescription)
            app.buttons["persona-avatar"].firstMatch.tap()
            app.buttons["background-activity-menu"].firstMatch.tap()
        }
        XCTAssertTrue(app.descendants(matching: .any)["background-activity-page"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
    }

    @MainActor private func assertExpandedActivity(_ app: XCUIApplication) {
        XCTAssertTrue(app.buttons["dispatch.task.todo_task_release"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any)["answered-line"].firstMatch.exists)
        app.descendants(matching: .any)["answered-line"].firstMatch.tap()
        XCTAssertTrue(app.descendants(matching: .any)["answered-detail"].firstMatch.waitForExistence(timeout: 5))
        XCTAssertTrue(has(app, "Run the synthetic checks?").exists)
        XCTAssertTrue(has(app, "Approved by the assistant.").exists)
        let record = app.buttons["supervision-record"].firstMatch
        XCTAssertTrue(record.waitForExistence(timeout: 10), app.debugDescription)
        record.tap()
        XCTAssertTrue(app.descendants(matching: .any)["supervision-input"].firstMatch.waitForExistence(timeout: 5))
        let work = app.buttons["work-head"].firstMatch
        XCTAssertTrue(work.waitForExistence(timeout: 5), app.debugDescription)
        work.tap()
        XCTAssertTrue(has(app, "It is now your task todo_task_release.").waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(has(app, "Sent to the example conversation.").exists)
        XCTAssertTrue(has(app, "Answered on behalf of the user.").exists)
        XCTAssertTrue(has(app, "Continue the synthetic checks.").exists)
        XCTAssertTrue(has(app, "Synthetic checks are complete.").exists)
    }

    @MainActor func testSilentRoundLeavesTimelineAndExpandsInBackgroundActivity() {
        let app = launch(["-showToolDetails", "YES"])
        defer { app.terminate() }
        XCTAssertTrue(has(app, "下一条真实输入仍在督办记录之外。").waitForExistence(timeout: 25), app.debugDescription)
        XCTAssertTrue(has(app, "需要你决定：示例任务今天发布，还是明天发布？").exists)
        XCTAssertFalse(app.buttons["supervision-record"].firstMatch.exists)
        XCTAssertFalse(app.buttons["dispatch.task.todo_task_release"].firstMatch.exists)
        XCTAssertFalse(app.descendants(matching: .any)["answered-line"].firstMatch.exists)
        XCTAssertFalse(has(app, "检查示例任务的进展，不需要用户处理。").exists)
        XCTAssertFalse(has(app, "后台核对也完成了，继续静默跟进。").exists)
        openActivity(app)
        assertExpandedActivity(app)
        let record = app.buttons["supervision-record"].firstMatch
        XCTAssertEqual(app.buttons.matching(identifier: "supervision-record").count, 1)
        record.tap()
        XCTAssertFalse(app.descendants(matching: .any)["supervision-input"].firstMatch.exists)
    }

    @MainActor func testInboxOpensTheSameBackgroundActivityWithoutMarkingSeen() {
        _ = mock("reset")
        let app = launch(tab: "mail")
        defer { app.terminate() }
        XCTAssertTrue(app.buttons["inbox.more"].firstMatch.waitForExistence(timeout: 20), app.debugDescription)
        let before = mock("seen")["seen"] as? [String] ?? []
        openActivity(app, inbox: true)
        XCTAssertTrue(app.buttons["supervision-record"].firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertEqual(mock("seen")["seen"] as? [String] ?? [], before)
    }

    @MainActor func testBackgroundActivityScreenshotsLightAndDark() {
        for scheme in ["light", "dark"] {
            let app = launch(["-ui.scheme", scheme, "-showToolDetails", "YES"])
            XCTAssertTrue(has(app, "下一条真实输入仍在督办记录之外。").waitForExistence(timeout: 25), app.debugDescription)
            let before = XCTAttachment(screenshot: app.screenshot())
            before.name = "before-main-\(scheme)"
            before.lifetime = .keepAlways
            add(before)
            openActivity(app)
            assertExpandedActivity(app)
            let after = XCTAttachment(screenshot: app.screenshot())
            after.name = "after-background-\(scheme)"
            after.lifetime = .keepAlways
            add(after)
            app.terminate()
        }
    }

    @MainActor func testLegacyDisabledPreferenceStillFoldsTheWholeRound() {
        let app = launch(["-ui.newChatLayout", "NO"])
        defer { app.terminate() }
        XCTAssertTrue(has(app, "需要你决定：示例任务今天发布，还是明天发布？").waitForExistence(timeout: 25))
        XCTAssertFalse(has(app, "示例任务的测试通过，无需你处理。").exists)
        XCTAssertFalse(app.buttons["supervision-record"].firstMatch.exists)
        openActivity(app)
        XCTAssertTrue(app.buttons["supervision-record"].firstMatch.waitForExistence(timeout: 10))
    }
}
