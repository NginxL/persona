import XCTest

/// Permission mode change lines in an Agent's chat (#10297, `ModeChangeLine`): the mock's widget agent with its three
/// changes (`MOCK_MODE_CHANGES=1` on the mock): iPhone's pick before its exchange, MacBook's pick of Allow Everything by a
/// restart after it, and that restart called off because the session runs in another process. `PERSONA_SHOTS` saves the
/// chat as `ios-mode-lines-<PERSONA_SHOTS_APPEARANCE>.png`.
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
}
