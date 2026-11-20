import XCTest

/// Settings → Model Providers → Add against the repository's synthetic provider hub (MOCK_PROVIDERS=1 and MOCK_MANAGERS=1).
final class ProviderPasteUITests: XCTestCase {
    private var fixtureHub: URL {
        get throws {
            let value = try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_PROVIDER_PASTE_HUB"])
            guard let url = URL(string: value), ["127.0.0.1", "localhost", "::1"].contains(url.host) else {
                throw XCTSkip("needs the local provider fixture")
            }
            return url
        }
    }

    @MainActor private func openWizard(_ look: String = "light", largest: Bool = false, demo: Bool = false) throws -> XCUIApplication {
        continueAfterFailure = false
        let app = XCUIApplication()
        // The test enters the providers page directly, then presses its ordinary Add button.
        app.launchArguments = ["-dev.openChat", "", "-tab", "settings", "-openModelLaunchers", "YES", "-ui.scheme", look]
        if demo {
            app.launchArguments += ["-demo.on", "YES"]
        } else {
            guard ProcessInfo.processInfo.environment["PERSONA_PROVIDER_PASTE_HUB"] != nil else {
                throw XCTSkip("needs a local mock hub with MOCK_PROVIDERS=1 and MOCK_MANAGERS=1")
            }
            let url = try fixtureHub.absoluteString.addingPercentEncoding(withAllowedCharacters: .alphanumerics)!
            app.launchArguments += ["-pairURL", "persona://pair?url=" + url + "&token=mock"]
        }
        app.launchArguments += ["-ui.textSize", largest ? "large" : "standard"]
        if largest { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let alert = springboard.alerts.firstMatch
        while alert.waitForExistence(timeout: 1) {
            guard let button = ["允许", "Allow", "好", "OK"].map({ alert.buttons[$0] }).first(where: \.exists) else { break } // i18n-ignore: system prompt
            button.tap()
        }
        let add = app.buttons["launchers.add"]
        XCTAssertTrue(add.waitForExistence(timeout: 20), "the provider list must open first")
        add.tap()
        let kind = app.buttons["wizard.kind.custom"]
        XCTAssertTrue(kind.waitForExistence(timeout: 10), "the normal Add wizard should offer its gateway row")
        return app
    }

    @MainActor private func reach(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<10 {
            if element.exists && element.isHittable { return }
            app.swipeUp()
        }
        XCTAssertTrue(element.exists && element.isHittable, "missing \(element)")
    }

    @MainActor private func waitEnabled(_ element: XCUIElement) {
        let enabled = XCTNSPredicateExpectation(predicate: NSPredicate(format: "isEnabled == YES"), object: element)
        XCTAssertEqual(XCTWaiter.wait(for: [enabled], timeout: 15), .completed)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        guard let folder = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let image = app.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        try image.pngRepresentation.write(to: URL(fileURLWithPath: folder).appendingPathComponent(name + ".png"))
    }

    private func launcherNames() throws -> Set<String> {
        let data = try Data(contentsOf: fixtureHub.appendingPathComponent("api/v1/launchers"))
        let root = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        let rows = try XCTUnwrap(root["launchers"] as? [[String: Any]])
        return Set(rows.compactMap { $0["name"] as? String })
    }

    @MainActor func testAddOffersPastingAndOnlyTheFoundAlias() throws {
        let app = try openWizard()
        let paste = app.buttons["wizard.paste"]
        reach(paste, in: app)
        let found = app.buttons["wizard.alias.cc_kimi_fast"]
        reach(found, in: app)
        waitEnabled(found)
        found.tap()
        let chosen = app.buttons["import.provider.cc_kimi_fast"]
        XCTAssertTrue(chosen.waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["import.provider.cc_kimi"].exists, "the preview included a sibling alias")
        XCTAssertTrue(app.buttons["import.confirm"].isEnabled)
        try shot(app, "alias-preview")
    }

    @MainActor func testHubWithoutTheFeatureKeepsTheExistingManualImport() throws {
        let app = try openWizard(demo: true)
        let manual = app.buttons["wizard.kind.import"]
        if !manual.exists {
            let advanced = app.descendants(matching: .any)["wizard.pick.advanced"].firstMatch
            reach(advanced, in: app)
            advanced.tap()
        }
        reach(manual, in: app)
        XCTAssertFalse(app.buttons["wizard.paste"].exists)
        XCTAssertFalse(app.buttons["wizard.alias.cc_kimi_fast"].exists)
        manual.tap()
        XCTAssertTrue(app.buttons["import.machine.macbook"].waitForExistence(timeout: 8))
    }

    @MainActor func testMatchesOpenTheExistingProvidersModelsOrOnlyItsKey() throws {
        for sameKey in [true, false] {
            let app = try openWizard()
            let paste = app.buttons["wizard.paste"]
            reach(paste, in: app)
            paste.tap()
            let field = app.descendants(matching: .any)["wizard.paste.text"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 8))
            field.tap()
            // gw_full and its matching key are from MOCK_PROVIDERS=1.
            let key = sameKey ? "sk-example-gw" : "fixture-different"
            field.typeText("ANTHROPIC_BASE_URL=https://gw.example.com\nANTHROPIC_AUTH_TOKEN=" + key + "\nANTHROPIC_MODEL=fixture-model")
            let match = app.buttons["wizard.paste.match.gw_full"]
            reach(match, in: app)
            if !sameKey { match.tap() }
            app.buttons["wizard.paste.continue"].tap()
            if sameKey {
                let newModel = app.buttons["wizard.model.fixture-model"]
                reach(newModel, in: app)
                XCTAssertTrue(app.buttons["wizard.save"].exists)
            } else {
                let name = app.textFields["wizard.name"]
                XCTAssertTrue(name.waitForExistence(timeout: 8))
                XCTAssertEqual(name.value as? String, "gw_full")
                XCTAssertTrue(app.buttons["wizard.next"].exists)
            }
            app.terminate()
        }
    }

