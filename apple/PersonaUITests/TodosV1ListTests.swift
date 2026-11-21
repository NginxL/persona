import XCTest

/// The V1 To-dos list (#10465, docs/apple/ios.md "To-dos (V1, #10465)") against the mock hub's To-dos view, in the
/// Sidebar layout on an iPhone, whose drawer has both the To-dos row and the Pages heading that folds it.
/// `-showTodosV1 YES` stands for the release; the list still needs a hub that lists `todo_view`.
///
/// - `PERSONA_TODOS_HUB` (`MOCK_TODO_VIEW=1`): the drawer's To-dos row, the folded Pages heading, the bar's Needs you
///   and the Needs you header say one N.
/// - `PERSONA_TODOS_LATE_HUB` (`MOCK_TODO_VIEW=late`): a hello that lists the view only after a reconnect never swaps the
///   page that is up, nor the one a to-do was pushed over; the next time To-dos opens, it is the V1 list.
/// - `PERSONA_TODOS_FRAMES=1` with `PERSONA_SHOTS=<dir>` (and `PERSONA_TODOS_HUB`): the V1 frames this list draws, saved
///   for review (`PERSONA_TODOS_SCHEME` light or dark, `PERSONA_TODOS_LANGUAGE`, `PERSONA_TODOS_COLOR`); with
///   `PERSONA_TODOS_TODAY=1` instead, today's list, drawer and folded Pages heading with the gate off, the same on a build
///   of `main` and of this branch (before = after). `testActionFrames` draws the rows' actions the same way.
/// - The rows' writes against `PERSONA_TODOS_HUB`: a tick and its Undo, the Someday swipe, Delete… and its Undo, each
///   checked on the mock after the Undo window (the mock is put back after each); offline, the link said once.
final class TodosV1ListTests: XCTestCase {
    private var app: XCUIApplication!
    private let env = ProcessInfo.processInfo.environment

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .phone else { throw XCTSkip("iPhone only") }
    }

    @MainActor private func launch(_ hub: String, _ extra: [String] = [], layout: String = "sidebar", v1: Bool = true) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "todos", "-app.navigation", layout]
            + (v1 ? ["-showTodosV1", "YES"] : []) + extra
        app.launchPinned()
    }

    /// What the To-dos page draws and whether the gate is on (the app's `todos-page` probe, `-probes YES`).
    private struct PageProbe: Decodable { var v1: Bool; var gate: Bool }

    /// Back from a pushed page: the system's back button, else the edge swipe.
    @MainActor private func back() {
        let system = app.navigationBars.buttons.matching(identifier: "BackButton").allElementsBoundByIndex.first(where: \.isHittable)
        if let system { system.tap(); return }
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.0, dy: 0.5))
            .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.1)
    }

    /// The rows of the list in sight under the bar, top first.
    @MainActor private func rowsInSight() -> [XCUIElement] {
        // iOS 26 pins the bar over the list; on iOS 18 it is the list's first row and scrolls away.
        let bar = element("todos.bar.all")
        let floor = max(bar.exists ? bar.frame.maxY : 0, app.navigationBars.firstMatch.frame.maxY)
        let height = app.windows.firstMatch.frame.height
        return app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.row.'"))
            .allElementsBoundByIndex.filter { $0.frame.midY > floor && $0.frame.midY < height }
            .sorted { $0.frame.minY < $1.frame.minY }
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    /// ☰ at a place's root, then a place in the drawer; ☰ tried again while the page under it still settles.
    @MainActor private func open(_ place: String) {
        let menu = element("shell-place-sidebar"), row = element(place)
        XCTAssertTrue(menu.waitForExistence(timeout: 20), "no ☰:\n\(app.debugDescription)")
        for _ in 0..<3 where !row.exists {
            if menu.isHittable { menu.tap() }
            _ = row.waitForExistence(timeout: 5)
        }
        XCTAssertTrue(row.exists, "no \(place) in the drawer:\n\(app.debugDescription)")
        row.tap()
    }

    /// The last number in a spoken label ("Needs you, 3"): the count a badge adds to its row.
    private func number(_ label: String) -> Int? {
        label.split(whereSeparator: { !$0.isNumber }).last.flatMap { Int($0) }
    }

    @MainActor func testOneNumberEverywhere() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        launch(hub)
        let bar = element("todos.bar.needs_you")
        XCTAssertTrue(bar.waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        let header = element("todos.section.needs_you")
        XCTAssertTrue(header.waitForExistence(timeout: 20))
        let n = try XCTUnwrap(number(bar.label), bar.label)
        XCTAssertGreaterThan(n, 0)
        XCTAssertEqual(header.value as? String, String(n), "the Needs you header")

        element("sidebar-drawer-button").tap()
        let row = element("sidebar-todos")
        XCTAssertTrue(row.waitForExistence(timeout: 10), "no drawer:\n\(app.debugDescription)")
        XCTAssertEqual(number(row.label), n, "the drawer's To-dos row: \(row.label)")
        let places = element("sidebar-fold-places")
        places.tap()
        let tally = element("sidebar-fold-places.todos")
        XCTAssertTrue(tally.waitForExistence(timeout: 10), "no tally on the folded Pages heading:\n\(app.debugDescription)")
        XCTAssertEqual(tally.value as? String, String(n), "the folded Pages heading")
        places.tap() // unfolded again, as the next run expects
        XCTAssertTrue(row.waitForExistence(timeout: 10))
    }

    @MainActor func testAHelloThatComesLateNeverSwapsThePageUp() throws {
        guard let hub = env["PERSONA_TODOS_LATE_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_VIEW=late (PERSONA_TODOS_LATE_HUB)")
        }
        // The mock's hellos from the start of their sequence, as a fresh mock's, also after an earlier run against it; no
        // app of an earlier run connected to start it early.
        XCUIApplication().terminate()
        _ = try mock(try XCTUnwrap(URL(string: hub)), "GET", "mock/reset")
        // Nothing remembered from an earlier run: the first hello decides, and it lacks the view. The phone's own layout:
        // ☰ opens the drawer from any place.
        launch(hub, ["-hubTodoFeatures.", "0", "-probes", "YES"], layout: "separate")
        let today = element("todos.filter")
        XCTAssertTrue(today.waitForExistence(timeout: 60), "today's list first:\n\(app.debugDescription)")
        // The mock's hellos: without, with, without, with, MOCK_TODO_VIEW_LATE_SECONDS (5 s) apart. The gate comes on
        // inside the window (else the test proves nothing), and the page stays today's list.
        let until = Date().addingTimeInterval(22)
        var gateCame = false
        while Date() < until {
            XCTAssertFalse(element("todos.v1").exists, "the page swapped under the owner")
            XCTAssertTrue(today.exists, "today's list went")
            let probe = app.probe("todos-page", as: PageProbe.self)
            XCTAssertEqual(probe?.v1, false, "the page's own choice moved")
            gateCame = gateCame || probe?.gate == true
            sleep(1)
        }
        XCTAssertTrue(gateCame, "no hello with the view came in the window: \(String(describing: app.probeEnvelope("todos-page")))")
        // A to-do pushed over today's list and popped: still today's list (the page decides once, never on a pop). On
        // iOS 18 today's list opens no to-do in the phone's own layout, on main as well, so the pop is checked on iOS 26.
        if #available(iOS 26.0, *) {
            let row = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.row.'")).firstMatch
            XCTAssertTrue(row.waitForExistence(timeout: 10), "no row on today's list:\n\(app.debugDescription)")
            let page = element("todo.toggleDone")
            for _ in 0..<3 where !page.exists {
                row.tap()
                _ = page.waitForExistence(timeout: 8)
            }
            XCTAssertTrue(page.exists, "no to-do page:\n\(app.debugDescription)")
            back()
            XCTAssertTrue(today.waitForExistence(timeout: 10), "today's list after Back:\n\(app.debugDescription)")
            XCTAssertFalse(element("todos.v1").exists, "Back swapped the page")
        }
        // Opened again, by way of the Inbox: the V1 list.
        open("sidebar-inbox")
        XCTAssertTrue(element("inbox.more").waitForExistence(timeout: 20), "no Inbox:\n\(app.debugDescription)")
        open("sidebar-todos")
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 20), "the V1 list the next time:\n\(app.debugDescription)")
    }

    /// The calm rule: with only the owner's plain to-dos left, no bar and no section headers; Someday stays folded at the
    /// end. The mock's other to-dos are deleted for it and put back after (`/mock/reset`).
    @MainActor func testTheCalmListHasNoBar() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        let base = try XCTUnwrap(URL(string: hub))
        let call = { (method: String, path: String) in try self.mock(base, method, path) }
        defer { _ = try? call("GET", "mock/reset") }
        struct List: Decodable { struct Row: Decodable { var id: String }; var todos: [Row] }
        let plain = ["t11", "t12", "t14"]
        for row in try JSONDecoder().decode(List.self, from: call("GET", "api/v1/todos")).todos
        where !plain.contains(row.id) && !row.id.hasPrefix("t12-") {
            _ = try call("DELETE", "api/v1/todos/" + row.id)
        }
        launch(hub)
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        XCTAssertTrue(element("todos.row.t11").waitForExistence(timeout: 20))
        XCTAssertTrue(element("todos.fold.someday").exists, "Someday folded at the end")
        XCTAssertFalse(element("todos.bar.all").exists, "a calm list draws no bar")
        XCTAssertFalse(element("todos.section.todo").exists, "nor section headers")
    }

    /// The frames the list draws, for the review (no checks beyond reaching them).
    @MainActor func testFrames() throws {
        guard env["PERSONA_TODOS_FRAMES"] == "1", let hub = env["PERSONA_TODOS_HUB"], let dir = env["PERSONA_SHOTS"] else {
            throw XCTSkip("PERSONA_TODOS_FRAMES=1 with PERSONA_TODOS_HUB and PERSONA_SHOTS")
        }
        let scheme = env["PERSONA_TODOS_SCHEME"] ?? "light"
        let language = env["PERSONA_TODOS_LANGUAGE"] ?? "en"
        let locale = ["en": "en_US", "zh-Hans": "zh_CN", "ja": "ja_JP"][language] ?? "en_US"
        var extra = ["-ui.scheme", scheme, "-AppleLanguages", "(\(language))", "-AppleLocale", locale]
        if let color = env["PERSONA_TODOS_COLOR"] { extra += ["-ui.color", color] }
        if let size = env["PERSONA_TODOS_SIZE"] { extra += ["-UIPreferredContentSizeCategoryName", size] }
        let frames = Set((env["PERSONA_TODOS_FRAMES_ONLY"] ?? "").split(separator: ",").map(String.init))
        // `list` names the list as it opens (its frame id has no state).
        let wants = { (frame: String) in frames.isEmpty || frames.contains(frame.isEmpty ? "list" : frame) }
        let suffix = env["PERSONA_TODOS_SUFFIX"].map { "-" + $0 } ?? ""
        let shot = { (frame: String) in
            sleep(2) // the bar's reveal and the rows' marks settle
            let png = XCUIScreen.main.screenshot().pngRepresentation
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("iphone-list\(frame)\(suffix)-\(scheme).png"))
        }
        // The iPhone's own layout, as the frames draw it: ☰ on the list, the shell's drawer.
        launch(hub, extra, layout: "separate")
        // The list; a calm one has no bar.
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        _ = element("todos.bar.all").waitForExistence(timeout: 10)
        if wants("") { shot("") }
        // A menu goes with a tap beside it, at the page's leading edge (it opens from the trailing one), then the page is
        // there to tap again.
        let away = {
            self.app.coordinate(withNormalizedOffset: CGVector(dx: 0.04, dy: 0.6)).tap()
            let bar = self.element("todos.bar.needs_you")
            for _ in 0..<10 where !bar.isHittable { usleep(300_000) }
        }
        if wants("~more") {
            element("todos.more").tap()
            shot("~more")
            away()
        }
        if wants("~group") {
            XCTAssertTrue(element("todos.grouping").waitForExistence(timeout: 10), "no grouping menu:\n\(app.debugDescription)")
            element("todos.grouping").tap()
            shot("~group")
            away()
        }
        for (frame, id) in [("~filter-needs", "needs_you"), ("~filter-progress", "in_progress"), ("~filter-todo", "todo")] where wants(frame) {
            element("todos.bar." + id).tap()
            shot(frame)
        }
        if wants("~done"), element("todos.bar.done").exists {
            element("todos.bar.done").tap()
            shot("~done")
        }
        if wants("~scrolled") {
            element("todos.bar.all").tap()
            app.collectionViews.firstMatch.swipeUp(velocity: .slow)
            shot("~scrolled")
            // The list's end: Just finished, the closed rows, Someday and Done today folded.
            app.collectionViews.firstMatch.swipeUp(velocity: .fast)
            app.collectionViews.firstMatch.swipeUp(velocity: .fast)
            shot("~scrolled-end")
            app.collectionViews.firstMatch.swipeDown(velocity: .fast)
            app.collectionViews.firstMatch.swipeDown(velocity: .fast)
            app.collectionViews.firstMatch.swipeDown(velocity: .fast)
        }
        if wants("~canwait-open") {
            element("todos.bar.all").tap()
            let fold = element("todos.fold.can_wait")
            if fold.waitForExistence(timeout: 5) {
                fold.tap()
                shot("~canwait-open")
                fold.tap()
            }
        }
        if wants("~drawer") {
            element("todos.bar.all").tap()
            element("shell-place-sidebar").tap()
            XCTAssertTrue(element("sidebar-todos").waitForExistence(timeout: 10))
            shot("~drawer")
        }
        // The groupings, each as the list opens with it (kept on the device: `todos.v1.grouping`).
        for (frame, grouping) in [("~group-project", "project"), ("~group-due", "due")] where wants(frame) {
            app.terminate()
            launch(hub, extra + ["-todos.v1.grouping", grouping], layout: "separate")
            XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
            _ = element("todos.bar.all").waitForExistence(timeout: 10)
            shot(frame)
        }
        // A choice with nothing under it: Needs You, the to-dos that need the owner deleted on the mock (put back after).
        if wants("~filter-needs-empty") {
            let base = try XCTUnwrap(URL(string: hub))
            defer { _ = try? mock(base, "GET", "mock/reset") }
            struct List: Decodable {
                struct Row: Decodable { struct View: Decodable { var section: String }; var id: String; var view: View? }
                var todos: [Row]
            }
            for row in try JSONDecoder().decode(List.self, from: mock(base, "GET", "api/v1/todos")).todos
            where ["needs_you", "can_wait"].contains(row.view?.section ?? "") {
                _ = try mock(base, "DELETE", "api/v1/todos/" + row.id)
            }
            app.terminate()
            launch(hub, extra, layout: "separate")
            XCTAssertTrue(element("todos.bar.needs_you").waitForExistence(timeout: 60), "no bar:\n\(app.debugDescription)")
            element("todos.bar.needs_you").tap()
            shot("~filter-needs-empty")
        }
    }

    /// Today's To-dos with the gate off, as `main` draws them (plan 2.6.1: before = after): the list, the drawer with the
    /// To-dos row and its count, and the Pages heading folded with its tally. The fold is put back after. Rows say their
    /// ages from now, so with `PERSONA_MOCK_STARTED` (when the mock started, Unix seconds; its times are whole minutes
    /// before that) the shots wait until 70 s after it, and two runs say the same ages.
    @MainActor func testTodayFrames() throws {
        guard env["PERSONA_TODOS_TODAY"] == "1", let hub = env["PERSONA_TODOS_HUB"], let dir = env["PERSONA_SHOTS"] else {
            throw XCTSkip("PERSONA_TODOS_TODAY=1 with PERSONA_TODOS_HUB and PERSONA_SHOTS")
        }
        let scheme = env["PERSONA_TODOS_SCHEME"] ?? "light"
        let shot = { (frame: String) in
            sleep(2)
            let png = XCUIScreen.main.screenshot().pngRepresentation
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("iphone-today\(frame)-\(scheme).png"))
        }
        launch(hub, ["-ui.scheme", scheme, "-AppleLanguages", "(en)", "-AppleLocale", "en_US"], v1: false)
        XCTAssertTrue(element("todos.filter").waitForExistence(timeout: 60), "no To-dos:\n\(app.debugDescription)")
        if let started = env["PERSONA_MOCK_STARTED"].flatMap(Double.init) {
            while Date().timeIntervalSince1970 - started < 70 { usleep(200_000) }
        }
        shot("")
        element("sidebar-drawer-button").tap()
        XCTAssertTrue(element("sidebar-todos").waitForExistence(timeout: 10), "no drawer:\n\(app.debugDescription)")
        shot("~drawer")
        let places = element("sidebar-fold-places")
        places.tap()
        shot("~drawer-folded")
        places.tap()
        XCTAssertTrue(element("sidebar-todos").waitForExistence(timeout: 10), "the Pages heading unfolded again")
    }

    /// The list is put back where it rested when its page is made again (another place in between), and stays where it
    /// is when a to-do pushed over it pops.
    @MainActor func testTheListStaysWhereItRested() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        launch(hub, layout: "separate")
        XCTAssertTrue(element("todos.bar.all").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        sleep(1)
        let first = rowsInSight().first?.identifier
        let list = app.collectionViews.firstMatch
        list.swipeUp(velocity: .slow)
        list.swipeUp(velocity: .slow)
        sleep(2) // at rest
        let top = try XCTUnwrap(rowsInSight().first, "no rows in sight")
        let id = top.identifier, frame = top.frame
        XCTAssertNotEqual(id, first, "scrolled past the first rows")
        // A to-do pushed over the list, then Back: not moved.
        top.tap()
        XCTAssertTrue(element("todo.toggleDone").waitForExistence(timeout: 20), "no to-do page:\n\(app.debugDescription)")
        back()
        let again = element(id)
        XCTAssertTrue(again.waitForExistence(timeout: 10))
        sleep(1)
        XCTAssertEqual(again.frame.minY, frame.minY, accuracy: 2, "Back moved the list")
        // Another place, then To-dos again: a page made anew, put back where the list rested.
        open("sidebar-inbox")
        XCTAssertTrue(element("inbox.more").waitForExistence(timeout: 20), "no Inbox:\n\(app.debugDescription)")
        open("sidebar-todos")
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 20), "no V1 list:\n\(app.debugDescription)")
        sleep(2)
        XCTAssertTrue(rowsInSight().prefix(2).map(\.identifier).contains(id), "not put back to \(id): \(rowsInSight().map(\.identifier))")
    }

    // MARK: The rows' writes (the tick, the swipe, the menu)

    /// A to-do as the mock hub has it now: whether it is closed and whether it is in Someday; nil once it is gone.
    private struct MockTodo: Decodable {
        struct Row: Decodable {
            var done_at: String?
            var someday_until: String?
        }
        var todo: Row
    }

    private func mockTodo(_ base: URL, _ id: String) throws -> MockTodo.Row? {
        try? JSONDecoder().decode(MockTodo.self, from: mock(base, "GET", "api/v1/todos/" + id)).todo
    }

    /// The list, opened in the iPhone's own layout and scrolled to its end, where `id`'s row is in sight clear of the bars
    /// and of New To-Do at the foot.
    @MainActor private func openList(_ hub: String, showing id: String) {
        launch(hub, ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"], layout: "separate")
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        _ = element("todos.bar.all").waitForExistence(timeout: 10)
        toEnd()
        bring(id)
        let row = element("todos.row." + id)
        XCTAssertTrue(row.exists && clear(row), "no row \(id) in sight:\n\(app.debugDescription)")
    }

    /// The list scrolled to its end, at rest.
    @MainActor private func toEnd() {
        app.collectionViews.firstMatch.swipeUp(velocity: .fast)
        app.collectionViews.firstMatch.swipeUp(velocity: .fast)
        sleep(2)
    }

    /// The list moved a little at a time until `id`'s row is clear (`clear`).
    @MainActor private func bring(_ id: String) {
        let row = element("todos.row." + id), window = app.windows.firstMatch
        for _ in 0..<8 where !(row.exists && clear(row)) {
            let up = row.exists && row.frame.minY > window.frame.height / 2
            let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.55))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: up ? -140 : 140)), withVelocity: .slow,
                       thenHoldForDuration: 0.3)
            sleep(1)
        }
    }

    /// Whether `row` is in sight between the bars and New To-Do, where a finger reaches it.
    @MainActor private func clear(_ row: XCUIElement) -> Bool {
        let pill = element("todos.new")
        let floor = pill.exists ? pill.frame.minY : app.windows.firstMatch.frame.height
        // iOS 26 pins the bar over the list; on iOS 18 it is the list's first row and scrolls away.
        let bar = element("todos.bar.all")
        var top = max(bar.exists ? bar.frame.maxY : 0, app.navigationBars.firstMatch.frame.maxY)
        // A section's heading pinned under the bar covers the rows scrolling under it.
        let headings = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.section.'"))
        for heading in headings.allElementsBoundByIndex where heading.frame.minY <= top + 8 && heading.frame.maxY > top {
            top = heading.frame.maxY
        }
        return row.isHittable && row.frame.minY > top && row.frame.maxY < floor
    }

    /// The row's trailing swipe, drawn open (not the full swipe, which is Done).
    @MainActor private func revealSwipe(_ id: String) {
        let row = element("todos.row." + id)
        row.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5))
            .press(forDuration: 0.05, thenDragTo: row.coordinate(withNormalizedOffset: CGVector(dx: 0.45, dy: 0.5)),
                   withVelocity: .slow, thenHoldForDuration: 0.2)
    }

    /// A menu's or a swipe's button by its identifier, else by its words in English, Chinese or Japanese. The identifier
    /// is asked for first; UIKit does not always carry it onto a swipe's or a dialog's button, and the words are the
    /// fallback only then, which the log says ("found by its words").
    @MainActor private func button(_ id: String, _ words: String...) -> XCUIElement {
        // A confirmation dialog lists its buttons twice (the sheet and its accessibility copy): the one a finger reaches.
        let byID = app.buttons.matching(identifier: id)
        if byID.firstMatch.waitForExistence(timeout: 2) {
            return byID.allElementsBoundByIndex.first(where: \.isHittable) ?? byID.firstMatch
        }
        print("todos-ui: \(id) found by its words")
        return app.buttons.matching(NSPredicate(format: "label IN %@", words)).firstMatch
    }

    /// The list moved until `id`'s row is clear and in the lower half of the screen, where a dialog's popover pointing
    /// at it shows whether it points at the row it asks about.
    @MainActor private func lower(_ id: String) {
        let row = element("todos.row." + id), window = app.windows.firstMatch
        for _ in 0..<8 where !(row.exists && clear(row) && row.frame.minY > window.frame.height * 0.55) {
            let down = !row.exists || row.frame.minY < window.frame.height * 0.55
            let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: down ? 120 : -120)), withVelocity: .slow,
                       thenHoldForDuration: 0.3)
            sleep(1)
        }
    }

    /// The tick is drawn at once and Undo sends nothing; left alone, the to-do closes on the hub after the window.
    @MainActor func testTickAndUndo() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        let base = try XCTUnwrap(URL(string: hub))
        defer { _ = try? mock(base, "GET", "mock/reset") }
        openList(hub, showing: "t11")
        let tick = element("todos.tick.t11")
        XCTAssertFalse(tick.isSelected)
        tick.tap()
        XCTAssertTrue(tick.waitForSelected(true), "ticked at once")
        let undo = app.buttons["toast-action"]
        XCTAssertTrue(undo.waitForExistence(timeout: 3), "Done · Undo:\n\(app.debugDescription)")
        undo.tap()
        XCTAssertTrue(tick.waitForSelected(false), "Undo unticks it")
        sleep(6) // past the Undo window: nothing may have gone
        XCTAssertNil(try mockTodo(base, "t11")?.done_at, "Undo sent nothing")
        tick.tap()
        XCTAssertTrue(tick.waitForSelected(true))
        let until = Date().addingTimeInterval(20)
        while Date() < until, try mockTodo(base, "t11")?.done_at == nil { sleep(1) }
        XCTAssertNotNil(try mockTodo(base, "t11")?.done_at, "closed on the hub once the window ended")
    }

    /// Someday from the trailing swipe: the row leaves for the Someday fold at once, and the hub has it after the window.
    @MainActor func testSwipeSomeday() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        let base = try XCTUnwrap(URL(string: hub))
        defer { _ = try? mock(base, "GET", "mock/reset") }
        openList(hub, showing: "t11")
        revealSwipe("t11")
        let someday = button("todos.swipe.someday", "Someday", "以后再做", "いつか")
        XCTAssertTrue(someday.waitForExistence(timeout: 5), "no Someday in the swipe:\n\(app.debugDescription)")
        someday.tap()
        XCTAssertTrue(element("todos.row.t11").waitForNonExistence(timeout: 5), "folded under Someday at once")
        let until = Date().addingTimeInterval(20)
        while Date() < until, try mockTodo(base, "t11")?.someday_until == nil { sleep(1) }
        XCTAssertNotNil(try mockTodo(base, "t11")?.someday_until, "in Someday on the hub once the window ended")
    }

    /// Delete… asks first, naming the to-do; the row goes at once, Undo brings it back and nothing is deleted; left
    /// alone, the hub deletes it after the window.
    @MainActor func testDeleteAndUndo() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        let base = try XCTUnwrap(URL(string: hub))
        defer { _ = try? mock(base, "GET", "mock/reset") }
        openList(hub, showing: "t11")
        let delete = {
            self.element("todos.row.t11").press(forDuration: 1.2)
            let item = self.button("todos.menu.delete", "Delete…", "删除…", "削除…")
            if !item.waitForExistence(timeout: 5) { XCTFail("no Delete… in the menu:\n\(self.app.debugDescription)") }
            item.tap()
            let confirm = self.button("todos.delete.confirm", "Delete To-Do", "删除待办", "ToDoを削除")
            if !confirm.waitForExistence(timeout: 5) { XCTFail("Delete… asks first:\n\(self.app.debugDescription)") }
            confirm.tap()
        }
        delete()
        XCTAssertTrue(element("todos.row.t11").waitForNonExistence(timeout: 5), "gone at once")
        let undo = app.buttons["toast-action"]
        XCTAssertTrue(undo.waitForExistence(timeout: 3))
        undo.tap()
        XCTAssertTrue(element("todos.row.t11").waitForExistence(timeout: 5), "Undo brings it back")
        sleep(6)
        XCTAssertNotNil(try mockTodo(base, "t11"), "Undo deleted nothing")
        delete()
        let until = Date().addingTimeInterval(20)
        while Date() < until, try mockTodo(base, "t11") != nil { sleep(1) }
        XCTAssertNil(try mockTodo(base, "t11"), "deleted on the hub once the window ended")
    }

    /// Offline, the list's own line says the hub is out of reach, and the shell's pill does not say it again over the
    /// bar's counts (docs/apple/shell.md "Said once").
    @MainActor func testOfflineIsSaidOnce() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        launch(hub, ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-simulateOffline", "YES"], layout: "separate")
        XCTAssertTrue(element("todos.offline").waitForExistence(timeout: 30), "no offline line:\n\(app.debugDescription)")
        sleep(2) // the pill would have come by now
        XCTAssertFalse(element("shell-connection").exists, "said twice:\n\(app.debugDescription)")
    }

    /// The rows' actions as the frames draw them, for the review: the swipe and its other forms (Undo, Reopen, Remove
    /// from Someday), a tick with its Undo, the long-press menu and its Project, Date…, Delete…'s and the working tick's
    /// confirmations from rows low on the page, Mark Done on a finished row seen a day ago, New To-Do, a new row being
    /// saved and one the hub could not be reached for, a row under a pinned heading, and the offline line. The mock is
    /// put back before each.
    @MainActor func testActionFrames() throws {
        guard env["PERSONA_TODOS_FRAMES"] == "1", let hub = env["PERSONA_TODOS_HUB"], let dir = env["PERSONA_SHOTS"] else {
            throw XCTSkip("PERSONA_TODOS_FRAMES=1 with PERSONA_TODOS_HUB and PERSONA_SHOTS")
        }
        let base = try XCTUnwrap(URL(string: hub))
        defer {
            _ = try? mock(base, "GET", "mock/delay?s=0")
            _ = try? mock(base, "GET", "mock/reset")
        }
        let scheme = env["PERSONA_TODOS_SCHEME"] ?? "light"
        let language = env["PERSONA_TODOS_LANGUAGE"] ?? "en"
        let locale = ["en": "en_US", "zh-Hans": "zh_CN", "ja": "ja_JP"][language] ?? "en_US"
        var extra = ["-ui.scheme", scheme, "-AppleLanguages", "(\(language))", "-AppleLocale", locale]
        if let color = env["PERSONA_TODOS_COLOR"] { extra += ["-ui.color", color] }
        if let size = env["PERSONA_TODOS_SIZE"] { extra += ["-UIPreferredContentSizeCategoryName", size] }
        let frames = Set((env["PERSONA_TODOS_FRAMES_ONLY"] ?? "").split(separator: ",").map(String.init))
        let wants = { (frame: String) in frames.isEmpty || frames.contains(frame) }
        let shot = { (frame: String, wait: UInt32) in
            sleep(wait)
            let png = XCUIScreen.main.screenshot().pngRepresentation
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("iphone-list\(frame)-\(scheme).png"))
        }
        // Each from a fresh mock and launch, the list scrolled to its end as the frames draw it.
        let fresh = { (more: [String]) in
            _ = try? self.mock(base, "GET", "mock/delay?s=0")
            _ = try? self.mock(base, "GET", "mock/reset")
            self.app?.terminate()
            self.launch(hub, extra + more, layout: "separate")
            if !self.element("todos.v1").waitForExistence(timeout: 60) { XCTFail("no V1 list:\n\(self.app.debugDescription)") }
            _ = self.element("todos.bar.all").waitForExistence(timeout: 10)
            sleep(1)
        }
        let longPress = { (id: String) in self.element("todos.row." + id).press(forDuration: 1.2) }
        if wants("~swiped") {
            fresh([])
            toEnd()
            bring("t09")
            revealSwipe("t09")
            shot("~swiped", 1)
        }
        if wants("~ticked") {
            fresh([])
            toEnd()
            bring("t11")
            element("todos.tick.t11").tap()
            shot("~ticked", 1) // within the toast's 4 s
            element("todos.tick.t11").tap() // taken back: nothing goes, and the next launch has nothing to send
        }
        if wants("~swiped-undo") {
            fresh([])
            toEnd()
            bring("t11")
            // The hub slow to answer: the tick is still the device's while the swipe opens, past its 4 s window.
            _ = try? mock(base, "GET", "mock/delay?s=12")
            element("todos.tick.t11").tap()
            revealSwipe("t11")
            shot("~swiped-undo", 1)
            _ = try? mock(base, "GET", "mock/delay?s=0")
            sleep(14) // the close lands and is read back: nothing is left on the device for the next launch
        }
        if wants("~swiped-reopen") || wants("~swiped-someday-out") {
            fresh([])
            toEnd()
            for (frame, fold, id) in [("~swiped-reopen", "done", "t08"), ("~swiped-someday-out", "someday", "t14")] where wants(frame) {
                let line = element("todos.fold." + fold)
                if line.waitForExistence(timeout: 5), !element("todos.row." + id).exists { line.tap() }
                toEnd()
                bring(id)
                revealSwipe(id)
                shot(frame, 1)
                app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.2)).tap() // the swipe closes
                sleep(1)
            }
        }
        if wants("~row-menu") {
            fresh([])
            toEnd()
            bring("t11")
            longPress("t11")
            shot("~row-menu", 2)
        }
        if wants("~project") {
            fresh([])
            toEnd()
            bring("t11")
            longPress("t11")
            button("todos.menu.project", "Project", "项目", "プロジェクト").tap()
            shot("~project", 2)
        }
        if wants("~date") {
            fresh([])
            toEnd()
            bring("t11")
            longPress("t11")
            button("todos.menu.date", "Date…", "日期…", "日付…").tap()
            XCTAssertTrue(element("todos.due.day").waitForExistence(timeout: 10), "no Date… sheet:\n\(app.debugDescription)")
            shot("~date", 2)
        }
        if wants("~confirm-delete") {
            fresh([])
            toEnd()
            lower("t11")
            longPress("t11")
            button("todos.menu.delete", "Delete…", "删除…", "削除…").tap()
            shot("~confirm-delete", 2)
        }
        // Persona working on it, and an Agent waiting for the owner on it (the Widget's plan): each asks from its row.
        for (frame, id) in [("~confirm-stop", "t06"), ("~confirm-stop-agent", "t02")] where wants(frame) {
            fresh([])
            lower(id)
            element("todos.tick." + id).tap()
            shot(frame, 2)
        }
        if wants("~mark-done") {
            // Book the dentist, finished and seen a day ago: To do, its line ending in Mark Done.
            _ = try? mock(base, "GET", "mock/reset")
            _ = try mock(base, "GET", "mock/todo-view?seen_hours=25")
            _ = try mock(base, "PUT", "api/v1/todos/t07/seen")
            app?.terminate()
            launch(hub, extra, layout: "separate")
            XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
            toEnd()
            bring("t07")
            XCTAssertTrue(element("todos.markDone.t07").waitForExistence(timeout: 10), "no Mark Done:\n\(app.debugDescription)")
            shot("~mark-done", 1)
        }
        if wants("~header") {
            // A row half under a pinned heading, the list at rest.
            fresh([])
            let from = app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
            from.press(forDuration: 0.05, thenDragTo: from.withOffset(CGVector(dx: 0, dy: -230)), withVelocity: .slow,
                       thenHoldForDuration: 0.5)
            shot("~header", 1)
        }
        if wants("~new") || wants("~added") || wants("~saving") || wants("~new-unsaved") {
            let add = { (frame: String, before: (() -> Void)?) in
                fresh([])
                self.toEnd()
                self.bring("t11")
                self.element("todos.new").tap()
                let title = self.element("todos.new.title")
                XCTAssertTrue(title.waitForExistence(timeout: 10), "no New To-Do sheet:\n\(self.app.debugDescription)")
                title.typeText("Buy stamps")
                // A fresh simulator's keyboard first shows its own tip, a system button with no identifier: its English
                // word, which every launch here asks for.
                let tip = self.app.buttons["Continue"]
                if tip.waitForExistence(timeout: 2) { tip.tap() }
                if frame == "~new" { shot("~new", 1); return }
                before?()
                // The list was left at To do: the new row comes first in it, with "Added · Undo" over the list.
                self.element("todos.new.add").tap()
                shot(frame, frame == "~new-unsaved" ? 4 : 1)
            }
            if wants("~new") { add("~new", nil) }
            if wants("~added") { add("~added", nil) }
            // The hub slow to answer: "Saving…" while it is out. Then it lands and is read back before the next frame
            // puts the mock back, so nothing is left on the device or made on the mock after its reset.
            if wants("~saving") {
                add("~saving") { _ = try? self.mock(base, "GET", "mock/delay?s=8") }
                _ = try? mock(base, "GET", "mock/delay?s=0")
                sleep(10)
            }
            // The hub gone as it is sent: the row says it is not on the hub, or not sure it was saved. Then Remove, so
            // the next launches draw nothing of it.
            if wants("~new-unsaved") {
                add("~new-unsaved") { _ = try? self.mock(base, "GET", "mock/down?s=20") }
                let row = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.row.new-'")).firstMatch
                if row.waitForExistence(timeout: 5) {
                    row.press(forDuration: 1.2)
                    let remove = button("todos.menu.remove", "Remove", "移除", "取り除く")
                    if remove.waitForExistence(timeout: 5) { remove.tap() }
                }
                sleep(20) // the mock back up
            }
        }
        if wants("~offline") {
            fresh(["-simulateOffline", "YES"])
            XCTAssertTrue(element("todos.offline").waitForExistence(timeout: 15), "no offline line:\n\(app.debugDescription)")
            shot("~offline", 2)
            XCTAssertFalse(element("shell-connection").exists, "the link said twice")
        }
    }

    /// A call to the mock hub.
    private func mock(_ base: URL, _ method: String, _ path: String) throws -> Data {
        // A query rides after the path as it is ("mock/delay?s=8"): `appending(path:)` would escape its "?".
        let parts = path.split(separator: "?", maxSplits: 1).map(String.init)
        var url = base.appending(path: parts[0])
        if parts.count == 2, let queried = URL(string: url.absoluteString + "?" + parts[1]) { url = queried }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        var out: Data?
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { data, _, _ in out = data; done.fulfill() }.resume()
        wait(for: [done], timeout: 20)
        return try XCTUnwrap(out)
    }
}

private extension XCUIElement {
    /// Waits (up to 5 s) for a toggle-like element to say it is selected, or not.
    func waitForSelected(_ selected: Bool) -> Bool {
        for _ in 0..<25 {
            if isSelected == selected { return true }
            usleep(200_000)
        }
        return isSelected == selected
    }
}
