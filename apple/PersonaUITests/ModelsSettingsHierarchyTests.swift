import XCTest

/// Common model choices precede advanced setup; the existing purpose editor remains the only editor.
final class ModelsSettingsHierarchyTests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py")
        }
        hub = value
    }

    @MainActor private func launch(purpose: String? = nil, models: Bool = true) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", ""]
        if models { app.launchArguments += ["-openModelConfiguration", "YES"] }
        if let purpose { app.launchArguments += ["-openModelPurpose", purpose] }
        app.launchPinned()
        return app
    }

    @MainActor func testModelsShowsCommonUsesBeforeProviderSetup() {
        let app = launch()
        let mainChat = app.descendants(matching: .any)["model-purpose-assistant"].firstMatch
        XCTAssertTrue(mainChat.waitForExistence(timeout: 10))
        XCTAssertTrue(mainChat.isHittable, "Common choices are visible when Models opens.")
        let providers = app.descendants(matching: .any)["settings-model-providers"].firstMatch
        if providers.isHittable { XCTAssertGreaterThan(providers.frame.minY, mainChat.frame.minY) }
        for _ in 0..<4 where !providers.isHittable { app.swipeUp() }
        XCTAssertTrue(providers.exists)
        XCTAssertFalse(app.searchFields.firstMatch.exists, "Provider search belongs to the advanced destination.")
    }

    @MainActor func testAgentGroupsToolsAndKeepsTheTriageDestination() {
        let app = launch(models: false)
        XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
        let agents = app.settingsAdvancedRow("settings-agents")
        XCTAssertTrue(agents.waitForExistence(timeout: 10))
        agents.tap()
        XCTAssertTrue(app.descendants(matching: .any)["settings-agent-claude"].waitForExistence(timeout: 5))
        XCTAssertTrue(app.descendants(matching: .any)["settings-agent-codex"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["settings-agent-agy"].exists)
        let triage = app.descendants(matching: .any)["settings-triage"].firstMatch
        XCTAssertTrue(triage.exists)
        triage.tap()
        XCTAssertTrue(app.descendants(matching: .any)["triage-enabled"].waitForExistence(timeout: 10))
    }

    @MainActor func testPurposeEditorDisclosesProviderAndRuntimeOnlyOnRequest() {
        let app = launch(purpose: "assistant")
        let advanced = app.descendants(matching: .any)["model-config-advanced"].firstMatch
        XCTAssertTrue(advanced.waitForExistence(timeout: 10))
        XCTAssertFalse(app.descendants(matching: .any)["model-config-provider"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["model-config-runtime"].exists)
        for _ in 0..<4 where !advanced.isHittable { app.swipeUp() }
        advanced.tap()
        XCTAssertTrue(app.descendants(matching: .any)["model-config-provider"].waitForExistence(timeout: 5))
    }
}
