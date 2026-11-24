import XCTest

/// A page opened from inside another is a secondary page (#9971): it always has Back, Back returns to the page it came
/// from, and the sidebar's choice stays where it was. A page picked in the sidebar is a root (#8359, #8603; covered by
/// `SidebarRootsUITests`). Here, in the Sidebar layout:
/// - a task's card in the main conversation pushes its Agent's chat (it was made a root: no Back, the sidebar on the Agent);
/// - under iPhone Duo's side bars, Back stays where it is when the Agent comes up: a task still starting puts it at the top
///   of the side column too (`DetailHeader`), not at the top left;
/// - a rightward swipe from the middle of the page goes back (iOS 26's swipe back from anywhere), on the Agent's chat and
///   on the task's page that hides the system bar.
/// Against Fixtures/dispatch_hub.py (PERSONA_MOCK_HUB). `-sideBars YES` lays the pages out as under the Duo's side bars on
/// any simulator. PERSONA_SHOTS=<dir> keeps the screens (PERSONA_SHOTS_DEVICE names the device; PERSONA_SHOTS_EXTERNAL=1
/// asks the host for them, as the open Duo's own come out black).
final class SecondaryPageBackTests: XCTestCase {
    private var app: XCUIApplication!
    private var hub = ""
    private static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private static let widgetAgent = "macbook/a1f3c2d4-0000-4000-8000-000000000001"

    private struct Page: Decodable, CustomStringConvertible {
        var root: String
        var path: Int
        var description: String { "root \(root), path \(path)" }
    }

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs Fixtures/dispatch_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    // MARK: - Tests

    /// The task's card pushes the Agent's chat on the conversation: Back is there and leads back; the sidebar's root is the
    /// conversation all along.
    @MainActor func testATaskCardPushesItsAgentWithBack() throws {
        try cardPushes(sideBars: false)
    }

    /// The same under side bars (the system's bar holds Back, at the top of the side column on the Duo).
    @MainActor func testATaskCardPushesItsAgentWithBackUnderSideBars() throws {
        try cardPushes(sideBars: true)
    }

    /// The sidebar's one selected row stays the main conversation's while a task's card pushes its Agent's chat on it
    /// (#11032): a drill-down, not a pick; the Agent's row is not selected. Read where the column stays beside the page
    /// (the iPad, the open Duo on its side), and everywhere once Back has returned and the sidebar is brought out.
    @MainActor func testAnAgentPushedFromTheMainConversationLeavesItSelected() throws {
        launch(sideBars: false)
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        // Beside the page only: a drawer or a column over the page would cover the card.
        if main.waitForExistence(timeout: 5), main.isHittable {
            app.assertOneSelectedSidebarRow("sidebar-main", "the main conversation at launch")
            // Laid over the page (the open Duo upright), it covers the card: slid away with its own button first.
            let fold = app.descendants(matching: .any)["sidebar-fold-column"].firstMatch
            if !conversation.isHittable, fold.exists, fold.isHittable {
                fold.tap()
                let deadline = Date.now.addingTimeInterval(5)
                while Date.now < deadline, !conversation.isHittable { usleep(200_000) }
            }
        }
        shot("selection-0-main")
        let before = page()
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        if let before {
            let pushed = settledPage { $0.path == before.path + 1 }
            XCTAssertEqual(pushed?.root, before.root, "the card changed the sidebar's root: \(before) → \(pushed.map(\.description) ?? "-")")
        }
        if main.exists && main.isHittable {
            app.assertOneSelectedSidebarRow("sidebar-main", "an Agent pushed from the main conversation")
        }
        shot("selection-1-pushed")
        oneBack("the Agent's chat").tap()
        assertConversation("Back from the Agent's chat")
        bringOutSidebar("back on the main conversation")
        app.assertOneSelectedSidebarRow("sidebar-main", "back on the main conversation")
        shot("selection-2-back")
    }

