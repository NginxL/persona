import UIKit
import XCTest

/// A recording's Summary on iPhone (#5026, docs/apple/recording.md「Minutes」): the made-up finished meeting
/// (`-showMeetingSample minutes`) against a mock hub (PERSONA_MOCK_HUB) that writes made-up minutes from its sentences
/// (`POST /mock/recording_minutes {"mode":"ready"}`): two key points, a decision at 0:44, an action item with an owner
/// and a due date, and an open question. PERSONA_SHOTS=<dir> saves cards-<appearance>-summary / -context / -transcript,
/// at xxxLarge (the interface's cap) and at the largest text size -summary-cap / -context-cap / -summary-largest /
/// -context-largest, and a cited photo's card -photo (PERSONA_SHOTS_APPEARANCE names the appearance; run once in light
/// and once in dark).
final class MinutesCardsUITests: XCTestCase {
    private let cards = ["minutes-card-points", "minutes-card-decisions", "minutes-card-open", "minutes-card-actions"]

    /// The switch defaults to Summary with the four cards; an item opens its transcript lines in place; its time switches
    /// to Transcript with that line lit and in view; the switch goes back to the Summary.
    @MainActor func testSummaryCardsAndTheTranscriptOneTapAway() throws {
        let app = try openSummary()
        let control = app.segmentedControls["recording-view-switch"].firstMatch
        XCTAssertTrue(app.buttons["minutes-copy"].exists && app.buttons["minutes-share"].exists, app.debugDescription)
        // The switch takes the minutes' sections' place: no transcript under the cards.
        XCTAssertFalse(element(app, "recording-sentence").exists, "the transcript shows under the Summary:\n\(app.debugDescription)")
        // Below the switch (at the accessibility sizes a screen or more further down).
        if !element(app, cards[0]).waitForExistence(timeout: 5) { reveal(element(app, cards[0]), in: app) }
        XCTAssertTrue(element(app, cards[0]).exists, app.debugDescription)
        try shot(app, "summary")
        // Every card, scrolled into view one by one (rows off the screen are not in the tree).
        for id in cards {
            let card = element(app, id)
            reveal(card, in: app)
            XCTAssertTrue(card.exists, "no \(id):\n\(app.debugDescription)")
        }
        XCTAssertTrue(app.staticTexts["张三 · 周五前"].firstMatch.exists, "the action item has no owner and date:\n\(app.debugDescription)")
        back(to: control, in: app)

        // The decision opens, in place, to the transcript lines around 0:44: the line before it and the two after it.
        let decisions = element(app, "minutes-card-decisions")
        let disclosure = decisions.buttons["minutes-item-context"].firstMatch
        reveal(disclosure, in: app)
        XCTAssertTrue(disclosure.isHittable, app.debugDescription)
        disclosure.tap()
        let lines = decisions.descendants(matching: .any).matching(identifier: "minutes-item-lines").firstMatch
        XCTAssertTrue(lines.waitForExistence(timeout: 3), app.debugDescription)
        for words in ["锁屏和灵动岛要能看到时长", "决定：默认用中文转写", "张三负责周五前", "还没定的是 Mac"] {
            let line = lines.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
            XCTAssertTrue(line.exists, "no line \(words) under the decision:\n\(lines.debugDescription)")
        }
        XCTAssertFalse(lines.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@", "下周同一时间")).firstMatch.exists,
                       "more lines than the decision's neighbours")
        XCTAssertEqual(disclosure.value as? String, "已展开")
        XCTAssertFalse(element(app, "recording-sentence").exists, "opening an item left the Summary")
        try shot(app, "context")

        // Its time: the Transcript, that line lit and on the screen.
        let time = decisions.buttons["minutes-time"].firstMatch
        reveal(time, in: app)
        time.tap()
        let lit = element(app, "recording-sentence-lit")
        XCTAssertTrue(lit.waitForExistence(timeout: 3), app.debugDescription)
        // Lit as the transcript appears; the scroll to it follows on the next turn.
        XCTAssertTrue(hittable(lit, within: 2), "the line is not in view")
        XCTAssertTrue(app.staticTexts["决定：默认用中文转写，中英混说也要能用。"].firstMatch.isHittable, "the decision's line is not in view")
        XCTAssertFalse(element(app, cards[0]).exists, "the cards stay over the transcript")
        try shot(app, "transcript")

        // And back: the Summary again, the transcript gone.
        back(to: control, in: app)
        XCTAssertTrue(segment(control, "recording-view-transcript", 1).isSelected, app.debugDescription)
        segment(control, "recording-view-summary", 0).tap()
        // The rows under it change: at the accessibility sizes the list can move the switch off the screen.
        back(to: control, in: app)
        XCTAssertTrue(segment(control, "recording-view-summary", 0).isSelected, app.debugDescription)
        let card = element(app, cards[0])
        reveal(card, in: app)
        XCTAssertTrue(card.exists, app.debugDescription)
        XCTAssertFalse(element(app, "recording-sentence").exists, "the transcript stays under the Summary")
    }

