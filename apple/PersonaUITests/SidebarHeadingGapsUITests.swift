import XCTest

/// The Sidebar layout's three section headings (#10551): Pages, Persona Chats and Agents are as tall and as far apart as
/// each other, folded and unfolded, in the iPad's and the open iPhone Duo's column and the phone's drawer. The Agents
/// heading carries its ⋯, Personal Agents and + and sits in the Agents list's own section; it once stood further down
/// than the other two (the sections' gap, and its words placed in a row of another height).
/// Each heading is one row of the sidebar's list: the rows' frames give the heights and gaps, and each heading's
/// words (the fold button's box, the Agents name) must be centred as far down their row. Against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB), on any simulator. PERSONA_SHOTS=<dir> keeps the screens (PERSONA_SHOTS_DEVICE names the device,
/// PERSONA_SHOTS_APPEARANCE the look; PERSONA_SHOTS_EXTERNAL=1 asks `shotwatch.sh` for a `simctl` screenshot, which the
/// iPhone Duo needs).
final class SidebarHeadingGapsUITests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    /// How far two measures may differ: half a point, under one pixel on every screen.
    private let tolerance: CGFloat = 0.5

    /// A heading measured: its words' element and its row.
    private struct Heading {
        let name: String
        let words: CGRect
        let row: CGRect
        /// From the row's top to the words' middle: the heading box centres its words, so it is the same in every
        /// heading. nil where the element is the whole row (a row's only button can be reported as its cell), which
        /// says nothing of where the words are.
        var middle: CGFloat? { abs(words.height - row.height) < 0.5 ? nil : words.midY - row.minY }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
    }

    /// Both Persona sections folded: the three headings follow each other, one heading's height apart.
    @MainActor func testTheHeadingsAreEvenlySpacedFolded() {
        launch(folded: true)
        let (places, chats, agents) = elements()
        reach(agents, below: 0, "the Agents heading")
        let rows = cells()
        let a = measure("places", places, rows), b = measure("chats", chats, rows), c = measure("agents", agents, rows)
        report("folded", [a, b, c], extra: "pitch \(b.row.minY - a.row.minY) / \(c.row.minY - b.row.minY)")
        shot("folded")
        sameRows([a, b, c])
        XCTAssertEqual(b.row.minY - a.row.minY, c.row.minY - b.row.minY, accuracy: tolerance,
                       "Pages → Persona Chats is \(b.row.minY - a.row.minY) pt, Persona Chats → Agents \(c.row.minY - b.row.minY) pt")
        XCTAssertEqual(b.row.minY - a.row.maxY, c.row.minY - b.row.maxY, accuracy: tolerance, "the gaps between the headings differ")
    }

    /// Both unfolded: each heading stands as far below the row before it, and its first row as far below it.
    @MainActor func testTheHeadingsAreEvenlySpacedUnfolded() {
        launch(folded: false)
        let (places, chats, agents) = elements()
        let a = measure("places", places, cells())
        let placesBelow = below(a.row)
        // Persona Chats: the last place above its heading, the first chat below it.
        reach(chats, below: 60, "the Persona Chats heading")
        let b = measure("chats", chats, cells())
        let chatsAbove = above(b.row), chatsBelow = below(b.row)
        shot("unfolded-chats")
        // Agents: the last of Persona's chats above it, the filters below it.
        reach(agents, below: 60, "the Agents heading")
        let c = measure("agents", agents, cells())
        let agentsAbove = above(c.row), agentsBelow = below(c.row)
        report("unfolded", [a, b, c], extra: "above: chats \(chatsAbove) agents \(agentsAbove); below: places \(placesBelow) "
               + "chats \(chatsBelow) agents \(agentsBelow)")
        shot("unfolded-agents")
        sameRows([a, b, c])
        XCTAssertEqual(chatsAbove, agentsAbove, accuracy: tolerance, "above Persona Chats \(chatsAbove) pt, above Agents \(agentsAbove) pt")
        XCTAssertEqual(placesBelow, chatsBelow, accuracy: tolerance, "below Pages \(placesBelow) pt, below Persona Chats \(chatsBelow) pt")
        XCTAssertEqual(chatsBelow, agentsBelow, accuracy: tolerance, "below Persona Chats \(chatsBelow) pt, below Agents \(agentsBelow) pt")
    }

    // MARK: Helpers

    /// The Sidebar layout pinned, Persona's two sections folded or not (their remembered values), on the conversation.
    @MainActor private func launch(folded: Bool) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let fold = folded ? "YES" : "NO"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-tab", "assistant", "-app.navigation", "sidebar",
                               "-sidebar.fold.places", fold, "-sidebar.fold.chats", fold]
        app.launchPinned(navigation: true)
        // The iPad's and the open Duo's column is out; a phone's drawer opens from the persona's face.
        let places = element("sidebar-fold-places"), face = element("assistant-sidebar-button")
        let shown = NSPredicate { _, _ in (places.exists && places.isHittable) || face.exists }
        XCTAssertEqual(XCTWaiter().wait(for: [XCTNSPredicateExpectation(predicate: shown, object: NSNull())], timeout: 25), .completed,
                       "no sidebar and no face:\n\(app.debugDescription)")
        if !(places.exists && places.isHittable) {
            face.tap()
            XCTAssertTrue(places.waitForExistence(timeout: 10), "the drawer did not open:\n\(app.debugDescription)")
        }
        usleep(1_000_000) // the column's slide and the list's first layout
    }

    @MainActor private func elements() -> (XCUIElement, XCUIElement, XCUIElement) {
        let places = element("sidebar-fold-places"), chats = element("sidebar-fold-chats")
        // Persona Chats may still be below the screen (unfolded, at a large text size): `reach` brings it.
        XCTAssertTrue(places.waitForExistence(timeout: 10), "no Pages heading:\n\(app.debugDescription)")
        // Before #10551 the Agents heading's words were its only element: measured too, so the test reports the old gaps.
        let named = element("sidebar-agents-heading")
        let agents = named.waitForExistence(timeout: 3) ? named : app.staticTexts["Agents"].firstMatch
        return (places, chats, agents)
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    /// The sidebar's list: the collection view the Pages heading is in (the column beside the page, or the drawer), found
    /// while that heading is on screen and kept as the list scrolls it away.
    @MainActor private var list: XCUIElement {
        if let found { return found }
        // The one that holds the heading: on a phone the page's own list lies under the drawer, behind the same point.
        let count = app.collectionViews.count
        let index = (0..<count).first { app.collectionViews.element(boundBy: $0).descendants(matching: .any)["sidebar-fold-places"].exists } ?? 0
        let list = app.collectionViews.element(boundBy: index)
        found = list
        return list
    }

    private var found: XCUIElement?

    /// Scrolls the sidebar until `target` and `room` points under it are on screen (the next row's top with it).
    @MainActor private func reach(_ target: XCUIElement, below room: CGFloat, _ step: String) {
        let column = list
        for _ in 0..<12 {
            let bottom = column.frame.maxY - Self.footRoom
            let shown = target.exists && target.isHittable
            if shown, target.frame.maxY + room < bottom { break }
            // A held drag by what is missing (half the list while the heading is not there yet): a swipe's momentum
            // carried a heading past the list's top on a phone.
            let missing = shown ? target.frame.maxY + room - bottom + 16 : column.frame.height / 2
            let distance = min(missing, column.frame.height / 2)
            let start = column.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75))
            start.press(forDuration: 0.1, thenDragTo: start.withOffset(CGVector(dx: 0, dy: -distance)),
                        withVelocity: .slow, thenHoldForDuration: 0.4)
        }
        usleep(500_000)
        XCTAssertTrue(target.exists && target.isHittable, "\(step): not reached in the sidebar:\n\(app.debugDescription)")
    }

    /// What the column's foot (New Chat, Search, Settings) and the list's bottom margin may cover.
    private static let footRoom: CGFloat = 80

    /// The list's rows on screen, top to bottom.
    @MainActor private func cells() -> [CGRect] {
        list.cells.allElementsBoundByIndex.map(\.frame).filter { $0.height > 1 }.sorted { $0.minY < $1.minY }
    }

    @MainActor private func measure(_ name: String, _ words: XCUIElement, _ rows: [CGRect]) -> Heading {
        let w = words.frame
        guard let row = rows.first(where: { $0.contains(CGPoint(x: w.midX, y: w.midY)) }) else {
            XCTFail("\(name): its words \(w) are in no row of the list: \(rows)\n\(app.debugDescription)")
            return Heading(name: name, words: w, row: .zero)
        }
        return Heading(name: name, words: w, row: row)
    }

    /// The three headings' rows are as tall, and their words are centred as far down them.
    private func sameRows(_ h: [Heading]) {
        for (x, y) in zip(h, h.dropFirst()) {
            XCTAssertEqual(x.row.height, y.row.height, accuracy: tolerance,
                           "\(x.name) is \(x.row.height) pt tall, \(y.name) \(y.row.height) pt")
        }
        let placed = h.filter { $0.middle != nil }
        XCTAssertFalse(placed.isEmpty, "no heading whose words can be placed: \(h.map(\.words))")
        for (x, y) in zip(placed, placed.dropFirst()) {
            XCTAssertEqual(x.middle!, y.middle!, accuracy: tolerance,
                           "\(x.name)'s words are centred \(x.middle!) pt below its row's top, \(y.name)'s \(y.middle!) pt")
        }
        // Where only one can be placed (unfolded, the Persona headings come as whole rows): its words in the middle of
        // the heading box, which fills the row under the room above it (`SidebarHeadingBox`; the box grows with the
        // text size, the room above does not).
        if placed.count == 1, let one = placed.first {
            let box = (one.row.height + Self.headingAbove) / 2
            XCTAssertEqual(one.middle!, box, accuracy: tolerance,
                           "\(one.name)'s words are centred \(one.middle!) pt below its row's top, its heading box's middle is \(box) pt")
        }
    }

    /// `SidebarHeadingBox.above` (`Theme.Space.s`): the room over each heading, inside its row.
    private static let headingAbove: CGFloat = 8

    /// From the bottom of the row before `row` to its top.
    @MainActor private func above(_ row: CGRect) -> CGFloat {
        guard let previous = cells().last(where: { $0.maxY <= row.minY + tolerance }) else {
            XCTFail("no row above \(row)")
            return .nan
        }
        return row.minY - previous.maxY
    }

    /// From `row`'s bottom to the top of the row after it.
    @MainActor private func below(_ row: CGRect) -> CGFloat {
        guard let next = cells().first(where: { $0.minY >= row.maxY - tolerance }) else {
            XCTFail("no row below \(row)")
            return .nan
        }
        return next.minY - row.maxY
    }

    private func report(_ state: String, _ headings: [Heading], extra: String) {
        let parts = headings.map { "\($0.name) row y \($0.row.minY) h \($0.row.height), words y \($0.words.minY) h \($0.words.height) middle \($0.middle.map { "\($0)" } ?? "-")" }
        print("SIDEBAR-HEADING-GAPS \(state): " + parts.joined(separator: "; ") + "; " + extra)
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone")
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let file = "headings-\(device)-\(appearance)-\(name).png"
        let folder = URL(fileURLWithPath: dir)
        sleep(1)
        guard env["PERSONA_SHOTS_EXTERNAL"] != nil else {
            try? app.screenshot().pngRepresentation.write(to: folder.appendingPathComponent(file))
            return
        }
        try? file.write(to: folder.appendingPathComponent("want"), atomically: true, encoding: .utf8)
        let target = folder.appendingPathComponent(file)
        let deadline = Date.now.addingTimeInterval(15)
        while Date.now < deadline, !FileManager.default.fileExists(atPath: target.path) { Thread.sleep(forTimeInterval: 0.2) }
    }
}
