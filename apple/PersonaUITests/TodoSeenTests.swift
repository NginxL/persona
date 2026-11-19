import XCTest

/// Got It on the To-dos list (#5707, #7148) against the mock hub (`MOCK_TODO_LIST=1`): a Just Finished row files under
/// Done at once, and Undo puts it back. Its compact chip shares the status line under the time and has a finger-sized
/// hit area without opening the to-do. `TEST_RUNNER_PERSONA_TODOS_HUB=<mock URL>`; `TEST_RUNNER_PERSONA_SHOTS=<dir>`
/// also saves the screens (`TEST_RUNNER_PERSONA_SHOTS_APPEARANCE` names them).
final class TodoSeenTests: XCTestCase {
    private var app: XCUIApplication!

    @MainActor func testGotItFilesAJustFinishedRowUnderDoneAndUndoPutsItBack() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_TODOS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_LIST=1 (PERSONA_TODOS_HUB)")
        }
        continueAfterFailure = false
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "todos", "-todos.view", "list",
                               "-todos.filter", "all", "-todos.sort", "recent"]
        app.launchPinned()

        XCTAssertTrue(app.buttons["todos.filter"].waitForExistence(timeout: 60))
        // Other Open starts open: its header is a plain section header, not a fold button (Shelved's is one)
        XCTAssertTrue(app.descendants(matching: .any)["todos.section.other"].waitForExistence(timeout: 10))
        XCTAssertFalse(app.buttons["todos.section.other"].exists, "Other Open starts folded")
        shot("todos-seen-top")

        let gotIt = app.buttons["todos.seen.tl_speed"]
        for _ in 0..<8 where !gotIt.isHittable { app.swipeUp() }
        XCTAssertTrue(gotIt.waitForExistence(timeout: 10), "no Got It on the Just Finished row")
        shot("todos-seen-row")
        gotIt.tap()
        let undo = undoButton()
        XCTAssertTrue(undo.waitForExistence(timeout: 5), "no toast with Undo")
        let moved = app.descendants(matching: .any).matching(NSPredicate(format: "label IN %@",
            ["Moved to Done", "已移到「已完成」", "「完了」に移動しました"])).firstMatch // i18n-ignore: the toast in each language
        XCTAssertTrue(moved.waitForExistence(timeout: 5), "Got It did not immediately say Moved to Done")
        XCTAssertTrue(gotIt.waitForNonExistence(timeout: 5), "the row stayed in Just Finished")
        XCTAssertTrue(app.buttons["todos.filter"].exists, "Got It opened the to-do instead of filing it")
        XCTAssertFalse(app.buttons["todo.toggleDone"].exists, "the to-do detail opened")
        shot("todos-seen-toast")
        undo.tap()
        XCTAssertTrue(app.buttons["todos.seen.tl_speed"].waitForExistence(timeout: 10), "Undo did not put it back")
    }

    /// UI geometry for #7148 in both appearances and at both reachable interface sizes. The time is the rightmost text
    /// above Accepted in this known row, so no new production accessibility identifiers are needed for the measurement.
    @MainActor func testGotItIsAtTheStatusLineRightUnderTheTime() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_TODOS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_LIST=1 (PERSONA_TODOS_HUB)")
        }
        continueAfterFailure = true
        for scheme in ["light", "dark"] {
            for (tag, size) in [("large", "UICTContentSizeCategoryL"), ("xxxLarge", "UICTContentSizeCategoryXXXL")] {
                launch(hub, scheme: scheme, size: size)
                let row = revealSeenRow()
                let gotIt = app.buttons["todos.seen.tl_speed"]
                shot("todos-seen-layout-\(scheme)-\(tag)")
                guard gotIt.exists, row.exists else {
                    XCTFail("no visible Just Finished row and Got It at \(scheme), \(tag)")
                    app.terminate()
                    continue
                }
                let texts = app.staticTexts.allElementsBoundByIndex.filter {
                    $0.exists && row.frame.insetBy(dx: -1, dy: -1).contains(CGPoint(x: $0.frame.midX, y: $0.frame.midY))
                }
                let status = texts.first { $0.label == "Accepted" }
                let time = status.flatMap { status in
                    texts.filter { $0.frame.maxY <= status.frame.minY + 1 }.max { $0.frame.maxX < $1.frame.maxX }
                }
                guard let status, let time else {
                    XCTFail("could not measure Accepted and its time in \(row.label): \(texts.map(\.label))")
                    app.terminate()
                    continue
                }
                XCTAssertEqual(gotIt.frame.midY, status.frame.midY, accuracy: 2,
                               "Got It is on its own line, not the status line: \(gotIt.frame) vs \(status.frame)")
                XCTAssertGreaterThan(gotIt.frame.minX, status.frame.maxX,
                                     "Got It is not at the status content's right")
                XCTAssertGreaterThan(gotIt.frame.midY, time.frame.maxY - 2,
                                     "Got It is not below the time")
                XCTAssertEqual(gotIt.frame.maxX, time.frame.maxX, accuracy: 2,
                               "Got It and the time do not share the row's trailing edge")
                XCTAssertLessThanOrEqual(gotIt.frame.maxX, row.frame.maxX + 1, "Got It spills out of the row")
                app.terminate()
            }
        }
    }

    /// The drawn capsule stays compact (the renderer tests measure it); a touch 18 pt below its center still files the
    /// row immediately and never activates the NavigationLink underneath.
    @MainActor func testGotItHitAreaExtendsBelowTheCompactCapsuleWithoutOpeningDetail() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_TODOS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_LIST=1 (PERSONA_TODOS_HUB)")
        }
        continueAfterFailure = false
        launch(hub, scheme: "light", size: "UICTContentSizeCategoryL")
        _ = revealSeenRow()
        let gotIt = app.buttons["todos.seen.tl_speed"]
        XCTAssertTrue(gotIt.waitForExistence(timeout: 10))
        let frame = gotIt.frame
        app.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: frame.midX, dy: frame.midY + 18)).tap()
        let undo = undoButton()
        XCTAssertTrue(undo.waitForExistence(timeout: 5), "a tap 18 pt below Got It's center did not activate it")
        XCTAssertTrue(gotIt.waitForNonExistence(timeout: 5))
        XCTAssertTrue(app.buttons["todos.filter"].exists, "the offset tap opened the to-do")
        XCTAssertFalse(app.buttons["todo.toggleDone"].exists, "the offset tap opened the to-do detail")
        undo.tap()
        XCTAssertTrue(app.buttons["todos.seen.tl_speed"].waitForExistence(timeout: 10))
    }

    /// Reopen is this closed row's existing full-swipe action. Got It remains after it in the leading actions.
    @MainActor func testLeadingSwipeKeepsReopenBeforeGotIt() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_TODOS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_LIST=1 (PERSONA_TODOS_HUB)")
        }
        continueAfterFailure = false
        launch(hub, scheme: "light", size: "UICTContentSizeCategoryL")
        let row = revealSeenRow()
        row.swipeRight()
        let reopen = app.buttons.matching(NSPredicate(format: "label == %@", "Reopen")).firstMatch
        let gotIt = app.buttons.matching(NSPredicate(format: "label == %@", "Got It")).firstMatch
        XCTAssertTrue(reopen.waitForExistence(timeout: 5), "the closed row lost its full-swipe Reopen action")
        XCTAssertTrue(gotIt.waitForExistence(timeout: 5), "the leading swipe lost Got It")
        XCTAssertLessThanOrEqual(reopen.frame.maxX, gotIt.frame.minX,
                                 "Got It must follow the full swipe's own action: \(reopen.frame) vs \(gotIt.frame)")
    }

    @MainActor private func launch(_ hub: String, scheme: String, size: String) {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "todos", "-todos.view", "list",
                               "-todos.filter", "all", "-todos.sort", "recent", "-ui.scheme", scheme,
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-ui.textSize", "standard",
                               "-UIPreferredContentSizeCategoryName", size]
        app.launchPinned()
        XCTAssertTrue(app.buttons["todos.filter"].waitForExistence(timeout: 60))
    }

    @MainActor private func revealSeenRow() -> XCUIElement {
        let row = app.descendants(matching: .any)["todos.row.tl_speed"].firstMatch
        let window = app.windows.firstMatch.frame
        for _ in 0..<24 {
            if row.exists, row.isHittable, window.insetBy(dx: -0.5, dy: -0.5).contains(row.frame) { return row }
            app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        }
        XCTFail("the Just Finished row never became fully visible:\n\(app.debugDescription.prefix(4000))")
        return row
    }

    private func undoButton() -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label IN %@", ["Undo", "撤销", "取り消す"])).firstMatch // i18n-ignore: toast Undo
    }

    @MainActor private func shot(_ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let look = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + look + ".png"))
        }
    }
}
