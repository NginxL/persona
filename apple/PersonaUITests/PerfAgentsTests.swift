import XCTest

/// Agents: the list, agent chats and what opens from them, and the machine pages (#5730).
/// Mock (`scripts/app/perf-test.sh`, suite `agents`): MOCK_AGENTS=1 MOCK_BIG=1 MOCK_CHAT_LINES=1 MOCK_SUBAGENT_CARDS=1
/// MOCK_FILES=1 MOCK_MACHINES=1 MOCK_TURN_IMAGES=many.
final class PerfAgentsTests: PerfTestCase {
    static let weather = "macbook/a1f3c2d4-0000-4000-8000-000000000001" // long history, working
    static let weatherTitle = "给天气 App 加主屏幕小组件" // i18n-ignore: the mock hub's title for `weather`
    static let big = "server/a1f3c2d4-0000-4000-8000-000000000008" // MOCK_BIG: 650 commands in one exchange
    static let lines = "macbook/a1f3c2d4-0000-4000-8000-0000000c1e01" // MOCK_CHAT_LINES: every kind of line
    static let subagents = "macbook/a1f3c2d4-0000-4000-8000-0000000c1e02" // MOCK_SUBAGENT_CARDS
    static let files = "macbook/a1f3c2d4-0000-4000-8000-000000000006" // MOCK_FILES
    static let blog = "studio/a1f3c2d4-0000-4000-8000-000000000003" // MOCK_TURN_IMAGES=many

    @MainActor private func openList(_ extra: [String] = []) {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", "", "-openAgentsFresh", "YES"] + extra)
    }

