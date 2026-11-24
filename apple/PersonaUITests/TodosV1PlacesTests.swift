import XCTest

/// The To-dos places (#10465; docs/design/proposals/todos-redesign.md 3.3; DESIGN_SYSTEM 3.5 "To-dos places") against
/// the mock hub's To-dos view (`MOCK_TODO_VIEW=1`, `PERSONA_TODOS_HUB`), on an iPhone in its own layout: the drawer's
/// To-dos row folds open Today, Upcoming, the projects with their part of N and All Projects…; a place picked there or
/// from the list's title menu is the list; every place but All To-dos says what needs the owner elsewhere in the list's
/// one guard line, with Show All; only All To-dos and a project's place draw the Filter, and Upcoming's ⋯ holds Show as
/// Calendar alone.
/// `-showTodosV1 YES` stands for the release. With `PERSONA_TODOS_FRAMES=1` and `PERSONA_SHOTS=<dir>`, the V1 frames
/// this step draws are saved for review instead (`PERSONA_TODOS_SCHEME`, `PERSONA_TODOS_LANGUAGE`, `PERSONA_TODOS_COLOR`,
/// `PERSONA_TODOS_SIZE`, `PERSONA_TODOS_SUFFIX`, `PERSONA_TODOS_FRAMES_ONLY`, as `TodosV1ListTests`); on an iPad, the
/// Sidebar layout's on its side (`ipad11-landscape-closed~…`).
final class TodosV1PlacesTests: XCTestCase {
    private var app: XCUIApplication!
    private let env = ProcessInfo.processInfo.environment

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    override func tearDown() {
        if UIDevice.current.userInterfaceIdiom == .pad { XCUIDevice.shared.orientation = .portrait }
        super.tearDown()
    }