    /// At the largest text size (AX5) an item's words keep growing while its time and disclosure stay as at xxxLarge, where
    /// #6813 caps the interface (`docs/design/DESIGN_SYSTEM.md` 6.2: the minutes are reading content, the time chip and the
    /// disclosure a card's controls). At xxxLarge itself (`cap`, the first launch) the time sits after the words, beside
    /// them and not under them, with the disclosure after it. At AX5 the time and the disclosure are as big as there, side
    /// by side and on the screen, and the words are taller than there. Where the pair sits at AX5 (beside the words as at
    /// xxxLarge, or under them) is left to #6448's follow-up. The disclosure opens the lines around the item, and every card
    /// is there.
    @MainActor func testLargestTextSizeCapsTheTimeAndGrowsTheWords() throws {
        var atTheCap: (time: CGSize, disclosure: CGSize, words: CGFloat)?
        for look in ["cap", "largest"] {
            let app = try openSummary(Self.textSize(look))
            try shot(app, "summary-\(look)")
            let decisions = element(app, "minutes-card-decisions")
            let words = decisions.staticTexts["决定：默认用中文转写，中英混说也要能用"].firstMatch
            let time = decisions.buttons["minutes-time"].firstMatch
            let disclosure = decisions.buttons["minutes-item-context"].firstMatch
            reveal(disclosure, in: app)
            let window = app.windows.firstMatch.frame
            XCTAssertTrue(words.exists && window.contains(time.frame) && window.contains(disclosure.frame),
                          "\(look): the decision's time and disclosure aren't on the screen:\n\(app.debugDescription)")
            XCTAssertGreaterThanOrEqual(disclosure.frame.minX, time.frame.maxX - 1, "\(look): the disclosure isn't after the time")
            XCTAssertLessThan(abs(disclosure.frame.midY - time.frame.midY), 1, "\(look): the disclosure isn't beside the time")
            if let capped = atTheCap {
                XCTAssertEqual(time.frame.width, capped.time.width, accuracy: 0.5, "\(look): the time is \(time.frame.size), \(capped.time) at xxxLarge")
                XCTAssertEqual(time.frame.height, capped.time.height, accuracy: 0.5, "\(look): the time is \(time.frame.size), \(capped.time) at xxxLarge")
                XCTAssertEqual(disclosure.frame.width, capped.disclosure.width, accuracy: 0.5,
                               "\(look): the disclosure is \(disclosure.frame.size), \(capped.disclosure) at xxxLarge")
                XCTAssertEqual(disclosure.frame.height, capped.disclosure.height, accuracy: 0.5,
                               "\(look): the disclosure is \(disclosure.frame.size), \(capped.disclosure) at xxxLarge")
                XCTAssertGreaterThan(words.frame.height, capped.words + 1,
                                     "\(look): the words are \(words.frame.height) pt tall, \(capped.words) pt at xxxLarge: they stopped at the cap")
            } else {
                XCTAssertGreaterThanOrEqual(time.frame.minX, words.frame.maxX - 1, "\(look): the time isn't after the words")
                XCTAssertLessThan(time.frame.minY, words.frame.maxY - 1, "\(look): the time is under the words")
                atTheCap = (time.frame.size, disclosure.frame.size, words.frame.height)
            }
            XCTAssertTrue(disclosure.isHittable, app.debugDescription)
            disclosure.tap()
            XCTAssertTrue(decisions.descendants(matching: .any).matching(identifier: "minutes-item-lines").firstMatch.waitForExistence(timeout: 3),
                          app.debugDescription)
            try shot(app, "context-\(look)")
            for id in cards {
                let card = element(app, id)
                reveal(card, in: app)
                XCTAssertTrue(card.exists, "no \(id) at \(look):\n\(app.debugDescription)")
            }
            app.terminate()
        }
    }

