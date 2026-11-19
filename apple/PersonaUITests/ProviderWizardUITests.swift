import XCTest

/// Settings › Model Providers (#2455) in Demo Mode (DemoHub, no hub needed): the provider wizard walked through (Choose Provider → Connect →
/// Choose Models → Save), Import from ~/.zshrc (a machine, its aliases ticked, imported) and a machine whose personad is too old.
/// TEST_RUNNER_PERSONA_SHOTS=<dir> keeps a screenshot of each step, named with TEST_RUNNER_PERSONA_APPEARANCE (light / dark).
final class ProviderWizardUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws { continueAfterFailure = false }

    @MainActor private func launch(_ step: String) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-tab", "settings", "-dev.openChat", "",
                               "-openModelLaunchers", "YES", "-openProviderWizard", step]
        app.launchPinned()
    }

    @MainActor private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let look = ProcessInfo.processInfo.environment["PERSONA_APPEARANCE"] ?? "light"
        try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "wizard-\(name)-\(look).png"))
    }

    @MainActor private func el(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    @MainActor private func type(_ id: String, _ text: String) {
        let f = el(id)
        XCTAssertTrue(f.waitForExistence(timeout: 5), "no \(id):\n\(app.debugDescription)")
        f.tap()
        f.typeText(text)
    }

    @MainActor func testTheWizardAddsAProvider() {
        launch("pick")
        let custom = el("wizard.kind.custom")
        XCTAssertTrue(custom.waitForExistence(timeout: 15), "no 选供应商:\n\(app.debugDescription)")
        XCTAssertTrue(el("wizard.kind.anthropic").exists && el("wizard.kind.import").exists)
        shot("1-pick")
        custom.tap()

        let next = app.buttons["wizard.next"]
        XCTAssertTrue(next.waitForExistence(timeout: 5))
        XCTAssertFalse(next.isEnabled, "下一步 before anything is filled in")
        type("wizard.name", "jv_ui\n")
        type("wizard.url", "https://gateway.example.com\n")
        type("wizard.key", "sk-test-ui")
        XCTAssertTrue(next.isEnabled, "下一步 once name, address and key are in")
        app.buttons["wizard.discover"].tap()
        XCTAssertTrue(app.staticTexts["5 个"].waitForExistence(timeout: 5), "no models discovered:\n\(app.debugDescription)")
        app.buttons["wizard.test"].tap()
        XCTAssertTrue(el("wizard.testResult").waitForExistence(timeout: 5), "no test result")
        XCTAssertTrue(app.staticTexts["连接正常"].exists)
        shot("2-connect")

        next.tap()
        let glm = app.buttons["wizard.model.glm-5"]
        XCTAssertTrue(glm.waitForExistence(timeout: 5), "no 勾选模型:\n\(app.debugDescription)")
        let save = app.buttons["wizard.save"]
        XCTAssertFalse(save.isEnabled, "保存 with no model ticked")
        glm.tap()
        app.buttons["wizard.model.kimi-k2"].tap()
        XCTAssertEqual(glm.label, "已勾选 GLM-5")
        XCTAssertTrue(save.isEnabled)
        shot("3-models")
        // Back keeps what was typed.
        app.buttons["wizard.back"].tap()
        XCTAssertTrue(next.waitForExistence(timeout: 5))
        next.tap()
        XCTAssertTrue(app.buttons["wizard.model.glm-5"].waitForExistence(timeout: 5))
        XCTAssertEqual(app.buttons["wizard.model.glm-5"].label, "已勾选 GLM-5")
        save.tap()
        XCTAssertTrue(save.waitForNonExistence(timeout: 10), "the sheet stays:\n\(app.debugDescription)")
        PasswordPrompt.assertNone(app, "a provider's API key")
        XCTAssertTrue(app.staticTexts["jv_ui"].waitForExistence(timeout: 10), "jv_ui is not in the list:\n\(app.debugDescription)")
    }

    @MainActor func testImportFromZshrc() {
        launch("import")
        let mac = app.buttons["import.machine.macbook"]
        XCTAssertTrue(mac.waitForExistence(timeout: 15), "no machine list:\n\(app.debugDescription)")
        shot("4-import-machine")
        mac.tap()
        let glm = app.buttons["import.provider.cc_glm"]
        XCTAssertTrue(glm.waitForExistence(timeout: 10), "no preview:\n\(app.debugDescription)")
        XCTAssertEqual(glm.label, "已勾选 cc_glm")
        XCTAssertTrue(app.staticTexts["合并到 demo_gateway"].exists)
        shot("5-import-preview")
        glm.tap()
        XCTAssertEqual(glm.label, "未勾选 cc_glm")
        app.buttons["import.confirm"].tap()
        XCTAssertTrue(el("import.result").waitForExistence(timeout: 10), "no result:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "新增 1 个")).firstMatch.exists, "\(app.debugDescription)")
        shot("6-import-done")
        app.buttons["wizard.done"].tap()
        XCTAssertTrue(app.staticTexts["cc_kimi"].waitForExistence(timeout: 10), "cc_kimi is not in the list")
    }

    /// Model Settings › Main Chat has no How It Runs since #9799: Demo Mode answers as the hub does, without `runtimes`
    /// (the picker for an older hub that still sends them is covered by ProviderWizardTests).
    @MainActor func testTheMainConversationsRuntime() {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-tab", "settings", "-dev.openChat", "",
                               "-openModelConfiguration", "YES", "-openModelPurpose", "assistant"]
        app.launchPinned()
        let advanced = el("model-config-advanced")
        XCTAssertTrue(advanced.waitForExistence(timeout: 15), "no Advanced:\n\(app.debugDescription)")
        for _ in 0..<4 where !advanced.isHittable { app.swipeUp() }
        advanced.tap()
        XCTAssertTrue(el("model-config-provider").waitForExistence(timeout: 5), "Advanced did not open:\n\(app.debugDescription)")
        shot("8-runtime")
        XCTAssertFalse(el("model-config-runtime").exists, "Main Chat still offers How It Runs:\n\(app.debugDescription)")
    }

    /// Choose Models › Advanced has the provider's own environment (#5953), written like the headers.
    @MainActor func testAdvancedHasTheProvidersEnvironment() {
        launch("models")
        let advanced = el("wizard.advanced")
        XCTAssertTrue(advanced.waitForExistence(timeout: 15), "no Advanced:\n\(app.debugDescription)")
        advanced.tap()
        // Found by its placeholder: the disclosure group's identifier is its fields' too.
        let env = app.descendants(matching: .any).matching(NSPredicate(format: "placeholderValue == %@", "环境变量，一行一个 NAME=value")).firstMatch
        for _ in 0..<8 where !(env.exists && env.isHittable) { app.swipeUp() }
        XCTAssertTrue(env.isHittable, "no environment field:\n\(app.debugDescription)")
        env.tap()
        env.typeText("HTTP_PROXY=")
        shot("9-advanced-env")
    }

    /// Advanced › Proxy (#6238): one row saying the choice, and a page like iOS Settings' Configure Proxy behind it. Manual
    /// without a server can't be saved; with one, the row says the server.
    @MainActor func testTheProxyPageIsLikeConfigureProxy() {
        launch("models")
        // A model ticked, so the provider can be saved: Manual without a server is what stops it below.
        let glm = app.buttons["wizard.model.glm-5"]
        XCTAssertTrue(glm.waitForExistence(timeout: 15), "no 勾选模型:\n\(app.debugDescription)")
        glm.tap()
        let save = app.buttons["wizard.save"]
        XCTAssertTrue(save.isEnabled, "保存 with GLM-5 ticked")
        let advanced = el("wizard.advanced")
        XCTAssertTrue(advanced.waitForExistence(timeout: 15), "no Advanced:\n\(app.debugDescription)")
        advanced.tap()
        // Found by its label, the title and the choice together: the disclosure group's identifier is its rows' too.
        let row = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "代理")).firstMatch
        for _ in 0..<8 where !(row.exists && row.isHittable) { app.swipeUp() }
        XCTAssertTrue(row.isHittable, "no Proxy row:\n\(app.debugDescription)")
        XCTAssertTrue(row.label.contains("跟随这台电脑"), row.label)
        shot("12-proxy-row")

        row.tap()
        let follow = el("wizard.proxy.follow")
        XCTAssertTrue(follow.waitForExistence(timeout: 5), "no proxy page:\n\(app.debugDescription)")
        XCTAssertTrue(follow.isSelected && !el("wizard.proxy.server").exists)
        XCTAssertTrue(el("wizard.proxy.bypass").exists)
        shot("13-proxy-follow")

        el("wizard.proxy.direct").tap()
        XCTAssertTrue(el("wizard.proxy.bypass").waitForNonExistence(timeout: 5), "No Proxy still lists addresses")
        XCTAssertTrue(el("wizard.proxy.direct").isSelected)
        shot("14-proxy-direct")

        el("wizard.proxy.manual").tap()
        let server = el("wizard.proxy.server")
        XCTAssertTrue(server.waitForExistence(timeout: 5), "no server field:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["请填写代理服务器，或选「跟随这台电脑」。"].exists, "no hint for an empty server:\n\(app.debugDescription)")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(row.waitForExistence(timeout: 5) && row.label.contains("手动"), row.label)
        XCTAssertFalse(save.isEnabled, "Manual without a server saves")

        row.tap()
        XCTAssertTrue(server.waitForExistence(timeout: 5))
        server.tap()
        server.typeText("http://127.0.0.1:7890")
        type("wizard.proxy.bypass", "localhost, .corp.example")
        XCTAssertTrue(app.staticTexts["请填写代理服务器，或选「跟随这台电脑」。"].waitForNonExistence(timeout: 5))
        shot("15-proxy-manual")
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(row.waitForExistence(timeout: 5) && row.label.contains("http://127.0.0.1:7890"), row.label)
        XCTAssertTrue(save.isEnabled, "Manual with a server does not save")
        shot("16-proxy-row-manual")
    }

    /// A wrapper script on a machine's PATH (#5954) is listed under From PATH, and its page says where it comes from.
    @MainActor func testAScriptOnPathSaysWhereItComesFrom() {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-tab", "settings", "-dev.openChat", "", "-openModelLaunchers", "YES"]
        app.launchPinned()
        let row = app.staticTexts["claude-work"]
        for _ in 0..<6 where !(row.exists && row.isHittable) { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no claude-work:\n\(app.debugDescription)")
        // Its own section since #6248 split the rc files' launchers by file.
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label BEGINSWITH %@", "来自 PATH")).firstMatch.exists, "\(app.debugDescription)")
        shot("10-path-list")
        row.tap()
        // The row reads as one element, its title and value together.
        let source = app.descendants(matching: .any).matching(NSPredicate(format: "label == %@", "来源、PATH 上的脚本")).firstMatch
        XCTAssertTrue(source.waitForExistence(timeout: 10), "no source:\n\(app.debugDescription)")
        let text = app.buttons.matching(NSPredicate(format: "label CONTAINS %@", "脚本原文")).firstMatch
        for _ in 0..<6 where !(text.exists && text.isHittable) { app.swipeUp() }
        text.tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "~/.local/bin/claude-work")).firstMatch.waitForExistence(timeout: 5),
                      "no script text:\n\(app.debugDescription)")
        shot("11-path-detail")
    }

    @MainActor func testAnOutdatedNodeIsExplained() {
        launch("import")
        let mini = app.buttons["import.machine.mini"]
        XCTAssertTrue(mini.waitForExistence(timeout: 15))
        mini.tap()
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "personad 太旧")).firstMatch.waitForExistence(timeout: 10),
                      "no outdated message:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "重新部署这台电脑")).firstMatch.exists)
        shot("7-import-outdated")
    }
}
