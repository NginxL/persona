import XCTest

/// The recipes agent's transcript with MOCK_MODEL_CHANGE=1: Opus answered its first exchange and Sonnet its second, so
/// one non-interactive separator sits between the second message and its reply. Runs in light, dark and largest text;
/// PERSONA_SHOTS saves `ios-model-change-<appearance>.png` for visual review. A separate chat covers canonical and unknown.
///   PORT=<port> MOCK_MODEL_CHANGE=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MODEL_CHANGE_HUB=http://127.0.0.1:<port> xcodebuild test … -only-testing:PersonaUITests/ModelChangeUITests
final class ModelChangeUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private let session = "macbook/a1f3c2d4-0000-4000-8000-000000000007"
    private let canonicalSession = "server/ses_e560fd3e1ffeHgJk8LzCv5MdWq"

    @MainActor private func launch(_ hub: String, session: String? = nil, appearance: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-openSession", session ?? self.session,
                               "-ui.style", "glass"] + appearance
        app.launchPinned()
        return app
    }

    @MainActor private func labeled(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", text)).firstMatch
    }

    @MainActor private func shot(_ app: XCUIApplication, _ appearance: String) throws {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try XCUIScreen.main.screenshot().pngRepresentation.write(
            to: URL(fileURLWithPath: dir).appending(path: "ios-model-change-\(appearance).png"))
    }

    @MainActor private func hub() throws -> String {
        guard let hub = env["PERSONA_MODEL_CHANGE_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MODEL_CHANGE=1 (PERSONA_MODEL_CHANGE_HUB)")
        }
        return hub
    }

    @MainActor func testKnownModelChangeHasAnAccessibleSeparatorInLightDarkAndLargestText() throws {
        continueAfterFailure = false
        for appearance in ["light", "dark", "large"] {
            let look = appearance == "large"
                ? ["-ui.scheme", "light", "-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
                : ["-ui.scheme", appearance]
            let app = launch(try hub(), appearance: look)
            let reply = labeled(app, "排好了：用时短的菜谱排在前面，同样用时的按收藏数。")
            XCTAssertTrue(reply.waitForExistence(timeout: 30), "the recipes chat did not load:\n\(app.debugDescription)")
            acceptIconAlert(timeout: 2)
            let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
            XCTAssertTrue(board.alerts.firstMatch.waitForNonExistence(timeout: 3), "a system alert covers the divider screenshot")

            let divider = app.descendants(matching: .any).matching(identifier: "chat.modelChange").firstMatch
            XCTAssertTrue(divider.waitForExistence(timeout: 10), "no divider for the model that actually answered:\n\(app.debugDescription)")
            let words = divider.label
            XCTAssertTrue(words == "模型已从 Opus 5.5 切换到 Sonnet 5.5" || words == "Model changed from Opus 5.5 to Sonnet 5.5",
                          "the separator must name both friendly models, not the raw ids: \(words)")
            XCTAssertNotEqual(divider.elementType, .button, "a model change is information, not a control")
            XCTAssertTrue(divider.isHittable, "the screenshot must show the divider unobscured")
            try shot(app, appearance)
        }
    }

    @MainActor func testCanonicalAliasesAndUnknownModelsHaveNoDivider() throws {
        continueAfterFailure = false
        let app = launch(try hub(), session: canonicalSession, appearance: ["-ui.scheme", "light"])
        let reply = labeled(app, "旧节点没有记录回答模型。")
        XCTAssertTrue(reply.waitForExistence(timeout: 30), "the canonical/unknown chat did not load:\n\(app.debugDescription)")
        XCTAssertEqual(app.descendants(matching: .any).matching(identifier: "chat.modelChange").count, 0,
                       "aliases, snapshots and unknown models are not model changes")
    }
}
