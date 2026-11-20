import XCTest

/// A paired mock hub with extension.editor-fixture (Reading helper). Normal hubs never receive these fixture controls.
final class ExtensionModelCandidateUITests: XCTestCase {
    private var hub: URL!
    private let purpose = "extension.editor-fixture"

    override func setUp() async throws {
        continueAfterFailure = false
        let address = ProcessInfo.processInfo.environment["PERSONA_CANDIDATE_HUB"]
        guard let address, let url = URL(string: address) else { throw XCTSkip("Needs a local candidate editor fixture") }
        hub = url
        guard let reset = try? await data("mock/candidate-editor/reset"),
              let result = try? JSONSerialization.jsonObject(with: reset) as? [String: String], result["fixture"] == purpose else {
            throw XCTSkip("Needs the local extension.editor-fixture model configuration scenario")
        }
    }

    @MainActor private func open(_ id: String, scheme: String = "light", largest: Bool = false, language: String = "en") -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-AppleLanguages", "(\(language))", "-AppleLocale", language, "-pairURL", pair, "-tab", "settings",
                               "-ui.scheme", scheme, "-openModelConfiguration", "YES", "-openModelPurpose", id]
        if largest { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchPinned()
        let ready = id == purpose ? "model-candidates-mode" : "model-config-save"
        _ = app.buttons[ready].waitForExistence(timeout: 15)
        // Switching the app appearance can bring up the system’s confirmation that its icon also changed.
        let alert = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch
        if alert.waitForExistence(timeout: 2) {
            for label in ["OK", "好"] where alert.buttons[label].exists {
                alert.buttons[label].tap()
                _ = alert.waitForNonExistence(timeout: 5)
                break
            }
        }
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = "candidate-ios-" + name
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    private func data(_ path: String) async throws -> Data {
        let (data, _) = try await URLSession.shared.data(from: hub.appending(path: path))
        return data
    }

    @MainActor private func policy() async throws -> [String: Any] {
        let data = try await data("api/v1/model-config/purposes/" + purpose)
        let detail = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let model = try XCTUnwrap(detail["purpose"] as? [String: Any])
        return try XCTUnwrap(model["candidates"] as? [String: Any])
    }

    @MainActor func testCustomListIsEditableThroughThePurposeEditor() async throws {
        let app = open(purpose)
        let mode = app.buttons["model-candidates-mode"]
        XCTAssertTrue(mode.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "atlas-small")).firstMatch.exists)
        XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "No provider offers")).firstMatch.exists)
        mode.tap()
        app.buttons["My list"].tap()
        let save = app.buttons["model-config-save"]
        XCTAssertFalse(save.isEnabled)
        let add = app.buttons["model-candidates-add"]
        add.tap()
        add.tap()
        XCTAssertFalse(add.isEnabled)
        let second = app.buttons["model-candidate-provider-1"]
        second.tap()
        XCTAssertTrue(app.buttons["Orbit Gateway"].exists)
        XCTAssertFalse(app.buttons["Terminal only"].exists)
        XCTAssertFalse(app.buttons["Hidden provider"].exists)
        app.buttons["Orbit Gateway"].tap()
        app.buttons["model-candidate-actions-1"].tap()
        app.buttons["Move Up"].tap()
        XCTAssertTrue(app.buttons["model-candidate-provider-0"].label.contains("Orbit") ||
                      String(describing: app.buttons["model-candidate-provider-0"].value).contains("Orbit"))
        app.buttons["model-candidate-actions-1"].tap()
        app.buttons["Delete"].tap()
        XCTAssertTrue(add.isEnabled)
        save.tap()
        let savedCustom = await waitForPolicy("custom")
        XCTAssertTrue(savedCustom)
        let saved = try await policy()
        let custom = try XCTUnwrap(saved["custom"] as? [[String: Any]])
        XCTAssertEqual(custom.count, 1)
        XCTAssertEqual(custom[0]["provider"] as? String, "launcher.orbit")
        XCTAssertEqual(custom[0]["configured_model"] as? String, "")
        mode.tap()
        app.buttons["Purpose model only"].tap()
        save.tap()
        let savedOff = await waitForPolicy("off")
        XCTAssertTrue(savedOff)
        mode.tap()
        app.buttons["Extension lists"].tap()
        save.tap()
        let restored = await waitForPolicy("extension")
        XCTAssertTrue(restored)
        app.terminate()
        let ordinary = open("agent.new")
        XCTAssertTrue(ordinary.buttons["model-config-save"].waitForExistence(timeout: 12))
        XCTAssertFalse(ordinary.buttons["model-candidates-mode"].exists)
    }

    @MainActor func testARevisionConflictKeepsTheDraftUntilReloadIsConfirmed() async throws {
        let app = open(purpose)
        let mode = app.buttons["model-candidates-mode"]
        XCTAssertTrue(mode.waitForExistence(timeout: 15))
        mode.tap()
        app.buttons["Purpose model only"].tap()
        _ = try await data("mock/candidate-editor/custom")
        let save = app.buttons["model-config-save"]
        save.tap()
        let reload = app.buttons["model-config-reload"]
        for _ in 0..<5 where !reload.isHittable { app.swipeUp() }
        XCTAssertTrue(reload.waitForExistence(timeout: 8))
        XCTAssertFalse(save.isEnabled)
        reload.tap()
        let confirm = app.buttons.matching(identifier: "model-config-confirm-reload").firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
        let cancel = app.buttons["Cancel"].firstMatch
        if cancel.isHittable { cancel.tap() }
        else { app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.1)).tap() }
        XCTAssertFalse(confirm.exists)
        XCTAssertFalse(save.isEnabled)
        reload.tap()
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
        confirm.tap()
        for _ in 0..<5 where !mode.isHittable { app.swipeDown() }
        XCTAssertTrue(mode.label.contains("My list") || String(describing: mode.value).contains("My list"))
        XCTAssertFalse(save.isEnabled)
    }

    @MainActor private func waitForPolicy(_ expected: String) async -> Bool {
        let deadline = Date().addingTimeInterval(6)
        repeat {
            if (try? await policy()["mode"] as? String) == expected { return true }
            try? await Task.sleep(for: .milliseconds(100))
        } while Date() < deadline
        return false
    }

    @MainActor func testCandidateControlsAndEmptyHistoryUseTheSelectedInterfaceLanguage() async throws {
        _ = try await data("mock/candidate-editor/empty")
        for (language, source, custom, empty) in [
            ("zh-Hans", "模型来源", "自定义列表", "此扩展尚未请求任何模型列表。"),
            ("ja", "モデルの選択元", "自分のリスト", "この拡張機能はまだモデルリストをリクエストしていません。")
        ] {
            let app = open(purpose, language: language)
            let mode = app.buttons["model-candidates-mode"]
            XCTAssertTrue(mode.waitForExistence(timeout: 15))
            XCTAssertTrue(mode.label.contains(source))
            XCTAssertTrue(app.staticTexts[empty].exists)
            mode.tap()
            XCTAssertTrue(app.buttons[custom].exists)
            app.terminate()
        }
    }

    @MainActor func testRequestedAndCustomListsAtBothTextSizesAndAppearances() async throws {
        for scheme in ["light", "dark"] {
            for largest in [false, true] {
                _ = try await data("mock/candidate-editor/reset")
                let size = largest ? "largest" : "default"
                let app = open(purpose, scheme: scheme, largest: largest)
                let picker = app.buttons["model-candidates-mode"]
                XCTAssertTrue(picker.waitForExistence(timeout: 15), app.debugDescription)
                let reason = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "No provider offers")).firstMatch
                XCTAssertTrue(reason.exists, app.debugDescription)
                if largest && !reason.isHittable {
                    let start = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
                    let end = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
                    start.press(forDuration: 0.15, thenDragTo: end, withVelocity: .slow, thenHoldForDuration: 0.15)
                }
                shot(app, "requested-" + size + "-" + scheme)
                app.terminate()
                _ = try await data("mock/candidate-editor/custom")
                let custom = open(purpose, scheme: scheme, largest: largest)
                XCTAssertTrue(custom.buttons["model-candidates-mode"].waitForExistence(timeout: 15), custom.debugDescription)
                let field = custom.textFields["model-candidate-model-0"]
                for _ in 0..<4 where !field.isHittable { custom.swipeUp() }
                XCTAssertTrue(field.exists, custom.debugDescription)
                shot(custom, "custom-" + size + "-" + scheme)
                custom.terminate()
            }
        }
    }
}
