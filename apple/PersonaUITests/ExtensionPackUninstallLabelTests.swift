import XCTest

/// A pack's uninstall confirmation keeps both of its buttons on one line at every text size (#5201): the system's dialog
/// ignores `lineLimit`, so the words are short, and the two buttons are the same height. Against scripts/mock/mock_hub.py with MOCK_EXTENSIONS=1
/// MOCK_EXTENSION_PACK=1 (`TEST_RUNNER_PERSONA_EXTENSION_PACK_HUB=http://127.0.0.1:<port>`), in zh-Hans and English, at the
/// default and the largest accessibility text size, light and dark; `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots
/// (`pack-labels-<language>-<size>-<scheme>.png`). `TEST_RUNNER_PERSONA_PHASE=before` takes the same screenshots on an older
/// build, checking nothing.
final class ExtensionPackUninstallLabelTests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var before: Bool { env["PERSONA_PHASE"] == "before" }

    @MainActor func testBothButtonsStayOnOneLine() throws {
        guard let hub = env["PERSONA_EXTENSION_PACK_HUB"] else {
            throw XCTSkip("needs mock_hub.py with MOCK_EXTENSIONS=1 MOCK_EXTENSION_PACK=1 (PERSONA_EXTENSION_PACK_HUB)")
        }
        continueAfterFailure = false
        let languages = [("zh-Hans", "zh_CN"), ("en", "en_US")]
        let sizes = [("default", [String]()),
                     ("largest", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"])]
        for (language, locale) in languages { for (size, sizeArgs) in sizes { for scheme in ["light", "dark"] {
            let app = XCUIApplication()
            let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
            app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-ui.scheme", scheme, "-probes", "YES",
                                   "-openMarketExtension", "3f2a9c1d0b/example-pack",
                                   "-AppleLanguages", "(\(language))", "-AppleLocale", locale] + sizeArgs
            app.launchPinned()
            let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
            for label in ["允许", "Allow"] where board.buttons[label].waitForExistence(timeout: 3) { board.buttons[label].tap() }
            // The app icon follows the color scheme, and the system says so in an alert over the page: closed first.
            if board.alerts.firstMatch.waitForExistence(timeout: 2) {
                for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
                    board.alerts.buttons[label].tap(); sleep(1); break
                }
            }

            // At the largest text the actions are further down a lazy list: scrolled to, a part of a screen at a time.
            let remove = app.buttons["market-uninstall"].firstMatch
            XCTAssertTrue(app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "Example"))
                .firstMatch.waitForExistence(timeout: 15), "the pack's market page")
            for _ in 0..<14 where !(remove.exists && remove.isHittable) {
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.72))
                    .press(forDuration: 0.05, thenDragTo: app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)))
            }
            XCTAssertTrue(remove.waitForExistence(timeout: 5))
            remove.tap()
            let confirmation = app.buttons["market-uninstall-members"].firstMatch
            XCTAssertTrue(confirmation.waitForExistence(timeout: 5), "the confirmation")
            sleep(1)
            if !before {
                // A dialog lists its buttons twice; select the drawn copies without putting hittable in a query predicate.
                let only = try XCTUnwrap(visibleDialogButton("market-uninstall-confirm", in: app), "Pack Only is not visible")
                let all = try XCTUnwrap(visibleDialogButton("market-uninstall-members", in: app), "Everything is not visible")
                // The dialog has no Cancel (a popover; tapping outside closes it), so the two buttons are measured
                // against each other: the same height. Before #5201 the longer one took a line more (zh-Hans one and
                // two lines, English two and three); both wrapping alike is ruled out by the words' measured widths
                // and the screenshots.
                XCTAssertTrue(only.exists, "Pack Only")
                XCTAssertEqual(only.frame.height, all.frame.height, accuracy: 2,
                               "\(only.label) / \(all.label) at \(size) in \(language): not the same number of lines")
                for button in [only, all] {
                    let context = "\(button.label) at \(size) in \(language), \(scheme)"
                    let label = try labelFrame(for: button, in: app)
                    assertLabel(label, inside: button, context: context)
                }
            }
            if let dir = env["PERSONA_SHOTS"] {
                try? XCUIScreen.main.screenshot().pngRepresentation
                    .write(to: URL(fileURLWithPath: dir).appending(path: "pack-labels-\(language)-\(size)-\(scheme).png"))
            }
        } } }
    }

    @MainActor private func visibleDialogButton(_ id: String, in app: XCUIApplication) -> XCUIElement? {
        app.buttons.matching(identifier: id).allElementsBoundByIndex.first(where: { $0.exists && $0.isHittable })
    }

    private struct DialogLabel: Decodable {
        let text: String
        let x, y, width, height: Double
        var frame: CGRect { CGRect(x: x, y: y, width: width, height: height) }
    }

    @MainActor private func labelFrame(for button: XCUIElement, in app: XCUIApplication) throws -> CGRect {
        let probe = app.descendants(matching: .any)["probe.confirmation-dialog-labels"].firstMatch
        let value = try XCTUnwrap(probe.value as? String, "missing dialog-label probe")
        let labels = try JSONDecoder().decode([DialogLabel].self, from: Data(value.utf8))
        return try XCTUnwrap(labels.first(where: { $0.text == button.label })?.frame,
                             "no UIKit label for \(button.label) in \(value)")
    }

    @MainActor private func assertLabel(_ labelFrame: CGRect, inside button: XCUIElement, context: String) {
        let buttonFrame = button.frame
        // A shared edge can round differently between accessibility frames; half a point covers only that rounding.
        let contained = buttonFrame.insetBy(dx: -0.5, dy: -0.5).contains(labelFrame)
        if !contained {
            let evidence = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
            evidence.name = "Confirmation label overflow: \(context)"
            evidence.lifetime = .keepAlways
            add(evidence)
        }
        XCTAssertTrue(contained,
                      "\(context): label \(labelFrame) is outside button \(buttonFrame)")
    }
}
