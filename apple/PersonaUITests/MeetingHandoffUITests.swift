import XCTest

/// Handing a meeting to an Agent after it (#8852, docs/assistant/meeting-minutes.md "Handing a meeting to an Agent
/// (#8852)") against the mock hub (`-showMeetingSample after-minutes`, minutes ready, its own Agents): the page's ⋯ offers
/// Hand to Agent…, the sheet picks the latest Agent; a try the hub keeps as failed (`/mock/meeting_handoff` fail) says why
/// in the sheet and, closed, on the page's row with Retry; Retry opens the sheet on the same Agent, and once it went the
/// row says where with Open. zh-Hans. PERSONA_SHOTS=<dir> saves handoff-<appearance>-<name>.png.
final class MeetingHandoffUITests: XCTestCase {
    @MainActor func testHandToAgentFailThenRetry() throws {
        let hub = try mockHub()
        // A fresh start with minutes written once the meeting is complete: the hub has something to hand.
        try post(hub, "/mock/recording_minutes", ["mode": "ready"])
        addTeardownBlock { [self] in try? post(hub, "/mock/recording_minutes", ["mode": "pending"]) }
        try post(hub, "/mock/meeting_handoff", ["fail": "send_failed"])
        addTeardownBlock { [self] in try? post(hub, "/mock/meeting_handoff", ["fail": ""]) }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "after-minutes"]
        app.launchPinned()

        // The ⋯ offers it once the hub has the meeting and an Agent is listed.
        let menu = element(app, "meeting-after-menu")
        XCTAssertTrue(menu.waitForExistence(timeout: 40), "no menu after the meeting:\n\(app.debugDescription)")
        let item = app.buttons["meeting-after-handoff"]
        XCTAssertTrue(waitFor(40) {
            menu.tap()
            if item.waitForExistence(timeout: 2) { return true }
            app.tap() // close the menu, the hub may not have the meeting yet
            return false
        }, "no Hand to Agent in the menu:\n\(app.debugDescription)")
        try shot(app, "menu")
        item.tap()

        // The sheet: the latest Agent picked, the transcript off.
        let send = element(app, "meeting-handoff-send")
        XCTAssertTrue(send.waitForExistence(timeout: 10), app.debugDescription)
        let agents = app.buttons.matching(identifier: "meeting-handoff-agent")
        XCTAssertGreaterThan(agents.count, 0, app.debugDescription)
        XCTAssertTrue(agents.firstMatch.isSelected, "the latest Agent isn't picked")
        let transcript = app.switches["meeting-handoff-transcript"]
        XCTAssertTrue(transcript.exists && (transcript.value as? String) == "0", "the transcript should be off")
        try shot(app, "sheet")

        // Kept as failed: why, in the sheet.
        send.tap()
        let problem = element(app, "meeting-handoff-problem")
        XCTAssertTrue(problem.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(problem.label.contains("消息没发出去"), problem.label)
        element(app, "sheet-close").tap()

        // The row: why, with Retry.
        let row = element(app, "meeting-handoff-row")
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        let retry = app.buttons["meeting-handoff-retry"]
        XCTAssertTrue(retry.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "没能交给")).firstMatch.exists, app.debugDescription)
        try shot(app, "failed")

        // Retry: the same Agent, and this time it goes.
        try post(hub, "/mock/meeting_handoff", ["fail": ""])
        retry.tap()
        XCTAssertTrue(send.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(agents.firstMatch.isSelected, "Retry didn't pick the same Agent")
        send.tap()
        let open = app.buttons["meeting-handoff-open"]
        XCTAssertTrue(open.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "已交给")).firstMatch.exists, app.debugDescription)
        try shot(app, "sent")
    }

    // MARK: Helpers

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    private func post(_ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    @MainActor private func waitFor(_ seconds: TimeInterval, _ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if ok() { return true }
            usleep(250_000)
        }
        return ok()
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/handoff-\(appearance)-\(name).png"))
    }
}
