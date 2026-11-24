import XCTest

/// Run with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASK_TRAY_ACTIVITY=1.
final class AssistantTrayActivityUITests: XCTestCase {
    @MainActor func testWaitingParentsShowActiveWorkAndRealWaitKeepsTheClock() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"], env["PERSONA_TRAY_ACTIVITY"] == "1" else {
            throw XCTSkip("Needs the deterministic task-tray activity mock")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-dev.openChat", "",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001",
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launchPinned()
        let running = app.buttons["action-bar.running"]
        XCTAssertTrue(running.waitForExistence(timeout: 30), app.debugDescription)
        let ready = NSPredicate(format: "label CONTAINS '4'")
        expectation(for: ready, evaluatedWith: running)
        waitForExpectations(timeout: 30)
        running.tap()
        let rows = app.buttons.matching(identifier: "action-tray.task")
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertEqual(rows.count, 4, app.debugDescription)
        let labels = rows.allElementsBoundByIndex.map(\.label)
        let command = try XCTUnwrap(labels.first { $0.contains("Background build verification") })
        let workflow = try XCTUnwrap(labels.first { $0.contains("Workflow review verification") })
        let child = try XCTUnwrap(labels.first { $0.contains("Subagent review verification") })
        let wait = try XCTUnwrap(labels.first { $0.contains("Scheduled wake-up verification") })
        XCTAssertTrue(command.contains("后台任务 2 个在跑"), command)
        XCTAssertTrue(workflow.contains("Verify 2/2") && workflow.contains("5/6+"), workflow)
        XCTAssertTrue(child.contains("子 Agent 1 个在跑"), child)
        XCTAssertTrue(wait.contains("等待中"), wait)
        XCTAssertFalse(wait.contains("后台"), wait)
        // The actual rendered controls, not only the text projection: three Working spinners and one clock.
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "action-tray.working-mark").count, 3, app.debugDescription)
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "action-tray.waiting-mark").count, 1, app.debugDescription)
        if let directory = env["PERSONA_SHOTS"] {
            let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            let platform = "iphone"
            let path = "\(directory)/\(platform)-\(appearance)-activity.png"
            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: path))
        }
    }
}
