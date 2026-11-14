import XCTest

/// An Agents chat's header, open and folded (`ChromeHeader`), opened from the list as a user would: it stays below the
/// status bar, and the one Back VoiceOver and XCUITest find is the one on screen, which goes back. In the BeautifulUI
/// style the folded bar once sat in the status bar's band (its place pill on the clock), and the hidden bar's Back stayed
/// in the accessibility tree, hittable but going nowhere (2026-11-02). Both styles with
/// `TEST_RUNNER_PERSONA_UI_STYLE=beautiful`.
final class ChatHeaderTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    /// Opens the first agent from the list; `folded`: the header folds itself after its idle seconds, else it is held open.
    /// `sideBars`: laid out as under iPhone Duo's side bars (`-sideBars YES`): the system's bar takes the actions, Back
    /// first, and the page draws no back of its own (2026-11-04: two backs, the system's and ours, one over the other).
    @MainActor private func openChat(folded: Bool, sideBars: Bool = false) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "chats", "-tab", "dev",
                               "-dev.openChat", "", "-openAgentsFresh", "YES", "-holdChromeOpen", folded ? "NO" : "YES",
                               "-sideBars", sideBars ? "YES" : "NO"]
        app.launchPinned()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no agent in the list")
        sleep(1)
        row.tap()
        XCTAssertTrue(app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch.waitForExistence(timeout: 10)
                      || app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch.waitForExistence(timeout: 5),
                      "the chat did not open")
        sleep(folded ? 6 : 2) // folded: past the 3 s it waits, and the fold's animation
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    /// The status bar's bottom: SpringBoard's status bar where XCUITest finds it, else the shortest one a notched or
    /// Dynamic Island iPhone has.
    @MainActor private var statusBarBottom: CGFloat {
        let bar = XCUIApplication(bundleIdentifier: "com.apple.springboard").statusBars.firstMatch
        return bar.exists && bar.frame.height > 0 ? bar.frame.maxY : 44
    }

    @MainActor private func check(_ what: String, folded: Bool, sideBars: Bool = false) {
        shot(what)
        if sideBars { // the system's own back (UIKit's "BackButton", labelled with the page before) besides ours
            let system = app.navigationBars.buttons.matching(identifier: "BackButton").allElementsBoundByIndex.filter(\.isHittable)
            XCTAssertTrue(system.isEmpty, "\(what): the system's back as well as 返回: \(system.map(\.label))")
            checkOneTitle(what)
        }
        let backs = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex
        if backs.count != 1 {
            let t = XCTAttachment(string: app.debugDescription)
            t.name = "\(what) tree"
            t.lifetime = .keepAlways
            add(t)
        }
        XCTAssertEqual(backs.count, 1, "\(what): \(backs.count) 返回 in the accessibility tree (a hidden one exposed?): \(backs.map(\.frame))")
        guard let back = backs.first else { return }
        XCTAssertTrue(back.isHittable, "\(what): 返回 not hittable")
        let top = statusBarBottom
        XCTAssertGreaterThanOrEqual(back.frame.minY, top - 1, "\(what): 返回 at \(back.frame) is in the status bar (to \(top))")
        if folded {
            let place = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch
            XCTAssertTrue(place.exists, "\(what): no folded bar")
            XCTAssertGreaterThanOrEqual(place.frame.minY, top - 1, "\(what): the folded bar at \(place.frame) is on the status bar (to \(top))")
        }
        back.tap()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 5) && row.isHittable, "\(what): 返回 did not go back to the list")
    }

    @MainActor func testOpenHeaderInTheShell() {
        openChat(folded: false)
        check("shell open", folded: false)
    }

    @MainActor func testFoldedHeaderInTheShell() {
        openChat(folded: true)
        check("shell folded", folded: true)
    }

    @MainActor func testSideBarsInTheShell() {
        openChat(folded: false, sideBars: true)
        checkSideBars("shell side bars")
    }

    /// Folded, the slim bar keeps the open header's ⋯ (#7962): one of it, a finger's square, across from ‹ with the
    /// capsule still in the middle, and the same items as the open header's ⋯ (one menu for both, `ChatView.moreMenu`).
    @MainActor func testFoldedHeaderHasTheOpenMenu() {
        openChat(folded: true)
        let place = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch
        XCTAssertTrue(place.exists, "no folded bar")
        let mores = app.buttons.matching(identifier: "chat.more").allElementsBoundByIndex
        XCTAssertEqual(mores.count, 1, "\(mores.count) ⋯ in the accessibility tree with the header folded: \(mores.map(\.frame))")
        guard let more = mores.first else { return }
        XCTAssertTrue(more.isHittable, "the folded ⋯ is not hittable")
        XCTAssertGreaterThanOrEqual(min(more.frame.width, more.frame.height), 44, "the folded ⋯ is smaller than a finger: \(more.frame)")
        XCTAssertGreaterThanOrEqual(more.frame.midY, statusBarBottom, "the folded ⋯ at \(more.frame) is in the status bar")
        XCTAssertEqual(more.frame.midY, place.frame.midY, accuracy: 1, "the folded ⋯ is off the capsule's line")
        let window = app.windows.firstMatch.frame
        let back = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch
        if back.exists { // across from ‹ (a split's first page has none); ‹ reports its glyph's frame, so by the middles
            XCTAssertEqual(window.maxX - more.frame.midX, back.frame.midX - window.minX, accuracy: 2, "⋯ \(more.frame) and ‹ \(back.frame) are not mirrored")
            XCTAssertEqual(place.frame.midX, window.midX, accuracy: 1, "the capsule \(place.frame) is off the middle")
        }
        let folded = menuItems(more, "folded")

        // The open header: held open, and opened from the capsule when the chat lands folded (an unread one does).
        openChat(folded: false)
        let slim = app.buttons.matching(NSPredicate(format: "label ENDSWITH %@", "显示标题和标签")).firstMatch
        if slim.exists, slim.isHittable { slim.tap(); sleep(2) }
        XCTAssertFalse(slim.exists, "the slim bar still shows with the header opened")
        let open = app.buttons.matching(identifier: "chat.more").firstMatch
        XCTAssertTrue(open.waitForExistence(timeout: 5), "no ⋯ in the open header")
        XCTAssertEqual(menuItems(open, "open"), folded, "the folded ⋯ lists other items than the open header's")
    }

    /// The items ⋯ opens, by label: the buttons there with its menu up and not before. The menu stays up (the next
    /// launch closes it).
    @MainActor private func menuItems(_ more: XCUIElement, _ what: String) -> [String] {
        let before = Set(app.buttons.allElementsBoundByIndex.map(\.label))
        more.tap()
        let rename = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "重命名")).firstMatch
        XCTAssertTrue(rename.waitForExistence(timeout: 5), "\(what): ⋯ did not open its menu")
        sleep(1)
        shot("\(what) menu")
        let items = app.buttons.allElementsBoundByIndex.map(\.label).filter { !before.contains($0) }
        XCTAssertTrue(items.contains { $0.hasPrefix("重命名") }, "\(what): the menu's items not found: \(items)")
        return items
    }

    /// The session's name once, in the header (`chat.title`): the system draws its bar's title over the content, not in
    /// the side column, so it showed a second time right above the header's (2026-11-08, TestFlight).
    @MainActor private func checkOneTitle(_ what: String) {
        let header = app.buttons["chat.title"].firstMatch
        XCTAssertTrue(header.exists, "\(what): no title in the header")
        let system = app.navigationBars.staticTexts.allElementsBoundByIndex.filter { !$0.label.isEmpty && header.label.hasPrefix($0.label) }
        XCTAssertTrue(system.isEmpty, "\(what): the system bar's title as well as the header's: \(system.map(\.label))")
    }

    /// One back, the chat's own: as `check` on an iPhone width; the split's first page (iPad, iPhone Duo open) has none.
    @MainActor private func checkSideBars(_ what: String) {
        guard app.windows.firstMatch.frame.width > 600 else { return check(what, folded: false, sideBars: true) }
        shot(what)
        checkOneTitle(what)
        let backs = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).allElementsBoundByIndex.filter(\.isHittable)
        let system = app.navigationBars.buttons.matching(identifier: "BackButton").allElementsBoundByIndex.filter(\.isHittable)
        XCTAssertTrue(backs.isEmpty && system.isEmpty, "\(what): a back on the split's first page: \(backs.map(\.frame)) \(system.map(\.label))")
    }
}

