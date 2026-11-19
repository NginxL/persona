import UIKit
import XCTest

/// The Agents header with its permission mode (#3719): a screenshot of each mode, the agent waiting in the background with
/// Allow Everything (the screenshot the issue came from), Codex, no space tabs and the folded bar, light and dark; the
/// mode menu and its confirmations; and what the header costs under the hub's pushes (`-perfCounts YES`). Runs on a
/// build without the change too (`PERSONA_PHASE=before`, no menu to open), so the shots and counts compare.
///   PORT=18931 MOCK_HEADER_MODES=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18931 TEST_RUNNER_PERSONA_SHOTS=<dir> xcodebuild test …
///     -only-testing:PersonaUITests/HeaderModeShotTests
final class HeaderModeShotTests: XCTestCase {
    private var hub = ""
    private var app: XCUIApplication!
    private var phase: String { ProcessInfo.processInfo.environment["PERSONA_PHASE"] ?? "after" }

    private static let widget = "macbook/a1f3c2d4-0000-4000-8000-000000000001" // Auto-Edit, can take Allow Everything
    private static let tests = "macbook/a1f3c2d4-0000-4000-8000-000000000002" // Ask First, needs you, a long title
    private static let blog = "studio/a1f3c2d4-0000-4000-8000-000000000003" // Allow Everything, four background waits
    private static let orders = "server/a1f3c2d4-0000-4000-8000-000000000004" // Codex
    private static let photos = "studio/a1f3c2d4-0000-4000-8000-000000000005" // Plan Only, needs you
    private static let review = "macbook/a1f3c2d4-0000-4000-8000-000000000006" // no mode reported yet
    private static let recipe = "macbook/a1f3c2d4-0000-4000-8000-000000000007" // offline, no space tabs
    private static let release = "macbook/a1f3c2d4-0000-4000-8000-000000000008" // Auto, unread

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_HEADER_MODES=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        _ = mock("reset")
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

    @MainActor func testHeaderShots() throws {
        let iPad = isPad
        let scenes: [(String, String)] = iPad
            ? [("bypass-waiting", Self.blog), ("edits", Self.widget)]
            : [("bypass-waiting", Self.blog), ("ask", Self.tests), ("edits", Self.widget), ("plan", Self.photos), ("auto", Self.release),
               ("unknown", Self.review), ("codex", Self.orders), ("no-tabs", Self.recipe)]
        for scheme in ["light", "dark"] {
            for (name, session) in scenes {
                open(session, scheme: scheme)
                shot(name, scheme)
            }
            open(Self.blog, scheme: scheme, folded: true)
            shot("folded", scheme)
        }
    }

    /// The largest accessibility text size (the UI change self-check): the second row with its mode, and the folded bar.
    @MainActor func testLargestTextShots() throws {
        guard phase == "after", !isPad else { throw XCTSkip("the header after the change, on iPhone") }
        let largest = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        for scheme in ["light", "dark"] {
            open(Self.blog, scheme: scheme, extra: largest)
            shot("bypass-waiting-ax", scheme)
            open(Self.blog, scheme: scheme, folded: true, extra: largest)
            shot("folded-ax", scheme)
        }
    }

    /// The menu, and the confirmations for going into Allow Everything and for leaving it with no way back.
    @MainActor func testModeMenuShots() throws {
        guard phase == "after", !isPad else { throw XCTSkip("the mode menu is the change itself (iPhone shots)") }
        continueAfterFailure = true // a missing title fails the test and still leaves every shot after it to look at
        for scheme in ["light", "dark"] {
            open(Self.widget, scheme: scheme)
            let mode = app.buttons["chat.permissionMode"]
            XCTAssertTrue(mode.waitForExistence(timeout: 10), "the header's mode")
            mode.tap()
            sleep(1)
            shot("menu-can-allow", scheme)
            assertMenuTitle()
            menuItem(["完全访问", "Full access"]).tap() // i18n-ignore: the test runs in zh-Hans
            XCTAssertTrue(app.alerts.firstMatch.waitForExistence(timeout: 5), "going into Allow Everything asks first")
            shot("confirm-allow", scheme)
            cancelAlert()

            open(Self.blog, scheme: scheme)
            let blogMode = app.buttons["chat.permissionMode"]
            XCTAssertTrue(blogMode.waitForExistence(timeout: 10), "the header's mode")
            blogMode.tap()
            sleep(1)
            shot("menu-in-allow", scheme)
            assertMenuTitle()
            menuItem(["计划模式", "Plan mode"]).tap() // i18n-ignore: the test runs in zh-Hans
            XCTAssertTrue(app.alerts.firstMatch.waitForExistence(timeout: 5), "leaving Allow Everything with no way back asks first")
            shot("confirm-leave", scheme)
            cancelAlert()

            // An agent not started with Allow Everything: the row is there, disabled, saying why.
            open(Self.tests, scheme: scheme)
            let testsMode = app.buttons["chat.permissionMode"]
            XCTAssertTrue(testsMode.waitForExistence(timeout: 10), "the header's mode")
            testsMode.tap()
            sleep(1)
            shot("menu-cannot-allow", scheme)
            assertMenuTitle()
        }
    }

