import Vision
import XCTest

/// The screenshot banner (#9508, docs/apple/ios.md 「Screenshot banner」) against scripts/mock/mock_hub.py
/// (PERSONA_MOCK_HUB): with Settings' switch on, a system screenshot of a chat (XCUIScreen: taken outside the app, as
/// the side and volume-up buttons take one) shows the assistant's banner where the top bar is, while the bar stays on
/// screen, takes taps and is VoiceOver's; off, the screenshot shows the bar. The Assistant chat and an Agent chat, light
/// and dark; and the switch in Settings › Appearance & Display. TEST_RUNNER_PERSONA_SHOTS=<dir> keeps
/// `screenshot-banner-<assistant|agent>-<on|off>-<light|dark>.png` and `screenshot-banner-settings-<light|dark>.png`,
/// and `testLooks` (only then) the banner at the default and the largest bar text size with a long name too (#10172).
///   PORT=18793 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18793 xcodebuild test … -only-testing:PersonaUITests/ScreenshotBannerUITests
final class ScreenshotBannerUITests: XCTestCase {
    private var hub = ""
    private static let main = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private static let agent = "macbook/a1f3c2d4-0000-4000-8000-000000000002"

    override func setUpWithError() throws {
        guard let h = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        hub = h
    }

    @MainActor func testAssistantChat() throws {
        for scheme in ["light", "dark"] {
            let off = try look(banner: false, scheme: scheme, page: "assistant", ["-openAssistant", Self.main], control: "assistant-new")
            let on = try look(banner: true, scheme: scheme, page: "assistant", ["-openAssistant", Self.main], control: "assistant-new",
                              tapThrough: "assistant-welcome")
            XCTAssertGreaterThan(off.spread, 40, "\(scheme): with the switch off the screenshot has the bar's New Chat")
            XCTAssertGreaterThan(on.changed(from: off), Self.gone, "\(scheme): with it on the screenshot has the banner where New Chat is")
        }
    }

    @MainActor func testAgentChat() throws {
        let args = ["-openSession", Self.agent, "-holdChromeOpen", "YES"]
        for scheme in ["light", "dark"] {
            let off = try look(banner: false, scheme: scheme, page: "agent", args, control: "chat.more")
            let on = try look(banner: true, scheme: scheme, page: "agent", args, control: "chat.more")
            XCTAssertGreaterThan(off.spread, 40, "\(scheme): with the switch off the screenshot has the header's ⋯")
            XCTAssertGreaterThan(on.changed(from: off), Self.gone, "\(scheme): with it on the screenshot has the banner where ⋯ is")
        }
    }

    /// The iPad's bar (`classicBar`: the face over the name in the middle, New Chat beside the sidebar): on screen and
    /// for VoiceOver with the switch on, and the screenshots kept for a look (the lockup can reach the middle of a
    /// narrow page, so no pixel check here). Skips on a phone.
    @MainActor func testIPad() throws {
        guard UIDevice.current.userInterfaceIdiom == .pad else { throw XCTSkip("an iPad's bar") }
        for scheme in ["light", "dark"] {
            for banner in [false, true] {
                _ = try look(banner: banner, scheme: scheme, page: "ipad", ["-openAssistant", Self.main], control: "persona-avatar")
            }
        }
    }