    /// The app on To-dos: on an iPhone in its own layout; on an iPad only in the Sidebar layout (its frames).
    @MainActor private func launch(_ hub: String, _ extra: [String] = [], layout: String = "separate", v1: Bool = true) throws {
        let pad = UIDevice.current.userInterfaceIdiom == .pad
        guard !pad || layout == "sidebar" else { throw XCTSkip("iPhone only") }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openShellPage", "todos", "-app.navigation", layout]
            + (pad ? ["-app.tab", "assistant", "-openSplitFresh", "YES"] : [])
            + (v1 ? ["-showTodosV1", "YES"] : []) + extra
        app.launchPinned()
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    /// ☰ at To-dos' root, until the drawer shows `row` (the places unfolded, if an earlier run left them folded).
    @MainActor private func drawer(showing row: String) {
        let menu = element("shell-place-sidebar"), target = element(row), fold = element("sidebar-todos-fold")
        XCTAssertTrue(menu.waitForExistence(timeout: 20), "no ☰:\n\(app.debugDescription)")
        for _ in 0..<3 where !target.exists {
            if menu.isHittable { menu.tap() }
            _ = target.waitForExistence(timeout: 5)
            if !target.exists, fold.exists, fold.value as? String != nil, !element("sidebar-todos-place-today").exists { fold.tap() }
        }
        XCTAssertTrue(target.exists, "no \(row) in the drawer:\n\(app.debugDescription)")
    }

    /// The list's title as a menu: the navigation bar's title button (the system's title menu has no identifier of its
    /// own; the bar's other buttons have theirs).
    @MainActor private func titleMenu() -> XCUIElement {
        let bar = app.navigationBars.firstMatch
        let known: Set<String> = ["shell-place-sidebar", "todos.view", "BackButton"]
        return bar.buttons.allElementsBoundByIndex.first { !known.contains($0.identifier) && $0.isHittable } ?? bar.staticTexts.firstMatch
    }

    /// ⋯: the page's own menu in the navigation bar, or on iOS 27 built with its SDK the bar's overflow menu, which
    /// carries no identifier of ours: the bar's trailing button. (Upcoming's calendar has a List | Calendar choice of the
    /// same identifier under the bar.)
    @MainActor private func viewMenu() -> XCUIElement {
        let bar = app.navigationBars.firstMatch
        let ours = bar.buttons.matching(identifier: "todos.view").firstMatch
        if ours.waitForExistence(timeout: 3) { return ours }
        return bar.buttons.allElementsBoundByIndex.last(where: \.isHittable) ?? ours
    }

    /// An open menu's item by its identifier, else by its place: SwiftUI carries no identifier onto a menu's items on
    /// iOS 26 and later, and these menus list their items in a fixed order.
    @MainActor private func menuItem(_ id: String, at index: Int) -> XCUIElement {
        let byID = app.buttons.matching(identifier: id).allElementsBoundByIndex.first(where: \.isHittable)
        if let byID { return byID }
        let menus = app.collectionViews.allElementsBoundByIndex.filter { $0.identifier != "todos.v1" }
        guard let menu = menus.last else { return app.buttons[id] }
        return menu.buttons.element(boundBy: index)
    }

    /// A choice from the Filter's menu, by its identifier (iOS 18 names a choice that is on "Filtered by: ‹choice›").
    @MainActor private func choose(_ choice: String) {
        let named = element("todos.filter.value")
        let filter = !element("todos.filter").exists && named.exists ? named : element("todos.filter")
        XCTAssertTrue(filter.waitForExistence(timeout: 20), "no Filter:\n\(app.debugDescription)")
        filter.tap()
        let order = ["all", "needs_you", "in_progress", "todo", "done"]
        let item = menuItem("todos.filter." + choice, at: order.firstIndex(of: choice) ?? 0)
        XCTAssertTrue(item.waitForExistence(timeout: 5), "no \(choice) in the Filter's menu:\n\(app.debugDescription)")
        item.tap()
    }

    /// N as the Needs you heading says it (its spoken value).
    @MainActor private func needsYou() -> Int? {
        let header = element("todos.section.needs_you")
        return header.exists ? number(header.value as? String ?? "") : nil
    }

    /// The last number in a spoken label ("Kyoto trip, 1"): the count a badge adds to its row.
    private func number(_ label: String) -> Int? {
        label.split(whereSeparator: { !$0.isNumber }).last.flatMap { Int($0) }
    }

    private var hub: String {
        get throws {
            guard let hub = env["PERSONA_TODOS_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_TODO_VIEW=1 (PERSONA_TODOS_HUB)") }
            return hub
        }
    }

