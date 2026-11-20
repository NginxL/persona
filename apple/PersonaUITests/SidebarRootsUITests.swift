import XCTest

/// The Sidebar layout's page (#8359, docs/design/sidebar-navigation.md option 3): every sidebar item is a root of its own.
/// A pick replaces the page with no push and no Back; only a drill-down inside a page pushes, with Back; each root keeps
/// its drill-down while the user is elsewhere; the root in front picked again goes back to it; at a root an edge swipe
/// reaches no other item. Read from the app's own probe (`probe.sidebar-page`: the root, the path, and how many animated
/// navigation transitions UIKit has run). Demo data in the Merged layout: an iPad, the open iPhone Duo (upright, where the
/// column lies over the page, and on its side) and an iPhone (the drawer).
final class SidebarRootsUITests: XCTestCase {
    private var app: XCUIApplication!

    private struct Page: Decodable, CustomStringConvertible {
        var root: String
        var path: Int
        var transitions: Int
        var depth: Int
        var description: String { "root \(root), path \(path), transitions \(transitions), depth \(depth)" }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    @MainActor private func launch(_ extra: [String] = []) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-newAssistant", "YES", "-tab", "assistant", "-dev.openChat", "",
                               "-app.navigation", "sidebar", "-probes", "YES"] + extra
        app.launchPinned(navigation: false)
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    /// The sidebar's scrolling column: beside the page, or a phone's drawer (whose host names it as the Separate layout's).
    @MainActor private var column: XCUIElement {
        let split = element("unified-sidebar")
        return split.exists ? split : element("assistant-sidebar")
    }

    /// The page's probe: the Sidebar layout's (and the Persona tab's), or the Separate layout's Agents tab's (#8603).
    private var probeName = "sidebar-page"

    @MainActor private func page() -> Page? { app.probe(probeName, as: Page.self) }

    /// What brings the column out and is in it: the main conversation's row; the Agents tab has none, its head's fold button.
    @MainActor private var columnAnchor: XCUIElement {
        probeName == "sidebar-page" ? element("sidebar-main") : element("sidebar-fold-column")
    }

    /// The chat in front's title: the open header's, or the folded bar's.
    @MainActor private func chatTitle() -> String {
        // From a snapshot: while one chat replaces another the element can go between a check and a read.
        for id in ["chat.title", "chat.compactTitle"] {
            if let label = (try? element(id).snapshot())?.label, !label.isEmpty { return label }
        }
        return ""
    }

    /// The page once `root` is in front and nothing has moved for a moment (a push, had there been one, has run).
    @MainActor private func settled(_ root: String, _ step: String) -> Page {
        var last: Page?
        var stableSince = Date.now
        let deadline = Date.now.addingTimeInterval(20) // a page with live charts (Machines) slows every query
        while Date.now < deadline {
            let now = page()
            if now?.root != last?.root || now?.path != last?.path || now?.transitions != last?.transitions { stableSince = .now }
            last = now
            if now?.root == root, Date.now.timeIntervalSince(stableSince) > 1.0 { return now! }
            Thread.sleep(forTimeInterval: 0.2)
        }
        XCTFail("\(step): the page did not settle on \(root): \(last.map(\.description) ?? "no probe")\n\(app.debugDescription)")
        return last ?? Page(root: "", path: 0, transitions: 0, depth: 0)
    }

    /// UIKit's own back button on the page in front: in the navigation bar, or in the system's side bars (the open Duo on
    /// its side).
    @MainActor private var systemBack: Bool {
        app.buttons.matching(identifier: "BackButton").allElementsBoundByIndex.contains { $0.exists && $0.isHittable }
    }