    /// The banner names the assistant as the bar does (#12243). Against a mock with one assistant
    /// (`MOCK_MANAGERS=1 PORT=… python3 scripts/mock/mock_hub.py`), the Sidebar layout's Assistant chat (the drawer row
    /// at compact width, the bar beside the column where it stands beside the page): the bar's name pill says the
    /// current assistant's name, and the system screenshot's banner, read with Vision, says the same name, never the
    /// persona's ("Persona", "Pers…"), which it said before. TEST_RUNNER_PERSONA_SHOTS keeps
    /// `screenshot-banner-name-<off|on>-<light|dark>.png` (off: what the screen shows; on: what a screenshot shows).
    @MainActor func testTheBannerSaysTheBarsName() throws {
        for scheme in ["light", "dark"] {
            let args = ["-openAssistant", Self.main, "-app.navigation", "sidebar"]
            try look(banner: false, scheme: scheme, page: "name", args, control: "persona-avatar", measure: false)
            let app = launch(["-ui.scheme", scheme] + args, banner: true)
            defer { app.terminate() }
            let pill = app.descendants(matching: .any)["persona-avatar"].firstMatch
            XCTAssertTrue(pill.waitForExistence(timeout: 30), "\(scheme): no name pill")
            // "<name>" online, "<name>, Connecting…" while it connects (a full-width comma in Chinese).
            let name = String(pill.label.split(whereSeparator: { $0 == "," || $0 == "\u{FF0C}" }).first ?? "")
                .trimmingCharacters(in: .whitespaces)
            XCTAssertFalse(name.isEmpty, "\(scheme): the pill has no name")
            RunLoop.current.run(until: Date().addingTimeInterval(2))
            let shot = Self.screenshot(of: app)
            save(shot, "screenshot-banner-name-on-\(scheme)")
            // The banner's strip: the screenshot's whole width (on its side wider than the app's frame says), down past
            // the bar.
            let strip = CGRect(x: 0, y: 0, width: 10_000, height: max(pill.frame.maxY + 16, 110))
            let lines = try Self.text(in: shot, strip)
            let words = lines.map(\.text)
            let said = try XCTUnwrap(Self.bannerName(lines), "\(scheme): no banner in the screenshot: \(words)")
            XCTAssertTrue(said.contains(name), "\(scheme): the banner says \(said), the bar \(name): \(words)")
            if name != "Persona" {
                XCTAssertFalse(said.contains("Pers"), "\(scheme): the banner says the persona's name: \(words)")
            }
        }
    }

    /// A system screenshot of the display the app is on: on iPhone Duo `XCUIScreen.main` is the outer display, black
    /// while the Duo is open, so the screen as large as the app's window (the open one's inner display).
    @MainActor private static func screenshot(of app: XCUIApplication) -> XCUIScreenshot {
        let size = app.frame.size
        let shots = XCUIScreen.screens.map { $0.screenshot() }
        func fits(_ s: XCUIScreenshot) -> Bool {
            let i = s.image.size
            return (abs(i.width - size.width) < 2 && abs(i.height - size.height) < 2)
                || (abs(i.width - size.height) < 2 && abs(i.height - size.width) < 2)
        }
        return shots.first(where: fits) ?? XCUIScreen.main.screenshot()
    }

    /// What the banner's name reads: the text before the repository on its row (Vision may read the two as one line),
    /// else the nearest line to the repository's left on that row. Beside the iPad's or the open Duo's column the
    /// column's head says the assistant's name in the same row, further left: not the banner's.
    private static func bannerName(_ lines: [(text: String, box: CGRect)]) -> String? {
        guard let repo = lines.first(where: { $0.text.contains(repository) }),
              let at = repo.text.range(of: repository) else { return nil }
        let before = repo.text[..<at.lowerBound].trimmingCharacters(in: .whitespaces)
        if before.count > 2 { return before }
        // GitHub's mark alone may read as a letter: not a name.
        return lines.filter { abs($0.box.midY - repo.box.midY) < repo.box.height && $0.box.maxX <= repo.box.minX + 0.01
            && $0.text.trimmingCharacters(in: .whitespaces).count > 1 }
            .max { $0.box.maxX < $1.box.maxX }?.text
    }

    /// The lines Vision reads in `rect` (points) of a screenshot, Chinese and English, each with its box (Vision's:
    /// normalized, from the bottom left).
    private static func text(in shot: XCUIScreenshot, _ rect: CGRect) throws -> [(text: String, box: CGRect)] {
        // Upright first: a screenshot of a display on its side (the open Duo's) keeps its pixels as they were and says
        // how to turn them.
        let raw = shot.image
        let format = UIGraphicsImageRendererFormat()
        format.scale = raw.scale
        let image = UIGraphicsImageRenderer(size: raw.size, format: format).image { _ in raw.draw(at: .zero) }
        let cg = try XCTUnwrap(image.cgImage)
        let scale = CGFloat(cg.width) / image.size.width
        let r = CGRect(x: rect.minX * scale, y: rect.minY * scale, width: rect.width * scale, height: rect.height * scale).integral
        let crop = try XCTUnwrap(cg.cropping(to: r.intersection(CGRect(x: 0, y: 0, width: cg.width, height: cg.height))))
        let request = VNRecognizeTextRequest()
        request.recognitionLevel = .accurate
        request.recognitionLanguages = ["zh-Hans", "en-US"]
        request.usesLanguageCorrection = false
        try VNImageRequestHandler(cgImage: crop).perform([request])
        return (request.results ?? []).compactMap { o in o.topCandidates(1).first.map { ($0.string, o.boundingBox) } }
    }