/// What the open header presents stays while it is up: folded, the header leaves the view tree, so it must not fold
/// itself under its own sheet or menu.
final class ChatHeaderPresentationTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    /// Opens the multi-agent weather fixture from the list, as a user would, with its header open: its space has several
    /// tabs. Not the list's first row: that is an unread agent alone in its space, which lands with the header folded and
    /// has no tabs (#8003). Searched for when it is not on screen, as `ReturnToChatTests` does.
    @MainActor private func openChat() {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "chats", "-tab", "dev",
                               "-dev.openChat", "", "-openAgentsFresh", "YES", "-holdChromeOpen", "NO"]
        app.launchPinned()
        XCTAssertTrue(app.buttons.matching(identifier: "agents.row").firstMatch.waitForExistence(timeout: 15), "no agent in the list")
        sleep(1)
        let row = app.buttons.matching(NSPredicate(format: "identifier == 'agents.row' AND label CONTAINS %@",
                                                  "给天气 App 加主屏幕小组件")).firstMatch // i18n-ignore: bundled synthetic fixture title
        if !row.exists || !row.isHittable {
            app.revealAgentsSearch()
            let search = app.searchFields.firstMatch
            XCTAssertTrue(search.waitForExistence(timeout: 5), "the Agents search did not open")
            search.tap()
            search.typeText("天气") // i18n-ignore: bundled synthetic fixture title
        }
        XCTAssertTrue(row.waitForExistence(timeout: 15), "the multi-agent fixture is not in the list")
        row.tap()
        // Landed folded (at its newest end): the slim bar's title opens the header, as a user would. A second is enough
        // to tell; longer would eat into the 3 s the open header waits before folding itself.
        sleep(1)
        let compact = app.buttons["chat.compactTitle"].firstMatch
        if compact.exists, compact.isHittable { compact.tap() }
    }

    @MainActor func testTheTabsSheetStays() {
        openChat()
        let tabs = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "同一空间的")).firstMatch
        XCTAssertTrue(tabs.waitForExistence(timeout: 5), "no ⋯ for the space's tabs")
        tabs.tap()
        // Its title counts the agents ("6 agents"; the tests run in Chinese, hence the pattern). It counted tabs
        // ("N tabs") before b81594a005, 2026-11-05.
        let sheet = app.staticTexts.matching(NSPredicate(format: "label MATCHES %@", "[0-9]+ 个 Agent")).firstMatch
        XCTAssertTrue(sheet.waitForExistence(timeout: 5), "the tabs sheet did not open")
        sleep(6) // past the 3 s the header waits before folding
        XCTAssertTrue(sheet.exists, "the tabs sheet closed by itself")
    }

    @MainActor func testTheMoreMenuStays() {
        openChat()
        let more = app.buttons.matching(NSPredicate(format: "label == %@", "更多")).firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no ⋯")
        more.tap()
        let rename = app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", "重命名")).firstMatch
        XCTAssertTrue(rename.waitForExistence(timeout: 5), "the menu did not open")
        sleep(6)
        XCTAssertTrue(rename.exists, "the menu closed by itself")
    }
}
