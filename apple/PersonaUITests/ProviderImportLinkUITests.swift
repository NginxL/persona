import XCTest

/// Opening a provider import link (#5428): `persona://provider/import` reads the draft through the hub and opens the
/// wizard pre-filled, in light and dark; nothing is saved before Save, and a refused link shows the hub's plain-words note.
final class ProviderImportLinkUITests: XCTestCase {
    override func setUpWithError() throws { continueAfterFailure = true }

    private var hub: String { ProcessInfo.processInfo.environment["PERSONA_RUN_HUB"] ?? "http://127.0.0.1:18760" }
    private var pairLink: String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    private func link(_ payload: String, version: Int = 1) -> URL {
        let data = Data(payload.utf8).base64EncodedString()
            .replacingOccurrences(of: "+", with: "-").replacingOccurrences(of: "/", with: "_").replacingOccurrences(of: "=", with: "")
        return URL(string: "persona://provider/import?v=\(version)&data=\(data)")!
    }

    @MainActor private func launch(_ scheme: String, largest: Bool = false) -> XCUIApplication {
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pairLink, "-openChatsFresh", "YES", "-ui.scheme", scheme]
        if largest { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchPinned()
        sleep(8) // pairing and the hub's first answer settle before the external link arrives
        dismissPermissionPrompt(in: app)
        return app
    }

    /// A fresh stock simulator asks for notifications; its SpringBoard alert would cover the wizard and our own refusal.
    @MainActor private func dismissPermissionPrompt(in app: XCUIApplication) {
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["不允许", "Don’t Allow", "Don't Allow"] { // i18n-ignore: the system's own buttons
            let button = board.alerts.buttons[label]
            if button.waitForExistence(timeout: 1) { button.tap(); break }
        }
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 3), "a system alert would cover the provider flow")
    }

    @MainActor private func open(_ url: URL) {
        // Deliver an external link to the running app, preserving an in-memory import pending through pairing.
        XCUIDevice.shared.system.open(url)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["打开", "Open"] { // i18n-ignore: the system's own dialog
            let b = springboard.buttons[label].firstMatch
            if b.waitForExistence(timeout: 4) { b.tap(); break }
        }
    }

    private func launcherNames() -> Set<String> {
        guard let data = try? Data(contentsOf: URL(string: hub + "/api/v1/launchers")!),
              let answer = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let launchers = answer["launchers"] as? [[String: Any]] else { return [] }
        return Set(launchers.compactMap { $0["name"] as? String })
    }

    @MainActor func testImportLinkOpensWizardPrefilled() throws {
        let payload = #"{"kind":"custom","name":"Example Gateway","endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1","models":["m1","m2"]}]}"#
        for (scheme, largest) in [("light", false), ("dark", false), ("light", true), ("dark", true)] {
            let before = launcherNames()
            XCTAssertFalse(before.isEmpty, "the mock's providers are readable")
            let app = launch(scheme, largest: largest)
            open(link(payload))
            // The wizard is pre-filled with the draft: its name and base URL sit in fields.
            let base = app.textFields.matching(NSPredicate(format: "value CONTAINS[c] %@", "example.invalid")).firstMatch
            XCTAssertTrue(base.waitForExistence(timeout: 10), "the wizard is pre-filled with the draft's base URL:\n\(app.debugDescription)")
            XCTAssertEqual(app.textFields["wizard.name"].value as? String, "example-gateway")
            sleep(1)
            let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] ?? "/tmp"
            let suffix = scheme + (largest ? "-ax" : "")
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("persona-import-wizard-\(suffix).png"))
            XCTAssertEqual(launcherNames(), before, "opening and reviewing the link saves nothing")
            app.terminate()
        }
    }

    @MainActor func testImportLinkArrivingBeforePairingOpensAfterConnect() throws {
        let payload = #"{"kind":"custom","name":"Example Gateway","endpoints":[{"protocol":"openai-chat","baseUrl":"https://example.invalid/v1","models":["m1"]}]}"#
        let before = launcherNames()
        let app = XCUIApplication()
        app.launchArguments = ["-openWelcome", "YES", "-openChatsFresh", "YES", "-ui.scheme", "light"]
        app.launchPinned()
        dismissPermissionPrompt(in: app)
        // No paired API exists yet: the import must stay pending through the user's explicit Connect.
        open(link(payload))
        open(URL(string: pairLink)!)
        // The native alert on iOS 27 can expose only the visible title of its buttons.
        let confirm = app.alerts.buttons.matching(NSPredicate(format: "identifier == %@ OR label IN %@",
                                                               "pair.confirm", ["Connect", "连接", "接続"])).firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), "the pairing link asks before connecting:\n\(app.debugDescription)")
        confirm.tap()
        // A freshly paired hub can require confirmation of its suggested engine before showing the pending route.
        let engine = app.buttons["engine.save"]
        if engine.waitForExistence(timeout: 8) { engine.tap() }
        let base = app.textFields.matching(NSPredicate(format: "value CONTAINS[c] %@", "example.invalid")).firstMatch
        XCTAssertTrue(base.waitForExistence(timeout: 20), "the pending import opens after pairing:\n\(app.debugDescription)")
        XCTAssertEqual(launcherNames(), before, "the delayed review still saves nothing")
        app.terminate()
    }

    @MainActor func testRefusedImportLinkShowsPlainNote() throws {
        let app = launch("light")
        open(link(#"{"kind":"custom","name":"Example Gateway","endpoints":[]}"#, version: 2))
        let alert = app.alerts.firstMatch
        XCTAssertTrue(alert.waitForExistence(timeout: 10), "a refused link says why:\n\(app.debugDescription)")
        let words = alert.staticTexts.allElementsBoundByIndex.map(\.label)
        XCTAssertFalse(words.isEmpty)
        XCTAssertFalse(words.contains { $0.contains("persona://") || $0.contains("data=") }, "the refusal never echoes the key-bearing link")
        app.terminate()
    }
}
