import XCTest

/// The Inbox's title buttons are a finger tall (#6576): each section's title (it folds the section) and its Mark All Seen /
/// Clear All, each group's title and, while selecting, its tick, Done's title and its Jump To. Most were as tall as their
/// text, 16 to 18 pt; a title alone in its header filled the header, 40 pt (#5414). Each is measured on every screen from
/// the top of the list to the end of Done, and the ones that fold or pick are tapped `edge` points from their middle. Jump
/// To shares Done's row with Done's own button: a tap on it opens its menu and leaves Done open. A group's tick is a finger
/// wide as well (#6658), and a tap beside its glyph picks. Against
/// scripts/mock/mock_hub.py (its inbox has waiting agents, items in every tier and Done's groups):
///   PORT=<port> MOCK_MANAGERS=0 python3 scripts/mock/mock_hub.py
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port>
/// TEST_RUNNER_PERSONA_SHOTS=<dir> keeps the screens it measured; TEST_RUNNER_PERSONA_UI_SCHEME=dark runs it dark, and
/// TEST_RUNNER_PERSONA_TEXT_SIZE=<UIContentSizeCategory name> at that text size.
final class InboxTitleTargetUITests: XCTestCase {
    private var app: XCUIApplication!
    private var pair = ""

    /// The title buttons, by accessibility identifier.
    static let titles = ["inbox.sectionTitle", "inbox.clear", "inbox.groupTitle", "inbox.doneTitle", "inbox.jumpTo"]
    static let tick = "inbox.pickGroup"
    /// A finger's height, less a rounding point.
    static let floor: CGFloat = 43.5
    /// Where the edge taps land, from a button's middle: 2 pt inside a 44 pt button.
    static let edge: CGFloat = 20

