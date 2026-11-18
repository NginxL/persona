import XCTest

/// The top of the Agents page moves as one piece (#2478): opening a search, typing, scrolling the results, 「取消」, and
/// the tab picked again. The app writes where each block of its top is drawn on every frame (`-showSearchTrace`,
/// `SearchTrace`); after each step the frames are read back and checked, so what a recording would show is asserted:
///
/// - no block turns back on its way within a step (the list overshot ~140 pt and sprang back, 2026-11-05);
/// - no block jumps more than `jump` in one frame, outside a finger's drag;
/// - the shell's top bar and the floating field are never drawn over each other (TestFlight 70), nor both half drawn
///   in the same rows (#4928), at the standard text size and a larger one;
/// - each step ends where it should: the field out under the bar, up in the bar's row while searching, folded away
///   when the tab comes back (#740); while searching the filters scroll with the results (#1051, #2189); the field
///   is found again after a round trip (#314).
///
/// In both styles (`TEST_RUNNER_PERSONA_UI_STYLE`).
final class AgentsTopMotionTests: XCTestCase {
    /// The most a block may move in one frame (60 Hz) outside a drag: a 0.25 s move of 88 pt peaks at ~16, a dropped
    /// frame on a busy machine doubles it; the spring back was 138.
    private let jump: Double = 45
    /// Smaller moves than this are not a turn (layout rounding).
    private let still: Double = 0.75

    private var app: XCUIApplication!
    private var tracePath = ""

    override func setUp() {
        continueAfterFailure = true
        tracePath = "/tmp/agents-top-\(UUID().uuidString).trace"
    }

    override func tearDown() {
        try? FileManager.default.removeItem(atPath: tracePath)
    }

    @MainActor func testTheTopMovesAsOnePiece() throws { try walk() }

    /// The shell's top bar grows with the text (up to `Theme.Shell.topBarLargestTypeSize`), the field's way does not:
    /// the field came into the bar's rows while the bar was still fading (#4928).
    @MainActor func testTheTopMovesAsOnePieceInLargerText() throws { try walk(size: "UICTContentSizeCategoryXXXL") }

    /// At the largest accessibility size the field's words stop at the top bars' size and its capsule and band stay as
    /// they are (#10165): the same walk, the field's way unchanged.
    @MainActor func testTheTopMovesAsOnePieceAtTheLargestText() throws { try walk(size: "UICTContentSizeCategoryAccessibilityXXXL") }

    @MainActor private func walk(size: String? = nil) throws {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "chats", "-dev.openChat", "",
                               "-showSearchTrace", "YES", "-searchTracePath", tracePath, "-probes", "YES"]
        if let size { app.launchArguments += ["-UIPreferredContentSizeCategoryName", size] }
        app.launchPinned()
        XCTAssertTrue(app.collectionViews.firstMatch.waitForExistence(timeout: 20), "the Agents list")
        let shell = app.buttons["agents-new"].firstMatch.waitForExistence(timeout: 5)
        sleep(2)

        // Folded at launch: the filters right under the bar, no field.
        let rest = try XCTUnwrap(frames(since: 0).last?["filter"], "the filters' place at launch")

        let pulled = mark()
        let field = app.revealAgentsSearch()
        sleep(2)
        let out = check("pulling the field out", since: pulled, dragged: true)
        XCTAssertEqual(out.last?["filter"]?.y ?? 0, rest.y + band, accuracy: 1, "the field not all out after the pull")

        // Open, type, 「取消」: each moves as one piece.
        let opened = mark()
        field.tapCenter()
        sleep(2)
        let open = check("opening the search", since: opened)
        if let last = open.last, let float = last["float"], let filter = last["filter"], let nav = last["nav"] {
            XCTAssertEqual(float.y, nav.y, accuracy: 0.5, "the field not in the navigation bar's row while searching")
            XCTAssertEqual(filter.y, rest.y - (shell ? room : 0), accuracy: 1, "the filters not right under the field while searching")
        } else {
            XCTFail("no floating field or filters while searching")
        }

        let typed = mark()
        field.typeText("w")
        sleep(2)
        check("typing", since: typed, swapsContent: true)

        let closing = mark()
        closeButton.tap()
        sleep(2)
        let closed = check("closing the search", since: closing, swapsContent: true)
        if let last = closed.last {
            XCTAssertNil(last["float"], "the floating field still there after 取消")
            XCTAssertEqual(try XCTUnwrap(last["field"], "no field in the list's row after 取消").h, band, accuracy: 0.5,
                           "the field half folded after 取消")
            XCTAssertEqual(try XCTUnwrap(last["filter"]).y, rest.y + band, accuracy: 1, "the filters not under the field after 取消")
        }

        // Searching, the filters go with the results (#1051, #2189); 「取消」 from there comes back to the whole list.
        app.revealAgentsSearch().tapCenter()
        sleep(1)
        app.searchFields.firstMatch.typeText("w")
        sleep(1)
        let scrolled = mark()
        let before = try XCTUnwrap(frames(since: 0).last?["filter"], "the filters while searching").y
        dragRowsUp()
        sleep(1)
        let after = check("scrolling the results", since: scrolled, dragged: true)
        if let y = after.last?["filter"]?.y { XCTAssertLessThan(y, before - 20, "the filters stayed while the results scrolled") }
        closeButton.tap()
        sleep(2)
        let back = mark()
        sleep(1)
        XCTAssertEqual(frames(since: back).last?["filter"]?.y ?? 0, rest.y + band, accuracy: 1, "not back at the list's top after 取消")

