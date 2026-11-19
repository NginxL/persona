import XCTest

/// `/permissions` typed and sent (#6513): in a Persona conversation, whose composer has no "/" list on the phone, it opens
/// the mode list instead of going to the assistant (#7996 review); with a quoted context attached it stays a message.
/// Needs scripts/mock/mock_hub.py (`PERSONA_MOCK_HUB`); `PERSONA_SHOTS` saves the sheet.
final class PermissionsCommandUITests: XCTestCase {
    private var hub = ""
    /// The mock's main Persona conversation.
    private let persona = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    @MainActor func testTypedInAPersonaConversationOpensTheModeList() throws {
        let app = XCUIApplication()
        let url = URL(string: hub)!
        let pair = "persona://pair?url=" + (url.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics) ?? "") + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openChatsFresh", "YES", "-tab", "assistant", "-openAssistant", persona]
        app.launchPinned()
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no composer")
        let before = try sentCount()
        field.tap()
        // A draft kept from an earlier run would make it another message: start from an empty box.
        if let old = field.value as? String, !old.isEmpty, old != field.placeholderValue {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: old.count))
        }
        field.typeText("/permissions")
        app.buttons.matching(NSPredicate(format: "identifier == %@ OR label == %@", "shell-composer-send", "发送")).firstMatch.tap()
        let sheet = app.descendants(matching: .any)["permissions.sheet"].firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 8), "the mode list did not open")
        XCTAssertTrue(app.descendants(matching: .any)["permissions.bypassPermissions"].exists, "Allow Everything is not listed")
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "ios-persona-typed-\(look).png"))
        }
        // Nothing went to the assistant.
        XCTAssertEqual(try sentCount(), before, "/permissions was sent as a message")
    }

    /// How many messages the mock has taken so far (`GET /mock/sent` keeps them across resets).
    private func sentCount() throws -> Int {
        let json = try JSONSerialization.jsonObject(with: Data(contentsOf: URL(string: hub + "/mock/sent")!)) as? [String: Any]
        return (json?["sent"] as? [Any])?.count ?? 0
    }
}
