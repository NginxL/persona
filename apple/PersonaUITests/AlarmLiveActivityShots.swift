import XCTest

/// Screenshots of a Persona timer's Live Activity (#2278, docs/apple/live-activity.md "Alarms and timers (AlarmKit)"):
/// `-showAlarmSample` starts a 25-minute "Focus" timer as phone_timer_start does; the test answers AlarmKit's question,
/// then photographs the Dynamic Island (compact and expanded) and the Lock Screen. Not a pass / fail test beyond the
/// activity showing up: look at the attachments (`xcresulttool export attachments`). `PERSONA_ALARM_SAMPLE=timer|paused`.
/// On the simulator, which cannot ring a timer and whose AlarmKit prompt varies by runtime, the app draws the same
/// activity through ActivityKit (`AlarmsTool.showSample`) with the tool's own attributes: the drawing is the one a
/// real timer gets; that it rings needs a device.
final class AlarmLiveActivityShots: XCTestCase {
    @MainActor func testShots() throws {
        guard #available(iOS 26.0, *) else {
            throw XCTSkip("AlarmKit requires iOS 26")
        }
        guard let sample = ProcessInfo.processInfo.environment["PERSONA_ALARM_SAMPLE"] else {
            throw XCTSkip("set PERSONA_ALARM_SAMPLE=timer|paused")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-showAlarmSample", sample, "-tab", "settings"]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        #if !targetEnvironment(simulator)
        // First run only on a device: AlarmKit asks whether Persona may set alarms and timers.
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许", "許可"])).firstMatch
        if allow.waitForExistence(timeout: 8) { allow.tap() }
        sleep(sample == "paused" ? 6 : 3)
        #else
        sleep(3)
        #endif

        XCUIDevice.shared.press(.home)
        sleep(2)
        shot("\(sample)-compact")
        // Long-press the island to expand it; the long press does not always take, so twice.
        springboard.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 201, dy: 37)).press(forDuration: 1.5)
        sleep(2)
        shot("\(sample)-expanded")
        XCUIDevice.shared.press(.home)
        sleep(1)
        springboard.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 201, dy: 37)).press(forDuration: 2)
        sleep(2)
        shot("\(sample)-expanded2")
        XCUIDevice.shared.press(.home)
        sleep(1)

        // Lock, then wake to the Lock Screen.
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        // The system may ask whether Persona's Live Activities stay allowed; never one of the activity's own buttons.
        let always = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Always Allow", "始终允许"])).firstMatch
        if always.exists {
            always.tap()
            sleep(2)
        }
        shot("\(sample)-lock")
        let shown = springboard.staticTexts["Focus"].exists
        // Unlock again (swipe up; no passcode on the simulator): a locked simulator hangs the next test run.
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(1)
        XCTAssertTrue(shown, "the timer's activity is not on the Lock Screen")
    }

    @MainActor private func shot(_ name: String) {
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
    }
}
