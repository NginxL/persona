import XCTest

/// The Agents list with a lot of agents (scripts/mock/mock_hub.py with MOCK_AGENTS=1: ~120 agents, a space with 3 needing
/// you, 35 finished-unread and 12 working): the quick filters, the compact rows, folding spaces, marking read.
///   TEST_RUNNER_PERSONA_AGENTS_HUB=http://127.0.0.1:<port>
/// TEST_RUNNER_PERSONA_SHOTS=<dir> also saves a screenshot of each step there (docs/operations/testing.md「Agents list」).
final class AgentsListTests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_AGENTS_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 (PERSONA_AGENTS_HUB)")
        }
        hub = h
        _ = mock("reset") // every agent back to its state, nothing marked seen
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            add(attachment(XCTAttachment(screenshot: app.screenshot()), "failure"))
            add(attachment(XCTAttachment(string: app.debugDescription), "view tree"))
        }
        super.tearDown()
    }

    /// `fresh`: forget the list's saved look (cards, nothing folded, all machines) first. By space: these are about
    /// spaces, and the default grouping is By Repository (#2108, `AgentsByMachineTests`).
    /// `chat`: the chat left open last time ("<machine>/<session>"), which the app opens again over the list.
    /// It is restored only when the tab saved with it is Agents (`app.tab`): given too, or the tab the last run ended on
    /// (another test's) decides whether the chat opens.
    private func launch(fresh: Bool = true, chat: String = "") {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", chat, "-tab", "dev", "-dev.grouping", "space", "-probes", "YES"]
            + (fresh ? ["-openAgentsFresh", "YES"] : [])
        app.launchPinned()
        if chat.isEmpty { see(filter("all"), "the quick filters", timeout: 15) }
    }

    private func attachment(_ a: XCTAttachment, _ name: String) -> XCTAttachment {
        a.name = name
        a.lifetime = .keepAlways
        return a
    }

    private func shot(_ name: String) {
        let s = app.screenshot()
        add(attachment(XCTAttachment(screenshot: s), name))
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let style = ProcessInfo.processInfo.environment["PERSONA_STYLE"].map { "-" + $0 } ?? ""
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + style + ".png"))
        }
    }

    @discardableResult
    private func see(_ e: XCUIElement, _ what: String, timeout: TimeInterval = 10) -> XCUIElement {
        if !e.waitForExistence(timeout: timeout) { XCTFail("\(what) not found") }
        return e
    }

    private func gone(_ e: XCUIElement, _ what: String) {
        let deadline = Date.now.addingTimeInterval(10)
        while e.exists, Date.now < deadline { usleep(200_000) }
        XCTAssertFalse(e.exists, what)
    }

    /// A row or header that can be below the fold (under the Needs You cards and weather on a 6.3" phone; the list
    /// builds rows lazily, so it is not there to find until scrolled to). Split by machine (#2108), platform is on Mac
    /// Studio, the hub's last machine: under 「服务器」's and MacBook's spaces, several screens down.
    @discardableResult private func reach(_ e: XCUIElement, _ what: String) -> XCUIElement {
        for _ in 0..<10 where !e.waitForExistence(timeout: 2) { app.swipeUp(velocity: .slow) }
        return see(e, what)
    }

    private func text(_ s: String) -> XCUIElement {
        app.staticTexts.matching(NSPredicate(format: "label == %@", s)).firstMatch
    }

    private func button(_ s: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label CONTAINS %@", s)).firstMatch
    }

    private func filter(_ id: String) -> XCUIElement { app.buttons["agents.filter." + id] }

    /// The cards that need you. Not by their section's title: 「需要你」 is the 「需要你」 filter chip's word too.
    private func needsYouCards() -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: "agents.interaction").firstMatch
    }

    /// A space's header (the button that folds it): 「platform，已展开，…」.
    private func header(_ space: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", space + "，")).firstMatch
    }

    private func mock(_ what: String) -> [String: Any] {
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/" + what)!),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return [:] }
        return obj
    }

    private func seen() -> [String] { mock("seen")["seen"] as? [String] ?? [] }

    private func wait(_ what: String, _ test: () -> Bool) {
        let deadline = Date.now.addingTimeInterval(10)
        while Date.now < deadline, !test() { usleep(300_000) }
        XCTAssertTrue(test(), what)
    }

    private func listMenu() -> XCUIElement {
        see(app.buttons["shell-agents"].firstMatch, "the list menu")
    }

    /// Back to the top of the list, where the quick filters are (a list scrolled to a row below the cards).
    private func toTop() {
        for _ in 0..<6 where !(filter("all").exists && filter("all").isHittable) {
            app.collectionViews.firstMatch.swipeDown(velocity: .fast)
        }
    }

    /// The n-th agent row (card or compact).
    private func row(_ n: Int, compact: Bool = false) -> XCUIElement {
        app.buttons.matching(identifier: compact ? "agents.compactRow" : "agents.row").element(boundBy: n)
    }

    /// Agent rows on screen now.
    private func visibleRows() -> Int {
        let screen = app.windows.firstMatch.frame
        return app.buttons.matching(NSPredicate(format: "identifier IN %@", ["agents.row", "agents.compactRow"]))
            .allElementsBoundByIndex.filter { c in
            c.exists && screen.intersects(c.frame)
                && c.frame.minY > 100
        }.count
    }

    /// MOCK_WORK=1 supplies the triage's finished login fix in the app repository. Both views show its conclusion
    /// without expanding Seen or an idle repository.
    @MainActor func testJustFinishedInStatusAndRepository() throws {
        let url = try XCTUnwrap(URL(string: hub + "/api/v1/sessions"))
        let fixture = try String(contentsOf: url, encoding: .utf8)
        try XCTSkipUnless(fixture.contains("a9000000-0000-4000-8000-000000000001"), "needs MOCK_WORK=1")
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair]
        app.launchPinned()
        for grouped in ["state", "repo"] {
            app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-tab", "dev", "-dev.openChat", "",
                                   "-openAgentsFresh", "YES", "-dev.grouping", grouped, "-dev.machineFilter", "server"]
            app.launchPinned()
            let match = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "agents.row", "登录页崩溃")).firstMatch
            let row = reach(match, "the Just finished agent in " + grouped)
            XCTAssertTrue(row.label.contains("已合并 PR #12"), row.label)
            if grouped == "state" {
                XCTAssertTrue(header("刚做完待看").exists)
            } else {
                XCTAssertTrue(row.label.contains("刚做完待看"), row.label)
            }
        }
    }

    // MARK: Quick filters

    /// All / Needs You / Unread / Working with their counts, one tap each; they add up with the machine menu.
    @MainActor func testQuickFilters() throws {
        launch()
        XCTAssertEqual(filter("needsYou").label, "需要你 6")
        XCTAssertEqual(filter("unread").label, "完成未看 48")
        XCTAssertEqual(filter("working").label, "工作中 19")
        see(needsYouCards(), "the cards that need you, under 全部")
        // The cards that need you take most of the screen: the rows are below them.
        reach(text("给天气 App 加主屏幕小组件"), "a working agent under 全部")
        shot("agents-all")
        toTop()

        filter("unread").tap()
        XCTAssertTrue(filter("unread").isSelected)
        gone(text("给天气 App 加主屏幕小组件"), "working agents leave under 完成未看")
        XCTAssertFalse(needsYouCards().exists, "no cards that need you under 完成未看")
        see(button("全部标为已看"), "mark every one read, under 完成未看")
        shot("agents-unread")
        // platform is on Mac Studio, the hub's last machine: under 「服务器」's and MacBook's unread (#2108).
        reach(text("platform 任务 4"), "a finished-unread agent")
        toTop()

        filter("working").tap()
        reach(text("给天气 App 加主屏幕小组件"), "a working agent under 工作中")
        gone(text("platform 任务 4"), "finished ones leave under 工作中")
        toTop()

        filter("needsYou").tap()
        see(needsYouCards(), "the cards that need you, under 需要你")
        reach(text("补齐网络层的单元测试"), "an agent that needs you")
        gone(text("给天气 App 加主屏幕小组件"), "working agents leave under 需要你")
        toTop()

        // One machine: the counts are that machine's.
        listMenu().tap()
        see(app.buttons["MacBook"], "MacBook in the menu").tap()
        let unread = filter("unread")
        wait("完成未看 counts MacBook's only (\(unread.label))") { unread.label == "完成未看 6" }
        XCTAssertEqual(filter("needsYou").label, "需要你 1")
        filter("unread").tap()
        see(text("mobile 任务 1"), "MacBook's unread agent")
        gone(text("platform 任务 4"), "Mac Studio's agents leave")
        shot("example-machine")
        listMenu().tap()
        see(app.buttons["全部机器"], "全部机器").tap()
        wait("完成未看 counts every machine again") { filter("unread").label == "完成未看 48" }
        filter("all").tap()
        reach(text("给天气 App 加主屏幕小组件"), "everything again under 全部")
    }

    /// In the shell the list's menu hides behind the selected Agents switch: a machine chosen there says so on the page,
    /// and one tap shows every machine again (#1659: one machine chosen once hid the other computers' agents).
    @MainActor func testMachineFilterShowsInShell() throws {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", "", "-tab", "dev", "-openAgentsFresh", "YES"]
        app.launchPinned()
        see(filter("all"), "the quick filters", timeout: 15)
        let chip = app.buttons["agents.machineFilter"]
        XCTAssertFalse(chip.exists, "no machine chosen: nothing more on the page")
        let all = filter("unread").label

        see(app.buttons["shell-agents"], "the selected Agents switch").tap()
        see(app.buttons["MacBook"], "MacBook in the menu").tap()
        see(chip, "the chosen machine on the page")
        XCTAssertEqual(chip.label, "只看 MacBook")
        wait("完成未看 counts MacBook's only (\(filter("unread").label))") { filter("unread").label == "完成未看 6" }
        shot("agents-shell-machine-filter")

        chip.tap()
        gone(chip, "the chip goes with the filter")
        wait("every machine again (\(filter("unread").label))") { filter("unread").label == all }

        app.terminate()
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", "", "-tab", "dev"]
        app.launchPinned()
        see(filter("all"), "the list after a relaunch", timeout: 15)
        XCTAssertFalse(app.buttons["agents.machineFilter"].exists, "cleared for good, not only for this run")
    }

    // MARK: Compact rows

    /// Compact List in the menu: one line per agent, several times as many on screen; remembered across launches.
    @MainActor func testCompactRows() throws {
        launch()
        filter("unread").tap()
        // platform is on Mac Studio, the hub's last machine: under MacBook's unread first (#2108).
        reach(text("platform 任务 4"), "platform's rows")
        let cards = visibleRows()
        XCTAssertGreaterThan(cards, 0)

        listMenu().tap()
        see(app.buttons["细列表"], "细列表 in the menu").tap()
        see(row(0, compact: true), "compact rows")
        let compact = visibleRows()
        XCTAssertGreaterThanOrEqual(Double(compact), Double(cards) * 2.5, "compact \(compact) vs cards \(cards)")
        shot("agents-compact-unread")
        toTop()
        filter("all").tap()
        reach(text("给天气 App 加主屏幕小组件"), "a working agent")
        shot("agents-compact-all")
        for _ in 0..<4 { app.swipeUp(velocity: .fast) }
        shot("agents-compact-scrolled")

        app.terminate()
        launch(fresh: false)
        reach(row(0, compact: true), "still compact after a relaunch")
        listMenu().tap()
        see(app.buttons["头像"], "头像 in the menu").tap() // the default style (#2530)
        reach(row(0), "card rows again")
        XCTAssertFalse(row(0, compact: true).exists)
    }

    // MARK: Folding spaces

    /// Tapping a space's header folds it (its counts stay on the header) and back; remembered; Collapse All / Expand All.
    @MainActor func testFoldingSpaces() throws {
        launch()
        let platform = reach(header("platform"), "platform's header")
        XCTAssertTrue(platform.label.contains("已展开"), platform.label)
        reach(text("platform 任务 1"), "platform's first row")
        platform.tap()
        gone(text("platform 任务 1"), "platform's rows fold away")
        let folded = header("platform")
        XCTAssertTrue(folded.label.contains("已折叠"), folded.label)
        for n in ["3 个需要你", "12 个工作中", "35 个完成未看"] { XCTAssertTrue(folded.label.contains(n), "\(n) in \(folded.label)") }
        shot("agents-folded-one")

        app.terminate()
        launch(fresh: false)
        XCTAssertTrue(reach(header("platform"), "platform's header").label.contains("已折叠"), "still folded after a relaunch")
        XCTAssertFalse(text("platform 任务 1").exists)

        listMenu().tap()
        see(app.buttons["全部折叠"], "全部折叠").tap()
        for space in ["weather", "platform", "infra"] {
            XCTAssertTrue(see(header(space), "\(space)'s header").label.contains("已折叠"), "\(space) folded")
        }
        gone(text("给天气 App 加主屏幕小组件"), "the pinned space folds too")
        shot("agents-folded-all")
        listMenu().tap()
        see(app.buttons["全部展开"], "全部展开").tap()
        reach(text("platform 任务 1"), "platform open again")
        XCTAssertTrue(header("platform").label.contains("已展开"))

        // A folded space still opens from its menu.
        for _ in 0..<4 { app.swipeDown(velocity: .fast) }
        reach(header("weather"), "weather's header").tap()
        button("weather 的更多操作").tap()
        see(app.buttons["打开空间"], "打开空间 in the space's menu").tap()
        see(app.navigationBars["weather"], "the space page")
    }

    // MARK: Many unread

    /// A space's ⋯ › Mark All as Read marks its finished-unread agents seen in one go.
    @MainActor func testMarkSpaceRead() throws {
        launch()
        reach(header("platform"), "platform's header")
        button("platform 的更多操作").tap()
        see(app.buttons["全部标为已看（35）"], "全部标为已看 with the count").tap()
        wait("the hub hears of 35 seen (\(seen().count))") { seen().count == 35 }
        let h = header("platform")
        wait("no finished-unread left on the header (\(h.label))") { !h.label.contains("完成未看") }
        // The filters are the first list row now; return to them after inspecting a lower space.
        for _ in 0..<4 where !filter("unread").exists { app.collectionViews.firstMatch.swipeDown(velocity: .fast) }
        XCTAssertEqual(filter("unread").label, "完成未看 13")
        shot("agents-space-read")
    }

    /// Under Unread an opened agent stays where it was (seen now), so going down the list one by one works; switching
    /// filters drops it. Mark All as Read empties the filter at once (no confirmation).
    @MainActor func testWorkingThroughUnread() throws {
        launch()
        filter("unread").tap()
        let first = see(row(0), "the first unread row")
        let title = first.staticTexts.firstMatch.label
        first.tap()
        let back = see(app.buttons["返回"].firstMatch, "the chat")
        sleep(1) // the push has to finish first
        back.tap()
        see(text(title), "the opened agent stays in place under 完成未看")
        wait("the hub hears it was seen") { seen().count == 1 }
        XCTAssertEqual(filter("unread").label, "完成未看 47")
        let next = row(1)
        XCTAssertNotEqual(next.staticTexts.firstMatch.label, title, "the next one is below it")
        shot("agents-unread-walk")

        filter("all").tap()
        filter("unread").tap()
        gone(text(title), "a seen agent leaves once the filter is chosen again")

        button("全部标为已看").tap()
        wait("the hub hears of every one (\(seen().count))") { seen().count == 48 }
        see(text("没有完成未看的会话"), "the empty filter")
        XCTAssertEqual(filter("unread").label, "完成未看 0")
        shot("agents-unread-empty")
    }

    // MARK: The first row and the search drawer

    /// At rest the first row hugs the navigation bar (or its revealed search field). Check both layout and pixels:
    /// UIKit once moved the drawn chips down by the whole top inset while accessibility still reported their old frame.
    /// The page's top on a phone is its own (#2478, `AgentsHeader`): the field is the list's first row with the filters,
    /// under the navigation bar and, in the shell, under the room kept for its taller top bar.
    private func gapAboveFilters(_ state: String, searchShown: Bool = false) -> CGFloat {
        let bar = see(filter("all"), "the first-row quick filters").frame
        let nav = app.navigationBars.firstMatch.frame
        let shellRoom: CGFloat = app.buttons["agents-new"].firstMatch.exists ? 34 : 0 // AgentsHeader.room
        let top = nav.maxY + shellRoom
        let search = app.searchFields.firstMatch
        let visible = search.exists && search.isHittable && search.frame.height > 30
        XCTAssertEqual(visible, searchShown, "\(state): unexpected search field visibility")
        if searchShown && search.exists {
            // Searching, it is up in the navigation bar's row; otherwise under the bars.
            let floor = search.frame.minY < nav.maxY ? nav.minY : top
            XCTAssertGreaterThanOrEqual(search.frame.minY, floor - 1, "\(state): the search field is under the bars")
        }
        let edge = searchShown && search.exists ? search.frame.maxY : top
        print("filters \(state): bar \(bar) nav \(nav) search \(search.exists ? search.frame : .zero)")
        XCTAssertGreaterThanOrEqual(bar.minY, edge - 1, "\(state): the first row is under the bar")
        XCTAssertTrue(drawn(bar), "\(state): the bar is not drawn where it is laid out")
        return bar.minY - edge
    }

    /// The filters are a list row, so a scroll takes them out of view, with no pinned copy left over the list.
    private func filtersScrolledAway(_ state: String) {
        let chip = filter("all")
        XCTAssertTrue(!chip.exists || chip.frame.maxY <= app.navigationBars.firstMatch.frame.maxY,
                      "\(state): the filters stayed pinned instead of scrolling with the list")
    }

    @MainActor private func returnToTop() {
        for _ in 0..<14 {
            if filter("all").exists && filter("all").isHittable { break }
            app.collectionViews.firstMatch.swipeDown(velocity: .fast)
        }
        app.revealAgentsSearch()
    }

    /// Whether the selected chip (All: an accent-tinted capsule; in the BeautifulUI style its raised capsule, the surface
    /// with its word in ink) is on screen at `frame`. Element frames come from layout: behind a chat restored at launch
    /// the bar was laid out under the search field but drawn ~170 pt lower (the whole top inset again), the TestFlight
    /// 35 / 38 screenshots; only the pixels show it.
    private func drawn(_ frame: CGRect) -> Bool {
        let image = app.screenshot().image
        guard let cg = image.cgImage else { return false }
        let scale = CGFloat(cg.width) / app.windows.firstMatch.frame.width
        let r = frame.insetBy(dx: 4, dy: 4)
        guard r.width > 0, r.height > 0, app.windows.firstMatch.frame.contains(frame) else { return false }
        let w = Int(r.width * scale), h = Int(r.height * scale)
        var px = [UInt8](repeating: 0, count: w * h * 4)
        guard let ctx = CGContext(data: &px, width: w, height: h, bitsPerComponent: 8, bytesPerRow: w * 4,
                                  space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue)
        else { return false }
        ctx.draw(cg, in: CGRect(x: -r.minX * scale, y: -(CGFloat(cg.height) - r.maxY * scale), width: CGFloat(cg.width), height: CGFloat(cg.height)))
        if UITestStyle.isBeautiful {
            // Drawn there, the frame is mostly the chip's surface with its word in it at the other end of light and
            // dark; the fogged list that showed through in those screenshots has no crisp text at that spot.
            var light = 0, dark = 0
            for i in stride(from: 0, to: px.count, by: 4) {
                let m = max(px[i], px[i + 1], px[i + 2])
                if m > 225 { light += 1 } else if m < 110 { dark += 1 }
            }
            print("filters chip light \(light) dark \(dark) of \(w * h)")
            return (light * 2 > w * h && dark * 40 > w * h) || (dark * 2 > w * h && light * 40 > w * h)
        }
        var tinted = 0
        for i in stride(from: 0, to: px.count, by: 4) where Int(px[i + 2]) - Int(px[i]) > 20 { tinted += 1 }
        print("filters chip tinted \(tinted) of \(w * h)")
        return tinted * 2 > w * h
    }

    /// A vertical drag over the rows that are on screen, while the search keyboard is up. Then
    /// `collectionViews.firstMatch` is not the list: it is the keyboard's suggestion strip right above the keys (a 44 pt
    /// collection view of its own, outside `keyboards`), so an element swipe there never moved the list. An element
    /// swipe on the list itself starts from the middle of its whole frame, which on a small phone is about the
    /// keyboard's edge (it did not scroll on an iPhone SE). So drag inside the part of the list that takes touches:
    /// below the bars and the search field, above the suggestion strip and the keys.
    private func dragListUp() {
        let list = app.collectionViews.containing(.button, identifier: "agents.filter.all").firstMatch.frame
        var top = max(list.minY, app.navigationBars.firstMatch.frame.maxY)
        let search = app.searchFields.firstMatch
        if search.exists && search.isHittable { top = max(top, search.frame.maxY) }
        var bottom = list.maxY
        let keyboard = app.keyboards.firstMatch
        if keyboard.exists { bottom = min(bottom, keyboard.frame.minY) }
        // Strips sitting on that edge (the suggestion bar on the keys) cover the rows too.
        for other in app.collectionViews.allElementsBoundByIndex where other.frame != list {
            if other.frame.maxY >= bottom - 1, other.frame.minY > top { bottom = min(bottom, other.frame.minY) }
        }
        print("drag rows: list \(list) keyboard \(keyboard.exists ? keyboard.frame : .zero) area \(top)...\(bottom)")
        guard list.height > 0, bottom - top >= 60 else {
            XCTFail("no room to drag the rows: list \(list), keyboard \(keyboard.exists ? keyboard.frame : .zero), "
                    + "search \(search.exists ? search.frame : .zero), area \(top)...\(bottom)")
            return
        }
        let origin = app.coordinate(withNormalizedOffset: .zero)
        let from = origin.withOffset(CGVector(dx: list.midX, dy: top + (bottom - top) * 0.8))
        let to = origin.withOffset(CGVector(dx: list.midX, dy: top + (bottom - top) * 0.2))
        from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .default, thenHoldForDuration: 0.1)
    }

    /// Back from a chat to the list; `atTop`: the list was at its top, so its filters are on screen again.
    private func backToList(atTop: Bool = true) {
        _ = app.buttons["返回"].firstMatch.waitForExistence(timeout: 3)
        // The chat's own header (its back button is not in a navigation bar), full or folded.
        if let back = app.buttons.matching(NSPredicate(format: "label IN %@", ["返回", "Back"])).allElementsBoundByIndex
            .first(where: { $0.isHittable }) { back.tap() } else {
            let w = app.windows.firstMatch
            w.coordinate(withNormalizedOffset: CGVector(dx: 0.01, dy: 0.5))
                .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.5)))
        }
        see(atTop ? filter("all") : app.collectionViews.firstMatch, "the list again")
        sleep(1)
    }

    /// The first row scrolls with the list. At every rest position at the top there is no blurred gap; a pull
    /// reveals search, which starts folded. Keep the chat, refresh, foreground, tab and search transitions covered.
    @MainActor func testFilterBarHugsTheTop() throws {
        launch()
        sleep(1)
        let limit: CGFloat = 24
        shot("filters-top")
        XCTAssertLessThan(gapAboveFilters("open"), limit, "before scrolling, with search folded")
        app.revealAgentsSearch()
        XCTAssertLessThan(gapAboveFilters("pulled", searchShown: true), limit)
        shot("filters-pulled")

        app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        filtersScrolledAway("scrolled")
        shot("filters-scrolled")
        app.collectionViews.firstMatch.swipeUp(velocity: .fast)
        filtersScrolledAway("moving")
        for _ in 0..<12 { app.collectionViews.firstMatch.swipeUp(velocity: .fast) }
        filtersScrolledAway("bottom")
        shot("filters-bottom")
        returnToTop()
        XCTAssertLessThan(gapAboveFilters("back", searchShown: true), limit)

        // Pull to refresh, then let the list settle before looking for the old blurred gap.
        app.collectionViews.firstMatch.swipeDown(velocity: .slow)
        sleep(1)
        XCTAssertLessThan(gapAboveFilters("refreshed", searchShown: true), limit)
        shot("filters-refreshed")
        // Needs You's cards can fill the screen above the first row: scroll to it, and back to the top after the chat
        // (the list keeps its place on the way back, #382).
        reach(row(0), "the first row").tap()
        backToList(atTop: false)
        returnToTop()
        XCTAssertLessThan(gapAboveFilters("chat-back", searchShown: true), limit)
        app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        filtersScrolledAway("chat-back-scrolled")
        returnToTop()
        XCTAssertLessThan(gapAboveFilters("chat-back-top", searchShown: true), limit)

        XCUIDevice.shared.press(.home)
        sleep(2)
        app.activate()
        see(filter("all"), "the list after coming back")
        XCTAssertLessThan(gapAboveFilters("foreground", searchShown: true), limit)
        ShellFinger(app).switchTo(agents: false, "Assistant during the filter-bar round trip")
        sleep(1)
        ShellFinger(app).switchTo(agents: true, "Agents during the filter-bar round trip")
        see(filter("all"), "the list after switching tabs")
        XCTAssertLessThan(gapAboveFilters("tabs"), limit, "the reopened tab starts with folded search")
        shot("filters-after-tabs")

        let search = app.revealAgentsSearch()
        search.tapCenter()
        search.typeText("platform")
        sleep(1)
        XCTAssertLessThan(gapAboveFilters("searching", searchShown: true), limit)
        shot("filters-searching")
        // With the keyboard up, `collectionViews.firstMatch` is the keyboard's suggestion strip, not the list, so its
        // swipe left the first row where it was. A synthesized drag over the visible rows scrolls it away (as in
        // ChatScrollKeyboardTests); not checked with a finger on a device.
        dragListUp()
        filtersScrolledAway("searching-scrolled")
        shot("filters-searching-scrolled")
    }

    /// The initial layout can happen behind a restored chat. Returning must still show a folded search and the
    /// selected chip drawn at its frame, with no duplicate top inset; pulling then reveals the search drawer.
    @MainActor func testFilterBarHugsTheTopBehindARestoredChat() throws {
        let key = try XCTUnwrap(firstAgentKey())
        launch(chat: key)
        see(app.buttons["更多"].firstMatch, "the restored chat", timeout: 15)
        backToList()
        shot("filters-restored-back")
        XCTAssertLessThan(gapAboveFilters("restored-back"), 24)
        app.revealAgentsSearch()
        XCTAssertLessThan(gapAboveFilters("restored-pulled", searchShown: true), 24)
        shot("filters-restored-pulled")
        app.collectionViews.firstMatch.swipeUp(velocity: .slow)
        filtersScrolledAway("restored-back-scrolled")
        returnToTop()
        XCTAssertLessThan(gapAboveFilters("restored-top", searchShown: true), 24)
    }

    private func firstAgentKey() -> String? {
        guard let data = try? Data(contentsOf: URL(string: hub + "/api/v1/sessions")!),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let s = (obj["sessions"] as? [[String: Any]])?.first(where: { $0["assistant"] as? Bool != true }),
              let m = s["machine_id"] as? String, let id = s["session_id"] as? String else { return nil }
        return m + "/" + id
    }

    // MARK: Long list

    /// Scrolling ~120 agents and switching the look: hitches in the test report (no hard limit on a simulator).
    @MainActor func testScrollingTheLongList() throws {
        launch()
        listMenu().tap()
        see(app.buttons["全部展开"].exists ? app.buttons["全部展开"] : app.buttons["全部折叠"], "the fold item")
        app.buttons["细列表"].tap()
        let list = app.collectionViews.firstMatch
        measure(metrics: [XCTOSSignpostMetric.scrollingAndDecelerationMetric]) {
            list.swipeUp(velocity: .fast)
            list.swipeDown(velocity: .fast)
        }
    }

    /// By Status: Seen starts folded and its header is its one fold — one tap shows its agents, with no "N more idle" row to
    /// tap as well (2026-10-31: it took two taps where the other states take one); a second tap folds it again.
    @MainActor func testStateGroupingOpensSeenInOneTap() throws {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "dev", "-openAgentsFresh", "YES", "-dev.grouping", "state"]
        app.launchPinned()
        see(filter("all"), "the quick filters", timeout: 15)
        // Seen is the last section, under every other agent (~120 in the mock).
        for _ in 0..<20 where !(header("已看").exists && header("已看").isHittable) { app.swipeUp(velocity: .fast) }
        let seen = see(header("已看"), "已看's header")
        XCTAssertTrue(seen.label.contains("已折叠"), "已看 starts folded: \(seen.label)")
        seen.tap()
        wait("已看 opens") { self.header("已看").label.contains("已展开") }
        // Its agents come right under the header (the list builds them as they scroll in).
        app.swipeUp(velocity: .slow)
        let top = header("已看").exists ? header("已看").frame.maxY : 0
        let under = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["agents.row", "agents.compactRow"]))
            .allElementsBoundByIndex.filter { $0.exists && $0.frame.minY > top }
        XCTAssertFalse(under.isEmpty, "no agents under 已看 after one tap")
        XCTAssertFalse(app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "展开 ")).firstMatch.exists,
                       "a 还有 N 个空闲 row inside 已看")
        shot("agents-state-seen-open")
        // The swipe may have taken the header off the top: back to it before folding.
        for _ in 0..<5 where !(header("已看").exists && header("已看").isHittable) { app.swipeDown(velocity: .slow) }
        header("已看").tap()
        wait("已看 folds again") { self.header("已看").label.contains("已折叠") }
    }
}
