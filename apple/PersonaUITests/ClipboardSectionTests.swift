import UIKit
import XCTest

/// Clipboard on a computer's page (#3064), against scripts/mock/mock_hub.py with MOCK_CLIPBOARD=on, or =off with
/// `TEST_RUNNER_PERSONA_CLIPBOARD_STATE=off` for the default-off state (the switch only, the offer setting off)
/// (`TEST_RUNNER_PERSONA_CLIPBOARD_HUB`): Get Clipboard puts MacBook's on the phone and previews it, Send Clipboard puts
/// the phone's on MacBook's, then the one-time paste hint comes; with the setting on, coming back after copying elsewhere
/// offers to send it. PERSONA_SHOTS keeps the screenshots, named by `TEST_RUNNER_PERSONA_LOOK`. The simulator's "Allow
/// Paste?" for what another app copied could not be answered here (its TCC store refused the answer), so Send sends what
/// the app copied itself.
final class ClipboardSectionTests: XCTestCase {
    private var env: [String: String] { ProcessInfo.processInfo.environment }

    @MainActor private func open(_ extra: [String] = []) throws -> XCUIApplication {
        guard let hub = env["PERSONA_CLIPBOARD_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_CLIPBOARD=on (PERSONA_CLIPBOARD_HUB)") }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=" + UITestToken.device
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openMachines", "YES", "-openMachineDetail", "macbook",
                               "-openMachineClipboard", "YES", "-clipboard.pasteHintDismissed", "NO"] + extra
        app.launchPinned()
        allowSystemAlert(["允许", "Allow"], wait: 4) // the notification question on a fresh simulator
        XCTAssertTrue(app.switches["clipboard-switch"].firstMatch.waitForExistence(timeout: 30), "no clipboard section:\n\(app.debugDescription)")
        return app
    }

    /// Get, then Send what Get put on the phone back, then the hint. Copied by this app, it is read without "Allow
    /// Paste?"; a prompt that does come (a device, something another app copied) is answered Allow.
    @MainActor func testGetThenSendThenTheHint() throws {
        guard env["PERSONA_CLIPBOARD_STATE"] != "off" else { throw XCTSkip("MOCK_CLIPBOARD=on") }
        let app = try open()
        app.buttons["clipboard-get"].firstMatch.tap()
        let preview = app.descendants(matching: .any)["clipboard-preview"].firstMatch
        XCTAssertTrue(preview.waitForExistence(timeout: 10), "no preview:\n\(app.debugDescription)")
        shot(app, "clipboard-fetched")
        let fetched = try hubClipboard()["text"] as? String
        XCTAssertNotNil(fetched)

        // Through the hub's clipboard: Send must change it to what the phone holds, so clear it first.
        try putHubClipboard("")
        app.buttons["clipboard-send"].firstMatch.tap()
        allowSystemAlert(["允许粘贴", "Allow Paste"], app: app, wait: 2)
        // The toast floats in a window of its own that takes no touches (out of XCUITest's sight): the hub's clipboard
        // says it was sent, the screenshots show the toast.
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline, try hubClipboard()["text"] as? String != fetched { usleep(300_000) }
        XCTAssertEqual(try hubClipboard()["text"] as? String, fetched)
        let hint = app.descendants(matching: .any)["clipboard-hint"].firstMatch
        XCTAssertTrue(hint.waitForExistence(timeout: 5), "the paste hint after the first allowed paste")
        shot(app, "clipboard-sent-hint")
        app.buttons["clipboard-hint-dismiss"].firstMatch.tap()
        XCTAssertTrue(hint.waitForNonExistence(timeout: 5))
    }

    /// An image fetched from the computer is a visible, accessible picture, not a blank success row (#7925).
    @MainActor func testGetImagePreview() throws {
        guard env["PERSONA_CLIPBOARD_STATE"] == "image" else { throw XCTSkip("MOCK_CLIPBOARD=image") }
        let app = try open()
        app.buttons["clipboard-get"].firstMatch.tap()
        let preview = app.descendants(matching: .any)["clipboard-preview"].firstMatch
        XCTAssertTrue(preview.waitForExistence(timeout: 10), "no preview:\n\(app.debugDescription)")
        XCTAssertTrue(preview.images.firstMatch.waitForExistence(timeout: 5), "the fetched picture is visible")
        shot(app, "clipboard-image-fetched")
    }

    /// The self-contained DEBUG screenshot path prepares its picture too (#7925).
    @MainActor func testDebugImagePreview() throws {
        guard env["PERSONA_CLIPBOARD_STATE"] != "off" else { throw XCTSkip("needs the clipboard switch on") }
        let app = try open(["-showClipboardFetched", "image"])
        let preview = app.descendants(matching: .any)["clipboard-preview"].firstMatch
        XCTAssertTrue(preview.waitForExistence(timeout: 10), "no DEBUG preview:\n\(app.debugDescription)")
        XCTAssertTrue(preview.images.firstMatch.waitForExistence(timeout: 5), "the DEBUG picture is not blank")
    }

    /// Off by default (MOCK_CLIPBOARD=off): the switch with its explanation and nothing to tap, and the offer setting off.
    @MainActor func testOffByDefault() throws {
        guard env["PERSONA_CLIPBOARD_STATE"] == "off" else { throw XCTSkip("MOCK_CLIPBOARD=off (PERSONA_CLIPBOARD_STATE=off)") }
        let app = try open()
        let toggle = app.switches["clipboard-switch"].firstMatch
        XCTAssertEqual(toggle.value as? String, "0")
        XCTAssertTrue(toggle.label.contains("默认关闭"), toggle.label)
        XCTAssertFalse(app.buttons["clipboard-send"].exists)
        XCTAssertFalse(app.buttons["clipboard-get"].exists)
        XCTAssertFalse(app.buttons["system-paste"].exists)
        shot(app, "clipboard-default-off")
        app.terminate()

        app.launchArguments = Array(app.launchArguments.prefix(2)) + ["-tab", "settings", "-openConnectorDevice", "YES"] // the same pairing
        app.launchPinned()
        let offer = app.switches["clipboard-offer"].firstMatch
        XCTAssertTrue(app.navigationBars.firstMatch.waitForExistence(timeout: 30))
        for _ in 0..<12 where !(offer.exists && offer.isHittable) { app.swipeUp() }
        XCTAssertTrue(offer.waitForExistence(timeout: 5))
        XCTAssertEqual(offer.value as? String, "0", "the offer is opt-in")
        shot(app, "clipboard-offer-default-off")
    }

    /// Settings › Phone Data › Clipboard, then back from another app after copying: the offer to send it (a toast in its
    /// own window, out of XCUITest's sight: the screenshot shows it).
    @MainActor func testOfferAfterCopyingElsewhere() throws {
        guard env["PERSONA_CLIPBOARD_STATE"] != "off" else { throw XCTSkip("MOCK_CLIPBOARD=on") }
        guard let hub = env["PERSONA_CLIPBOARD_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_CLIPBOARD=on (PERSONA_CLIPBOARD_HUB)") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=" + UITestToken.device
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openConnectorDevice", "YES", "-clipboard.offerOnReturn", "YES"]
        app.launchPinned()
        allowSystemAlert(["允许", "Allow"], wait: 4)
        XCTAssertTrue(app.navigationBars.firstMatch.waitForExistence(timeout: 30))
        let offer = app.switches["clipboard-offer"].firstMatch
        for _ in 0..<12 where !(offer.exists && offer.isHittable) { app.swipeUp() } // the list's last section, made when shown
        XCTAssertTrue(offer.waitForExistence(timeout: 5), "no offer switch:\n\(app.debugDescription)")
        XCTAssertEqual(offer.value as? String, "1")
        shot(app, "clipboard-offer-setting")

        XCUIDevice.shared.press(.home)
        sleep(1)
        UIPasteboard.general.string = "copied elsewhere" // only its change count is looked at: no prompt
        app.activate()
        sleep(2)
        shot(app, "clipboard-offer-toast")
    }

    /// The hub's clipboard of MacBook now (the mock answers for any device token).
    private func hubClipboard() throws -> [String: Any] { try call("GET", nil) }

    private func putHubClipboard(_ text: String) throws { _ = try call("PUT", ["text": text]) }

    private func call(_ method: String, _ body: [String: String]?) throws -> [String: Any] {
        let hub = try XCTUnwrap(env["PERSONA_CLIPBOARD_HUB"])
        var req = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/machines/macbook/clipboard")))
        req.httpMethod = method
        req.httpBody = try body.map { try JSONSerialization.data(withJSONObject: $0) }
        req.setValue("Bearer \(UITestToken.device)", forHTTPHeaderField: "Authorization")
        let done = expectation(description: "clipboard")
        var out: [String: Any] = [:]
        URLSession.shared.dataTask(with: req) { data, _, _ in
            out = (data.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return out
    }

    /// Taps the first of `labels` on a system alert (SpringBoard's, or the app's own paste prompt).
    @discardableResult @MainActor private func allowSystemAlert(_ labels: [String], app: XCUIApplication? = nil, wait: TimeInterval) -> Bool {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let deadline = Date.now.addingTimeInterval(wait)
        while Date.now < deadline {
            for source in [app, springboard].compactMap({ $0 }) {
                let button = source.buttons.matching(NSPredicate(format: "label IN %@", labels)).firstMatch
                if button.exists { button.tap(); return true }
            }
            usleep(250_000)
        }
        return false
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let look = env["PERSONA_LOOK"].map { "-" + $0 } ?? ""
        let full = "\(name)-\(UITestStyle.name)\(look)"
        let png = app.screenshot()
        let attachment = XCTAttachment(screenshot: png)
        attachment.name = full
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = env["PERSONA_SHOTS"] {
            try? png.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(full + ".png"))
        }
    }
}