    /// The drawer's To-dos row folds open the places, each project with its part of N; Today picked there is the list,
    /// with what needs the owner elsewhere and Show All back to All To-dos.
    @MainActor func testTheDrawerOpensAPlace() throws {
        try launch(try hub)
        XCTAssertTrue(element("todos.section.needs_you").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        let n = try XCTUnwrap(needsYou())
        XCTAssertTrue(element("todos.filter").exists, "All To-dos draws the Filter")
        drawer(showing: "sidebar-todos-place-today")
        for id in ["upcoming", "project:kyoto", "project:weather", "project:home", "more"] {
            XCTAssertTrue(element("sidebar-todos-place-" + id).exists, "no \(id) under To-dos:\n\(app.debugDescription)")
        }
        for project in ["kyoto", "weather", "home"] {
            // Said in words beside its name ("1 needs you"), as the title menu says it.
            let row = element("sidebar-todos-place-project:" + project)
            XCTAssertEqual(number(row.value as? String ?? ""), 1, "\(project)'s part of N: \(row.label), \(String(describing: row.value))")
        }
        XCTAssertNil(number(element("sidebar-todos-place-today").label), "Today draws no count")
        XCTAssertNil(number(element("sidebar-todos-place-today").value as? String ?? ""))
        // Folded by the owner and back: the places go and come, To-dos stays.
        element("sidebar-todos-fold").tap()
        XCTAssertTrue(element("sidebar-todos-place-today").waitForNonExistence(timeout: 5), "the places folded away")
        XCTAssertTrue(element("sidebar-todos").exists)
        element("sidebar-todos-fold").tap()
        XCTAssertTrue(element("sidebar-todos-place-today").waitForExistence(timeout: 5))

        element("sidebar-todos-place-today").tap()
        let line = element("todos.guard")
        XCTAssertTrue(line.waitForExistence(timeout: 20), "Today says what needs the owner elsewhere:\n\(app.debugDescription)")
        XCTAssertTrue(line.label.contains(String(n)), "all \(n) are elsewhere: \(line.label)")
        XCTAssertFalse(element("todos.section.needs_you").exists, "nothing in Today needs the owner")
        XCTAssertFalse(element("todos.filter").exists, "Today draws no Filter")
        line.tap()
        XCTAssertTrue(element("todos.section.needs_you").waitForExistence(timeout: 10), "Show All: All To-dos")
        XCTAssertFalse(element("todos.guard").exists)
    }

    /// The title menu lists the same places; a project picked there is the list in place, its Needs you heading counting
    /// its own rows, with the Filter; under a choice, one guard line counts what the choice hides and what is outside the
    /// project, and its Show All is All To-dos.
    @MainActor func testTheTitleMenuPicksAPlace() throws {
        try launch(try hub)
        XCTAssertTrue(element("todos.section.needs_you").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        let n = try XCTUnwrap(needsYou())
        XCTAssertTrue(element("todos.row.t05").waitForExistence(timeout: 10), "the newsletter is listed in All To-dos")
        let title = titleMenu()
        XCTAssertTrue(title.exists, "no title menu:\n\(app.debugDescription)")
        title.tap()
        // A menu item keeps no identifier: the project by its name, which is the hub's (the knowledge base's title).
        let kyoto = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Kyoto trip'")).firstMatch
        XCTAssertTrue(kyoto.waitForExistence(timeout: 10), "no Kyoto trip in the title menu:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Weather App'")).firstMatch.exists)
        kyoto.tap()
        XCTAssertTrue(element("todos.row.t01").waitForExistence(timeout: 10), "Kyoto trip's to-do")
        XCTAssertTrue(element("todos.row.t05").waitForNonExistence(timeout: 5), "another project's to-do left the list")
        XCTAssertEqual(needsYou(), 1, "the place's part of N")
        XCTAssertTrue(element("todos.guard").label.contains(String(n - 1)), "the others need the owner elsewhere")
        choose("in_progress")
        let lines = app.descendants(matching: .any).matching(identifier: "todos.guard")
        XCTAssertTrue(lines.firstMatch.waitForExistence(timeout: 10), "no guard line:\n\(app.debugDescription)")
        XCTAssertEqual(lines.count, 1, "one line, not a place's and a choice's")
        XCTAssertTrue(lines.firstMatch.label.contains(String(n)), "every one that needs the owner is hidden: \(lines.firstMatch.label)")
        lines.firstMatch.tap()
        XCTAssertTrue(element("todos.row.t05").waitForExistence(timeout: 10), "Show All: All To-dos")
        XCTAssertEqual(needsYou(), n, "on All, with every section")
    }

