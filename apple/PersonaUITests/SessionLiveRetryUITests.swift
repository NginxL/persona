import XCTest

/// The live model retry above an Agent chat's composer (#887), against MOCK_LIVE_STATUS=retry. Every language, text
/// size and appearance checks the observed error and whole attempt count; ordinary work removes the note.
final class SessionLiveRetryUITests: XCTestCase {
    private let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    private let raw = "429 Synthetic rate limit for retry capture"
    private var hub = ""

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_LIVE_STATUS=retry (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor func testRetryAndRecoveryInEveryLanguageSizeAndAppearance() throws {
        let languages: [(String, String, String, String)] = [
            ("en", "en_US", "(en)", "Retrying model request (5/10)"),
            ("zh-Hans", "zh_CN", "(zh-Hans)", "正在重试模型请求（5/10）"),
            ("ja", "ja_JP", "(ja)", "モデルリクエストを再試行中（5/10）"),
        ]
        for (language, locale, languages, attempt) in languages {
            for size in ["default", "largest"] {
                for scheme in ["light", "dark"] {
                    let name = "\(language)-\(size)-\(scheme)"
                    let app = XCUIApplication()
                    let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
                    app.launchArguments = ["-pairURL", pair, "-openSession", widget, "-ui.scheme", scheme,
                                           "-AppleLanguages", languages, "-AppleLocale", locale]
                    if size == "largest" {
                        app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
                    }
                    app.launchPinned()
                    let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                    if board.alerts.firstMatch.waitForExistence(timeout: 2) {
                        for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: system alert
                            board.alerts.buttons[label].tap()
                            break
                        }
                    }
                    let note = app.descendants(matching: .any)["chat.model-retry"].firstMatch
                    XCTAssertTrue(note.waitForExistence(timeout: 25), "\(name): no retry note:\n\(app.debugDescription)")
                    let detailed = NSPredicate(format: "label CONTAINS %@ AND label CONTAINS %@", raw, attempt)
                    XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: detailed, object: note)], timeout: 20),
                                   .completed, "\(name): detailed 429 never replaced the generic retry")
                    XCTAssertTrue(note.label.contains(raw), "\(name): observed error missing: \(note.label)")
                    XCTAssertTrue(note.label.contains(attempt), "\(name): attempt missing or truncated: \(note.label)")
                    try shot(app, name)

                    let gone = NSPredicate(format: "exists == false")
                    XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: gone, object: note)], timeout: 20),
                                   .completed, "\(name): ordinary work did not clear the retry")
                    sleep(4) // this mock run ends before the next launch subscribes again
                    app.terminate()
                }
            }
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = "model-retry-\(name)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try screenshot.pngRepresentation.write(to: URL(fileURLWithPath: "\(directory)/apple-retry-\(name).png"))
    }
}
