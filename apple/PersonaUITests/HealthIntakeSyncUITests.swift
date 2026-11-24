import XCTest

/// Uses only an isolated local mock and this suite's disposable iPhone simulator.
final class HealthIntakeSyncUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    @MainActor private func launch(settings: Bool = true, status: String? = nil, scheme: String = "light", demo: Bool = false) throws -> XCUIApplication {
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-tab", settings ? "settings" : "assistant", "-ui.scheme", scheme,
                               "-showHealthActivityExample", "unavailable", "-showHealthHomeTrendsExample", "empty"]
        if demo {
            app.launchArguments += ["-demo.on", "YES"]
        } else {
            let hub = try XCTUnwrap(env["PERSONA_HEALTH_HUB"])
            let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
            app.launchArguments += ["-pairURL", pair, "-demo.on", "NO", "-dev.openChat", ""]
        }
        if settings { app.launchArguments += ["-openWorkoutReports", "YES"] }
        else { app.launchArguments += ["-openShellPage", "health"] }
        if let status { app.launchArguments += ["-showIntakeSyncStatus", status] }
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["Allow", "允许", "OK", "好"] where springboard.buttons[label].waitForExistence(timeout: 0.5) {
            springboard.buttons[label].tap()
        }
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let capture = app.screenshot()
        let attachment = XCTAttachment(screenshot: capture)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] {
            let root = URL(fileURLWithPath: dir)
            try capture.pngRepresentation.write(to: root.appending(path: name + ".png"))
            try app.debugDescription.write(to: root.appending(path: name + ".ax.txt"), atomically: true, encoding: .utf8)
        }
    }

    @MainActor func testSettingsStatusPreviews() throws {
        for (state, scheme) in [("synced", "light"), ("synced", "dark"), ("denied", "light"), ("failed", "dark")] {
            let app = try launch(status: state, scheme: scheme)
            XCTAssertTrue(app.staticTexts["health-intake-sync-status"].waitForExistence(timeout: 20), app.debugDescription)
            XCTAssertTrue(app.switches["health-intake-sync-toggle"].exists)
            let expected = state == "synced" ? "已同步" : state == "denied" ? "没有允许" : "上次写入失败"
            XCTAssertTrue(app.staticTexts["health-intake-sync-status"].label.contains(expected))
            try shot(app, "sync-settings-\(state)-\(scheme)")
            app.terminate()
        }
    }

    @MainActor func testDemoHasNeitherTheSwitchNorTheHomeLink() throws {
        var app = try launch(demo: true)
        XCTAssertTrue(app.staticTexts["运动后自动生成报告"].waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertFalse(app.switches["health-intake-sync-toggle"].exists)
        app.terminate()
        app = try launch(settings: false, demo: true)
        XCTAssertTrue(app.descendants(matching: .any)["health-today"].waitForExistence(timeout: 15), app.debugDescription)
        XCTAssertFalse(app.buttons["health-intake-sync-link"].exists)
        app.terminate()
    }

    @MainActor func testOptInBackfillsAndHidesTheHomeLink() throws {
        var app = try launch()
        let existing = app.switches["health-intake-sync-toggle"]
        XCTAssertTrue(existing.waitForExistence(timeout: 20), app.debugDescription)
        if existing.value as? String == "1" {
            existing.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
            let off = XCTNSPredicateExpectation(predicate: NSPredicate(format: "value == %@", "0"), object: existing)
            XCTAssertEqual(XCTWaiter.wait(for: [off], timeout: 10), .completed)
        }
        app.terminate()
        app = try launch(settings: false)
        let link = app.buttons["health-intake-sync-link"]
        XCTAssertTrue(link.waitForExistence(timeout: 20), app.debugDescription)
        for scheme in ["light", "dark"] {
            if scheme == "dark" { app.terminate(); app = try launch(settings: false, scheme: scheme) }
            let row = app.buttons["health-intake-sync-link"]
            if !row.isHittable { app.swipeUp() }
            try shot(app, "sync-home-off-\(scheme)")
        }
        app.buttons["health-intake-sync-link"].tap()
        let toggle = app.switches["health-intake-sync-toggle"]
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), app.debugDescription)
        toggle.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)).tap()
        Thread.sleep(forTimeInterval: 2)
        if let dir = env["PERSONA_SHOTS"] {
            try app.debugDescription.write(to: URL(fileURLWithPath: dir).appending(path: "sync-authorization.ax.txt"), atomically: true, encoding: .utf8)
        }
        let water = app.switches["UIA.Health.Write.Water.SwitchCell.Switch"]
        if water.waitForExistence(timeout: 3) {
            // System identifiers, read from the actual authorization sheet, work in either interface language.
            for kind in ["Protein", "Water"] {
                let permission = app.switches["UIA.Health.Write.\(kind).SwitchCell.Switch"]
                if permission.value as? String != "1" { permission.tap() }
            }
            let allow = app.buttons["UIA.Health.Allow.Button"]
            if !allow.isHittable { app.tables.firstMatch.swipeUp() }
            XCTAssertTrue(allow.isEnabled)
            allow.tap()
        }
        let status = app.staticTexts["health-intake-sync-status"]
        XCTAssertTrue(status.waitForExistence(timeout: 15), app.debugDescription)
        let synced = XCTNSPredicateExpectation(predicate: NSPredicate(format: "label CONTAINS %@", "已同步"), object: status)
        XCTAssertEqual(XCTWaiter.wait(for: [synced], timeout: 15), .completed, app.debugDescription)
        try shot(app, "sync-settings-live")
        app.terminate()
        app = try launch(settings: false)
        XCTAssertTrue(app.descendants(matching: .any)["health-today"].waitForExistence(timeout: 15))
        XCTAssertFalse(app.buttons["health-intake-sync-link"].exists)
        app.terminate()
    }
}
