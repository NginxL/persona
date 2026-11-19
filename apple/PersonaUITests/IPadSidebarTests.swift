import XCTest

/// The iPad's Persona sidebar is the iPhone drawer's (docs/apple/ios.md「iPad」, 2026-11-05 user): the main conversation
/// ("Main Chat" under the assistant's head, #9973) first, then Inbox … Machines, the conversations, New Chat + Settings at the foot above Persona's face and machine.
/// Inbox and Settings are not tabs: Inbox opens beside the sidebar, Settings as a sheet. Against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB), on an iPad simulator, portrait and landscape. TEST_RUNNER_PERSONA_SHOTS=<dir> keeps screenshots;
/// TEST_RUNNER_PERSONA_HOLD=<seconds> stays on each page for `simctl io screenshot` (XCUITest's own turn in landscape).
final class IPadSidebarTests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("iPad only") }
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant"]
    }

    override func tearDown() {
        XCUIDevice.shared.orientation = .portrait
        super.tearDown()
    }

    @MainActor private func shot(_ name: String) {
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("ipad-sidebar-\(name).png"))
        }
        if let hold = ProcessInfo.processInfo.environment["PERSONA_HOLD"].flatMap(UInt32.init) { sleep(hold) }
    }

    @MainActor private func element(_ id: String) -> XCUIElement { app.descendants(matching: .any)[id].firstMatch }

    @MainActor func testSidebarIsTheIPhones() {
        for (name, orientation) in [("portrait", UIDeviceOrientation.portrait), ("landscape", .landscapeLeft)] {
            XCUIDevice.shared.orientation = orientation
            app.launchPinned()
            let main = element("sidebar-main"), inbox = element("sidebar-inbox"), machines = element("sidebar-machines")
            XCTAssertTrue(main.waitForExistence(timeout: 20), "\(name): no Persona row:\n\(app.debugDescription)")
            XCTAssertTrue(inbox.exists && machines.exists, "\(name): the places are missing")
            // Persona first, then Inbox … Machines (the sidebar is the left column).
            XCTAssertLessThan(main.frame.minY, inbox.frame.minY, "\(name): Persona is not on top")
            // The sidebar's head names the assistant with its face; the main conversation's row under it is "Main Chat"
            // (#9973). The name is read there, not written here.
            let head = element("assistant-switcher")
            XCTAssertTrue(head.waitForExistence(timeout: 5), "\(name): no assistant at the sidebar's head:\n\(app.debugDescription)")
            let assistant = head.label.split(separator: ",").first.map { $0.trimmingCharacters(in: .whitespaces) } ?? ""
            XCTAssertFalse(assistant.isEmpty, "\(name): the sidebar's head says no name: \(head.label)")
            XCTAssertTrue(main.label.hasPrefix("主对话"), "\(name): the main row is not Main Chat: \(main.label)")
            // One face on the screen (#9973): the head's. The top bar beside the column is the name pill alone, a
            // finger's height (the face over its pill is taller), named as the head names the assistant.
            let bar = element("persona-avatar")
            XCTAssertTrue(bar.waitForExistence(timeout: 10), "\(name): no top bar menu:\n\(app.debugDescription)")
            XCTAssertTrue(bar.label.hasPrefix(assistant), "\(name): the top bar says \(bar.label), the head \(assistant)")
            XCTAssertLessThan(bar.frame.height, 56, "\(name): the top bar still draws a face beside the column: \(bar.frame)")
            XCTAssertEqual(app.buttons.matching(identifier: "sidebar-main").count, 1, "\(name): the main conversation shows more than once")
            XCTAssertLessThan(inbox.frame.minY, machines.frame.minY)
            XCTAssertLessThan(main.frame.maxX, app.windows.firstMatch.frame.width / 2 + 100, "\(name): not in the left column")
            // New Chat and Settings at the foot, below the places, New Chat on the left.
            let new = element("sidebar-new"), settings = element("sidebar-settings")
            XCTAssertTrue(new.exists && settings.exists, "\(name): no 新对话 / 设置 at the foot")
            XCTAssertGreaterThan(new.frame.minY, machines.frame.maxY)
            XCTAssertEqual(new.frame.midY, settings.frame.midY, accuracy: 4, "\(name): 新对话 and 设置 in one row")
            XCTAssertLessThan(new.frame.minX, settings.frame.minX)
            // The foot's New Chat is the only one (#4206): none on the conversation's top bar beside it.
            XCTAssertFalse(element("assistant-new").exists, "\(name): a New Chat on the page beside the sidebar")
            // The tabs (iPad's bar at the top, above the sidebar's rows): Persona and Agents, no Inbox or Settings.
            func tabs(_ label: String) -> [XCUIElement] {
                app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", label)).allElementsBoundByIndex
                    .filter { $0.exists && $0.frame.maxY < main.frame.minY - 60 }
            }
            XCTAssertFalse(tabs("Agents").isEmpty, "\(name): no Agents tab:\n\(app.debugDescription)")
            XCTAssertTrue(tabs("收件箱").isEmpty, "\(name): 收件箱 is still a tab")
            XCTAssertTrue(tabs("设置").isEmpty, "\(name): 设置 is still a tab")
            shot("home-\(name)")

            // Inbox opens beside the sidebar, which stays.
            inbox.tap()
            let more = element("inbox.more")
            XCTAssertTrue(more.waitForExistence(timeout: 10), "\(name): no inbox:\n\(app.debugDescription)")
            XCTAssertGreaterThan(more.frame.minX, inbox.frame.maxX, "\(name): the inbox is not beside the sidebar")
            XCTAssertTrue(main.exists && inbox.isHittable, "\(name): the sidebar went away with the inbox")
            shot("inbox-\(name)")
            // Persona goes back to the conversation.
            main.tap()
            XCTAssertTrue(element("sidebar-search").waitForExistence(timeout: 5)) // Search at the column's foot (#4206)

            // Settings is a sheet with its ×.
            settings.tap()
            let close = app.buttons["settings-close"].firstMatch
            XCTAssertTrue(close.waitForExistence(timeout: 10), "\(name): no 设置 sheet:\n\(app.debugDescription)")
            shot("settings-\(name)")
            close.tap()
            XCTAssertTrue(close.waitForNonExistence(timeout: 5), "\(name): 设置 did not close")
            XCTAssertTrue(main.isHittable)
            app.terminate()
        }
    }

    /// With the column folded the page has no face beside it: the top bar's face comes back over its name pill (#9973),
    /// and goes again with the column.
    @MainActor func testTheFaceComesBackWithTheColumnFolded() {
        XCUIDevice.shared.orientation = .landscapeLeft
        app.launchPinned()
        // The column folds from its head; the page's sidebar button is there only while it is away (#4206).
        let bar = element("persona-avatar"), fold = element("sidebar-fold-column"), back = element("assistant-sidebar-button")
        XCTAssertTrue(element("sidebar-main").waitForExistence(timeout: 20), "no Persona column:\n\(app.debugDescription)")
        XCTAssertTrue(bar.waitForExistence(timeout: 10) && fold.exists, "no top bar or fold:\n\(app.debugDescription)")
        XCTAssertLessThan(bar.frame.height, 56, "a face beside the column: \(bar.frame)")
        fold.tap()
        XCTAssertTrue(waitFor { !self.element("sidebar-main").isHittable }, "the column did not fold")
        XCTAssertTrue(waitFor { bar.frame.height >= 56 }, "no face with the column folded: \(bar.frame)")
        shot("folded-landscape")
        XCTAssertTrue(back.waitForExistence(timeout: 5), "nothing brings the column back:\n\(app.debugDescription)")
        back.tap()
        XCTAssertTrue(waitFor { self.element("sidebar-main").isHittable }, "the column did not come back")
        XCTAssertTrue(waitFor { bar.frame.height < 56 }, "the face stayed with the column out: \(bar.frame)")
    }

    @MainActor private func waitFor(_ timeout: TimeInterval = 10, _ condition: @escaping () -> Bool) -> Bool {
        let end = Date().addingTimeInterval(timeout)
        while Date() < end {
            if condition() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        return condition()
    }
}
