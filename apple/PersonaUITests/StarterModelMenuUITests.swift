import XCTest

/// A new Persona conversation's model menu (#11472, `StarterModesMenu`): with nothing picked the button names Main chat's
/// default model, the launchers each list their models a level down, a model picked there names the button, and Follow
/// Main Chat default goes back. Light and dark. Runs on a mock hub that reports Main chat's default:
///   MOCK_MODEL_DEFAULTS=1 PORT=18932 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18932 TEST_RUNNER_PERSONA_SHOTS=<dir> xcodebuild test …
///     -only-testing:PersonaUITests/StarterModelMenuUITests
/// The test sets Main chat to the mock's `claude` launcher with `sonnet` (the launcher's own model is `opus`), so the
/// default and the launcher's own model differ. It runs in zh-Hans.
final class StarterModelMenuUITests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB) with MOCK_MODEL_DEFAULTS=1")
        }
        hub = h
        continueAfterFailure = true // both looks are shot even when the first misses something
    }

    @MainActor func testTheStartersMenuPicksALaunchersModel() throws {
        try setMainChat(model: "sonnet")
        for scheme in ["light", "dark"] {
            open(scheme: scheme)
            let button = app.descendants(matching: .any)["shell-composer-modes"].firstMatch
            XCTAssertTrue(button.waitForExistence(timeout: 20), "no composer model menu:\n\(app.debugDescription)")
            XCTAssertTrue(wait(for: button, titleHas: "Sonnet"), "\(scheme): the button names Main chat's model, not the launcher's own: \(title(button))")
            shot("\(scheme)-button")

            button.tap()
            let claude = row(beginningWith: "claude · Sonnet") // the launcher it starts on says its model
            XCTAssertTrue(claude.waitForExistence(timeout: 10), "\(scheme): no claude launcher row:\n\(app.debugDescription)")
            sleep(1) // the menu has finished opening
            shot("\(scheme)-launchers")

            claude.tap()
            let fable = row(beginningWith: "Fable")
            XCTAssertTrue(fable.waitForExistence(timeout: 10), "\(scheme): claude's models a level down:\n\(app.debugDescription)")
            XCTAssertTrue(row(beginningWith: "Sonnet 5.5").exists, "\(scheme): Main chat's model is one of the rows")
            sleep(1)
            shot("\(scheme)-models")
            // The rows nearest the button, the launcher's Default and Fable: the menu opens upward with its first rows at
            // the bottom, so a drag up on a row scrolls them into view.
            let grip = row(beginningWith: "Haiku 5.5")
            grip.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: grip.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: -4)))
            sleep(1)
            shot("\(scheme)-models-default")

            fable.tap()
            XCTAssertTrue(wait(for: button, titleHas: "Fable"), "\(scheme): the picked model names the button: \(title(button))")
            shot("\(scheme)-picked")

            button.tap()
            let back = row(beginningWith: "跟随主对话默认") // i18n-ignore: zh-Hans "Follow Main Chat default"
            XCTAssertTrue(back.waitForExistence(timeout: 10), "\(scheme): a pick offers the way back:\n\(app.debugDescription)")
            sleep(1)
            shot("\(scheme)-picked-menu")
            back.tap()
            XCTAssertTrue(wait(for: button, titleHas: "Sonnet"), "\(scheme): back on Main chat's default: \(title(button))")
        }
    }

    /// Main chat's model in the mock (`PATCH …/model-config/purposes/assistant`), at its current revision.
    private func setMainChat(model: String) throws {
        let purpose = URL(string: hub + "/api/v1/model-config/purposes/assistant")!
        let read = try call(URLRequest(url: purpose))
        let revision = ((read["purpose"] as? [String: Any])?["revision"] as? Int) ?? 0
        var patch = URLRequest(url: purpose)
        patch.httpMethod = "PATCH"
        patch.setValue("application/json", forHTTPHeaderField: "Content-Type")
        patch.httpBody = try JSONSerialization.data(withJSONObject: ["revision": revision, "provider": "launcher.claude", "model": model])
        let saved = try call(patch)
        let effective = (saved["purpose"] as? [String: Any])?["effective"] as? [String: Any]
        XCTAssertEqual(effective?["model"] as? String, model, "the mock took Main chat's model: \(saved)")
    }

    private func call(_ request: URLRequest) throws -> [String: Any] {
        var request = request
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let done = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var body: Data?
        URLSession.shared.dataTask(with: request) { data, _, _ in
            body = data
            done.signal()
        }.resume()
        _ = done.wait(timeout: .now() + 10)
        return try XCTUnwrap(body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }, "no answer from \(request.url!)")
    }

    /// The new-conversation page with the keyboard in its field: the shell composer shows its model menu only while open.
    @MainActor private func open(scheme: String) {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", scheme, "-tab", "assistant", "-app.tab", "assistant", "-newAssistant", "YES"]
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        HomeScreen.dismissIconNote()
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no composer:\n\(app.debugDescription)")
        field.tapCenter()
    }

    /// A row of the open menu by the start of its label (a submenu's or a switch's).
    @MainActor private func row(beginningWith text: String) -> XCUIElement {
        app.collectionViews.descendants(matching: .any).matching(NSPredicate(format: "label BEGINSWITH %@", text)).firstMatch
    }

    /// The model button's title: its label is "Model and thinking effort", the title its value.
    @MainActor private func title(_ button: XCUIElement) -> String { button.value as? String ?? "" }

    @MainActor private func wait(for button: XCUIElement, titleHas text: String) -> Bool {
        let has = NSPredicate(format: "value CONTAINS %@", text)
        return XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: has, object: button)], timeout: 10) == .completed
    }

    @MainActor private func shot(_ name: String) {
        let device = UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone"
        let screen = XCUIScreen.main.screenshot()
        let a = XCTAttachment(screenshot: screen)
        a.name = "\(device)-\(name)"
        a.lifetime = .keepAlways
        add(a)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? screen.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/starter-model-\(device)-\(name).png"))
    }
}