    /// Upcoming lists the dated to-dos by day, each day with its +; To-dos opened again is on the last place.
    @MainActor func testUpcomingIsByDayAndToDosReopensOnIt() throws {
        try launch(try hub, ["-openTodoPlace", "upcoming", "-todos.v1.upcomingCalendar", "NO"])
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        let days = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.upcoming.add.day:'"))
        XCTAssertTrue(days.firstMatch.waitForExistence(timeout: 20), "no day headings:\n\(app.debugDescription)")
        XCTAssertFalse(element("todos.filter").exists, "Upcoming draws no Filter")
        XCTAssertTrue(element("todos.row.t11").exists, "the passport is dated")
        XCTAssertFalse(element("todos.row.t13").exists, "the car insurance has no date")
        // Its ⋯ holds Show as Calendar alone: the days are its grouping and its order.
        viewMenu().tap()
        XCTAssertTrue(menuItem("todos.places.showCalendar", at: 0).waitForExistence(timeout: 5), "no Show as Calendar:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons.matching(identifier: "todos.view.sort.suggested").firstMatch.exists, "no Sort By on Upcoming")
        app.coordinate(withNormalizedOffset: CGVector(dx: 0.04, dy: 0.6)).tap() // the menu closes
        XCTAssertTrue(days.firstMatch.waitForExistence(timeout: 5))
        // Another page, then To-dos again: still Upcoming.
        drawer(showing: "sidebar-inbox")
        element("sidebar-inbox").tap()
        XCTAssertTrue(element("inbox.more").waitForExistence(timeout: 20), "no Inbox:\n\(app.debugDescription)")
        drawer(showing: "sidebar-todos")
        element("sidebar-todos").tap()
        XCTAssertTrue(days.firstMatch.waitForExistence(timeout: 20), "To-dos reopened on Upcoming:\n\(app.debugDescription)")
        // To-dos in front: its row is All To-dos.
        drawer(showing: "sidebar-todos")
        element("sidebar-todos").tap()
        XCTAssertTrue(element("todos.filter").waitForExistence(timeout: 20), "All To-dos, with its Filter:\n\(app.debugDescription)")
        XCTAssertFalse(days.firstMatch.exists, "no day headings")
    }

    /// New To-Do on Today says the to-do is due today and makes it there: listed in Today at once, and still there once
    /// the hub has it under its own id. The mock is put back after, so the other tests' Today is as before.
    @MainActor func testANewToDoStartsInTheTodayItIsMadeIn() throws {
        let hub = try hub
        let reset = try XCTUnwrap(URL(string: hub + "/mock/reset"))
        addTeardownBlock {
            var request = URLRequest(url: reset)
            request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
            _ = try? await URLSession.shared.data(for: request)
        }
        try launch(hub, ["-openTodoPlace", "today", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"])
        let new = element("todos.new")
        XCTAssertTrue(new.waitForExistence(timeout: 60), "no New To-Do:\n\(app.debugDescription)")
        new.tap()
        let title = element("todos.new.title")
        XCTAssertTrue(title.waitForExistence(timeout: 10), "no New To-Do sheet:\n\(app.debugDescription)")
        XCTAssertTrue(element("todos.new.due").exists, "Today's sheet says when it is due:\n\(app.debugDescription)")
        title.typeText("Water the ferns")
        // A fresh simulator's keyboard first shows its own tip, a system button with no identifier: its English word.
        let tip = app.buttons["Continue"]
        if tip.waitForExistence(timeout: 2) { tip.tap() }
        element("todos.new.add").tap()
        let rows = { (prefix: String) in
            self.app.descendants(matching: .any)
                .matching(NSPredicate(format: "identifier BEGINSWITH %@ AND label CONTAINS 'Water the ferns'", "todos.row." + prefix))
                .firstMatch
        }
        XCTAssertTrue(rows("").waitForExistence(timeout: 10), "listed in Today at once:\n\(app.debugDescription)")
        XCTAssertTrue(rows("todo_").waitForExistence(timeout: 20), "still in Today once the hub has it:\n\(app.debugDescription)")
        XCTAssertTrue(element("todos.guard").exists, "still Today, with what needs the owner elsewhere")
    }

    /// An hour tapped on Upcoming's calendar opens the list's New To-Do (saved on the device first, as the foot's +),
    /// due that day at that time, All Day off; never the calendar's own sheet, which waits for the hub.
    @MainActor func testAnHourOnUpcomingsCalendarStartsANewToDoThen() throws {
        try launch(try hub, ["-openTodoPlace", "upcoming", "-todos.v1.upcomingCalendar", "YES", "-todos.calendar.mode", "day"])
        XCTAssertTrue(element("todo.calendar.strip").waitForExistence(timeout: 60), "no calendar day:\n\(app.debugDescription)")
        hourOnTheCalendar().tap() // the mock's dated to-dos are all day: every hour is empty
        XCTAssertTrue(element("todos.new.title").waitForExistence(timeout: 10), "no New To-Do:\n\(app.debugDescription)")
        XCTAssertTrue(element("todos.new.due").exists, "due on the day tapped")
        XCTAssertEqual(element("todos.new.allDay").value as? String, "0", "at the hour tapped, not all day")
        XCTAssertFalse(element("todo.calendar.newTitle").exists, "not the calendar's own sheet")
    }

    /// An hour on the calendar's day, below its strip and its all-day row: by its column when the column is an element.
    @MainActor private func hourOnTheCalendar() -> XCUICoordinate {
        let hours = element("todo.calendar.axis")
        return hours.exists ? hours.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
            : app.coordinate(withNormalizedOffset: CGVector(dx: 0.6, dy: 0.7))
    }

    /// A project picked in All Projects… that the title menu does not list (the mock's Garden, nothing open in it) is
    /// listed there all the same, so the menu ticks the place shown.
    @MainActor func testTheTitleMenuListsAProjectPickedInAllProjects() throws {
        try launch(try hub, ["-openTodoPlace", "project:garden"])
        XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 60), "no V1 list:\n\(app.debugDescription)")
        let title = titleMenu()
        XCTAssertTrue(title.waitForExistence(timeout: 20), "no title menu:\n\(app.debugDescription)")
        _ = app.navigationBars.staticTexts["Garden"].waitForExistence(timeout: 20) // the knowledge base's projects read
        title.tap()
        let garden = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Garden'")).firstMatch
        XCTAssertTrue(garden.waitForExistence(timeout: 10), "Garden is not in the title menu:\n\(app.debugDescription)")
    }

    // MARK: Frames

    /// The app again with `args`, on its list or Upcoming's calendar.
    @MainActor private func relaunch(_ hub: String, _ args: [String], layout: String = "separate") throws {
        app?.terminate()
        try launch(hub, args, layout: layout)
        let up = element("todos.v1").waitForExistence(timeout: 60) || element("todo.calendar.title").exists
        XCTAssertTrue(up, "no V1 list")
    }

    /// The Sidebar layout's frames on an iPad on its side (`ipad11-landscape/closed`): the places in the sidebar with the
    /// one shown shaded, on Today, on a project and on All To-dos, and a place picked while a to-do is pushed: its root,
    /// swapped in without a pop.
    @MainActor private func padFrames(_ hub: String, _ extra: [String], wants: (String) -> Bool, shot: (String) -> Void) throws {
        XCUIDevice.shared.orientation = .landscapeLeft
        let relaunch = { (args: [String]) in try self.relaunch(hub, extra + args, layout: "sidebar") }
        for (frame, place) in [("~today", "today"), ("~kyoto-place", "project:kyoto"), ("~all", "all")] where wants(frame) {
            try relaunch(["-openTodoPlace", place, "-todos.v1.upcomingCalendar", "NO"])
            _ = element("sidebar-todos-place-today").waitForExistence(timeout: 20)
            shot(frame)
        }
        if wants("~swap") {
            try relaunch(["-openTodoPlace", "project:kyoto"])
            let row = element("todos.row.t01")
            XCTAssertTrue(row.waitForExistence(timeout: 20), "no Kyoto trip's to-do:\n\(app.debugDescription)")
            row.tap()
            XCTAssertTrue(row.waitForNonExistence(timeout: 10), "the to-do did not open")
            shot("~swap-pushed")
            element("sidebar-todos-place-today").tap()
            XCTAssertTrue(element("todos.v1").waitForExistence(timeout: 10), "Today not at its root:\n\(app.debugDescription)")
            shot("~swap-today")
        }
    }

    /// The frames this step draws, for the review (no checks beyond reaching them): `~drawer`, `~menu`, `~today` (the
    /// mock has nothing due today: its empty state), `~kyoto`, a project with nothing open (`~kyoto-empty`, the mock's
    /// Garden) and its title menu (`~menu-garden`), a project under a choice with its one guard line (`~kyoto-filter`),
    /// ⋯ in a project and on Upcoming, Upcoming as a list, scrolled under the bar (where it is longer than the screen, as
    /// at the largest text size: its headings scroll away with their cards) and as a calendar, New To-Do's sheet on
    /// Today, in a project, on Upcoming, from a day heading's + and from an hour on Upcoming's calendar, and All
    /// Projects… with each project's counts.
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
        let wants = { (frame: String) in frames.isEmpty || frames.contains(frame) }
        let suffix = env["PERSONA_TODOS_SUFFIX"].map { "-" + $0 } ?? ""
        let pad = UIDevice.current.userInterfaceIdiom == .pad
        let device = pad ? "ipad11-landscape-closed" : "iphone-list"
        let shot = { (frame: String) in
            sleep(2) // the bars' reveal and the rows' marks settle
            let screen = XCUIScreen.main.screenshot()
            // An iPad on its side gives the portrait buffer with an orientation tag: drawn upright here, untagged, so
            // every viewer shows it as the screen was.
            let png = pad ? Self.upright(screen.image) : screen.pngRepresentation
            try? png?.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(device)\(frame)\(suffix)-\(scheme).png"))
        }
        if UIDevice.current.userInterfaceIdiom == .pad { return try padFrames(hub, extra, wants: wants, shot: shot) }
        let relaunch = { (args: [String]) in try self.relaunch(hub, extra + args) }
        try relaunch(["-openTodoPlace", "all"])
        _ = element("todos.section.needs_you").waitForExistence(timeout: 10)
        if wants("~drawer") {
            drawer(showing: "sidebar-todos-place-today")
            shot("~drawer")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)).tap() // the page beside the drawer closes it
            _ = element("todos.section.needs_you").waitForExistence(timeout: 5)
        }
        if wants("~menu") {
            titleMenu().tap()
            _ = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Kyoto trip'")).firstMatch.waitForExistence(timeout: 5)
            shot("~menu")
            app.coordinate(withNormalizedOffset: CGVector(dx: 0.04, dy: 0.6)).tap()
        }
        for (frame, place) in [("~today", "today"), ("~kyoto", "project:kyoto"), ("~kyoto-empty", "project:garden"),
                               ("~upcoming", "upcoming")] where wants(frame) {
            try relaunch(["-openTodoPlace", place, "-todos.v1.upcomingCalendar", "NO"])
            shot(frame)
        }
        if wants("~menu-garden") {
            try relaunch(["-openTodoPlace", "project:garden"])
            _ = app.navigationBars.staticTexts["Garden"].waitForExistence(timeout: 10)
            titleMenu().tap()
            _ = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'Garden'")).firstMatch.waitForExistence(timeout: 5)
            shot("~menu-garden")
        }
        if wants("~kyoto-filter") {
            try relaunch(["-openTodoPlace", "project:kyoto"])
            choose("in_progress")
            _ = element("todos.guard").waitForExistence(timeout: 10)
            shot("~kyoto-filter")
        }
        for (frame, place) in [("~more-kyoto", "project:kyoto"), ("~more-upcoming", "upcoming")] where wants(frame) {
            try relaunch(["-openTodoPlace", place, "-todos.v1.upcomingCalendar", "NO"])
            viewMenu().tap()
            _ = menuItem(place == "upcoming" ? "todos.places.showCalendar" : "todos.view.sort.suggested", at: 0).waitForExistence(timeout: 5)
            shot(frame)
        }
        if wants("~upcoming-scrolled") {
            // A day's heading under the bar: headings scroll away with their cards, so no row runs into its words.
            try relaunch(["-openTodoPlace", "upcoming", "-todos.v1.upcomingCalendar", "NO"])
            let list = element("todos.v1")
            list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.7))
                .press(forDuration: 0.05, thenDragTo: list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45)))
            shot("~upcoming-scrolled")
        }
        if wants("~upcoming-calendar") {
            try relaunch(["-openTodoPlace", "upcoming", "-todos.v1.upcomingCalendar", "YES", "-todos.calendar.mode", "week"])
            _ = element("todo.calendar.title").waitForExistence(timeout: 10)
            shot("~upcoming-calendar")
        }
        // New To-Do in a place: the sheet says where the to-do starts (Today's Due row, the project under the title,
        // Upcoming's tomorrow, a day heading's day, the calendar's hour).
        let newSheet = { (frame: String) in
            _ = self.element("todos.new.title").waitForExistence(timeout: 10)
            let tip = self.app.buttons["Continue"]
            if tip.waitForExistence(timeout: 2) { tip.tap() }
            shot(frame)
        }
        for (frame, place) in [("~new-today", "today"), ("~new-kyoto", "project:kyoto"), ("~new-upcoming", "upcoming")]
            where wants(frame) {
            try relaunch(["-openTodoPlace", place, "-todos.v1.upcomingCalendar", "NO"])
            element("todos.new").tap()
            newSheet(frame)
        }
        if wants("~new-day") {
            try relaunch(["-openTodoPlace", "upcoming", "-todos.v1.upcomingCalendar", "NO"])
            let days = app.descendants(matching: .any).matching(NSPredicate(format: "identifier BEGINSWITH 'todos.upcoming.add.day:'"))
            if days.element(boundBy: 1).waitForExistence(timeout: 10) { days.element(boundBy: 1).tap() }
            newSheet("~new-day")
        }
        if wants("~new-slot") {
            try relaunch(["-openTodoPlace", "upcoming", "-todos.v1.upcomingCalendar", "YES", "-todos.calendar.mode", "day"])
            if element("todo.calendar.strip").waitForExistence(timeout: 10) { hourOnTheCalendar().tap() }
            newSheet("~new-slot")
        }
        if wants("~all-projects") {
            try relaunch(["-openTodoPlace", "all"])
            drawer(showing: "sidebar-todos-place-more")
            element("sidebar-todos-place-more").tap()
            _ = element("todos.places.project.project:kyoto").waitForExistence(timeout: 10)
            shot("~all-projects")
        }
    }

    /// `image` drawn with its orientation applied, at its scale, as a PNG with no orientation tag.
    @MainActor private static func upright(_ image: UIImage) -> Data? {
        let format = UIGraphicsImageRendererFormat()
        format.scale = image.scale
        return UIGraphicsImageRenderer(size: image.size, format: format).image { _ in image.draw(at: .zero) }.pngData()
    }

    /// Today's To-dos with the gate off, as `main` draws them (plan 2.6.1: before = after): its calendar, which takes
    /// its colours from a parameter now (`TodoCalendarView.state`, nil as before), and the drawer's To-dos row. Run with
    /// the same test bundle against a build of `main` and of this branch (`PERSONA_TODOS_TODAY=1`).
    @MainActor func testTodayFrames() throws {
        guard env["PERSONA_TODOS_TODAY"] == "1", let hub = env["PERSONA_TODOS_HUB"], let dir = env["PERSONA_SHOTS"] else {
            throw XCTSkip("PERSONA_TODOS_TODAY=1 with PERSONA_TODOS_HUB and PERSONA_SHOTS")
        }
        let scheme = env["PERSONA_TODOS_SCHEME"] ?? "light"
        let suffix = env["PERSONA_TODOS_SUFFIX"].map { "-" + $0 } ?? ""
        let shot = { (frame: String) in
            sleep(2)
            let png = XCUIScreen.main.screenshot().pngRepresentation
            try? png.write(to: URL(fileURLWithPath: dir).appendingPathComponent("iphone-today\(frame)\(suffix)-\(scheme).png"))
        }
        try launch(hub, ["-ui.scheme", scheme, "-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-todos.view", "calendar",
                         "-todos.calendar.mode", "week"], v1: false)
        XCTAssertTrue(element("todo.calendar.title").waitForExistence(timeout: 60), "no calendar:\n\(app.debugDescription)")
        shot("~calendar")
        drawer(showing: "sidebar-todos")
        shot("~drawer")
    }
}
