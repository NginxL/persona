import VisionKit
import XCTest

/// Pairing can choose a QR photo even where the camera scanner is unsupported or unavailable (#11057). The action uses
/// the existing localized Choose from Photos copy and opens the system picker without a full-photo-library prompt.
final class PairPhotoPickerUITests: XCTestCase {
    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor func testChooseFromPhotosIsAvailableWithoutCameraSupport() throws {
        guard !(DataScannerViewController.isSupported && DataScannerViewController.isAvailable) else {
            throw XCTSkip("this case covers devices where the camera scanner is unsupported or unavailable")
        }
        let app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-pairURL", "", "-ui.scheme", "light"]
        app.launchPinned()
        defer { app.terminate() }

        XCTAssertTrue(app.buttons["welcome.pair"].waitForExistence(timeout: 10), "no welcome page")
        app.buttons["welcome.pair"].tap()
        let scanOrToken = app.buttons["扫码或其他配对方式"]
        XCTAssertTrue(scanOrToken.waitForExistence(timeout: 10), "no scan-or-token pairing method")
        scanOrToken.tap()

        let choose = app.buttons["pair.choosePhoto"]
        XCTAssertTrue(choose.waitForExistence(timeout: 10),
                      "photo QR selection disappeared with the camera scanner:\n\(app.debugDescription)")
        XCTAssertTrue(["Choose from Photos", "从照片选取", "写真から選択"].contains(choose.label),
                      "the new action does not use the localized Choose from Photos copy: \(choose.label)")
        choose.tap()

        let covered = expectation(for: NSPredicate(format: "hittable == false"), evaluatedWith: choose)
        XCTAssertEqual(XCTWaiter.wait(for: [covered], timeout: 10), .completed,
                       "Choose from Photos did not open a picker:\n\(app.debugDescription)")
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let fullAccess = springboard.buttons.matching(NSPredicate(format: "label IN %@",
            ["Allow Full Access", "允许完全访问", "フルアクセスを許可"])).firstMatch
        XCTAssertFalse(fullAccess.exists, "the system PhotosPicker should not request full photo-library access")
    }

    @MainActor func testPickingAValidPhotoAsksBeforePairing() throws {
        let env = ProcessInfo.processInfo.environment
        guard env["PERSONA_MOCK_HUB"] != nil else { throw XCTSkip("needs the mock hub that names the seeded QR") }
        let app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-pairURL", "", "-ui.scheme", "light"]
        app.launchPinned()
        defer { app.terminate() }

        XCTAssertTrue(app.buttons["welcome.pair"].waitForExistence(timeout: 10), "no welcome page")
        app.buttons["welcome.pair"].tap()
        let scanOrToken = app.buttons["扫码或其他配对方式"]
        XCTAssertTrue(scanOrToken.waitForExistence(timeout: 10), "no scan-or-token pairing method")
        scanOrToken.tap()
        let choose = app.buttons["pair.choosePhoto"]
        XCTAssertTrue(choose.waitForExistence(timeout: 10), "no photo action")
        choose.tap()

        let pictures = app.images.matching(identifier: "PXGGridLayout-Info")
        XCTAssertTrue(pictures.firstMatch.waitForExistence(timeout: 20), "no seeded QR photo in the system picker")
        XCTAssertGreaterThanOrEqual(pictures.count, 3, "the seeded QR photos are not all present")
        pictures.element(boundBy: 0).tap()
        let addButton = app.buttons.matching(identifier: "Add").firstMatch
        if addButton.waitForExistence(timeout: 2) { addButton.tap() }

        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 15), "the valid photo paired without the existing confirmation")
        XCTAssertTrue(alert.label.contains("127.0.0.1"), "the confirmation names a different hub: \(alert.label)")
        XCTAssertTrue(alert.buttons["pair.confirm"].exists, "the photo did not reach the normal Connect action")
        let screen = app.screenshot()
        let attachment = XCTAttachment(screenshot: screen)
        attachment.name = "pair-photo-confirm-light"
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] {
            try? screen.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("pair-photo-confirm-light.png"))
        }
        alert.buttons["pair.cancel"].firstMatch.tap()
        XCTAssertFalse(app.buttons["engine.save"].exists, "Cancel paired anyway")
    }

    @MainActor func testPhotoActionInLightDarkAndLargeText() throws {
        let env = ProcessInfo.processInfo.environment
        for (scheme, large) in [("light", false), ("dark", false), ("light", true)] {
            let app = XCUIApplication()
            app.launchArguments = ["-openWelcome", "YES", "-pairURL", "", "-ui.scheme", scheme]
            if large { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
            app.launchPinned()
            XCTAssertTrue(app.buttons["welcome.pair"].waitForExistence(timeout: 10), "no welcome page")
            app.buttons["welcome.pair"].tap()
            let scanOrToken = app.buttons["扫码或其他配对方式"]
            XCTAssertTrue(scanOrToken.waitForExistence(timeout: 10), "no scan-or-token pairing method")
            scanOrToken.tap()
            let choose = app.buttons["pair.choosePhoto"]
            XCTAssertTrue(choose.waitForExistence(timeout: 10), "no photo action")
            let screen = app.screenshot()
            let name = "pair-photo-screen-\(scheme)-\(large ? "large" : "default")"
            let attachment = XCTAttachment(screenshot: screen)
            attachment.name = name
            attachment.lifetime = .keepAlways
            add(attachment)
            if let dir = env["PERSONA_SHOTS"] {
                try? screen.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
            }
            app.terminate()
        }
    }
}
