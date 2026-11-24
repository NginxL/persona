import XCTest

/// A cancelled task and its replacement open each other through the lineage links (#5732).
/// Run against scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_INTERRUPTS=1; TEST_RUNNER_PERSONA_MOCK_HUB supplies its loopback URL.
final class TodoLineageTests: XCTestCase {
    @MainActor func testHandoffLinksOpenBothDirections() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_INTERRUPTS=1 (PERSONA_MOCK_HUB)")
        }
        continueAfterFailure = false
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "todos",
                               "-todos.view", "list", "-todos.filter", "all", "-todos.sort", "recent", "-showDoneTodos", "YES",
                               "-AppleLanguages", "(zh-Hans)", "-AppleLocale", "zh_CN"]
        app.launchPinned()

        XCTAssertTrue(app.buttons["todos.filter"].waitForExistence(timeout: 60), app.debugDescription)
        let openNew = app.buttons["todos.row.todo_cut_deps2"]
        for _ in 0..<16 where !openNew.isHittable {
            app.swipeUp()
            _ = openNew.waitForExistence(timeout: 1)
        }
        XCTAssertTrue(openNew.isHittable, app.debugDescription)
        openNew.tap()
        let openOld = app.buttons["todo.lineage.open.todo_cut_deps"]
        XCTAssertTrue(openOld.waitForExistence(timeout: 60), app.debugDescription)
        openOld.tap()
        let reason = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "原方案已停下")).firstMatch
        XCTAssertTrue(reason.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(reason.isHittable, "the cancelled task stayed behind the current detail")
        XCTAssertTrue(app.buttons["todo.lineage.superseded.todo_cut_deps2"].isHittable)

        app.buttons["todo.lineage.superseded.todo_cut_deps2"].tap()
        XCTAssertTrue(reason.waitForNonExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(app.buttons["todo.lineage.open.todo_cut_deps"].isHittable)
    }
}