    @MainActor func testPasteOpensTheEditableConnectionAndReviewSavesNothing() throws {
        for (look, largest) in [("light", false), ("dark", false), ("light", true), ("dark", true)] {
            let app = try openWizard(look, largest: largest)
            let before = try launcherNames()
            XCTAssertFalse(before.isEmpty)
            let suffix = look + (largest ? "-ax" : "")
            let paste = app.buttons["wizard.paste"]
            reach(paste, in: app)
            try shot(app, "entries-" + suffix)
            let alias = app.buttons["wizard.alias.cc_kimi_fast"]
            reach(alias, in: app)
            waitEnabled(alias)
            try shot(app, "found-" + suffix)
            alias.tap()
            XCTAssertTrue(app.buttons["import.provider.cc_kimi_fast"].waitForExistence(timeout: 10))
            try shot(app, "alias-" + suffix)
            app.buttons["wizard.back"].tap()
            reach(paste, in: app)
            paste.tap()
            let field = app.descendants(matching: .any)["wizard.paste.text"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 10))
            let next = app.buttons["wizard.paste.continue"]
            XCTAssertFalse(next.isEnabled)
            field.tap()
            field.typeText("ANTHROPIC_BASE_URL=https://atlas.invalid\nANTHROPIC_MODEL=atlas-small\nHOME=/demo")
            waitEnabled(next)
            let selected = app.buttons["wizard.paste.draft.0"]
            reach(selected, in: app)
            selected.tap()
            let note = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "HOME")).firstMatch
            reach(note, in: app)
            XCTAssertFalse(note.label.contains("/demo"))
            try shot(app, "review-" + suffix)
            XCTAssertEqual(try launcherNames(), before)
            next.tap()
            let base = app.textFields.matching(NSPredicate(format: "value CONTAINS %@", "atlas.invalid")).firstMatch
            XCTAssertTrue(base.waitForExistence(timeout: 8), "the editable connection should contain the draft URL")
            XCTAssertEqual(try launcherNames(), before)
            app.terminate()
        }
    }
}
