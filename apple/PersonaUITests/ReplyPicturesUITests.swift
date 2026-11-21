import XCTest

/// Pictures in a reply fit the text (#9474): a lone one is one height and its shape's width, runs are squares (the
/// sixth counting the rest), one that did not load is a small tile, and a tap opens the viewer on all of the reply's.
///   PORT=18961 MOCK_AGENTS=1 MOCK_REPLY_IMAGES=1 python3 scripts/mock/mock_hub.py &
///   TEST_RUNNER_PERSONA_MOCK_HUB=http://127.0.0.1:18961 xcodebuild test … -only-testing:PersonaUITests/ReplyPicturesUITests
/// Light, then dark.
final class ReplyPicturesUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_AGENTS=1 MOCK_REPLY_IMAGES=1 (PERSONA_MOCK_HUB)")
        }
    }

    @MainActor private func shot(_ name: String, _ scheme: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "reply-pictures \(name) \(UITestStyle.name) \(scheme)"
        a.lifetime = .keepAlways
        add(a)
    }

    @MainActor private func picture(_ label: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: "reply-picture").matching(NSPredicate(format: "label == %@", label)).firstMatch
    }

    /// Within a point of `want` (frames come back in points, rounded by the screen's scale).
    private func near(_ got: CGFloat, _ want: CGFloat) -> Bool { abs(got - want) <= 1 }

    @MainActor func testPicturesFitTheReplyAndOpenTogether() {
        for scheme in ["light", "dark"] { check(scheme) }
    }

    @MainActor private func check(_ scheme: String) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000008", "-ui.scheme", scheme]
        app.launchPinned()
        acceptIconAlert() // the icon follows the scheme

        // The reply's end: the run of eight (six squares, the sixth counting two more) and the one that failed.
        let failed = app.descendants(matching: .any)["reply-picture-failed"]
        XCTAssertTrue(failed.waitForExistence(timeout: 30), "the picture the hub could not fetch did not become a tile")
        XCTAssertTrue(near(failed.frame.width, 80) && near(failed.frame.height, 80), "failed tile \(failed.frame)")
        let more = app.descendants(matching: .any)["reply-picture-more"]
        XCTAssertTrue(more.waitForExistence(timeout: 10), "no +N on the sixth square")
        XCTAssertTrue(near(more.frame.width, more.frame.height), "the counted square is not square: \(more.frame)")
        sleep(2)
        shot("end", scheme)

        // Up the reply: the lone pictures are one height, as wide as their shape.
        let kitchen = picture("厨房")
        for _ in 0 ..< 6 where !kitchen.isHittable { app.swipeDown() }
        XCTAssertTrue(kitchen.waitForExistence(timeout: 10), "no kitchen picture")
        XCTAssertTrue(near(kitchen.frame.height, 240) && near(kitchen.frame.width, 180), "portrait \(kitchen.frame), want 180 × 240")
        let room = picture("客厅")
        XCTAssertTrue(room.exists && near(room.frame.height, 240) && near(room.frame.width, 320), "landscape \(room.frame), want 320 × 240")
        let building = picture("楼"), street = picture("街道")
        XCTAssertTrue(building.exists && street.exists, "the pair is not drawn")
        XCTAssertTrue(near(building.frame.width, building.frame.height) && near(building.frame.width, street.frame.width)
            && near(building.frame.minY, street.frame.minY), "the pair is not two squares side by side: \(building.frame) \(street.frame)")
        sleep(1)
        shot("lone", scheme)

        // A tap opens the viewer on every picture of the reply (14), at the one tapped.
        room.tap()
        let title = app.descendants(matching: .any)["viewer-title"]
        XCTAssertTrue(title.waitForExistence(timeout: 10), "the viewer did not open")
        XCTAssertTrue(title.label.contains("4 / 14"), "viewer at \(title.label), want 4 / 14")
        sleep(2)
        shot("viewer", scheme)
    }
}

