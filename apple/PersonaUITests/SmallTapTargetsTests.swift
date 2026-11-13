import XCTest

/// Four small controls are hit as a finger-sized area and drawn as before (#6577, from #5414; `tapTargetOutset`):
/// full-screen search's clear ×, a waiting comment's ×, a forwarded page's server chooser, and the Terminals section
/// header's ⋯. Each one's frame is at least `Theme.Size.tapTarget`, and a tap off what it draws, inside that frame,
/// still does what it does. Against scripts/mock/mock_hub.py (PERSONA_MOCK_HUB); PERSONA_SHOTS=<dir> keeps each screen
/// in light and dark (`-ui.scheme`).
///   PORT=18795 MOCK_PREVIEW=1 MOCK_TERMINAL_PANES=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18795 xcodebuild test … -only-testing:PersonaUITests/SmallTapTargetsTests
final class SmallTapTargetsTests: XCTestCase {
    private var hub = ""
    private let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
    /// `Theme.Size.tapTarget` at the default text size.
    private let tapTarget: CGFloat = 44

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_PREVIEW=1 MOCK_TERMINAL_PANES=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        continueAfterFailure = true // each appearance runs and keeps its screen, as the frames are checked
    }

    /// Full-screen search's × (#3997): a tap above the glyph, still over the field, clears what was typed. Search covers
    /// the window only on an open iPhone Duo (#3705), which a simulator opens in Xcode's Device Hub alone:
    /// `-openFullSearch` puts it over any phone's window.
    @MainActor func testSearchClearIsAFingerWide() {
        for scheme in ["light", "dark"] {
            let app = launch(["-tab", "assistant", "-app.tab", "assistant", "-openFullSearch", "YES"], scheme: scheme)
            let field = element(app, "full-search-field")
            if !field.waitForExistence(timeout: 15) { // the sidebar is a closed drawer that was never drawn: open it
                let sidebar = element(app, "assistant-sidebar-button")
                see(sidebar, "no Chat History button", app)
                sidebar.tap()
            }
            see(field, "-openFullSearch opened no field", app)
            field.tap()
            field.typeText("韩梅梅")
            let clear = app.buttons["full-search-clear"]
            see(clear, "no × in a field with words", app)
            sleep(1)
            shot(app, "search-clear-\(scheme)")
            assertTarget(clear, "the search field's ×")
            tap(clear, dy: -15, app)
            XCTAssertTrue(clear.waitForNonExistence(timeout: 5), "a tap 15 pt above the × did not clear the search: \(clear.frame)")
            // Its left: past the gap before it, 2 pt into the end of the field (the × is the later of the two).
            field.typeText("韩")
            see(clear, "no × after typing again", app)
            tap(clear, dx: -20, app)
            XCTAssertTrue(clear.waitForNonExistence(timeout: 5), "a tap 20 pt left of the ×'s centre did not clear the search: \(clear.frame)")
            app.terminate()
        }
    }

    /// A waiting comment's × (#2822): drawn in a 28 pt well; a tap 12 pt under the well, in the card, drops it.
    @MainActor func testCommentRemoveIsAFingerWide() {
        for scheme in ["light", "dark"] {
            let app = launch(["-tab", "dev", "-app.tab", "chats", "-dev.openChat", widget, "-openSession", widget,
                              "-openChatsFresh", "YES", "-showSampleComments", "YES"], scheme: scheme)
            let waiting = app.descendants(matching: .any).matching(identifier: "composer.comment")
            XCTAssertTrue(waitUntil(15) { waiting.count == 3 }, "no sample comments over the box:\n\(app.debugDescription)")
            let remove = app.buttons.matching(identifier: "composer.comment.remove").firstMatch
            see(remove, "no × on the first comment", app)
            sleep(1)
            shot(app, "comment-remove-\(scheme)")
            assertTarget(remove, "the comment card's ×")
            // 12 pt under the 28 pt well, in the card. The frame is what tells it from the old well: iOS also takes a near
            // miss on a small button with no other control around it, as here, not one beside a field (search's ×).
            tap(remove, dy: 26, app)
            XCTAssertTrue(waitUntil(5) { waiting.count == 2 }, "a tap 12 pt under the × did not drop the comment: \(remove.frame)")
            app.terminate()
        }
    }

    /// A forwarded page's server chooser (#4845): a caption line under the title. A tap above the title, and one just over
    /// the page (the header's last points), list the session's servers: more than 44 pt apart, all in the header.
    @MainActor func testPreviewServerChooserIsAFingerTall() throws {
        _ = try Data(contentsOf: URL(string: hub + "/mock/preview?mode=1")!) // web runs, api is stopped
        for scheme in ["light", "dark"] {
            let app = launch(["-tab", "dev", "-openSession", widget, "-openChatsFresh", "YES", "-holdChromeOpen", "YES",
                              "-openPreviewServers", "YES"], scheme: scheme)
            let web = app.buttons["preview-server-web"]
            see(web, "the running web server is not listed", app, timeout: 25)
            web.tap()
            see(app.webViews.staticTexts["Good morning"], "the page did not load through the hub", app, timeout: 25)
            let chooser = app.buttons["link-servers"]
            see(chooser, "no server chooser in the card's header", app)
            sleep(1)
            shot(app, "preview-chooser-\(scheme)")
            assertTarget(chooser, "the preview's server chooser")
            let f = chooser.frame
            let title = app.staticTexts.matching(NSPredicate(format: "label == 'Acme Notes'")).allElementsBoundByIndex
                .first { $0.frame.maxY <= f.midY && $0.frame.minX < f.midX }
            guard let title else { XCTFail("no title over the chooser:\n\(app.debugDescription.prefix(4000))"); return }
            let top = title.frame.minY - 6, bottom = app.webViews.firstMatch.frame.minY - 3
            print("tap target: chooser probes at y \(top) and \(bottom), \(bottom - top) pt apart; title \(title.frame)")
            for (y, what) in [(top, "above the title"), (bottom, "just over the page")] {
                tap(chooser, dy: y - f.midY, app)
                let list = element(app, "preview-server-api")
                see(list, "a tap \(what) (y \(y)) did not list the servers", app, timeout: 5)
                if list.exists { app.buttons["preview-close"].firstMatch.tap(); _ = list.waitForNonExistence(timeout: 5) }
            }
            XCTAssertGreaterThanOrEqual(bottom - top, tapTarget, "the probes are not a finger apart")
            app.terminate()
        }
    }

    /// The Terminals section header's ⋯ (#5061): a tap above the glyph opens its menu.
    @MainActor func testTerminalsHeaderMenuIsAFingerWide() {
        for scheme in ["light", "dark"] {
            let app = launch(["-tab", "dev", "-app.tab", "chats", "-dev.openChat", "", "-openTerminalsPage", "all"], scheme: scheme)
            let more = app.buttons["terminals.more"].firstMatch
            see(more, "no ⋯ in a computer's header on Terminals", app, timeout: 20)
            sleep(1)
            shot(app, "terminals-more-\(scheme)")
            assertTarget(more, "the Terminals header's ⋯")
            tap(more, dy: -15, app)
            let item = app.buttons.matching(NSPredicate(format: "label BEGINSWITH '在 ' AND label ENDSWITH '新开终端'")).firstMatch
            see(item, "a tap 15 pt above the ⋯ did not open its menu", app, timeout: 5)
            app.terminate()
        }
    }

    // MARK: -

    @MainActor private func assertTarget(_ e: XCUIElement, _ what: String) {
        print("tap target: \(what) \(e.frame)")
        XCTAssertGreaterThanOrEqual(e.frame.width, tapTarget, "\(what) is \(e.frame.width) pt wide: \(e.frame)")
        XCTAssertGreaterThanOrEqual(e.frame.height, tapTarget, "\(what) is \(e.frame.height) pt tall: \(e.frame)")
    }

    /// A touch `dx`, `dy` points off the element's centre, where the glyph or the text is drawn.
    @MainActor private func tap(_ e: XCUIElement, dx: CGFloat = 0, dy: CGFloat = 0, _ app: XCUIApplication) {
        let f = e.frame
        app.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: f.midX + dx, dy: f.midY + dy)).tap()
    }

    @MainActor private func launch(_ args: [String], scheme: String) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-ui.scheme", scheme] + args
        app.launchPinned()
        dismissAlerts()
        return app
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    @MainActor private func see(_ e: XCUIElement, _ what: String, _ app: XCUIApplication, timeout: TimeInterval = 10) {
        XCTAssertTrue(e.waitForExistence(timeout: timeout), "\(what):\n\(app.debugDescription.prefix(4000))")
    }

    private func dismissAlerts() {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let ok = springboard.alerts.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许", "OK", "好"])).firstMatch
        if ok.waitForExistence(timeout: 2) { ok.tap() }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("targets-\(name).png"))
        }
    }

    private func waitUntil(_ timeout: TimeInterval, _ ok: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if ok() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.3))
        }
        return false
    }
}
