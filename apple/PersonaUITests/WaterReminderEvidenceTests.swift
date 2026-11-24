import XCTest

/// System-surface evidence using synthetic simctl pushes from a task-owned loopback fixture.
final class WaterReminderEvidenceTests: XCTestCase {
    private var environment: [String: String] { ProcessInfo.processInfo.environment }

    @MainActor private func shot(_ name: String) throws {
        guard let folder = environment["PERSONA_SHOTS"] else { return }
        try XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: folder).appending(path: name + ".png"))
    }

    @MainActor func testExpandWaterReminderAndItsAmountField() async throws {
        guard let fixture = environment["PERSONA_REMINDER_FIXTURE"], let url = URL(string: fixture + "/push") else {
            throw XCTSkip("Requires a synthetic simctl push fixture")
        }
        continueAfterFailure = false
        let app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-showPushKey", "YES"]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 8) { allow.tap() }
        XCUIDevice.shared.press(.home)
        let (_, response) = try await URLSession.shared.data(from: url)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
        let banner = springboard.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Water reminder sample")).firstMatch
        XCTAssertTrue(banner.waitForExistence(timeout: 15), springboard.debugDescription)
        banner.press(forDuration: 1.5)
        let drink = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Drank a Glass", "喝了一杯"])).firstMatch
        let later = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Later", "等会儿"])).firstMatch
        let log = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Log Another Amount", "记别的量"])).firstMatch
        XCTAssertTrue(drink.waitForExistence(timeout: 8), springboard.debugDescription)
        XCTAssertTrue(later.exists)
        XCTAssertTrue(log.exists)
        try shot("water-reminder-buttons")
        log.tap()
        let field = springboard.textFields.firstMatch
        let editor = field.exists ? field : springboard.textViews.element(boundBy: springboard.textViews.count - 1)
        XCTAssertTrue(editor.waitForExistence(timeout: 8), springboard.debugDescription)
        try shot("water-reminder-amount")
        XCUIDevice.shared.press(.home)
    }

    @MainActor func testLogWaterAppearsInShortcuts() throws {
        guard environment["PERSONA_REMINDER_FIXTURE"] != nil else { throw XCTSkip("System-surface evidence run only") }
        continueAfterFailure = false
        let shortcuts = XCUIApplication(bundleIdentifier: "com.apple.shortcuts")
        shortcuts.launchPinned(navigation: false)
        let continueButton = shortcuts.buttons.matching(NSPredicate(format: "label IN %@", ["Continue", "继续"])).firstMatch
        if continueButton.waitForExistence(timeout: 5) { continueButton.tap() }
        print("SHORTCUTS_INITIAL\n" + shortcuts.debugDescription)
        let create = shortcuts.buttons.matching(NSPredicate(format: "label IN %@", ["Create Shortcut", "New Shortcut", "创建快捷指令", "新建快捷指令", "Add", "添加"])).firstMatch
        XCTAssertTrue(create.waitForExistence(timeout: 10), shortcuts.debugDescription)
        create.tap()
        print("SHORTCUTS_EDITOR\n" + shortcuts.debugDescription)
        let search = shortcuts.searchFields.firstMatch
        XCTAssertTrue(search.waitForExistence(timeout: 10), shortcuts.debugDescription)
        search.tap()
        search.typeText("Log Water")
        let water = shortcuts.descendants(matching: .any).matching(NSPredicate(format: "label IN %@", ["Log Water", "记一杯水", "水分を記録"])).firstMatch
        XCTAssertTrue(water.waitForExistence(timeout: 15), shortcuts.debugDescription)
        try shot("shortcuts-log-water")
    }

    @MainActor func testDrinkCallbackRunsInBackgroundAndDeduplicatesThePush() async throws {
        guard let fixture = environment["PERSONA_REMINDER_FIXTURE"] else { throw XCTSkip("Requires a synthetic reminder fixture") }
        continueAfterFailure = false
        _ = try await URLSession.shared.data(from: URL(string: fixture + "/reset")!)
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + fixture.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-demo.on", "NO", "-showPushKey", "YES"]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        XCUIDevice.shared.press(.home)
        for attempt in 0..<2 {
            _ = try await URLSession.shared.data(from: URL(string: fixture + "/push")!)
            let banner = springboard.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Water reminder sample")).firstMatch
            XCTAssertTrue(banner.waitForExistence(timeout: 15), springboard.debugDescription)
            banner.press(forDuration: 1.5)
            let drink = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Drank a Glass", "喝了一杯"])).firstMatch
            XCTAssertTrue(drink.waitForExistence(timeout: 8), springboard.debugDescription)
            if attempt == 0 { try shot("water-reminder-buttons-clean") }
            drink.tap()
            if attempt == 0 {
                let answer = springboard.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Sample water recorded")).firstMatch
                XCTAssertTrue(answer.waitForExistence(timeout: 15), springboard.debugDescription)
                answer.swipeUp()
            }
            XCTAssertEqual(app.state, .runningBackground)
        }
        let (data, _) = try await URLSession.shared.data(from: URL(string: fixture + "/writes")!)
        let object = try XCTUnwrap(try JSONSerialization.jsonObject(with: data) as? [String: Any])
        let writes = try XCTUnwrap(object["writes"] as? [[String: Any]])
        XCTAssertEqual(writes.count, 1)
        XCTAssertEqual(writes.first?["reminder"] as? String, "synthetic-reminder-a1")
        XCTAssertEqual(writes.first?["action"] as? String, "drink")
        XCTAssertEqual(writes.first?["space"] as? String, "health")
        XCTAssertNil(writes.first?["manager"])
    }
}
