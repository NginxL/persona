import XCTest

/// Jump To in the Inbox's Done goes where it was last asked to and stays there (#6661). A group none of whose items has
/// come yet loads its first page, and Jump To scrolled to it once one was there, up to 5 s later, whatever happened
/// meanwhile: a second Jump To to another group was undone when the first group's page came, and so was the user's own
/// drag. The mock holds the group's page until the test lets it go, and the app waits for it longer than 5 s
/// (`-inboxJumpWait 60`): XCUITest's taps and drags each take seconds on a busy machine. Against scripts/mock/mock_hub.py
/// with a busy Done (a page of history holds a few days, so the oldest group's items come only with its own page):
///   PORT=<port> MOCK_MANAGERS=0 MOCK_INBOX_HISTORY=busy python3 scripts/mock/mock_hub.py
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:<port>
final class InboxJumpToRaceUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private var pair = ""
    /// From letting the page go to its items listed, and a scroll Jump To would make then.
    private static let settle: UInt32 = 6

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MANAGERS=0 MOCK_INBOX_HISTORY=busy (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    override func tearDown() {
        if let app, (testRun?.totalFailureCount ?? 0) > 0 {
            let a = XCTAttachment(screenshot: app.screenshot())
            a.name = "failure"
            a.lifetime = .keepAlways
            add(a)
        }
        mock("inbox_done_release") // never leave the mock holding pages
        super.tearDown()
    }

    /// Jump To the oldest group (its page held), then to the newest (already listed): the list stays at the newest when
    /// the oldest's page comes.
    @MainActor func testALaterJumpToWins() throws {
        let (newest, oldest) = try openDone()
        mock("inbox_done_hold")
        jump(to: oldest)
        jump(to: newest)
        XCTAssertTrue(wait(3) { self.topGroup()?.hasPrefix(newest) == true }, "the second Jump To did not go to \(newest) (top: \(topGroup() ?? "none"))")
        mock("inbox_done_release")
        sleep(Self.settle)
        XCTAssertTrue(topGroup()?.hasPrefix(newest) == true,
                      "the first Jump To's page came and took the list from \(newest) to \(topGroup() ?? "none")")
        foldDoneGroups() // the groups' folds are kept across launches
    }

    /// Jump To the oldest group (its page held), then drag the list by hand: it stays where the hand left it when the
    /// page comes.
    @MainActor func testAHandScrollWins() throws {
        let (_, oldest) = try openDone()
        mock("inbox_done_hold")
        jump(to: oldest)
        drag(down: true)
        sleep(1)
        let left = topGroup()
        XCTAssertFalse(left?.hasPrefix(oldest) == true, "the drag ended at \(oldest) itself")
        mock("inbox_done_release")
        sleep(Self.settle)
        XCTAssertEqual(topGroup(), left, "the Jump To's page came and pulled the list from where the hand left it")
        foldDoneGroups() // the groups' folds are kept across launches
    }

    /// Jump To the oldest group (its page held) and nothing else: once the page comes, the list goes there, as before
    /// (nothing between, the menu closing included, cancels it).
    @MainActor func testALoneJumpToStillGoesThere() throws {
        let (_, oldest) = try openDone()
        mock("inbox_done_hold")
        jump(to: oldest)
        sleep(2)
        XCTAssertFalse(topGroup()?.hasPrefix(oldest) == true, "Jump To went to \(oldest) before its page came")
        mock("inbox_done_release")
        XCTAssertTrue(wait(TimeInterval(Self.settle)) { self.topGroup()?.hasPrefix(oldest) == true },
                      "Jump To did not go to \(oldest) once its page came (top: \(topGroup() ?? "none"))")
        foldDoneGroups() // the groups' folds are kept across launches
    }

    /// Jump To the oldest group (its page held), then open an item of the newest group over the list: the page comes
    /// while the list is out of sight, and back on it the list is where it was left (leaving the list stops the wait).
    @MainActor func testLeavingTheListStopsIt() throws {
        let (newest, oldest) = try openDone()
        mock("inbox_done_hold")
        jump(to: oldest)
        sleep(1)
        XCTAssertTrue(topGroup()?.hasPrefix(newest) == true, "Jump To moved the list before its page came (top: \(topGroup() ?? "none"))")
        openItem(under: newest)
        mock("inbox_done_release")
        sleep(Self.settle)
        let back = app.navigationBars.buttons["BackButton"].firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 3), "no Back on the item:\n\(app.debugDescription)")
        back.tap()
        // a group's title, not Jump To: Done's title and its Jump To are off screen if the list moved while unseen
        XCTAssertTrue(app.buttons["inbox.groupTitle"].firstMatch.waitForExistence(timeout: 5), "not back on the list:\n\(app.debugDescription)")
        sleep(1)
        XCTAssertTrue(topGroup()?.hasPrefix(newest) == true,
                      "the Jump To's page came while the list was out of sight and took it to \(topGroup() ?? "none")")
        foldDoneGroups() // the groups' folds are kept across launches
    }

    /// Holds or lets go the mock's Done group pages (`/mock/inbox_done_hold`, `/mock/inbox_done_release`).
    private func mock(_ path: String) {
        _ = try? Data(contentsOf: URL(string: hub + "/mock/" + path)!)
    }

    // MARK: -

    /// Launches, opens Done with every group folded (a run that failed may have left some open), and opens the newest group (its page loaded): the newest and the
    /// oldest groups' titles.
    @MainActor private func openDone() throws -> (newest: String, oldest: String) {
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", "light", "-tab", "mail", "-inbox.kind", "", "-inbox.source", "",
                               "-inbox.grouping", "time", "-inboxJumpWait", "60"]
        for attempt in 0..<2 { // the first launch after pairing may open on the Assistant: once more, paired
            if attempt > 0 { app.terminate() }
            app.launchPinned()
            acceptIconAlert(timeout: 2)
            if app.buttons["inbox.sectionTitle"].firstMatch.waitForExistence(timeout: 15) { break }
        }
        sleep(2) // Done's day counts: its groups and Jump To
        let done = app.buttons["inbox.doneTitle"].firstMatch // at the end, made once the list gets near it
        for _ in 0..<8 where !onScreen(done) { app.swipeUp() }
        XCTAssertTrue(onScreen(done), "no Done title:\n\(app.debugDescription)")
        done.tap()
        foldDoneGroups()
        for _ in 0..<3 { app.swipeUp() } // the end: every group's title on screen under Done's
        sleep(1)
        let titles = doneGroupTitles()
        guard let newest = titles.first, let oldest = titles.last, titles.count > 2 else {
            throw XCTSkip("Done lists fewer than three groups: \(titles)")
        }
        jump(to: newest) // the list's own done items: there already
        XCTAssertTrue(wait(5) { self.topGroup()?.hasPrefix(newest) == true }, "Jump To did not go to \(newest) (top: \(topGroup() ?? "none"))")
        drag(down: true) // Done's title, just above the newest group, back on screen: Jump To without a scroll between
        sleep(1)
        XCTAssertTrue(onScreen(app.buttons["inbox.jumpTo"].firstMatch), "Jump To is not on screen:\n\(app.debugDescription)")
        return (newest, oldest)
    }

    /// Opens the first item under `group`'s title (Done's rows have no identifier: a tap just below the title), and
    /// waits for its page over the list.
    @MainActor private func openItem(under group: String) {
        let title = shownDoneGroups().first { Self.title($0.label) == group }
        XCTAssertNotNil(title, "\(group)'s title is not on screen:\n\(app.debugDescription)")
        let window = app.windows.firstMatch
        window.coordinate(withNormalizedOffset: .zero)
            .withOffset(CGVector(dx: window.frame.midX, dy: (title?.frame.maxY ?? 0) + 30)).tap()
        XCTAssertTrue(app.buttons["inbox.jumpTo"].firstMatch.waitForNonExistence(timeout: 5),
                      "the item did not open over the list:\n\(app.debugDescription)")
    }

    /// Picks `group` in Jump To's menu: its item reads "<title>, <count>" (a plain prefix would also take the shell's
    /// Back, "今天要处理的事", for 今天).
    @MainActor private func jump(to group: String) {
        pick(NSPredicate(format: "label BEGINSWITH %@ AND identifier != %@", group + ", ", "inbox.groupTitle"), "\(group) in Jump To")
    }

    @MainActor private func foldDoneGroups() {
        pick(NSPredicate(format: "label IN %@", ["Collapse All", "全部折叠"]), "Collapse All")
        sleep(1)
    }

    @MainActor private func pick(_ item: NSPredicate, _ name: String) {
        let jump = app.buttons["inbox.jumpTo"].firstMatch
        for _ in 0..<3 where !onScreen(jump) { drag(down: true) } // above, after a jump
        for _ in 0..<8 where !onScreen(jump) { app.swipeUp() }
        jump.tap()
        let button = app.buttons.matching(item).firstMatch
        XCTAssertTrue(button.waitForExistence(timeout: 3), "no \(name):\n\(app.debugDescription)")
        button.tap()
    }

    /// Done's groups' titles (without their counts), newest first, as the list shows them under Done's title.
    @MainActor private func doneGroupTitles() -> [String] {
        guard let root = try? app.windows.firstMatch.snapshot() else { return [] }
        var done: CGRect?, groups: [(CGFloat, String)] = []
        func walk(_ e: XCUIElementSnapshot) {
            if e.identifier == "inbox.doneTitle" { done = e.frame }
            if e.identifier == "inbox.groupTitle" { groups.append((e.frame.minY, e.label)) }
            e.children.forEach(walk)
        }
        walk(root)
        guard let done else { return [] }
        return groups.filter { $0.0 >= done.maxY - 1 }.sorted { $0.0 < $1.0 }.map { Self.title($0.1) }
    }

    /// The Done group whose title is topmost on screen, under the navigation bar (the one stuck there, or the first below it).
    @MainActor private func topGroup() -> String? {
        shownDoneGroups().first.map { Self.title($0.label) }
    }

    /// Done's groups' titles on screen, top first. Needs You and FYI group by time too (a 今天 of their own): a title
    /// above Done's, while Done's is in the list, is theirs.
    @MainActor private func shownDoneGroups() -> [(frame: CGRect, label: String)] {
        guard let root = try? app.windows.firstMatch.snapshot() else { return [] }
        let top = app.navigationBars.firstMatch.frame.maxY, bottom = app.windows.firstMatch.frame.maxY
        var done: CGRect?, shown: [(frame: CGRect, label: String)] = []
        func walk(_ e: XCUIElementSnapshot) {
            if e.identifier == "inbox.doneTitle" { done = e.frame }
            if e.identifier == "inbox.groupTitle", e.frame.maxY > top + 1, e.frame.minY < bottom { shown.append((e.frame, e.label)) }
            e.children.forEach(walk)
        }
        walk(root)
        return shown.filter { done == nil || $0.frame.minY >= done!.maxY - 1 }.sorted { $0.frame.minY < $1.frame.minY }
    }

    /// A group title's label is its title and count ("1 个月前、8", "1+ Month Ago, 8").
    private static func title(_ label: String) -> String {
        String(label.split(whereSeparator: { $0 == "、" || $0 == "," }).first ?? Substring(label))
    }

    /// A finger's drag of a third of the screen, let go without a flick.
    @MainActor private func drag(down: Bool) {
        let window = app.windows.firstMatch
        let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: down ? 0.35 : 0.7))
        let to = window.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: down ? 0.7 : 0.35))
        from.press(forDuration: 0.05, thenDragTo: to, withVelocity: .default, thenHoldForDuration: 0.1)
    }

    @MainActor private func onScreen(_ e: XCUIElement) -> Bool {
        guard e.exists else { return false }
        let search = app.searchFields.firstMatch
        let f = e.frame, top = app.navigationBars.firstMatch.frame.maxY
        let bottom = search.exists ? search.frame.minY : app.windows.firstMatch.frame.maxY
        return f.height > 1 && f.minY >= top && f.maxY <= bottom
    }

    @MainActor private func wait(_ timeout: TimeInterval, _ condition: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if condition() { return true }
            usleep(200_000)
        }
        return condition()
    }
}
