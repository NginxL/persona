import XCTest

/// Permission mode change lines in an Agent's chat (#10297, `ModeChangeLine`): the mock's widget agent with its three
/// changes (`MOCK_MODE_CHANGES=1` on the mock): iPhone's pick before its exchange, MacBook's pick of Allow Everything by a
/// restart after it, and that restart called off because the session runs in another process. `PERSONA_SHOTS` saves the
/// chat as `ios-mode-lines-<PERSONA_SHOTS_APPEARANCE>.png`. `testWithdrawingAWaitingRestartIsOneLine` picks through the
/// mock itself and wants a fresh mock: run it on its own.
///   PORT=<port> MOCK_MODE_CHANGES=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/ModeChangeLineTests
final class ModeChangeLineTests: XCTestCase {
    @MainActor func testTheAgentsChatShowsItsModeChanges() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MODE_CHANGES=1 (PERSONA_MOCK_HUB)") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + (hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? "") + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001"]
        app.launchPinned()
        let lines = app.descendants(matching: .any).matching(identifier: "chat.modeChange")
        XCTAssertTrue(lines.firstMatch.waitForExistence(timeout: 30), "no mode change line in the chat")
        sleep(3) // the second look at the changes, and the list settling
        let labels = lines.allElementsBoundByIndex.map(\.label)
        // the two after the exchange, at the chat's end: who picked what, and the restart called off with why
        XCTAssertTrue(labels.contains { $0.hasPrefix("MacBook") }, "\(labels)")
        XCTAssertTrue(labels.contains { $0.contains("会话换了进程") || $0.contains("another process") }, "\(labels)")
        if let dir = env["PERSONA_SHOTS"] {
            let look = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "ios-mode-lines-\(look).png"))
        }
    }

    /// Withdrawing a restart into Full access that waits (#11089): Phone picks Full access for the widget agent, whose
    /// Claude has no bypass flag, then the mode it runs in again before the turn ends. The chat says once that the restart
    /// was called off and the session stays in its mode: not that another switch took its place, and no "changed the
    /// permission mode" line, since nothing changed. Picks through the mock's /modes as the hub would; needs a mock of its
    /// own (no MOCK_MODE_CHANGES needed). `PERSONA_SHOTS` saves the chat as `ios-mode-cancel-<PERSONA_SHOTS_APPEARANCE>.png`
    /// before the checks, so a run against an older mock shows what it drew.
    @MainActor func testWithdrawingAWaitingRestartIsOneLine() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        continueAfterFailure = false
        let session = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
        for mode in ["bypassPermissions", "acceptEdits"] { // Full access by a restart, then the mode it runs in
            var r = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/sessions/" + session + "/modes")))
            r.httpMethod = "POST"
            r.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
            r.setValue("application/json", forHTTPHeaderField: "Content-Type")
            r.httpBody = try JSONSerialization.data(withJSONObject: ["permission_mode": mode])
            _ = try await URLSession.shared.data(for: r)
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + (hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? "") + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-openSession", session]
        app.launchPinned()
        let lines = app.descendants(matching: .any).matching(identifier: "chat.modeChange")
        let calledOff = lines.matching(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS %@", "已取消", "Called off"))
        XCTAssertTrue(calledOff.firstMatch.waitForExistence(timeout: 30), "no line for the withdrawn restart")
        try await Task.sleep(for: .seconds(3)) // the second look at the changes, and the list settling
        if let dir = env["PERSONA_SHOTS"] {
            let look = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "ios-mode-cancel-\(look).png"))
        }
        let labels = lines.allElementsBoundByIndex.map(\.label)
        XCTAssertTrue(labels.contains { $0.contains("会话保持当前模式") || $0.contains("stays in its current mode") }, "\(labels)")
        XCTAssertFalse(labels.contains { $0.contains("被另一次切换取代") || $0.contains("another switch took its place") }, "\(labels)")
        XCTAssertFalse(labels.contains { $0.hasPrefix("Phone") && ($0.contains("改成了") || $0.contains("changed the permission mode")) },
                       "\(labels)")
    }
}
