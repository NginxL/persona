import XCTest

/// A fold or an unfold while the app is active (iPhone Duo) builds the whole tree again, the pages' own width class
/// with it: what they held must come back (#2836). `-swapTreeAt` with `-swapTreeWhole YES` flips the whole app's width
/// class a moment while it is active. Regular width only (an iPad, or the iPhone Duo simulator open).
final class TreeSwapFoldTests: XCTestCase {
    private struct Swaps: Decodable { var count: Int; var rebuilt: Int }
    private struct Chat: Decodable { var key: String; var appeared: Int }

    override func setUpWithError() throws {
        continueAfterFailure = false
        XCUIDevice.shared.orientation = ProcessInfo.processInfo.environment["PERSONA_ORIENTATION"] == "landscape" ? .landscapeLeft : .portrait
    }

    /// The weather fixture's chat open on Agents (demo data): after the fold and the unfold, Agents shows it again.
    @MainActor func testAnOpenAgentChatComesBackAfterAFold() throws {
        let app = XCUIApplication()
        let fixture = "macbook/a1f3c2d4-0000-4000-8000-000000000001"
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "chats", "-tab", "dev", "-probes", "YES", "-holdChromeOpen", "YES",
                               "-openAgentsFresh", "YES", "-openSession", fixture, "-swapTreeAt", "30", "-swapTreeWhole", "YES"]
        app.launchPinned()
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("the tree swaps in regular width only (iPad, iPhone Duo open)") }
        let end = Date.now.addingTimeInterval(25)
        var shown: Chat?
        repeat { shown = app.probe("chat", as: Chat.self, page: "agents"); if shown == nil { sleep(1) } } while shown == nil && Date.now < end
        XCTAssertTrue(shown?.key.hasSuffix(fixture) == true, "the fixture's chat did not open: \(String(describing: shown))")
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.count, 0, "the width flipped before the chat was open")
        let flipEnd = Date.now.addingTimeInterval(60)
        while app.probe("tree", as: Swaps.self)?.count != 1, Date.now < flipEnd { sleep(1) }
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.rebuilt, 2, "the tree did not swap to the shell and back")
        // The new tree's chat, read once the trees built before are gone (they live on a moment, and the old Agents page
        // was where the chat opened, unseen).
        sleep(6)
        let again = app.probe("chat", as: Chat.self, page: "agents")
        XCTAssertTrue(again?.key.hasSuffix(fixture) == true, "Agents did not show the chat again after the fold: \(String(describing: again))")
    }

    /// Health's checkup reports on their way up (`-showReportUpload`, three made-up 24 MB reports the mock hub reads
    /// slowly, one after another): after the fold and the unfold, Health opened again shows them still going up. The
    /// uploads are the app's, not the page's (the page is built again, at the Persona page's root).
    @MainActor func testHealthReportsStillGoUpAfterAFold() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)") }
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let names = ["体检报告-1.pdf", "体检报告-2.pdf", "体检报告-3.pdf"]
        app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-app.tab", "assistant", "-showReportUpload", names.joined(separator: ","),
                               "-showReportUploadMB", "24", "-probes", "YES", "-swapTreeAt", "30", "-swapTreeWhole", "YES"]
        app.launchPinned()
        guard app.windows.firstMatch.frame.width > 600 else { throw XCTSkip("the tree swaps in regular width only (iPad, iPhone Duo open)") }
        let health = app.buttons["sidebar-health"].firstMatch
        XCTAssertTrue(health.waitForExistence(timeout: 20), "no Health in the sidebar:\n\(app.debugDescription)")
        health.tap()
        XCTAssertTrue(app.buttons["health-upload-report"].waitForExistence(timeout: 10), "Health did not open its home")
        XCTAssertTrue(app.buttons["取消上传 \(names[0])"].waitForExistence(timeout: 15), "the reports did not start up:\n\(app.debugDescription)")
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.count, 0, "the width flipped before the reports were on their way")
        let flipEnd = Date.now.addingTimeInterval(60)
        while app.probe("tree", as: Swaps.self)?.count != 1, Date.now < flipEnd { sleep(1) }
        XCTAssertEqual(app.probe("tree", as: Swaps.self)?.rebuilt, 2, "the tree did not swap to the shell and back")
        let again = app.buttons["sidebar-health"].firstMatch
        XCTAssertTrue(again.waitForExistence(timeout: 10), "no Health in the sidebar after the fold:\n\(app.debugDescription)")
        again.tap()
        XCTAssertTrue(app.buttons["health-upload-report"].waitForExistence(timeout: 10), "Health did not reopen its home after the fold")
        let going = NSPredicate { _, _ in names.contains { app.buttons["取消上传 \($0)"].exists } }
        expectation(for: going, evaluatedWith: nil)
        waitForExpectations(timeout: 10)
        let s = app.screenshot()
        let a = XCTAttachment(screenshot: s); a.name = "health-reports-after-fold"; a.lifetime = .keepAlways; add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? s.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("health-reports-after-fold-\(ProcessInfo.processInfo.environment["PERSONA_STYLE"] ?? "x").png"))
        }
    }
}
