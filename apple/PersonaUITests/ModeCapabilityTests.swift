import XCTest

/// Mode chips follow the hub's optional capabilities.modes flag (#1843), not a guess from the agent name.
/// Run with MOCK_SOURCES=1 MOCK_MODE_CAPS=1 and TEST_RUNNER_PERSONA_MOCK_HUB.
final class ModeCapabilityTests: XCTestCase {
    private var hub = ""
    private let desktop = "server/c1a0de00-1111-4000-8000-000000000004"
    private let agy = "server/a6700000-1111-4000-8000-000000000006"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    @MainActor private func open(_ session: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", session]
        app.launchPinned()
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no composer:\n\(app.debugDescription)")
        field.tapCenter()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "no keyboard")
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(directory)/mode-capability-\(appearance)-\(name).png"))
    }

    @MainActor func testDesktopClaudeDoesNotOfferModesItCannotApply() throws {
        let app = open(desktop)
        defer { app.terminate() }
        try shot(app, "desktop-open")
        let menu = app.buttons["shell-composer-modes"].firstMatch
        if menu.waitForExistence(timeout: 5) { menu.tap() }
        let permissionMode = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "权限模式")).firstMatch
        XCTAssertFalse(permissionMode.waitForExistence(timeout: 5), "a desktop Claude with modes:false must not offer a change that always fails")
        try shot(app, "desktop")
    }

    @MainActor func testRuntimeWithModesOffersThem() throws {
        let app = open(agy)
        defer { app.terminate() }
        try shot(app, "agy-open")
        let menu = app.buttons["shell-composer-modes"].firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 20), "an agy runtime with modes:true must offer its modes:\n\(app.debugDescription)")
        menu.tap()
        try shot(app, "agy-menu")
        let plan = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "计划模式")).firstMatch
        XCTAssertTrue(plan.waitForExistence(timeout: 5), "the runtime's permission modes are missing:\n\(app.debugDescription)")
        try shot(app, "agy")
    }
}
