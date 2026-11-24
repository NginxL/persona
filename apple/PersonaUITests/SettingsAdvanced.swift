import XCTest

extension XCUIApplication {
    /// Opens Settings › Advanced from Settings home (#13395): the rows the home no longer shows (Appearance & Display,
    /// Language & Dictation, Storage, Agent, Hub Version, Avatar, Devices & Pairing…, `SettingsCatalog.advanced`).
    /// Scrolls Settings home to its Advanced row, taps it and waits for the page. False when either is missing.
    @MainActor @discardableResult func openSettingsAdvancedPage(timeout: TimeInterval = 15) -> Bool {
        let list = descendants(matching: .any)["settings-list"].firstMatch
        guard list.waitForExistence(timeout: 30) else { return false }
        let row = descendants(matching: .any)["settings-advanced"].firstMatch
        for _ in 0..<12 where !(row.exists && row.isHittable) { list.swipeUp() }
        guard row.exists else { return false }
        row.tap()
        return descendants(matching: .any)["settings-advanced-list"].firstMatch.waitForExistence(timeout: timeout)
    }

    /// Scrolls Settings › Advanced until the row `id` can be tapped, and returns it.
    @MainActor func settingsAdvancedRow(_ id: String) -> XCUIElement {
        let list = descendants(matching: .any)["settings-advanced-list"].firstMatch
        let row = descendants(matching: .any)[id].firstMatch
        for _ in 0..<12 where !(row.exists && row.isHittable) { list.swipeUp() }
        return row
    }
}
