import Foundation
import XCTest

/// UI version 10's graph on the mock's made-up 200-node Work page and its compact overview card, plus UI version 9's
/// gauge card (`MOCK_EXTENSIONS=1 MOCK_EXTENSION_GRAPH=1 MOCK_EXTENSION_GAUGE=1`,
/// `TEST_RUNNER_PERSONA_GRAPH_HUB=http://127.0.0.1:<port>`).
/// Covers zoom, folding, full screen, a node's row-style push, and the still preview opening the same canvas.
/// `TEST_RUNNER_PERSONA_SHOTS` keeps screenshots; `TEST_RUNNER_PERSONA_GRAPH_OUTER_SHOTS=1` asks a driver to take them
/// with `simctl io screenshot`, and `TEST_RUNNER_PERSONA_GRAPH_SCHEME=light|dark` chooses the appearance.
final class ExtensionGraphUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var pad: Bool { UIDevice.current.userInterfaceIdiom == .pad }
    private var scheme: String { env["PERSONA_GRAPH_SCHEME"] ?? "light" }
    private let workEntry = "sidebar-extension-example-work-work"

    @MainActor private func launch(tab: String = "assistant") throws -> XCUIApplication {
        guard let hub = env["PERSONA_GRAPH_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_GRAPH=1 MOCK_EXTENSION_GAUGE=1 (PERSONA_GRAPH_HUB)")
        }
        continueAfterFailure = false
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-tab", tab, "-dev.openChat", "", "-demo.on", "NO",
                               "-ui.scheme", scheme]
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where springboard.buttons[label].waitForExistence(timeout: 3) {
            springboard.buttons[label].tap()
        }
        return app
    }

    @MainActor private func openWork(_ app: XCUIApplication) {
        let row = app.descendants(matching: .any)[workEntry]
        if !row.waitForExistence(timeout: 10) {
            let sidebar = app.buttons["assistant-sidebar-button"]
            XCTAssertTrue(sidebar.waitForExistence(timeout: 10), app.debugDescription)
            sidebar.tap()
        }
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["extension-ui-ready"].waitForExistence(timeout: 20),
                      app.debugDescription)
        XCTAssertTrue(app.descendants(matching: .any)["extension-graph"].waitForExistence(timeout: 20),
                      app.debugDescription)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let png = app.screenshot()
        let attachment = XCTAttachment(screenshot: png)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let base = URL(fileURLWithPath: dir).appending(path: "graph-\(pad ? "ipad" : "iphone")-\(name)-\(scheme).png")
        if env["PERSONA_GRAPH_OUTER_SHOTS"] == "1" {
            outerShot(base)
        } else {
            try? png.pngRepresentation.write(to: base)
        }
    }

    /// A driver takes the simulator's own image (`simctl io screenshot`), avoiding XCTest's rotated iPad captures.
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
        let bounds = app.windows.firstMatch.frame.insetBy(dx: 0, dy: 120)
        return elements.first { $0.exists && bounds.contains($0.frame) && $0.isHittable }
    }

    @MainActor func testZoomFoldFullScreenAndNodePush() throws {
        let app = try launch()
        openWork(app)
        shot(app, "page")
        let zoomOut = app.buttons["extension-graph-zoom-out"]
        XCTAssertTrue(zoomOut.waitForExistence(timeout: 10), app.debugDescription)
        zoomOut.tap()
        app.buttons["extension-graph-zoom-in"].tap()

        // Fold a visible parent through its chip, then restore it. The chip is hidden from VoiceOver because the node
        // carries the same custom action, so drive its actual children-edge coordinate rather than an AX duplicate.
        var foldPair: (parent: XCUIElement, child: XCUIElement)?
        for part in 0..<8 {
            for step in [3, 4, 1, 2, 0, 5] {
                let parent = app.buttons["extension-graph-node-p\(part).s\(step)"]
                let child = app.buttons["extension-graph-node-p\(part).s\(step).c0"]
                if visible([parent], in: app) != nil && child.exists {
                    foldPair = (parent, child)
                    break
                }
            }
            if foldPair != nil { break }
        }
        let (parent, child) = try XCTUnwrap(foldPair, "no visible parent with children:\n\(app.debugDescription)")
        parent.coordinate(withNormalizedOffset: CGVector(dx: 1, dy: 0.5)).tap()
        XCTAssertTrue(child.waitForNonExistence(timeout: 5), app.debugDescription)
        parent.coordinate(withNormalizedOffset: CGVector(dx: 1, dy: 0.5)).tap()
        XCTAssertTrue(child.waitForExistence(timeout: 5), app.debugDescription)

        app.buttons["extension-graph-full-screen"].tap()
        XCTAssertTrue(app.buttons["extension-graph-close"].waitForExistence(timeout: 10), app.debugDescription)
        shot(app, "full-screen")
        let cover = app.descendants(matching: .any)["extension-graph-cover"]
        XCTAssertTrue(cover.waitForExistence(timeout: 10), app.debugDescription)
        let nodes = cover.descendants(matching: .button).matching(
            NSPredicate(format: "identifier BEGINSWITH %@", "extension-graph-node-")).allElementsBoundByIndex
        guard let selected = visible(nodes, in: app) else {
            XCTFail("no visible node with an action in the full-screen canvas:\n\(app.debugDescription)")
            return
        }
        selected.tap()
        let pushed = app.staticTexts["This page stands for a node's own page: its question, its result and its steps."]
        XCTAssertTrue(pushed.waitForExistence(timeout: 15), app.debugDescription)
        app.navigationBars.buttons.element(boundBy: 0).tap()
        XCTAssertTrue(app.buttons["extension-graph-full-screen"].waitForExistence(timeout: 15), app.debugDescription)
        shot(app, "page-after-push")
        app.buttons["extension-graph-fit"].tap()
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
        let plugins = app.descendants(matching: .any)["settings-plugins"]
        XCTAssertTrue(plugins.waitForExistence(timeout: 10), app.debugDescription)
        plugins.tap()
        let card = app.descendants(matching: .any)["plugin-card-example-work-work-card"]
        let preview = app.descendants(matching: .any)["extension-graph-preview"]
        let extensionsList = app.descendants(matching: .any)["extensions-list"]
        for _ in 0..<10 where !card.waitForExistence(timeout: 2) { extensionsList.swipeUp(velocity: .fast) }
        XCTAssertTrue(card.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(preview.waitForExistence(timeout: 15), app.debugDescription)
        preview.tap()
        XCTAssertTrue(app.buttons["extension-graph-close"].waitForExistence(timeout: 15), app.debugDescription)
        shot(app, "preview-full-screen")
        app.buttons["extension-graph-close"].tap()
        XCTAssertTrue(preview.waitForExistence(timeout: 10), app.debugDescription)
        shot(app, "overview")
    }

    @MainActor func testGaugeCard() throws {
        let app = try launch()
        let row = app.descendants(matching: .any)["sidebar-extension-example-quota-quota"]
        if !row.waitForExistence(timeout: 10) {
            let sidebar = app.buttons["assistant-sidebar-button"]
            XCTAssertTrue(sidebar.waitForExistence(timeout: 10), app.debugDescription)
            sidebar.tap()
        }
        XCTAssertTrue(row.waitForExistence(timeout: 10), app.debugDescription)
        row.tap()
        XCTAssertTrue(app.descendants(matching: .any)["extension-ui-ready"].waitForExistence(timeout: 20),
                      app.debugDescription)
        for key in ["five_hours", "week", "fable_week"] {
            XCTAssertTrue(app.descendants(matching: .any)["plugin-ui-tile-\(key)"].waitForExistence(timeout: 10),
                          app.debugDescription)
        }
        shot(app, "gauge")
    }
}
