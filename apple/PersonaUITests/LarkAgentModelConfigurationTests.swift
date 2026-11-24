import XCTest

/// Exercises the shipping iOS editor against Fixtures/lark_agent_hub.py, never a production hub: Lark /agent and the
/// Inbox's AI twin, the uses of Agents the user can set apart.
final class LarkAgentModelConfigurationTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"],
              URL(string: value)?.host == "127.0.0.1" else {
            throw XCTSkip("needs the loopback-only Lark model fixture")
        }
        hub = value
    }

    private func json(_ path: String, method: String = "GET") async throws -> [String: Any] {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = method
        request.timeoutInterval = 10
        let (data, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
        return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }

    @MainActor private func launch(scheme: String, entry: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let common = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", "", "-ui.scheme", scheme]
        // Complete first pairing before applying a deep link, which pairing itself resets.
        app.launchArguments = common
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].firstMatch.waitForExistence(timeout: 10)
                      || app.buttons["settings-close"].waitForExistence(timeout: 10), "the initial pairing did not reach the app")
        app.terminate()
        app.launchArguments = common + entry
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_MODEL_SHOTS"] {
            try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor private func choose(_ app: XCUIApplication, picker: String, option: String) {
        let control = app.descendants(matching: .any)[picker].firstMatch
        XCTAssertTrue(control.waitForExistence(timeout: 10))
        control.tapCenter()
        let choice = app.buttons[option].firstMatch
        XCTAssertTrue(choice.waitForExistence(timeout: 5), app.debugDescription)
        choice.tapCenter()
    }

    @MainActor private func saveAndWait(_ app: XCUIApplication, status: String) {
        let save = app.buttons["model-config-save"]
        XCTAssertTrue(save.isEnabled)
        save.tapCenter()
        let predicate = NSPredicate(format: "enabled == false")
        expectation(for: predicate, evaluatedWith: save)
        waitForExpectations(timeout: 10)
        let state = app.staticTexts.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "model-config-inheritance", status)).firstMatch
        // Following the restore button scrolls the form down. At large text sizes the top status cell then leaves the
        // accessibility hierarchy, so bring it back on screen instead of treating its absence as a failed save.
        for _ in 0..<6 where !state.exists { app.swipeDown() }
        XCTAssertTrue(state.waitForExistence(timeout: 10), "saved configuration did not reach \(status)")
    }

    @MainActor private func exercise(scheme: String) async throws {
        _ = try await json("/mock/lark-agent/reset", method: "POST")
        let app = launch(scheme: scheme, entry: ["-openConnectorFeishu", "YES"])
        let link = app.buttons["model-config-link"]
        for _ in 0..<5 where !link.isHittable { app.swipeUp() }
        XCTAssertTrue(link.waitForExistence(timeout: 10))
        link.tapCenter()
        XCTAssertTrue(app.navigationBars["飞书 /agent"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["跟随 Agents"].firstMatch.exists)

        choose(app, picker: "model-config-provider", option: "示例启动器")
        let models = app.buttons["model-config-model-list"]
        XCTAssertTrue(models.waitForExistence(timeout: 10))
        models.tapCenter()
        let model = app.buttons["newChat.model.example-model"]
        XCTAssertTrue(model.waitForExistence(timeout: 10))
        model.tapCenter()
        choose(app, picker: "model-config-effort", option: "高")
        saveAndWait(app, status: "单独配置")
        var result = try await json("/api/v1/model-config/purposes/lark.agent")
        var purpose = result["purpose"] as! [String: Any]
        var binding = purpose["override"] as! [String: Any]
        XCTAssertEqual(purpose["id"] as? String, "lark.agent")
        XCTAssertEqual(purpose["revision"] as? Int, 1)
        XCTAssertEqual(binding["provider"] as? String, "launcher.demo")
        XCTAssertEqual(binding["model"] as? String, "example-model")
        XCTAssertEqual(binding["effort"] as? String, "high")
        try shot(app, "\(scheme)-independent")

        let restore = app.buttons["model-config-restore"]
        for _ in 0..<3 where !restore.isHittable { app.swipeUp() }
        XCTAssertTrue(restore.isEnabled)
        restore.tapCenter()
        saveAndWait(app, status: "跟随 Agents")
        app.swipeDown()
        result = try await json("/api/v1/model-config/purposes/lark.agent")
        purpose = result["purpose"] as! [String: Any]
        binding = purpose["override"] as! [String: Any]
        let effective = purpose["effective"] as! [String: Any]
        XCTAssertTrue(binding.isEmpty)
        XCTAssertEqual(purpose["revision"] as? Int, 2)
        XCTAssertEqual(effective["model"] as? String, "agents-default")
        XCTAssertEqual(effective["effort"] as? String, "medium")
        try shot(app, "\(scheme)-following")

        let audit = try await json("/mock/lark-agent")
        let writes = audit["writes"] as! [[String: Any]]
        XCTAssertEqual(writes.count, 2)
        XCTAssertEqual(writes[0]["revision"] as? Int, 0)
        XCTAssertEqual(writes[1]["revision"] as? Int, 1)
        XCTAssertTrue(writes[1]["provider"] is NSNull && writes[1]["model"] is NSNull && writes[1]["effort"] is NSNull)
        let parent = audit["parent"] as! [String: Any]
        XCTAssertEqual(parent["revision"] as? Int, 11)
        XCTAssertEqual((parent["override"] as! [String: Any])["model"] as? String, "agents-default")
        print("LARK_UI_READBACK \(scheme): save revision=\(writes[0]["revision"]!) provider=\(writes[0]["provider"]!) model=\(writes[0]["model"]!) effort=\(writes[0]["effort"]!); reset request_revision=\(writes[1]["revision"]!) result_revision=\(purpose["revision"]!) override_fields=\(binding.count) effective_model=\(effective["model"]!) parent_revision=\(parent["revision"]!); provider/model/effort=null verified")
        app.terminate()
    }

    @MainActor func testLightIndependentLauncherModelAndResetFromLarkSettings() async throws {
        try await exercise(scheme: "light")
    }

    @MainActor func testDarkIndependentLauncherModelAndResetFromLarkSettings() async throws {
        try await exercise(scheme: "dark")
    }

    /// The AI twin (#4232) from the model catalog: its row says it follows Agents, a save keeps a launcher, model and
    /// effort of its own, Follow Agents clears them; Agents and Lark /agent are never written.
    @MainActor private func exerciseTwin(scheme: String) async throws {
        _ = try await json("/mock/lark-agent/reset", method: "POST")
        let app = launch(scheme: scheme, entry: ["-openModelConfiguration", "YES"])
        let row = app.buttons["model-purpose-inbox.agent"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        // Below the fold at large text sizes: scroll until the whole row is on screen.
        let screen = app.windows.firstMatch.frame
        for _ in 0..<6 where !row.isHittable || row.frame.maxY > screen.maxY - 80 { app.swipeUp() }
        XCTAssertTrue(row.label.contains("跟随 Agents"), row.label)
        try shot(app, "\(scheme)-twin-catalog")
        row.tapCenter()
        XCTAssertTrue(app.navigationBars["AI 分身执行"].waitForExistence(timeout: 10))
        XCTAssertTrue(app.staticTexts["跟随 Agents"].firstMatch.exists)

        choose(app, picker: "model-config-provider", option: "示例启动器")
        let models = app.buttons["model-config-model-list"]
        XCTAssertTrue(models.waitForExistence(timeout: 10))
        models.tapCenter()
        let model = app.buttons["newChat.model.example-model"]
        XCTAssertTrue(model.waitForExistence(timeout: 10))
        model.tapCenter()
        choose(app, picker: "model-config-effort", option: "低")
        saveAndWait(app, status: "单独配置")
        var purpose = try await json("/api/v1/model-config/purposes/inbox.agent")["purpose"] as! [String: Any]
        var binding = purpose["override"] as! [String: Any]
        XCTAssertEqual(purpose["revision"] as? Int, 1)
        XCTAssertEqual(binding["provider"] as? String, "launcher.demo")
        XCTAssertEqual(binding["model"] as? String, "example-model")
        XCTAssertEqual(binding["effort"] as? String, "low")
        try shot(app, "\(scheme)-twin-independent")

        let restore = app.buttons["model-config-restore"]
        for _ in 0..<3 where !restore.isHittable { app.swipeUp() }
        XCTAssertTrue(restore.isEnabled)
        restore.tapCenter()
        saveAndWait(app, status: "跟随 Agents")
        app.swipeDown()
        purpose = try await json("/api/v1/model-config/purposes/inbox.agent")["purpose"] as! [String: Any]
        binding = purpose["override"] as! [String: Any]
        let effective = purpose["effective"] as! [String: Any]
        XCTAssertTrue(binding.isEmpty)
        XCTAssertEqual(purpose["revision"] as? Int, 2)
        XCTAssertEqual(effective["model"] as? String, "agents-default")
        try shot(app, "\(scheme)-twin-following")

        let audit = try await json("/mock/model-child/inbox.agent")
        let writes = audit["writes"] as! [[String: Any]]
        XCTAssertEqual(writes.count, 2)
        XCTAssertEqual(writes[0]["revision"] as? Int, 0)
        XCTAssertEqual(writes[1]["revision"] as? Int, 1)
        XCTAssertTrue(writes[1]["provider"] is NSNull && writes[1]["model"] is NSNull && writes[1]["effort"] is NSNull)
        let parent = audit["parent"] as! [String: Any]
        XCTAssertEqual(parent["revision"] as? Int, 11)
        let lark = (audit["others"] as! [String: Any])["lark.agent"] as! [String: Any]
        XCTAssertEqual(lark["revision"] as? Int, 0)
        print("TWIN_UI_READBACK \(scheme): save revision=\(writes[0]["revision"]!) provider=\(writes[0]["provider"]!) model=\(writes[0]["model"]!) effort=\(writes[0]["effort"]!); reset request_revision=\(writes[1]["revision"]!) result_revision=\(purpose["revision"]!) override_fields=\(binding.count) effective_model=\(effective["model"]!) parent_revision=\(parent["revision"]!) lark_revision=\(lark["revision"]!)")
        app.terminate()
    }

    @MainActor func testLightTwinLauncherModelAndResetFromTheModelCatalog() async throws {
        try await exerciseTwin(scheme: "light")
    }

    @MainActor func testDarkTwinLauncherModelAndResetFromTheModelCatalog() async throws {
        try await exerciseTwin(scheme: "dark")
    }

    /// The AI twin never displays the parent Agents' Full access as its own default. Its explicit choice saves to the
    /// same purpose record, Plan stays unavailable, and reset writes an explicit null back to Default permissions.
    @MainActor private func exerciseTwinPermission(scheme: String) async throws {
        _ = try await json("/mock/lark-agent/reset", method: "POST")
        let app = launch(scheme: scheme, entry: ["-openModelConfiguration", "YES", "-openModelPurpose", "inbox.agent"])
        let picker = app.buttons["model-config-permission"].firstMatch
        XCTAssertTrue(picker.waitForExistence(timeout: 10), "no Inbox permission control")
        XCTAssertTrue(picker.label.contains("默认权限"), "an unset AI twin must not inherit Agents Full access: \(picker.label)")
        picker.tapCenter()
        XCTAssertFalse(app.buttons["计划模式"].exists, "Plan rejects the MCP calls the Inbox needs")
        let full = app.buttons["完全访问"].firstMatch
        XCTAssertTrue(full.waitForExistence(timeout: 5), app.debugDescription)
        full.tapCenter()
        saveAndWait(app, status: "单独配置")
        var purpose = try await json("/api/v1/model-config/purposes/inbox.agent")["purpose"] as! [String: Any]
        var binding = purpose["override"] as! [String: Any]
        XCTAssertEqual(binding["permission_mode"] as? String, "bypassPermissions")
        try shot(app, "\(scheme)-twin-permission-full-access")

        let restore = app.buttons["model-config-restore"]
        for _ in 0..<4 where !restore.isHittable { app.swipeUp() }
        XCTAssertTrue(restore.isEnabled)
        restore.tapCenter()
        saveAndWait(app, status: "跟随 Agents")
        purpose = try await json("/api/v1/model-config/purposes/inbox.agent")["purpose"] as! [String: Any]
        binding = purpose["override"] as! [String: Any]
        XCTAssertTrue(binding.isEmpty)
        XCTAssertEqual((purpose["effective"] as! [String: Any])["permission_mode"] as? String, "default")
        let audit = try await json("/mock/model-child/inbox.agent")
        let writes = audit["writes"] as! [[String: Any]]
        XCTAssertEqual(writes.count, 2)
        XCTAssertEqual(writes[0]["permission_mode"] as? String, "bypassPermissions")
        XCTAssertTrue(writes[1]["permission_mode"] is NSNull)
        XCTAssertEqual((audit["parent"] as! [String: Any])["revision"] as? Int, 11)
        print("INBOX_PERMISSION_UI \(scheme): explicit=\(writes[0]["permission_mode"]!) reset_is_null=\(writes[1]["permission_mode"] is NSNull) effective=\((purpose["effective"] as! [String: Any])["permission_mode"]!) parent_revision=11")
        app.terminate()
    }

    @MainActor func testLightTwinPermissionSaveAndReset() async throws {
        try await exerciseTwinPermission(scheme: "light")
    }

    @MainActor func testDarkTwinPermissionSaveAndReset() async throws {
        try await exerciseTwinPermission(scheme: "dark")
    }

    @MainActor func testModelCatalogOpensTheIndependentPurposeAndMessagesEffort() async throws {
        _ = try await json("/mock/lark-agent/reset", method: "POST")
        let app = launch(scheme: "light", entry: ["-openModelConfiguration", "YES"])
        let row = app.buttons["model-purpose-lark.agent"]
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        row.tapCenter()
        XCTAssertTrue(app.navigationBars["飞书 /agent"].waitForExistence(timeout: 10))
        choose(app, picker: "model-config-provider", option: "示例 Messages API")
        XCTAssertTrue(app.descendants(matching: .any)["model-config-effort"].exists)
        app.terminate()
    }
}
