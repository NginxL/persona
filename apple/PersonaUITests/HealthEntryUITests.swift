import XCTest

/// A sidebar Health pick opens the home even when the space already has conversations (#13273).
/// Uses scripts/mock/mock_hub.py's default Health history on iPhone and iPad.
final class HealthEntryUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
    }

    @MainActor func testHealthOpensHomeWithConversations() throws {
        try opensHome(layout: "sidebar", screenshots: true)
    }

    @MainActor func testHealthOpensHomeInSeparateLayout() throws {
        try opensHome(layout: "separate", screenshots: false)
    }

    @MainActor private func opensHome(layout: String, screenshots: Bool) throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant",
                               "-app.navigation", layout, "-newAssistant", "YES"]
        if let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] {
            app.launchArguments += ["-ui.scheme", appearance]
        }
        app.launchPinned()
        defer { app.terminate() }

        let health = app.buttons["sidebar-health"].firstMatch
        if !health.exists || !health.isHittable {
            let sidebar = app.buttons["assistant-sidebar-button"].firstMatch
            XCTAssertTrue(sidebar.waitForExistence(timeout: 20), "no sidebar button:\n\(app.debugDescription)")
            sidebar.tap()
        }
        XCTAssertTrue(health.waitForExistence(timeout: 20), "no Health in the sidebar:\n\(app.debugDescription)")
        health.tap()

        let today = app.descendants(matching: .any)["health-today"].firstMatch
        XCTAssertTrue(today.waitForExistence(timeout: 15), "Health did not open its home:\n\(app.debugDescription)")
        if screenshots { try shot(app, "home") }
        let upload = app.buttons["health-upload-report"].firstMatch
        for _ in 0..<5 where !upload.exists || !upload.isHittable { app.swipeUp() }
        XCTAssertTrue(upload.waitForExistence(timeout: 15), "the home's report upload is missing:\n\(app.debugDescription)")
        XCTAssertTrue(upload.isHittable, "the home did not show a reachable report upload")
        // List creates rows as they come into view; the existing chats can start below the first screen.
        let conversation = app.buttons.matching(identifier: "health-conversation").firstMatch
        for _ in 0..<5 where !conversation.exists || !conversation.isHittable { app.swipeUp() }
        XCTAssertTrue(conversation.waitForExistence(timeout: 15), "the home's existing chats are missing:\n\(app.debugDescription)")
        XCTAssertTrue(conversation.isHittable, "the home did not list a reachable health conversation")
        let chatTitle = app.buttons["chat.title"].firstMatch
        XCTAssertFalse(chatTitle.exists && chatTitle.isHittable, "Health resumed a conversation instead of its home")
        if screenshots { try shot(app, "conversations") }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
        let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        let filename = "health-entry-\(device)-\(appearance)-\(name)"
        attachment.name = filename
        attachment.lifetime = .keepAlways
        add(attachment)
        try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(filename).png"))
    }
}
