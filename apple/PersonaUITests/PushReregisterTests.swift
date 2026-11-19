import XCTest

/// A push registration the hub retired (#9611) is registered again (#9612), against scripts/mock/mock_hub.py
/// (TEST_RUNNER_PERSONA_PUSH_HUB=http://127.0.0.1:<port>) on a stock simulator (it answers the notification question):
/// the app registers for pushes, the mock retires that registration (`GET /mock/devices/retire`), and the next
/// Settings › Notifications write, answered 409 `push_registration_required`, makes the app register again (one more
/// `POST /api/v1/devices` in `GET /mock/devices`) and then goes through. A simulator that gives no APNs token skips.
final class PushReregisterTests: XCTestCase {
    private func hub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_PUSH_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_PUSH_HUB)")
        }
        return hub
    }

    private func json(_ url: String) -> [String: Any]? {
        var req = URLRequest(url: URL(string: url)!)
        req.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var out: [String: Any]?
        let done = DispatchSemaphore(value: 0)
        URLSession.shared.dataTask(with: req) { data, _, _ in
            out = data.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 10)
        return out
    }

    private func registrations(_ hub: String) -> Int {
        (json(hub + "/mock/devices")?["devices"] as? [Any])?.count ?? 0
    }

    /// Waits for at least `count` registrations; false when they didn't come in time.
    private func waitRegistrations(_ hub: String, atLeast count: Int, timeout: TimeInterval = 30) -> Bool {
        let deadline = Date.now.addingTimeInterval(timeout)
        while Date.now < deadline {
            if registrations(hub) >= count { return true }
            usleep(500_000)
        }
        return false
    }

    @MainActor func testARetiredRegistrationIsRegisteredAgain() throws {
        let hub = try hub()
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"]
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        app.terminate()

        // Paired now: without `-pairURL` (which keeps DEBUG runs from registering) the app registers for pushes.
        app.launchArguments = ["-tab", "settings", "-openNotificationSettings", "YES"]
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        guard waitRegistrations(hub, atLeast: 1) else {
            throw XCTSkip("this simulator gave the app no APNs token: no push registration to retire")
        }
        sleep(2) // the launch's registration has settled
        let before = registrations(hub)
        XCTAssertEqual(json(hub + "/mock/devices/retire?on=1")?["retired"] as? Bool, true)

        let quiet = app.switches["notify.quiet"]
        for _ in 0..<6 where !(quiet.exists && quiet.isHittable) { app.swipeUp() }
        XCTAssertTrue(quiet.waitForExistence(timeout: 10), "no Quiet Hours switch:\n\(app.debugDescription)")
        let wasOn = quiet.value as? String == "1"
        quiet.switches.firstMatch.exists ? quiet.switches.firstMatch.tap() : quiet.tap()

        XCTAssertTrue(waitRegistrations(hub, atLeast: before + 1, timeout: 20), "the 409 started no registration")
        XCTAssertEqual(json(hub + "/mock/devices/retire?on=0")?["retired"] as? Bool, false)
        // The write went through after the registration: the hub has the switch's new state.
        let deadline = Date.now.addingTimeInterval(10)
        var enabled: Bool?
        while Date.now < deadline {
            enabled = (json(hub + "/api/v1/devices/check/prefs")?["quiet"] as? [String: Any])?["enabled"] as? Bool
            if enabled == !wasOn { break }
            usleep(500_000)
        }
        XCTAssertEqual(enabled, !wasOn, "the write was not tried again after registering")
        XCTAssertEqual(registrations(hub), before + 1, "registered more than once")
    }
}
