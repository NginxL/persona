import XCTest

/// A synthetic loopback hub with MOCK_STARRED=1. Run on dedicated compact and regular iOS simulators.
final class StarredMessagesUITests: XCTestCase {
    private let reviewSession = "a1f3c2d4-0000-4000-8000-000000000006"
    private let reply = "PR #42 看完了"
    private let original = "Original words from the reused line"
    private var hub: URL!
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let raw = env["PERSONA_STARRED_HUB"], let url = URL(string: raw), url.host == "127.0.0.1" else {
            throw XCTSkip("Requires a dedicated loopback MOCK_STARRED=1 hub.")
        }
        hub = url
    }

    @MainActor func testAgentReplyStarCopyUndoAndOpenOriginal() async throws {
        try await clear()
        let app = launch(agent: true)
        let message = text(reply, app: app)
        XCTAssertTrue(message.waitForExistence(timeout: 30), "the Agent reply is missing")
        press(message, in: app)
        let star = element("chat.starMessage", app: app)
        XCTAssertTrue(star.waitForExistence(timeout: 8), "Star is missing from the existing message menu")
        shot(app, "agent-menu")
        star.tap()
        XCTAssertTrue(element("chat-starred-mark", app: app).waitForExistence(timeout: 12))

        openStarred(app, fromAgent: true)
        XCTAssertTrue(text(reply, app: app).waitForExistence(timeout: 12), "the saved reply is missing")
        shot(app, "agent-snapshot")
        element("starred-copy", app: app).tap()
        XCTAssertTrue(text("Copied", app: app).waitForExistence(timeout: 8))

        element("starred-unstar", app: app).tap()
        let undo = app.buttons.matching(NSPredicate(format: "label == %@", "Undo")).firstMatch
        XCTAssertTrue(undo.waitForExistence(timeout: 12), "Unstar did not offer Undo")
        undo.tap()
        XCTAssertTrue(text(reply, app: app).waitForExistence(timeout: 12), "Undo did not restore the snapshot")

        let saved = try JSONSerialization.jsonObject(with: await request("GET", "/api/v1/assistant/starred")) as? [String: Any]
        let savedID = try XCTUnwrap((saved?["stars"] as? [[String: Any]])?.first?["item_id"] as? String)

        element("starred-open-original", app: app).tap()
        let highlighted = app.descendants(matching: .any).matching(identifier: "probe.chat")
            .matching(NSPredicate(format: "value CONTAINS %@ AND value CONTAINS %@", "highlighted", savedID)).firstMatch
        XCTAssertTrue(highlighted.waitForExistence(timeout: 15), "Open Original did not highlight the saved item")
        XCTAssertTrue(element("chat.answer.\(savedID)", app: app).isHittable, "the original message is not on the active page")
        shot(app, "agent-original")
        try await clear()
    }

    @MainActor func testCrossDeviceSearchAndRemovedOriginalRemainReadable() async throws {
        try await clear()
        try await request("POST", "/mock/starred-source", ["phase": "original"])
        let app = launch(agent: false)
        openStarred(app)
        let seed: [String: Any] = ["machine_id": "macbook", "session_id": reviewSession, "item_id": "line900#0",
            "conversation": "agent", "role": "user", "title": "Saved Design Review", "speaker": "Reviewer",
            "text": original, "source_fingerprint": String(repeating: "a", count: 32)]
        try await request("POST", "/api/v1/assistant/starred", seed)
        XCTAssertTrue(text("Saved Design Review", app: app).waitForExistence(timeout: 15), "another device's snapshot did not arrive")
        let search = app.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 6))
        search.tap()
        let unmatched = "No match at all"
        search.typeText(unmatched)
        XCTAssertTrue(text("Saved Design Review", app: app).waitForNonExistence(timeout: 8))
        search.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: unmatched.count))
        search.tap()
        search.typeText("Reviewer")
        XCTAssertTrue(text("Saved Design Review", app: app).waitForExistence(timeout: 8), "search did not match the displayed speaker")
        let cancel = app.buttons["Cancel"].firstMatch
        if cancel.exists { cancel.tap() }

        try await request("POST", "/mock/starred-source", ["phase": "unreadable"])
        element("starred-open-original", app: app).tap()
        XCTAssertTrue(text("Couldn't check the original message. Try again.", app: app).waitForExistence(timeout: 12))
        XCTAssertTrue(element("starred-open-original", app: app).exists)
        try await request("POST", "/mock/starred-source", ["phase": "reset"])
        element("starred-open-original", app: app).tap()
        XCTAssertTrue(text("The original message is unavailable.", app: app).waitForExistence(timeout: 12))
        XCTAssertTrue(text(original, app: app).exists)
        XCTAssertTrue(element("starred-copy", app: app).exists)
        XCTAssertTrue(element("starred-unstar", app: app).exists)
        shot(app, "unavailable-snapshot")
        try await clear()
    }

    @MainActor func testAssistantUserMessageUsesItsOwnConversationKind() async throws {
        try await clear()
        let app = launch(agent: false, showSidebar: false)
        let question = text("今天有什么需要我处理的？", app: app) // i18n-ignore: synthetic hub transcript
        XCTAssertTrue(question.waitForExistence(timeout: 30), "the Assistant question is missing")
        press(question, in: app)
        let action = element("chat.starMessage", app: app)
        XCTAssertTrue(action.waitForExistence(timeout: 8))
        action.tap()
        XCTAssertTrue(element("chat-starred-mark", app: app).waitForExistence(timeout: 12))

        let body = try JSONSerialization.jsonObject(with: await request("GET", "/api/v1/assistant/starred")) as? [String: Any]
        let star = try XCTUnwrap((body?["stars"] as? [[String: Any]])?.first)
        XCTAssertEqual(star["conversation"] as? String, "assistant")
        XCTAssertEqual(star["role"] as? String, "user")
        try await clear()
    }

    @MainActor private func launch(agent: Bool, showSidebar: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        let url = hub.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
        let appearance = env["PERSONA_STARRED_APPEARANCE"] ?? "light"
        app.launchArguments = ["-pairURL", "persona://pair?url=\(url)&token=mock", "-probes", "YES", "-AppleLanguages", "(en)",
                               "-AppleLocale", "en_US", "-ui.scheme", appearance]
        if agent {
            app.launchArguments += ["-tab", "dev", "-openSession", "macbook/\(reviewSession)"]
        } else {
            app.launchArguments += ["-tab", "assistant", "-app.tab", "assistant"]
            if showSidebar { app.launchArguments += ["-openAssistantSidebar", "YES"] }
        }
        if env["PERSONA_STARRED_LARGE"] == "1" {
            app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        }
        app.launchPinned()
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons["Allow"].firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        return app
    }

    @MainActor private func openStarred(_ app: XCUIApplication, fromAgent: Bool = false) {
        let row = element("sidebar-starred", app: app)
        if fromAgent, !row.exists || !row.isHittable {
            let assistant = element("shell-assistant", app: app)
            if !assistant.exists || !assistant.isHittable {
                let back = element("chevron.left", app: app)
                if back.exists, back.isHittable { back.tap() }
            }
            if assistant.waitForExistence(timeout: 6), assistant.isHittable { assistant.tap() }
        }
        if !row.exists || !row.isHittable {
            let openers = ["assistant-sidebar-button", "sidebar-drawer-button", "chat-sidebar-button", "shell-place-sidebar", "ToggleSidebar"]
            for id in openers where !row.exists || !row.isHittable {
                let opener = element(id, app: app)
                if opener.exists, opener.isHittable {
                    opener.tap()
                    _ = row.waitForExistence(timeout: 4)
                }
            }
        }
        if !row.exists || !row.isHittable {
            let window = app.windows.firstMatch
            window.coordinate(withNormalizedOffset: CGVector(dx: 0.2, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)))
        }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "Starred is missing from Pages:\n\(app.debugDescription)")
        row.tap()
        XCTAssertTrue(element("starred-messages-page", app: app).waitForExistence(timeout: 14))
    }

    @MainActor private func press(_ row: XCUIElement, in app: XCUIApplication) {
        let window = app.windows.firstMatch.frame
        var frame = row.frame
        if !window.contains(CGPoint(x: frame.midX, y: frame.midY)) { frame.origin.y = window.height - frame.maxY }
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX, dy: frame.midY)).press(forDuration: 0.6)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] {
            try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "\(name).png"))
        }
    }

    private func element(_ id: String, app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func text(_ words: String, app: XCUIApplication) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", words)).firstMatch
    }

    @discardableResult private func request(_ method: String, _ path: String, _ json: [String: Any]? = nil) async throws -> Data {
        var req = URLRequest(url: try XCTUnwrap(URL(string: path, relativeTo: hub)))
        req.httpMethod = method
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        if let json {
            req.setValue("application/json", forHTTPHeaderField: "Content-Type")
            req.httpBody = try JSONSerialization.data(withJSONObject: json)
        }
        let (data, response) = try await URLSession.shared.data(for: req)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200, "\(method) \(path)")
        return data
    }

    private func clear() async throws {
        let body = try JSONSerialization.jsonObject(with: await request("GET", "/api/v1/assistant/starred")) as? [String: Any]
        for id in (body?["stars"] as? [[String: Any]] ?? []).compactMap({ $0["id"] as? String }) {
            try await request("DELETE", "/api/v1/assistant/starred/\(id)")
        }
        try await request("POST", "/mock/starred-source", ["phase": "reset"])
    }
}