    override func setUpWithError() throws {
        continueAfterFailure = true // every button measured, whichever fails
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MANAGERS=0 (PERSONA_MOCK_HUB)")
        }
        pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.name = "failure"
            a.lifetime = .keepAlways
            add(a)
        }
        super.tearDown()
    }

    /// Every title button from the top of the list to the end of Done (open, its groups listed), and every group's tick
    /// while selecting, is at least a finger tall.
    @MainActor func testEveryTitleButtonIsAFingerTall() throws {
        launch(["-showInboxDone", "YES"])
        var found: [String: CGRect] = [:]
        shot("done")
        collect(Self.titles, into: &found)
        for _ in 0..<6 { app.swipeDown(velocity: .fast); collect(Self.titles, into: &found) }
        shot("top")
        var top: [String: CGRect] = [:]
        collect(Self.titles, into: &top)
        for (key, f) in top.sorted(by: { $0.value.minY < $1.value.minY }) { print("INBOX-TITLE-TOP \(key): y \(f.minY) h \(f.height)") }
        for _ in 0..<12 { app.swipeUp(); collect(Self.titles, into: &found) }
        assertFingerTall(found, Self.titles)

        launch(["-showInboxPicked", "1"])
        var ticks: [String: CGRect] = [:]
        shot("selecting")
        for _ in 0..<4 { collect([Self.tick], into: &ticks); app.swipeUp() }
        assertFingerTall(ticks, [Self.tick])
        // and a finger wide, its glyph where it was (#6658)
        for (key, frame) in ticks.sorted(by: { $0.key < $1.key }) {
            print("INBOX-TICK \(key): \(frame.width) × \(frame.height) pt at x \(frame.minX)")
            XCTAssertGreaterThanOrEqual(frame.width, Self.floor, "\(key) is \(frame.width) pt wide")
        }
    }

    /// Near their edges, a section's title folds and opens the section, Done's title opens and folds Done, Jump To opens its
    /// menu, a group's title folds the group and a group's tick picks it. A plain tap on Jump To opens its menu and leaves
    /// Done as it was: the two share a row. Each fold it makes it undoes (a group's fold is kept across launches).
    @MainActor func testTitleButtonsTakeATapNearTheirEdge() throws {
        launch([])
        for _ in 0..<8 { app.swipeDown(velocity: .fast) }
        let agent = app.buttons["inbox.agentRow"].firstMatch
        XCTAssertTrue(agent.waitForExistence(timeout: 5), "no agents waiting (the mock's, in an open group):\n\(app.debugDescription)")

        // Needs You's and FYI's titles fold them: the list gets short enough for Done's groups to show under its title.
        let sections = app.buttons.matching(identifier: "inbox.sectionTitle")
        tap(sections.element(boundBy: 0), dy: -Self.edge)
        XCTAssertTrue(agent.waitForNonExistence(timeout: 3), "a tap near Needs You's top edge did not fold it")
        sleep(1)
        tap(sections.element(boundBy: 1), dy: Self.edge)
        let done = app.buttons["inbox.doneTitle"].firstMatch
        XCTAssertTrue(wait { self.onScreen(done) && done.frame.midY < self.app.windows.firstMatch.frame.height * 0.7 },
                      "a tap near FYI's bottom edge did not fold it (Done's title is not up the screen):\n\(app.debugDescription)")
        XCTAssertEqual(doneGroups(), 0, "Done starts open")

        // Done's title opens Done, then folds it, then opens it again.
        tap(done, dy: Self.edge)
        XCTAssertTrue(wait { self.doneGroups() > 0 }, "a tap near Done's bottom edge did not open it")
        tap(done, dy: -Self.edge)
        XCTAssertTrue(wait { self.doneGroups() == 0 }, "a tap near Done's top edge did not fold it")
        tap(done, dy: 0)
        XCTAssertTrue(wait { self.doneGroups() > 0 }, "a tap on Done's title did not open it")

        // Jump To: its menu, and Done as it was.
        let jump = app.buttons["inbox.jumpTo"].firstMatch
        let collapse = app.buttons.matching(NSPredicate(format: "label IN %@", ["Collapse All", "全部折叠"])).firstMatch
        XCTAssertTrue(onScreen(jump), "Done lists no Jump To:\n\(app.debugDescription)")
        for (dy, place) in [(CGFloat(0), "on Jump To"), (-Self.edge, "near Jump To's top edge")] {
            tap(jump, dy: dy)
            XCTAssertTrue(collapse.waitForExistence(timeout: 3), "a tap \(place) did not open its menu")
            if collapse.exists {
                // beside the menu, which opens from Jump To on the right: closes it and does nothing else
                app.windows.firstMatch.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 8, dy: jump.frame.midY)).tap()
                XCTAssertTrue(collapse.waitForNonExistence(timeout: 3), "Jump To's menu stayed open")
            }
            sleep(1)
            // open: its groups listed (how many of them the lazy list has made varies)
            XCTAssertGreaterThan(doneGroups(), 0, "a tap \(place) folded Done")
        }

        // FYI and Needs You open again; the waiting agents' group title folds their rows away, then opens them again.
        tap(sections.element(boundBy: 1), dy: 0)
        sleep(1)
        tap(sections.element(boundBy: 0), dy: Self.edge)
        XCTAssertTrue(agent.waitForExistence(timeout: 3), "a tap near Needs You's bottom edge did not open it")
        sleep(1)
        let agents = app.buttons.matching(identifier: "inbox.groupTitle")
            .matching(NSPredicate(format: "label BEGINSWITH %@ OR label BEGINSWITH %@", "Agents Waiting on You", "Agent 在等你")).firstMatch
        XCTAssertTrue(agents.exists, "no Agents Waiting on You group:\n\(app.debugDescription)")
        tap(agents, dy: -Self.edge)
        XCTAssertTrue(agent.waitForNonExistence(timeout: 3), "a tap near a group title's top edge did not fold it")
        tap(agents, dy: 0)
        XCTAssertTrue(agent.waitForExistence(timeout: 3), "a tap on a group's title did not open it")

        // Selecting: a group's tick picks the whole group.
        launch(["-showInboxPicked", "1"])
        let tick = app.buttons[Self.tick].firstMatch
        XCTAssertTrue(tick.waitForExistence(timeout: 10), "selecting shows no group tick:\n\(app.debugDescription)")
        // up the screen, clear of the bar that marks the picked ones done
        let window = app.windows.firstMatch
        for _ in 0..<4 where tick.frame.midY > window.frame.height * 0.6 {
            window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.6))
                .press(forDuration: 0.1, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.4)),
                       withVelocity: .slow, thenHoldForDuration: 0.3)
            sleep(1)
        }
        XCTAssertTrue(onScreen(tick), "the group tick is not on screen:\n\(app.debugDescription)")
        let before = picked()
        tap(tick, dy: Self.edge)
        XCTAssertTrue(wait { self.picked() != before }, "a tap near a group tick's bottom edge picked nothing (\(before) picked)")
        // beside its glyph, in the header's inset (#6658): it unpicks the group again
        let after = picked()
        tap(tick, dx: -18, dy: 0)
        XCTAssertTrue(wait { self.picked() != after }, "a tap 18 pt left of a group tick's middle did nothing (\(after) picked)")
    }

    // MARK: -

    @MainActor private func launch(_ extra: [String]) {
        app?.terminate()
        app = XCUIApplication()
        let env = ProcessInfo.processInfo.environment
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", env["PERSONA_UI_SCHEME"] ?? "light", "-tab", "mail",
                               "-inbox.kind", "", "-inbox.source", "", "-inbox.grouping", "time"] + extra
            + (env["PERSONA_TEXT_SIZE"].map { ["-UIPreferredContentSizeCategoryName", $0] } ?? [])
        // the first launch after pairing may open on the Assistant instead: once more, paired
        for attempt in 0..<2 {
            if attempt > 0 { app.terminate() }
            app.launchPinned()
            acceptIconAlert(timeout: 2)
            if app.buttons["inbox.sectionTitle"].firstMatch.waitForExistence(timeout: 15) { break }
        }
        XCTAssertTrue(app.buttons["inbox.sectionTitle"].firstMatch.exists, "the inbox did not load:\n\(app.debugDescription)")
        sleep(2) // Done's day counts and its Jump To; -showInboxDone's scroll
    }

    /// The buttons with `ids` on screen, by identifier and label.
    @MainActor private func collect(_ ids: [String], into found: inout [String: CGRect]) {
        guard let root = try? app.windows.firstMatch.snapshot() else { return XCTFail("no snapshot") }
        let screen = app.windows.firstMatch.frame
        func walk(_ e: XCUIElementSnapshot) {
            if ids.contains(e.identifier), e.frame.height > 1, e.frame.minY >= screen.minY, e.frame.maxY <= screen.maxY {
                found["\(e.identifier)|\(e.label)"] = e.frame
            }
            e.children.forEach(walk)
        }
        walk(root)
    }

    private func assertFingerTall(_ found: [String: CGRect], _ ids: [String]) {
        for id in ids {
            XCTAssertTrue(found.keys.contains { $0.hasPrefix(id + "|") }, "no \(id) on any screen")
        }
        for (key, frame) in found.sorted(by: { $0.key < $1.key }) {
            print("INBOX-TITLE \(key): \(frame.height) pt")
            XCTAssertGreaterThanOrEqual(frame.height, Self.floor, "\(key) is \(frame.height) pt tall")
        }
    }

    /// Wholly on screen, below the navigation bar (a lazy list also makes rows and titles it does not show).
    @MainActor private func onScreen(_ e: XCUIElement) -> Bool {
        guard e.exists else { return false }
        let search = app.searchFields.firstMatch
        let f = e.frame, top = app.navigationBars.firstMatch.frame.maxY
        let bottom = (search.exists ? search.frame.minY : app.windows.firstMatch.frame.maxY) - Self.edge
        return f.height > 1 && f.minY >= top && f.maxY <= bottom
    }

    /// Taps `e` `dx`, `dy` points from its middle.
    @MainActor private func tap(_ e: XCUIElement, dx: CGFloat = 0, dy: CGFloat) {
        e.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5)).withOffset(CGVector(dx: dx, dy: dy)).tap()
    }

    /// The group titles below Done's: Done's groups, listed while it is open.
    @MainActor private func doneGroups() -> Int {
        guard let root = try? app.windows.firstMatch.snapshot() else { return -1 }
        var done: CGRect?, groups: [CGRect] = []
        func walk(_ e: XCUIElementSnapshot) {
            if e.identifier == "inbox.doneTitle" { done = e.frame }
            if e.identifier == "inbox.groupTitle", e.frame.height > 1 { groups.append(e.frame) }
            e.children.forEach(walk)
        }
        walk(root)
        guard let done else { return -1 }
        return groups.filter { $0.minY >= done.maxY - 1 }.count
    }

    /// How many rows are picked.
    @MainActor private func picked() -> Int {
        app.descendants(matching: .any).matching(identifier: "inbox.pickRow").allElementsBoundByIndex.filter(\.isSelected).count
    }

    @MainActor private func wait(_ timeout: TimeInterval = 3, _ condition: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if condition() { return true }
            usleep(200_000)
        }
        return condition()
    }

    @MainActor private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        let env = ProcessInfo.processInfo.environment
        let size = env["PERSONA_TEXT_SIZE"] == nil ? "" : "-ax"
        let file = URL(fileURLWithPath: dir).appendingPathComponent("inbox-titles-\(name)-\(env["PERSONA_UI_SCHEME"] ?? "light")\(size).png")
        try? app.windows.firstMatch.screenshot().pngRepresentation.write(to: file)
    }
}
