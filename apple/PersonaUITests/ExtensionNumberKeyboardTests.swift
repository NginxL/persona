import XCTest

/// Uses the existing integer picker fixture, without changing its schema: MOCK_EXTENSIONS=1 MOCK_EXTENSION_PICKERS=1.
final class ExtensionNumberKeyboardTests: XCTestCase {
    @MainActor func testLightNumberPadActions() async throws { try await exercise(scheme: "light", value: 8) }
    @MainActor func testDarkNumberPadActions() async throws { try await exercise(scheme: "dark", value: 9) }
    @MainActor func testLargestNumberPadActions() async throws { try await exercise(scheme: "light", value: 10, largest: true) }

    @MainActor private func exercise(scheme: String, value: Int, largest: Bool = false) async throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_EXTENSIONS_HUB"], env["PERSONA_EXTENSIONS_PICKERS"] == "1" else {
            throw XCTSkip("needs MOCK_EXTENSIONS=1 MOCK_EXTENSION_PICKERS=1 (PERSONA_EXTENSIONS_HUB, PERSONA_EXTENSIONS_PICKERS=1)")
        }
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openExtension", "example-agent-chat", "-ui.scheme", scheme,
                               "-UIPreferredContentSizeCategoryName", largest ? "UICTContentSizeCategoryAccessibilityXXXL" : "UICTContentSizeCategoryL"]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 2) { springboard.buttons[label].tap() }
        // Pairing can finish after the debug launch route and return to the chat. Open Settings through its real
        // drawer entry in that case; Settings applies the same extension route when it appears.
        let title = app.staticTexts["Example 聊天"].firstMatch
        if !title.waitForExistence(timeout: 5) {
            let drawer = app.buttons["assistant-sidebar-button"].firstMatch
            XCTAssertTrue(drawer.waitForExistence(timeout: 10), app.debugDescription)
            drawer.tap()
            let settings = app.drawerSettingsButton
            XCTAssertTrue(settings.waitForExistence(timeout: 10), app.debugDescription)
            settings.tap()
        }
        XCTAssertTrue(title.waitForExistence(timeout: 15), app.debugDescription)
        let field = app.textFields["plugin-field-agent_continue_hours"].firstMatch
        // Scroll only before editing; no swipe or drag is allowed between typing and saving.
        for _ in 0..<10 where !(field.exists && field.isHittable) { app.swipeUp() }
        XCTAssertTrue(field.exists && field.isHittable, app.debugDescription)
        field.tap()
        if let previous = field.value as? String, Double(previous) != nil {
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: previous.count))
        }
        field.typeText(String(value))
        XCTAssertEqual(field.value as? String, String(value))
        let done = app.buttons["plugin-keyboard-done"]
        let save = app.buttons["plugin-keyboard-save"]
        XCTAssertTrue(done.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertEqual(app.buttons.matching(identifier: "plugin-keyboard-done").count, 1)
        XCTAssertTrue(app.keyboards.firstMatch.exists)
        XCTAssertTrue(done.isEnabled && done.isHittable)
        XCTAssertTrue(save.isEnabled && save.isHittable, "Save must be reachable without scrolling with the number pad up")
        let look = largest ? "largest-light" : scheme
        try shot(app, "keyboard-" + look, env: env)
        save.tap()
        var saved: [String: Any] = [:]
        for _ in 0..<50 {
            saved = try await config(hub)
            if (saved["agent_continue_hours"] as? NSNumber)?.intValue == value { break }
            try await Task.sleep(for: .milliseconds(100))
        }
        XCTAssertEqual((saved["agent_continue_hours"] as? NSNumber)?.intValue, value, "The hub must receive the edited config")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "A successful save ends editing")
        XCTAssertFalse(app.buttons["plugin-keyboard-save"].exists)
        try shot(app, "saved-" + look, env: env)
        if let dir = env["PERSONA_NUMBER_RAW"] {
            let data = try JSONSerialization.data(withJSONObject: saved, options: [.prettyPrinted, .sortedKeys])
            try data.write(to: URL(fileURLWithPath: dir).appending(path: "saved-\(look).json"))
        }
        // Saving ended editing. Refocus the saved field to test Done on its own.
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        for _ in 0..<10 where !field.isHittable { app.swipeUp() }
        field.tap()
        XCTAssertTrue(done.waitForExistence(timeout: 5))
        XCTAssertTrue(app.keyboards.firstMatch.exists)
        XCTAssertFalse(save.isEnabled, "No unsaved edit remains after the hub accepted it")
        done.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "Done only dismisses the keyboard")
        XCTAssertFalse(app.buttons["plugin-keyboard-save"].exists)
        try shot(app, "done-" + look, env: env)
        app.terminate()
    }

    private func config(_ hub: String) async throws -> [String: Any] {
        var request = URLRequest(url: URL(string: hub + "/api/v1/extensions/example-agent-chat/-/")!)
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
        let detail = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        return try XCTUnwrap(detail["config"] as? [String: Any])
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String, env: [String: String]) throws {
        guard let dir = env["PERSONA_SHOTS"] else { return }
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "number-\(name).png"))
    }
}