    /// Under side bars Back is where the Agent's chat has it before the Agent is up: the task's starting page has it in the
    /// system's bar, at the same place, and no ‹ of its own at the top left.
    @MainActor func testUnderSideBarsBackStaysPutWhileTheAgentStarts() throws {
        launch(sideBars: true)
        guard wide else { throw XCTSkip("pushed beside the sidebar in regular width only (iPad, iPhone Duo open)") }
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        let agentBack = oneBack("the Agent's chat")
        shot("agent-sidebars")
        agentBack.tap()
        assertConversation("Back from the Agent's chat")

        link("todo_dispatch_starting").tap()
        XCTAssertTrue(startingPage.waitForExistence(timeout: 10), app.debugDescription)
        let startingBack = oneBack("the starting task's page")
        XCTAssertEqual(startingBack.frame.midX, agentBack.frame.midX, accuracy: 2,
                       "Back moved: \(startingBack.frame) on the starting page, \(agentBack.frame) on the Agent's chat")
        XCTAssertEqual(startingBack.frame.midY, agentBack.frame.midY, accuracy: 2,
                       "Back moved: \(startingBack.frame) on the starting page, \(agentBack.frame) on the Agent's chat")
        shot("starting-sidebars")
        startingBack.tap()
        assertConversation("Back from the starting task's page")
    }

    /// With the page's own header (bars across the top): the starting page's ‹ is its header's, one, and leads back.
    @MainActor func testTheStartingPageHasOneBack() throws {
        launch(sideBars: false)
        guard wide else { throw XCTSkip("pushed beside the sidebar in regular width only; a phone's sheet: TaskPageBackTests") }
        link("todo_dispatch_starting").tap()
        XCTAssertTrue(startingPage.waitForExistence(timeout: 10), app.debugDescription)
        let back = oneBack("the starting task's page")
        shot("starting-header")
        back.tap()
        assertConversation("Back from the starting task's page")
    }

    /// Under side bars Back is the system's (`ChatView.systemBack`), and a rightward swipe from the middle of the Agent's
    /// chat goes back (iOS 26's swipe back from anywhere).
    @MainActor func testUnderSideBarsASwipeFromTheMiddleOfTheAgentsChatGoesBack() throws {
        launch(sideBars: true)
        // An iPhone never has side bars; `-sideBars` only lays the page out as under them. The Duo's outer display has real
        // ones (PERSONA_SIDEBARS_REAL); in regular width the simulation holds.
        if !wide && ProcessInfo.processInfo.environment["PERSONA_SIDEBARS_REAL"] == nil {
            throw XCTSkip("side bars only on iPhone Duo (PERSONA_SIDEBARS_REAL)")
        }
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        _ = oneBack("the Agent's chat")
        swipeRight(from: app.buttons.matching(identifier: "chat.title").firstMatch, below: 160)
        assertConversation("a swipe from the middle of the Agent's chat")
    }

    /// The same on a task's page before its Agent is up (regular width, where it is pushed).
    @MainActor func testUnderSideBarsASwipeFromTheMiddleOfAStartingTaskGoesBack() throws {
        launch(sideBars: true)
        guard wide else { throw XCTSkip("pushed beside the sidebar in regular width only; a phone's sheet closes from its edge") }
        link("todo_dispatch_starting").tap()
        let starting = startingPage
        XCTAssertTrue(starting.waitForExistence(timeout: 10), app.debugDescription)
        _ = oneBack("the starting task's page")
        swipeRight(from: starting, below: 0)
        assertConversation("a swipe from the middle of the starting task's page")
    }