    /// An agent that has not reported its mode yet (a Claude Code agent before its first prompt): the header's menu, its
    /// button's VoiceOver label and the composer's model menu say just "Permission Mode", as the capsule does, never
    /// "Permission Mode: Unknown" (#5596).
    @MainActor func testUnknownModeShots() throws {
        guard !isPad else { throw XCTSkip("iPhone shots") }
        continueAfterFailure = true // every shot is taken even when a label is wrong (a build without the change)
        let plain = ["权限模式", "Permission Mode"] // i18n-ignore: the test runs in zh-Hans
        for scheme in ["light", "dark"] {
            open(Self.review, scheme: scheme)
            let mode = app.buttons["chat.permissionMode"]
            XCTAssertTrue(mode.waitForExistence(timeout: 10), "the header's mode")
            XCTAssertTrue(plain.contains(mode.label), "the header button's VoiceOver label: \(mode.label)")
            mode.tap()
            sleep(1)
            shot("menu-unknown", scheme)
            assertNoUnknownMode()
            let title = app.descendants(matching: .any)
                .matching(NSPredicate(format: "label IN %@ AND identifier != %@", plain, "chat.permissionMode")).firstMatch
            XCTAssertTrue(title.waitForExistence(timeout: 3), "the menu's title line; the screen's elements:\n\(app.debugDescription)")

            open(Self.review, scheme: scheme)
            let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
            XCTAssertTrue(field.waitForExistence(timeout: 10), "the composer")
            field.tap() // the model button is on the open composer's second row
            let modes = app.buttons["shell-composer-modes"]
            XCTAssertTrue(modes.waitForExistence(timeout: 10), "the composer's model button")
            modes.tap()
            sleep(1)
            // The long model list opens at the current model. The mode item is at the far end, and the menu draws (and
            // lists) a row only once it is scrolled into view.
            let item = app.descendants(matching: .any).matching(NSPredicate(
                format: "(label BEGINSWITH %@ OR label BEGINSWITH %@) AND identifier != %@", plain[0], plain[1], "chat.permissionMode")).firstMatch
            for _ in 0..<4 where !(item.exists && item.isHittable) {
                app.collectionViews.firstMatch.swipeDown()
                sleep(1)
            }
            shot("composer-menu-unknown", scheme)
            XCTAssertTrue(item.exists && item.isHittable, "the model menu's Permission Mode item on screen; the screen's elements:\n\(app.debugDescription)")
            if item.exists { XCTAssertTrue(plain.contains(item.label), "the model menu's Permission Mode item: \(item.label)") }
            assertNoUnknownMode()
        }
        // The largest accessibility text size (the UI change self-check): the header's menu.
        let largest = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        for scheme in ["light", "dark"] {
            open(Self.review, scheme: scheme, extra: largest)
            let mode = app.buttons["chat.permissionMode"]
            XCTAssertTrue(mode.waitForExistence(timeout: 10), "the header's mode")
            mode.tap()
            sleep(1)
            shot("menu-unknown-ax", scheme)
            assertNoUnknownMode()
        }
    }

    @MainActor private func assertNoUnknownMode() {
        let unknown = NSPredicate(format: "label BEGINSWITH %@ OR label BEGINSWITH %@", "权限模式：未知", "Permission Mode: Unknown") // i18n-ignore
        XCTAssertFalse(app.descendants(matching: .any).matching(unknown).firstMatch.exists, "a mode named Unknown")
    }

    /// The menu's first line says the mode, "Permission Mode: …", as on the web and Android (the header's button has the
    /// same words as its VoiceOver label, so it is left out).
    @MainActor private func assertMenuTitle() {
        let says = NSPredicate(format: "(label BEGINSWITH %@ OR label BEGINSWITH %@) AND identifier != %@",
                               "权限模式：", "Permission Mode: ", "chat.permissionMode") // i18n-ignore: the test runs in zh-Hans
        let title = app.descendants(matching: .any).matching(says).firstMatch
        if !title.waitForExistence(timeout: 3) { XCTFail("the menu's title line; the screen's elements:\n\(app.debugDescription)") }
    }

