import XCTest

final class SettingsNavigationUITests: XCTestCase {
    override func setUp() { continueAfterFailure = false }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    @MainActor private func launch(_ extra: [String] = [], hubKey: String = "PERSONA_SETTINGS_HUB",
                                   language: (String, String) = ("zh-Hans", "zh_CN")) throws -> XCUIApplication {
        guard let hub = ProcessInfo.processInfo.environment[hubKey] else {
            throw XCTSkip("Needs an isolated mock hub in \(hubKey)")
        }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", "",
                               "-AppleLanguages", "(\(language.0))", "-AppleLocale", language.1] + extra
        app.launchPinned()
        return app
    }

    @MainActor private func element(_ id: String, _ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    @MainActor private func reveal(_ id: String, _ app: XCUIApplication) -> XCUIElement {
        let target = element(id, app)
        let list = element("settings-list", app)
        for _ in 0..<12 where !target.isHittable { list.swipeUp() }
        return target
    }

    /// The first element whose identifier starts with `prefix`.
    @MainActor private func element(prefix: String, _ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH %@", prefix)).firstMatch
    }

    /// TEST_RUNNER_PERSONA_UI_TEXT_SIZE=<content size category> launches at that text size (the largest for screenshots).
    private static var textSize: [String] {
        guard let size = ProcessInfo.processInfo.environment["PERSONA_UI_TEXT_SIZE"], !size.isEmpty else { return [] }
        return ["-UIPreferredContentSizeCategoryName", size]
    }

    /// TEST_RUNNER_PERSONA_SHOTS=<dir> keeps a screenshot, named with TEST_RUNNER_PERSONA_SHOTS_APPEARANCE when set.
    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
        let look = env["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("settings-\(device)-\(name)\(look).png"))
    }

    /// Each row of Appearance & Display that leads to a page opens it, and Back comes back (#12035). On an iPad the
    /// Settings list is a column beside the page; a page the column opened by its own selection, outside Settings' path,
    /// opened none of its rows. iPad in portrait and landscape; an iPhone in its one stack.
    @MainActor func testAppearancePagesOpenAndComeBack() throws {
        let pad = UIDevice.current.userInterfaceIdiom == .pad
        let turns: [(String, UIDeviceOrientation)] = pad ? [("portrait", .portrait), ("landscape", .landscapeLeft)] : [("portrait", .portrait)]
        // Each row, and the start of an identifier only its page has.
        let pages = [("settings-color", "color-"), ("settings-style", "style-"), ("settings-navigation", "navigation-layout-"),
                     ("settings-layout-page", "layout-agents-"), ("settings-scheme", "scheme-")]
        for (turn, orientation) in turns {
            XCUIDevice.shared.orientation = orientation
            let app = try launch(Self.textSize)
            XCTAssertTrue(element("settings-list", app).waitForExistence(timeout: 30), "\(turn): no Settings:\n\(app.debugDescription)")
            // Appearance & Display is under Settings › Advanced (#13395).
            XCTAssertTrue(app.openSettingsAdvancedPage(), "\(turn): no Settings › Advanced:\n\(app.debugDescription)")
            let appearance = app.settingsAdvancedRow("settings-appearance")
            XCTAssertTrue(appearance.waitForExistence(timeout: 10), "\(turn): no Appearance & Display:\n\(app.debugDescription)")
            appearance.tap()
            XCTAssertTrue(element("settings-color", app).waitForExistence(timeout: 10), "\(turn): Appearance & Display did not open")
            shot(app, "\(turn)-appearance")
            for (row, mark) in pages {
                let link = element(row, app)
                XCTAssertTrue(link.waitForExistence(timeout: 10), "\(turn): no \(row) on Appearance & Display:\n\(app.debugDescription)")
                link.tap()
                let page = element(prefix: mark, app)
                XCTAssertTrue(page.waitForExistence(timeout: 10), "\(turn): \(row) did not open its page:\n\(app.debugDescription)")
                if row == "settings-layout-page" { shot(app, "\(turn)-layout") }
                let back = app.navigationBars.buttons["BackButton"].firstMatch
                XCTAssertTrue(back.waitForExistence(timeout: 5), "\(turn): no Back on the page \(row) opened:\n\(app.debugDescription)")
                back.tap()
                XCTAssertTrue(page.waitForNonExistence(timeout: 10), "\(turn): Back did not leave the page \(row) opened")
            }
            app.terminate()
        }
    }

    /// A page opened from the search results opens its own pages too (#12035).
    @MainActor func testSearchResultOpensItsPages() throws {
        let app = try launch()
        XCTAssertTrue(element("settings-list", app).waitForExistence(timeout: 30))
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText("外观")
        let result = element("settings-search-appearance", app)
        XCTAssertTrue(result.waitForExistence(timeout: 10), "no Appearance & Display result:\n\(app.debugDescription)")
        result.tap()
        let layout = element("settings-layout-page", app)
        XCTAssertTrue(layout.waitForExistence(timeout: 10), "the result did not open Appearance & Display")
        layout.tap()
        XCTAssertTrue(element(prefix: "layout-agents-", app).waitForExistence(timeout: 10),
                      "Agents List Layout did not open from the result's page:\n\(app.debugDescription)")
    }

    /// Settings home: the core rows under the three scopes, then Advanced (#13395); Advanced has the rest under the same
    /// scopes.
    @MainActor func testThreeGroupsAndCommonContainers() throws {
        let app = try launch()
        XCTAssertTrue(element("settings-list", app).waitForExistence(timeout: 30))
        XCTAssertTrue(element("settings-models", app).exists)
        XCTAssertTrue(element("settings-assistant", app).exists)
        XCTAssertTrue(element("settings-assistant-switch", app).exists)
        XCTAssertTrue(reveal("settings-notifications", app).exists)
        XCTAssertTrue(element("settings-group-device", app).exists)
        XCTAssertTrue(reveal("settings.hubs", app).exists)
        XCTAssertTrue(element("settings-group-hub", app).exists)
        XCTAssertTrue(reveal("settings-advanced", app).exists)
        for moved in ["settings-appearance", "settings-pairing", "settings-persona", "settings-agents"] {
            XCTAssertFalse(element(moved, app).exists, "\(moved) is under Advanced, not on Settings home")
        }
        XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
        XCTAssertTrue(app.settingsAdvancedRow("settings-appearance").exists)
        XCTAssertTrue(app.settingsAdvancedRow("settings-pairing").exists)
        XCTAssertTrue(element("settings-persona", app).exists)
        XCTAssertFalse(element("settings-new-chat-check", app).exists)
    }

    /// Screenshots for review (#13395): Settings home at its top and its foot, and Settings › Advanced at its top and its
    /// foot, light and dark, in English and Chinese. Only with TEST_RUNNER_PERSONA_SHOTS:
    /// `settings-<home|advanced>-<top|foot>-<light|dark>-<language>.png`.
    @MainActor func testAdvancedShots() throws {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { throw XCTSkip("screenshots only") }
        func shot(_ app: XCUIApplication, _ name: String) {
            sleep(1) // a scroll settles
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: name + ".png"))
        }
        for language in [("en", "en_US"), ("zh-Hans", "zh_CN")] {
            for scheme in ["light", "dark"] {
                let app = try launch(["-ui.scheme", scheme], language: language)
                let tag = "\(scheme)-\(language.0)"
                XCTAssertTrue(element("settings-list", app).waitForExistence(timeout: 30))
                sleep(2) // the hub's roster and machines arrive
                shot(app, "settings-home-top-\(tag)")
                XCTAssertTrue(reveal("settings-advanced", app).exists)
                shot(app, "settings-home-foot-\(tag)")
                XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
                shot(app, "settings-advanced-top-\(tag)")
                XCTAssertTrue(app.settingsAdvancedRow("settings-pairing").exists)
                shot(app, "settings-advanced-foot-\(tag)")
                app.terminate()
            }
        }
    }

    @MainActor func testSearchAliasesOpenADevicePage() throws {
        let app = try launch()
        XCTAssertTrue(element("settings-list", app).waitForExistence(timeout: 30))
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 10))
        field.tap()
        field.typeText("听写")
        let result = element("settings-search-language", app)
        XCTAssertTrue(result.waitForExistence(timeout: 10))
        result.tap()
        XCTAssertTrue(element("dictation-language", app).waitForExistence(timeout: 10))
    }

    @MainActor func testSearchDeveloperAliasesAndNoResults() throws {
        let app = try launch(["-openAppearance", "search:commit"])
        XCTAssertTrue(element("settings-search-about", app).waitForExistence(timeout: 30))
        element("settings-search-about", app).tap()
        XCTAssertTrue(element("settings-diagnostics", app).waitForExistence(timeout: 10))
        XCTAssertFalse(app.staticTexts["构建"].exists)
        element("settings-diagnostics", app).tap()
        XCTAssertTrue(app.staticTexts["构建"].waitForExistence(timeout: 10))
    }

    @MainActor func testLegacySettingsPathsStillOpen() throws {
        let app = try launch(["-openTriageSettings", "YES"])
        XCTAssertTrue(element("triage-enabled", app).waitForExistence(timeout: 30))
    }

    @MainActor func testSettingsAssistantSwitchOpensSharedList() throws {
        let app = try launch()
        let header = element("settings-assistant-switch", app)
        XCTAssertTrue(header.waitForExistence(timeout: 30), "no Settings assistant switch:\n\(app.debugDescription)")
        XCTAssertTrue(header.label.contains("的设置, "), "the full Settings title and current state: \(header.label)")
        XCTAssertFalse(header.label.contains("别的助手有事等你处理"), "Settings has no other-assistant waiting dot: \(header.label)")
        header.tap()
        XCTAssertTrue(element("assistant-switcher-list", app).waitForExistence(timeout: 10),
                      "the shared list did not open:\n\(app.debugDescription)")
        XCTAssertTrue(element("assistant-switcher-new", app).waitForExistence(timeout: 10))
        XCTAssertTrue(element("assistant-switcher-manage", app).exists)
    }

    @MainActor func testSingleAssistantSwitchHasNoDefaultOrWaitingBadge() throws {
        let app = try launch(hubKey: "PERSONA_SETTINGS_SINGLE_HUB")
        let header = element("settings-assistant-switch", app)
        XCTAssertTrue(header.waitForExistence(timeout: 30), "no single-assistant Settings switch:\n\(app.debugDescription)")
        header.tap()
        let list = element("assistant-switcher-list", app)
        XCTAssertTrue(list.waitForExistence(timeout: 10), "the single-assistant list did not open:\n\(app.debugDescription)")
        let row = element("assistant-switcher-row-assistant", app)
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no current assistant row:\n\(app.debugDescription)")
        let rows = list.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH %@", "assistant-switcher-row-"))
        XCTAssertEqual(rows.count, 1, "one installed assistant means one row:\n\(app.debugDescription)")
        // AssistantSwitcherText.spoken is the row's accessibility representation of its Default chip and waiting badge.
        XCTAssertFalse(row.label.contains("默认"), "one assistant has no Default chip: \(row.label)")
        XCTAssertFalse(row.label.contains("件等你处理"), "the only, current assistant has no waiting count: \(row.label)")
    }
}
