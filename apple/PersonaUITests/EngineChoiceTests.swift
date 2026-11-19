import XCTest

/// Real pairing completion and Settings persist the same engine contract against the isolated mock hub.
final class EngineChoiceTests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_ENGINE_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_ENGINE_HUB)")
        }
        hub = value
        _ = try Data(contentsOf: URL(string: hub + "/mock/reset")!)
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let shot = XCTAttachment(screenshot: app.screenshot())
            shot.lifetime = .keepAlways
            add(shot)
            add(XCTAttachment(string: app.debugDescription))
        }
    }

    private var pair: String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// Opens the pairing link from outside the app; it asks "Connect to <host>?" before it pairs (#9929): Connect.
    @MainActor private func openPairLink() {
        app.open(URL(string: pair)!)
        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 10), "the pairing link did not ask before pairing")
        alert.buttons["连接"].tap()
    }

    @MainActor private func shot(_ name: String) throws {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"] ?? "light"
            try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("engine-\(name)-\(style).png"))
        }
    }

    private func current(_ machine: String) throws -> String? {
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/machines")!)
        let root = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return (root?["machines"] as? [[String: Any]])?.first { $0["id"] as? String == machine }?["engine"] as? String
    }

    /// Confirms the preselected engine (pairing must pick one; there is no skip). The short Duo cover can put the
    /// button below the viewport; reach it before tapping.
    @MainActor private func confirmEngine() {
        let save = app.buttons["engine.save"]
        for _ in 0..<4 {
            if save.waitForExistence(timeout: 1) && save.isHittable { break }
            app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        }
        XCTAssertTrue(save.waitForExistence(timeout: 5), "no reachable way to confirm the engine")
        XCTAssertFalse(app.buttons["engine.skip"].exists, "pairing must not offer to skip the engine")
        save.tap()
    }

    /// Pairing finishes on the Shell, whose drawer exposes Inbox and Settings.
    @MainActor private func pairedRoot() {
        for id in ["shell-assistant", "shell-agents", "assistant-sidebar-button"] {
            XCTAssertTrue(app.buttons[id].firstMatch.waitForExistence(timeout: 10), "no paired root control: \(id)")
        }
        app.buttons["assistant-sidebar-button"].firstMatch.tapCenter()
        XCTAssertTrue(app.buttons["sidebar-inbox"].firstMatch.waitForExistence(timeout: 5), "no paired drawer Inbox route")
        XCTAssertTrue(app.drawerSettingsButton.waitForExistence(timeout: 5), "no paired drawer Settings route")
        app.descendants(matching: .any)["assistant-drawer-close"].firstMatch.tapCenter()
        XCTAssertFalse(app.buttons["engine.save"].exists, "the engine step did not finish")
    }

    @MainActor func testPairingMustChooseAnEngine() throws {
        app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-pairURL", ""]
        app.launchPinned()
        XCTAssertTrue(app.buttons["welcome.pair"].waitForExistence(timeout: 10))
        openPairLink()
        XCTAssertTrue(app.buttons["engine.save"].waitForExistence(timeout: 15))
        XCTAssertTrue(app.staticTexts["这台电脑用哪个引擎"].exists)
        try shot("pairing")
        app.buttons["engine.option.herdr-core"].tap()
        app.buttons["engine.save"].tap()
        pairedRoot()
        XCTAssertEqual(try current("server"), "herdr-core")
        openPairLink()
        XCTAssertTrue(app.buttons["engine.save"].waitForExistence(timeout: 10))
        // Pairing again preselects the configured engine; confirming keeps it.
        XCTAssertTrue(app.buttons["engine.option.herdr-core"].isSelected)
        confirmEngine()
        pairedRoot()
        XCTAssertEqual(try current("server"), "herdr-core", "the preselected engine is the configured one")
    }

    @MainActor func testSettingsEngineRowAndPicker() throws {
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "server"]
        app.launchPinned()
        let row = app.buttons["machine.engine"]
        XCTAssertTrue(row.waitForExistence(timeout: 15))
        if !row.isHittable { app.swipeUp() }
        try shot("settings-row")
        row.tap()
        XCTAssertTrue(app.buttons["engine.save"].waitForExistence(timeout: 10))
        app.buttons["engine.option.herdr-core"].tap()
        try shot("settings-picker")
        app.buttons["engine.save"].tap()
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertTrue(row.label.contains("Herdr App"), row.label)
        XCTAssertEqual(try current("server"), "herdr-core")
    }
    /// Also Show botmux Sessions (#2575): on by default under the engine picker, saved at once when flipped.
    @MainActor func testBotmuxSwitchUnderEngine() throws {
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "server"]
        app.launchPinned()
        // A fresh install's notifications prompt covers the page until answered (ChatBackTests).
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.buttons.matching(NSPredicate(format: "label IN %@", ["允许", "Allow"])).firstMatch
        if allow.waitForExistence(timeout: 5) { allow.tap() }
        let row = app.buttons["machine.engine"]
        XCTAssertTrue(row.waitForExistence(timeout: 15))
        if !row.isHittable { app.swipeUp() }
        row.tap()
        let toggle = app.switches["engine.botmux"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10))
        if !toggle.isHittable { app.swipeUp() }
        XCTAssertEqual(toggle.value as? String, "1", "on by default")
        try shot("botmux")
        toggle.switches.firstMatch.tap()
        let off = NSPredicate(format: "value == '0'")
        expectation(for: off, evaluatedWith: toggle)
        waitForExpectations(timeout: 5)
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/machines/server/botmux")!)
        XCTAssertEqual((try JSONSerialization.jsonObject(with: data) as? [String: Any])?["on"] as? Bool, false)
    }

    @MainActor func testSettingsTracksPushAndSavesOffline() async throws {
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "nas"]
        app.launchPinned()
        let row = app.buttons["machine.engine"]
        XCTAssertTrue(row.waitForExistence(timeout: 15))
        if !row.isHittable { app.swipeUp() }
        var request = URLRequest(url: URL(string: hub + "/api/v1/machines/nas/engine")!)
        request.httpMethod = "PUT"
        request.httpBody = Data("{\"engine\":\"herdr-core\"}".utf8)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        _ = try await URLSession.shared.data(for: request)
        let pushed = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "machine.engine", "Herdr App")).firstMatch
        XCTAssertTrue(pushed.waitForExistence(timeout: 10), "the row follows the machines push")
        row.tap()
        XCTAssertTrue(app.staticTexts["离线，暂无检测信息"].firstMatch.waitForExistence(timeout: 10))
        app.buttons["engine.option.herdr"].tap()
        confirmEngine()
        XCTAssertTrue(row.waitForExistence(timeout: 10))
        XCTAssertEqual(try current("nas"), "herdr")
    }

    @MainActor func testManualPairingShowsEngineStep() throws {
        app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-pairURL", ""]
        app.launchPinned()
        XCTAssertTrue(app.buttons["welcome.pair"].waitForExistence(timeout: 10))
        app.buttons["welcome.pair"].tap()
        let scanOrToken = app.buttons["扫码或其他配对方式"]
        XCTAssertTrue(scanOrToken.waitForExistence(timeout: 10), "no scan-or-token pairing method")
        scanOrToken.tap()
        let otherWays = app.buttons["pair.other"]
        XCTAssertTrue(otherWays.waitForExistence(timeout: 10), "no manual pairing entry")
        otherWays.tap()
        let address = app.textFields["服务器地址"]
        XCTAssertTrue(address.waitForExistence(timeout: 10))
        address.tap()
        address.typeText(hub)
        app.secureTextFields["pair.token"].tap()
        app.secureTextFields["pair.token"].typeText("mock")
        app.navigationBars.buttons["连接"].tap()
        XCTAssertTrue(app.buttons["engine.save"].waitForExistence(timeout: 15))
        // Never chosen: herdr is preselected; confirming saves it.
        XCTAssertTrue(app.buttons["engine.option.herdr"].isSelected)
        confirmEngine()
        pairedRoot()
        // The address beside the token looked like a login: iOS must not offer to save the token (#1598).
        PasswordPrompt.assertNone(app, "the pairing token")
        XCTAssertEqual(try current("server"), "herdr")
    }

    @MainActor func testPairingFailureStaysOnTheStep() throws {
        guard let errorHub = ProcessInfo.processInfo.environment["PERSONA_ENGINE_ERROR_HUB"] else {
            throw XCTSkip("needs MOCK_ENGINE_ERROR=1 hub")
        }
        hub = errorHub
        app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-pairURL", ""]
        app.launchPinned()
        openPairLink()
        XCTAssertTrue(app.buttons["engine.save"].waitForExistence(timeout: 15))
        app.buttons["engine.option.herdr-core"].tap()
        app.buttons["engine.save"].tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "保存失败")).firstMatch.waitForExistence(timeout: 10))
        try shot("pairing-error")
        // No way around choosing: the step stays, with no skip, until a save succeeds.
        XCTAssertFalse(app.buttons["engine.skip"].exists)
        XCTAssertTrue(app.buttons["engine.save"].exists)
    }

}