        // Another page and back: folded again (#740), and the field comes out on a pull again (#314).
        let away = mark()
        let finger = ShellFinger(app)
        finger.tap(agents: false)
        sleep(2)
        finger.tap(agents: true)
        sleep(2)
        let returned = check("coming back to the tab", since: away, pageMoves: true)
        XCTAssertEqual(returned.last?["filter"]?.y ?? 0, rest.y, accuracy: 1, "not folded again when the tab came back")
        XCTAssertTrue(app.revealAgentsSearch().exists, "no search field after coming back")
    }

    /// The field's band and the shell's room over the list (`AgentsHeader`).
    private let band = 54.0, room = 34.0

    /// The ✕ beside the field while searching.
    @MainActor private var closeButton: XCUIElement {
        let close = app.navigationBars.buttons.matching(NSPredicate(format: "label IN {'取消', 'Cancel', '关闭', 'Close'}")).firstMatch
        XCTAssertTrue(close.waitForExistence(timeout: 5), "the ✕ beside the field")
        return close
    }

    // MARK: The frames

    /// One block on one frame: its top, height, and how opaque it is drawn.
    struct Block { var y: Double; var h: Double; var a: Double }

    private func mark() -> Double { Date.now.timeIntervalSince1970 }

    /// The frames written since `t`, each block by name (only those drawn).
    private func frames(since t: Double) -> [[String: Block]] {
        guard let text = try? String(contentsOfFile: tracePath, encoding: .utf8) else { return [] }
        var out: [[String: Block]] = []
        for line in text.split(separator: "\n") {
            let p = line.split(separator: " ")
            guard p.count > 3, p[1] == "f", let time = Double(p[2]), time >= t else { continue }
            var blocks: [String: Block] = [:], alpha: [String: Double] = [:]
            for kv in p.dropFirst(3) {
                let pair = kv.split(separator: "=", maxSplits: 1).map(String.init)
                guard pair.count == 2 else { continue }
                if pair[0].hasSuffix(".a") { alpha[String(pair[0].dropLast(2))] = Double(pair[1]); continue }
                guard ["topbar", "float", "field", "filter", "needs", "nav"].contains(pair[0]) else { continue }
                let yh = pair[1].split(separator: "/").compactMap { Double($0) }
                if yh.count == 2 { blocks[pair[0]] = Block(y: yh[0], h: yh[1], a: 1) }
            }
            for (k, a) in alpha { blocks[k]?.a = a }
            out.append(blocks.filter { $0.value.a > 0.02 })
        }
        return out
    }

    /// The frames of a step, checked. `dragged`: a finger moved the list (its blocks go with it, back and forth).
    /// `swapsContent`: the list's rows change (only the filters and the field are followed). `pageMoves`: the whole
    /// page slides in (only the end is checked).
    @discardableResult
    private func check(_ step: String, since t: Double, dragged: Bool = false, swapsContent: Bool = false,
                       pageMoves: Bool = false) -> [[String: Block]] {
        // The trace is written in batches: give it a moment.
        usleep(300_000)
        let all = frames(since: t)
        XCTAssertGreaterThan(all.count, 10, "\(step): the app wrote no frames (\(tracePath))")
        guard !pageMoves else { return all }
        var names = ["topbar", "float", "field", "filter"]
        if !swapsContent { names.append("needs") }
        for name in names {
            var last: Double?, way = 0.0, run: [Double] = []
            for f in all {
                guard let b = f[name] else { last = nil; way = 0; continue }
                run.append(b.y)
                if let l = last {
                    let d = b.y - l
                    if !dragged { XCTAssertLessThanOrEqual(abs(d), jump, "\(step): \(name) jumped \(Int(d)) pt in a frame \(trail(run))") }
                    // A finger's drag overshoots and springs back on its own (the list's rubber band).
                    if !dragged, abs(d) > still {
                        if way != 0, (d > 0) != (way > 0) { XCTFail("\(step): \(name) turned back (\(Int(way)) then \(Int(d))) \(trail(run))") }
                        way = d
                    }
                }
                last = b.y
            }
        }
        // The shell's bar and the floating field never over each other.
        for f in all {
            if let bar = f["topbar"], let field = f["float"], bar.a > 0.05, field.a > 0.05 {
                XCTAssertGreaterThanOrEqual(field.y, bar.y + bar.h - 1, "\(step): the field under the shell's bar (bar \(bar), field \(field))")
            }
            // Nor both half drawn in the same rows: the bar's words fading out and the field's coming in ghosted (#4928).
            if let bar = f["topbar"], let field = f["float"], (0.05...0.95).contains(bar.a), (0.05...0.95).contains(field.a),
               field.y < bar.y + bar.h, bar.y < field.y + field.h {
                XCTFail("\(step): the shell's bar and the field both half drawn in the same rows (bar \(bar), field \(field))")
            }
        }
        return all
    }

    private func trail(_ ys: [Double]) -> String { ys.suffix(12).map { String(Int($0)) }.joined(separator: " ") }

    /// A drag over the rows on screen, below the field and above the keyboard (see `AgentsListTests.dragListUp`).
    @MainActor private func dragRowsUp() {
        let w = app.windows.firstMatch.frame
        var bottom = w.maxY * 0.55
        let keyboard = app.keyboards.firstMatch
        if keyboard.exists { bottom = min(bottom, keyboard.frame.minY - 60) }
        let top = w.minY + 180
        let origin = app.coordinate(withNormalizedOffset: .zero)
        origin.withOffset(CGVector(dx: w.midX, dy: bottom))
            .press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: w.midX, dy: top)), withVelocity: .slow, thenHoldForDuration: 0.5)
    }
}
