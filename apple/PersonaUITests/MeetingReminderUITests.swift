import XCTest

/// The calendar meeting reminder (#8731 G2, docs/apple/recording.md「Meeting recordings」) against a mock hub
/// (PERSONA_MOCK_HUB): Settings › Meetings has the switch, on by default, on every iPhone (the Persona rows only where
/// the hub offers attending: MOCK_NO_MEETING_ATTENDANCE=1 has none); a reminder's Start opens the room of the meeting named
/// after the event, and a plain tap the Meetings page. A notification can't be reached without the system's prompt, so
/// `-showMeetingReminder start|open` answers a made-up meeting's reminder through its own content. zh-Hans.
/// PERSONA_SHOTS=<dir> saves reminder-<appearance>-<name>[-<PERSONA_SHOTS_TAG>].png.
final class MeetingReminderUITests: XCTestCase {
    /// The switch, on, with the way to calendar access (the simulator's calendar was never asked for).
    @MainActor func testTheSwitchInSettings() throws {
        let app = try launch(["-tab", "settings", "-openMeetingSettings", "YES"])
        let toggle = app.switches["settings-meeting-reminders"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 20), "no reminder switch:\n\(app.debugDescription)")
        XCTAssertEqual(toggle.value as? String, "1", "the reminder is on by default")
        try shot(app, "settings")
    }

    /// Settings' Meetings row, on every iPhone (with no Persona role to show where the hub offers none).
    @MainActor func testTheRowOnEveryHub() throws {
        let app = try launch(["-tab", "settings"])
        let settings = app.descendants(matching: .any)["settings-list"].firstMatch
        XCTAssertTrue(settings.waitForExistence(timeout: 15), app.debugDescription)
        let neighbor = app.buttons["settings-knowledge-selection"]
        for _ in 0..<8 where !neighbor.isHittable { settings.swipeUp() }
        XCTAssertTrue(app.buttons["settings-meetings"].exists, "no Meetings row:\n\(app.debugDescription)")
        try shot(app, "list")
    }

    /// Start: the meeting named after the event records, in its room, as a real start begins it (a hub with the meeting
    /// conversation, Invite Persona Automatically on: Persona is in the room).
    @MainActor func testStartOpensTheRoom() throws {
        if let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] {
            try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
            try post(hub, "/mock/meeting_attendance", ["on": true, "default": "listener"])
            // The hub's default back to off for the later tests (a failure skips what follows, not a teardown block).
            addTeardownBlock { [hub] in Self.postNow(hub, "/mock/meeting_attendance", #"{"on": true, "default": "off"}"#) }
        }
        let app = try launch(["-tab", "assistant", "-showMeetingReminder", "start"])
        XCTAssertTrue(element(app, "recording-live-hide").waitForExistence(timeout: 25), "no meeting room:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["产品周会"].firstMatch.exists, "not the event's name:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "meeting-room-persona").waitForExistence(timeout: 10), "Persona isn't in the room:\n\(app.debugDescription)")
        try shot(app, "start")
    }

    /// A plain tap: the Meetings page, where the event's Upcoming row has the same Start.
    @MainActor func testATapOpensMeetings() throws {
        let app = try launch(["-tab", "assistant", "-showMeetingReminder", "open"])
        XCTAssertTrue(app.buttons["recording-meeting-start"].firstMatch.waitForExistence(timeout: 20), "not Meetings:\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "recording-live-hide").exists, "a tap started a meeting")
    }

    // MARK: Helpers

    @MainActor private func launch(_ arguments: [String]) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        XCUIDevice.shared.appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] == "dark" ? .dark : .light
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair] + arguments
        app.launchPinned()
        return app
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func post(_ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    /// A POST that waits for its answer, for a teardown block (no expectation there).
    private static func postNow(_ hub: String, _ path: String, _ json: String) {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "POST"
        request.httpBody = Data(json.utf8)
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: request) { _, _, _ in done.signal() }.resume()
        _ = done.wait(timeout: .now() + 5)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        let tag = env["PERSONA_SHOTS_TAG"].map { "-" + $0 } ?? ""
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/reminder-\(appearance)-\(name)\(tag).png"))
    }
}
