import XCTest

/// An Agents query is part of the visible filtering state (#7332). On a compact phone it stays reachable after the
/// keyboard goes, while results scroll, and when the page comes back; an empty field keeps the existing fold and
/// pull-to-reveal. Regular-width iOS uses `AssistantFullSearch`: its fixed bottom field gets the same scroll/edit/clear
/// coverage without inventing a drawer that layout does not have.
/// Run against scripts/mock/mock_hub.py with PERSONA_MOCK_HUB. PERSONA_SHOTS keeps every checkpoint.
final class AgentsSearchVisibilityUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private let query = "a"
    private let band = 54.0 // AgentsHeader.band, as in AgentsTopMotionTests
    private var restFieldFrame = CGRect.null

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let tree = XCTAttachment(string: app.debugDescription)
            tree.name = "search visibility failure tree"
            tree.lifetime = .keepAlways
            add(tree)
        }
        app?.terminate()
        super.tearDown()
    }

    @MainActor private func launch() {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-tab", "dev", "-dev.openChat", "",
                               "-app.navigation", "separate", "-openAgentsFresh", "YES", "-dev.grouping", "repo",
                               "-probes", "YES"]
        if let size = ProcessInfo.processInfo.environment["PERSONA_UI_TEXT_SIZE"] {
            app.launchArguments += ["-UIPreferredContentSizeCategoryName", size]
        }
        if let scheme = ProcessInfo.processInfo.environment["PERSONA_UI_SCHEME"] {
            app.launchArguments += ["-ui.scheme", scheme]
        }
        app.launchPinned(navigation: false)
        acceptIconAlert()
        XCTAssertTrue(app.buttons["agents.filter.all"].waitForExistence(timeout: 25), "no Agents list")
        sleep(2) // the initial empty-field fold has finished
    }

    @MainActor private func revealSearch() -> XCUIElement {
        app.revealAgentsSearch()
    }

    @MainActor private func beginQuery() -> XCUIElement {
        let field = revealSearch()
        sleep(1) // hittable starts at half revealed; wait for the ordinary reveal/settle to finish
        let restingY = field.frame.midY
        sleep(1)
        XCTAssertEqual(field.frame.midY, restingY, accuracy: 1, "the revealed field never reached a stable rest position")
        restFieldFrame = field.frame
        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 8), "the search keyboard did not open")
        field.typeText(query)
        XCTAssertEqual(field.value as? String, query)
        let typingIntro = app.buttons["Continue"].firstMatch
        if typingIntro.waitForExistence(timeout: 1) { typingIntro.tap() }
        sleep(1)
        return field
    }

    @MainActor private func assertFieldVisible(_ step: String, expected: String? = nil,
                                               file: StaticString = #filePath, line: UInt = #line) -> XCUIElement {
        let field = app.searchFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "\(step): no Agents search field", file: file, line: line)
        let visibleFields = app.searchFields.allElementsBoundByIndex.filter { $0.exists && $0.isHittable }
        XCTAssertEqual(visibleFields.count, 1, "\(step): duplicate or covered Agents search fields", file: file, line: line)
        XCTAssertTrue(field.isHittable, "\(step): the query field is off screen or covered", file: file, line: line)
        if let expected { XCTAssertEqual(field.value as? String, expected, "\(step): the query changed", file: file, line: line) }
        let window = app.windows.firstMatch.frame
        XCTAssertTrue(window.contains(field.frame), "\(step): \(field.frame) is outside \(window)", file: file, line: line)
        return field
    }

    /// Select the architecture actually on screen, not a width threshold: a compact landscape window can be wide.
    @MainActor private var regularWidth: Bool {
        let sidebarSearch = app.buttons["sidebar-search"].firstMatch
        return sidebarSearch.exists && sidebarSearch.isHittable
    }

    @MainActor private func assertRetainedAtRest(_ step: String, file: StaticString = #filePath,
                                                 line: UInt = #line) -> XCUIElement {
        let field = assertFieldVisible(step, expected: query, file: file, line: line)
        XCTAssertEqual(field.frame.midY, restFieldFrame.midY, accuracy: 1,
                       "\(step): the retained field rose or left its ordinary row", file: file, line: line)
        XCTAssertFalse(app.keyboards.firstMatch.exists, "\(step): returning opened the keyboard", file: file, line: line)
        if !regularWidth {
            XCTAssertTrue(app.buttons["agents-new"].firstMatch.isHittable,
                          "\(step): the shell bar did not return", file: file, line: line)
        }
        return field
    }

    @MainActor private var agentsList: XCUIElement {
        let window = app.windows.firstMatch.frame
        return app.collectionViews.allElementsBoundByIndex.first(where: {
            $0.exists && $0.frame.minX <= window.minX + 1 && $0.frame.height > window.height * 0.4
        }) ?? app.collectionViews.firstMatch
    }

    @MainActor private func scrollFar() {
        let list = agentsList
        XCTAssertTrue(list.exists, "no Agents results list")
        for _ in 0..<7 {
            let from = list.coordinate(withNormalizedOffset: CGVector(dx: 0.02, dy: 0.78))
            let to = list.coordinate(withNormalizedOffset: CGVector(dx: 0.02, dy: 0.22))
            from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .fast, thenHoldForDuration: 0.1)
        }
        sleep(1)
    }

    @MainActor private func toTop() {
        let all = app.buttons["agents.filter.all"].firstMatch
        for _ in 0..<12 where !(all.exists && all.isHittable) { agentsList.swipeDown(velocity: .fast) }
        XCTAssertTrue(all.exists && all.isHittable, "the list did not return to its quick filters")
    }

    @MainActor private var fullSearchList: XCUIElement {
        let identified = app.collectionViews["full-search"].firstMatch
        if identified.exists { return identified }
        let window = app.windows.firstMatch.frame
        return app.collectionViews.allElementsBoundByIndex.first(where: {
            $0.exists && $0.isHittable && $0.frame.height > window.height * 0.4
        }) ?? app.collectionViews.firstMatch
    }

    @MainActor private func scrollFullSearch() {
        let list = fullSearchList
        XCTAssertTrue(list.exists, "no full-search results list")
        for _ in 0..<7 {
            // Start above the keyboard: the collection extends behind it, but a touch on the keyboard cannot scroll it.
            let from = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.50))
            let to = list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.20))
            from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .fast, thenHoldForDuration: 0.1)
        }
        sleep(1)
    }

    @MainActor private func clearRetainedByTouch(_ field: XCUIElement) {
        XCTAssertTrue(field.exists && field.isHittable, "the retained field is not reachable for Clear")
        // The inactive field is one VoiceOver element with a Clear Search custom action; its nested UIButton is
        // deliberately not a second accessibility element. Exercise the physical clear control at its trailing edge.
        field.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
    }

    @MainActor private func dismissSoftwareKeyboard() {
        let field = app.textFields["full-search-field"].firstMatch
        XCTAssertTrue(field.exists && field.isHittable, "the regular search field is not reachable for Return")
        field.typeText("\n")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 5), "Return did not dismiss the keyboard")
    }

    @MainActor private func assertFullSearchVisible(_ step: String, expected: String? = nil) -> XCUIElement {
        let field = app.textFields["full-search-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5) && field.isHittable, "\(step): no visible full-search field")
        if let expected { XCTAssertEqual(field.value as? String, expected, "\(step): the query changed") }
        XCTAssertTrue(app.windows.firstMatch.frame.contains(field.frame), "\(step): the field is outside the window")
        return field
    }

    @MainActor private func openResultAndReturn() {
        let rows = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["agents.row", "agents.compactRow"]))
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 10), "the query has no result to open")
        // A row in the middle of the window, not the first in the tree: after a long scroll that one can lie under the
        // top bar, where a tap on its centre reaches the status bar and scrolls the list to its top instead.
        let window = app.windows.firstMatch.frame
        let row = rows.allElementsBoundByIndex.first(where: { row in
            guard row.exists else { return false }
            let y = row.frame.midY
            return y > window.minY + window.height * 0.3 && y < window.minY + window.height * 0.75 && row.isHittable
        }) ?? rows.firstMatch
        if !row.isHittable { app.bringAboveTabBar(row) }
        row.tap()
        let opened = app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier IN %@ OR label == %@", ["chat.title", "chat.compactTitle"], "更多")).firstMatch
        XCTAssertTrue(opened.waitForExistence(timeout: 12), "the search result did not open")
        if let back = app.buttons.matching(NSPredicate(format: "label IN %@", ["返回", "Back"]))
            .allElementsBoundByIndex.first(where: { $0.exists && $0.isHittable }) {
            back.tap()
        } else {
            let window = app.windows.firstMatch
            window.coordinate(withNormalizedOffset: CGVector(dx: 0.005, dy: 0.5))
                .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.9, dy: 0.5)),
                       withVelocity: .slow, thenHoldForDuration: 0.1)
        }
        XCTAssertTrue(rows.firstMatch.waitForExistence(timeout: 10), "Back did not return to the query results")
        sleep(1)
    }

    @MainActor private func leaveAndReturnToAgents() {
        if app.buttons["shell-assistant"].firstMatch.exists {
            let finger = ShellFinger(app)
            finger.switchTo(agents: false, "Assistant, leaving Agents", limit: 5)
            // Not at once, as in `AgentsTopMotionTests`: a tap on the switch within about a second of leaving can be
            // lost while the Assistant page is first built (#10227). These tests are about the field once Agents is
            // back, not about the switch.
            sleep(2)
            finger.switchTo(agents: true, "Agents again", limit: 5)
        } else {
            let persona = app.buttons["Persona"].firstMatch
            let agents = app.buttons["Agents"].firstMatch
            XCTAssertTrue(persona.waitForExistence(timeout: 5) && agents.exists, "no native Persona | Agents tabs")
            persona.tap()
            sleep(1)
            agents.tap()
        }
        sleep(2)
    }

    @MainActor private func shot(_ name: String) {
        let screenshot = app.screenshot()
        let attachment = XCTAttachment(screenshot: screenshot)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? FileManager.default.createDirectory(atPath: directory, withIntermediateDirectories: true)
            try? screenshot.pngRepresentation.write(to: URL(fileURLWithPath: directory).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor func testQueryStaysVisibleAndEditableAfterScrollingThenClearingFolds() throws {
        launch()
        try XCTSkipIf(regularWidth, "regular-width full search has its own regression below")
        _ = beginQuery()
        shot("query-01-keyboard")
        app.searchFields.firstMatch.typeText("\n")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 4), "Return did not dismiss the keyboard")
        _ = assertFieldVisible("keyboard dismissed", expected: query)
        shot("query-02-keyboard-dismissed")

        scrollFar()
        _ = assertFieldVisible("active query scrolled", expected: query)
        shot("query-03-active-scrolled")

        openResultAndReturn()
        shot("query-04-result-return")
        _ = assertRetainedAtRest("returned from or opened a result")
        scrollFar()
        shot("query-05-retained-scrolled")
        let retained = assertRetainedAtRest("retained query scrolled")
        XCTAssertFalse(app.buttons["agents.filter.all"].firstMatch.isHittable,
                       "the retained activation did not start beyond the ordinary field's fold")

        // This must be a real edit, not merely an accessibility element that is visible behind the results list.
        retained.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 8), "tapping the retained field did not open the keyboard")
        retained.typeText("x")
        XCTAssertEqual(retained.value as? String, query + "x", "the visible field did not take typed input")
        retained.typeText(XCUIKeyboardKey.delete.rawValue)
        XCTAssertEqual(retained.value as? String, query)
        retained.typeText("\n")
        XCTAssertTrue(app.keyboards.firstMatch.waitForNonExistence(timeout: 4), "Return did not dismiss the edited keyboard")
        sleep(1)
        openResultAndReturn()
        _ = assertRetainedAtRest("second retained return")
        scrollFar()

        let clearing = assertRetainedAtRest("before clearing the retained query")
        clearRetainedByTouch(clearing)
        sleep(1)
        XCTAssertFalse(app.searchFields.firstMatch.isHittable, "clearing at a folded position must restore the empty-field fold")
        XCTAssertFalse(app.keyboards.firstMatch.exists, "clearing also activated search")
        XCTAssertFalse(app.navigationBars.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel", "关闭", "Close"]))
            .firstMatch.isHittable, "clearing opened the active-search Close control")
        shot("query-06-cleared-folded")
        toTop()
        _ = revealSearch()
        shot("query-07-empty-revealed")
    }

    @MainActor func testRetainedQueryStaysVisibleWhenAgentsReturnsToFront() throws {
        launch()
        try XCTSkipIf(regularWidth, "regular-width full search has its own regression below")
        _ = beginQuery()
        app.searchFields.firstMatch.typeText("\n")
        openResultAndReturn()
        _ = assertRetainedAtRest("before Agents leaves the front")
        // First with the list at its top, where an empty field folds again when the tab comes back (#740): the
        // retained one must leave the list where it is, or its first rows go under the field.
        let topFiltersY = app.buttons["agents.filter.all"].firstMatch.frame.midY
        leaveAndReturnToAgents()
        _ = assertRetainedAtRest("Agents returned to front with the list at its top")
        XCTAssertEqual(app.buttons["agents.filter.all"].firstMatch.frame.midY, topFiltersY, accuracy: 1,
                       "tab return folded the list's top under the retained field")
        // Then with the list left part way, where an empty field would settle.
        let from = agentsList.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.65))
        from.press(forDuration: 0.05, thenDragTo: agentsList.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.62)),
                   withVelocity: .slow, thenHoldForDuration: 0.2)
        sleep(1)
        let filtersY = app.buttons["agents.filter.all"].firstMatch.frame.midY
        sleep(1)
        XCTAssertEqual(app.buttons["agents.filter.all"].firstMatch.frame.midY, filtersY, accuracy: 1,
                       "idle settling moved the results under the retained field")
        leaveAndReturnToAgents()
        shot("query-08-tab-return")
        _ = assertRetainedAtRest("Agents returned to front")
        XCTAssertEqual(app.buttons["agents.filter.all"].firstMatch.frame.midY, filtersY, accuracy: 1,
                       "tab return folded the results under the retained field")
    }

    @MainActor func testClearingAfterSmallRetainedDragsSettlesEmptyField() throws {
        launch()
        try XCTSkipIf(regularWidth, "regular-width full search has its own regression below")
        for (name, distance, reveals) in [("show", 20.0, true), ("fold", 40.0, false)] {
            if name != "show" { launch() }
            _ = beginQuery()
            app.searchFields.firstMatch.typeText("\n")
            openResultAndReturn()
            _ = assertRetainedAtRest("before the \(name) small drag")
            let restY = app.buttons["agents.filter.all"].firstMatch.frame.midY
            let from = agentsList.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.65))
            let to = from.withOffset(CGVector(dx: 0, dy: -distance))
            from.press(forDuration: 0.2, thenDragTo: to, withVelocity: .slow, thenHoldForDuration: 0.5)
            sleep(1)
            let draggedY = app.buttons["agents.filter.all"].firstMatch.frame.midY
            let scrolled = restY - draggedY
            if reveals {
                XCTAssertGreaterThan(scrolled, 5)
                XCTAssertLessThan(scrolled, band / 2 - 2)
            } else {
                XCTAssertGreaterThan(scrolled, band / 2 + 2)
                XCTAssertLessThan(scrolled, band - 4)
            }
            let retained = assertRetainedAtRest("retained after the \(name) small drag")
            clearRetainedByTouch(retained)
            sleep(2)
            let settledY = app.buttons["agents.filter.all"].firstMatch.frame.midY
            XCTAssertEqual(settledY, reveals ? restY : restY - band, accuracy: 2,
                           "clearing after a \(scrolled) pt retained drag left the empty field half folded")
            XCTAssertEqual(app.searchFields.firstMatch.isHittable, reveals,
                           "the settled empty field's hit testing does not match its position")
            shot("query-09-clear-small-\(name)")
            app.terminate()
        }
    }

    @MainActor func testEmptyFieldKeepsItsExistingFoldAndReveal() throws {
        launch()
        try XCTSkipIf(regularWidth, "regular-width full search has its own regression below")
        XCTAssertFalse(app.searchFields.firstMatch.isHittable, "the initial empty field should be folded")
        shot("empty-01-initial-fold")
        _ = revealSearch()
        shot("empty-02-revealed")
        scrollFar()
        XCTAssertFalse(app.searchFields.firstMatch.isHittable, "scrolling must still fold an empty field")
        shot("empty-03-scrolled-fold")
        leaveAndReturnToAgents()
        XCTAssertFalse(app.searchFields.firstMatch.isHittable, "returning to Agents must still leave an empty field folded")
        shot("empty-04-tab-return-fold")
        toTop()
        _ = revealSearch()
        shot("empty-05-revealed-again")
    }

    @MainActor func testRegularFullSearchStaysVisibleWhileScrollingAndClearing() throws {
        launch()
        try XCTSkipUnless(regularWidth, "compact custom-header regressions above cover this layout")
        app.buttons["sidebar-search"].firstMatch.tap()
        let field = assertFullSearchVisible("regular search opened")
        shot("regular-empty-01-opened")
        let scope = app.descendants(matching: .any)["full-search-scope"].firstMatch
        let agents = scope.buttons["Agents"].firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 5), "no Agents scope in regular search")
        if !agents.isSelected { agents.tap() }
        field.tap()
        field.typeText(query)
        XCTAssertEqual(field.value as? String, query)
        shot("regular-query-01-keyboard")

        scrollFullSearch()
        // Search visibility, not the simulator's interactive keyboard gesture, is this test's subject.
        dismissSoftwareKeyboard()
        _ = assertFullSearchVisible("regular query scrolled", expected: query)
        shot("regular-query-02-scrolled")
        sleep(3)
        _ = assertFullSearchVisible("regular query idle", expected: query)

        field.tap()
        XCTAssertTrue(app.keyboards.firstMatch.waitForExistence(timeout: 5), "the retained regular field did not reopen the keyboard")
        field.typeText("x")
        XCTAssertEqual(field.value as? String, query + "x")
        field.typeText(XCUIKeyboardKey.delete.rawValue)
        XCTAssertEqual(field.value as? String, query)
        dismissSoftwareKeyboard()

        let clear = app.buttons["full-search-clear"].firstMatch
        XCTAssertTrue(clear.waitForExistence(timeout: 5) && clear.isHittable, "no reachable regular-search clear button")
        clear.tap()
        XCTAssertTrue(clear.waitForNonExistence(timeout: 5), "clearing left the regular query in place")
        _ = assertFullSearchVisible("regular search cleared")
        shot("regular-empty-02-cleared")
        app.buttons["full-search-close"].firstMatch.tap()
        XCTAssertTrue(field.waitForNonExistence(timeout: 5), "regular search did not close")
        shot("regular-empty-03-list")
    }
}
