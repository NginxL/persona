import XCTest

/// The main conversation's readiness on a new user's hub (#4193, docs/assistant/onboarding.md 4.4, 5.1, 5.2): against
/// scripts/mock/mock_hub.py with MOCK_READINESS=<scenario> (PERSONA_MOCK_HUB; PERSONA_READINESS names the scenario for the
/// screenshots). A new conversation shows the card for the missing step; with something typed, send says what to do
/// first, goes there, and the draft stays. PERSONA_SHOTS=<dir> writes the screenshots.
final class ReadinessUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    @MainActor private func launch(fresh: Bool = false, tab: String = "assistant") throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_READINESS (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", tab, "-openDraftsFresh", "YES"] + (fresh ? ["-openChatsFresh", "YES"] : [])
        if let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] {
            app.launchArguments += ["-ui.scheme", look]
        }
        addUIInterruptionMonitor(withDescription: "notifications") { alert in
            for label in ["允许", "Allow", "好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); return true }
            return false
        }
        app.launchPinned()
        dismissSystemAlerts(wait: 4)
        return app
    }

    /// Notification permission and the icon-change notice belong to SpringBoard, not the app under test.
    @MainActor private func dismissSystemAlerts(wait: TimeInterval = 1) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        var deadline = Date.now.addingTimeInterval(wait)
        while Date.now < deadline {
            let alert = springboard.alerts.firstMatch
            if alert.waitForExistence(timeout: 0.5) {
                for label in ["允许", "Allow", "好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); break }
                deadline = Date.now.addingTimeInterval(1.5)
            }
        }
    }

    /// Restarts the synthetic hub in another readiness state; mock_scenarios.py waits until it answers again.
    private func switchReadiness(_ name: String) throws {
        guard let control = ProcessInfo.processInfo.environment["PERSONA_READINESS_CONTROL"] else {
            throw XCTSkip("needs scripts/mock/mock_scenarios.py (PERSONA_READINESS_CONTROL)")
        }
        let url = URL(string: control + "/scenario?name=readiness-" + name)!
        let done = expectation(description: "readiness scenario")
        URLSession.shared.dataTask(with: url) { _, response, error in
            XCTAssertNil(error)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 60)
    }

    /// Reads a synthetic hub endpoint after pairing the same loopback URL as the app.
    private func mockGET(_ path: String) throws -> Data {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs PERSONA_MOCK_HUB")
        }
        let done = expectation(description: path)
        var body = Data()
        URLSession.shared.dataTask(with: URL(string: hub + path)!) { data, response, error in
            XCTAssertNil(error)
            XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
            body = data ?? Data()
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 15)
        return body
    }

    private func readinessReadCount() throws -> Int {
        let hits = try JSONSerialization.jsonObject(with: mockGET("/mock/hits")) as? [String: Any]
        return (hits?["hits"] as? [String: Any])?["GET /api/v1/assistant/readiness"] as? Int ?? 0
    }

    /// Proves a real readiness request happened, then lets its response land before checking the card.
    private func waitForReadinessRead(after before: Int) throws {
        var count = before
        for _ in 0..<35 {
            count = try readinessReadCount()
            if count > before { break }
            sleep(1)
        }
        XCTAssertGreaterThan(count, before, "no real readiness refresh arrived")
        sleep(1)
    }

    @MainActor private func launchMain() throws -> XCUIApplication {
        guard let main = ProcessInfo.processInfo.environment["PERSONA_MAIN"] else {
            throw XCTSkip("needs PERSONA_MAIN")
        }
        let app = try launch()
        sleep(4) // Pairing lands on a new conversation first; the second launch opens the existing main one.
        app.terminate()
        app.launchArguments = ["-tab", "assistant", "-openAssistant", main, "-openDraftsFresh", "YES"]
        if let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] {
            app.launchArguments += ["-ui.scheme", look]
        }
        app.launchPinned()
        dismissSystemAlerts(wait: 4)
        return app
    }

    private var scenario: String { ProcessInfo.processInfo.environment["PERSONA_READINESS"] ?? "scenario" }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        dismissSystemAlerts()
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "\(name)-\(look).png"))
        }
    }

    /// On a new conversation: wait for the card (the mock's readiness), or note there is none (full).
    @MainActor private func openNew(_ app: XCUIApplication) {
        let new = app.buttons["assistant-new"].firstMatch
        if new.waitForExistence(timeout: 15), new.isHittable { new.tap() }
    }

    @MainActor func testUsageLimitDismissalSurvivesPollingAndRecovery() throws {
        guard scenario == "rate_limited" else { throw XCTSkip("needs MOCK_READINESS=rate_limited") }
        let app = try launchMain()
        let warning = app.staticTexts["模型用得太快了"].firstMatch
        XCTAssertTrue(warning.waitForExistence(timeout: 25), "no usage-limit card:\n\(app.debugDescription)")
        shot(app, "readiness-rate-limited")

        // The default Claude is limited; this conversation explicitly routes its next message to a Codex model.
        let chatMenu = app.descendants(matching: .any)["shell-assistant"].firstMatch
        XCTAssertTrue(chatMenu.waitForExistence(timeout: 10))
        chatMenu.tap()
        let launcher = app.descendants(matching: .any)["启动器：claude"].firstMatch
        XCTAssertTrue(launcher.waitForExistence(timeout: 5), "no launcher menu:\n\(app.debugDescription)")
        launcher.tap()
        let codex = app.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", "codex")).firstMatch
        XCTAssertTrue(codex.waitForExistence(timeout: 10), "no Codex launcher:\n\(app.debugDescription)")
        codex.tap()
        let model = app.descendants(matching: .any)["GPT-5.5-Codex"].firstMatch
        XCTAssertTrue(model.waitForExistence(timeout: 10), "no Codex model:\n\(app.debugDescription)")
        model.tap()
        let scoped = app.descendants(matching: .any).matching(
            NSPredicate(format: "label CONTAINS %@", "这是默认模型连接的限流提醒；当前对话使用另一个模型连接。")).firstMatch
        XCTAssertTrue(scoped.waitForExistence(timeout: 5), "the card does not distinguish default and active launchers:\n\(app.debugDescription)")
        shot(app, "readiness-rate-limited-codex")

        let field = app.textViews["shell-composer-field"].exists ? app.textViews["shell-composer-field"] : app.textFields["shell-composer-field"]
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no composer:\n\(app.debugDescription)")
        field.tap()
        field.typeText("synthetic codex routing")
        let send = app.buttons["shell-composer-send"].exists ? app.buttons["shell-composer-send"].firstMatch : app.buttons["发送"].firstMatch
        send.tap()
        let cleared = NSPredicate(format: "value == '' OR value == %@", field.placeholderValue ?? "")
        expectation(for: cleared, evaluatedWith: field)
        waitForExpectations(timeout: 8)
        _ = try mockGET("/mock/say?text=Synthetic%20Codex%20answer")
        XCTAssertTrue(app.staticTexts["Synthetic Codex answer"].waitForExistence(timeout: 10), "no synthetic answer on the Codex route")
        let sent = try JSONSerialization.jsonObject(with: mockGET("/mock/sent")) as? [String: Any]
        let last = (sent?["sent"] as? [[String: Any]])?.last
        XCTAssertEqual(last?["text"] as? String, "synthetic codex routing")
        XCTAssertEqual(last?["launcher"] as? String, "codex")
        XCTAssertEqual(last?["model"] as? String, "gpt-5.5-codex")
        let readiness = try JSONSerialization.jsonObject(with: mockGET("/api/v1/assistant/readiness")) as? [String: Any]
        let modelStep = (readiness?["steps"] as? [[String: Any]])?.first { $0["id"] as? String == "model" }
        XCTAssertEqual(modelStep?["code"] as? String, "rate_limited", "Codex answering must not clear Claude's real limit")
        XCTAssertEqual(modelStep?["launcher"] as? String, "claude")
        XCTAssertEqual(readiness?["dismissed"] as? Bool, false)
        shot(app, "readiness-rate-limited-codex-answered")

        field.tap()
        field.typeText("synthetic draft")
        let later = app.buttons["以后再说"].firstMatch
        XCTAssertTrue(later.waitForExistence(timeout: 5), "no accessible Later control:\n\(app.debugDescription)")
        later.tap()
        XCTAssertTrue(scoped.waitForNonExistence(timeout: 5), "Later did not dismiss the usage-limit card")
        let dismissedAt = try readinessReadCount()
        try waitForReadinessRead(after: dismissedAt)
        XCTAssertFalse(scoped.exists, "an ordinary refresh revived the dismissed occurrence")
        XCTAssertEqual(field.value as? String, "synthetic draft")
        shot(app, "readiness-rate-limited-dismissed")

        try switchReadiness("full")
        let recoveringAt = try readinessReadCount()
        try waitForReadinessRead(after: recoveringAt)
        XCTAssertFalse(scoped.exists)
        try switchReadiness("rate_limited")
        let failingAt = try readinessReadCount()
        XCUIDevice.shared.press(.home)
        sleep(1)
        app.activate() // full stops polling; a foreground revisit observes the later failure without resetting app state.
        try waitForReadinessRead(after: failingAt)
        XCTAssertTrue(warning.waitForExistence(timeout: 20), "a real failure after recovery did not appear:\n\(app.debugDescription)")
        XCTAssertTrue(scoped.waitForExistence(timeout: 5), "the new card lost the active Codex scope:\n\(app.debugDescription)")
        XCTAssertEqual(field.value as? String, "synthetic draft")
        shot(app, "readiness-rate-limited-new-failure")
    }

    @MainActor func testTheCardSaysWhatIsMissingAndSendGoesThere() throws {
        let app = try launch()
        openNew(app)
        let card = app.descendants(matching: .any)["readiness-card"].firstMatch
        // Nothing blocks on these: an unverified model (unknown) and an assistant restarting (503: as before).
        if ["full", "unverified", "assistant_offline"].contains(scenario) {
            sleep(4)
            shot(app, "readiness-\(scenario)")
            XCTAssertFalse(card.exists, "a card on a ready hub:\n\(app.debugDescription)")
            return
        }
        XCTAssertTrue(card.waitForExistence(timeout: 20), "no readiness card:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "readiness-\(scenario)")
        if scenario == "zero" {
            // Add a Computer on the card opens adding a computer in place (#4196).
            let add = card.buttons["添加电脑"].firstMatch
            XCTAssertTrue(add.waitForExistence(timeout: 5), "no 添加电脑 on the card:\n\(app.debugDescription)")
            add.tap()
            sleep(2)
            shot(app, "readiness-zero-add-computer")
            return
        }
        guard ["zero", "offline", "pending", "no_claude", "not_logged_in", "bad_key", "launcher_missing", "node_outdated"].contains(scenario) else { return }

        // Something typed: send says what to do first; tapping it opens that and the draft stays.
        let field = app.textViews["shell-composer-field"].exists ? app.textViews["shell-composer-field"] : app.textFields["shell-composer-field"]
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no Shell composer:\n\(app.debugDescription)")
        field.tap()
        field.typeText("你好")
        let gate = app.buttons["composer-send-gate"].firstMatch
        XCTAssertTrue(gate.waitForExistence(timeout: 5), "send did not turn into what to do first:\n\(app.debugDescription)")
        shot(app, "readiness-\(scenario)-gate")
        gate.tap()
        sleep(2)
        shot(app, "readiness-\(scenario)-gate-opened")
    }

    /// A probe that outlasts the old 30 s client timeout still gets the hub's result; an HTTP failure stays beside the
    /// checklist button that started it. Run slow / error with MOCK_READINESS=not_logged_in, or model_error with bad_key.
    /// PERSONA_PROBE_EXPECT=slow|error|model_error.
    @MainActor func testTryMessageWaitsForAnAnswerOrShowsFailure() throws {
        let env = ProcessInfo.processInfo.environment
        guard let expected = env["PERSONA_PROBE_EXPECT"], ["slow", "error", "model_error"].contains(expected) else {
            throw XCTSkip("needs PERSONA_PROBE_EXPECT=slow|error|model_error and the matching mock probe delay / error")
        }
        let app = try launch(fresh: true, tab: "settings")
        let entry = app.descendants(matching: .any)["settings.checklist"].firstMatch
        XCTAssertTrue(entry.waitForExistence(timeout: 20), "no checklist in Settings:\n\(app.debugDescription)")
        entry.tap()
        let rowID = expected == "model_error" ? "model" : "first_message"
        let row = app.descendants(matching: .any)["checklist.\(rowID)"].firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no Try a message row:\n\(app.debugDescription)")
        let tryButton = row.buttons["试一句"].firstMatch
        XCTAssertTrue(tryButton.waitForExistence(timeout: 5), "no Try a message:\n\(app.debugDescription)")
        let began = Date.now
        tryButton.tap()

        let result = row.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "试了一句，没成功")).firstMatch
        XCTAssertTrue(result.waitForExistence(timeout: expected == "slow" ? 50 : 15),
                      "the probe ended without its \(expected) outcome beside the button that started it:\n\(app.debugDescription)")
        let reason = expected == "slow" ? "Not logged in" : "mock 探针没有回答"
        XCTAssertTrue(row.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", reason)).firstMatch.exists,
                      "the probe's failure reason is missing from \(rowID)")
        if expected == "model_error" {
            let first = app.descendants(matching: .any)["checklist.first_message"].firstMatch
            XCTAssertFalse(first.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", reason)).firstMatch.exists,
                           "another row must not claim the model row's failure")
        }
        if expected == "slow" {
            XCTAssertGreaterThan(Date.now.timeIntervalSince(began), 30, "the mock really outlasted the old client timeout")
        }
        XCTAssertTrue(tryButton.isEnabled, "a finished probe leaves Try a message available for another attempt")
        shot(app, "readiness-probe-\(expected)")
    }

    /// MOCK_START_ERROR=<code> on a ready hub: the refused start is a card with the hub's words, not a red line.
    @MainActor func testARefusedStartIsACard() throws {
        guard ProcessInfo.processInfo.environment["PERSONA_START_ERROR"] != nil else { throw XCTSkip("needs MOCK_START_ERROR") }
        let app = try launch()
        openNew(app)
        let field = app.textViews.firstMatch.waitForExistence(timeout: 15) ? app.textViews.firstMatch : app.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5))
        field.tap()
        field.typeText("你好")
        let send = app.buttons["shell-composer-send"].exists ? app.buttons["shell-composer-send"].firstMatch : app.buttons["发送"].firstMatch
        send.tap()
        let card = app.descendants(matching: .any)["readiness-card"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 15), "no card for the refused start:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "readiness-start-\(ProcessInfo.processInfo.environment["PERSONA_START_ERROR"] ?? "")")
    }

    /// The main conversation of MOCK_READINESS=not_logged_in / bad_key: its failed turn is a Model Not Connected card, not
    /// Claude Code's words as an answer, and the readiness card sits over its composer.
    @MainActor func testTheMainConversationShowsTheFix() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"],
              let main = ProcessInfo.processInfo.environment["PERSONA_MAIN"] else { throw XCTSkip("needs PERSONA_MOCK_HUB and PERSONA_MAIN") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant"]
        app.launchPinned()
        // The first launch after pairing lands on the new conversation whatever is asked: pair, then open the page.
        sleep(4)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let denyNotifications = springboard.buttons["不允许"].firstMatch
        if denyNotifications.exists { denyNotifications.tap() }
        app.terminate()
        app.launchArguments = ["-tab", "assistant", "-openAssistant", main, "-openDraftsFresh", "YES"]
        app.launchPinned()
        if denyNotifications.waitForExistence(timeout: 2) { denyNotifications.tap() }
        let fix = app.buttons["去修"].firstMatch
        XCTAssertTrue(fix.waitForExistence(timeout: 25), "no 模型没接好 card:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "readiness-\(scenario)-main")
        if let expected = ProcessInfo.processInfo.environment["PERSONA_EXPECT_TITLE"] {
            let title = app.staticTexts[expected].firstMatch
            XCTAssertTrue(title.waitForExistence(timeout: 5), "the error must name its launcher:\n\(app.debugDescription)")
            fix.tap()
            let detail = app.descendants(matching: .any)["launcher.detail"].firstMatch
            XCTAssertTrue(detail.waitForExistence(timeout: 10), "the repair must open the named launcher, not the wizard:\n\(app.debugDescription)")
        }
    }
}
