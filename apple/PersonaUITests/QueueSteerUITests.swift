import XCTest

/// Steering a Claude's queue (#6088): a queued message moves down a place, then one tap on Send Now gives the agent the
/// whole queue, with no confirmation (#7169), and the strip goes. Against scripts/mock/mock_hub.py with MOCK_QUEUE=1
/// (PERSONA_MOCK_HUB): the widget agent, a Claude in a herdr pane, has two messages queued.
final class QueueSteerUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_QUEUE=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    /// The sessions the mock got a `…/queue/send-now` for, in order (`GET /mock/send_nows`).
    private func sendNows() throws -> [String] {
        struct Log: Decodable { var send_nows: [String] }
        return try JSONDecoder().decode(Log.self, from: Data(contentsOf: URL(string: hub + "/mock/send_nows")!)).send_nows
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "/tmp/persona-steer-\(name).png"))
    }

    @MainActor func testAQueuedMessageMovesDownThenTheQueueIsSentNow() throws {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001"]
        app.launchPinned()

        let sendNow = app.buttons["queue-send-now"]
        XCTAssertTrue(sendNow.waitForExistence(timeout: 20), "no Send Now on the queue strip:\n\(app.debugDescription)")
        shot(app, "strip")

        // Open the strip, then move the first message down: the hub re-queues them in the new order.
        // The strip's id lands on its disclosure button or on a group around it, depending on the OS.
        let strip = app.descendants(matching: .any).matching(identifier: "queue-strip").firstMatch
        if strip.elementType == .button { strip.tap() } else { strip.buttons.firstMatch.tap() }
        let first = app.buttons.matching(identifier: "queue-item").firstMatch
        XCTAssertTrue(first.waitForExistence(timeout: 5), "the strip did not open:\n\(app.debugDescription)")
        XCTAssertTrue(first.label.contains("README"), first.label)
        first.tap()
        let down = app.buttons["queue-move-down"]
        XCTAssertTrue(down.waitForExistence(timeout: 5), "no Move Down in the message's menu:\n\(app.debugDescription)")
        shot(app, "menu")
        down.tap()
        let moved = NSPredicate { _, _ in !first.label.contains("README") }
        wait(for: [XCTNSPredicateExpectation(predicate: moved, object: nil)], timeout: 10)
        XCTAssertTrue(app.buttons.matching(identifier: "queue-item").element(boundBy: 1).label.contains("README"))

        // One tap sends, with nothing to confirm: one request to the hub, and the queue goes to the agent and the strip
        // with it.
        let before = try sendNows().count
        sendNow.tap()
        XCTAssertTrue(strip.waitForNonExistence(timeout: 10), "one tap on Send Now did not send the queue:\n\(app.debugDescription)")
        XCTAssertEqual(Array(try sendNows().dropFirst(before)), ["macbook/a1f3c2d4-0000-4000-8000-000000000001"], "one tap, one send-now")
        XCTAssertFalse(app.sheets.firstMatch.exists, "Send Now asked first:\n\(app.debugDescription)")
        shot(app, "sent")
    }
}
