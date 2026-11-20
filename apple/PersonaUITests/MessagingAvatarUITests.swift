import XCTest

/// Run against scripts/mock/messaging_avatar_hub.py. Both portraits and all names are synthetic.
final class MessagingAvatarUITests: XCTestCase {
    @MainActor func testMessagingPicturesInListsAndChats() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_MESSAGING_AVATARS"] else { throw XCTSkip("needs the messaging avatar fixture") }
        let before = env["PERSONA_MESSAGING_AVATARS_BASELINE"] == "1"
        let largest = env["PERSONA_MESSAGING_AVATARS_LARGEST"] == "1"
        let app = XCUIApplication()
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let cases = [("en", "en_US", "light"), ("zh-Hans", "zh_CN", "light"), ("ja", "ja_JP", "light"),
                     ("en", "en_US", "dark"), ("zh-Hans", "zh_CN", "dark"), ("ja", "ja_JP", "dark")]
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", ""]
        app.launchPinned()
        try await Task.sleep(for: .seconds(2))
        app.terminate()
        for (lang, locale, theme) in cases where (!before && !largest) || lang == "en" {
            app.launchArguments = ["-pairURL", pair, "-tab", "feishu", "-demo.on", "NO", "-dev.openChat", "",
                                   "-integrations.lark.enabled", "YES", "-larkTop", "NO", "-ui.scheme", theme,
                                   "-AppleLanguages", "(\(lang))", "-AppleLocale", locale]
            if largest {
                app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL", "-ui.textSize", "large"]
            }
            let suffix = largest ? "-largest" : ""
            app.launchPinned()
            let tab = app.buttons["shell-feishu"].firstMatch
            if tab.waitForExistence(timeout: 8) { tab.tapCenter() }
            let group = app.buttons["feishu-chat-studio"].firstMatch
            XCTAssertTrue(group.waitForExistence(timeout: 18), app.debugDescription)
            XCTAssertTrue(app.buttons["feishu-chat-robin"].firstMatch.exists)
            XCTAssertTrue(app.buttons["feishu-chat-avery"].firstMatch.exists)
            try await Task.sleep(for: .seconds(1))
            try capture(app, "messaging-\(lang)-\(theme)-list\(suffix)", env["PERSONA_SHOTS"])
            group.tapCenter()
            let content = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "The river study is ready.")).firstMatch
            XCTAssertTrue(content.waitForExistence(timeout: 12), app.debugDescription)
            try await Task.sleep(for: .seconds(1))
            try capture(app, "messaging-\(lang)-\(theme)-chat\(suffix)", env["PERSONA_SHOTS"])
            app.terminate()
        }
        if !before {
            let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/messaging-avatars")!)
            let report = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: [[String: Any]]])
            let reads = try XCTUnwrap(report["reads"])
            XCTAssertTrue(reads.contains { $0["picture"] as? String == "room" && $0["route"] as? String == "avatars"
                && $0["account"] as? String == "1" && [64, 128, 256, 512].contains($0["size"] as? Int ?? 0) })
            XCTAssertTrue(reads.contains { $0["picture"] as? String == "person" && $0["route"] as? String == "avatars" })
        }
    }

    @MainActor private func capture(_ app: XCUIApplication, _ name: String, _ dir: String?) throws {
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir { try image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png")) }
    }
}
