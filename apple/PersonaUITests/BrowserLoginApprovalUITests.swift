import XCTest

/// Settings › Devices › Scan Browser Sign-in Code (#10293) against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB), each
/// step opened with sample data (`-showBrowserLoginSample`): the entry, the request from the LAN, from the internet and
/// forwarded, the number, how it ends, and that Decline closes the sheet. With PERSONA_SHOTS set it writes what it saw
/// there (PERSONA_SHOTS_APPEARANCE names the appearance; PERSONA_SHOTS_SIZE, a content size category such as
/// UICTContentSizeCategoryXXXL, launches at that text size).
final class BrowserLoginApprovalUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = value
    }

    /// Launches at Settings › Devices with that step open. Now and then the app was found on the Assistant instead of
    /// Settings (the sample's sheet asked for while Settings' own was still opening): it is launched once more.
    @MainActor private func launch(_ step: String, expect id: String) -> XCUIApplication {
        let app = launch(step)
        if element(app, id).waitForExistence(timeout: 15) { return app }
        app.terminate()
        return launch(step)
    }

    @MainActor private func launch(_ step: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-dev.openChat", "", "-openDevices", "YES",
                               "-showBrowserLoginSample", step]
        if let size = ProcessInfo.processInfo.environment["PERSONA_SHOTS_SIZE"] {
            app.launchArguments += ["-UIPreferredContentSizeCategoryName", size]
        }
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let size = env["PERSONA_SHOTS_SIZE"].map { $0.contains("XXXL") ? "-xxxl" : "-" + $0 } ?? ""
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try image.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/browser-login-\(appearance)\(size)-\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    @MainActor func testTheEntryIsOnDevices() throws {
        let app = launch("none", expect: "devices.scanBrowser")
        XCTAssertTrue(element(app, "devices.scanBrowser").waitForExistence(timeout: 20), "no Scan Browser Sign-in Code on Devices")
        try shot(app, "entry")
    }

    @MainActor func testARequestFromTheLAN() throws {
        let app = launch("lan", expect: "browserLogin.allow")
        XCTAssertTrue(element(app, "browserLogin.allow").waitForExistence(timeout: 20))
        XCTAssertTrue(element(app, "browserLogin.decline").exists)
        XCTAssertTrue(element(app, "browserLogin.close").exists, "the sheet leaves by its ✕")
        XCTAssertFalse(element(app, "browserLogin.internetWarning").exists, "a LAN request has no warning")
        try shot(app, "lan")
    }

    @MainActor func testARequestFromTheInternetIsWarnedAbout() throws {
        let app = launch("internet", expect: "browserLogin.allow")
        XCTAssertTrue(element(app, "browserLogin.internetWarning").waitForExistence(timeout: 20))
        XCTAssertTrue(element(app, "browserLogin.allow").exists, "warned, not refused")
        try shot(app, "internet")
    }

    @MainActor func testAForwardedRequestSaysItCantTell() throws {
        let app = launch("unknown", expect: "browserLogin.allow")
        XCTAssertTrue(element(app, "browserLogin.allow").waitForExistence(timeout: 20))
        try shot(app, "unknown")
    }

    @MainActor func testTheNumberReadsDigitByDigit() throws {
        let app = launch("number", expect: "browserLogin.number")
        let number = element(app, "browserLogin.number")
        XCTAssertTrue(number.waitForExistence(timeout: 20))
        XCTAssertEqual(number.label, "4 7")
        XCTAssertTrue(element(app, "browserLogin.cancel").exists)
        XCTAssertTrue(element(app, "browserLogin.close").exists)
        try shot(app, "number")
    }

    @MainActor func testHowItEnds() throws {
        for step in ["signedIn", "voided"] {
            let app = launch(step, expect: "browserLogin.result")
            XCTAssertTrue(element(app, "browserLogin.result").waitForExistence(timeout: 20), step)
            XCTAssertTrue(element(app, "browserLogin.done").exists, step)
            try shot(app, step)
            app.terminate()
        }
    }

    @MainActor func testDeclineClosesTheSheet() throws {
        let app = launch("lan", expect: "browserLogin.allow")
        let decline = element(app, "browserLogin.decline")
        XCTAssertTrue(decline.waitForExistence(timeout: 20))
        decline.tap()
        XCTAssertTrue(element(app, "browserLogin.allow").waitForNonExistence(timeout: 10), "Decline left the sheet open")
        XCTAssertTrue(element(app, "devices.scanBrowser").waitForExistence(timeout: 10))
    }
}
