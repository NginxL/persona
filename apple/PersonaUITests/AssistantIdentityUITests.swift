import XCTest

final class AssistantIdentityUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    /// Back on the default assistant's main conversation for the classes after this one: these tests open another
    /// conversation and switch assistants (`AssistantUITestReset`).
    override class func tearDown() {
        MainActor.assumeIsolated { AssistantUITestReset.toDefaultMain(hub: ProcessInfo.processInfo.environment["PERSONA_IDENTITY_MULTIPLE_HUB"]) }
        super.tearDown()
    }

    @MainActor private func launch(_ hubKey: String, extra: [String] = []) throws -> XCUIApplication {
        let hub = try XCTUnwrap(env[hubKey], "Provide an isolated mock hub for this roster size")
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-demo.on", "NO", "-openChatsFresh", "YES", "-pairURL", pair, "-tab", "assistant", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000002", "-dev.openChat", "", "-openAssistantSidebar", "NO"] + extra
        app.launchPinned()
        XCTAssertTrue(app.buttons["assistant-sidebar-button"].waitForExistence(timeout: 20), app.debugDescription)
        return app
    }

    @MainActor private func identityHeading(_ app: XCUIApplication) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "identifier == %@ AND label == %@", "assistant-sidebar", "Persona")).firstMatch
    }

    /// The sidebar's current-assistant switcher (`AssistantSwitcherHeader`, the default assistant here). The drawer's
    /// identifier can cover its own on a phone (`drawerSettingsButton`).
    @MainActor private func switcherHeader(_ app: XCUIApplication) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "identifier == %@ OR (identifier == %@ AND label BEGINSWITH %@)",
                                         "assistant-switcher", "assistant-sidebar", "助手")).firstMatch
    }

    @MainActor func testSingleAssistantKeepsNameAvatarAndNavigation() throws {
        let app = try launch("PERSONA_IDENTITY_SINGLE_HUB")
        let sidebar = app.buttons["assistant-sidebar-button"]
        XCTAssertTrue((sidebar.value as? String ?? "").contains("Persona"), "the sidebar button draws and names the persona")
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "manager-switcher").firstMatch.exists)
        sidebar.tap()
        XCTAssertTrue(app.drawerSettingsButton.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.drawerNewChatButton.exists)
        let header = switcherHeader(app)
        XCTAssertTrue(header.waitForExistence(timeout: 20), app.debugDescription)
        XCTAssertFalse(identityHeading(app).exists, "the switcher replaces the old plain-text heading")
        XCTAssertTrue(app.buttons["sidebar-main"].label.hasPrefix("主对话"), "the main row names the conversation; the head names Persona (#9973)")

        header.tap()
        let row = app.descendants(matching: .any).matching(identifier: "assistant-switcher-row-assistant").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        let rows = app.buttons.matching(NSPredicate(format: "identifier BEGINSWITH %@", "assistant-switcher-row-"))
        XCTAssertEqual(rows.count, 1, app.debugDescription)
        XCTAssertFalse(app.descendants(matching: .any).matching(identifier: "assistant-switcher-row-m-3fa21c").firstMatch.exists)
        XCTAssertFalse(row.label.contains("默认"), "one assistant needs no Default mark: \(row.label)")
        let create = app.descendants(matching: .any).matching(identifier: "assistant-switcher-new").firstMatch
        let manage = app.descendants(matching: .any).matching(identifier: "assistant-switcher-manage").firstMatch
        XCTAssertTrue(create.exists && manage.exists, app.debugDescription)
        create.tap()
        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 10), "no name alert after the list closed:\n\(app.debugDescription)")
        alert.buttons["取消"].tap()
        let closed = XCTNSPredicateExpectation(predicate: NSPredicate(format: "exists == false"), object: alert)
        XCTAssertEqual(XCTWaiter.wait(for: [closed], timeout: 10), .completed, "Cancel left the name alert open")
    }

    @MainActor private func shot(_ app: XCUIApplication, name: String) throws {
        acceptIconAlert(timeout: 3)
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let folder = env["PERSONA_SHOTS"] {
            try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: folder).appending(path: name + ".png"))
        }
        if let folder = env["PERSONA_SIMCTL_HANDOFF"] {
            let root = URL(fileURLWithPath: folder)
            let ack = root.appending(path: name + ".ack")
            if FileManager.default.fileExists(atPath: ack.path) { try FileManager.default.removeItem(at: ack) }
            let ready = try JSONSerialization.data(withJSONObject: ["file": name + ".png"])
            try ready.write(to: root.appending(path: "ready.json"), options: .atomic)
            let expected = XCTNSPredicateExpectation(predicate: NSPredicate { @Sendable _, _ in
                FileManager.default.fileExists(atPath: ack.path)
            }, object: nil)
            let result = XCTWaiter.wait(for: [expected], timeout: 30)
            XCTAssertEqual(result, .completed, "The host must capture this exact page with simctl before continuing")
            if result != .completed { throw NSError(domain: "PersonaScreenshotHandoff", code: 1) }
        }
    }

    @MainActor func testVoiceAttributionAndCaptionsFollowRosterSize() throws {
        for (count, hubKey) in [("single", "PERSONA_IDENTITY_SINGLE_HUB"), ("multiple", "PERSONA_IDENTITY_MULTIPLE_HUB")] {
            // On the default assistant, whichever an earlier test switched to (the pick is kept across launches).
            let onDefault = count == "multiple" ? ["-openAssistantManager", "assistant"] : []
            for look in ["light", "dark"] {
                let chat = try launch(hubKey, extra: ["-ui.scheme", look] + onDefault)
                let body = chat.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "早上好")).firstMatch
                XCTAssertTrue(body.waitForExistence(timeout: 20), chat.debugDescription)
                XCTAssertFalse(chat.descendants(matching: .any).matching(identifier: "manager-switcher").firstMatch.exists, "no chips under the top bar")
                try shot(chat, name: "iphone-\(count)-chat-\(look)")
                chat.buttons["assistant-sidebar-button"].tap()
                XCTAssertTrue(chat.drawerSettingsButton.waitForExistence(timeout: 10), chat.debugDescription)
                XCTAssertTrue(switcherHeader(chat).waitForExistence(timeout: 20), chat.debugDescription)
                XCTAssertFalse(identityHeading(chat).exists)
                try shot(chat, name: "iphone-\(count)-sidebar-\(look)")
                chat.terminate()
                let app = try launch(hubKey, extra: ["-ui.scheme", look, "-showVoiceSample", "speaking"] + onDefault)
                let tail = app.descendants(matching: .any).matching(identifier: "voice-tail").firstMatch
                XCTAssertTrue(tail.waitForExistence(timeout: 20), app.debugDescription)
                try shot(app, name: "iphone-\(count)-voice-\(look)")
                app.buttons["voice-more"].tap()
                app.buttons["字幕全文"].tap()
                let captions = app.descendants(matching: .any).matching(identifier: "voice-transcript").firstMatch
                XCTAssertTrue(captions.waitForExistence(timeout: 10), app.debugDescription)
                // With one assistant the captions name the persona, as before; with several they name the current
                // assistant (the mock's default one here), as the sidebar's head does.
                let named = app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", count == "single" ? "Persona" : "助手"))
                XCTAssertGreaterThan(named.count, 0, "voice attribution: the persona's name with one assistant, the current assistant's with several")
                try shot(app, name: "iphone-\(count)-captions-\(look)")
                app.terminate()
            }
        }
    }

    @MainActor func testLegacyHubKeepsOldHeaderWithoutSwitcher() throws {
        guard env["PERSONA_IDENTITY_LEGACY_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MANAGERS=0 (PERSONA_IDENTITY_LEGACY_HUB)")
        }
        let app = try launch("PERSONA_IDENTITY_LEGACY_HUB")
        app.buttons["assistant-sidebar-button"].tap()
        XCTAssertTrue(app.drawerSettingsButton.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(identityHeading(app).waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertFalse(switcherHeader(app).exists, "a hub without a roster keeps the old Persona header")
    }

    @MainActor func testMultipleAssistantsKeepIdentityAndSwitching() throws {
        let app = try launch("PERSONA_IDENTITY_MULTIPLE_HUB", extra: ["-openAssistantManager", "assistant"])
        app.buttons["assistant-sidebar-button"].tap()
        XCTAssertTrue(app.drawerSettingsButton.waitForExistence(timeout: 10), app.debugDescription)
        let header = switcherHeader(app)
        XCTAssertTrue(header.waitForExistence(timeout: 20), app.debugDescription)
        XCTAssertTrue(app.buttons["sidebar-main"].label.hasPrefix("主对话"), "the main row names the conversation; the head names Persona (#9973)")
        header.tap()
        let work = app.descendants(matching: .any).matching(identifier: "assistant-switcher-row-m-3fa21c").firstMatch
        XCTAssertTrue(work.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any).matching(identifier: "assistant-switcher-row-m-8b04d2").firstMatch.exists)
        work.tap()
        // The head itself (not the row just tapped, while the list goes away) names the assistant switched to.
        let named = app.buttons.matching(NSPredicate(format: "(identifier == %@ OR identifier == %@) AND label BEGINSWITH %@",
                                                     "assistant-switcher", "assistant-sidebar", "工作")).firstMatch
        XCTAssertTrue(named.waitForExistence(timeout: 10), "the head names the assistant switched to:\n\(app.debugDescription)")
    }
}
