import XCTest

/// The Fleet activity's Deny button on the lock screen resolves the request: the mock hub serves its permission-only
/// Fleet, the test locks the phone, taps Deny and asks the mock hub (scripts/mock/mock_hub.py, PERSONA_MOCK_HUB)
/// whether the resolve for the request the activity showed arrived.
final class LiveActivityActionTests: XCTestCase {
    @MainActor func testDenyFromTheLockScreen() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let data = try Data(contentsOf: URL(string: hub + "/mock/fleet?mode=permission")!)
        let reply = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        let fleet = reply?["fleet"] as? [String: Any]
        let items = fleet?["items"] as? [[String: Any]] ?? []
        // The Lock Screen acts on the first permission or plan. Permission mode has no plans, so this is the request
        // whose Deny the test taps; on a rerun against the same hub it is the next still-pending permission.
        let displayed = try XCTUnwrap(items.first {
            $0["kind"] as? String == "permission" && $0["interaction"] is String
        }, "the mock has no permission request for the Fleet activity")
        let requestID = try XCTUnwrap(displayed["interaction"] as? String)
        addTeardownBlock {
            // Restore the plain mock's default even when launch or an assertion fails; later classes share the hub.
            _ = try? Data(contentsOf: URL(string: hub + "/mock/fleet?mode=off")!)
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"]
        app.launchPinned()
        clearLaunchAlerts(springboard)
        sleep(4)
        XCUIDevice.shared.press(.home)
        sleep(1)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        // Unlocked and on the home screen again whatever happens next (#9692): a failure below (an assertion, a tap that
        // finds nothing) ends the test without running a `defer`, and a locked simulator fails every UI test after it.
        // Teardown blocks run after a failure too.
        addTeardownBlock { @MainActor [self] in unlock(springboard) }
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        let activity = springboard.descendants(matching: .any)["activity-content-view"].firstMatch
        let deny = activity.buttons.matching(NSPredicate(format: "label IN %@", ["拒绝", "Deny", "拒否"])).firstMatch
        // The system's Live Activities prompt can arrive with the activity already visible, and its English Allow must
        // not be confused with the activity's Allow. Its buttons belong to the system's swipe-action cell; the activity's
        // buttons are inside activity-content-view. Accept the first-run Allow and a later Always Allow, never the request.
        let systemActions = springboard.buttons.matching(NSPredicate(format: "identifier == %@ AND label IN %@",
                                                                      "swipe-action-button-identifier",
                                                                      ["Allow", "Always Allow", "允许", "始终允许"])).firstMatch
        for _ in 0..<2 {
            guard systemActions.waitForExistence(timeout: 3) else { break }
            systemActions.tap()
            sleep(1)
        }

        XCTAssertTrue(deny.waitForExistence(timeout: 10), "no Deny on the lock screen activity:\n\(springboard.debugDescription)")
        XCTAssertTrue(activity.buttons.matching(NSPredicate(format: "label IN %@", ["允许", "Allow", "許可"])).firstMatch.exists)
        let shot = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        shot.name = "lock-actions"
        shot.lifetime = .keepAlways
        add(shot)
        deny.tap()

        let deadline = Date.now.addingTimeInterval(15)
        var last: [String: Any]?
        while Date.now < deadline, last == nil {
            if let data = try? Data(contentsOf: URL(string: hub + "/mock/resolved")!),
               let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
               let list = obj["resolved"] as? [[String: Any]] {
                last = list.last { $0["id"] as? String == requestID }
            }
            if last == nil { usleep(300_000) }
        }
        let got = try XCTUnwrap(last, "the hub never got the resolve for \(requestID)")
        XCTAssertEqual(got["id"] as? String, requestID)
        XCTAssertEqual((got["resolution"] as? [String: Any])?["allow"] as? Bool, false)
    }

    /// A new simulator's launch alerts: allow notifications and close the icon-change alert. Either can consume the Home
    /// press before the activity starts, and on a consecutive run neither is there.
    @MainActor private func clearLaunchAlerts(_ springboard: XCUIApplication) {
        let alert = springboard.alerts.firstMatch
        for _ in 0..<2 {
            guard alert.waitForExistence(timeout: 3) else { return }
            let allow = alert.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
            if allow.exists { allow.tap() } else { alert.buttons.firstMatch.tap() }
            sleep(1)
        }
    }

    /// Wakes the phone, unlocks it (a swipe up: no passcode on the simulator) and goes to the home screen. Harmless when it
    /// is unlocked already: the swipe up from the bottom edge goes home too.
    @MainActor private func unlock(_ springboard: XCUIApplication) {
        XCUIDevice.shared.press(.home)
        sleep(1)
        springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
            .press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(1)
        XCUIDevice.shared.press(.home)
    }
}
