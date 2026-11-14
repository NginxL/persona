import XCTest

/// The Knowledge page's kind pills stay put (#7507): no pill moves when another kind is picked or a kind's list arrives,
/// in the plain style and in BeautifulUI, light and dark, at the largest text size and in English. Against
/// scripts/mock/mock_hub.py with MOCK_KB_TOPICS=1 (PERSONA_MOCK_HUB); the hub answers late while a kind loads
/// (`/mock/delay`), so each pick is read twice, while its list is on the way and once it is there. PERSONA_SHOTS=<dir>
/// keeps screenshots of two picks and the frames each read found (PERSONA_SHOTS_PHASE names the run, such as before or
/// after).
final class KnowledgeKindPillsUITests: XCTestCase {
    private var hub = ""
    private static let kinds = ["projects", "topics", "chats", "people", "docs", "meetings", "events", "recordings"]
    /// How late the hub answers while a pick is read: long enough to read the pills before the list lands.
    private static let lateBy = 1.5

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_KB_TOPICS=1 (PERSONA_MOCK_HUB)")
        }
        hub = h
        addTeardownBlock { [hub] in _ = try? Data(contentsOf: URL(string: hub + "/mock/delay?s=0")!) }
    }

    @MainActor func testPillsStayPutInThePlainStyle() throws { try pillsStayPut(style: "glass") }

    @MainActor func testPillsStayPutInBeautifulUI() throws { try pillsStayPut(style: "beautiful") }

    // MARK: Helpers

    private struct Look { var name: String; var args: [String] }

    @MainActor private func pillsStayPut(style: String) throws {
        // English too: Chinese titles are as wide in the picked weight as in the others, Latin ones are not.
        let looks = [Look(name: "light", args: ["-ui.scheme", "light"]), Look(name: "dark", args: ["-ui.scheme", "dark"]),
                     Look(name: "light-ax5", args: ["-ui.scheme", "light", "-UIPreferredContentSizeCategoryName",
                                                    "UICTContentSizeCategoryAccessibilityXXXL"]),
                     Look(name: "light-en", args: ["-ui.scheme", "light", "-AppleLanguages", "(en)", "-AppleLocale", "en_US"])]
        var moved: [String] = []
        var log: [String] = []
        for look in looks {
            _ = try? Data(contentsOf: URL(string: hub + "/mock/delay?s=0")!)
            let app = XCUIApplication()
            let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
            app.launchArguments = ["-pairURL", pair, "-tab", "assistant", "-openAssistantPage", "knowledge", "-ui.style", style] + look.args
            app.launchPinned()
            let first = app.buttons["kb.kind.projects"]
            XCTAssertTrue(first.waitForExistence(timeout: 30), "no kind pills (\(style) \(look.name)):\n\(app.debugDescription)")
            let start = try frames(app)
            let base = try settled(app)
            log.append("\(look.name) start: " + describe(start.frames))
            if let d = diff(start.frames, base.frames) { moved.append("\(look.name): the projects list arriving moved \(d)") }
            _ = try? Data(contentsOf: URL(string: hub + "/mock/delay?s=\(Self.lateBy)")!)
            for kind in Self.kinds.dropFirst() + ["projects"] {
                let pill = app.buttons["kb.kind." + kind]
                XCTAssertTrue(pill.exists, "no \(kind) pill (\(style) \(look.name))")
                pill.tap()
                let loading = try frames(app)
                let loaded = try settled(app)
                XCTAssertTrue(app.buttons["kb.kind." + kind].isSelected, "\(kind) is not the selected pill (\(style) \(look.name))")
                if let d = diff(base.frames, loading.frames) {
                    moved.append("\(look.name): picking \(kind)\(loading.spinner ? " (list on the way)" : "") moved \(d)")
                }
                if let d = diff(base.frames, loaded.frames) { moved.append("\(look.name): \(kind)'s list arriving moved \(d)") }
                // Projects once picked again at the end: by then SpringBoard's banner about a changed icon (another
                // `-ui.scheme`) has gone from over the page.
                if kind == "meetings" || kind == "projects" { shot(app, "\(style)-\(look.name)-\(kind)") }
            }
            _ = try? Data(contentsOf: URL(string: hub + "/mock/delay?s=0")!)
            app.terminate()
        }
        report(style: style, lines: log + (moved.isEmpty ? ["no pill moved"] : moved))
        XCTAssertTrue(moved.isEmpty, "kind pills moved (\(style)):\n" + moved.joined(separator: "\n"))
    }

    /// Every pill's frame in one read of the page, and whether a list was still loading then.
    @MainActor private func frames(_ app: XCUIApplication) throws -> (frames: [String: CGRect], spinner: Bool) {
        var out: [String: CGRect] = [:]
        var spinner = false
        func walk(_ e: XCUIElementSnapshot) {
            if e.identifier.hasPrefix("kb.kind."), e.elementType == .button { out[String(e.identifier.dropFirst(8))] = e.frame }
            if e.elementType == .activityIndicator { spinner = true }
            e.children.forEach(walk)
        }
        walk(try app.snapshot())
        XCTAssertEqual(Set(out.keys), Set(Self.kinds), "not every kind pill is on the page")
        return (out, spinner)
    }

    /// The frames once no list is loading any more and the pick's animation is over.
    @MainActor private func settled(_ app: XCUIApplication) throws -> (frames: [String: CGRect], spinner: Bool) {
        let end = Date().addingTimeInterval(20)
        var f = try frames(app)
        while f.spinner, Date() < end {
            Thread.sleep(forTimeInterval: 0.25)
            f = try frames(app)
        }
        XCTAssertFalse(f.spinner, "a list never arrived")
        Thread.sleep(forTimeInterval: 0.6)
        return try frames(app)
    }

    /// The pills whose frame differs by more than half a point, or nil when none does.
    private func diff(_ a: [String: CGRect], _ b: [String: CGRect]) -> String? {
        let off = Self.kinds.compactMap { k -> String? in
            guard let x = a[k], let y = b[k] else { return nil }
            let same = abs(x.minX - y.minX) <= 0.5 && abs(x.minY - y.minY) <= 0.5 && abs(x.width - y.width) <= 0.5 && abs(x.height - y.height) <= 0.5
            return same ? nil : "\(k) \(Self.text(x)) → \(Self.text(y))"
        }
        return off.isEmpty ? nil : off.joined(separator: ", ")
    }

    private func describe(_ f: [String: CGRect]) -> String { Self.kinds.map { "\($0) \(f[$0].map(Self.text) ?? "-")" }.joined(separator: ", ") }

    private static func text(_ r: CGRect) -> String { String(format: "(%.1f, %.1f, w %.1f, h %.1f)", r.minX, r.minY, r.width, r.height) }

    private func report(style: String, lines: [String]) {
        let text = lines.joined(separator: "\n")
        print("kind pills (\(style)):\n" + text)
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let phase = env["PERSONA_SHOTS_PHASE"] ?? "run"
        try? text.write(toFile: dir + "/frames-\(style)-\(phase).txt", atomically: true, encoding: .utf8)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        let env = ProcessInfo.processInfo.environment
        if let dir = env["PERSONA_SHOTS"] {
            let phase = env["PERSONA_SHOTS_PHASE"] ?? "run"
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name)-\(phase).png"))
        }
    }
}
