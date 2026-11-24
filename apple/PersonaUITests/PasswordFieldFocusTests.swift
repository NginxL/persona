import XCTest

/// A card's password field takes the keyboard: a tap on it, before anything is typed, leaves the app in the foreground
/// with the on-screen keyboard up (#1580). Covers the password card (secret_run) in Persona's conversation and the
/// generic fields card (`PermissionInputForm`) in an agent's chat, using the supported compact Shell. The password
/// card then shows its field and Run above the keyboard, without scrolling inside the card first (#1581).
/// Needs the on-screen keyboard: a simulator a test has typed into with `typeText` shows none (docs/lessons/testing.md),
/// so run this on a fresh stock simulator (`scripts/app/sim-boot.sh --profile stock`), before tests that type.
///   PORT=18938 MOCK_SECRET_RUN=pending MOCK_SETTINGS_INPUT=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_SECRET_RUN_HUB=http://127.0.0.1:18938 xcodebuild test … -only-testing:PersonaUITests/PasswordFieldFocusTests
/// Screenshots of the focused card: `xcrun simctl io <udid> screenshot` while the keyboard is up (an XCUITest screenshot
/// leaves the keyboard's place blank on the iOS 27.0 simulator).
final class PasswordFieldFocusTests: XCTestCase {
    /// The mock's main Persona conversation (the password card) and the agent chat with the fields card.
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let review = "macbook/a1f3c2d4-0000-4000-8000-000000000006"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_SECRET_RUN_HUB"] != nil else {
            throw XCTSkip("needs MOCK_SECRET_RUN=pending MOCK_SETTINGS_INPUT=1 scripts/mock/mock_hub.py (PERSONA_SECRET_RUN_HUB)")
        }
    }

    @MainActor func testPasswordCardFieldTakesTheKeyboard() {
        focus(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main], run: "secret-run.run")
    }

    @MainActor func testFieldsCardFieldTakesTheKeyboard() {
        focus(["-tab", "dev", "-openSession", review])
    }

    /// Opens the card's page, taps its password field and checks the app is still up with the keyboard showing and the
    /// cards settled; with `run`, that button and the field can be tapped as they are.
    @MainActor private func focus(_ page: [String], run: String? = nil) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_SECRET_RUN_HUB"]!
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let app = XCUIApplication()
        // Paired once on its own first: pairing with another hub than the last run's starts over on Persona's page and
        // drops the page arguments (docs/lessons/testing.md).
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        app.terminate()
        app.launchArguments = ["-pairURL", pair] + page
        app.launchPinned()
        LiveActivityShots.allowNotifications()

        let field = app.secureTextFields["interaction.field.password"]
        XCTAssertTrue(field.waitForExistence(timeout: 20), "no password field:\n\(app.debugDescription)")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "a keyboard is up before the tap")
        field.tap()
        // The keyboard slides up and the cards refit themselves to it: wait for both, then check the app came through.
        _ = app.keyboards.firstMatch.waitForExistence(timeout: 5)
        sleep(2)
        XCTAssertEqual(app.state, .runningForeground, "the app left the foreground when the password field took focus")
        XCTAssertTrue(app.keyboards.count > 0, "no on-screen keyboard after tapping the password field:\n\(app.debugDescription)")
        XCTAssertTrue(field.exists, "the password field is gone with the keyboard up:\n\(app.debugDescription)")
        // Settled: a cap that keeps refitting itself redraws the page every frame and leaves the app busy, yet still in
        // the foreground with the keyboard up; its cards' area never holds one frame.
        // The assistant's pager (#12831) holds its cards; an Agent's chat keeps the stack.
        let dock = app.otherElements["question-dock"].firstMatch
        let cards = dock.exists ? dock : app.scrollViews["pending-cards"]
        let settled = cards.frame
        for _ in 0..<5 {
            Thread.sleep(forTimeInterval: 0.2)
            XCTAssertEqual(cards.frame, settled, "the cards' area keeps resizing with the keyboard up")
        }
        if let run {
            let button = app.buttons[run]
            XCTAssertTrue(button.exists && button.isHittable, "\(run) is out of sight with the keyboard up:\n\(app.debugDescription)")
            XCTAssertTrue(field.isHittable, "the password field is out of sight with the keyboard up")
        }
    }
}
