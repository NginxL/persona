import XCTest

/// Settings › This Device › Live Screen (#6398, #6396) against scripts/mock/mock_hub.py (TEST_RUNNER_PERSONA_SCREEN_HUB,
/// MOCK_SCREEN_VIDEO=1): the route switch is there only while direct routes are offered (here a DEBUG launch with
/// -showScreenRoutes), first, above the quality on mobile data; it is on by default and stays as set, and the page reads in light and dark, at the default size and at xxxLarge (where interface
/// text stops growing), in English and Chinese. Screenshots are attachments (PERSONA_SHOTS also writes them out).
final class LiveScreenSettingsTests: XCTestCase {
    private var hub: String? { ProcessInfo.processInfo.environment["PERSONA_SCREEN_HUB"] }

    @MainActor private func launch(_ args: [String], fresh: Bool = true) throws -> XCUIApplication {
        guard let hub else { throw XCTSkip("needs the mock: TEST_RUNNER_PERSONA_SCREEN_HUB") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=" + UITestToken.device
        app.launchArguments = ["-pairURL", pair, "-tab", "settings"] + args
        app.launchPinned()
        dismissIconNotice(app)
        return app
    }

    /// The row in This Device opens the page; the switch is on; turned off, it stays off on the next launch.
    @MainActor func testTheRouteSwitch() throws {
        let app = try launch(["-showScreenRoutes", "YES", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        let row = app.buttons["settings-live-screen"].firstMatch
        for _ in 0..<8 where !row.isHittable { app.swipeUp() }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the row:\n\(app.debugDescription)")
        row.tap()
        let toggle = app.switches["settings-live-screen-routes"].firstMatch
        XCTAssertTrue(toggle.waitForExistence(timeout: 10), "the switch:\n\(app.debugDescription)")
        XCTAssertEqual(toggle.value as? String, "1", "on by default")
        toggle.switches.firstMatch.exists ? toggle.switches.firstMatch.tap() : toggle.tap()
        XCTAssertEqual(toggle.value as? String, "0")
        app.terminate()

        let again = try launch(["-showScreenRoutes", "YES", "-openLiveScreenSettings", "YES"])
        let kept = again.switches["settings-live-screen-routes"].firstMatch
        XCTAssertTrue(kept.waitForExistence(timeout: 10))
        XCTAssertEqual(kept.value as? String, "0", "kept off")
        kept.switches.firstMatch.exists ? kept.switches.firstMatch.tap() : kept.tap() // back on for the next test
        XCTAssertEqual(kept.value as? String, "1")
    }

    /// Without direct routes offered (a release build until acceptance) the page has no route switch; the quality section
    /// (#6396) is there for a computer with video, as the mock's MacBook is.
    @MainActor func testNoRouteSwitchWithoutDirectRoutes() throws {
        let app = try launch(["-openLiveScreenSettings", "YES"])
        XCTAssertTrue(app.descendants(matching: .any)["settings-live-screen-mobile-data"].firstMatch.waitForExistence(timeout: 15),
                      "the quality section:\n\(app.debugDescription)")
        XCTAssertFalse(app.switches["settings-live-screen-routes"].exists, "no route switch while direct routes aren't offered")
    }

    /// The page and the row for review: light and dark, default size and xxxLarge, English and Chinese. The page is
    /// reached by tapping its row, as people reach it (opened at launch, an iPad's detail column draws a short page's
    /// large title over its first row).
    @MainActor func testShots() throws {
        for lang in ["en", "zh-Hans"] {
            for scheme in ["light", "dark"] {
                for size in ["UICTContentSizeCategoryL", "UICTContentSizeCategoryXXXL"] {
                    let locale = lang == "en" ? "en_US" : "zh_CN"
                    let app = try launch(["-showScreenRoutes", "YES", "-ui.scheme", scheme, "-UIPreferredContentSizeCategoryName", size,
                                          "-AppleLanguages", "(\(lang))", "-AppleLocale", locale])
                    dismissIconNotice(app)
                    let row = app.buttons["settings-live-screen"].firstMatch
                    XCTAssertTrue(app.searchFields.firstMatch.waitForExistence(timeout: 15), "Settings:\n\(app.debugDescription)")
                    for _ in 0..<12 where !(row.exists && row.isHittable) { app.swipeUp() }
                    XCTAssertTrue(row.waitForExistence(timeout: 5), "the row:\n\(app.debugDescription)")
                    let short = size.hasSuffix("XXXL") ? "xxxl" : "default"
                    if short == "default" { shot(app, "live-screen-row-\(lang)-\(scheme)") }
                    row.tap()
                    XCTAssertTrue(app.switches["settings-live-screen-routes"].firstMatch.waitForExistence(timeout: 10))
                    sleep(1) // the push settles
                    shot(app, "live-screen-settings-\(lang)-\(scheme)-\(short)")
                    app.terminate()
                }
            }
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let full = "\(name)-\(UITestStyle.name)"
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = full
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(full + ".png"))
        }
    }

    /// The icon follows the look, and iOS says so in an alert over the screen: tap it away.
    @MainActor @discardableResult private func dismissIconNotice(_ app: XCUIApplication) -> Bool {
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        for source in [app, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
            let alert = source.alerts.containing(notice).firstMatch
            if alert.waitForExistence(timeout: 2), alert.buttons.firstMatch.exists {
                alert.buttons.firstMatch.tap()
                return true
            }
        }
        return false
    }
}
