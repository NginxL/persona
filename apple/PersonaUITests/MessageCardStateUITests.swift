import XCTest

/// A message card about a task (docs/assistant/unified-entry.md「Message cards link to tasks」) once its message was answered (#4797): handled
/// elsewhere it has no Send and says the draft was not sent; sent from the card it says it was replied. Against
/// scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_AT=11 (the card at the bottom) and
/// MOCK_TASKS_MESSAGE=filed / sent / unset (PERSONA_MOCK_HUB, PERSONA_MOCK_MESSAGE says which). With PERSONA_SHOTS set it
/// writes the card there (PERSONA_SHOTS_APPEARANCE names the appearance).
final class MessageCardStateUITests: XCTestCase {
    private var hub = ""
    private var message = ""
    private let main = "server/b2e4d6f8-0000-4000-8000-000000000001"

    override func setUpWithError() throws {
        let env = ProcessInfo.processInfo.environment
        guard let h = env["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 MOCK_TASKS_AT=11 (PERSONA_MOCK_HUB)")
        }
        hub = h
        message = env["PERSONA_MOCK_MESSAGE"] ?? ""
        continueAfterFailure = false
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/message-card-\(appearance)-\(name).png"))
    }

    @MainActor func testTheCardSaysWhereItsReplyStands() async throws {
        if ProcessInfo.processInfo.environment["PERSONA_MOCK_DRAFT_ACTIONS"] == "1" {
            _ = try await fixture("reset")
        }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "assistant", "-tab", "assistant", "-openAssistant", main]
        app.launchPinned()
        #if os(iOS)
        // The notification question from SpringBoard covers the card on a fresh install.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        #endif

        let reply = app.descendants(matching: .any)["messageCard.reply"].firstMatch
        XCTAssertTrue(reply.waitForExistence(timeout: 20), "no message card:\n\(app.debugDescription)")
        let send = app.buttons["messageCard.send"] // The composer has its own send button.
        let filedLine = app.descendants(matching: .any)["messageCard.filed"].firstMatch
        switch message {
        case "filed":
            XCTAssertTrue(app.staticTexts["已处理，未发送的草稿"].exists, app.debugDescription)
            XCTAssertTrue(filedLine.exists, "no line saying the draft was not sent")
            XCTAssertFalse(send.exists, "a handled card still offers Send")
        case "sent":
            XCTAssertTrue(app.staticTexts["已以你的名义回复"].exists, app.debugDescription)
            XCTAssertFalse(send.exists, "a sent card still offers Send")
            XCTAssertFalse(filedLine.exists)
        default:
            XCTAssertTrue(app.staticTexts["按任务进度起草的回复"].exists, app.debugDescription)
            XCTAssertTrue(send.exists, "an open card has no Send")
            XCTAssertTrue(app.buttons["messageCard.dismiss"].exists, "an open draft has no Don't handle")
            XCTAssertFalse(filedLine.exists)
        }
        try shot(app, message.isEmpty ? "open" : message)
    }

    @MainActor private func fixture(_ action: String, method: String = "POST") async throws -> [String: Any] {
        var request = URLRequest(url: URL(string: hub + "/mock/draft/" + action)!)
        request.httpMethod = method
        let (data, response) = try await URLSession.shared.data(for: request)
        XCTAssertEqual((response as? HTTPURLResponse)?.statusCode, 200)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    @MainActor private func launchDraft() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-dev.openChat", "", "-openAssistant", main]
        app.launchPinned()
        #if os(iOS)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 3) { allow.tap() }
        #endif
        return app
    }

    @MainActor private func reveal(_ element: XCUIElement, in tray: XCUIElement) {
        for _ in 0..<4 where !element.isHittable {
            tray.swipeUp()
        }
    }

    @MainActor func testDismissRemovesOnlyTheDraftUndoRestoresAndReloadKeepsItDismissed() async throws {
        _ = try await fixture("reset")
        let app = launchDraft()
        let needs = app.buttons["action-bar.needs-you"]
        XCTAssertTrue(needs.waitForExistence(timeout: 20), app.debugDescription)
        needs.tap()
        let tray = app.otherElements["action-tray.needs-you"].firstMatch
        XCTAssertTrue(tray.waitForExistence(timeout: 5), app.debugDescription)
        app.expandTrayRows() // the reply draft's row is folded to a line (#5569)
        let dismiss = tray.buttons["messageCard.dismiss"].firstMatch
        XCTAssertTrue(dismiss.waitForExistence(timeout: 10), app.debugDescription)
        reveal(dismiss, in: tray)
        XCTAssertTrue(dismiss.isHittable, app.debugDescription)
        XCTAssertGreaterThanOrEqual(dismiss.frame.height, 44, "the pending decision target is too small")
        try shot(app, "needs-you-open")
        let countBefore = needs.label
        dismiss.tap()
        XCTAssertTrue(dismiss.waitForNonExistence(timeout: 10), "draft stayed in Needs you")
        let undo = app.buttons["toast-action"]
        XCTAssertTrue(undo.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertNotEqual(needs.label, countBefore, "Needs you count did not change")
        undo.tap()
        XCTAssertTrue(dismiss.waitForExistence(timeout: 10), "Undo did not restore the card")
        let edit = tray.buttons["messageCard.edit"].firstMatch
        reveal(edit, in: tray)
        edit.tap()
        let field = tray.textFields.firstMatch.exists ? tray.textFields.firstMatch : tray.textViews.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), app.debugDescription)
        field.tap()
        field.typeText("Local edit that must not be sent")
        reveal(edit, in: tray)
        edit.tap()
        reveal(dismiss, in: tray)
        dismiss.tap()
        XCTAssertTrue(dismiss.waitForNonExistence(timeout: 10))
        app.terminate()
        let reopened = launchDraft()
        let reopenedNeeds = reopened.buttons["action-bar.needs-you"]
        XCTAssertTrue(reopenedNeeds.waitForExistence(timeout: 20))
        reopenedNeeds.tap()
        reopened.expandTrayRows(timeout: 2)
        XCTAssertFalse(reopened.otherElements["action-tray.needs-you"].buttons["messageCard.dismiss"].exists,
                       "a dismissed card returned after relaunch")
        _ = try await fixture("progress")
        reopened.expandTrayRows()
        XCTAssertTrue(reopened.otherElements["action-tray.needs-you"].buttons["messageCard.dismiss"].firstMatch.waitForExistence(timeout: 10),
                      "a new same-reference draft should still need attention")
        let evidence = try await fixture("evidence", method: "GET")
        XCTAssertEqual((evidence["sent"] as? [Any])?.count, 0, "Dismiss sent a reply")
        XCTAssertEqual(evidence["task_mutations"] as? Int, 0, "Dismiss changed the task")
        XCTAssertEqual(evidence["agent_mutations"] as? Int, 0, "Dismiss changed the Agent")
        let operations = evidence["operations"] as? [[String: Any]] ?? []
        XCTAssertEqual(operations.compactMap { $0["action"] as? String }, ["dismiss", "undo-dismiss", "dismiss"])
        XCTAssertTrue(operations.filter { $0["action"] as? String == "dismiss" }.allSatisfy {
            ($0["body"] as? [String: String])?["draft"] == "测试已通过，稍后提交变更。"
        }, "Dismiss used edited text rather than the original draft")
        let attachment = XCTAttachment(data: try JSONSerialization.data(withJSONObject: evidence, options: .sortedKeys), uniformTypeIdentifier: "public.json")
        attachment.name = "draft-no-send-evidence"
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    @MainActor func testFailedDismissKeepsTheCardAndCanBeRetried() async throws {
        _ = try await fixture("reset")
        _ = try await fixture("fail-next")
        let app = launchDraft()
        let dismiss = app.buttons["messageCard.dismiss"].firstMatch
        XCTAssertTrue(dismiss.waitForExistence(timeout: 20), app.debugDescription)
        dismiss.tap()
        let toast = app.descendants(matching: .any).matching(
            NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "toast", "无法忽略草稿")
        ).firstMatch
        XCTAssertTrue(toast.waitForExistence(timeout: 5), "no failure toast:\n\(app.debugDescription)")
        XCTAssertTrue(dismiss.exists, "a failed request removed the draft")
        XCTAssertTrue(dismiss.isEnabled, "a failed request left the card busy")
        dismiss.tap()
        XCTAssertTrue(dismiss.waitForNonExistence(timeout: 10))
        let evidence = try await fixture("evidence", method: "GET")
        XCTAssertEqual((evidence["sent"] as? [Any])?.count, 0)
    }
}