    /// View bodies and main-thread time under twelve rounds of the hub's pushes, with the busy widget agent's chat open
    /// (its header has tabs, the loop and background pill and, after the change, the mode). Written to
    /// `perf-<phase>-<device>.json` beside the shots.
    @MainActor func testHeaderCostUnderPushes() throws {
        open(Self.widget, scheme: "light", extra: ["-perfCounts", "YES"])
        sleep(3)
        // Three windows of twelve rounds each, so the noise between windows shows next to any difference.
        var windows: [[String: Any]] = []
        for _ in 0..<3 {
            let start = counts()
            mock("churn?n=12&ms=400&steps=1")
            let deadline = Date.now.addingTimeInterval(30)
            while (mock("churned")["rounds"] as? Int ?? 0) < 12, Date.now < deadline { usleep(300_000) }
            sleep(2)
            let end = counts()
            var bodies: [String: Int] = [:]
            for (k, v) in end["bodies"] as? [String: Int] ?? [:] { bodies[k] = v - ((start["bodies"] as? [String: Int])?[k] ?? 0) }
            windows.append(["bodies": bodies, "events": (end["events"] as? Int ?? 0) - (start["events"] as? Int ?? 0),
                            "passes": (end["passes"] as? Int ?? 0) - (start["passes"] as? Int ?? 0),
                            "ms": (end["ms"] as? Double ?? 0) - (start["ms"] as? Double ?? 0), "max_ms": end["max"] as? Double ?? 0])
        }
        var out = windows[0] // the first window at the top level, as an older run of this test wrote it
        out["phase"] = phase
        out["device"] = device
        out["rounds"] = 12
        out["windows"] = windows
        let data = try JSONSerialization.data(withJSONObject: out, options: [.sortedKeys, .prettyPrinted])
        let a = XCTAttachment(data: data, uniformTypeIdentifier: "public.json")
        a.name = "perf-\(phase)-\(device).json"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try data.write(to: URL(fileURLWithPath: "\(dir)/perf-\(phase)-\(device).json"))
        }
    }

    // MARK: -

    private var isPad: Bool { UIDevice.current.userInterfaceIdiom == .pad }
    private var device: String { isPad ? "ipad" : "iphone" }

    @MainActor private func open(_ session: String, scheme: String, folded: Bool = false, extra: [String] = []) {
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-openChatsFresh", "YES", "-tab", "dev", "-openSession", session,
                               "-ui.scheme", scheme, "-holdChromeOpen", "YES"] + (folded ? ["-showCompactChrome", "YES"] : []) + extra
        app.launchPinned()
        LiveActivityShots.allowNotifications()
        let title = app.descendants(matching: .any)[folded ? "chat.compactTitle" : "chat.title"].firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 20), "the chat did not open: \(session)")
        sleep(folded ? 5 : 3) // folded 3 s after it opens; the backlog is in and the header has settled
        HomeScreen.dismissIconNote()
    }

    /// A menu row by its first words: the modes are switches, which the system may list as buttons, switches or menu items.
    @MainActor private func menuItem(_ labels: [String]) -> XCUIElement {
        let match = NSPredicate(format: labels.map { _ in "label BEGINSWITH %@" }.joined(separator: " OR "), argumentArray: labels)
        let kinds = NSPredicate(format: "elementType IN %@", [XCUIElement.ElementType.button, .switch, .menuItem].map(\.rawValue))
        let item = app.descendants(matching: .any).matching(match).matching(kinds).firstMatch
        XCTAssertTrue(item.waitForExistence(timeout: 3), "the menu's \(labels[0])")
        return item
    }

    @MainActor private func cancelAlert() {
        let alert = app.alerts.firstMatch
        let cancel = alert.buttons.matching(NSPredicate(format: "label IN %@", ["取消", "Cancel"])).firstMatch // i18n-ignore
        XCTAssertTrue(cancel.waitForExistence(timeout: 5), "the alert's Cancel")
        cancel.tap()
        sleep(1)
    }

    @discardableResult
    private func mock(_ what: String) -> [String: Any] {
        let done = expectation(description: what)
        var out: [String: Any] = [:]
        URLSession.shared.dataTask(with: URL(string: hub + "/mock/" + what)!) { data, _, _ in
            out = (data.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any]) ?? [:]
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        return out
    }

    @MainActor private func counts() -> [String: Any] {
        let probe = app.staticTexts["perf.counts"]
        XCTAssertTrue(probe.waitForExistence(timeout: 5), "perf.counts (launched with -perfCounts YES)")
        return (try? JSONSerialization.jsonObject(with: Data(probe.label.utf8))) as? [String: Any] ?? [:]
    }

    @MainActor private func shot(_ name: String, _ scheme: String) {
        let screen = XCUIScreen.main.screenshot() // the whole screen: a menu or an alert over the page too
        let a = XCTAttachment(screenshot: screen)
        a.name = "\(phase)-\(device)-\(name)-\(scheme)"
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? screen.pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/\(phase)-\(device)-\(name)-\(scheme).png"))
        }
    }
}
