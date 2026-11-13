import XCTest

/// The live view on the iPhone (#3862 phase 2; contract docs/assistant/meeting-minutes.md「Phase 2: live view」, the phone's
/// side docs/apple/recording.md「Live view (phase 2)」), end to end against a mock hub (PERSONA_MOCK_HUB): a room meeting
/// started with the live view shows the room's topics on its page once the room set them after a batch of lines, and its
/// Live Activity says the current topic; a hub without the live view shows none. A made-up meeting stands in for the
/// microphone (`-showMeetingSample room-taking-part`): its lines reach the hub as a real one's do, and the mock's room sets
/// made-up topics after each batch (`meeting_topics_set`). Every test starts from a fresh mock (`POST
/// /mock/recording_minutes`), with `/mock/meeting_live_view` switching the phase on or off, and the highlights (phase 2b,
/// `/mock/meeting_highlights`) off, so the page keeps phase 2's Live box (`MeetingHighlightsUITests` covers them, and a
/// meeting started without them on a hub with them); every call to the mock must answer 2xx. The app runs in zh-Hans:
/// elements are found by their identifiers. PERSONA_SHOTS=<dir> saves live-<appearance>-<name>.png.
final class MeetingLiveViewUITests: XCTestCase {
    private let live = "00000000-0000-0000-0000-000000000002"