    /// Settings › Advanced › Appearance & Display has the switch (on: the UI tests pass it, a fresh install's default is
    /// `ScreenshotBannerPrefs.enabledByDefault`, ScreenshotBannerTests).
    @MainActor func testTheSwitch() throws {
        for scheme in ["light", "dark"] {
            let app = launch(["-ui.scheme", scheme, "-tab", "settings", "-app.tab", "settings"], banner: true)
            defer { app.terminate() }
            XCTAssertTrue(app.openSettingsAdvancedPage(), "no Settings › Advanced")
            let appearance = app.settingsAdvancedRow("settings-appearance")
            appearance.tap()
            let toggle = app.switches["settings-screenshot-banner"].firstMatch
            XCTAssertTrue(app.descendants(matching: .any)["settings-style"].firstMatch.waitForExistence(timeout: 15), "no Appearance & Display")
            for _ in 0..<6 where !toggle.isHittable { app.swipeUp() }
            XCTAssertTrue(toggle.exists, "no Persona Banner in Screenshots switch")
            XCTAssertEqual(toggle.value as? String, "1")
            save(app.screenshot(), "screenshot-banner-settings-\(scheme)")
        }
    }

    /// For a look (#10172): with the switch on, system screenshots of the Assistant chat at the default text size and
    /// the largest the bar takes (xxxLarge), with the mock's assistant name and a very long one, and of an Agent chat.
    /// Only with TEST_RUNNER_PERSONA_SHOTS: `screenshot-banner-looks-<look>-<page>-<L|XXXL>-<short|long>.png`. The look
    /// is the simulator's (`xcrun simctl ui <udid> appearance light|dark` before the run; TEST_RUNNER_PERSONA_LOOKS
    /// names it): the app's own Light / Dark switches its icon too, and the system's "icon changed" alert would cover
    /// the screenshots.
    @MainActor func testLooks() async throws {
        let env = ProcessInfo.processInfo.environment
        guard env["PERSONA_SHOTS"] != nil else { throw XCTSkip("screenshots for a look") }
        let appearance = env["PERSONA_LOOKS"] ?? "system"
        let hub = hub
        addTeardownBlock { _ = try? await Self.rename(hub, "助手") } // the mock's own name for it
        let pages: [(page: String, args: [String], control: String)] = [
            ("assistant", ["-openAssistant", Self.main], "assistant-new"),
            ("agent", ["-openSession", Self.agent, "-holdChromeOpen", "YES"], "chat.more"),
        ]
        for (label, name) in [("short", nil), ("long", "家庭生活、研究资料与日程整理全能助理")] as [(String, String?)] {
            if let name {
                let status = try await Self.rename(hub, name)
                XCTAssertEqual(status, 200, "the mock did not rename the assistant")
            }
            for p in pages {
                for (size, category) in [("L", "UICTContentSizeCategoryL"), ("XXXL", "UICTContentSizeCategoryXXXL")] {
                    try look(banner: true, scheme: "system", page: p.page,
                             p.args + ["-UIPreferredContentSizeCategoryName", category], control: p.control,
                             name: "screenshot-banner-looks-\(appearance)-\(p.page)-\(size)-\(label)", measure: false)
                }
            }
        }
    }

    /// Renames the hub's own assistant on the mock, as Settings › Managers does (`PATCH /api/v1/managers/{id}`).
    private static func rename(_ hub: String, _ name: String) async throws -> Int {
        var request = URLRequest(url: URL(string: hub + "/api/v1/managers/assistant")!)
        request.httpMethod = "PATCH"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONSerialization.data(withJSONObject: ["name": name])
        let (_, response) = try await URLSession.shared.data(for: request)
        return (response as? HTTPURLResponse)?.statusCode ?? 0
    }

    // MARK: -

