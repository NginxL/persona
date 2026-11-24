import XCTest
import UIKit

/// The real intake HTTP client, backed by the repository mock's pending extension and two imported drinks.
final class HealthWaterMigrationUITests: XCTestCase {
    override func setUpWithError() throws {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = .portrait
    }

    @MainActor func testImportFromTheHealthHome() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs the repository mock hub") }
        let scheme = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-demo.on", "NO", "-dev.openChat", "",
                               "-app.tab", "assistant", "-app.navigation", "sidebar", "-newAssistant", "YES", "-ui.scheme", scheme]
        app.launchPinned()
        defer { app.terminate() }
        // Enter through the same sidebar action as the person, after pairing/restoring the app has settled.
        let health = app.buttons["sidebar-health"].firstMatch
        if !health.exists || !health.isHittable {
            let sidebar = app.buttons["assistant-sidebar-button"].firstMatch
            XCTAssertTrue(sidebar.waitForExistence(timeout: 20), app.debugDescription)
            sidebar.tap()
        }
        XCTAssertTrue(health.waitForExistence(timeout: 20), app.debugDescription)
        health.tap()
        let card = app.descendants(matching: .any)["health-water-migration"].firstMatch
        XCTAssertTrue(card.waitForExistence(timeout: 20), app.debugDescription)
        let button = app.buttons["health-water-import"].firstMatch
        XCTAssertTrue(button.isHittable)
        XCTAssertTrue(button.isEnabled)
        XCTAssertFalse(button.label.contains("\n"))
        let water = app.buttons["health-water-ring"].firstMatch
        XCTAssertTrue(water.waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertTrue((water.value as? String)?.contains("750") == true)
        XCTAssertLessThan(card.frame.minY, water.frame.minY, "migration appears above Today")
        try shot(app, "pending")
        button.tap()
        let toast = app.descendants(matching: .any)["toast"].firstMatch
        XCTAssertTrue(toast.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(toast.label.contains("已搬进 2 条，喝水扩展已停用"), toast.label)
        XCTAssertTrue(toast.label.contains("小组件、表盘和快捷指令"), toast.label)
        if scheme == "light", UIDevice.current.userInterfaceIdiom == .phone { try shot(app, "toast") }
        expectation(for: NSPredicate { _, _ in !card.exists }, evaluatedWith: nil)
        expectation(for: NSPredicate { _, _ in
            (water.value as? String)?.replacingOccurrences(of: ",", with: "").contains("1200") == true
        }, evaluatedWith: nil)
        waitForExpectations(timeout: 15)
        if scheme == "light", UIDevice.current.userInterfaceIdiom == .phone {
            expectation(for: NSPredicate { _, _ in !toast.exists }, evaluatedWith: nil)
            waitForExpectations(timeout: 10)
            try shot(app, "imported")
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ state: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
        // Only the pending iPad and dark screenshots are needed for review.
        if state != "pending", device == "ipad" { return }
        let scheme = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let name = "water-migration-\(device)-\(scheme)-\(state)"
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        try app.debugDescription.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".ax.txt"), atomically: true, encoding: .utf8)
    }
}
