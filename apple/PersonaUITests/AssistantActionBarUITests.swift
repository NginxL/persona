import XCTest

/// The action bar above the assistant's composer (#3393): Needs You and In Progress in the dock's low capsules, each
/// opening its tray above the bar, one at a time, each thing in one of them. Against scripts/mock/mock_hub.py with
/// MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 and MOCK_TASKS_DOCK unset (tasks, two agents of their own, one of them waiting)
/// (PERSONA_MOCK_HUB). With PERSONA_SHOTS set it writes the bar and each tray there (PERSONA_SHOTS_APPEARANCE names the
/// simulator's appearance in the file).
final class AssistantActionBarUITests: XCTestCase {
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TASKS=1 MOCK_TASKS_NO_ASKS=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/actionbar-\(appearance)-\(name).png"))
    }

    @MainActor func testTwoLowEntriesEachThingOnce() throws {
        let app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        // the main conversation, where the mock's tasks, plans and Agents opened by hand are
        app.launchArguments = ["-pairURL", url, "-tab", "assistant", "-dev.openChat", "",
                               "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
        app.launchPinned()

        let needs = app.buttons["action-bar.needs-you"]
        let running = app.buttons["action-bar.running"]
        XCTAssertTrue(running.waitForExistence(timeout: 20), "no action bar:\n\(app.debugDescription)")
        XCTAssertTrue(needs.exists)
        for gone in ["action-bar.pending", "action-bar.progress", "action-bar.agents", "action-bar.start", "task-dock.board"] {
            XCTAssertFalse(app.buttons[gone].exists, "\(gone) still shows beside the two entries")
        }
        // The tasks, the sessions and their states come in separate pulls: wait until the counts are in.
        let deadline = Date.now.addingTimeInterval(20)
        while !(needs.label.contains(where: \.isNumber) && running.label.contains(where: \.isNumber)) && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(needs.label.hasPrefix("要你处理"), needs.label)
        XCTAssertTrue(running.label.hasPrefix("进行中"), running.label)

        // One row of capsules as low as the dock's: the same height, well under a 44 pt button.
        let height = running.frame.height
        XCTAssertLessThan(height, 36, "the entries are \(height) pt tall")
        XCTAssertEqual(needs.frame.height, height, accuracy: 0.5)
        XCTAssertEqual(needs.frame.midY, running.frame.midY, accuracy: 0.5, "not one row")
        XCTAssertLessThan(needs.frame.maxX, running.frame.minX)
        sleep(2) // the conversation above settles
        try shot(app, "bar")

        // In Progress: the tasks going on and the agent of no task at work (/orders), each a row.
        running.tap()
        let tray = app.otherElements["action-tray.running"].firstMatch
        XCTAssertTrue(tray.waitForExistence(timeout: 5), app.debugDescription)
        // The tray stays inside the screen, whatever its rows say (#13656).
        let window = app.windows.firstMatch.frame
        XCTAssertGreaterThanOrEqual(tray.frame.minX, window.minX, "the tray starts left of the screen")
        XCTAssertLessThanOrEqual(tray.frame.maxX, window.maxX, "the tray runs past the screen's right edge")
        app.waitForRows(app.trayTasks, atLeast: 1)
        let runningRows = rowLabels(app)
        XCTAssertFalse(runningRows.isEmpty, app.debugDescription)
        XCTAssertTrue(running.isSelected, "In Progress is not marked open")
        XCTAssertEqual(running.frame.height, height, accuracy: 0.5, "the open entry changed its height")
        XCTAssertTrue(running.label.contains("\(runningRows.count)"), "\(running.label) counts other than its \(runningRows.count) rows")
        sleep(1)
        try shot(app, "running")

        // Needs You takes its place: one tray at a time, and nothing of In Progress is in it.
        needs.tap()
        XCTAssertTrue(app.otherElements["action-tray.needs-you"].firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(tray.waitForNonExistence(timeout: 5), "In Progress stayed open under Needs You")
        XCTAssertFalse(running.isSelected)
        // the photo sync waits for the user: its hand-made to-do's row, once (its Agent is folded into it), and only here
        app.waitForRows(app.trayTasks, atLeast: 1)
        let needRows = rowLabels(app)
        let photos = needRows.filter { $0.contains("照片同步") }
        XCTAssertEqual(photos.count, 1, "the photo sync is not one row: \(needRows)")
        XCTAssertTrue(photos.first?.contains("需要你") == true, "\(photos)")
        XCTAssertTrue(Set(needRows).isDisjoint(with: runningRows), "listed in both: \(Set(needRows).intersection(runningRows))")
        sleep(1)
        try shot(app, "needs-you")

        // The entry again closes its tray.
        needs.tap()
        XCTAssertTrue(app.otherElements["action-tray.needs-you"].firstMatch.waitForNonExistence(timeout: 5), "the tray did not close")

        // In Progress' All: the board, where finished things are.
        running.tap()
        let all = app.buttons["action-tray.board"]
        XCTAssertTrue(all.waitForExistence(timeout: 5), app.debugDescription)
        all.tap()
        XCTAssertTrue(app.buttons.matching(identifier: "task-board.agent").firstMatch.waitForExistence(timeout: 5), app.debugDescription)
        app.buttons["关闭"].firstMatch.tap()
        XCTAssertTrue(running.waitForExistence(timeout: 5))
    }

    /// The task and agent rows of the open tray, by label.
    @MainActor private func rowLabels(_ app: XCUIApplication) -> [String] {
        (app.trayTasks.allElementsBoundByIndex + app.trayAgents.allElementsBoundByIndex).map(\.label)
    }

    /// The entries at every text size (#5702): side by side at the largest size they made the row, and with it the whole
    /// chat page, wider than the screen, the header's avatar and the composer's buttons cut off at the edges. Now they go
    /// on to the next line when they don't fit. zh-Hans, English and Japanese, the default and the largest size, light
    /// and dark; `PERSONA_SHOTS` keeps `actionbar-width-<language>-<size>-<scheme>.png`. `PERSONA_PHASE=before` takes
    /// the same screenshots on an older build, checking nothing.
    @MainActor func testEntriesStayOnScreenAtEveryTextSize() throws {
        let env = ProcessInfo.processInfo.environment
        let before = env["PERSONA_PHASE"] == "before"
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let languages: [(String, [String])] = [
            ("zh-Hans", []),
            ("en", ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"]),
            ("ja", ["-AppleLanguages", "(ja)", "-AppleLocale", "ja_JP"]),
        ]
        for (language, languageArgs) in languages {
            for size in ["default", "largest"] {
                for scheme in ["light", "dark"] {
                    let name = "\(language)-\(size)-\(scheme)"
                    let app = XCUIApplication()
                    let largest = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
                    app.launchArguments = ["-pairURL", url, "-tab", "assistant", "-dev.openChat", "", "-ui.scheme", scheme,
                                           "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
                        + languageArgs + (size == "largest" ? largest : [])
                    app.launchPinned()
                    let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
                    for label in ["允许", "Allow"] where board.buttons[label].waitForExistence(timeout: 3) { board.buttons[label].tap() }

                    let needs = app.buttons["action-bar.needs-you"]
                    let running = app.buttons["action-bar.running"]
                    XCTAssertTrue(running.waitForExistence(timeout: 20), "\(name): no action bar:\n\(app.debugDescription)")
                    let deadline = Date.now.addingTimeInterval(20)
                    while !(needs.label.contains(where: \.isNumber) && running.label.contains(where: \.isNumber)) && Date.now < deadline {
                        usleep(200_000)
                    }
                    sleep(2) // the conversation above settles
                    if !before {
                        let window = app.windows.firstMatch.frame
                        let bar = app.descendants(matching: .any)["action-bar"].firstMatch
                        let avatar = app.descendants(matching: .any)["persona-avatar"].firstMatch
                        XCTAssertTrue(bar.exists, "\(name): \(app.debugDescription)")
                        for (what, element) in [("the bar", bar), ("Needs You", needs), ("In Progress", running), ("the avatar", avatar)]
                        where element.exists {
                            XCTAssertGreaterThanOrEqual(element.frame.minX, window.minX - 0.5, "\(name): \(what) starts left of the screen")
                            XCTAssertLessThanOrEqual(element.frame.maxX, window.maxX + 0.5, "\(name): \(what) ends right of the screen")
                        }
                        if size == "default" {
                            XCTAssertEqual(needs.frame.midY, running.frame.midY, accuracy: 0.5, "\(name): not one row")
                        }
                    }
                    if let dir = env["PERSONA_SHOTS"] {
                        // The app icon follows the color scheme, and the system says so in an alert over the page: closed first.
                        if board.alerts.firstMatch.waitForExistence(timeout: 2) {
                            for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
                                board.alerts.buttons[label].tap(); sleep(1); break
                            }
                        }
                        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/actionbar-width-\(name).png"))
                    }
                    app.terminate()
                }
            }
        }
    }
}
