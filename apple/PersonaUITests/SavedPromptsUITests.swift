import XCTest

/// Assistant Profile › Saved Prompts (#9436) against scripts/mock/mock_hub.py with MOCK_SAVED_PROMPTS=1
/// (PERSONA_MOCK_HUB): a row's swipe offers Delete, Delete asks first, and Cancel keeps the row and sends nothing; the
/// editor has the sheet's ✕ and ✓, and its ✕ leaves without saving (#11183). A mock
/// without saved prompts skips. With PERSONA_SHOTS set it writes what it saw there (PERSONA_SHOTS_APPEARANCE names the
/// simulator's appearance in the file names).
final class SavedPromptsUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_SAVED_PROMPTS=1 (PERSONA_MOCK_HUB)")
        }
        hub = value
    }

    /// The names the mock lists now.
    @MainActor private func names() async throws -> [String] {
        var request = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/assistant/saved-prompts")))
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let body = try JSONSerialization.jsonObject(with: try await URLSession.shared.data(for: request).0) as? [String: Any]
        return (body?["prompts"] as? [[String: Any]] ?? []).compactMap { $0["name"] as? String }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try image.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/saved-prompts-\(appearance)-\(name).png"))
    }

    /// The page with the mock's prompts, and its /review row (the copy on screen: `-openAssistantPage` opens the page
    /// under the Persona tab as well).
    @MainActor private func open() async throws -> (XCUIApplication, XCUIElement) {
        guard try await names().contains("review") else {
            throw XCTSkip("this mock hub has no saved prompts: start it with MOCK_SAVED_PROMPTS=1")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", "", "-openAssistantPage", "prompts",
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launchPinned()
        // A fresh simulator asks for notifications over everything at the first launch.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["Allow", "允许"] where springboard.buttons[label].waitForExistence(timeout: 3) {
            springboard.buttons[label].tap()
        }
        let row = app.buttons.matching(identifier: "saved-prompt-review").allElementsBoundByIndex.first { $0.isHittable }
            ?? app.buttons["saved-prompt-review"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no saved prompts:\n\(app.debugDescription)")
        return (app, row)
    }

    @MainActor func testASwipeAsksBeforeDeletingAndCancelKeepsIt() async throws {
        let before = try await names()
        let (app, row) = try await open()
        row.swipeLeft()
        let delete = app.buttons["Delete"].firstMatch
        XCTAssertTrue(delete.waitForExistence(timeout: 5), "no Delete on the swipe:\n\(app.debugDescription)")
        try shot(app, "swipe")

        delete.tap()
        let question = app.staticTexts["Delete “/review”?"].firstMatch
        XCTAssertTrue(question.waitForExistence(timeout: 5), "the swipe deleted without asking:\n\(app.debugDescription)")
        try shot(app, "confirm")
        let cancel = app.buttons["Cancel"].firstMatch
        if cancel.exists { cancel.tap() } else { app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1)).tap() } // iPad's popover
        XCTAssertTrue(question.waitForNonExistence(timeout: 5))
        XCTAssertTrue(row.exists, "Cancel kept the row")
        let after = try await names()
        XCTAssertEqual(after, before, "nothing was sent")
    }

    @MainActor func testTheEditorClosesFromItsCornerWithoutSaving() async throws {
        let before = try await names()
        let (app, row) = try await open()
        row.tap()
        let close = app.buttons["sheet-close"].firstMatch, save = app.buttons["saved-prompt-save"].firstMatch
        XCTAssertTrue(close.waitForExistence(timeout: 5), "no ✕ on the editor:\n\(app.debugDescription)")
        XCTAssertTrue(save.exists, "no ✓ on the editor")
        XCTAssertLessThan(close.frame.midX, save.frame.midX, "✕ at the leading corner, ✓ at the trailing one")
        let name = app.textFields["saved-prompt-name"].firstMatch
        name.tap()
        name.typeText("x")
        try shot(app, "editor")
        close.tap()
        XCTAssertTrue(close.waitForNonExistence(timeout: 5), "the ✕ left the editor open")
        let after = try await names()
        XCTAssertEqual(after, before, "the ✕ saved nothing")
    }
}
