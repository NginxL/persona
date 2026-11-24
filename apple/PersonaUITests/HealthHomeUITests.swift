import XCTest
import UIKit

/// Intake comes from the local mock's real HTTP routes; only Apple Health readings are synthetic.
final class HealthHomeUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    @MainActor private func launch(scheme: String = "light", largest: Bool = false,
                                   activity: String = "full", bottom: Bool = false, trends: String = "sample") throws -> XCUIApplication {
        guard let hub = env["PERSONA_HEALTH_HUB"] else { throw XCTSkip("needs PERSONA_HEALTH_HUB pointing to a local mock") }
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-demo.on", "NO", "-dev.openChat", "",
                               "-openShellPage", "health", "-ui.scheme", scheme,
                               "-showHealthActivityExample", activity,
                               "-showHealthHomeTrendsExample", trends, "-showHealthBottom", bottom ? "YES" : "NO",
                               "-UIPreferredContentSizeCategoryName", largest ? "UICTContentSizeCategoryAccessibilityXXXL" : "UICTContentSizeCategoryL"]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["Allow", "允许", "OK", "好"] where springboard.buttons[label].waitForExistence(timeout: 1) {
            springboard.buttons[label].tap()
        }
        let ready = bottom ? "health-privacy" : "health-today"
        XCTAssertTrue(app.descendants(matching: .any)[ready].waitForExistence(timeout: 20), app.debugDescription)
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        // The first UIKit ring layout can finish after the accessibility element first appears.
        Thread.sleep(forTimeInterval: 1)
        let tree = app.debugDescription
        let capture = app.screenshot()
        let attachment = XCTAttachment(screenshot: capture)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] {
            try capture.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: name + ".png"))
            try tree.write(to: URL(fileURLWithPath: dir).appending(path: name + ".ax.txt"), atomically: true, encoding: .utf8)
        }
    }

    @MainActor func testHomeLightAndDarkAtDefaultAndLargestSizes() throws {
        for scheme in ["light", "dark"] { for largest in [false, true] {
            let size = largest ? "largest" : "default"
            var app = try launch(scheme: scheme, largest: largest)
            XCTAssertTrue(app.descendants(matching: .any)["health-today"].exists)
            XCTAssertTrue(app.buttons["health-water-ring"].exists)
            XCTAssertTrue(app.buttons["health-protein-ring"].exists)
            XCTAssertFalse(app.buttons["health-activity-rings"].exists)
            try shot(app, "health-top-\(scheme)-\(size)")
            app.terminate()
            app = try launch(scheme: scheme, largest: largest, bottom: true)
            try shot(app, "health-bottom-\(scheme)-\(size)")
            app.terminate()
        } }
    }

    @MainActor func testLoggingAGlassChangesTheHubTotalAndUndoRestoresIt() throws {
        let app = try launch()
        let ring = app.buttons["health-water-ring"]
        let before = ring.value as? String
        let first = app.buttons["health-log-water"], second = app.buttons["health-log-water-amount"]
        XCTAssertEqual(first.frame.minY, second.frame.minY, accuracy: 1, "both water actions share a row at default size")
        XCTAssertEqual(first.frame.height, second.frame.height, accuracy: 1)
        first.tap()
        let changed = NSPredicate { _, _ in (ring.value as? String) != before }
        expectation(for: changed, evaluatedWith: nil)
        waitForExpectations(timeout: 10)
        let undo = app.buttons["toast-action"]
        XCTAssertTrue(undo.waitForExistence(timeout: 10))
        undo.tap()
        let restored = NSPredicate { _, _ in (ring.value as? String) == before }
        expectation(for: restored, evaluatedWith: nil)
        waitForExpectations(timeout: 10)
        app.terminate()
    }

    /// The app caps interface text in its window; the system's accessibility category still decides the layout.
    @MainActor func testAccessibilityWaterActionsUseFullWidth() throws {
        for scheme in ["light", "dark"] {
            let app = try launch(scheme: scheme, largest: true)
            let first = app.buttons["health-log-water"], second = app.buttons["health-log-water-amount"]
            XCTAssertGreaterThan(second.frame.minY, first.frame.maxY, "accessibility sizes stack the two water actions")
            XCTAssertEqual(first.frame.width, second.frame.width, accuracy: 1)
            XCTAssertFalse(first.label.contains("\n"))
            XCTAssertFalse(second.label.contains("\n"))
            try shot(app, "health-top-\(scheme)-largest")
            app.terminate()
        }
    }

    @MainActor func testOldHubShowsUpdateHint() throws {
        guard let hub = env["PERSONA_HEALTH_OLD_HUB"] else { throw XCTSkip("needs a local mock without health_intake") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-demo.on", "NO", "-dev.openChat", "",
                               "-openShellPage", "health", "-ui.scheme", "light"]
        app.launchPinned()
        XCTAssertTrue(app.staticTexts["health-update-hub"].waitForExistence(timeout: 20), app.debugDescription)
        XCTAssertFalse(app.descendants(matching: .any)["health-today"].exists)
        try shot(app, "health-update-hub")
        app.terminate()
    }

    @MainActor func testOtherWaterAndProteinAmountsUseTheEnteredUnits() throws {
        let app = try launch()
        for (button, ringID, text, unit) in [
            ("health-log-water-amount", "health-water-ring", "125.5", "毫升"),
            ("health-log-protein", "health-protein-ring", "12.3", "克"),
        ] {
            let ring = app.buttons[ringID]
            let before = ring.value as? String
            app.buttons[button].tap()
            let alert = app.alerts.firstMatch
            XCTAssertTrue(alert.waitForExistence(timeout: 5))
            XCTAssertTrue(alert.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", unit)).firstMatch.exists)
            let field = alert.textFields.firstMatch
            XCTAssertTrue(field.exists)
            field.tap()
            field.typeText(text)
            if button == "health-log-water-amount" { try shot(app, "health-water-amount") }
            let submit = alert.buttons["记录"]
            XCTAssertTrue(submit.isEnabled)
            submit.tap()
            expectation(for: NSPredicate { _, _ in (ring.value as? String) != before }, evaluatedWith: nil)
            waitForExpectations(timeout: 10)
            let undo = app.buttons["toast-action"]
            XCTAssertTrue(undo.waitForExistence(timeout: 10))
            undo.tap()
            expectation(for: NSPredicate { _, _ in (ring.value as? String) == before }, evaluatedWith: nil)
            waitForExpectations(timeout: 10)
        }
        app.terminate()
    }

    @MainActor func testIPadShowsTodayAndTrendsWithoutActivity() throws {
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad-only acceptance") }
        let app = try launch()
        XCTAssertTrue(app.descendants(matching: .any)["health-today"].exists)
        XCTAssertFalse(app.descendants(matching: .any)["health-activity-rings"].exists)
        XCTAssertTrue(app.descendants(matching: .any)["health-trends"].exists)
        try shot(app, "health-ipad")
        app.terminate()
    }

    @MainActor func testIntakeRingsOpenTheManagerDrawnDetails() throws {
        for scheme in ["light", "dark"] {
            for (kind, title) in [("water", "喝水"), ("protein", "蛋白质")] {
                let app = try launch(scheme: scheme)
                let ring = app.buttons["health-\(kind)-ring"]
                XCTAssertTrue(ring.isHittable, app.debugDescription)
                XCTAssertFalse(ring.label.isEmpty)
                ring.tap()
                XCTAssertTrue(app.descendants(matching: .any)["extension-ui-ready"].waitForExistence(timeout: 20), app.debugDescription)
                XCTAssertTrue(app.navigationBars[title].waitForExistence(timeout: 5), app.debugDescription)
                XCTAssertTrue(app.staticTexts["今天的记录"].exists)
                XCTAssertTrue(app.buttons["sheet-close"].isHittable)
                try shot(app, "health-detail-\(kind)-\(scheme)")
                app.buttons["sheet-close"].tap()
                XCTAssertTrue(app.buttons["health-\(kind)-ring"].waitForExistence(timeout: 5))
                app.terminate()
            }
        }
    }

    @MainActor func testActivityPermissionUnavailableMoveAndThreeRingStates() throws {
        for state in ["allow", "unreadable", "move", "full"] {
            let app = try launch(activity: state)
            let expected = state == "allow" ? "health-activity-allow" : state == "unreadable" ? "health-activity-unreadable" : "health-activity-rings"
            let element = app.descendants(matching: .any)[expected].firstMatch
            for _ in 0..<4 where !element.isHittable { app.swipeUp() }
            XCTAssertTrue(element.waitForExistence(timeout: 10), app.debugDescription)
            if state == "move" || state == "full" {
                XCTAssertFalse(app.buttons["health-activity-rings"].exists, "the ring tile has no action")
                let value = element.value as? String ?? ""
                XCTAssertTrue(value.contains("320") && value.contains("500"), value)
                XCTAssertEqual(value.contains("25"), state == "full", "only existing goal rows are accessible")
            } else if state == "allow" {
                XCTAssertGreaterThanOrEqual(element.frame.height, 44)
            }
            try shot(app, "health-activity-\(state)")
            app.terminate()
        }
    }

    @MainActor func testEmptyTrendsLeaveNoHomeSection() throws {
        let app = try launch(trends: "empty")
        XCTAssertFalse(app.descendants(matching: .any)["health-trends"].exists)
        app.terminate()
    }

    @MainActor func testHomeLoadsTrendsBeforeTheirConditionalRowExists() throws {
        let app = try launch()
        let trends = app.descendants(matching: .any)["health-trends"]
        for _ in 0..<5 where !trends.isHittable { app.swipeUp() }
        XCTAssertTrue(trends.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any)["health-trend-steps"].exists)
        try shot(app, "health-trends-home")
        app.terminate()
    }

    @MainActor func testLargestActivityKeepsTheValuesBelowTheRings() throws {
        let app = try launch(largest: true)
        let rings = app.otherElements["health-activity-rings"]
        app.swipeUp()
        XCTAssertTrue(rings.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertEqual(app.otherElements.matching(identifier: "health-activity-rings").count, 1)
        XCTAssertFalse(app.buttons["health-activity-rings"].exists)
        try shot(app, "health-activity-largest")
        app.terminate()
    }
}
