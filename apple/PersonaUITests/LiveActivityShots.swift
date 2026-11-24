import XCTest

/// Screenshots of the Fleet Live Activity with made-up agents (`-showFleetSample`): Dynamic Island compact and expanded,
/// lock screen. Not a pass / fail test: look at the attachments (xcresulttool export attachments). Needs nothing else.
final class LiveActivityShots: XCTestCase {
    @MainActor func testShots() throws {
        let env = ProcessInfo.processInfo.environment
        guard let phase = env["PERSONA_FLEET_SAMPLE"] else {
            throw XCTSkip("set PERSONA_FLEET_SAMPLE=needsYou|working|background|done|thinking|offline|current")
        }
        if let id = env["PERSONA_ISLAND_PEER"] { endPeer(id) } // one left over from an earlier run
        if phase != "current" { // "current": photograph whatever the activity shows now (after a real push)
            let app = XCUIApplication()
            app.launchArguments = ["-showFleetSample", phase, "-fleetDetails", env["PERSONA_FLEET_DETAILS"] ?? "NO",
                                   "-fleetIsland", env["PERSONA_FLEET_ISLAND"] ?? "a", "-tab", "settings",
                                   "-showFleetCount", env["PERSONA_FLEET_COUNT"] ?? "0"]
            // The persona on the activity (`persona`, the default, or the hub's `nwverse`, which needs PERSONA_MOCK_HUB to be downloaded from).
            if let persona = env["PERSONA_PERSONA"] { app.launchArguments += ["-openPersona", persona] }
            if let pair = Self.pair { app.launchArguments += ["-pairURL", pair] }
            // PERSONA_UI_LANGUAGE=en|ja|zh-Hans: the activity in that interface language (docs/apple/ios.md「Interface language」).
            if let lang = env["PERSONA_UI_LANGUAGE"] { app.launchArguments += ["-AppleLanguages", "(\(lang))"] }
            app.launchPinned()
            sleep(env["PERSONA_MOCK_HUB"] == nil ? 4 : 8) // a package is downloaded first
        }
        XCUIDevice.shared.press(.home)
        sleep(2)
        shot("\(phase)-compact")
        usleep(400_000)
        shot("\(phase)-compact2") // compared with the first: does the working gear turn?
        sleep(3)
        shot("\(phase)-compact3") // settled
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        if let id = env["PERSONA_ISLAND_PEER"] { shared(with: id, phase) }

        // Long-press the island to expand it.
        springboard.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 201, dy: 37)).press(forDuration: 1.5)
        sleep(2)
        shot("\(phase)-expanded")
        // The long press does not always take: a second try, shown separately.
        XCUIDevice.shared.press(.home)
        sleep(1)
        springboard.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 201, dy: 37)).press(forDuration: 2)
        sleep(2)
        shot("\(phase)-expanded2")
        XCUIDevice.shared.press(.home)
        sleep(1)

        // Lock, then wake to the lock screen.
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        // First time only: the system asks whether Persona may show Live Activities (on the lock screen). Its Allow is
        // also the activity's own button: with the activity showing (Deny is there), do not tap it.
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        let ours = springboard.buttons.matching(NSPredicate(format: "label CONTAINS %@ OR label CONTAINS %@ OR label CONTAINS %@",
                                                              "拒绝", "Deny", "拒否")).firstMatch
        if !ours.exists, allow.exists {
            allow.tap()
            sleep(2)
        }
        // Later the system asks again whether to keep allowing them; Always Allow is never one of the activity's buttons.
        let always = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Always Allow", "始终允许"])).firstMatch
        if always.exists {
            always.tap()
            sleep(2)
        }
        shot("\(phase)-lock")
        // Unlock again (swipe up; no passcode on the simulator): a locked simulator hangs the next test run.
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(1)
    }

    /// A persona chosen in Settings › Appearance › Avatar while the activity runs reaches the island within seconds, without a new
    /// activity (docs/apple/live-activity.md「Avatar」). `PERSONA_PERSONA_SWITCH=nwverse,persona`: starts on the first,
    /// then picks each next one in Settings and photographs the island 3 s after leaving the app. A hub package needs
    /// PERSONA_MOCK_HUB (scripts/mock/mock_hub.py with MOCK_PERSONAS=builtin).
    @MainActor func testSwitchingPersonaRedrawsTheIsland() throws {
        let env = ProcessInfo.processInfo.environment
        guard let ids = env["PERSONA_PERSONA_SWITCH"]?.split(separator: ",").map(String.init), let first = ids.first else {
            throw XCTSkip("set PERSONA_PERSONA_SWITCH=<persona id>,<persona id>…")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-showFleetSample", env["PERSONA_FLEET_SAMPLE"] ?? "working", "-fleetIsland", "a",
                               "-tab", "settings", "-openAppearance", "persona", "-openPersona", first]
        if let pair = Self.pair { app.launchArguments += ["-pairURL", pair] }
        app.launchPinned()
        sleep(env["PERSONA_MOCK_HUB"] == nil ? 4 : 8)
        Self.allowNotifications()
        XCUIDevice.shared.press(.home)
        sleep(3)
        shot("switch-0-\(first)")
        for (i, id) in ids.dropFirst().enumerated() {
            app.activate()
            let card = app.buttons["persona-\(id)"]
            if !card.waitForExistence(timeout: 3) { // Settings keeps its page; open Avatar again if it did not
                let close = app.buttons["settings-close"].firstMatch
                if close.exists { close.tapCenter() }
                let drawer = app.buttons["assistant-sidebar-button"].firstMatch
                XCTAssertTrue(drawer.waitForExistence(timeout: 10), "no drawer button")
                drawer.tapCenter()
                let settings = app.drawerSettingsButton
                XCTAssertTrue(settings.waitForExistence(timeout: 5), "no Settings entry")
                settings.tapCenter()
                XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced:\n\(app.debugDescription)")
                let row = app.settingsAdvancedRow("settings-persona")
                XCTAssertTrue(row.waitForExistence(timeout: 10), "no 形象 row:\n\(app.debugDescription)")
                row.tap()
            }
            XCTAssertTrue(card.waitForExistence(timeout: 20), "\(id) is not offered:\n\(app.debugDescription)")
            card.tap()
            let selected = expectation(for: NSPredicate(format: "selected == true"), evaluatedWith: app.buttons["persona-\(id)"])
            XCTAssertEqual(XCTWaiter.wait(for: [selected], timeout: 20), .completed, "choosing \(id) did not take")
            XCUIDevice.shared.press(.home)
            sleep(3)
            shot("switch-\(i + 1)-\(id)")
        }
        // Leave the simulator on the default for the other tests.
        app.terminate()
        app.launchArguments = ["-openPersona", "persona", "-tab", "settings"]
        app.launchPinned()
        sleep(2)
        app.terminate()
    }

    /// mock_hub's pairing link when PERSONA_MOCK_HUB is set: the hub a package persona is downloaded from.
    /// A simulator whose permissions were reset asks for notifications on launch: the prompt would take the Home press
    /// (and the app, not active behind it, starts no activity).
    @MainActor static func allowNotifications() {
        let alert = XCUIApplication(bundleIdentifier: "com.apple.springboard").alerts.firstMatch
        guard alert.waitForExistence(timeout: 2) else { return }
        for label in ["Allow", "允许"] where alert.buttons[label].exists {
            alert.buttons[label].tap()
            sleep(2)
            return
        }
    }

    static var pair: String? {
        ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"].map {
            "persona://pair?url=" + $0.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        }
    }

    /// Another app's activity beside ours (docs/apple/live-activity.md「Sharing the Dynamic Island with other apps」): the peer app
    /// (apple/IslandPeer, `PERSONA_ISLAND_PEER` is its bundle id) starts one when it opens. Then, with the peer in front
    /// in landscape, ours alone in the landscape island (limited in width too). The peer ends its activity again.
    @MainActor private func shared(with id: String, _ phase: String) {
        let peer = XCUIApplication(bundleIdentifier: id)
        peer.launchArguments = []
        peer.launch()
        sleep(2)
        XCUIDevice.shared.press(.home)
        sleep(2)
        shot("\(phase)-shared")
        peer.activate()
        XCUIDevice.shared.orientation = .landscapeLeft
        sleep(3)
        shot("\(phase)-landscape")
        XCUIDevice.shared.orientation = .portrait
        endPeer(id)
    }

    @MainActor private func endPeer(_ id: String) {
        let peer = XCUIApplication(bundleIdentifier: id)
        peer.terminate()
        peer.launchArguments = ["-end"]
        peer.launch()
        sleep(3)
        peer.terminate()
        XCUIDevice.shared.press(.home)
        sleep(1)
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
    }
}