    /// The sidebar can be tapped: beside the page it always is; laid over the page (the open Duo upright) or in a phone's
    /// drawer it is brought out first.
    @MainActor private func reveal(_ target: XCUIElement) {
        if target.exists && target.isHittable { return }
        let systemToggle = app.buttons.matching(NSPredicate(format: "identifier == 'ToggleSidebar' OR label IN %@",
                                                            ["Show Sidebar", "显示边栏", "显示侧边栏"])).firstMatch
        // The Separate layout's Agents tab with its column folded and nothing open: the empty page's Show List.
        let showList = app.buttons.matching(NSPredicate(format: "label IN %@", ["Show List", "显示列表"])).firstMatch
        for opener in [element("assistant-sidebar-button"), element("sidebar-drawer-button"), element("chat-sidebar-button"),
                       element("shell-place-sidebar"), element("sidebar-show-button"), element("chevron.left"), systemToggle, showList]
        where opener.exists && opener.isHittable {
            // At the frame just read: a button on its way out as the page settles failed the tap's own lookup.
            let frame = opener.frame
            app.windows.firstMatch.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX, dy: frame.midY)).tap()
            if target.waitForExistence(timeout: 3), target.isHittable { return }
        }
        // A phone's drawer from anywhere on a root: a drag to the right.
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: CGVector(dx: 0.3, dy: 0.5))
            .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.5)))
        XCTAssertTrue(target.waitForExistence(timeout: 3) && target.isHittable,
                      "the sidebar could not be brought out; the bar's buttons: \(app.navigationBars.buttons.debugDescription)\n\(app.debugDescription)")
    }

    /// Picks a sidebar row by its identifier, scrolling the column to it (and unfolding Pages if it is folded).
    @MainActor private func pick(_ id: String, _ step: String) {
        let target = element(id)
        let main = element("sidebar-main")
        reveal(main)
        if !target.exists, element("sidebar-fold-places").exists, id.hasPrefix("sidebar-") {
            element("sidebar-fold-places").tap()
        }
        // Above or below where the column was left scrolled.
        for _ in 0..<6 where !(target.exists && target.isHittable) { column.swipeDown(velocity: .fast) }
        for _ in 0..<8 where !(target.exists && target.isHittable) { column.swipeUp(velocity: .slow) }
        XCTAssertTrue(target.exists && target.isHittable, "\(step): \(id) not reachable in the sidebar:\n\(app.debugDescription)")
        target.tap()
    }

    /// The floating "N Need You" jump (`SidebarNeedsJump`) over a row near the column's foot takes the tap.
    @MainActor private func underJump(_ row: XCUIElement) -> Bool {
        let jump = app.buttons["sidebar-needs-jump"].firstMatch
        return jump.exists && row.exists && row.frame.insetBy(dx: 0, dy: -24).intersects(jump.frame)
    }

    /// The column's head (and the main conversation held under it) over a row scrolled to the column's top takes the tap.
    @MainActor private func underHead(_ row: XCUIElement) -> Bool {
        let main = columnAnchor
        return main.exists && row.exists && row.frame.midY < main.frame.maxY + 8
    }

    /// Taps a row clear of the jump and the head: the column scrolled a little first where either lies over the row.
    @MainActor private func tapClear(_ row: XCUIElement) {
        for _ in 0..<3 where underJump(row) || underHead(row) {
            if underJump(row) { column.swipeUp(velocity: .slow) } else { column.swipeDown(velocity: .slow) }
            Thread.sleep(forTimeInterval: 0.6) // the list's glide
        }
        // At the frame just read: a row the list moves as it settles failed the tap's own lookup.
        guard let frame = (try? row.snapshot())?.frame else { return }
        app.windows.firstMatch.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX, dy: frame.midY)).tap()
    }

    /// Picks an Agent's row in the sidebar's Agents list, scrolling to it; a row tapped while the list still glides after
    /// the scroll can be missed, so again until the Agent is the root (at most three taps).
    @MainActor private func pickAgent(_ step: String, titled title: String? = nil, excluding: String? = nil,
                                      until done: (() -> Bool)? = nil) -> Page {
        let rows = app.buttons.matching(identifier: "agents.row")
        let anchor = columnAnchor
        reveal(anchor)
        // Any Agent: the first row in reach, clear of the jump and the head (and not `excluding`); a named one: its row.
        func find() -> XCUIElement {
            if let title { return rows.matching(NSPredicate(format: "label CONTAINS %@", title)).firstMatch }
            // Found by its label, not its place in the list (an element bound by index can name another row, or none,
            // once the list has scrolled).
            let clear = rows.allElementsBoundByIndex.first {
                $0.exists && $0.isHittable && !underJump($0) && !underHead($0) && !(excluding.map($0.label.contains) ?? false)
            }
            guard let label = (clear.flatMap { try? $0.snapshot() })?.label else { return rows.firstMatch }
            return rows.matching(NSPredicate(format: "label == %@", label)).firstMatch
        }
        var row = find()
        for _ in 0..<16 where !(row.exists && row.isHittable) {
            column.swipeUp(velocity: .slow)
            row = find()
        }
        XCTAssertTrue(row.exists && row.isHittable, "\(step): no Agent's row \(title ?? ""):\n\(app.debugDescription)")
        let done = done ?? { self.page()?.root == "agent" }
        for _ in 0..<3 where !done() {
            if !(row.exists && row.isHittable) { reveal(anchor) }
            tapClear(row)
            _ = XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: NSPredicate { _, _ in done() }, object: nil)], timeout: 5)
        }
        return settled("agent", step)
    }

    @MainActor private func assertSwitched(_ before: Page, _ after: Page, _ step: String) {
        XCTAssertEqual(after.transitions, before.transitions, "\(step): the page was pushed or popped (\(before) → \(after))")
        XCTAssertFalse(systemBack, "\(step): a Back on a root:\n\(app.debugDescription)")
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = env["PERSONA_SHOTS_APPEARANCE"] ?? "system"
        let device = env["PERSONA_SHOTS_DEVICE"] ?? (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "phone")
        let file = "sidebar-roots-\(device)-\(look)-\(name).png"
        let folder = URL(fileURLWithPath: dir)
        guard env["PERSONA_SHOTS_EXTERNAL"] != nil else {
            try? app.screenshot().pngRepresentation.write(to: folder.appendingPathComponent(file))
            return
        }
        // The open Duo's screenshots from here come out black: the host takes this one (`simctl io … screenshot`) when it
        // sees `want` name the file, and writes it; the test waits for it, at most 15 s.
        try? file.write(to: folder.appendingPathComponent("want"), atomically: true, encoding: .utf8)
        let target = folder.appendingPathComponent(file)
        let deadline = Date.now.addingTimeInterval(15)
        while Date.now < deadline, !FileManager.default.fileExists(atPath: target.path) { Thread.sleep(forTimeInterval: 0.2) }
    }

    /// From the main conversation, Recordings and an Agent replace the root (no push, no Back); Machines → a machine pushes
    /// with Back; the conversation and back to Machines brings the machine's page back with no push; Machines again goes
    /// back to its root; an edge swipe at a root stays on it.
    @MainActor func testPicksReplaceTheRootAndOnlyDrillDownsPush() {
        launch()
        var now = settled("conversation", "launch")
        shot("0-conversation")

        pick("sidebar-meeting-recording", "Recordings")
        var next = settled("recordings", "Recordings from the conversation")
        XCTAssertEqual(next.path, 0, "Recordings is not the root")
        assertSwitched(now, next, "Recordings from the conversation")
        // Its own bar (its title; on the open Duo upright the button that brings the sidebar back): not hidden by a hide
        // the conversation still had waiting (`RootBarHidden`).
        XCTAssertTrue(app.navigationBars.firstMatch.waitForExistence(timeout: 3), "Recordings has no navigation bar:\n\(app.debugDescription)")
        shot("1-recordings")
        now = next

        next = pickAgent("an Agent from Recordings")
        XCTAssertEqual(next.path, 0, "the Agent's chat is not the root")
        assertSwitched(now, next, "an Agent")
        shot("2-agent")

        pick("sidebar-main", "the conversation")
        now = settled("conversation", "the conversation from the Agent")
        pick("sidebar-meeting-recording", "Recordings again")
        next = settled("recordings", "Recordings from the conversation")
        assertSwitched(now, next, "Recordings again from the conversation")
        pick("sidebar-main", "the conversation")
        now = settled("conversation", "the conversation from Recordings")
        next = pickAgent("an Agent from the conversation")
        assertSwitched(now, next, "an Agent from the conversation")

        // A drill-down pushes, with Back.
        pick("sidebar-machines", "Machines")
        now = settled("machines", "Machines")
        // The machine's card on the page, not a row in the column that names the same computer.
        let cards = app.buttons.matching(NSPredicate(format: "label BEGINSWITH 'MacBook'"))
        _ = cards.firstMatch.waitForExistence(timeout: 10)
        let pageStart = column.exists && column.isHittable ? column.frame.maxX : 0
        func onPage() -> XCUIElement? {
            cards.allElementsBoundByIndex.first { $0.exists && $0.isHittable && $0.frame.minX >= pageStart }
        }
        for _ in 0..<6 where onPage() == nil {
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.7))
                .press(forDuration: 0.05, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.4)))
        }
        guard let card = onPage() else { return XCTFail("no MacBook card on the page:\n\(app.debugDescription)") }
        card.tap()
        next = settled("machines", "a machine")
        XCTAssertEqual(next.path, 1, "the machine's page was not pushed: \(next)")
        XCTAssertEqual(next.transitions, now.transitions + 1, "the drill-down did not push: \(now) → \(next)")
        XCTAssertTrue(systemBack, "no Back on the machine's page:\n\(app.debugDescription)")
        shot("3-machine")

        // A phone's drawer: a sub-page has only Back, the drawer opens from a root (docs/design/sidebar-navigation.md,
        // "One button on each side"). The rest needs the column beside the page.
        if app.windows.firstMatch.frame.width < 600 { return }
        // The same with the column laid over the page (the open Duo upright, #8603): a sub-page has Back alone, so the
        // sidebar is reached from a root.
        if !(element("sidebar-main").exists && element("sidebar-main").isHittable) {
            XCTAssertFalse(element("sidebar-show-button").exists, "a sidebar button beside Back on a sub-page")
            return
        }

        // Machines keeps its drill-down while the conversation is in front.
        pick("sidebar-main", "the conversation")
        now = settled("conversation", "the conversation from the machine")
        XCTAssertEqual(now.path, 0)
        assertSwitched(next, now, "the conversation from a drilled Machines")
        pick("sidebar-machines", "Machines again")
        next = settled("machines", "Machines again")
        XCTAssertEqual(next.path, 1, "Machines did not come back at the machine's page: \(next)")
        XCTAssertEqual(next.transitions, now.transitions, "coming back to Machines pushed: \(now) → \(next)")
        XCTAssertTrue(systemBack, "the machine's page came back without its Back")

        // Machines in front picked again: back to its root.
        pick("sidebar-machines", "Machines in front")
        next = settled("machines", "Machines in front again")
        XCTAssertEqual(next.path, 0, "picking Machines in front did not go to its root: \(next)")

        // An edge swipe at a root reaches no other item.
        let window = app.windows.firstMatch
        let edge = UIDevice.current.userInterfaceIdiom == .pad || element("sidebar-main").isHittable
            ? (column.frame.maxX + 2) / window.frame.width : 0.005
        window.coordinate(withNormalizedOffset: CGVector(dx: edge, dy: 0.5))
            .press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: min(edge + 0.5, 0.95), dy: 0.5)))
        let afterSwipe = settled("machines", "an edge swipe at Machines' root")
        XCTAssertEqual(afterSwipe.path, 0)
    }

    /// Where the column's head and its list start: the assistant's name in the head, the fold button, the main
    /// conversation held over the list, and the first heading (Pages), once the column is at its top.
    @MainActor private func head(_ step: String) -> [String: CGRect] {
        let main = element("sidebar-main"), fold = element("sidebar-fold-column"), pages = element("sidebar-fold-places")
        reveal(main)
        // To the column's top, also where Pages already shows with the list a little scrolled.
        column.swipeDown(velocity: .fast)
        for _ in 0..<4 where !(pages.exists && pages.isHittable) { column.swipeDown(velocity: .fast) }
        // A list let go after a swipe settles a moment later.
        Thread.sleep(forTimeInterval: 1.0)
        // The head's title is the assistant's name, which the main conversation's row starts with.
        let name = main.label.components(separatedBy: CharacterSet(charactersIn: ",，")).first ?? ""
        let title = app.staticTexts.matching(NSPredicate(format: "label == %@", name)).allElementsBoundByIndex
            .first { $0.exists && $0.frame.maxY <= main.frame.minY && $0.frame.minX < main.frame.maxX }
        XCTAssertTrue(title != nil && pages.exists, "\(step): no head title (\(name)) or Pages heading:\n\(app.debugDescription)")
        return ["the head's title": title?.frame ?? .zero, "the fold button": fold.exists ? fold.frame : .zero,
                "the main conversation": main.frame, "the Pages heading": pages.frame]
    }

    @MainActor private func assertSame(_ a: CGRect, _ b: CGRect, _ what: String, _ step: String) {
        let off = max(abs(a.minX - b.minX), abs(a.minY - b.minY), abs(a.width - b.width), abs(a.height - b.height))
        XCTAssertLessThanOrEqual(off, 1, "\(step): \(what) moved: \(a) on the main conversation, \(b) here")
    }

    /// The column's head and where its list starts do not depend on the root beside it (#8359): the persona's face, the
    /// fold button and the Pages heading are where they are on the main conversation, on Recordings, Inbox and an
    /// Agent's chat, within 1 pt.
    @MainActor func testTheSidebarHeadStaysPutOnEveryRoot() {
        continueAfterFailure = true // every root's offsets, not only the first
        launch()
        _ = settled("conversation", "launch")
        let main = head("the main conversation")
        shot("head-0-conversation")
        func check(_ root: String) {
            let here = head(root)
            shot("head-\(root)")
            for (what, frame) in main.sorted(by: { $0.key < $1.key }) { assertSame(frame, here[what] ?? .zero, what, root) }
        }
        for (id, root) in [("sidebar-meeting-recording", "recordings"), ("sidebar-inbox", "inbox")] {
            pick(id, root)
            _ = settled(root, root)
            check(root)
        }
        _ = pickAgent("an Agent")
        check("agent")
    }

    /// The Separate layout at regular width (#8359): the Persona tab's column and the Agents tab's have the same head, so
    /// the assistant's name (beside its face) and the fold button sit at the same place on both, across and down.
    @MainActor func testThePersonaAndAgentsTabsHeadsLineUp() throws {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-newAssistant", "YES", "-tab", "assistant", "-dev.openChat", "",
                               "-app.navigation", "separate", "-probes", "YES"]
        app.launchPinned(navigation: false)
        // The iPad's tab bar, or the open Duo's own tab bar in its place (#4231).
        let persona = app.buttons.matching(NSPredicate(format: "identifier == 'shell-assistant' OR label == 'Persona'")).firstMatch
        let agents = app.buttons.matching(NSPredicate(format: "identifier == 'shell-agents' OR label == 'Agents'")).firstMatch
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 20))
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("the Separate layout's tabs at regular width") }
        XCTAssertTrue(persona.waitForExistence(timeout: 20) && agents.exists, "no Persona | Agents tabs:\n\(app.debugDescription)")
        continueAfterFailure = true
        persona.tap()
        let main = element("sidebar-main"), fold = element("sidebar-fold-column")
        reveal(main)
        let name = main.label.components(separatedBy: CharacterSet(charactersIn: ",，")).first ?? ""
        func head(_ step: String) -> (title: CGRect, fold: CGRect) {
            reveal(fold)
            Thread.sleep(forTimeInterval: 1.0)
            let title = app.staticTexts.matching(NSPredicate(format: "label == %@", name)).allElementsBoundByIndex
                .first { $0.exists && $0.isHittable && abs($0.frame.midY - fold.frame.midY) < 30 && $0.frame.maxX < fold.frame.minX }
            XCTAssertTrue(title != nil, "\(step): no head title (\(name)):\n\(app.debugDescription)")
            return (title?.frame ?? .zero, fold.frame)
        }
        let p = head("Persona")
        shot("tabs-persona")
        agents.tap()
        reveal(fold) // a column left folded by an earlier run is brought out (the empty page's Show List)
        XCTAssertTrue(fold.waitForExistence(timeout: 10), "the Agents tab has no column head:\n\(app.debugDescription)")
        let a = head("Agents")
        shot("tabs-agents")
        assertSame(p.title, a.title, "the head's name (beside the face)", "the Agents tab")
        assertSame(p.fold, a.fold, "the fold button", "the Agents tab")
        // A tab shown again after a switch: the tab bar's room once, not twice (#8596).
        for (tab, step) in [(persona, "the Persona tab again"), (agents, "the Agents tab again")] {
            tab.tap()
            Thread.sleep(forTimeInterval: 1.0)
            let again = head(step)
            assertSame(p.title, again.title, "the head's name (beside the face)", step)
            assertSame(p.fold, again.fold, "the fold button", step)
        }
        // An Agent's chat beside it keeps its header below the tab bar, where the bar runs across the top (the system's
        // side bars on the open Duo on its side hold the chat's actions instead).
        guard agents.frame.minY < app.windows.firstMatch.frame.height / 4 else { return }
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        for _ in 0..<10 where !(row.exists && row.isHittable) { column.swipeUp(velocity: .slow) }
        XCTAssertTrue(row.exists && row.isHittable, "no Agent's row:\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 0.6) // the list's glide after the swipe
        tapClear(row)
        let more = element("chat.more")
        XCTAssertTrue(more.waitForExistence(timeout: 10), "the Agent's chat did not open:\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1.0)
        XCTAssertGreaterThanOrEqual(more.frame.minY, agents.frame.maxY, "the chat's header is under the tab bar")
        shot("tabs-agents-chat")
    }

    /// The page's leading button follows how the page was reached, not what it is (#8603): an Agent's chat picked in the
    /// sidebar is a root, with the sidebar's button (the main conversation's) and no ‹, and the button brings the sidebar
    /// back without moving the page; the same kind of page opened from inside another (a waiting Agent on the main
    /// conversation, or Inbox's) is pushed, with ‹, which goes back to the page under it. With the sidebar beside the page
    /// it is folded first; on the open Duo upright a pick has slid it away; a phone's drawer root has the persona's face.
    @MainActor func testTheLeadingButtonFollowsHowThePageWasReached() {
        launch()
        _ = settled("conversation", "launch")
        let wide = app.windows.firstMatch.frame.width >= 600
        let back = app.buttons.matching(NSPredicate(format: "identifier == 'chevron.left' OR label IN %@", ["返回", "Back"]))
        func backShown() -> Bool { back.allElementsBoundByIndex.contains { $0.exists && $0.isHittable } }
        let sidebarButton = element("chat-sidebar-button"), drawerButton = element("sidebar-drawer-button")
        let systemToggle = app.buttons.matching(NSPredicate(format: "identifier == 'ToggleSidebar' OR label IN %@",
                                                            ["Show Sidebar", "显示边栏", "显示侧边栏"])).firstMatch
        /// Folds a column beside the page (the iPad, the Duo on its side); laid over it or in a drawer it is away already.
        func foldColumn(_ step: String) {
            let fold = element("sidebar-fold-column")
            if fold.exists && fold.isHittable { fold.tap() }
            let main = element("sidebar-main")
            let deadline = Date.now.addingTimeInterval(5)
            while Date.now < deadline, main.exists && main.isHittable { Thread.sleep(forTimeInterval: 0.2) }
            XCTAssertFalse(main.exists && main.isHittable, "\(step): the sidebar is still out:\n\(app.debugDescription)")
            Thread.sleep(forTimeInterval: 0.8) // the column's slide
        }

        // (a) A waiting Agent opened from inside the main conversation (or Inbox, where the conversation lists none): pushed.
        pick("sidebar-main", "the conversation")
        var under = "conversation"
        var before = settled("conversation", "the conversation")
        var card = app.buttons.matching(identifier: "shell-needs-agent").firstMatch
        if !card.waitForExistence(timeout: 3) {
            pick("sidebar-inbox", "Inbox")
            under = "inbox"
            before = settled("inbox", "Inbox")
            card = app.buttons.matching(identifier: "inbox.agentRow").firstMatch
        }
        if wide, !(card.exists && card.isHittable) { foldColumn("the page under the pushed chat") }
        for _ in 0..<6 where !(card.exists && card.isHittable) {
            app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.7))
                .press(forDuration: 0.05, thenDragTo: app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.4)))
        }
        XCTAssertTrue(card.exists && card.isHittable, "no waiting Agent on \(under):\n\(app.debugDescription)")
        shot("8603-under")
        // The same Agent is then picked in the sidebar: its chat's title leads the card's label.
        let title = card.label.components(separatedBy: CharacterSet(charactersIn: "、,，")).first?
            .trimmingCharacters(in: .whitespaces) ?? ""
        XCTAssertFalse(title.isEmpty, "the waiting Agent's card names no chat: \(card.label)")
        card.tap()
        let pushed = settled(under, "a waiting Agent opened from \(under)")
        XCTAssertEqual(pushed.path, 1, "the Agent's chat was not pushed on \(under): \(pushed)")
        XCTAssertEqual(pushed.transitions, before.transitions + 1, "the Agent's chat did not push: \(before) → \(pushed)")
        foldColumn("the pushed chat")
        XCTAssertTrue(back.firstMatch.waitForExistence(timeout: 5) && backShown(),
                      "no ‹ on an Agent's chat pushed from \(under):\n\(app.debugDescription)")
        XCTAssertFalse(sidebarButton.exists, "the sidebar's button on a pushed chat")
        XCTAssertFalse(drawerButton.exists && drawerButton.isHittable, "the drawer's face on a pushed chat")
        shot("8603-pushed")
        back.allElementsBoundByIndex.first { $0.exists && $0.isHittable }?.tap()
        let popped = settled(under, "‹ from the pushed chat")
        XCTAssertEqual(popped.path, 0, "‹ did not go back to \(under): \(popped)")
        XCTAssertFalse(backShown(), "back at \(under), a ‹ still shows:\n\(app.debugDescription)")
        shot("8603-popped")
        // Back at the root, one button brings the sidebar back: not none (UIKit's toggle went after the pop), not two.
        let openers = app.buttons.matching(NSPredicate(format: "identifier IN %@ OR label IN %@",
                                                       ["ToggleSidebar", "sidebar-show-button", "assistant-sidebar-button", "sidebar-drawer-button", "shell-place-sidebar"],
                                                       ["Show Sidebar", "显示边栏", "显示侧边栏"]))
            .allElementsBoundByIndex.filter { $0.exists && $0.isHittable }
        if !(element("sidebar-main").exists && element("sidebar-main").isHittable) {
            XCTAssertEqual(Set(openers.map(\.frame.minX)).count, 1, "back at \(under): \(openers.count) buttons bring the sidebar back:\n\(app.debugDescription)")
        }

        // (b) The same Agent picked in the sidebar: a root.
        let root = pickAgent("the same Agent picked in the sidebar", titled: title)
        XCTAssertEqual(root.path, 0, "the Agent's chat is not the root")
        foldColumn("the Agent's root")
        XCTAssertFalse(backShown(), "a ‹ on an Agent's chat picked in the sidebar:\n\(app.debugDescription)")
        let opener = [sidebarButton, drawerButton, systemToggle].first { $0.exists && $0.isHittable }
        XCTAssertNotNil(opener, "no button that opens the sidebar on the Agent's root:\n\(app.debugDescription)")
        if wide { XCTAssertTrue(sidebarButton.isHittable || systemToggle.isHittable, "the root's button is not the sidebar's") }
        shot("8603-root")
        opener?.tap()
        XCTAssertTrue(element("sidebar-main").waitForExistence(timeout: 5) && element("sidebar-main").isHittable,
                      "the root's button did not bring the sidebar out:\n\(app.debugDescription)")
        let afterOpen = settled("agent", "the sidebar brought out over the Agent's root")
        XCTAssertEqual(afterOpen.path, 0)
        XCTAssertEqual(afterOpen.transitions, root.transitions, "bringing the sidebar out moved the page: \(root) → \(afterOpen)")
    }

    /// Folding the sidebar beside an Agent's chat leaves the chat's top where it was: on the open Duo on its side the
    /// system's Show Sidebar came in a row of its own above the chat's header and pushed the header and the chat 58 pt
    /// down. Folded, the title stays put (within 1 pt), one button brings the sidebar back, on the title's line, and an
    /// open title still takes a tap. In the Sidebar layout and in the Separate layout's Agents tab.
    @MainActor func testFoldingTheSidebarKeepsTheChatsTop() throws {
        for separate in [false, true] {
            app = XCUIApplication()
            app.launchArguments = ["-demo.on", "YES", "-newAssistant", "YES", "-dev.openChat", "", "-openSplitFresh", "YES", "-probes", "YES",
                                   "-app.navigation", separate ? "separate" : "sidebar", "-tab", separate ? "dev" : "assistant"]
            app.launchPinned(navigation: false)
            XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 20))
            guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("a column beside the page, at regular width") }
            let layout = separate ? "the Agents tab" : "the Sidebar layout"
            if separate {
                let agentsTab = app.buttons.matching(NSPredicate(format: "identifier == 'shell-agents' OR label == 'Agents'")).firstMatch
                if agentsTab.waitForExistence(timeout: 10), !agentsTab.isSelected { agentsTab.tap() }
                probeName = "sidebar-page-agents"
            } else {
                probeName = "sidebar-page"
            }
            _ = settled("conversation", "\(layout) at launch")
            _ = pickAgent("an Agent in \(layout)")
            let fold = element("sidebar-fold-column")
            // Upright on the open Duo the column lies over the page and a pick slid it away: nothing beside the page moves.
            guard fold.exists && fold.isHittable else { throw XCTSkip("the column lies over the page here") }
            // The header's title: open (always, under side bars), or the folded bar's (the iPad's header folds on a long chat).
            let open = app.buttons["chat.title"].firstMatch, compact = app.buttons["chat.compactTitle"].firstMatch
            XCTAssertTrue(open.waitForExistence(timeout: 10) || compact.exists, "\(layout): no chat title:\n\(app.debugDescription)")
            Thread.sleep(forTimeInterval: 1.0) // the header's own settling after the pick
            let title = open.exists && open.isHittable ? open : compact
            let out = title.frame
            shot("fold-top-\(separate ? "tabs" : "sidebar")-0-out")

            fold.tap()
            let main = element(separate ? "sidebar-fold-column" : "sidebar-main")
            let deadline = Date.now.addingTimeInterval(5)
            while Date.now < deadline, main.exists && main.isHittable { Thread.sleep(forTimeInterval: 0.2) }
            Thread.sleep(forTimeInterval: 1.0) // the column's slide
            let folded = title.frame
            shot("fold-top-\(separate ? "tabs" : "sidebar")-1-folded")
            XCTAssertLessThanOrEqual(abs(folded.minY - out.minY), 1, "\(layout): folding moved the chat's title from \(out) to \(folded)")
            XCTAssertLessThanOrEqual(abs(folded.height - out.height), 1, "\(layout): folding changed the title's height: \(out) → \(folded)")

            // One button brings the sidebar back (the system's, or the chat's own in its header), beside the title.
            let openers = app.buttons.matching(NSPredicate(format: "identifier IN %@ OR label IN %@",
                                                           ["ToggleSidebar", "chat-sidebar-button", "sidebar-show-button"],
                                                           ["Show Sidebar", "显示边栏", "显示侧边栏"]))
                .allElementsBoundByIndex.filter { $0.exists && $0.isHittable }
            guard let first = openers.first else {
                return XCTFail("\(layout): no button brings the sidebar back:\n\(app.debugDescription)")
            }
            // A bar button is two elements, the button and its glass: one place.
            let apart = openers.filter { hypot($0.frame.midX - first.frame.midX, $0.frame.midY - first.frame.midY) > 44 }
            XCTAssertTrue(apart.isEmpty, "\(layout): \(openers.count) buttons bring the sidebar back, at \(openers.map(\.frame)):\n\(app.debugDescription)")
            XCTAssertTrue(folded.minY <= first.frame.midY && first.frame.midY <= folded.maxY,
                          "\(layout): the sidebar's button \(first.frame) is not on the title's line \(folded)")

            // The open title takes a tap where the system's bar now has a row: Session Info comes up over the chat.
            guard title == open else { app.terminate(); continue }
            title.tap()
            let covered = NSPredicate { _, _ in !title.exists || !title.isHittable }
            XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: covered, object: nil)], timeout: 5), .completed,
                           "\(layout): a tap on the folded chat's title opened nothing:\n\(app.debugDescription)")
            app.terminate()
        }
    }

    /// The Separate layout's Agents tab (#8603): an Agent picked in its list is the page's root, put in place with no push,
    /// as in the Sidebar layout; another one picked replaces it, again with no push; nothing picked, "Choose an Agent". With
    /// the column folded the chat has the sidebar's button and no ‹, which brings the column back.
    @MainActor func testTheAgentsTabSwitchesItsRootWithoutAPush() throws {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-newAssistant", "YES", "-tab", "dev", "-dev.openChat", "",
                               "-app.navigation", "separate", "-probes", "YES"]
        app.launchPinned(navigation: false)
        XCTAssertTrue(app.windows.firstMatch.waitForExistence(timeout: 20))
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("the Separate layout's tabs at regular width") }
        // The iPad's tab bar, or the open Duo's own tab bar in its place (#4231).
        let agentsTab = app.buttons.matching(NSPredicate(format: "identifier == 'shell-agents' OR label == 'Agents'")).firstMatch
        if agentsTab.waitForExistence(timeout: 10), !agentsTab.isSelected { agentsTab.tap() }
        probeName = "sidebar-page-agents"
        let empty = settled("conversation", "the Agents tab with nothing picked")
        XCTAssertEqual(empty.path, 0)
        shot("agents-tab-0-empty")

        let first = pickAgent("an Agent in the Agents tab")
        XCTAssertEqual(first.path, 0, "the Agent's chat is not the root: \(first)")
        assertSwitched(empty, first, "an Agent from Choose an Agent")
        let firstTitle = chatTitle()
        // The chat's own title, without what the folded bar's label adds ("…, show title and tags").
        let firstName = firstTitle.components(separatedBy: CharacterSet(charactersIn: ",，")).first ?? firstTitle
        shot("agents-tab-1-agent")

        let second = pickAgent("another Agent in the Agents tab", excluding: firstName,
                               until: { !self.chatTitle().isEmpty && self.chatTitle() != firstTitle })
        XCTAssertEqual(second.path, 0, "the second Agent's chat is not the root: \(second)")
        XCTAssertNotEqual(chatTitle(), firstTitle, "the second Agent did not replace the first")
        assertSwitched(first, second, "another Agent")
        shot("agents-tab-2-another")

        // The column folded (beside the page) or slid away (over it): the sidebar's button, no ‹; it brings the column back.
        let fold = element("sidebar-fold-column")
        if fold.exists && fold.isHittable { fold.tap() }
        Thread.sleep(forTimeInterval: 0.8)
        let back = app.buttons.matching(NSPredicate(format: "identifier == 'chevron.left' OR label IN %@", ["返回", "Back"]))
        XCTAssertFalse(back.allElementsBoundByIndex.contains { $0.exists && $0.isHittable }, "a ‹ on the Agents tab's root:\n\(app.debugDescription)")
        let opener = app.buttons.matching(NSPredicate(format: "identifier == 'chat-sidebar-button' OR identifier == 'ToggleSidebar' OR label IN %@",
                                                      ["Show Sidebar", "显示边栏", "显示侧边栏"])).allElementsBoundByIndex
            .first { $0.exists && $0.isHittable }
        XCTAssertNotNil(opener, "no button that brings the column back:\n\(app.debugDescription)")
        shot("agents-tab-3-folded")
        opener?.tap()
        XCTAssertTrue(fold.waitForExistence(timeout: 5) && fold.isHittable, "the column did not come back:\n\(app.debugDescription)")
        let shown = settled("agent", "the column brought back")
        XCTAssertEqual(shown.transitions, second.transitions, "bringing the column back moved the page: \(second) → \(shown)")
    }

    /// Exactly one row of the sidebar is selected, `id` (whose label holds `text`), with the column brought out first.
    @MainActor private func assertOneSelected(_ id: String, containing text: String? = nil, _ step: String) {
        bringOut()
        app.assertOneSelectedSidebarRow(id, containing: text, step)
    }

    /// The column brought out after a pick or a launch: a pick slides a column laid over the page (or a drawer) away, and
    /// the conversation's sidebar button can read as not hittable under the page's top for a moment, so it is tapped at
    /// its frame once that has passed.
    @MainActor private func bringOut() {
        let main = element("sidebar-main")
        if !(main.exists && main.isHittable) || app.windows.firstMatch.frame.width < 900 { Thread.sleep(forTimeInterval: 1.0) }
        let button = element("assistant-sidebar-button")
        if !(main.exists && main.isHittable), button.exists, let frame = (try? button.snapshot())?.frame, !frame.isEmpty {
            app.windows.firstMatch.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX, dy: frame.midY)).tap()
            _ = main.waitForExistence(timeout: 3)
        }
        reveal(main)
    }

    /// One row of the whole sidebar is selected at a time, and it is the page's root (#11032): Main Chat picked; an Agent
    /// picked in the Agents list (its row, no longer Main Chat's), also after the column is folded and brought back; a
    /// place (Recordings); a Persona chat; Main Chat again. The column beside the page, laid over it (the open Duo
    /// upright) and a phone's drawer; By State, then an Agent By Repository. An Agent pushed from inside the main
    /// conversation: `SecondaryPageBackTests.testAnAgentPushedFromTheMainConversationLeavesItSelected`.
    @MainActor func testOneRowIsSelectedAndItFollowsTheRoot() {
        // By State, as the owner's list: before #11032 only By Repository shaded the open Agent (checked below).
        launch(["-dev.grouping", "state"])
        _ = settled("conversation", "launch")
        // The app can come up on a new chat's page, which no row stands for: Main Chat picked first.
        pick("sidebar-main", "Main Chat")
        _ = settled("conversation", "Main Chat")
        assertOneSelected("sidebar-main", "Main Chat picked")
        shot("11032-0-main")

        _ = pickAgent("an Agent")
        let title = chatTitle()
        let name = title.components(separatedBy: CharacterSet(charactersIn: ",，")).first ?? title
        XCTAssertFalse(name.isEmpty, "the Agent's chat has no title")
        assertOneSelected("agents.row", containing: name, "an Agent picked in the sidebar")
        shot("11032-1-agent")

        // Folded beside the page and brought back: the same row.
        let fold = element("sidebar-fold-column")
        if fold.exists && fold.isHittable {
            fold.tap()
            let main = element("sidebar-main")
            let deadline = Date.now.addingTimeInterval(5)
            while Date.now < deadline, main.exists && main.isHittable { Thread.sleep(forTimeInterval: 0.2) }
            Thread.sleep(forTimeInterval: 0.8) // the column's slide
            let openers = app.buttons.matching(NSPredicate(format: "identifier IN %@ OR label IN %@",
                                                           ["chat-sidebar-button", "ToggleSidebar", "sidebar-show-button", "assistant-sidebar-button"],
                                                           ["Show Sidebar", "显示边栏", "显示侧边栏"]))
            if openers.allElementsBoundByIndex.contains(where: { $0.exists && $0.isHittable }) {
                assertOneSelected("agents.row", containing: name, "the column folded and brought back over the Agent")
            } else {
                // The open Duo on its side: no button brings a folded column back over an Agent's chat, on main too
                // (testFoldingTheSidebarKeepsTheChatsTop fails there the same way); the rest from a fresh launch.
                XCTContext.runActivity(named: "no button brings the folded column back here; relaunched") { _ in }
                app.terminate()
                launch(["-dev.grouping", "state"])
                _ = settled("conversation", "relaunch after the fold")
                bringOut()
            }
        }

        pick("sidebar-meeting-recording", "Recordings")
        _ = settled("recordings", "Recordings from the Agent")
        assertOneSelected("sidebar-meeting-recording", "a place picked")
        shot("11032-2-place")

        // A Persona chat: the conversation is the root again, its row (not Main Chat's) the selected one.
        pick("sidebar-conversation", "a Persona chat")
        _ = settled("conversation", "a Persona chat from Recordings")
        assertOneSelected("sidebar-conversation", "a Persona chat picked")
        shot("11032-3-persona-chat")

        pick("sidebar-main", "the main conversation")
        _ = settled("conversation", "the main conversation from a Persona chat")
        assertOneSelected("sidebar-main", "the main conversation picked again")
        shot("11032-4-main-again")
        // An Agent pushed from inside the main conversation keeps it selected: SecondaryPageBackTests (a task's card).

        // By Repository: the same one row, in the same shade as the others (no longer the tree's own wash).
        app.terminate()
        launch(["-dev.grouping", "repo"])
        _ = settled("conversation", "launch By Repository")
        bringOut()
        _ = pickAgent("an Agent By Repository")
        let repoTitle = chatTitle()
        let repoName = repoTitle.components(separatedBy: CharacterSet(charactersIn: ",，")).first ?? repoTitle
        assertOneSelected("agents.row", containing: repoName, "an Agent picked By Repository")
        shot("11032-5-agent-by-repository")
    }
}
