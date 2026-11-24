import XCTest

/// The same real navigation interaction on iOS and the Mac split layout, using only the synthetic mock hub.
final class SentNavigationUITests: XCTestCase {
    private var hub: String {
        get throws {
            try XCTUnwrap(ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"], "the isolated mock hub is required")
        }
    }

    @MainActor private func launch(legacy: Bool = false) throws -> XCUIApplication {
        let address = try hub
        _ = try Data(contentsOf: URL(string: address + "/mock/sent?mode=1")!)
        let pair = "persona://pair?url=" + address.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", "", legacy ? "-openSentAsYou" : "-openConnectors", "YES"]
        app.launchPinned()
        return app
    }

    @MainActor private func entry(_ app: XCUIApplication) -> XCUIElement {
        let row = app.buttons["connectors.sent"]
        #if os(iOS)
        for _ in 0..<12 where !row.isHittable { app.swipeUp() }
        #else
        let form = app.scrollViews.matching(NSPredicate(format: "identifier == %@", "connectors-list")).firstMatch
        for _ in 0..<8 where !row.isHittable { (form.exists ? form : app.scrollViews.firstMatch).swipeUp() }
        #endif
        return row
    }

    @MainActor private func back(_ app: XCUIApplication) {
        let button = app.navigationBars.buttons.element(boundBy: 0)
        XCTAssertTrue(button.waitForExistence(timeout: 10), "a secondary page needs Back")
        button.tap()
    }

    @MainActor func testEntryOpensSecondaryLedgerAndBackReturnsToConnectors() throws {
        let app = try launch()
        XCTAssertTrue(app.descendants(matching: .any)["connectors-list"].firstMatch.waitForExistence(timeout: 20))
        XCTAssertFalse(app.buttons["settings.sent"].exists)
        let row = entry(app)
        XCTAssertTrue(row.isHittable, app.debugDescription)
        XCTAssertTrue(row.label.contains("以你的名义发出的记录"))
        row.tap()
        XCTAssertTrue(app.buttons["sent.row.70"].waitForExistence(timeout: 20), app.debugDescription)
        back(app)
        XCTAssertTrue(entry(app).isHittable, "Back did not return to Connectors")
    }

    @MainActor func testLegacyLaunchHasTheSameParent() throws {
        let app = try launch(legacy: true)
        XCTAssertTrue(app.buttons["sent.row.70"].waitForExistence(timeout: 20), app.debugDescription)
        back(app)
        XCTAssertTrue(entry(app).isHittable, "the legacy launch lost its Connectors parent")
    }
}
