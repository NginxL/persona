import XCTest

/// Conversations an extension opened (#3758): All Chats filtered to Lark /agent lists Lark /agent's with its mark and
/// says where it came from, and the message that opened each says so under it (「来自 飞书 /agent」, 「来自扩展」).
/// Against scripts/mock/mock_hub.py with MOCK_AUTO_CONVERSATIONS=1 MOCK_EXTERNAL=1 (PERSONA_EXTERNAL_HUB). PERSONA_SHOTS
/// keeps the screenshots, light and dark.
final class ExternalConversationsTests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication!
    private static let otherExtension = "b2e4d6f8-0000-4000-8000-000000000310"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_EXTERNAL_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AUTO_CONVERSATIONS=1 MOCK_EXTERNAL=1 (PERSONA_EXTERNAL_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(_ scheme: String, open session: String? = nil, extra: [String] = []) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        var args = ["-pairURL", pair, "-tab", "assistant", "-ui.scheme", scheme, "-assistant.autoOpen", "YES"] + extra
        // `-newAssistant` would start on a new conversation after `-openAssistant` picked this one.
        args += session.map { ["-openAssistant", "server/" + $0] } ?? ["-newAssistant", "YES", "-dev.openChat", ""]
        app.launchArguments = args
        app.launchPinned()
    }

    /// The drawer's button on the phone; the iPad's column is there already. Returns the All Chats row.
    @MainActor private func openSidebar() -> XCUIElement {
        let menu = app.buttons["assistant-sidebar-button"].firstMatch
        let all = app.buttons["sidebar-all-chats"].firstMatch
        let end = Date().addingTimeInterval(20)
        while Date() < end && !menu.exists && !all.exists { usleep(250_000) }
        if all.exists && all.isHittable { return all }
        XCTAssertTrue(menu.exists, "no sidebar button:\n\(app.debugDescription)")
        sleep(1)
        menu.tapCenter()
        XCTAssertTrue(all.waitForExistence(timeout: 10), "no 全部对话 row:\n\(app.debugDescription)")
        return all
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let suffix = ProcessInfo.processInfo.environment["PERSONA_SHOTS_SUFFIX"] ?? ""
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/external-\(name)\(suffix).png"))
        }
    }

    @MainActor func testExternalConversationsSayWhereTheyCameFrom() {
        for scheme in ["light", "dark"] {
            launch(scheme)
            openSidebar().tap()

            // All Chats filtered to Lark /agent: its row says where it came from.
            let lark = app.buttons["auto-filter-lark_agent"].firstMatch
            XCTAssertTrue(lark.waitForExistence(timeout: 5), "no 飞书 /agent filter:\n\(app.debugDescription)")
            XCTAssertTrue(app.buttons["auto-filter-extension"].exists, "no 扩展 filter")
            lark.tap()
            let rows = app.buttons.matching(identifier: "auto-conversation")
            XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 5), "the list is empty")
            XCTAssertEqual(rows.count, 1, "飞书 /agent's list")
            XCTAssertTrue(rows.firstMatch.label.contains("来自 飞书 /agent"), rows.firstMatch.label)
            sleep(1)
            shot("list-\(scheme)")

            // Open, it says so under the message it was opened with.
            rows.firstMatch.tap()
            let line = app.descendants(matching: .any)["message-external-source"].firstMatch
            XCTAssertTrue(line.waitForExistence(timeout: 10), "no 「来自 飞书 /agent」 under the first message:\n\(app.debugDescription)")
            XCTAssertTrue(line.label.contains("来自 飞书 /agent"), line.label)
            sleep(1)
            shot("chat-lark-\(scheme)")
            app.terminate()

            // Another extension's: 「来自扩展」.
            launch(scheme, open: Self.otherExtension)
            let other = app.descendants(matching: .any)["message-external-source"].firstMatch
            XCTAssertTrue(other.waitForExistence(timeout: 20), "no 「来自扩展」:\n\(app.debugDescription)")
            XCTAssertTrue(other.label.contains("来自扩展"), other.label)
            sleep(1)
            shot("chat-extension-\(scheme)")
            app.terminate()
        }
    }

    /// The extension that opened them (#10358): with MOCK_EXTENSIONS=1 MOCK_OPENER_ICONS=1 the mock installs the Lark
    /// /agent conversation's opener as a made-up third-party extension with an icon (its name in PERSONA_OPENER_NAME,
    /// "Chat Bridge"), and its row in the sidebar's Recent and the line under its first message draw it and name it
    /// 「来自 Chat Bridge」 once its icon is in. Without MOCK_OPENER_ICONS nothing is installed: Lark's mark and
    /// 「来自 飞书 /agent」, as before. Light and dark, at the default and the largest text size; PERSONA_SHOTS keeps the
    /// screenshots, PERSONA_SHOTS_SUFFIX names the run.
    @MainActor func testTheOpenersIconAndName() {
        let name = ProcessInfo.processInfo.environment["PERSONA_OPENER_NAME"]
        let words = name.map { "来自 \($0)" } ?? "来自 飞书 /agent"
        let sizes: [(String, [String])] = [("default", []),
                                           ("ax5", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])]
        for (size, extra) in sizes {
            for scheme in ["light", "dark"] {
                // The sidebar's Recent: the row, named by its opener once that one's icon is in.
                launch(scheme, extra: extra)
                let menu = app.buttons["assistant-sidebar-button"].firstMatch
                XCTAssertTrue(menu.waitForExistence(timeout: 20), "no sidebar button:\n\(app.debugDescription)")
                sleep(1)
                menu.tapCenter()
                XCTAssertTrue(app.buttons["sidebar-main"].firstMatch.waitForExistence(timeout: 10), "no sidebar:\n\(app.debugDescription)")
                let row = app.buttons.matching(NSPredicate(format: "identifier == 'sidebar-conversation' AND label CONTAINS %@", words)).firstMatch
                for _ in 0..<8 where !(row.exists && row.isHittable && row.frame.maxY < app.frame.maxY - 40) {
                    // The drawer's own list, left of the page it slid from under.
                    app.coordinate(withNormalizedOffset: CGVector(dx: 0.25, dy: 0.75))
                        .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.25, dy: 0.45)))
                }
                XCTAssertTrue(row.waitForExistence(timeout: 15), "no Recent row saying \(words):\n\(app.debugDescription)")
                sleep(1)
                shot("opener-recent-\(size)-\(scheme)")
                app.terminate()

                // Opened: the line under the message it was opened with.
                launch(scheme, open: "b2e4d6f8-0000-4000-8000-000000000308", extra: extra)
                let line = app.descendants(matching: .any)
                    .matching(NSPredicate(format: "identifier == 'message-external-source' AND label CONTAINS %@", words)).firstMatch
                XCTAssertTrue(line.waitForExistence(timeout: 20), "no 「\(words)」 under the first message:\n\(app.debugDescription)")
                sleep(1)
                shot("opener-chat-\(size)-\(scheme)")
                app.terminate()
            }
        }
    }

    /// A conversation of the user's own says nothing of the kind.
    @MainActor func testTheUsersOwnSaysNothing() {
        launch("light", open: "b2e4d6f8-0000-4000-8000-000000000003")
        XCTAssertTrue(app.staticTexts["已经在日历里加好了。"].waitForExistence(timeout: 20), "the conversation did not open:\n\(app.debugDescription)")
        XCTAssertFalse(app.descendants(matching: .any)["message-external-source"].exists, "the user's own conversation says it came from outside")
    }
}