    /// With the page's own header (the system's bar and Back hidden: iPad, iPhone, the open Duo upright), UIKit gives the
    /// swipe back from anywhere no touch; the swipe from the leading edge goes back there (`SwipeBack.swift`).
    @MainActor func testWithItsOwnHeaderTheEdgeSwipeGoesBack() throws {
        launch(sideBars: false)
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        sleep(1)
        swipeRight(from: app.buttons.matching(identifier: "chat.title").firstMatch, below: 160)
        XCTExpectFailure("UIKit leaves a page that hides the system's Back out of the swipe back from anywhere (#9971)",
                         options: { let o = XCTExpectedFailure.Options(); o.isStrict = false; return o }()) {
            XCTAssertTrue(conversation.waitForExistence(timeout: 3) && conversation.isHittable, "the swipe from the middle went back")
        }
        if !conversation.isHittable {
            // From the page's leading edge: beside the open sidebar that is the line between them.
            let page = app.windows.firstMatch.frame
            let column = app.descendants(matching: .any)["unified-sidebar"].firstMatch
            let edge = column.exists && column.isHittable ? column.frame.maxX + 2 : 2
            let origin = app.coordinate(withNormalizedOffset: .zero)
            origin.withOffset(CGVector(dx: edge, dy: page.midY))
                .press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: min(page.maxX - 8, edge + 400), dy: page.midY)))
        }
        assertConversation("a swipe from the leading edge of the Agent's chat")
    }

    /// On the Agent's Files the swipe is the pane's first (back to the chat, as its ‹ is), and only then the page's.
    @MainActor func testOnFilesTheSwipeGoesToTheChatFirst() throws {
        launch(sideBars: true)
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        // Files from the chat's ⋯ (`-openPane` would put the conversation on it too).
        let more = app.buttons.matching(NSPredicate(format: "identifier == 'chat.more' OR label IN %@", ["更多", "More"]))
            .allElementsBoundByIndex.first { $0.exists && $0.isHittable }
        XCTAssertNotNil(more, "no ⋯ on the Agent's chat:\n\(app.debugDescription)")
        more?.tap()
        app.openAgentMenuMore() // Files is More's (#13396)
        XCTAssertTrue(app.buttons["文件"].firstMatch.waitForExistence(timeout: 5), "no Files in ⋯:\n\(app.debugDescription)")
        app.buttons["文件"].firstMatch.tap()
        let files = app.descendants(matching: .any)["files.pane"].firstMatch
        XCTAssertTrue(files.waitForExistence(timeout: 15), "Files did not open:\n\(app.debugDescription)")
        sleep(1)
        files.swipeRight(velocity: .fast)
        if ProcessInfo.processInfo.environment["PERSONA_SIDEBARS_REAL"] != nil {
            // The open Duo on its side: the pane's swipe does not go back to the chat there, on main too (not #9971's).
            XCTExpectFailure("the pane's swipe under the Duo's real side bars",
                             options: { let o = XCTExpectedFailure.Options(); o.isStrict = false; return o }()) {
                XCTAssertTrue(files.waitForNonExistence(timeout: 5), "the swipe did not leave Files")
            }
            if files.exists { _ = oneBack("Files").tap() } // the pane's ‹ goes back to the chat first
        }
        XCTAssertTrue(files.waitForNonExistence(timeout: 5), "the swipe did not leave Files:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons.matching(identifier: "chat.title").firstMatch.exists, "the swipe on Files left the Agent's page")
        XCTAssertFalse(conversation.isHittable, "the swipe on Files went back to the conversation")
        sleep(1)
        swipeRight(from: app.buttons.matching(identifier: "chat.title").firstMatch, below: 160)
        // Back on the chat the system's Back shows again, but UIKit does not take the swipe from anywhere up again on a page
        // that showed it only after it came (#9971, docs/lessons/ios.md); Back and the edge swipe go back.
        XCTExpectFailure("the swipe back from anywhere after Files (#9971)",
                         options: { let o = XCTExpectedFailure.Options(); o.isStrict = false; return o }()) {
            XCTAssertTrue(conversation.waitForExistence(timeout: 3) && conversation.isHittable, "the swipe after Files did not go back")
        }
        if !conversation.isHittable { _ = oneBack("the Agent's chat after Files").tap() }
        assertConversation("Back from the Agent's chat after Files")
    }

    // MARK: - Steps

    @MainActor private func cardPushes(sideBars: Bool) throws {
        launch(sideBars: sideBars)
        let before = page()
        link("todo_task_widget").tap()
        assertChat(Self.widgetAgent)
        if let before {
            let pushed = settledPage { $0.path == before.path + 1 }
            XCTAssertEqual(pushed?.root, before.root, "the card changed the sidebar's root: \(before) → \(pushed.map(\.description) ?? "-")")
            XCTAssertEqual(pushed?.path, before.path + 1, "the card did not push the Agent's chat: \(before) → \(pushed.map(\.description) ?? "-")")
        }
        XCTAssertFalse(app.descendants(matching: .any)["chat-sidebar-button"].exists, "the sidebar's button on a pushed chat")
        let back = oneBack("the Agent's chat")
        shot(sideBars ? "card-pushed-sidebars" : "card-pushed")
        back.tap()
        assertConversation("Back from the Agent's chat")
        if let before {
            let popped = settledPage { $0.path == before.path }
            XCTAssertEqual(popped?.root, before.root, "Back left the sidebar on another root: \(popped.map(\.description) ?? "-")")
        }
        shot(sideBars ? "card-back-sidebars" : "card-back")
    }

    // MARK: - Helpers

    @MainActor private func launch(sideBars: Bool, extra: [String] = []) {
        app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url, "-holdChromeOpen", "YES", "-ui.scheme", "system", "-tab", "assistant",
                               "-dev.openChat", "", "-openAssistant", Self.main, "-app.navigation", "sidebar", "-probes", "YES",
                               "-sideBars", sideBars ? "YES" : "NO"] + extra
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1"
        app.launchPinned()
        XCTAssertTrue(conversation.waitForExistence(timeout: 20), app.debugDescription)
    }

    /// The sidebar brought out where it is away: laid over the page (the open Duo upright) or a phone's drawer.
    @MainActor private func bringOutSidebar(_ step: String) {
        let main = app.descendants(matching: .any)["sidebar-main"].firstMatch
        if main.exists && main.isHittable { return }
        let openers = app.buttons.matching(NSPredicate(format: "identifier IN %@ OR label IN %@",
                                                       ["assistant-sidebar-button", "sidebar-drawer-button", "chat-sidebar-button",
                                                        "sidebar-show-button", "ToggleSidebar"],
                                                       ["Show Sidebar", "显示边栏", "显示侧边栏"]))
        // Right after a pop the button can read as not hittable for a moment: after the pop has settled, at its frame.
        let deadline = Date.now.addingTimeInterval(3)
        while Date.now < deadline, !openers.allElementsBoundByIndex.contains(where: { $0.exists && $0.isHittable }) { usleep(200_000) }
        let opener = openers.allElementsBoundByIndex.first { $0.exists && $0.isHittable } ?? openers.firstMatch
        if let frame = (try? opener.snapshot())?.frame, !frame.isEmpty {
            app.windows.firstMatch.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: frame.midX, dy: frame.midY)).tap()
        }
        XCTAssertTrue(main.waitForExistence(timeout: 5) && main.isHittable, "\(step): the sidebar did not come out:\n\(app.debugDescription)")
    }

    /// The task's page while its Agent starts: its message, which the conversation's card does not have (the card says
    /// its Agent is starting too).
    @MainActor private var startingPage: XCUIElement { app.staticTexts["开好以后这里就是它的对话。"].firstMatch }

    @MainActor private var wide: Bool { app.windows.firstMatch.frame.width >= 600 }

    /// The main conversation's In Progress entry: the conversation is in front.
    @MainActor private var conversation: XCUIElement { app.buttons["action-bar.running"] }

    @MainActor private func page() -> Page? { app.probe("sidebar-page", as: Page.self) }

    @MainActor private func settledPage(_ done: (Page) -> Bool) -> Page? {
        let deadline = Date.now.addingTimeInterval(10)
        var last = page()
        while Date.now < deadline {
            if let p = last, done(p) { return p }
            Thread.sleep(forTimeInterval: 0.3)
            last = page()
        }
        return last
    }

    /// The task's card, scrolled clear of the top bar and the dock.
    @MainActor private func link(_ id: String) -> XCUIElement {
        let link = app.buttons["dispatch.task.\(id)"]
        let tabs = app.buttons["shell-assistant"]
        let dock = app.otherElements["action-bar"]
        let deadline = Date.now.addingTimeInterval(30)
        while Date.now < deadline {
            if link.exists {
                let frame = link.frame
                // Clear of the top bar: the Sidebar layout's (the drawer's face, the title) is not the shell's tabs.
                let top = max(tabs.exists ? tabs.frame.maxY : app.frame.minY, 120)
                let bottom = dock.exists ? dock.frame.minY : app.frame.maxY
                if link.isHittable, frame.minY >= top, frame.maxY <= bottom { return link }
                // On the conversation's middle, not the card: one scrolled out of view has no frame to swipe on.
                let window = app.windows.firstMatch
                let from = window.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: frame.minY < top ? 0.35 : 0.65))
                from.press(forDuration: 0.05, thenDragTo: window.coordinate(withNormalizedOffset: CGVector(dx: 0.7, dy: frame.minY < top ? 0.6 : 0.4)))
            }
            usleep(400_000)
        }
        XCTFail("no card for \(id):\n\(app.debugDescription)")
        return link
    }

    @MainActor private func assertChat(_ session: String) {
        let header = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(header.waitForExistence(timeout: 10), "the Agent's chat did not open:\n\(app.debugDescription)")
        let deadline = Date.now.addingTimeInterval(10)
        while (header.value as? String) != session && Date.now < deadline { usleep(200_000) }
        XCTAssertEqual(header.value as? String, session, "opened another Agent")
    }

    /// The page's one way back, hittable, read once the page has settled.
    @MainActor private func oneBack(_ step: String) -> XCUIElement {
        let backs = app.buttons.matching(NSPredicate(format: "identifier == 'BackButton' OR label IN %@", ["返回", "Back"]))
        XCTAssertTrue(backs.firstMatch.waitForExistence(timeout: 10), "\(step): no Back:\n\(app.debugDescription)")
        sleep(1)
        let shown = backs.allElementsBoundByIndex.filter { $0.exists && $0.isHittable }
        XCTAssertEqual(shown.count, 1, "\(step): \(shown.count) Backs:\n\(app.debugDescription)")
        return shown.first ?? backs.firstMatch
    }

    @MainActor private func assertConversation(_ step: String) {
        let deadline = Date.now.addingTimeInterval(10)
        while !(conversation.exists && conversation.isHittable) && Date.now < deadline { usleep(200_000) }
        XCTAssertTrue(conversation.isHittable, "\(step) did not return to the main conversation:\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons.matching(identifier: "chat.title").firstMatch.exists, "\(step): the Agent's chat is still up")
    }

    /// A rightward drag (slow unless `fast`, a flick) that starts in the middle of the page (`below` points under the element, at its middle
    /// horizontally), far from the leading edge, the way a thumb swipes back.
    @MainActor private func swipeRight(from element: XCUIElement, below: CGFloat, fast: Bool = false) {
        XCTAssertTrue(element.waitForExistence(timeout: 10), app.debugDescription)
        let window = app.windows.firstMatch
        let frame = element.frame
        let start = CGVector(dx: frame.midX, dy: frame.midY + below)
        let origin = window.coordinate(withNormalizedOffset: .zero)
        origin.withOffset(start)
            .press(forDuration: 0.05, thenDragTo: origin.withOffset(CGVector(dx: min(window.frame.maxX - 8, start.dx + 320), dy: start.dy)),
                   withVelocity: fast ? .fast : .slow, thenHoldForDuration: 0)
    }

    @MainActor private func shot(_ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let device = env["PERSONA_SHOTS_DEVICE"] ?? (UIDevice.current.userInterfaceIdiom == .pad ? "ipad" : "iphone")
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "light"
        let file = "back-\(device)-\(appearance)-\(name).png"
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