    override func setUpWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/recording_minutes", ["mode": "pending"])
        try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "participant", "answer": "ok", "kb_read_only": false])
        try post(hub, "/mock/meeting_live_view", ["on": true])
        try post(hub, "/mock/meeting_highlights", ["on": false])
        // A voice extension from before phase 4 (on a MOCK_VOICE=1 mock): these questions go through `…/ask`, not a
        // meeting call's `text` frames (MeetingTalkUITests covers those). A mock without MOCK_VOICE has no such route.
        _ = try? Data(contentsOf: URL(string: hub + "/mock/voice_meeting?meeting=off")!)
    }

    override func tearDownWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "off"])
        try post(hub, "/mock/meeting_live_view", ["on": true])
        try post(hub, "/mock/meeting_highlights", ["on": true])
    }

    /// The room sets topics after a batch of the meeting's lines: the page shows them as chips under its header, the
    /// current one among them, as the hub has them; the Live Activity says "Now: <topic>" on the Lock Screen after the
    /// role, within its height even for the longest topic beside an answer.
    @MainActor func testTopicsShowOnThePageAndTheLockScreen() throws {
        let hub = try mockHub()
        let app = try meetingPage(hub)
        let topics = element(app, "meeting-topics")
        XCTAssertTrue(topics.waitForExistence(timeout: 40),
                      "no topics on the page; the hub has \(String(describing: try? self.topics(hub, self.live))):\n\(app.debugDescription)")
        let stored = try self.topics(hub, live)
        XCTAssertFalse(stored.isEmpty, "the page shows topics the hub doesn't have")
        let current = stored.first { $0["state"] as? String == "current" }
        if current != nil {
            XCTAssertTrue(element(app, "meeting-topic-current").waitForExistence(timeout: 10), "no current topic:\n\(app.debugDescription)")
        }
        XCTAssertTrue(element(app, "meeting-live").exists, "the Live box stays under the topics")
        try shot(app, "topics")
        guard current != nil else { return }

        // The longest topic, in Chinese (the hub's 80 characters; the activity carries 60), and an answer that would take
        // two lines: the Lock Screen keeps the role first, the topic cut after a second line and the answer to one, so it
        // stays within its ~160 pt (docs/apple/live-activity.md「Pitfalls」). The room sets the topics as its tool does;
        // the answer is a typed question's, the third line for a minute.
        let set: [[String: Any]] = [
            ["title": "录音入口", "state": "done", "at": 1],
            ["title": Self.longTopic, "state": "current", "at": 741],
            ["title": "下一步", "state": "open"],
        ]
        try post(hub, "/mock/meeting_topics", ["id": live, "topics": set])
        let now = element(app, "meeting-topic-current")
        let begins = String(Self.longTopic.prefix(12))
        XCTAssertTrue(waitFor(20) { now.exists && now.label.contains(begins) }, "the page never showed the room's new topic:\n\(app.debugDescription)")
        try shot(app, "topics-long")
        let answers = app.descendants(matching: .any).matching(identifier: "meeting-thread-answer")
        let before = answers.count
        try ask(app, "上次评审定的导出上限是多少？")
        XCTAssertTrue(waitFor(30) { answers.count > before }, "no answer:\n\(app.debugDescription)")
        let title = Self.longTopic

        // The Lock Screen: the current topic under the name (the activity follows the page's read of the topics).
        XCUIDevice.shared.press(.home)
        sleep(3)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        springboard.coordinate(withNormalizedOffset: .zero).withOffset(CGVector(dx: 201, dy: 37)).press(forDuration: 1.5)
        sleep(2)
        try screen("island-expanded")
        XCUIDevice.shared.press(.home)
        sleep(1)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(2)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        for label in ["Allow", "允许", "Always Allow", "始终允许"] {
            let b = springboard.buttons[label].firstMatch
            if b.exists { b.tap(); sleep(2) }
        }
        // The activity carries the first 60 characters, and the line shows fewer: its beginning is enough.
        let topic = springboard.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", String(title.prefix(12)))).firstMatch
        let found = topic.waitForExistence(timeout: 10)
        try screen("lock")
        // Unlock (swipe up; no passcode on the simulator): a locked simulator hangs the next test run.
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(1)
        XCTAssertTrue(found, "the Lock Screen doesn't say the current topic \(title):\n\(springboard.debugDescription)")
    }

    /// A hub without the live view (`{"on": false}`): `GET …/live` carries no topics, and the page shows none, only the
    /// phone's own lines in the Live box.
    @MainActor func testAHubWithoutTheLiveViewShowsNoTopics() throws {
        let hub = try mockHub()
        try post(hub, "/mock/meeting_live_view", ["on": false])
        let app = try meetingPage(hub)
        XCTAssertTrue(element(app, "meeting-live").waitForExistence(timeout: 10), app.debugDescription)
        // Its lines reached the room (a batch went), and still no topics.
        XCTAssertTrue(waitFor(20) { ((try? self.liveRead(hub, self.live))?["room"] as? [String: Any])?["state"] as? String == "ready" })
        sleep(5)
        XCTAssertFalse(element(app, "meeting-topics").exists, "topics from a hub without the live view:\n\(app.debugDescription)")
        try shot(app, "no-topics")
    }

    /// A current topic at the hub's longest (80 characters), in Chinese.
    private static let longTopic = "下季度企业版定价方案：按席位还是按用量收费，老客户迁移折扣与合同续签时间表，销售团队培训计划、渠道伙伴返点政策和年度预算审批流程的最终确认，以及各项工作的负责人"

    // MARK: Helpers: the app

    /// Types `text` into the page's composer and sends it (to the room: the answer is Persona's, which the activity shows).
    @MainActor private func ask(_ app: XCUIApplication, _ text: String) throws {
        let field = element(app, "meeting-ask-field")
        XCTAssertTrue(field.waitForExistence(timeout: 10), app.debugDescription)
        XCTAssertTrue(waitFor(15) { field.isEnabled }, "the composer is off: \(app.debugDescription)")
        field.tap()
        field.typeText(text)
        let send = app.buttons["meeting-ask-send"].firstMatch
        XCTAssertTrue(send.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(waitFor(15) { send.isEnabled }, "Send stays off: \(app.debugDescription)")
        send.tapCenter()
    }

    @MainActor private func meetingPage(_ hub: String) throws -> XCUIApplication {
        let app = XCUIApplication()
        // `-openChatsFresh`: the mock makes the room under the same id for the same made-up recording in every test.
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings", "-openChatsFresh", "YES",
                               "-showMeetingSample", "room-taking-part"]
        app.launchPinned()
        XCTAssertTrue(element(app, "meeting-thread-header").waitForExistence(timeout: 20), "no meeting page:\n\(app.debugDescription)")
        return app
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func waitFor(_ seconds: TimeInterval = 10, _ ok: () -> Bool) -> Bool {
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if ok() { return true }
            usleep(250_000)
        }
        return ok()
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        try save(app.screenshot().pngRepresentation, name)
    }

    @MainActor private func screen(_ name: String) throws {
        let image = XCUIScreen.main.screenshot()
        let a = XCTAttachment(screenshot: image)
        a.name = name
        a.lifetime = .keepAlways
        add(a)
        try save(image.pngRepresentation, name)
    }

    private func save(_ png: Data, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try png.write(to: URL(fileURLWithPath: "\(dir)/live-\(appearance)-\(name).png"))
    }

    // MARK: Helpers: the mock hub

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    /// A call to the mock, which must answer 2xx: a mock without the route (404) or that refused it (400) fails here.
    private func post(_ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = "POST"
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        let done = expectation(description: path)
        let answer = LiveMockAnswer()
        URLSession.shared.dataTask(with: request) { data, response, _ in
            answer.status = (response as? HTTPURLResponse)?.statusCode ?? 0
            answer.body = data.map { String(decoding: $0, as: UTF8.self) } ?? ""
            done.fulfill()
        }.resume()
        wait(for: [done], timeout: 5)
        XCTAssertTrue((200..<300).contains(answer.status), "POST \(path) answered \(answer.status): \(answer.body)")
    }

    /// `GET /api/v1/recordings/{id}/live` as the hub answers it.
    private func liveRead(_ hub: String, _ id: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/api/v1/recordings/\(id)/live")!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// The recording's topics as the hub has them (`topics` of `GET …/live`).
    private func topics(_ hub: String, _ id: String) throws -> [[String: Any]] {
        try liveRead(hub, id)["topics"] as? [[String: Any]] ?? []
    }
}

/// What the mock answered a call (`post`), written from URLSession's queue and read after the wait.
private final class LiveMockAnswer: @unchecked Sendable {
    var status = 0
    var body = ""
}
