import XCTest

/// A question card the app can't answer says why under it (#6868), instead of only greying out: a Codex thread on a shared
/// app-server not joined yet offers the ⋯ menu's Attach Live there, and attaching makes the card answerable; a subagent
/// its parent drives says to answer on the computer; a card the app can answer looks as it always did. Against
/// scripts/mock/mock_hub.py with MOCK_CANT_ANSWER=1 (TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port>); each test
/// starts from /mock/reset (the thread not joined). TEST_RUNNER_PERSONA_SHOTS=<dir> also saves the screens there as PNG,
/// in TEST_RUNNER_PERSONA_SHOTS_APPEARANCE (light by default).
final class CantAnswerCardTests: XCTestCase {
    private var hub = ""
    private let live = "server/c0de6868-1111-4000-8000-000000000001"
    private let subagent = "server/c0de6868-2222-4000-8000-000000000002"
    /// The default fixture's question on a terminal Agent: one the app answers.
    private let answerable = "macbook/a1f3c2d4-0000-4000-8000-000000000002"
    private var appearance: String { ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] ?? "light" }

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_CANT_ANSWER=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = false
    }

    /// The chat, at the text size `size` (a UIContentSizeCategory name).
    @MainActor private func open(_ session: String, size: String = "UICTContentSizeCategoryL") -> XCUIApplication {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", session, "-UIPreferredContentSizeCategoryName", size,
                               "-ui.scheme", appearance]
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let png = app.screenshot()
        let attachment = XCTAttachment(screenshot: png)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try png.pngRepresentation.write(to: URL(fileURLWithPath: "\(directory)/6868-\(appearance)-\(name).png"))
    }

    private func card(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)["chat.card." + id].firstMatch
    }

    private func option(_ card: XCUIElement, _ label: String) -> XCUIElement {
        card.buttons.matching(NSPredicate(format: "label CONTAINS %@", label)).firstMatch
    }

    private func line(_ app: XCUIApplication) -> XCUIElement { app.descendants(matching: .any)["chat.card.blocked"].firstMatch }

    private func lineText(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", text)).firstMatch
    }

    /// The reported card: greyed, it says attaching answers it here and offers Attach Live, which makes it answerable.
    @MainActor func testAttachLiveLetsTheCardBeAnswered() throws {
        let app = open(live)
        defer { app.terminate() }
        let card = card(app, "cxq_deploy_target")
        XCTAssertTrue(card.waitForExistence(timeout: 20), "no card:\n\(app.debugDescription)")
        let staging = option(card, "staging")
        XCTAssertTrue(staging.waitForExistence(timeout: 5), "no option:\n\(app.debugDescription)")
        XCTAssertFalse(staging.isEnabled, "the session can't approve: the card is greyed")
        XCTAssertTrue(line(app).waitForExistence(timeout: 5), "nothing says why the card is greyed:\n\(app.debugDescription)")
        XCTAssertTrue(lineText(app, "实时接入后就能在这里作答").exists, "the line says attaching answers it here")
        let attach = app.buttons["chat.card.attach"].firstMatch
        XCTAssertTrue(attach.exists && attach.isEnabled, "no Attach Live under the card")
        XCTAssertEqual(attach.label, "实时接入", "the ⋯ menu's label")
        XCTAssertGreaterThanOrEqual(attach.frame.height, 44, "a finger's height")
        try shot(app, "attach")

        attach.tap()
        let gone = NSPredicate(format: "exists == false")
        expectation(for: gone, evaluatedWith: line(app))
        waitForExpectations(timeout: 10)
        let enabled = NSPredicate(format: "isEnabled == true")
        expectation(for: enabled, evaluatedWith: option(card, "staging"))
        waitForExpectations(timeout: 10)
        try shot(app, "attached")
    }

    /// At the largest text size (the interface stops at xxxLarge) the words wrap beside the button, both in reach.
    @MainActor func testTheLineAtTheLargestTextSize() throws {
        let app = open(live, size: "UICTContentSizeCategoryAccessibilityXXXL")
        defer { app.terminate() }
        XCTAssertTrue(card(app, "cxq_deploy_target").waitForExistence(timeout: 20), "no card:\n\(app.debugDescription)")
        XCTAssertTrue(line(app).waitForExistence(timeout: 5), "nothing says why the card is greyed:\n\(app.debugDescription)")
        let attach = app.buttons["chat.card.attach"].firstMatch
        XCTAssertTrue(attach.exists && attach.isHittable, "Attach Live out of reach:\n\(app.debugDescription)")
        let text = lineText(app, "实时接入后就能在这里作答")
        XCTAssertTrue(text.isHittable, "the line out of view")
        XCTAssertLessThanOrEqual(text.frame.maxX, attach.frame.minX + 1, "the words don't run under the button")
        try shot(app, "attach-xxxl")
    }

    /// A subagent its parent drives: nothing to attach, the computer answers it.
    @MainActor func testASubagentsCardSaysToAnswerOnTheComputer() throws {
        let app = open(subagent)
        defer { app.terminate() }
        let card = card(app, "cxq_staging_vars")
        XCTAssertTrue(card.waitForExistence(timeout: 20), "no card:\n\(app.debugDescription)")
        XCTAssertFalse(option(card, "按生产的值").isEnabled, "the session can't approve: the card is greyed")
        XCTAssertTrue(line(app).waitForExistence(timeout: 5), "nothing says why the card is greyed:\n\(app.debugDescription)")
        XCTAssertTrue(lineText(app, "到电脑上处理").exists, "the line says to answer on the computer")
        XCTAssertFalse(app.buttons["chat.card.attach"].exists, "nothing to attach")
        try shot(app, "computer")
    }

    /// A card the app answers says nothing under it and offers nothing more.
    @MainActor func testAnAnswerableCardIsAsBefore() throws {
        let app = open(answerable)
        defer { app.terminate() }
        let card = card(app, "ix_tests")
        XCTAssertTrue(card.waitForExistence(timeout: 20), "no card:\n\(app.debugDescription)")
        XCTAssertTrue(option(card, "XCTest").isEnabled, "the app answers it")
        XCTAssertFalse(line(app).exists, "nothing under a card the app answers")
        XCTAssertFalse(app.buttons["chat.card.attach"].exists, "no Attach Live under a card the app answers")
        try shot(app, "answerable")
    }
}
