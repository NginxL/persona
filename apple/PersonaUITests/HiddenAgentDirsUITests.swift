import XCTest

/// #8578: Settings › Agent › Agent Folders, the Agents menu's temporary reveal, and a hidden folder's pending
/// approval coming back once its rule is off. #10053: a Block rule's Agent is gone even from Show hidden sessions, and
/// the rule's action can be switched from the page. Against scripts/mock/mock_hub.py (`TEST_RUNNER_PERSONA_MOCK_HUB`), whose
/// Mac Studio has one Agent in `/Users/me/code/photo-sync` with three pending cards. `TEST_RUNNER_PERSONA_SHOTS=<dir>`
/// keeps `hidden-dirs-<look>-<step>.png`; `TEST_RUNNER_PERSONA_LOOKS` picks the looks (default `light,dark,ax5`).
final class HiddenAgentDirsUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private var look = "light"
    private let folder = "~/code/photo-sync"
    private let hiddenTitle = "照片同步脚本支持 HEIC" // i18n-ignore: the mock's made-up Agent

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    private static let looks: [String: [String]] = [
        "light": ["-ui.scheme", "light"],
        "dark": ["-ui.scheme", "dark"],
        // The largest accessibility size is set on the simulator (`simctl ui <udid> content_size
        // accessibility-extra-extra-extra-large`): the launch argument does not reach every page.
        "ax5": ["-ui.scheme", "light"],
    ]

    private var chosenLooks: [String] {
        (ProcessInfo.processInfo.environment["PERSONA_LOOKS"] ?? "light,dark,ax5").split(separator: ",").map(String.init)
    }

    @MainActor func testHiddenFoldersPageMenuAndPendingCard() throws {
        for name in chosenLooks {
            look = name
            try runLook()
        }
    }

    @MainActor private func runLook() throws {
        // Agents before: no rule.
        try putRules([])
        launch(["-tab", "dev"])
        XCTAssertTrue(hiddenRow.waitForExistence(timeout: 30), "the photo-sync Agent is not listed before hiding:\n\(app.debugDescription)")
        shot("1-agents-before")

        // The rule on (and a switched-off one beside it): the Agent leaves the list live, its cards with it.
        try putRules([[folder, true], ["*scratch*", false]])
        XCTAssertTrue(waitGone(hiddenRow, 15), "the hidden folder's Agent is still listed")
        shot("2-agents-hidden")

        // ⋯ › Show hidden sessions, then ⋯ › Hide directory matches.
        openListMenu()
        let show = menuItem("Show hidden sessions")
        XCTAssertTrue(show.exists, "no Show hidden sessions in the menu:\n\(app.debugDescription)")
        shot("3-menu-show-hidden")
        show.tap()
        XCTAssertTrue(hiddenRow.waitForExistence(timeout: 10), "Show hidden sessions did not bring the Agent back")
        shot("4-agents-showing-hidden")
        openListMenu()
        let hide = menuItem("Hide directory matches")
        let shown = hide.exists
        shot("5-menu-hide-matches")
        XCTAssertTrue(shown, "no Hide directory matches in the menu:\n\(app.debugDescription)")
        hide.tap()
        XCTAssertTrue(waitGone(hiddenRow, 10), "Hide directory matches did not hide the Agent again")

        // Reconnect with the rule on: the cards stay in the hub's inventory, not on screen.
        app.terminate()
        launch(["-tab", "dev"])
        XCTAssertTrue(app.buttons.matching(identifier: "agents.row").firstMatch.waitForExistence(timeout: 30), "Agents did not load after reconnecting")
        sleep(2)
        XCTAssertFalse(hiddenRow.exists, "the hidden Agent came back after reconnecting")
        XCTAssertTrue(try pendingIDs().contains("ix_photos_check"), "the hub dropped the hidden folder's pending approval")
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", hiddenTitle)).firstMatch.exists,
                       "a hidden folder's card is still shown")
        shot("6-reconnected-hidden")

        // Settings › Agent › Hidden Agent Folders on Mac Studio.
        openHiddenFoldersPage()
        shot("7-page-rules")
        let field = app.textFields["Folder path or glob"].firstMatch
        bringIntoView(field)
        field.tap()
        field.typeText("abc")
        XCTAssertTrue(app.staticTexts["Start with / or ~/, or * for a folder name"].waitForExistence(timeout: 5), "no inline error for abc")
        shot("8-page-inline-error")
        field.typeText(String(repeating: XCUIKeyboardKey.delete.rawValue, count: 3))
        dismissKeyboard()

        // Switch the rule off from the page: the Agent and its cards come back, and the approval can be answered.
        bringIntoView(ruleSwitch)
        // The switch element spans its label (the pattern); tap the knob at its trailing end.
        ruleSwitch.coordinate(withNormalizedOffset: CGVector(dx: 0.93, dy: 0.5)).tap()
        XCTAssertTrue(waitFor { (try? self.rules().first?["enabled"] as? Bool) == false }, "switching the rule off did not save")
        shot("9-page-rule-off")
        app.terminate()
        launch(["-tab", "dev"])
        XCTAssertTrue(hiddenRow.waitForExistence(timeout: 30), "the Agent is not back after switching its rule off")
        shot("10-agents-unhidden")
        try answerApproval()

        // Load failed, then Retry.
        try putRules([[folder, true]])
        // Every read fails until Retry is about to be tapped (the page reads the first computer, then Mac Studio).
        try get("/mock/agent_dirs_fail?n=100")
        openHiddenFoldersPage()
        let retry = app.buttons["Retry"].firstMatch
        XCTAssertTrue(retry.waitForExistence(timeout: 15), "no Retry after a failed load:\n\(app.debugDescription)")
        shot("11-page-load-failed")
        try get("/mock/agent_dirs_fail?n=0")
        retry.tap()
        XCTAssertTrue(ruleSwitch.waitForExistence(timeout: 15), "Retry did not load the rules")
        shot("12-page-retried")

        // Claude Code Settings › Blocked Config Folders, for comparison.
        app.navigationBars.buttons.element(boundBy: 0).tap()
        let claude = app.buttons["Claude Code Settings"].firstMatch
        XCTAssertTrue(claude.waitForExistence(timeout: 10))
        claude.tap()
        let blocked = app.staticTexts["Blocked Config Folders"].firstMatch
        bringIntoView(blocked, top: true)
        shot("13-blocked-config-folders")
        app.terminate()

        // #10053: a Block rule. The mock lists nothing of the folder, as a node that blocks it: Show hidden sessions
        // does not bring it back.
        try putRules([[folder, true, "block"]])
        launch(["-tab", "dev"])
        XCTAssertTrue(app.buttons.matching(identifier: "agents.row").firstMatch.waitForExistence(timeout: 30), "Agents did not load")
        sleep(2)
        XCTAssertFalse(hiddenRow.exists, "a blocked folder's Agent is listed")
        shot("14-agents-blocked")
        openListMenu()
        menuItem("Show hidden sessions").tap()
        sleep(2)
        XCTAssertFalse(hiddenRow.exists, "Show hidden sessions brought a blocked folder's Agent back")
        shot("15-blocked-not-in-hidden")
        openHiddenFoldersPage()
        let picker = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "Action: \(folder)")).firstMatch
        XCTAssertTrue(picker.waitForExistence(timeout: 15), "no action picker for the rule:\n\(app.debugDescription)")
        bringIntoView(picker)
        shot("16-page-block-rule")
        picker.tap()
        let hideItem = app.buttons["Hide"].firstMatch
        XCTAssertTrue(hideItem.waitForExistence(timeout: 5), "no Hide in the action menu:\n\(app.debugDescription)")
        hideItem.tap()
        XCTAssertTrue(waitFor { (try? self.rules().first?["action"] as? String) == "hide" }, "switching the rule to Hide did not save")
        shot("17-page-switched-to-hide")
        app.terminate()
        try putRules([])
    }

    // MARK: - Steps

    @MainActor private func launch(_ extra: [String]) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-openAgentsFresh", "YES", "-dev.grouping", "space",
                               "-holdChromeOpen", "YES", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"]
            + (Self.looks[look] ?? []) + extra
        app.launchPinned()
        allowNotificationsIfAsked()
    }

    /// A fresh install asks for notifications in a system alert over the list: the elements under it stay in the
    /// hierarchy, so "gone" checks would never pass. The alert's second button allows, in any system language.
    @MainActor private func allowNotificationsIfAsked() {
        let alert = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch
        if alert.waitForExistence(timeout: 3), alert.buttons.count > 1 {
            alert.buttons.element(boundBy: 1).tap()
        }
    }

    /// The hidden folder's Agent anywhere in Agents: its row, or its cards in Needs You.
    @MainActor private var hiddenRow: XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", hiddenTitle)).firstMatch
    }

    @MainActor private var ruleSwitch: XCUIElement {
        app.switches.matching(NSPredicate(format: "label CONTAINS %@", folder)).firstMatch
    }

    @MainActor private func openListMenu() {
        // On iPhone the list's options are the selected Agents segment's menu (ShellSwitch's workMenu).
        let menu = app.buttons["shell-agents"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 10), "no Agents list menu:\n\(app.debugDescription)")
        menu.tap()
    }

    /// A list menu item, scrolled to: at the largest text sizes the menu is taller than the screen.
    @MainActor private func menuItem(_ label: String) -> XCUIElement {
        let item = app.buttons[label].firstMatch
        _ = item.waitForExistence(timeout: 5)
        let menu = app.collectionViews.containing(NSPredicate(format: "label == %@", "By Repository")).firstMatch
        for _ in 0..<8 where (!item.exists || !item.isHittable) && menu.exists {
            menu.swipeUp()
        }
        return item
    }

    @MainActor private func openHiddenFoldersPage() {
        app.terminate()
        launch(["-tab", "settings"])
        XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
        let agent = app.settingsAdvancedRow("settings-agents")
        XCTAssertTrue(agent.waitForExistence(timeout: 20), "no Settings › Advanced › Agent:\n\(app.debugDescription)")
        agent.tap()
        let page = app.buttons["Agent Folders"].firstMatch
        XCTAssertTrue(page.waitForExistence(timeout: 10), "no Agent Folders row:\n\(app.debugDescription)")
        page.tap()
        // The computer picker starts on the first computer; pick Mac Studio.
        let picker = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Machine'")).firstMatch
        XCTAssertTrue(picker.waitForExistence(timeout: 10), "no computer picker:\n\(app.debugDescription)")
        if !picker.label.contains("Mac Studio") {
            picker.tap()
            let studio = app.buttons["Mac Studio"].firstMatch
            XCTAssertTrue(studio.waitForExistence(timeout: 5), "no Mac Studio in the picker:\n\(app.debugDescription)")
            studio.tap()
        }
        _ = ruleSwitch.waitForExistence(timeout: 10)
    }

    @MainActor private func answerApproval() throws {
        // Needs You lists the approval again (its Bash command), with its own Allow.
        let command = app.staticTexts.matching(NSPredicate(format: "label CONTAINS 'python3 -m unittest discover'")).firstMatch
        XCTAssertTrue(command.waitForExistence(timeout: 15), "the photo-sync approval is not back:\n\(app.debugDescription)")
        let allow = app.buttons.matching(NSPredicate(format: "identifier == 'agents.interaction' AND label == 'Allow'"))
            .allElementsBoundByIndex.filter { $0.frame.minY > command.frame.minY }.min { $0.frame.minY < $1.frame.minY }
        let button = try XCTUnwrap(allow, "the approval has no Allow:\n\(app.debugDescription)")
        shot("10b-approval-back")
        button.tap()
        XCTAssertTrue(waitFor { (try? self.pendingIDs().contains("ix_photos_check")) == false }, "allowing did not reach the hub")
        shot("10c-approval-answered")
    }

    // MARK: - Helpers

    @MainActor private func shot(_ step: String) {
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = "hidden-dirs-\(look)-\(step)"
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("hidden-dirs-\(look)-\(step).png"))
        }
    }

    @MainActor private func bringIntoView(_ element: XCUIElement, top: Bool = false) {
        for _ in 0..<10 where !element.exists || !element.isHittable || (top && element.frame.minY > app.frame.midY) {
            app.swipeUp()
        }
    }

    @MainActor private func dismissKeyboard() {
        if app.keyboards.firstMatch.exists { app.navigationBars.firstMatch.tap() }
    }

    @MainActor private func waitGone(_ element: XCUIElement, _ seconds: TimeInterval) -> Bool {
        waitFor(seconds) { !element.exists }
    }

    @MainActor private func waitFor(_ seconds: TimeInterval = 15, _ condition: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(seconds)
        while Date() < end {
            if condition() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.5))
        }
        return condition()
    }

    private func putRules(_ rules: [[Any]]) throws {
        // [pattern, enabled] (a rule from before blocking: it hides) or [pattern, enabled, action].
        let body = ["rules": rules.map { r -> [String: Any] in
            var rule: [String: Any] = ["pattern": r[0], "enabled": r[1]]
            if r.count > 2 { rule["action"] = r[2] }
            return rule
        }]
        try call("PUT", "/api/v1/machines/studio/agent-dirs/hidden", body: body)
    }

    private func rules() throws -> [[String: Any]] {
        let data = try call("GET", "/api/v1/machines/studio/agent-dirs/hidden")
        return (try JSONSerialization.jsonObject(with: data) as? [String: Any])?["rules"] as? [[String: Any]] ?? []
    }

    private func pendingIDs() throws -> [String] {
        let json = try JSONSerialization.jsonObject(with: try call("GET", "/api/v1/interactions"))
        let list = (json as? [[String: Any]]) ?? ((json as? [String: Any])?["interactions"] as? [[String: Any]]) ?? []
        return list.compactMap { $0["id"] as? String }
    }

    @discardableResult private func get(_ path: String) throws -> Data { try call("GET", path) }

    @discardableResult private func call(_ method: String, _ path: String, body: Any? = nil) throws -> Data {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = method
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        if let body { request.httpBody = try JSONSerialization.data(withJSONObject: body) }
        let done = DispatchSemaphore(value: 0)
        var out: Result<Data, Error> = .failure(URLError(.timedOut))
        URLSession.shared.dataTask(with: request) { data, response, error in
            if let error { out = .failure(error) }
            else if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
                out = .failure(URLError(.badServerResponse, userInfo: ["status": http.statusCode, "path": path]))
            } else { out = .success(data ?? Data()) }
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 10)
        return try out.get()
    }
}
