import XCTest

/// The Git tab's writes on the iPhone and iPad (#7013) against the mock hub's made-up repository: tick a file, commit
/// what is staged from the commit bar's sheet, Push asks first; a refused commit says why and keeps the message.
/// Run with `MOCK_FILES=1 MOCK_GIT_WRITE=1` and `PERSONA_GIT_HUB=<mock url>`; `PERSONA_SHOTS=<dir>` writes screenshots.
final class GitWriteUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_GIT_HUB"] else {
            throw XCTSkip("需要 MOCK_FILES=1 MOCK_GIT_WRITE=1 的 mock hub 和 PERSONA_GIT_HUB") // i18n-ignore: test skip reason
        }
        self.hub = hub
    }

    @discardableResult private func control(_ query: String) async throws -> [String: Any] {
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/git-write?" + query)!)
        return try JSONSerialization.jsonObject(with: data) as! [String: Any]
    }

    @MainActor private func launch() -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001",
                               "-openPane", "git", "-openAgentsFresh", "YES", "-openChatsFresh", "YES"]
        app.launchPinned()
        XCTAssertTrue(app.staticTexts["Provider.swift"].waitForExistence(timeout: 20), app.debugDescription)
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let tag = env["PERSONA_SHOTS_TAG"] ?? "shot"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/git-write-\(tag)-\(name).png"))
    }

    @MainActor private func toast(_ app: XCUIApplication, _ words: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
    }

    /// Taps e once it stops moving: on an iPad the form sheet slides up as the keyboard comes, and a tap aimed at
    /// where the button will be lands outside the sheet while it is still on its way.
    @MainActor private func tapSettled(_ e: XCUIElement) {
        var last = e.frame
        for _ in 0..<20 {
            RunLoop.current.run(until: Date().addingTimeInterval(0.25))
            let now = e.frame
            if now == last && e.isHittable { break }
            last = now
        }
        e.tap()
    }

    @MainActor func testStageCommitAndPushAfterAsking() async throws {
        try await control("reset=1")
        let app = launch()
        XCTAssertTrue(app.buttons["git.commit"].waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "1-tab")
        // Provider.swift is not staged: its tick box (a toggle to VoiceOver) stages it
        let tick = app.switches["暂存 Provider.swift"]
        XCTAssertTrue(tick.waitForExistence(timeout: 10), app.debugDescription)
        tick.tap()
        XCTAssertTrue(app.switches["取消暂存 Provider.swift"].waitForExistence(timeout: 10), app.debugDescription)
        let afterTick = try await control("")
        XCTAssertEqual(afterTick["files"] as? Int, 4)

        app.buttons["git.commit"].tap()
        let message = app.descendants(matching: .any)["git.commit.message"].firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 10), app.debugDescription)
        message.typeText("小组件：逐小时预报的数据和视图")
        try shot(app, "2-commit")
        tapSettled(app.buttons["git.commit.go"])
        XCTAssertTrue(toast(app, "已提交 eeeeeee").waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "3-committed")

        // a branch without an upstream: Push says it makes one, and only goes once the user says so
        let push = app.buttons["git-push"]
        XCTAssertTrue(push.waitForExistence(timeout: 10))
        push.tap()
        let ask = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "远端还没有 widget-hourly")).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), app.debugDescription)
        try shot(app, "4-push-ask")
        let asked = try await control("")
        XCTAssertEqual(asked["ahead"] as? Int, 3, "nothing is pushed before the yes")
        // the dialog's button: by its identifier, or its word where the system drops identifiers in dialogs
        tapSettled(app.buttons.matching(NSPredicate(format: "identifier == 'git.push.confirm' OR (label == '推送' AND identifier != 'git-push')")).firstMatch)
        XCTAssertTrue(toast(app, "已推送 widget-hourly").waitForExistence(timeout: 10), app.debugDescription)
        let pushed = try await control("")
        XCTAssertEqual(pushed["ahead"] as? Int, 0)
        XCTAssertFalse(app.buttons["git-push"].waitForExistence(timeout: 2), "nothing left to push")
    }

    @MainActor func testARefusedCommitSaysWhyAndKeepsTheMessage() async throws {
        try await control("reset=1")
        let app = launch()
        try await control("fail=git_hook_failed")
        app.buttons["git.commit"].tap()
        let message = app.descendants(matching: .any)["git.commit.message"].firstMatch
        XCTAssertTrue(message.waitForExistence(timeout: 10))
        message.typeText("先试一版")
        tapSettled(app.buttons["git.commit.go"])
        let title = app.staticTexts["提交被拒绝了（通常是 pre-commit 之类的 hook）"]
        XCTAssertTrue(title.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "Line Length Violation")).firstMatch.exists)
        try shot(app, "5-refused")
        app.alerts.buttons.firstMatch.tap()
        XCTAssertEqual(message.value as? String, "先试一版", "the message stays for another try")
    }

    /// A hub without `git_write` (run with MOCK_GIT_WRITE_FEATURE=0): the tab as before, no tick boxes, bar or Push.
    @MainActor func testWithoutTheHubsWritesTheTabIsAsBefore() async throws {
        guard ProcessInfo.processInfo.environment["PERSONA_GIT_NO_WRITE"] == "1" else { throw XCTSkip("PERSONA_GIT_NO_WRITE=1 with MOCK_GIT_WRITE_FEATURE=0") }
        try await control("reset=1")
        let app = launch()
        XCTAssertFalse(app.switches["暂存 Provider.swift"].exists)
        XCTAssertFalse(app.buttons["git.commit"].exists)
        XCTAssertFalse(app.buttons["git-push"].exists)
        try shot(app, "0-before")
    }
}
