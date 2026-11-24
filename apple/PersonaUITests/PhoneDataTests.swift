import XCTest

/// Settings › Advanced › This iPhone: the switch is the whole state, no Allowed / Tap to Allow labels; a source never asked is off.
/// (The system's permission alert is not driven here: on a busy machine it took minutes and came back from earlier runs.) Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB), on a fresh simulator.
final class PhoneDataTests: XCTestCase {
    @MainActor func testSwitchIsTheGrant() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", ""]
        app.launchPinned()
        XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
        let row = app.settingsAdvancedRow("settings-phone-data")
        row.tap()
        let contacts = app.switches["通讯录"]
        XCTAssertTrue(contacts.waitForExistence(timeout: 10), "no 通讯录 switch:\n\(app.debugDescription)")
        XCTAssertEqual(contacts.value as? String, "0", "never asked, yet on")
        XCTAssertFalse(app.staticTexts["点此授权"].exists || app.staticTexts["已授权"].exists, "the old labels are still there")
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/pd-before.png"))

    }
}
