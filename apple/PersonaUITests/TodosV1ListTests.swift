import XCTest

/// The V1 To-dos list (#10465, docs/apple/ios.md "To-dos (V1, #10465)") against the mock hub's To-dos view, in the
/// Sidebar layout on an iPhone, whose drawer has both the To-dos row and the Pages heading that folds it.
/// `-showTodosV1 YES` stands for the release; the list still needs a hub that lists `todo_view`.
///
/// - `PERSONA_TODOS_HUB` (`MOCK_TODO_VIEW=1`): the drawer's To-dos row, the folded Pages heading, the Needs you header and
///   the guard line under a filter say one N; the foot toolbar's Filter, search and New To-Do.
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

    /// The rows of the list in sight under the navigation bar, top first.
    @MainActor private func rowsInSight() -> [XCUIElement] {
        let floor = app.navigationBars.firstMatch.frame.maxY
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
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        let header = element("todos.section.needs_you")
        XCTAssertTrue(header.waitForExistence(timeout: 20))
        let n = try XCTUnwrap((header.value as? String).flatMap { Int($0) }, String(describing: header.value))
        XCTAssertGreaterThan(n, 0)
        // A filter that leaves Needs you out says the same N on its guard line.
        choose("in_progress")
        let guardLine = element("todos.guard")
        XCTAssertTrue(guardLine.waitForExistence(timeout: 10), "no guard line:\n\(app.debugDescription)")
        XCTAssertEqual(number(guardLine.label), n, "the guard line: \(guardLine.label)")
        element("todos.filter.clear").tap()
        XCTAssertTrue(header.waitForExistence(timeout: 10))

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

    /// The calm rule: with only the owner's plain to-dos left, no Filter and no section headers, search and New To-Do
    /// still at the foot; Someday stays folded at the end. The mock's other to-dos are deleted for it and put back after
    /// (`/mock/reset`).
    @MainActor func testTheCalmListHasNoFilter() throws {
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
        XCTAssertFalse(element("todos.filter").exists, "a calm list draws no Filter")
        XCTAssertFalse(element("todos.section.todo").exists, "nor section headers")
        XCTAssertTrue(element("todos.new").exists, "New To-Do stays")
        // ⋯ still sorts, and offers no grouping: a calm list has one card.
        viewMenu().tap()
        XCTAssertTrue(menuItem("todos.view.sort.suggested", at: 0).waitForExistence(timeout: 5), "no Sort By in ⋯:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons.matching(identifier: "todos.view.byProject").firstMatch.exists, "no Group by Project while calm")
        let menus = app.collectionViews.allElementsBoundByIndex.filter { $0.identifier != "todos.v1" }
        if let menu = menus.last { XCTAssertEqual(menu.buttons.count, TodosV1ListTests.sorts.count, "the four sorts alone") }
    }

    /// ⋯'s sorts, in its order.
    private static let sorts = ["suggested", "activity", "due", "priority"]

    /// The rows are compact (DESIGN_SYSTEM 3.4 "To-dos list", the row): their one padding, 12 pt above and below, and
    /// none of the cell's own, and their ticks 16 pt inside the card. The list reads a row's insets only from its outermost
    /// view, and rows that set them inside their cell were 20 to 30 pt taller and, on iOS 18 and 27, 20 pt inside.
    @MainActor func testTheRowsAreCompact() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        launch(hub, ["-todos.v1.byProject", "NO", "-todos.v1.sort", "suggested"], layout: "separate")
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        // Two rows one under the other in Working: from the first's tick to the second's is the first's cell.
        let first = element("todos.tick.t05"), second = element("todos.tick.t06"), words = element("todos.row.t05")
        XCTAssertTrue(first.waitForExistence(timeout: 20) && second.exists && words.exists, "no Working rows:\n\(app.debugDescription)")
        let cell = second.frame.minY - first.frame.minY
        let padding = cell - words.frame.height
        // 12 above and below, and the tick ring rising a little above the title's line; the system's own margins on top
        // made it 59 pt.
        XCTAssertLessThanOrEqual(padding, 2 * 12 + 8, "the row's one padding, nothing more: a \(cell)-pt cell for \(words.frame.height) pt of words")
        // The tick under its heading's words, both 16 pt inside the card (iOS 26 and later: iOS 18 can set a heading's
        // side insets only with all four, so its headings keep the system's).
        if #available(iOS 26.0, *) {
            let heading = element("todos.section.working")
            XCTAssertTrue(heading.exists)
            XCTAssertEqual(first.frame.minX, heading.frame.minX, accuracy: 1.5, "the tick under the heading's words")
        }
    }

    /// A choice from the Filter's menu, by its identifier.
    @MainActor private func choose(_ choice: String) {
        // iOS 18 draws a choice that is on as "Filtered by: ‹choice›", which opens the same menu (its glyph is × there).
        let named = element("todos.filter.value")
        let filter = !element("todos.filter").exists && named.exists ? named : element("todos.filter")
        XCTAssertTrue(filter.waitForExistence(timeout: 20), "no Filter:\n\(app.debugDescription)")
        filter.tap()
        let order = ["all", "needs_you", "in_progress", "todo", "done"]
        let item = menuItem("todos.filter." + choice, at: order.firstIndex(of: choice) ?? 0)
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no \(choice) in the Filter's menu:\n\(app.debugDescription)")
        item.tap()
    }

    /// An open menu's item by its identifier, else by its place: SwiftUI carries no identifier onto a menu's items on
    /// iOS 26 and later, and these menus list their items in a fixed order (the Filter's `menuOrder(.fixed)`, ⋯'s
    /// sorts then Group by Project).
    @MainActor private func menuItem(_ id: String, at index: Int) -> XCUIElement {
        let byID = app.buttons.matching(identifier: id).allElementsBoundByIndex.first(where: \.isHittable)
        if let byID { return byID }
        let menus = app.collectionViews.allElementsBoundByIndex.filter { $0.identifier != "todos.v1" }
        guard let menu = menus.last else { return app.buttons[id] }
        return menu.buttons.element(boundBy: index)
    }

    /// ⋯: the page's own menu, or on iOS 27 built with its SDK the bar's overflow menu, which carries no identifier of
    /// ours: the navigation bar's trailing button.
    @MainActor private func viewMenu() -> XCUIElement {
        let ours = element("todos.view")
        if ours.waitForExistence(timeout: 3) { return ours }
        return app.navigationBars.firstMatch.buttons.allElementsBoundByIndex.last(where: \.isHittable) ?? ours
    }

    /// The search field, opened (on iOS 26 at the foot, folded to ⌕ while a filter is on; on iOS 18 under the title).
    @MainActor private func searchField() -> XCUIElement {
        let field = app.searchFields.firstMatch
        if !(field.exists && field.isHittable) {
            let button = app.buttons.matching(NSPredicate(format: "identifier == 'Search' OR label ==[c] 'search'")).allElementsBoundByIndex
                .first(where: \.isHittable)
            if let button { button.tap() } else { app.collectionViews.firstMatch.swipeDown(velocity: .slow) }
        }
        XCTAssertTrue(field.waitForExistence(timeout: 10), "no search field:\n\(app.debugDescription)")
        return field
    }

    /// The foot toolbar: the Filter narrows the list and names its choice; a choice that leaves Needs you out says so on
    /// a guard line, whose Show All is All again, as × is, the list back at its top; ⋯ holds Sort By (Suggested, Recently
    /// Changed, Due Date, Priority), the one checked kept, and Group by Project, greyed under Needs You; the search keeps
    /// the sections, sets the choice aside and ends with Search Chats, which opens the app's search over the chats with
    /// the words; + opens New To-Do. Launched in English only because the system's search controls (its ⌕, Cancel and
    /// ×) carry no identifier and are found by their words; what the test checks of ours is by identifier.
    @MainActor func testTheFilterSearchAndNewToDo() throws {
        guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
        // The arrangement as the list starts, whatever an earlier run left on the device.
        launch(hub, ["-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-todos.v1.byProject", "NO", "-todos.v1.sort", "suggested"],
               layout: "separate")
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        XCTAssertTrue(element("todos.section.needs_you").waitForExistence(timeout: 20))
        let sorts = Self.sorts
        let sort = { (id: String) in self.menuItem("todos.view.sort." + id, at: sorts.firstIndex(of: id) ?? 0) }
        let byProject = { self.menuItem("todos.view.byProject", at: sorts.count) }
        let project = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.section.project:'")).firstMatch
        // iOS 26 names the choice on the Filter itself; iOS 18 says "Filtered by: ‹choice›" between its glyph and +.
        let atRest = element("todos.filter").value as? String
        choose("in_progress")
        XCTAssertTrue(element("todos.guard").waitForExistence(timeout: 10), "no guard line:\n\(app.debugDescription)")
        XCTAssertFalse(element("todos.section.needs_you").exists, "Needs you is left out")
        XCTAssertTrue(element("todos.section.working").exists)
        let named = element("todos.filter").exists ? element("todos.filter") : element("todos.filter.value")
        let value = named.value as? String
        XCTAssertTrue(value?.isEmpty == false && value != atRest, "the Filter names its choice: \(value ?? "nil") at rest \(atRest ?? "nil")")
        element("todos.guard").tap()
        XCTAssertTrue(element("todos.section.needs_you").waitForExistence(timeout: 10), "Show All")
        XCTAssertFalse(element("todos.guard").exists)
        // A choice re-files the list from its top: down the list under In Progress, then × (All again, no guard line),
        // and the first heading is in sight.
        choose("in_progress")
        XCTAssertTrue(element("todos.guard").waitForExistence(timeout: 10))
        app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        sleep(1)
        let clearAll = element("todos.filter.clear")
        XCTAssertTrue(clearAll.waitForExistence(timeout: 5))
        clearAll.tap()
        let needsHeader = element("todos.section.needs_you")
        XCTAssertTrue(needsHeader.waitForExistence(timeout: 10))
        sleep(1)
        XCTAssertTrue(needsHeader.frame.minY >= app.navigationBars.firstMatch.frame.maxY - 2
                      && needsHeader.frame.maxY < app.windows.firstMatch.frame.height / 2,
                      "a choice puts the list at its top: \(needsHeader.frame) under \(app.navigationBars.firstMatch.frame)")
        choose("needs_you")
        XCTAssertTrue(element("todos.row.t01").waitForExistence(timeout: 10))
        XCTAssertFalse(element("todos.section.working").exists)
        XCTAssertFalse(element("todos.guard").exists, "nothing that needs the owner is hidden")
        // ⋯: the four sorts and Group by Project, greyed under Needs You; Due Date checked once chosen, then Suggested
        // again (the sort is kept on the device).
        viewMenu().tap()
        XCTAssertTrue(sort("suggested").waitForExistence(timeout: 5), "no Sort By in ⋯:\n\(app.debugDescription)")
        XCTAssertTrue(sort("activity").exists && sort("due").exists && sort("priority").exists)
        XCTAssertTrue(byProject().exists && !byProject().isEnabled, "Group by Project greyed under Needs You")
        sort("due").tap()
        viewMenu().tap()
        XCTAssertTrue(sort("due").waitForExistence(timeout: 5) && sort("due").isSelected, "Due Date checked")
        sort("suggested").tap()
        element("todos.filter.clear").tap()
        // Group by Project under All: a heading per project, Needs you still first; then off again (kept on the device).
        XCTAssertTrue(element("todos.section.working").waitForExistence(timeout: 10))
        viewMenu().tap()
        XCTAssertTrue(byProject().waitForExistence(timeout: 5) && byProject().isEnabled)
        byProject().tap()
        XCTAssertTrue(project.waitForExistence(timeout: 10), "a project's heading:\n\(app.debugDescription)")
        XCTAssertTrue(element("todos.section.needs_you").exists && !element("todos.section.working").exists)
        viewMenu().tap()
        XCTAssertTrue(byProject().waitForExistence(timeout: 5))
        byProject().tap()
        XCTAssertTrue(element("todos.section.working").waitForExistence(timeout: 10), "the sections again")
        // The search: the Kyoto trip by its title, under To Do; nothing else; then the widening row.
        choose("todo")
        let field = searchField()
        field.tap()
        field.typeText("Kyoto")
        XCTAssertTrue(element("todos.row.t01").waitForExistence(timeout: 10), "a match outside the choice:\n\(app.debugDescription)")
        XCTAssertFalse(element("todos.row.t05").exists, "a row that does not match")
        XCTAssertFalse(element("todos.guard").exists, "the choice is set aside while searching")
        XCTAssertTrue(element("todos.searchChats").waitForExistence(timeout: 5), "Search Chats for …")
        // Search Chats: the app's search over Persona's chats, with the words, and no Agents scope on an everyday page.
        element("todos.searchChats").tap()
        let chats = element("full-search-field")
        XCTAssertTrue(chats.waitForExistence(timeout: 10), "no full search:\n\(app.debugDescription)")
        XCTAssertEqual(chats.value as? String, "Kyoto", "the words go with it")
        XCTAssertFalse(element("full-search-scope").exists, "Persona's chats only")
        element("full-search-close").tap()
        XCTAssertTrue(chats.waitForNonExistence(timeout: 10))
        XCTAssertTrue(element("todos.searchChats").waitForExistence(timeout: 10), "back on the list's search")
        let cancel = app.buttons.matching(NSPredicate(format: "label ==[c] 'cancel' OR label ==[c] 'close'")).allElementsBoundByIndex
            .first(where: \.isHittable)
        try XCTUnwrap(cancel, "no way out of the search:\n\(app.debugDescription)").tap()
        XCTAssertTrue(element("todos.guard").waitForExistence(timeout: 10), "the choice is back once the field is cleared")
        element("todos.filter.clear").tap()
        // New To-Do from the foot.
        element("todos.new").tap()
        XCTAssertTrue(element("todos.new.title").waitForExistence(timeout: 10), "no New To-Do sheet:\n\(app.debugDescription)")
        element("sheet-close").tap()
        XCTAssertTrue(element("todos.new.title").waitForNonExistence(timeout: 10))
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
            sleep(2) // the menus' and the rows' marks settle
            let png = XCUIScreen.main.screenshot().pngRepresentation
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("iphone-list\(frame)\(suffix)-\(scheme).png"))
        }
        // Each from a fresh launch: the iPhone's own layout, as the frames draw it (☰ on the list, the shell's drawer).
        // The arrangement is said at every launch, since the page keeps what a frame launched with (Group by Project, a
        // sort) and the frames after it would draw it too.
        let arranged = ["-todos.v1.byProject", "NO", "-todos.v1.sort", "suggested"]
        let fresh = { (more: [String]) in
            self.app?.terminate()
            self.launch(hub, extra + (more.isEmpty ? arranged : more), layout: "separate")
            if !self.element("todos.v1").waitForExistence(timeout: 60) { XCTFail("no V1 list:\n\(self.app.debugDescription)") }
            _ = self.element("todos.new").waitForExistence(timeout: 10)
            sleep(1)
        }
        // A menu goes with a tap beside it, at the page's leading edge.
        let away = {
            self.app.coordinate(withNormalizedOffset: CGVector(dx: 0.04, dy: 0.45)).tap()
            sleep(1)
        }
        fresh([])
        if wants("") { shot("") }
        if wants("~filter-menu") {
            element("todos.filter").tap()
            shot("~filter-menu")
            away()
        }
        if wants("~view") {
            viewMenu().tap()
            shot("~view")
            away()
        }
        for (frame, id) in [("~filter-needs", "needs_you"), ("~filter-progress", "in_progress"), ("~filter-todo", "todo"),
                            ("~filter-done", "done")] where wants(frame) {
            choose(id)
            shot(frame)
        }
        // ⋯ under Needs You: Group by Project greyed, with why in its section's heading.
        if wants("~view-needs") {
            choose("needs_you")
            viewMenu().tap()
            shot("~view-needs")
            away()
            fresh([])
        }
        // Group by Project and a sort other than Suggested, each kept on the device, as the list opens with it (the
        // whole arrangement said, so a frame never draws the one before it).
        for (frame, more) in [("~group-project", ["-todos.v1.byProject", "YES", "-todos.v1.sort", "suggested"]),
                              ("~sorted-priority", ["-todos.v1.byProject", "NO", "-todos.v1.sort", "priority"]),
                              ("~sorted-due", ["-todos.v1.byProject", "NO", "-todos.v1.sort", "due"])]
        where wants(frame) {
            fresh(more)
            shot(frame)
            fresh([])
        }
        if wants("~filter-progress-scrolled") {
            choose("in_progress")
            app.collectionViews.firstMatch.swipeUp(velocity: .slow)
            shot("~filter-progress-scrolled") // the title's subtitle, once the guard line has gone (iOS 26 and later)
        }
        if wants("~filter-progress-menu") {
            choose("in_progress")
            element("todos.filter").tap()
            shot("~filter-progress-menu")
            away()
        }
        if wants("~scrolled") || wants("~end") || wants("~someday-open") || wants("~canwait-open") || wants("~drawer")
            || wants("~search") || wants("~search-results") || wants("~search-empty") {
            fresh([])
        }
        if wants("~canwait-open") {
            let fold = element("todos.fold.can_wait")
            if fold.waitForExistence(timeout: 5) {
                fold.tap()
                shot("~canwait-open")
                fold.tap()
            }
        }
        if wants("~scrolled") {
            app.collectionViews.firstMatch.swipeUp(velocity: .slow)
            shot("~scrolled")
        }
        if wants("~end") || wants("~someday-open") {
            // The list's end: Just finished, then Someday and Done today folded on the page after the last card.
            app.collectionViews.firstMatch.swipeUp(velocity: .fast)
            app.collectionViews.firstMatch.swipeUp(velocity: .fast)
            app.collectionViews.firstMatch.swipeUp(velocity: .fast)
            if wants("~end") { shot("~end") }
            if wants("~someday-open"), element("todos.fold.someday").exists {
                element("todos.fold.someday").tap()
                app.collectionViews.firstMatch.swipeUp(velocity: .fast)
                shot("~someday-open")
                element("todos.fold.someday").tap()
            }
            for _ in 0..<4 { app.collectionViews.firstMatch.swipeDown(velocity: .fast) }
        }
        if wants("~drawer") {
            element("shell-place-sidebar").tap()
            XCTAssertTrue(element("sidebar-todos").waitForExistence(timeout: 10))
            shot("~drawer")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap()
            sleep(1)
        }
        if wants("~search") || wants("~search-results") || wants("~search-empty") {
            let field = searchField()
            field.tap()
            if wants("~search") { shot("~search") }
            field.typeText("Kyoto")
            if wants("~search-results") { shot("~search-results") }
            if wants("~search-empty") {
                field.typeText(" ryokan booking form")
                shot("~search-empty")
            }
        }
        // The calm list, the mock's other to-dos deleted (put back after): one card with no heading, no Filter; then
        // the card's only row, a one-line one, swiped.
        for (frame, keep) in [("~calm", ["t11", "t12", "t14"]), ("~swiped-only", ["t11"])] where wants(frame) {
            let base = try XCTUnwrap(URL(string: hub))
            defer { _ = try? mock(base, "GET", "mock/reset") }
            struct Rows: Decodable { struct Row: Decodable { var id: String }; var todos: [Row] }
            for row in try JSONDecoder().decode(Rows.self, from: mock(base, "GET", "api/v1/todos")).todos
            where !keep.contains(row.id) && !(keep.contains("t12") && row.id.hasPrefix("t12-")) {
                _ = try mock(base, "DELETE", "api/v1/todos/" + row.id)
            }
            fresh([])
            _ = element("todos.row.t11").waitForExistence(timeout: 20)
            if frame == "~swiped-only" { revealSwipe("t11") }
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
            fresh([])
            choose("needs_you")
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
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        _ = element("todos.row.t01").waitForExistence(timeout: 20)
        sleep(1)
        let first = rowsInSight().first?.identifier
        let list = app.collectionViews.firstMatch
        // Twice, which with the compact rows takes the list to its very end: the to-do page has no foot toolbar, so
        // while it is in front the list's end is a toolbar's height shorter, and the list must come back where it was.
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
    /// and of the foot toolbar.
    @MainActor private func openList(_ hub: String, showing id: String) {
        launch(hub, ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"], layout: "separate")
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        _ = element("todos.row.t01").waitForExistence(timeout: 20)
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

    /// Whether `row` is in sight between the navigation bar and the foot toolbar, where a finger reaches it.
    @MainActor private func clear(_ row: XCUIElement) -> Bool {
        let new = element("todos.new")
        let floor = new.exists ? new.frame.minY : app.windows.firstMatch.frame.height
        let top = app.navigationBars.firstMatch.frame.maxY
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
    /// list (docs/apple/shell.md "Said once").
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
    /// saved and one the hub could not be reached for, and the offline line. The mock is
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
        // Each from a fresh mock and launch, the list scrolled to its end as the frames draw it, in the list's own
        // arrangement whatever an earlier run left on the device.
        let fresh = { (more: [String]) in
            _ = try? self.mock(base, "GET", "mock/delay?s=0")
            _ = try? self.mock(base, "GET", "mock/reset")
            self.app?.terminate()
            self.launch(hub, extra + ["-todos.v1.byProject", "NO", "-todos.v1.sort", "suggested"] + more, layout: "separate")
            if !self.element("todos.v1").waitForExistence(timeout: 60) { XCTFail("no V1 list:\n\(self.app.debugDescription)") }
            _ = self.element("todos.row.t01").waitForExistence(timeout: 20)
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
        if wants("~swiped-first") {
            // The first row of the first card, under its heading: the swipe inside the card's top corners.
            fresh([])
            revealSwipe("t01")
            shot("~swiped-first", 1)
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