    /// A key point that cites a meeting photo (#3862 phase 3): the Summary's card shows the photo's thumbnail under the
    /// item, as the minutes' sections did, and a tap opens it full screen. The photo goes to the mock hub as the phone
    /// would send it, the mock writes the minutes again citing it, and the page reads them on the next launch.
    @MainActor func testCitedPhotoInTheSummary() throws {
        var app = try openSummary()
        let hub = try mockHub()
        try addPhoto(hub, caption: "白板：录音入口的三步")
        app.terminate()
        app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "minutes"]
        app.launchPinned()
        _ = findSwitch(app, searching: false)
        let points = element(app, "minutes-card-points")
        let photo = points.descendants(matching: .any).matching(identifier: "minutes-photo").firstMatch
        reveal(photo, in: app)
        XCTAssertTrue(photo.waitForExistence(timeout: 10), "no cited photo in the Key Points card:\n\(app.debugDescription)")
        XCTAssertTrue(points.staticTexts["白板：录音入口的三步"].firstMatch.exists, app.debugDescription)
        XCTAssertFalse(element(app, "minutes-photo-removed").exists, "a listed photo reads Photo removed")
        sleep(1) // the thumbnail's picture, read from the hub
        try shot(app, "photo")
        photo.tap()
        let viewer = element(app, "meeting-photo-viewer")
        XCTAssertTrue(viewer.waitForExistence(timeout: 10), "the photo didn't open:\n\(app.debugDescription)")
    }

    // MARK: Helpers

    /// `POST /api/v1/recordings/{id}/photos` for the made-up meeting: a small PNG with `caption`, at 0:30.
    @MainActor private func addPhoto(_ hub: String, caption: String) throws {
        let side = CGSize(width: 320, height: 240)
        let png = UIGraphicsImageRenderer(size: side, format: { let f = UIGraphicsImageRendererFormat(); f.scale = 1; return f }()).pngData { context in
            UIColor.white.setFill()
            context.fill(CGRect(origin: .zero, size: side))
            UIColor.systemBlue.setStroke()
            for row in 0..<3 {
                let box = UIBezierPath(roundedRect: CGRect(x: 24, y: 24 + CGFloat(row) * 68, width: 272, height: 52), cornerRadius: 8)
                box.lineWidth = 4
                box.stroke()
            }
        }
        var request = URLRequest(url: URL(string: hub + "/api/v1/recordings/00000000-0000-0000-0000-000000000003/photos")!)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        request.httpBody = try JSONSerialization.data(withJSONObject: [
            "id": UUID().uuidString.lowercased(), "at": 30, "mime": "image/png", "data": png.base64EncodedString(), "caption": caption,
        ])
        var status = 0
        let done = expectation(description: "photo")
        URLSession.shared.dataTask(with: request) { _, response, _ in
            status = (response as? HTTPURLResponse)?.statusCode ?? 0
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 10)
        XCTAssertEqual(status, 200, "the mock didn't take the photo")
    }

    /// The made-up meeting's page once its minutes are ready: uploaded, written by the hub, read again (every 15 s while
    /// it waits), and on Summary, the default.
    @MainActor private func openSummary(_ extra: [String] = []) throws -> XCUIApplication {
        let hub = try mockHub()
        try minutesMode(hub, "ready")
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-showMeetingSample", "minutes"] + extra
        app.launchPinned()
        let control = findSwitch(app, searching: !extra.isEmpty)
        XCTAssertTrue(segment(control, "recording-view-summary", 0).isSelected, "the default isn't Summary:\n\(app.debugDescription)")
        return app
    }

    /// The launch arguments for a text size: the system's, with the app's own Text Size at Standard so that only the
    /// system's counts. `largest` AX5; `cap` xxxLarge, where #6813 caps the interface.
    private static func textSize(_ look: String) -> [String] {
        let size = look == "largest" ? "AccessibilityXXXL" : "XXXL"
        return ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategory\(size)", "-ui.textSize", "standard"]
    }

    /// The page's switch once the minutes are ready. At the accessibility sizes (the launch's, `searching`, or the
    /// simulator's own) the page's header can push it below the screen, and rows off it are not in the tree: look a
    /// screen further down, then come back up, while the minutes are on their way. At the default size it is waited for
    /// where it shows, the page left as it opened.
    @MainActor private func findSwitch(_ app: XCUIApplication, searching: Bool) -> XCUIElement {
        let control = app.segmentedControls["recording-view-switch"].firstMatch
        if searching || !control.waitForExistence(timeout: 45) {
            let deadline = Date.now.addingTimeInterval(45)
            while !control.exists, Date.now < deadline {
                app.swipeUp()
                if control.exists { break }
                app.swipeDown()
                sleep(1)
            }
        }
        XCTAssertTrue(control.exists, app.debugDescription)
        return control
    }

    /// A segment of the switch: by its identifier where SwiftUI carries it to the segment, else by its place.
    @MainActor private func segment(_ control: XCUIElement, _ id: String, _ index: Int) -> XCUIElement {
        let named = control.buttons[id].firstMatch
        return named.exists ? named : control.buttons.element(boundBy: index)
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    /// Brings `element` on screen: down the page first, then back up if it was passed.
    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        var swipes = 0
        while !(element.exists && element.isHittable), swipes < 6 { app.swipeUp(); swipes += 1 }
        while !(element.exists && element.isHittable), swipes < 14 { app.swipeDown(); swipes += 1 }
    }

    /// Whether `element` is on the screen to be tapped within `seconds`.
    @MainActor private func hittable(_ element: XCUIElement, within seconds: TimeInterval) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        repeat {
            if element.exists && element.isHittable { return true }
            usleep(200_000)
        } while Date.now < deadline
        return false
    }

    /// Back up to the switch at the top of the page.
    @MainActor private func back(to control: XCUIElement, in app: XCUIApplication) {
        var swipes = 0
        // Up to a dozen screens: the cards run that long at the accessibility sizes.
        while !(control.exists && control.isHittable), swipes < 12 { app.swipeDown(); swipes += 1 }
        // At the page's top it is below the screen there (the list goes back to its top as the rows under the switch
        // change): down again, slowly.
        while !(control.exists && control.isHittable), swipes < 20 { app.swipeUp(velocity: .slow); swipes += 1 }
        XCTAssertTrue(control.exists, "no switch:\n\(app.debugDescription)")
    }

    private func minutesMode(_ hub: String, _ mode: String) throws {
        var request = URLRequest(url: URL(string: hub + "/mock/recording_minutes")!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: ["mode": mode])
        let done = expectation(description: "mode")
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/cards-\(appearance)-\(name).png"))
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }
}
