import XCTest

/// A connector's Inbox rows wear their conversations' pictures (#10923), on scripts/mock/mock_hub.py with
/// MOCK_TELEGRAM=signed_in MOCK_TELEGRAM_INBOX=1 (PERSONA_MOCK_HUB): Telegram's private chat shows the other person's
/// picture and its group mention the group's, from the hub's avatars route, with Telegram's mark in the corner; a hub
/// before the route (`/mock/telegram_avatars?mode=before`: no `avatar_url`) leaves both on their initials, as every row
/// was drawn until now. It sets the mode itself and puts it back. Light and dark are attached, and kept in
/// TEST_RUNNER_PERSONA_SHOTS=<dir> when set.
final class InboxConnectorAvatarUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    /// The conversation pages use the same shared loader for their participants (#10994).
    @MainActor func testConversationAvatars() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_THREAD_AVATARS"] else {
            throw XCTSkip("needs the synthetic Telegram and GitHub Inbox threads from scripts/mock/mock_hub.py")
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        for (source, item, marker) in [("telegram", "telegram:dm1", "thread.box"), ("github", "github:review12", "github.description")] {
            for (scheme, largest) in [("light", false), ("dark", false), ("light", true), ("dark", true)] {
                let app = XCUIApplication()
                app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-openInboxItem", item, "-ui.scheme", scheme]
                if largest { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL", "-ui.textSize", "large"] }
                app.launchPinned()
                let content = app.descendants(matching: .any).matching(identifier: marker).firstMatch
                XCTAssertTrue(content.waitForExistence(timeout: 25), "\(source) thread missing\n\(app.debugDescription)")
                dismissStartupAlerts()
                shot(app, "thread-\(source)-\(scheme)" + (largest ? "-largest" : ""), env)
                app.terminate()
            }
        }
    }

    @MainActor func testTelegramRowsWearTheirPictures() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TELEGRAM=signed_in MOCK_TELEGRAM_INBOX=1 (PERSONA_MOCK_HUB)")
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        defer { _ = try? Data(contentsOf: URL(string: hub + "/mock/telegram_avatars?mode=1")!) }
        for (mode, phase) in [("before", "before"), ("1", "after")] {
            _ = try Data(contentsOf: URL(string: hub + "/mock/telegram_avatars?mode=" + mode)!)
            for scheme in ["light", "dark"] {
                let app = XCUIApplication()
                // Telegram's rows alone (its source chip), so both are on the first screen under every other source's.
                app.launchArguments = ["-pairURL", pair, "-tab", "mail", "-inbox.kind", "", "-inbox.source", "telegram", "-ui.scheme", scheme]
                app.launchPinned()
                dismissStartupAlerts()
                func text(_ s: String) -> XCUIElement {
                    app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
                }
                XCTAssertTrue(text("周末的示例活动还去吗").waitForExistence(timeout: 20), "\(phase) \(scheme): no Telegram private chat\n\(app.debugDescription)")
                XCTAssertTrue(text("这个示例方案你怎么看").exists, "\(phase) \(scheme): no Telegram group mention")
                // The pictures come from the hub once; give them the moment a first load takes.
                Thread.sleep(forTimeInterval: 2)
                shot(app, "inbox-telegram-avatars-\(phase)-\(scheme)", env)
                app.terminate()
            }
        }
    }

    @MainActor private func dismissStartupAlerts() {
        let alert = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch
        // SpringBoard may show both notification permission and the icon-change notice on a fresh install.
        let labels = ["不允许", "Don’t Allow", "Don't Allow", "好", "OK"] // i18n-ignore: SpringBoard's own buttons
        for _ in 0..<2 {
            guard alert.waitForExistence(timeout: 2) else { return }
            guard let button = labels.map({ alert.buttons[$0] }).first(where: \.exists) else {
                XCTFail("Unexpected SpringBoard alert: \(alert.label)")
                return
            }
            button.tap()
        }
        XCTAssertTrue(alert.waitForNonExistence(timeout: 3))
    }

    @MainActor private func shot(_ app: XCUIApplication, _ file: String, _ env: [String: String]) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = file
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["PERSONA_SHOTS"] {
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file + ".png"))
        }
    }
}
