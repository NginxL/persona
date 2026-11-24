import XCTest

/// After the meeting, in one screen (#8731 V3, docs/design/DESIGN_SYSTEM.md "After the meeting"), against a mock hub
/// (PERSONA_MOCK_HUB) that writes the made-up sprint review's minutes with an overview (`POST /mock/recording_minutes`
/// `{"mode": "ready", "shape": "overview"}`, `-showMeetingSample after-overview`): the facts, the summary card with its
/// headline and counts, the to-dos (the minutes' Action Items), the meeting map and its Full Screen, the parts by name, and
/// the menu's Recording Details. With the minutes still being written (`after-pending`, `{"mode": "pending"}`) the summary
/// card says so. zh-Hans. PERSONA_SHOTS=<dir> saves after-<appearance>-<name>.png.
final class MeetingAfterUITests: XCTestCase {
    @MainActor func testTheScreenAfterTheMeeting() throws {
        let hub = try mockHub()
        try minutes(hub, ["mode": "ready", "shape": "overview"])
        let app = launch(hub, sample: "after-overview")
        let summary = element(app, "meeting-after-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 45), "no summary card:\n\(app.debugDescription)")
        XCTAssertTrue(waitFor(45) { summary.staticTexts["决定"].exists }, "the card has no counts:\n\(summary.debugDescription)")
        XCTAssertTrue(element(app, "meeting-after-facts").exists, "no facts line")
        XCTAssertTrue(element(app, "recording-wrapup-title").exists, "no title")
        try shot(app, "top")

        // The to-dos: the minutes' Action Items card, with Add to To-Dos.
        let todos = element(app, "minutes-card-actions")
        reveal(todos, in: app)
        XCTAssertTrue(todos.exists, "no to-dos:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["minutes-add"].firstMatch.exists, "no Add to To-Dos")

        // The map: the minutes' topics; Full Screen opens the meeting room's Map view, Done closes it.
        let map = element(app, "meeting-after-map")
        reveal(map, in: app)
        XCTAssertTrue(map.exists, "no map:\n\(app.debugDescription)")
        try shot(app, "map")
        app.buttons["meeting-after-map-full"].firstMatch.tap()
        XCTAssertTrue(element(app, "meeting-room-map").waitForExistence(timeout: 5), "Full Screen opened no map:\n\(app.debugDescription)")
        try shot(app, "map-full")
        app.buttons["meeting-map-done"].firstMatch.tap()
        XCTAssertTrue(element(app, "meeting-room-map").waitForNonExistence(timeout: 5), "the map stays")

        // The parts by name: Minutes first; Transcript shows the lines.
        let minutesTab = app.buttons["meeting-after-tab-minutes"].firstMatch
        reveal(minutesTab, in: app)
        XCTAssertTrue(minutesTab.isSelected, "Minutes isn't the first part:\n\(app.debugDescription)")
        app.buttons["meeting-after-tab-transcript"].firstMatch.tap()
        let line = element(app, "recording-timeline-sentence")
        reveal(line, in: app)
        XCTAssertTrue(line.exists, "Transcript shows no lines:\n\(app.debugDescription)")
        try shot(app, "transcript")

        // The menu: Recording Details opens the recording's page.
        app.swipeDown()
        app.swipeDown()
        let menu = app.buttons["meeting-after-menu"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 5), app.debugDescription)
        menu.tap()
        app.buttons["meeting-after-details"].firstMatch.tap()
        XCTAssertTrue(app.buttons["recording-delete"].firstMatch.waitForExistence(timeout: 10) || app.navigationBars["录音"].exists,
                      "Recording Details opened no recording page:\n\(app.debugDescription)")
    }

    /// The minutes still being written: the navigation title and the pending summary are both present.
    @MainActor func testTheMinutesOnTheirWay() throws {
        let hub = try mockHub()
        try minutes(hub, ["mode": "pending"])
        let app = launch(hub, sample: "after-pending")
        let summary = element(app, "meeting-after-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 45), "no summary card:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "meeting-after-minutes").waitForExistence(timeout: 30), "the card doesn't say where the minutes are")
        XCTAssertTrue(app.navigationBars.staticTexts["会议"].firstMatch.exists, "no Meeting title in the navigation bar")
        try shot(app, "pending")
    }

    // MARK: Helpers

    @MainActor private func launch(_ hub: String, sample: String) -> XCUIApplication {
        XCUIDevice.shared.appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] == "dark" ? .dark : .light
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", sample]
        app.launchPinned()
        return app
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    /// Scrolls until `target` is on screen (or a few swipes went by).
    @MainActor private func reveal(_ target: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<6 where !(target.exists && target.isHittable) { app.swipeUp() }
    }

    @MainActor private func waitFor(_ seconds: TimeInterval = 10, _ ok: () -> Bool) -> Bool {
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
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/after-\(appearance)-\(name).png"))
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    private func minutes(_ hub: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + "/mock/recording_minutes")!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: "minutes")
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }
}
