import XCTest

/// A pairing link opened from outside the app asks "Connect to <host>?" before it pairs (#9929): Cancel leaves the app
/// unpaired on its welcome page, Connect pairs. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB); light and dark are
/// attached, and kept in TEST_RUNNER_PERSONA_SHOTS=<dir> when set.
final class PairLinkConfirmUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testPairingLinkAsksBeforePairing() throws {
        let env = ProcessInfo.processInfo.environment
        guard let hub = env["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        let link = URL(string: "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock")!
        let host = URL(string: hub)!.host()!
        for scheme in ["light", "dark"] {
            let app = XCUIApplication()
            app.launchArguments = ["-openWelcome", "YES", "-pairURL", "", "-ui.scheme", scheme]
            app.launchPinned()
            let welcome = app.buttons["welcome.pair"]
            XCTAssertTrue(welcome.waitForExistence(timeout: 10), "\(scheme): no welcome page")
            dismissIconAlert()

            app.open(link)
            let alert = app.alerts.firstMatch
            XCTAssertTrue(alert.waitForExistence(timeout: 10), "\(scheme): the link paired without asking\n\(app.debugDescription)")
            XCTAssertTrue(alert.label.contains(host), "\(scheme): the question does not name the hub: \(alert.label)")
            shot(app, "pair-confirm-\(scheme)", env)
            alert.buttons["取消"].tap()
            gone(alert, "\(scheme): Cancel did not close the question")
            sleep(2)
            XCTAssertTrue(welcome.exists, "\(scheme): Cancel paired anyway")
            XCTAssertFalse(app.buttons["engine.save"].exists, "\(scheme): Cancel paired anyway (the engine step)")

            app.open(link)
            XCTAssertTrue(alert.waitForExistence(timeout: 10), "\(scheme): the second link did not ask")
            alert.buttons["连接"].tap()
            // Paired: the hub's first step (choosing its engine) or the app itself, no welcome page.
            let paired = NSPredicate { _, _ in
                app.buttons["engine.save"].exists || app.buttons["shell-assistant"].exists || !welcome.exists
            }
            expectation(for: paired, evaluatedWith: NSNull())
            waitForExpectations(timeout: 20)
            app.terminate()
        }
    }

    /// 「你已更改“Persona”的图标」 (the icon follows Light & Dark) is SpringBoard's, over the app and the question.
    @MainActor private func dismissIconAlert() {
        let alert = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch
        guard alert.waitForExistence(timeout: 2) else { return }
        for label in ["好", "OK"] where alert.buttons[label].exists { alert.buttons[label].tap(); break }
        _ = alert.waitForNonExistence(timeout: 3)
    }

    @MainActor private func gone(_ element: XCUIElement, _ message: String) {
        expectation(for: NSPredicate(format: "exists == false"), evaluatedWith: element)
        waitForExpectations(timeout: 5)
        XCTAssertFalse(element.exists, message)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ file: String, _ env: [String: String]) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = file
        a.lifetime = .keepAlways
        add(a)
        if let dir = env["PERSONA_SHOTS"] {
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(file + ".png"))
        }
    }
}
