import XCTest

/// Issue #9612 item 4: a device that cannot receive pushes says so on Persona's main screen, not only in Settings.
/// Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB). `-showPushHealth unreachable` needs no APNs token; `direct` is
/// the healthy control. PERSONA_SHOTS optionally writes the light/dark and largest-text captures outside the repository.
final class PushHealthNoticeUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    /// `PERSONA_ORIENTATION=landscape`: the captures held sideways (the Duo open on its side), like `ComposerExpandTests`.
    private var landscape: Bool { ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" }

    override func tearDown() {
        if landscape { XCUIDevice.shared.orientation = .portrait }
        app?.terminate()
        super.tearDown()
    }

    /// `keepClosed`: read the ✕ an earlier launch remembered (#12319); otherwise every launch starts with none closed.
    @MainActor private func launch(_ sample: String, look: String = "light", largest: Bool = false, keepClosed: Bool = false) {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001",
                               "-showPushHealth", sample, "-ui.scheme", look]
            + (keepClosed ? [] : ["-pushNotice.dismissed", "{}"])
            + (largest ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : [])
        app.launchPinned()
        if landscape { XCUIDevice.shared.orientation = .landscapeLeft }

        // Answer only the system's notification question. A previously authorized simulator shows no alert.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["允许", "Allow"])).firstMatch
        if allow.waitForExistence(timeout: 2) { allow.tap() }

        // Prove the fixture reached Persona's main screen before blaming a missing notice on the implementation.
        let composer = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(composer.waitForExistence(timeout: 20), "Persona's main screen did not open:\n\(app.debugDescription)")
    }

    @MainActor private var notice: XCUIElement {
        app.descendants(matching: .any)["pushHealth.notice"].firstMatch
    }

    @MainActor private func requireNotice() -> XCUIElement {
        XCTAssertTrue(notice.waitForExistence(timeout: 10),
                      "an unreachable push sample has no notice on Persona's main screen:\n\(app.debugDescription)")
        XCTAssertTrue(notice.isHittable, "the push-health notice exists but is not visible")
        return notice
    }

    @MainActor func testUnhealthySampleShowsMainScreenNoticeAndHealthySampleDoesNot() {
        launch("unreachable")
        _ = requireNotice()

        launch("direct")
        XCTAssertFalse(notice.waitForExistence(timeout: 2), "a healthy direct push route must not show the notice")
    }

    /// #12319: the ✕ hides the notice and the next launch remembers; another cause shows its own notice, and pushes
    /// reaching the device again forget the ✕. Settings › Notifications keeps the status.
    @MainActor func testClosingTheNoticeIsRememberedUntilItsCauseChanges() {
        launch("unreachable")
        let close = requireNotice().buttons["notice.dismiss"]
        XCTAssertTrue(close.isHittable, "the notice has no visible close button:\n\(app.debugDescription)")
        XCTAssertEqual(close.label, "隐藏")
        close.tap()
        XCTAssertTrue(notice.waitForNonExistence(timeout: 5), "the ✕ did not hide the notice")

        launch("unreachable", keepClosed: true)
        XCTAssertFalse(notice.waitForExistence(timeout: 3), "a closed notice came back on the next launch")

        launch("moved", keepClosed: true)
        _ = requireNotice()

        launch("direct", keepClosed: true)
        launch("unreachable", keepClosed: true)
        _ = requireNotice()
    }

    @MainActor func testNoticeUsesPlainLanguageAndOneStatusSpecificRepairAction() {
        launch("unreachable")
        let notice = requireNotice()
        let primary = notice.staticTexts.firstMatch.label
        XCTAssertTrue(primary.contains("通知") && primary.contains("设备"),
                      "the primary message must say which everyday capability is affected: \(primary)")
        XCTAssertTrue(primary.contains("检查网络"), "the primary message must name this status's repair: \(primary)")
        for detail in ["HTTP", "relay", "中继", "credential", "凭据", "APNs", "apns.json", "403", "502",
                       "The request timed out", "topic not served"] {
            XCTAssertFalse(primary.localizedCaseInsensitiveContains(detail),
                           "engineering detail '\(detail)' is the primary message: \(primary)")
        }

        let repairs = notice.buttons.matching(NSPredicate(format: "identifier != %@", "notice.dismiss"))
        XCTAssertEqual(repairs.count, 1, "the notice must offer exactly one repair action besides its ✕")
        let action = notice.buttons["pushHealth.action"]
        XCTAssertEqual(action.label, "Persona 通知设置",
                       "the action must name its actual in-app destination: \(action.label)")
        action.tap()

        // The action uses the existing repair surface; no physical APNs registration or external App Store is involved.
        let repair = app.descendants(matching: .any)["notifications.backgroundPush"].firstMatch
        XCTAssertTrue(repair.waitForExistence(timeout: 10),
                      "the notice's action did not open Settings › Notifications:\n\(app.debugDescription)")
        let guidance = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@",
                                                            "检查网络", "下次打开 Persona")).firstMatch
        XCTAssertTrue(guidance.waitForExistence(timeout: 5),
                      "the existing status-specific repair was not kept:\n\(app.debugDescription)")
    }

    @MainActor func testNoticeCapturesInLightAndDarkAtDefaultAndLargestText() {
        for largest in [false, true] {
            for look in ["light", "dark"] {
                launch("unreachable", look: look, largest: largest)
                let notice = requireNotice()
                XCTAssertTrue(notice.buttons["pushHealth.action"].isHittable && notice.buttons["notice.dismiss"].isHittable,
                              "the repair action is not visible at \(look), largest=\(largest)")
                shot(look: look, largest: largest)
            }
        }
    }

    @MainActor private func shot(look: String, largest: Bool) {
        // Light & Dark changes can make SpringBoard report the app-icon change over the screen under test.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let alert = springboard.alerts.firstMatch
        for label in ["好", "OK"] where alert.buttons[label].waitForExistence(timeout: 1) {
            alert.buttons[label].tap()
            break
        }
        let image = app.screenshot()
        let name = "push-health-notice-\(look)-\(largest ? "largest" : "default")\(landscape ? "-landscape" : "")-\(UITestStyle.name)"
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: name + ".png"))
    }
}
