import XCTest

/// A meeting both imported and recorded is one row (#8872, docs/assistant/meeting-minutes.md) against a mock hub that
/// lists a Lark meeting named as the made-up weekly meeting (MOCK_IMPORTED_MEETING=1, `-showMeetingSample after-minutes`):
/// the meeting's own page links to its Lark notes under the facts, and Knowledge's Meetings list shows the meeting once,
/// as the recording's row marked with Lark. zh-Hans. PERSONA_SHOTS=<dir> saves imported-<appearance>-<name>.png.
final class MeetingImportedUITests: XCTestCase {
    @MainActor func testOneRowAndItsNotes() throws {
        // The page after the meeting: once the hub has the recording, its Lark notes under the facts.
        var app = try launch(["-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "after-minutes"])
        let notes = element(app, "kb-imported-notes")
        XCTAssertTrue(notes.waitForExistence(timeout: 40), "no link to the imported notes:\n\(app.debugDescription)")
        XCTAssertTrue(notes.label.contains("飞书会议纪要"), notes.label)
        try shot(app, "page")
        app.terminate()

        // Knowledge's Meetings: the meeting once, the recording's row, saying it has Lark notes.
        app = try launch(["-tab", "assistant", "-openAssistantPage", "knowledge", "-showKnowledgeKind", "meetings",
                          "-showMeetingSample", "after-minutes"])
        let row = app.buttons["kb.recording"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no recording row:\n\(app.debugDescription)")
        let weekly = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "产品周会"))
        XCTAssertTrue(waitFor(10) { weekly.count == 1 }, "产品周会 is not one row: \(weekly.count)\n\(app.debugDescription)")
        XCTAssertTrue(row.label.contains("产品周会") && (row.value as? String ?? "").contains("飞书会议纪要"), "\(row.label) / \(String(describing: row.value))")
        try shot(app, "list")
    }

    // MARK: Helpers

    @MainActor private func launch(_ arguments: [String]) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair] + arguments
        app.launchPinned()
        return app
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
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/imported-\(appearance)-\(name).png"))
    }
}