/// DSH Markdown targets on the session machine (#8953), against Fixtures/markdown_local_files_hub.py. The four
/// appearance runs are also AC5's screenshot evidence: light/dark at the default and largest accessibility text sizes.
final class MarkdownLocalFilesUITests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication!
    private let missingReason = "does not exist"
    private let deniedReason = "Permission denied"
    private let unreadableReason = "could not be read"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MARKDOWN_LOCAL_HUB"] else {
            throw XCTSkip("needs Fixtures/markdown_local_files_hub.py (PERSONA_MARKDOWN_LOCAL_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(scheme: String = "light", size: String? = nil) -> XCUIApplication {
        app?.terminate()
        let app = XCUIApplication()
        self.app = app
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-tab", "dev",
                               "-openSession", "studio/session-3f2a9c4e-0000-4000-8000-000000000003",
                               "-openAgentsFresh", "YES", "-openChatsFresh", "YES",
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-ui.scheme", scheme]
            + (size.map { ["-UIPreferredContentSizeCategoryName", $0] } ?? [])
        app.launchPinned()
        acceptIconAlert()
        return app
    }

    @MainActor private func picture(_ label: String, failed: Bool = false) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: failed ? "reply-picture-failed" : "reply-picture")
            .matching(NSPredicate(format: "label == %@", label)).firstMatch
    }

    @MainActor private func reason(_ words: String) -> XCUIElement {
        app.descendants(matching: .any)
            .matching(NSPredicate(format: "label CONTAINS %@ OR value CONTAINS %@", words, words)).firstMatch
    }

    @MainActor private func reveal(_ element: XCUIElement, _ what: String) -> XCUIElement {
        for _ in 0..<12 where !(element.exists && element.isHittable) { app.swipeDown() }
        for _ in 0..<6 where !(element.exists && element.isHittable) { app.swipeUp() }
        XCTAssertTrue(element.waitForExistence(timeout: 15) && element.isHittable, "no \(what):\n\(app.debugDescription)")
        return element
    }

    @MainActor private func shot(_ name: String, scheme: String, size: String) {
        let png = app.screenshot().pngRepresentation
        let attachment = XCTAttachment(data: png, uniformTypeIdentifier: "public.png")
        attachment.name = "markdown-local-\(name)-\(scheme)-\(size)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let directory = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? png.write(to: URL(fileURLWithPath: directory)
            .appending(path: "markdown-local-\(name)-\(scheme)-\(size).png"))
    }

    @MainActor func testLocalPNGsAndFailureReasonsInLightDarkAndTextSizes() {
        let sizes = [("default", nil), ("ax5", "UICTContentSizeCategoryAccessibilityXXXL")] as [(String, String?)]
        for scheme in ["light", "dark"] {
            for (tag, size) in sizes {
                _ = launch(scheme: scheme, size: size)
                let absolute = reveal(picture("Absolute screenshot"), "absolute local PNG")
                XCTAssertGreaterThan(absolute.frame.width, 0)
                let relative = reveal(picture("Relative screenshot"), "cwd-relative local PNG")
                XCTAssertGreaterThan(relative.frame.width, 0)
                shot("valid", scheme: scheme, size: tag)

                for (alt, words) in [("Missing screenshot", missingReason), ("Denied screenshot", deniedReason),
                                     ("Unreadable screenshot", unreadableReason), ("Inline denied screenshot", deniedReason),
                                     ("Lone denied screenshot", deniedReason)] {
                    _ = reveal(picture(alt, failed: true), alt + " failure tile")
                    XCTAssertTrue(reason(words).waitForExistence(timeout: 10), "\(alt) has no reason (\(words)):\n\(app.debugDescription)")
                }
                shot("failures", scheme: scheme, size: tag)
            }
        }
    }

    @MainActor private func readCount(path: String, cwd: String) async throws -> Int {
        var components = try XCTUnwrap(URLComponents(string: hub + "/mock/markdown-local-reads"))
        components.queryItems = [URLQueryItem(name: "path", value: path), URLQueryItem(name: "cwd", value: cwd)]
        let data = try await URLSession.shared.data(from: try XCTUnwrap(components.url)).0
        let body = try JSONSerialization.jsonObject(with: data) as? [String: Any]
        return try XCTUnwrap(body?["count"] as? Int)
    }

    @MainActor func testFailedLocalImageRetriesOnlyWhenTapped() async throws {
        _ = launch()
        let failed = reveal(picture("Missing screenshot", failed: true), "missing local image")
        XCTAssertTrue(reason(missingReason).waitForExistence(timeout: 10), app.debugDescription)
        let first = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        sleep(1)
        let unchanged = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        XCTAssertEqual(unchanged, first, "a failed image retried without a tap")
        for _ in 0..<8 { app.swipeUp() }
        for _ in 0..<8 { app.swipeDown() }
        _ = reveal(failed, "missing local image after scrolling away and back")
        let reappeared = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        XCTAssertEqual(reappeared, first, "a recreated failure tile retried without a tap")
        failed.tap()
        sleep(2)
        let retried = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        XCTAssertGreaterThan(retried, first, "the tap did not retry the machine-file read")
        sleep(1)
        let settled = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        XCTAssertEqual(settled, retried, "the retry kept repeating without another tap")
    }

    @MainActor func testFullScreenLocalFailureReasonAndRetry() async throws {
        _ = launch()
        let absolute = reveal(picture("Absolute screenshot"), "absolute local PNG")
        absolute.tap()
        XCTAssertTrue(app.buttons["viewer-close"].waitForExistence(timeout: 15), "the full-screen viewer did not open")
        let page = app.windows.firstMatch
        for _ in 0..<2 { page.swipeLeft(velocity: .fast) }
        XCTAssertTrue(reason(missingReason).waitForExistence(timeout: 15), "the full-screen failure lost its reason")
        let first = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        let retry = app.buttons["Retry"].firstMatch
        XCTAssertTrue(retry.waitForExistence(timeout: 5), "the full-screen local failure cannot retry")
        retry.tap()
        sleep(2)
        let retried = try await readCount(path: "shots/missing.png", cwd: "/Users/me/code/reports")
        XCTAssertGreaterThan(retried, first, "the full-screen Retry did not reread the file")
        app.buttons["viewer-close"].tap()
    }

    @MainActor func testLocalLinksOpenTheFileViewerAndWebLinksStayInApp() {
        _ = launch()
        for (label, body) in [("Absolute report", "Absolute report body."), ("Relative report", "Relative report body.")] {
            reveal(app.links[label].firstMatch, label + " link").tap()
            XCTAssertTrue(app.descendants(matching: .any)["file-markdown"].waitForExistence(timeout: 20),
                          "\(label) did not open the file viewer:\n\(app.debugDescription)")
            XCTAssertTrue(app.staticTexts[body].waitForExistence(timeout: 10), "wrong file for \(label)")
            app.buttons["file-close"].tap()
        }

        for (label, words) in [("Missing report", missingReason), ("Denied report", deniedReason),
                               ("Unreadable report", unreadableReason)] {
            reveal(app.links[label].firstMatch, label + " link").tap()
            XCTAssertTrue(reason(words).waitForExistence(timeout: 20), "\(label) silently did nothing:\n\(app.debugDescription)")
            XCTAssertTrue(app.buttons["file-close"].exists, "\(label) did not use the file viewer")
            app.buttons["file-close"].tap()
        }

        reveal(app.links["Web report"].firstMatch, "HTTPS link").tap()
        XCTAssertTrue(app.buttons["link-close"].waitForExistence(timeout: 15), "HTTPS no longer opens in-app")
        XCTAssertFalse(app.buttons["file-close"].exists, "HTTPS became a machine file")
    }
}
