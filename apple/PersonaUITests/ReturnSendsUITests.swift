import XCTest

/// Return Key Sends (#8178): with the switch on, the software keyboard's Return sends what is typed in an agent's and the
/// assistant's conversation, once, and empties the box; off, Return is a new line and nothing is sent. Against
/// scripts/mock/composer_hub.py (TEST_RUNNER_PERSONA_MOCK_HUB=…), as ComposerExpandTests.
final class ReturnSendsUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    /// An agent of the default manager's computers (Mac Studio's blog migration, idle). The weather agent other composer
    /// tests use runs on the MacBook of the mock's second manager, whose sessions the app holds only while that
    /// manager's assistant is the current one: opened by key from the default assistant, its composer is disabled
    /// (#8640).
    private let agent = "studio/a1f3c2d4-0000-4000-8000-000000000003"
    private let assistant = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/composer_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "composer tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        super.tearDown()
    }

    @MainActor func testReturnSendsFromAnAgentsConversation() { sends(isAssistant: false) }

    @MainActor func testReturnSendsFromTheAssistantsConversation() { sends(isAssistant: true) }

    @MainActor func testReturnIsANewLineWhenOff() {
        open(isAssistant: false, on: false)
        startTyping()
        let marker = "rs-off-\(Int(Date.now.timeIntervalSince1970))"
        field.typeText("\(marker)\nsecond line")
        sleep(1)
        XCTAssertEqual(field.value as? String, "\(marker)\nsecond line", "Return did not make a new line with the switch off")
        sleep(3)
        XCTAssertEqual(sent(marker).count, 0, "something was sent with the switch off")
        clear(field)
        sleep(2) // the draft is saved a moment after the last change
    }

    /// A hardware keyboard: Return sends, Shift-Return is a new line. Synthetic modifier keys need
    /// TEST_RUNNER_PERSONA_HARDWARE_KEYS=1 (docs/apple/ios.md: the simulator does not always deliver them).
    @MainActor func testHardwareShiftReturnIsANewLineAndReturnSends() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_HARDWARE_KEYS"] == "1" else {
            throw XCTSkip("needs TEST_RUNNER_PERSONA_HARDWARE_KEYS=1")
        }
        open(isAssistant: false, on: true)
        startTyping()
        let marker = "rs-keys-\(Int(Date.now.timeIntervalSince1970))"
        field.typeText(marker)
        field.typeKey(.return, modifierFlags: .shift)
        field.typeText("two")
        sleep(1)
        XCTAssertEqual(field.value as? String, "\(marker)\ntwo", "Shift-Return did not make a new line")
        XCTAssertEqual(sent(marker).count, 0, "Shift-Return sent")
        field.typeKey(.return, modifierFlags: [])
        let deadline = Date.now.addingTimeInterval(20)
        while sent(marker).isEmpty, Date.now < deadline { sleep(1) }
        sleep(4)
        XCTAssertEqual(sent(marker).count, 1, "Return did not send once: \(sent(marker))")
    }

    /// The switch on its page and the box with it on (the keyboard's key reads "send"), in PERSONA_SHOTS_APPEARANCE.
    @MainActor func testScreenshots() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_SHOTS"] != nil else { throw XCTSkip("needs TEST_RUNNER_PERSONA_SHOTS") }
        open(isAssistant: true, on: true)
        startTyping()
        field.typeText("Book a table for two at 7")
        // The keyboard is the evidence (its key reads "send"): wait for it to be up and settled before the picture.
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "no keyboard for the picture")
        sleep(2)
        shot("composer-on")
        clear(field)
        sleep(2)
        app.terminate()
        // The same in an Agent conversation.
        open(isAssistant: false, on: true)
        startTyping()
        field.typeText("Also keep the old URLs")
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "no keyboard for the picture")
        sleep(2)
        shot("agent-composer-on")
        clear(field)
        sleep(2)
        app.terminate()
        app.launchArguments = ["-AppleLanguages", "(\(language))", "-ui.returnSends", "YES", "-ui.scheme", appearance, "-tab", "settings"]
        app.launchPinned()
        XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
        let row = app.settingsAdvancedRow("settings-appearance")
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no Appearance row:\n\(app.debugDescription)")
        row.tap()
        let toggle = element("settings-return-sends")
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), "no Return Key Sends switch:\n\(app.debugDescription)")
        sleep(1)
        shot("settings")
    }

    @MainActor private func sends(isAssistant: Bool) {
        open(isAssistant: isAssistant, on: true)
        startTyping()
        let marker = "rs-\(isAssistant ? "assistant" : "agent")-\(Int(Date.now.timeIntervalSince1970))"
        field.typeText(marker)
        shot("\(isAssistant ? "assistant" : "agent")-on-typed")
        field.typeText("\n")
        let deadline = Date.now.addingTimeInterval(20)
        while sent(marker).isEmpty, Date.now < deadline { sleep(1) }
        sleep(4) // a second send would have come by now
        let got = sent(marker)
        XCTAssertEqual(got.count, 1, "the hub got \(got.count) messages: \(got)")
        XCTAssertFalse(got.first?.contains("\\n") == true, "the line break went with the message: \(got)")
        let value = field.value as? String ?? ""
        XCTAssertTrue(value.isEmpty || value == field.placeholderValue, "the box kept \(value)")
    }

    private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any).matching(identifier: id).firstMatch }
    private var field: XCUIElement { element("shell-composer-field") }
    private var language: String { ProcessInfo.processInfo.environment["PERSONA_UI_LANGUAGE"] ?? "zh-Hans" }
    private var appearance: String { ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light" }

    @MainActor private func open(isAssistant: Bool, on: Bool) {
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        // Pair first on its own: a first pairing resets the route arguments (as ComposerKeyboardTests).
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(\(language))"]
        app.launchPinned()
        XCTAssertTrue(app.buttons["Persona"].waitForExistence(timeout: 15) || field.waitForExistence(timeout: 15))
        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(\(language))", "-holdChromeOpen", "YES",
                               "-ui.returnSends", on ? "YES" : "NO", "-ui.scheme", appearance,
                               "-dev.openChat", "", "-app.tab", isAssistant ? "assistant" : "chats",
                               "-tab", isAssistant ? "assistant" : "dev"]
        app.launchArguments += isAssistant ? ["-openAssistant", assistant] : ["-openSession", agent]
        app.launchPinned()
        XCTAssertTrue(field.waitForExistence(timeout: 20), "the conversation did not open:\n\(app.debugDescription)")
        sleep(2)
        XCTAssertTrue(field.isEnabled, "the composer is disabled: the app does not hold this conversation (#8640)")
    }

    /// The keyboard on the box, the box empty (drafts are kept across runs).
    @MainActor private func startTyping() {
        for _ in 1...2 where !app.keyboards.firstMatch.exists {
            field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
            _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
        }
        XCTAssertTrue(app.keyboards.firstMatch.exists, "no keyboard")
        clear(field)
    }

    @MainActor private func clear(_ field: XCUIElement) {
        for _ in 1...3 {
            let value = field.value as? String ?? ""
            let typed = value == field.placeholderValue ? 0 : value.count
            guard typed > 0 else { return }
            field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.9)).tap()
            field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: typed + 2))
        }
    }

    /// The mock's record of sends (`/mock/sent`) with `marker` in them.
    private func sent(_ marker: String) -> [String] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/sent")!),
              let all = (try? JSONSerialization.jsonObject(with: data) as? [String: Any])?["sent"] as? [Any] else { return [] }
        return all.compactMap { entry in
            guard let d = try? JSONSerialization.data(withJSONObject: entry), let s = String(data: d, encoding: .utf8) else { return nil }
            return s.contains(marker) ? s : nil
        }
    }

    @MainActor private func shot(_ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let url = URL(fileURLWithPath: dir).appendingPathComponent("return-sends-\(name)-\(appearance)-\(language).png")
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: url)
    }
}
