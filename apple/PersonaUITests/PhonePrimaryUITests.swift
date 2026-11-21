import XCTest

/// Settings › Devices › Primary Device (#12019) against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB): the row with the
/// hub's choice, the menu (Automatic and the phones), a choice reaching the hub, and a refused choice put back. A mock
/// without the feature (MOCK_PHONE=none) or with one phone (MOCK_PHONE=single) must show no row. With PERSONA_SHOTS set
/// it writes what it saw there (PERSONA_SHOTS_APPEARANCE names the appearance, PERSONA_SHOTS_DEVICE the device).
/// Start the mock fresh for each run: it keeps the choice. `PERSONA_PHONE_MODE` names how the mock was started
/// (`default`, `none`, `single`, `refuse`).
final class PhonePrimaryUITests: XCTestCase {
    private var hub: String!
    private var mode: String { ProcessInfo.processInfo.environment["PERSONA_PHONE_MODE"] ?? "default" }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let value = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py") }
        hub = value
    }

    /// The hub's own answer to GET /api/v1/auth/tokens: its `phone.primary`, nil without the object.
    @MainActor private func hubPrimary() async throws -> String? {
        var r = URLRequest(url: try XCTUnwrap(URL(string: hub + "/api/v1/auth/tokens")))
        r.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let data = try await URLSession.shared.data(for: r).0
        let object = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        return (object["phone"] as? [String: Any])?["primary"] as? String
    }

    @MainActor private func hubHas(_ value: String, file: StaticString = #filePath, line: UInt = #line) async throws {
        var now = try await hubPrimary()
        for _ in 0..<40 where now != value {
            try await Task.sleep(for: .milliseconds(250))
            now = try await hubPrimary()
        }
        XCTAssertEqual(now, value, "the hub's primary device", file: file, line: line)
    }

    @MainActor private func openDevices() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "settings", "-openDevices", "YES"]
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let image = app.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? "iphone"
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try image.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/phoneprimary-\(device)-\(appearance)-\(name).png"))
    }

    /// The Automatic entry in whichever of the three languages the run is in.
    private func automatic(_ app: XCUIApplication) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label IN %@", ["Automatic", "自动", "自動"])).firstMatch
    }

    private func shows(_ row: XCUIElement, _ text: String) -> Bool {
        row.label.contains(text) || (row.value as? String)?.contains(text) == true
    }

    private func picker(_ app: XCUIApplication) -> XCUIElement { app.descendants(matching: .any)["devices.phonePrimary"].firstMatch }

    /// Scrolls until the row is on screen.
    @MainActor private func reveal(_ e: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<8 where !(e.exists && e.isHittable) {
            let from = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
            let to = app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4))
            from.press(forDuration: 0.05, thenDragTo: to)
        }
    }

    /// With the feature on and two phones, the row is there, its menu lists Automatic and the phones, a choice reaches
    /// the hub (and Automatic takes it back); a refused choice shows the old value again.
    @MainActor func testChoosingThePrimaryDevice() async throws {
        guard mode == "default" || mode == "refuse" else { throw XCTSkip("needs a mock hub with two phones") }
        let app = openDevices()
        let row = picker(app)
        XCTAssertTrue(app.staticTexts["Devices"].firstMatch.waitForExistence(timeout: 20) || row.waitForExistence(timeout: 20))
        XCTAssertTrue(row.waitForExistence(timeout: 20), "no Primary Device row:\n\(app.debugDescription)")
        reveal(row, in: app)
        try shot(app, "1-closed")
        row.tap()
        let work = app.buttons["工作 iPhone"].firstMatch
        XCTAssertTrue(work.waitForExistence(timeout: 10), "the menu lacks the work phone:\n\(app.debugDescription)")
        XCTAssertTrue(automatic(app).exists, "the menu lacks Automatic")
        try shot(app, "2-menu")
        work.tap()
        if mode == "refuse" {
            // The hub refuses: the row goes back to what it showed before.
            try await Task.sleep(for: .seconds(2))
            let kept = try await hubPrimary()
            XCTAssertEqual(kept, "")
            XCTAssertFalse(shows(row, "工作 iPhone"), "the old choice is back: \(row.debugDescription)")
            try shot(app, "4-refused")
            return
        }
        try await hubHas("dev_work")
        XCTAssertTrue(shows(row, "工作 iPhone"), "the row shows the choice: \(row.debugDescription)")
        try shot(app, "3-chosen")
        row.tap()
        automatic(app).tap()
        try await hubHas("")
    }

    /// An older hub and one phone: no row, and the page is the one it was.
    @MainActor func testNoRowWithoutAChoice() async throws {
        guard mode == "none" || mode == "single" else { throw XCTSkip("needs MOCK_PHONE=none or single") }
        let app = openDevices()
        XCTAssertTrue(app.buttons["devices.connect"].firstMatch.waitForExistence(timeout: 20), "the page did not open")
        try await Task.sleep(for: .seconds(2))
        XCTAssertFalse(picker(app).exists, "a Primary Device row without anything to choose")
        try shot(app, "no-row-\(mode)")
    }
}