    @MainActor private func launch(_ extra: [String], banner: Bool) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        let tab = extra.contains("-app.tab") ? [] : ["-app.tab", "assistant"]
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-openChatsFresh", "YES", "-ui.screenshotBanner", banner ? "YES" : "NO"]
            + tab + extra
        app.launchPinned()
        return app
    }

    /// The banner's repository (`PersonaBrandLockup.repository`): never in the accessibility tree.
    private static let repository = "nwverse/persona"
    /// The share of the control's square a screenshot with the banner draws otherwise than one with the bar: the bar's
    /// control is gone from it (the banner's repository and GitHub's mark stand about there now, #10172).
    private static let gone = 0.03

    /// A control's square in a screenshot: each pixel's luminance.
    struct Crop {
        var luma: [Int]
        /// The lightest pixel less the darkest: a button has edges, a plain backdrop has none.
        var spread: Int { (luma.max() ?? 0) - (luma.min() ?? 0) }
        /// The share of pixels more than a sixth of the range away from `other`'s (a square of the same size).
        func changed(from other: Crop) -> Double {
            guard luma.count == other.luma.count, !luma.isEmpty else { return 1 }
            return Double(zip(luma, other.luma).filter { abs($0 - $1) > 42 }.count) / Double(luma.count)
        }
    }

    /// Opens the page, checks its bar is on screen (it takes taps, VoiceOver reaches it) and the banner is not in the
    /// accessibility tree, takes a system screenshot and returns the control's square in it. `measure: false` only
    /// waits for the control and keeps the screenshot (at xxxLarge the Assistant bar lays New Chat out of reach).
    @MainActor @discardableResult
    private func look(banner: Bool, scheme: String, page: String, _ extra: [String], control id: String,
                      tapThrough: String? = nil, name: String? = nil, measure: Bool = true) throws -> Crop {
        let app = launch(["-ui.scheme", scheme] + extra, banner: banner)
        defer { app.terminate() }
        let el = app.descendants(matching: .any)[id].firstMatch
        XCTAssertTrue(el.waitForExistence(timeout: 30), "\(page): no \(id)")
        if measure { XCTAssertTrue(el.isHittable, "\(page): \(id) is on screen") }
        XCTAssertFalse(app.staticTexts[Self.repository].exists, "\(page): the banner is out of VoiceOver's reach")
        RunLoop.current.run(until: Date().addingTimeInterval(2))
        let shot = Self.screenshot(of: app)
        save(shot, name ?? "screenshot-banner-\(page)-\(banner ? "on" : "off")-\(scheme)")
        guard measure else { return Crop(luma: []) }
        let crop = try Self.crop(shot, el.frame.insetBy(dx: 4, dy: 4))
        if let tapThrough {
            // The bar in the capture layer still takes its taps.
            el.tap()
            XCTAssertTrue(app.descendants(matching: .any)[tapThrough].firstMatch.waitForExistence(timeout: 10), "\(page): \(id) took no tap")
        }
        return crop
    }

    private static func crop(_ shot: XCUIScreenshot, _ rect: CGRect) throws -> Crop {
        let image = shot.image
        let cg = try XCTUnwrap(image.cgImage)
        let scale = CGFloat(cg.width) / image.size.width
        let r = CGRect(x: rect.minX * scale, y: rect.minY * scale, width: rect.width * scale, height: rect.height * scale).integral
        let crop = try XCTUnwrap(cg.cropping(to: r))
        var px = [UInt8](repeating: 0, count: crop.width * crop.height * 4)
        let ctx = try XCTUnwrap(CGContext(data: &px, width: crop.width, height: crop.height, bitsPerComponent: 8, bytesPerRow: crop.width * 4,
                                          space: CGColorSpaceCreateDeviceRGB(), bitmapInfo: CGImageAlphaInfo.premultipliedLast.rawValue))
        ctx.draw(crop, in: CGRect(x: 0, y: 0, width: crop.width, height: crop.height))
        // Typed parts: with Vision imported, the one-line sum timed out Xcode 26's type-checker (#13374).
        return Crop(luma: stride(from: 0, to: px.count, by: 4).map { (i: Int) -> Int in
            let r: Int = Int(px[i]) * 299
            let g: Int = Int(px[i + 1]) * 587
            let b: Int = Int(px[i + 2]) * 114
            return (r + g + b) / 1000
        })
    }

    @MainActor private func save(_ shot: XCUIScreenshot, _ name: String) {
        let a = XCTAttachment(screenshot: shot)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
            try? shot.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent("\(name).png"))
        }
    }
}
