import XCTest

/// The first screen without commands and the checklist after pairing (#4195, docs/assistant/onboarding.md 4.5). The checklist
/// tests run against scripts/mock/mock_hub.py with MOCK_READINESS=<scenario> (PERSONA_MOCK_HUB; PERSONA_READINESS names
/// it for the screenshots). PERSONA_SHOTS=<dir> writes the screenshots (PERSONA_SHOTS_APPEARANCE in their names).
final class OnboardingFirstScreenUITests: XCTestCase {
    private var scenario: String { ProcessInfo.processInfo.environment["PERSONA_READINESS"] ?? "scenario" }

    /// The notification question and the icon's are SpringBoard's (as AddComputerTests).
    private func dismissSystemAlerts(wait: TimeInterval = 1) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        var deadline = Date.now.addingTimeInterval(wait)
        while Date.now < deadline {
            let alert = springboard.alerts.firstMatch
            if alert.waitForExistence(timeout: 0.5) {
                for label in ["允许", "Allow", "好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); break }
                deadline = Date.now.addingTimeInterval(1.5)
            }
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        dismissSystemAlerts()
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "\(name)-\(look).png"))
        }
    }

    /// Not paired: no command on the first screen; Other ways has the rest; a refused code says so in words.
    @MainActor func testTheFirstScreenHasNoCommands() throws {
        let app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES"]
        app.launchPinned()
        let pair = app.buttons["welcome.pair"]
        XCTAssertTrue(pair.waitForExistence(timeout: 20), "no first screen:\n\(app.debugDescription)")
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS 'personad'")).firstMatch.exists, "a command on the first screen")
        XCTAssertTrue(app.buttons["welcome.other"].exists && app.buttons["welcome.demo"].exists)
        sleep(1)
        shot(app, "welcome")

        app.buttons["welcome.help"].tap()
        XCTAssertTrue(app.alerts.firstMatch.waitForExistence(timeout: 5))
        app.alerts.firstMatch.buttons.matching(identifier: "welcome.help.done").firstMatch.tap()
        XCTAssertTrue(pair.isHittable)

        app.buttons["welcome.other"].tap()
        let address = app.textFields["服务器地址"]
        XCTAssertTrue(address.waitForExistence(timeout: 10), "其他方式 has no address field:\n\(app.debugDescription)")
        sleep(1)
        shot(app, "welcome-other")

        // A code that is not good any more: the hub answers 401 invalid_token (PERSONA_MOCK_HUB runs MOCK_PAIR_TOKEN).
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        address.tap()
        address.typeText(hub)
        let code = app.secureTextFields.firstMatch.exists ? app.secureTextFields.firstMatch : app.textFields["pair.token"]
        code.tap()
        code.typeText("jp_expired_code")
        app.buttons["连接"].tap()
        let expired = app.staticTexts["这个码过期了或已经用过了，再出一个"]
        XCTAssertTrue(expired.waitForExistence(timeout: 15), "the expired code is not said in words:\n\(app.debugDescription)")
        XCTAssertFalse(app.staticTexts.containing(NSPredicate(format: "label CONTAINS[c] 'invalid token'")).firstMatch.exists)
        sleep(1)
        shot(app, "welcome-expired")
    }

    @MainActor func testScanOpensQRPairing() {
        let app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES"]
        app.launchPinned()
        let pair = app.buttons["welcome.pair"]
        XCTAssertTrue(pair.waitForExistence(timeout: 20))
        pair.tap()
        XCTAssertTrue(app.buttons["pair.choosePhoto"].waitForExistence(timeout: 10))
    }

    /// Just paired with a hub in MOCK_READINESS=<scenario>: the checklist comes up in place of the app.
    @MainActor func testTheChecklistAfterPairing() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs PERSONA_MOCK_HUB") }
        let app = XCUIApplication()
        let link = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-openWelcome", "YES", "-openPairLink", link]
        app.launchPinned()
        let list = app.descendants(matching: .any)["onboarding-checklist"].firstMatch
        XCTAssertTrue(list.waitForExistence(timeout: 25), "no checklist after pairing:\n\(app.debugDescription)")
        sleep(2)
        shot(app, "checklist-\(scenario)")
        guard scenario != "full" else {
            XCTAssertTrue(app.buttons["checklist.done"].exists, "a ready hub's checklist has no 开始使用")
            return
        }
        // Later: the app, and Settings says how many steps are left.
        app.buttons["checklist.later"].tap()
        XCTAssertFalse(list.waitForExistence(timeout: 3) && list.isHittable, "the checklist stayed after 以后再说")
        if scenario == "zero" {
            app.terminate()
            app.launchArguments = ["-tab", "settings"]
            app.launchPinned()
            let row = app.buttons["settings.checklist"].firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 20), "no 还有 N 步 in 设置:\n\(app.debugDescription)")
            sleep(1)
            shot(app, "settings-steps-left")
        }
    }
}
