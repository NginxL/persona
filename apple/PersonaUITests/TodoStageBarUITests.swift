import XCTest

/// The to-do stage bar at the default and the largest text size (#5892), against scripts/mock/mock_hub.py with
/// MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB): on the chat's task card (the widget task, on Develop) and on
/// the detail page of the to-do 「天气 App 发 1.3 版」 (on Develop), in zh-Hans, English and Japanese, light and dark. At
/// both sizes the bar is one element that says the stage and lies within the window: the interface stops growing at
/// xxxLarge (#6813), so at the largest size it is drawn as at xxxLarge, not in its accessibility layout of six dots of
/// their own (#5892; #6448 removes that layout). `TEST_RUNNER_PERSONA_SHOTS=<dir>` keeps the screenshots
/// (`stagebar-<place>-<language>-<size>-<scheme>.png`); `TEST_RUNNER_PERSONA_PHASE=before` takes them on an older build,
/// checking nothing.
final class TodoStageBarUITests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }
    private var before: Bool { env["PERSONA_PHASE"] == "before" }

    @MainActor func testStageBarAtEveryTextSize() throws {
        guard let hub = env["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let languages: [(String, [String])] = [
            ("zh-Hans", []),
            ("en", ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]),
            ("ja", ["-AppleLanguages", "(ja)", "-AppleLocale", "ja_JP"]),
        ]
        let places: [(String, [String])] = [
            ("chat", ["-tab", "assistant", "-dev.openChat", "", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]),
            ("todo", ["-tab", "assistant", "-openAssistantPage", "todos", "-openTodoTitled", "天气 App 发 1.3 版"]), // i18n-ignore: the mock's to-do
        ]
        // The look outermost: the app icon follows it, and the system's alert about that comes only when it changes.
        var first = true, lastScheme = ""
        for scheme in ["light", "dark"] {
            for (language, languageArgs) in languages {
                for size in ["default", "largest"] {
                    for (place, placeArgs) in places {
                        let name = "\(place)-\(language)-\(size)-\(scheme)"
                        let largest = size == "largest" ? ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] : []
                        let app = XCUIApplication()
                        app.launchArguments = ["-pairURL", url, "-ui.scheme", scheme] + placeArgs + languageArgs + largest
                        app.launchPinned()
                        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                        // The notification prompt comes once per install: waited for only at the first launch.
                        for label in ["允许", "Allow"] where first ? board.buttons[label].waitForExistence(timeout: 3) : board.buttons[label].exists {
                            board.buttons[label].tap()
                        }
                        first = false

                        let bar = app.descendants(matching: .any)["todo.stage"].firstMatch
                        let window = app.windows.firstMatch.frame
                        // At the largest size the to-do detail's stage row can be below the screen (its notes and plan
                        // keep growing), and the list makes its rows only as they come near: scrolled down to until it exists.
                        var found = bar.waitForExistence(timeout: 10)
                        for _ in 0..<12 where !found {
                            scroll(app, by: window.height * 0.35)
                            found = bar.waitForExistence(timeout: 2)
                        }
                        XCTAssertTrue(found, "\(name): no stage bar:\n\(app.debugDescription)")
                        sleep(2) // the page settles
                        // The bar into the middle of the screen, clear of the navigation bar and the buttons at the bottom.
                        let clear = clearPart(app, window)
                        for _ in 0..<4 where !clear.contains(bar.frame) {
                            scroll(app, by: max(-window.height * 0.35, min(window.height * 0.35, bar.frame.midY - clear.midY)))
                            sleep(1)
                        }
                        if !before {
                            check(app, bar, clear: clear, language: language, name: name)
                        }
                        if let dir = env["PERSONA_SHOTS"] {
                            // The app icon follows the color scheme, and the system says so in an alert over the page: closed first.
                            if scheme != lastScheme ? board.alerts.firstMatch.waitForExistence(timeout: 3) : board.alerts.firstMatch.exists {
                                for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
                                    board.alerts.buttons[label].tap(); sleep(1); break
                                }
                            }
                            try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/stagebar-\(name).png"))
                        }
                        lastScheme = scheme
                        app.terminate()
                    }
                }
            }
        }
    }

    /// Scrolls the page by `dy` points (positive: the content moves up), dragged slowly and held at the end so it stops
    /// there instead of flinging past. Dragged by the page's margin at the screen's leading edge, where no row has a
    /// control: from the middle of the window it could land on one, and before #6813 it turned the to-do's Due date
    /// wheel at the largest size, so the stage bar never came into view (#6783).
    @MainActor private func scroll(_ app: XCUIApplication, by dy: CGFloat) {
        let margin = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.02, dy: 0.5))
        margin.press(forDuration: 0.05, thenDragTo: margin.withOffset(CGVector(dx: 0, dy: -dy)), withVelocity: .slow,
                     thenHoldForDuration: 0.3)
    }

    /// The part of the window clear of the navigation bar and the buttons at the bottom: the window less 15% at each end,
    /// and above the to-do page's buttons where they reach higher (one above the other at the largest size, #6151, they
    /// covered the bar in Japanese).
    @MainActor private func clearPart(_ app: XCUIApplication, _ window: CGRect) -> CGRect {
        let top = window.minY + window.height * 0.15
        var bottom = window.maxY - window.height * 0.15
        for id in ["todo.handToAgent", "todo.toggleDone"] where app.buttons[id].exists {
            bottom = min(bottom, app.buttons[id].frame.minY)
        }
        return CGRect(x: window.minX, y: top, width: window.width, height: bottom - top)
    }

    /// One element that says the stage and its step (in zh-Hans word for word), no dot of its own, wholly in the clear
    /// part of the window, so the screenshot shows it.
    @MainActor private func check(_ app: XCUIApplication, _ bar: XCUIElement, clear: CGRect, language: String, name: String) {
        XCTAssertTrue(clear.contains(bar.frame.insetBy(dx: 0.5, dy: 0.5)), "\(name): the bar \(bar.frame) not wholly in \(clear)")
        XCTAssertFalse(app.descendants(matching: .any)["todo.stage.develop"].exists,
                       "\(name): a dot of its own:\n\(app.debugDescription)")
        XCTAssertTrue(bar.label.contains("2"), "\(name): \(bar.label)")
        if language == "zh-Hans" { XCTAssertEqual(bar.label, "阶段：开发（第 2 步，共 6 步）", name) } // i18n-ignore: the app's words
    }
}