    /// Wait for the session's own header so the single cold launch measures the populated chat (#9167).
    @MainActor private func openChat(_ key: String, _ extra: [String] = [], title: String? = nil) {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", "", "-openSession", key, "-holdChromeOpen", "YES"] + extra)
        see(chatReady, "the chat \(key)", timeout: 30)
        if let title {
            let header = app.descendants(matching: .any)
                .matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "chat.title", title)).firstMatch
            see(header, "the header of \(title)", timeout: 20)
        }
        sleep(2)
    }

    /// What shows a chat is open: its title (the shell's compact header), the composer, or the old header's ···.
    private var chatReady: XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(format: "identifier IN %@ OR label == %@",
                                                             ["chat.compactTitle", "shell-composer-field"], "更多")).firstMatch
    }

    /// The agent rows sit under the Needs You cards (several screens on a phone): the Unread filter puts rows on top.
    @MainActor private func rowsOnTop() -> XCUIElementQuery {
        app.buttons["agents.filter.unread"].tap()
        sleep(2)
        let rows = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["agents.row", "agents.compactRow"]))
        for _ in 0..<6 where !(rows.firstMatch.exists && rows.firstMatch.isHittable) { list.swipeUp(velocity: .slow) }
        return rows
    }

    private var list: XCUIElement { app.collectionViews.firstMatch }

    // MARK: The Agents list

    /// ~130 agents as cards, grouped by repository (the default), with an app-driven frame window.
    @MainActor func testAgentsListScroll() throws {
        openList(["-dev.grouping", "repo"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        measureVerifiedGlide()
    }

    /// A query retained after a result opens and closes: the field stays reachable while the filtered list glides (#7332).
    @MainActor func testAgentsListScrollWithRetainedQuery() throws {
        openList(["-dev.grouping", "repo"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        let field = app.revealAgentsSearch()
        field.tap()
        field.typeText("a")
        field.typeText("\n")
        sleep(2)
        let row = see(app.buttons.matching(identifier: "agents.row").firstMatch, "a query result")
        row.tap()
        see(chatReady, "the result chat", timeout: 15)
        back()
        XCTAssertTrue(chatReady.waitForNonExistence(timeout: 10), "the result chat still covers the retained list")
        see(row, "the retained query results")
        let retained = app.searchFields.firstMatch
        XCTAssertTrue(retained.waitForExistence(timeout: 5) && retained.isHittable, "the retained field is not visible")
        XCTAssertTrue(row.isHittable, "the retained result row is not visible")
        XCTAssertEqual(retained.value as? String, "a")
        sleep(1) // let the navigation transition finish before FrameProbe chooses the list to glide
        measureVerifiedGlide()
    }

    /// Grouped by machine, then by state: other section structures over the same agents.
    @MainActor func testAgentsListScrollByMachine() throws {
        openList(["-dev.grouping", "machine"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        measureScroll(list)
    }

    @MainActor func testAgentsListScrollByState() throws {
        openList(["-dev.grouping", "state"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        measureScroll(list)
    }

    /// The compact rows: many more rows per screen.
    @MainActor func testAgentsCompactListScroll() throws {
        openList(["-dev.grouping", "repo", "-dev.compact", "YES"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        measureScroll(list)
    }

    /// Quick filters All → Unread → Working → All: the list re-filtered under an animation.
    @MainActor func testAgentsQuickFilters() throws {
        openList(["-dev.grouping", "repo"])
        let all = see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        measureTransition(navigation: false) {
            app.buttons["agents.filter.unread"].tap()
            usleep(600_000)
            app.buttons["agents.filter.working"].tap()
            usleep(600_000)
            all.tap()
            usleep(600_000)
        }
    }

    /// The list at rest while the hub pushes session changes (12 a round, every 300 ms).
    @MainActor func testAgentsListIdleUnderPushes() throws {
        openList(["-dev.grouping", "repo"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        measureIdle(push: "churn?n=12&ms=300")
    }

    /// Agents list → an agent chat → back.
    @MainActor func testOpenAgentChatFromList() throws {
        openList(["-dev.grouping", "repo"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        let rows = rowsOnTop()
        let row = see(rows.firstMatch, "an agent row")
        measureTransition {
            row.tap()
            wait(chatReady)
            sleep(1)
            back()
            wait(row)
            sleep(1)
        }
    }

    /// The heaviest chat (MOCK_BIG) opened from scratch: pushed over the list, then back. Each iteration opens it from
    /// the list again; after the first it is in memory (warm), the first one is cold.
    @MainActor func testOpenHeavyChat() throws {
        openList(["-dev.grouping", "repo"])
        see(app.buttons["agents.filter.all"], "the quick filters", timeout: 30)
        // MOCK_BIG's session is titled 「把构建脚本都整理一遍」 (shop-api › build): found by the list's search.
        app.revealAgentsSearch()
        let search = app.searchFields.firstMatch
        if search.waitForExistence(timeout: 5) { search.tap(); search.typeText("构建脚本") }
        sleep(2)
        let target = see(app.buttons.matching(NSPredicate(format: "identifier IN %@", ["agents.row", "agents.compactRow"]))
            .firstMatch, "the big chat's row")
        measureTransition {
            target.tap()
            wait(chatReady, timeout: 15)
            sleep(2)
            back()
            wait(target)
            sleep(1)
        }
    }

    // MARK: Agent chats

    /// The long history of a working chat (texts, tool rows, Markdown).
    @MainActor func testAgentChatScrollLongHistory() throws {
        openChat(Self.weather, ["-loadAll", "YES"], title: Self.weatherTitle)
        measureScroll(mainScroller(), first: .down)
    }

    /// 650 commands and 110 notes in one exchange, every work line open.
    @MainActor func testAgentChatScrollBig() throws {
        openChat(Self.big, ["-loadAll", "YES", "-openAllWork", "YES"])
        measureScroll(mainScroller(), first: .down)
    }

    /// Every kind of line the chat draws (ChatLines): plans, questions, subagents, diffs, tables, code.
    @MainActor func testAgentChatScrollAllLines() throws {
        openChat(Self.lines, ["-loadAll", "YES"])
        measureScroll(mainScroller(), first: .down)
    }

    /// Pictures in the turns (MOCK_TURN_IMAGES=many).
    @MainActor func testAgentChatScrollImages() throws {
        openChat(Self.blog, ["-loadAll", "YES"])
        measureScroll(mainScroller(), first: .down)
    }

    /// Typing a sentence into the composer of a long chat, and deleting it.
    @MainActor func testAgentChatTyping() throws {
        openChat(Self.weather)
        measureTyping(composer())
    }

    /// A work line opened and folded again (its steps unfold under `Theme.Motion.reveal`).
    @MainActor func testOpenAndFoldWorkLine() throws {
        openChat(Self.lines)
        let head = see(app.buttons["work-head"].firstMatch, "a work line", timeout: 15)
        measureTransition(navigation: false) {
            head.tap()
            sleep(1)
            head.tap()
            sleep(1)
        }
    }

    /// From the main agent to a running subagent's transcript and back (the strip under the header).
    @MainActor func testSwitchToSubagentAndBack() throws {
        openChat(Self.subagents)
        let sub = see(button(labelContains: "回归：搜索全量"), "the running subagent's chip", timeout: 15)
        let main = button(labelContains: "主 Agent")
        measureTransition(navigation: false) {
            sub.tap()
            sleep(2)
            main.tap()
            sleep(2)
        }
    }

    /// The subagent's own transcript scrolled.
    @MainActor func testSubagentScroll() throws {
        openChat(Self.subagents, ["-openAgent", "scdone"])
        sleep(5) // `-openAgent` switches 4 s after the chat shows
        measureScroll(mainScroller(), first: .down)
    }

    /// A question card over the chat: the chat scrolled behind it.
    @MainActor func testScrollBehindQuestionCard() throws {
        openChat(Self.weather, ["-showSampleQuestion", "YES"])
        measureScroll(mainScroller(), first: .down)
    }

    /// The terminal (More › Terminal, a full-screen cover) opened and closed.
    @MainActor func testTerminalOpenClose() throws {
        openChat(Self.weather, ["-terminalTipsSeen", "YES"])
        let more = button(labelContains: "更多")
        measureTransition(navigation: false) {
            if !more.exists || !more.isHittable { any("chat.compactTitle").tap(); sleep(1) }
            if more.exists && more.isHittable { more.tap() }
            let item = app.buttons["终端"].firstMatch
            if wait(item, timeout: 5) { item.tap() }
            let close = any("terminal-close")
            if wait(close) {
                sleep(1)
                let tips = app.buttons["知道了"]
                if tips.exists { tips.tap() }
                close.tap()
            }
            wait(close, shown: false)
            sleep(1)
        }
    }

    /// A Markdown file opened from the chat (the file viewer sheet), scrolled.
    @MainActor func testFileViewerScroll() throws {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", "", "-openSession", Self.files,
                "-openFilePath", "/Users/me/code/weather/README.md"])
        let view = app.descendants(matching: .any)
            .matching(NSPredicate(format: "identifier IN %@", ["file-code", "file-markdown"])).firstMatch
        see(view, "the file", timeout: 30)
        sleep(2)
        measureScroll(mainScroller())
    }

    /// A code file in the viewer (UIKit lines, highlighted off the main thread).
    @MainActor func testCodeFileScroll() throws {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", "", "-openSession", Self.files,
                "-openFilePath", "/Users/me/code/weather/server/handlers.go"])
        see(any("file-code"), "the code file", timeout: 30)
        sleep(2)
        measureScroll(mainScroller())
    }

    /// The image viewer paged through five pictures.
    @MainActor func testImageViewerPaging() throws {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", "", "-openSession", Self.blog, "-openViewedImage", "1"])
        see(app.buttons["viewer-close"], "the viewer", timeout: 30)
        let page = app.windows.firstMatch
        measureFrames(metrics()) {
            for _ in 0..<3 { page.swipeLeft(velocity: .fast); usleep(400_000) }
            for _ in 0..<3 { page.swipeRight(velocity: .fast); usleep(400_000) }
        }
    }

    /// The in-app link sheet dragged to the 0.6 detent and back up.
    @MainActor func testLinkSheetDetents() throws {
        launch(["-app.tab", "chats", "-tab", "dev", "-dev.openChat", "", "-openSession", Self.weather,
                "-openLinkURL", hub + "/"])
        see(app.buttons["link-close"], "the link sheet", timeout: 20)
        sleep(2)
        let w = app.windows.firstMatch
        measureFrames(metrics()) {
            w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.08))
                .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45)))
            sleep(1)
            w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.45))
                .press(forDuration: 0.05, thenDragTo: w.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.05)))
            sleep(1)
        }
    }

    // Machine coverage is in PerfMachinesTests, with explicit loaded-data and displacement checks.

    // MARK: Helpers

    /// The composer's text field: the shell's (typed into only, ShellComposerBar is not driven otherwise) or the old one.
    @MainActor func composer() -> XCUIElement {
        let shellField = any("shell-composer-field")
        if shellField.waitForExistence(timeout: 3) { return shellField }
        return app.textViews.firstMatch.exists ? app.textViews.firstMatch : app.textFields.firstMatch
    }
}
