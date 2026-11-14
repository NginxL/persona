import XCTest

/// The queue strip over an agent chat's composer at the largest accessibility text size (#4846): the title has the row's
/// width and a tap on it opens the strip without sending; Send Now sits on the line under it and sends at one tap
/// (#7169). At the default size the two share one line, as before. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB): the widget agent
/// works, a Claude in a terminal, so its queue has Send Now; `/mock/queue?text=…` queues a message for it.
/// Each test runs in light and dark (`-ui.scheme`); PERSONA_SHOTS=<dir> keeps each step.
final class QueueStripTextSizeUITests: XCTestCase {
    private var hub = ""
    private let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        continueAfterFailure = true
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    /// One message queued for the widget agent, afresh for each look (Send Now takes it).
    private func queueOne() {
        _ = mock("queue?clear=1")
        _ = mock("queue?text=" + "Check the widget in dark mode when you are done".addingPercentEncoding(withAllowedCharacters: .alphanumerics)!)
    }

    /// How many `…/queue/send-now` the mock has had (`GET /mock/send_nows`).
    private func sendNowCount() -> Int {
        struct Log: Decodable { var send_nows: [String] }
        return mock("send_nows").flatMap { try? JSONDecoder().decode(Log.self, from: $0) }?.send_nows.count ?? -1
    }

    override func tearDownWithError() throws {
        _ = mock("queue?clear=1")
    }

    @MainActor func testTheTitleOpensTheStripAtTheLargestTextSize() throws {
        for look in ["light", "dark"] {
            queueOne()
            let before = sendNowCount()
            let app = launch("UICTContentSizeCategoryAccessibilityXXXL", look)
            let (strip, title, send) = parts(app)
            shot(app, "xxxl-queued-\(look)")
            XCTAssertGreaterThan(title.frame.width, strip.frame.width / 2, "\(look): the title has no room: \(title.frame) in \(strip.frame)")
            XCTAssertGreaterThanOrEqual(send.frame.minY, title.frame.maxY - 1, "\(look): Send Now is beside the title, not under it: \(send.frame) and \(title.frame)")

            title.tap()
            XCTAssertTrue(app.buttons["queue-item"].firstMatch.waitForExistence(timeout: 5), "\(look): a tap on the title did not open the strip:\n\(app.debugDescription)")
            XCTAssertEqual(sendNowCount(), before, "\(look): a tap on the title sent the queue")
            shot(app, "xxxl-open-\(look)")

            send.tap()
            XCTAssertTrue(strip.waitForNonExistence(timeout: 10), "\(look): one tap on Send Now did not send the queue:\n\(app.debugDescription)")
            XCTAssertEqual(sendNowCount(), before + 1, "\(look): one tap, one send-now")
            shot(app, "xxxl-sent-\(look)")
            app.terminate()
        }
    }

    @MainActor func testTheTitleAndSendNowShareALineAtTheDefaultSize() throws {
        for look in ["light", "dark"] {
            queueOne()
            let app = launch("UICTContentSizeCategoryL", look)
            let (_, title, send) = parts(app)
            shot(app, "default-queued-\(look)")
            XCTAssertEqual(send.frame.midY, title.frame.midY, accuracy: title.frame.height / 2, "\(look): Send Now left the title's line: \(send.frame) and \(title.frame)")

            title.tap()
            XCTAssertTrue(app.buttons["queue-item"].firstMatch.waitForExistence(timeout: 5), "\(look): a tap on the title did not open the strip:\n\(app.debugDescription)")
            shot(app, "default-open-\(look)")
            app.terminate()
        }
    }

    // MARK: - Steps

    /// The widget agent's chat at the text size `size` (a UIContentSizeCategory name), in `look` (light or dark).
    @MainActor private func launch(_ size: String, _ look: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", widget, "-UIPreferredContentSizeCategoryName", size, "-ui.scheme", look]
        app.launchPinned()
        dismissIconNotice(app)
        return app
    }

    /// The app's icon follows its Light & Dark, so switching the look switches the icon, and the system says so in an
    /// alert over the chat (as `ChatBottomGapTests` finds): tap it away, if it comes within a few seconds.
    @MainActor private func dismissIconNotice(_ app: XCUIApplication) {
        let notice = NSPredicate(format: "label CONTAINS[c] 'icon' OR label CONTAINS '图标'")
        let deadline = Date().addingTimeInterval(5)
        while Date() < deadline {
            for source in [app, XCUIApplication(bundleIdentifier: "com.apple.springboard")] {
                let alert = source.alerts.containing(notice).firstMatch
                if alert.exists, alert.buttons.firstMatch.exists {
                    alert.buttons.firstMatch.tap()
                    _ = alert.waitForNonExistence(timeout: 3)
                    return
                }
            }
            Thread.sleep(forTimeInterval: 0.3)
        }
    }

    /// The strip, its title and its Send Now.
    @MainActor private func parts(_ app: XCUIApplication) -> (XCUIElement, XCUIElement, XCUIElement) {
        let strip = app.descendants(matching: .any)["queue-strip"].firstMatch
        XCTAssertTrue(strip.waitForExistence(timeout: 30), "no queue strip:\n\(app.debugDescription)")
        let title = strip.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "Claude")).firstMatch
        let send = app.buttons["queue-send-now"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 5), "no title in the strip:\n\(app.debugDescription)")
        XCTAssertTrue(send.waitForExistence(timeout: 5), "no Send Now in the strip:\n\(app.debugDescription)")
        return (strip, title, send)
    }

    // MARK: - Mock

    private func mock(_ path: String) -> Data? {
        guard let url = URL(string: hub + "/mock/" + path) else { return nil }
        return try? Data(contentsOf: url) // the mock on this Mac answers at once
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "queue-strip-\(name).png"))
    }
}
