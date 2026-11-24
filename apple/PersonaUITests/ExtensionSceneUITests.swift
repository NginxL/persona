import Foundation
import XCTest

/// UI version 11's 3D `scene` on the mock's made-up two-room Home page and its compact overview card
/// (`MOCK_EXTENSIONS=1 MOCK_EXTENSION_SCENE=1`, `TEST_RUNNER_PERSONA_SCENE_HUB=http://127.0.0.1:<port>`).
/// Covers opening at the default angle, drag-orbiting, zooming, full screen, a device pin's push keeping the person's
/// camera across the redraw on return, Reset View, and the compact overview card opening Full Screen.
final class ExtensionSceneUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var pad: Bool { UIDevice.current.userInterfaceIdiom == .pad }
    private var scheme: String { env["PERSONA_SCENE_SCHEME"] ?? "light" }
    private var shotTag: String { env["PERSONA_SCENE_TAG"] ?? scheme }
    private let homeEntry = "sidebar-extension-example-home-home-scene"

    @MainActor private func launch(tab: String = "assistant") throws -> XCUIApplication {
        guard let hub = env["PERSONA_SCENE_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_SCENE=1 (PERSONA_SCENE_HUB)")
        }
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        var args = ["-pairURL", pair, "-tab", tab, "-dev.openChat", "", "-demo.on", "NO", "-ui.scheme", scheme]
        if let contentSize = env["PERSONA_SCENE_CONTENT_SIZE"], !contentSize.isEmpty {
            args += ["-UIPreferredContentSizeCategoryName", contentSize]
        }
        app.launchArguments = args
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 3) { // i18n-ignore
            springboard.buttons[label].tap()
        }
        return app
    }

    @MainActor private func openHome(_ app: XCUIApplication) {
        let row = app.descendants(matching: .any)[homeEntry]
        if !row.waitForExistence(timeout: 10) {
            let sidebar = app.buttons["assistant-sidebar-button"]
            XCTAssertTrue(sidebar.waitForExistence(timeout: 10), app.debugDescription)
            sidebar.tap()
        }
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["extension-ui-ready"].waitForExistence(timeout: 20),
                      app.debugDescription)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let png = app.screenshot()
        let attachment = XCTAttachment(screenshot: png)
        attachment.name = "\(name)-\(shotTag)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let base = URL(fileURLWithPath: dir).appending(path: "scene-\(pad ? "ipad" : "iphone")-\(name)-\(shotTag).png")
        if env["PERSONA_SCENE_OUTER_SHOTS"] == "1" {
            outerShot(base)
        } else {
            try? png.pngRepresentation.write(to: base)
        }
    }

    @MainActor private func outerShot(_ base: URL) {
        let ready = base.appendingPathExtension("ready"), taken = base.appendingPathExtension("taken")
        try? FileManager.default.removeItem(at: ready)
        try? FileManager.default.removeItem(at: taken)
        FileManager.default.createFile(atPath: ready.path, contents: nil)
        let done = XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in
            FileManager.default.fileExists(atPath: taken.path)
        }, object: nil)
        XCTAssertEqual(XCTWaiter().wait(for: [done], timeout: 60), .completed,
                       "no simulator screenshot taken of \(base.lastPathComponent)")
    }

    @MainActor private func visible(_ elements: [XCUIElement], in app: XCUIApplication) -> XCUIElement? {
        let bounds = app.windows.firstMatch.frame.insetBy(dx: 0, dy: 96)
        return elements.first { $0.exists && bounds.contains($0.frame) && $0.isHittable }
    }

    @MainActor func testOrbitZoomResetFullScreenAndPinPush() throws {
        let app = try launch()
        openHome(app)
        let scene = app.descendants(matching: .any)["extension-scene"]
        XCTAssertTrue(scene.waitForExistence(timeout: 20), app.debugDescription)
        let reset = app.buttons["extension-scene-reset"]
        let zoomOut = app.buttons["extension-scene-zoom-out"]
        let zoomIn = app.buttons["extension-scene-zoom-in"]
        XCTAssertTrue(zoomOut.waitForExistence(timeout: 10), app.debugDescription)
        if reset.isEnabled {
            reset.tap()
        }
        XCTAssertTrue(reset.exists && !reset.isEnabled, "opening camera is untouched")
        shot(app, "page")

        // Drag across the open upper canvas (above device pins and clear of the leading back-swipe zone) to orbit.
        let start = scene.coordinate(withNormalizedOffset: CGVector(dx: 0.78, dy: 0.18))
        let finish = scene.coordinate(withNormalizedOffset: CGVector(dx: 0.32, dy: 0.14))
        start.press(forDuration: 0.05, thenDragTo: finish)
        XCTAssertTrue(reset.isEnabled, "orbiting enables Reset View")

        zoomOut.tap()
        zoomIn.tap()
        shot(app, "orbited")

        app.buttons["extension-scene-full-screen"].tap()
        let close = app.buttons["extension-scene-close"]
        XCTAssertTrue(close.waitForExistence(timeout: 10), app.debugDescription)
        shot(app, "full-screen")

        let cover = app.descendants(matching: .any)["extension-scene-cover"]
        XCTAssertTrue(cover.waitForExistence(timeout: 10), app.debugDescription)
        let pins = cover.descendants(matching: .button).matching(
            NSPredicate(format: "identifier BEGINSWITH %@", "extension-scene-pin-")).allElementsBoundByIndex
        let selected = try XCTUnwrap(visible(pins, in: app), "no visible device pin in full screen:\n\(app.debugDescription)")
        selected.tap()

        let pushed = app.staticTexts["This page stands for a device's own page: its switches, its temperature and its history."]
        XCTAssertTrue(pushed.waitForExistence(timeout: 15), app.debugDescription)
        app.navigationBars.buttons.element(boundBy: 0).tap()

        XCTAssertTrue(app.buttons["extension-scene-full-screen"].waitForExistence(timeout: 15), app.debugDescription)
        // Returning redraws the page (bedroom lamp flips state), while the person's orbited camera stays put.
        XCTAssertTrue(app.buttons["extension-scene-reset"].isEnabled, "camera stayed where the person left it across redraw")
        shot(app, "page-after-push")

        app.buttons["extension-scene-reset"].tap()
        XCTAssertFalse(app.buttons["extension-scene-reset"].isEnabled, "Reset View restores the opening angle")
    }

    @MainActor func testCompactPreviewOpensFullScreen() throws {
        let app = try launch(tab: "settings")
        if !app.descendants(matching: .any)["settings-list"].waitForExistence(timeout: 5) {
            let sidebar = app.buttons["assistant-sidebar-button"]
            XCTAssertTrue(sidebar.waitForExistence(timeout: 10), app.debugDescription)
            sidebar.tap()
            let settings = app.drawerSettingsButton
            XCTAssertTrue(settings.waitForExistence(timeout: 10), app.debugDescription)
            settings.tap()
        }
        XCTAssertTrue(app.descendants(matching: .any)["settings-list"].waitForExistence(timeout: 15),
                      app.debugDescription)
        shot(app, "non-scene-settings")

        let plugins = app.descendants(matching: .any)["settings-plugins"]
        XCTAssertTrue(plugins.waitForExistence(timeout: 10), app.debugDescription)
        plugins.tap()
        let card = app.descendants(matching: .any)["plugin-card-example-home-home-scene-card"]
        let preview = app.descendants(matching: .any)["extension-scene-preview"]
        let extensionsList = app.descendants(matching: .any)["extensions-list"]
        for _ in 0..<10 where !card.waitForExistence(timeout: 2) { extensionsList.swipeUp(velocity: .fast) }
        XCTAssertTrue(card.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(preview.waitForExistence(timeout: 15), app.debugDescription)
        shot(app, "overview")

        preview.tap()
        XCTAssertTrue(app.buttons["extension-scene-close"].waitForExistence(timeout: 15), app.debugDescription)
        shot(app, "preview-full-screen")
        app.buttons["extension-scene-close"].tap()
        XCTAssertTrue(preview.waitForExistence(timeout: 10), app.debugDescription)
    }

    @MainActor func testDowngradeFallbackRendersPinRows() throws {
        guard env["PERSONA_SCENE_EXPECT_DOWNGRADE"] == "1" else {
            throw XCTSkip("only runs when MOCK_EXTENSION_SCENE_DOWNGRADE=1")
        }
        let app = try launch()
        openHome(app)
        XCTAssertFalse(app.descendants(matching: .any)["extension-scene"].exists)
        XCTAssertTrue(app.staticTexts["Ceiling lamp"].waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts["Air conditioner"].exists, app.debugDescription)
        shot(app, "downgrade")
    }
}
