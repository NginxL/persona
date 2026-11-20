import XCTest

/// Run against apple/verify/feishu_avatar_hub.py. All account names, messages and pictures are synthetic.
final class FeishuAvatarUITests: XCTestCase {
    @MainActor func testFeishuPicturesAndInitialsAtTheirNormalEntrypoints() async throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_FEISHU_AVATAR_FIXTURE"] else { throw XCTSkip("needs the Feishu avatar fixture") }
        let before = env["PERSONA_FEISHU_AVATAR_BEFORE"] == "1"
        let textSize = env["PERSONA_FEISHU_AVATAR_TEXT"]
        let app = XCUIApplication()
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", ""]
        app.launchPinned()
        try await Task.sleep(for: .seconds(2))
        app.terminate()
        for (lang, locale, theme, largest) in [
            ("en", "en_US", "light", false), ("zh-Hans", "zh_CN", "light", false), ("ja", "ja_JP", "light", false),
            ("en", "en_US", "dark", false), ("zh-Hans", "zh_CN", "dark", false), ("ja", "ja_JP", "dark", false),
            ("en", "en_US", "light", true), ("en", "en_US", "dark", true)
        ] where (!before || (lang == "en" && !largest)) && (textSize == nil || (textSize == "largest") == largest) {
            app.launchArguments = ["-pairURL", pair, "-tab", "feishu", "-demo.on", "NO", "-dev.openChat", "",
                                   "-integrations.lark.enabled", "YES", "-larkTop", "NO", "-ui.scheme", theme,
                                   "-AppleLanguages", "(\(lang))", "-AppleLocale", locale]
            if largest {
                app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL", "-ui.textSize", "large"]
            }
            let suffix = "\(lang)-\(theme)" + (largest ? "-largest" : "")
            app.launchPinned()
            let tab = app.buttons["shell-feishu"].firstMatch
            if tab.waitForExistence(timeout: 8) { tab.tapCenter() }
            let group = app.buttons["feishu-chat-studio"].firstMatch
            XCTAssertTrue(group.waitForExistence(timeout: 18), app.debugDescription)
            XCTAssertTrue(app.buttons["feishu-chat-quiet"].firstMatch.exists)
            try await Task.sleep(for: .seconds(1))
            try capture(app, "feishu-list-" + suffix, env["PERSONA_SHOTS"])
            group.tapCenter()
            let line = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "The river study is ready.")).firstMatch
            XCTAssertTrue(line.waitForExistence(timeout: 14), app.debugDescription)
            try await Task.sleep(for: .seconds(1))
            try capture(app, "feishu-chat-" + suffix, env["PERSONA_SHOTS"])
            if !before && lang == "en" && !largest {
                let card = app.buttons.containing(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS[c] %@", "Reed", "profile")).firstMatch
                XCTAssertTrue(card.waitForExistence(timeout: 4), app.debugDescription)
                card.tapCenter()
                let signature = app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "Watercolour studies")).firstMatch
                XCTAssertTrue(signature.waitForExistence(timeout: 8), app.debugDescription)
                try await Task.sleep(for: .seconds(1))
                try capture(app, "feishu-profile-" + suffix, env["PERSONA_SHOTS"])
            }
            app.terminate()
        }
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/feishu-avatars")!)
        let ledger = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let rows = try XCTUnwrap(ledger["reads"] as? [[String: Any]])
        let missing = try XCTUnwrap(ledger["missing_paths"] as? [String])
        if before {
            XCTAssertTrue(rows.isEmpty)
            XCTAssertFalse(missing.isEmpty)
            return
        }
        XCTAssertTrue(missing.isEmpty)
        XCTAssertTrue((ledger["unavailable"] as? [String])?.contains("removed") == true)
        let projected = rows.filter { $0["shared"] as? Bool == true }
        XCTAssertEqual(Set(projected.compactMap { $0["key"] as? String }), ["photo/one", "reed"])
        XCTAssertTrue(projected.allSatisfy { [2, 3].contains($0["slot"] as? Int ?? 0)
            && [64, 128, 256, 512].contains($0["size"] as? Int ?? 0) })
        XCTAssertTrue(projected.contains { $0["key"] as? String == "reed" && $0["slot"] as? Int == 2 })
        // A simulator upgraded from the old build may restore Reed’s cached message before the current page arrives.
        // Its old gateway reference must also request a thumbnail through the paired hub.
        let legacy = rows.filter { $0["shared"] as? Bool != true }
        XCTAssertTrue(legacy.allSatisfy { $0["key"] as? String == "reed" && $0["slot"] as? Int == 2
            && [72, 144, 264, 528].contains($0["size"] as? Int ?? 0) })
    }

    @MainActor func testTwoGatewaySlotsAndSettingsHaveTheirOwnPictures() async throws {
        let env = ProcessInfo.processInfo.environment
        guard env["PERSONA_FEISHU_AVATAR_BEFORE"] != "1", let hub = env["PERSONA_FEISHU_AVATAR_FIXTURE"] else { throw XCTSkip("needs the current Feishu avatar fixture") }
        let app = XCUIApplication()
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let arguments = ["-pairURL", pair, "-demo.on", "NO", "-dev.openChat", "", "-integrations.lark.enabled", "YES",
                         "-ui.scheme", "light", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
        app.launchArguments = arguments + ["-tab", "feishu"]
        app.launchPinned()
        let tab = app.buttons["shell-feishu"].firstMatch
        if tab.waitForExistence(timeout: 8) { tab.tapCenter() }
        let second = app.buttons["feishu-account-3"]
        XCTAssertTrue(second.waitForExistence(timeout: 15), app.debugDescription)
        second.tapCenter()
        XCTAssertTrue(app.buttons["feishu-chat-quiet-notes"].waitForExistence(timeout: 10))
        try await Task.sleep(for: .seconds(1))
        try capture(app, "feishu-account-3-list", env["PERSONA_SHOTS"])
        app.buttons["feishu-account-2"].tapCenter()
        XCTAssertTrue(app.buttons["feishu-chat-quiet"].waitForExistence(timeout: 10))
        app.terminate()
        app.launchArguments = arguments + ["-tab", "settings", "-openConnectorFeishu", "YES"]
        app.launchPinned()
        XCTAssertTrue(app.descendants(matching: .any)["feishu-account-row-2"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.descendants(matching: .any)["feishu-account-row-3"].exists)
        try await Task.sleep(for: .seconds(1))
        try capture(app, "feishu-account-settings", env["PERSONA_SHOTS"])
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/feishu-avatars")!)
        let ledger = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let reads = try XCTUnwrap(ledger["reads"] as? [[String: Any]]).filter { $0["key"] as? String == "photo/one" }
        XCTAssertEqual(Set(reads.compactMap { $0["slot"] as? Int }), [2, 3])
        XCTAssertTrue(reads.allSatisfy { $0["shared"] as? Bool == true })
    }

    @MainActor private func capture(_ app: XCUIApplication, _ name: String, _ directory: String?) throws {
        let shot = app.screenshot()
        let attachment = XCTAttachment(screenshot: shot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let directory { try shot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent(name + ".png")) }
    }
}
