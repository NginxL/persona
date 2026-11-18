import XCTest

/// A meeting's tape and its replay, and the audio file in place of the microphone (#9417, docs/apple/recording.md "A
/// meeting's tape and its replay"), against a mock hub (PERSONA_MOCK_HUB). zh-Hans; elements by their identifiers.
///
/// - The made-up tape (`-showMeetingReplay sample`, `MeetingTapeSample`) at 2×: the room's live box takes the hub's lines
///   one by one, the Highlights page and the map follow the tape, End writes no minutes on the hub but shows the tape's,
///   and the mock never hears of the replayed meeting (no upload, no audio).
/// - A meeting started on the Recordings page with `-openMeetingAudio` and the made-up tape's audio
///   (Fixtures/meeting-tape/audio.m4a, macOS `say`): no microphone prompt, and the file's audio goes up the hub's audio
///   socket in hub transcription, whose lines come back to the room.
///
/// PERSONA_SHOTS=<dir> saves replay-<appearance>-<name>.png (PERSONA_SHOTS_APPEARANCE names the appearance).
final class MeetingReplayUITests: XCTestCase {
    private let replayed = "00000000-0000-0000-0000-000000009417"
    private static let audio = URL(fileURLWithPath: #filePath).deletingLastPathComponent().deletingLastPathComponent()
        .appending(path: "PersonaTests/Fixtures/meeting-tape/audio.m4a")

    override func setUpWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/recording_minutes", ["mode": "pending"])
        try post(hub, "/mock/meeting_conversation", ["on": true, "room": "ok", "end_seconds": 180])
        try post(hub, "/mock/meeting_attendance", ["on": true, "default": "off"])
    }

    override func tearDownWithError() throws {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { return }
        try post(hub, "/mock/meeting_transcription", Self.transcriptionOn)
        try put(hub, "/api/v1/assistant", ["meeting_transcription_default": "device"])
    }

    /// The mock's hub mode as it starts: on, ready, its upstream working, a line per 4 seconds of audio.
    private static var transcriptionOn: [String: Any] {
        ["on": true, "ready": true, "upstream": "ok", "fatal": "", "line_seconds": 4, "final_seconds": 2]
    }

    @MainActor func testTheMadeUpTapeReplaysItsLinesHighlightsAndMap() throws {
        let hub = try mockHub()
        XCUIDevice.shared.appearance = ProcessInfo.processInfo.environment["PERSONA_SHOTS_APPEARANCE"] == "dark" ? .dark : .light
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings",
                               "-showMeetingReplay", "sample", "-showMeetingReplaySpeed", "2"]
        app.launchPinned()
        XCTAssertTrue(element(app, "meeting-thread-header").waitForExistence(timeout: 20), "no meeting room:\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "meeting-room-persona").waitForExistence(timeout: 5), "Persona isn't in the room: \(app.debugDescription)")

        // The hub's lines come one by one, the newest last, and its caption before each.
        let lines = app.descendants(matching: .any).matching(identifier: "meeting-live-hub-line")
        XCTAssertTrue(waitFor(15) { lines.count >= 1 }, "no line from the tape: \(app.debugDescription)")
        XCTAssertTrue(waitFor(10) { lines.count >= 2 }, "only one line: \(app.debugDescription)")
        try shot(app, "live")
        let last = app.staticTexts["那我周五前把发布说明写好。"].firstMatch
        XCTAssertTrue(last.waitForExistence(timeout: 20), "the tape's last line never came: \(app.debugDescription)")
        try shot(app, "live-end")

        // The highlights, as the tape had them at its end.
        app.buttons["meeting-room-tab-highlights"].firstMatch.tap()
        let highlights = app.descendants(matching: .any).matching(identifier: "meeting-highlight")
        XCTAssertTrue(waitFor(10) { highlights.count == 2 }, "not the tape's two highlights: \(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["下周三先发测试版，正式版晚一周"].firstMatch.exists, app.debugDescription)
        try shot(app, "highlights")

        // The map: three topics, the last one on now.
        app.buttons["meeting-room-tab-map"].firstMatch.tap()
        let map = element(app, "meeting-room-map")
        XCTAssertTrue(map.waitForExistence(timeout: 5), app.debugDescription)
        XCTAssertTrue(waitFor(10) { map.staticTexts.containing(NSPredicate(format: "label CONTAINS %@", "发布说明")).count > 0 },
                      "the map doesn't have the last topic: \(app.debugDescription)")
        try shot(app, "map")

        // End: the tape's minutes, and the hub never heard of the meeting.
        app.buttons["meeting-room-tab-live"].firstMatch.tap()
        let end = app.buttons["recording-stop"].firstMatch
        XCTAssertTrue(end.waitForExistence(timeout: 5), app.debugDescription)
        end.tap()
        let confirm = app.buttons["结束并生成纪要"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
        try shot(app, "end")
        confirm.tap()
        // After the meeting (#8731 V3) the minutes are on the screen itself: the summary card, and the Minutes part under it.
        let summary = element(app, "meeting-after-summary")
        XCTAssertTrue(summary.waitForExistence(timeout: 20), "no summary after the replay: \(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["新版录音入口已合进主干，开始录音只要一步"].firstMatch.waitForExistence(timeout: 20),
                      "the tape's minutes aren't shown: \(app.debugDescription)")
        try shot(app, "after")
        try shot(app, "minutes")
        XCTAssertNil(try upload(hub, replayed), "the replayed meeting went to the hub")
        XCTAssertNil(try transcription(hub, replayed), "the replayed meeting opened an audio socket")
    }

    @MainActor func testAMeetingRecordsFromAnAudioFile() throws {
        let hub = try mockHub()
        XCTAssertTrue(FileManager.default.fileExists(atPath: Self.audio.path), "no fixture audio at \(Self.audio.path)")
        try post(hub, "/mock/meeting_transcription", Self.transcriptionOn)
        try put(hub, "/api/v1/assistant", ["meeting_transcription_default": "hub"])
        let before = Set(try transcriptions(hub).keys)
        let app = XCUIApplication()
        app.launchArguments = ["-pairURL", pair(hub), "-tab", "assistant", "-openShellPage", "recordings",
                               "-openMeetingAudio", Self.audio.path]
        app.launchPinned()
        let start = app.buttons["recording-meeting-start"].firstMatch
        XCTAssertTrue(start.waitForExistence(timeout: 15), app.debugDescription)
        let row = element(app, "meeting-transcription-line")
        XCTAssertTrue(waitFor(20) { row.exists && row.label.contains("云端") }, "the start isn't on the hub: \(row.label)")
        // Persona answering on screen, as the lane's sample meetings run.
        let invite = app.buttons["meeting-invite-persona"].firstMatch
        XCTAssertTrue(invite.waitForExistence(timeout: 5), app.debugDescription)
        invite.tap()
        XCTAssertTrue(element(app, "meeting-persona-invited").waitForExistence(timeout: 5), app.debugDescription)
        start.tap()
        XCTAssertTrue(element(app, "meeting-thread-header").waitForExistence(timeout: 20), "no meeting room:\n\(app.debugDescription)")
        // Nothing asked for the microphone: the file stands in for it.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        XCTAssertEqual(springboard.alerts.count, 0, "a system prompt: \(springboard.debugDescription)")

        // The file goes up the audio socket at real time (16 kHz PCM16: 32 000 bytes a second), and the hub's lines
        // come back to the room.
        var bytes = 0
        XCTAssertTrue(waitFor(30) {
            bytes = ((try? self.transcriptions(hub)) ?? [:]).filter { !before.contains($0.key) }.values
                .compactMap { $0["bytes"] as? Int }.max() ?? 0
            return bytes >= 6 * 32_000
        }, "the file's audio didn't reach the hub (\(bytes) bytes)")
        // The hub has written a line (the mock's count, not a guess of how long that takes)…
        var written = 0
        XCTAssertTrue(waitFor(30) {
            written = ((try? self.transcriptions(hub)) ?? [:]).filter { !before.contains($0.key) }.values
                .compactMap { $0["lines"] as? Int }.max() ?? 0
            return written >= 1
        }, "the hub wrote no line (\(written))")
        // …and the room shows the hub's words. Once the thread has anything the live box folds to the newest only: the
        // hub's caption while one is being said, else its last line (MeetingLiveBox). Which one depends on whether the
        // room's first message came before the first line, so either counts (#10109).
        let hubLine = element(app, "meeting-live-hub-line")
        let hubCaption = element(app, "meeting-live-interim")
        XCTAssertTrue(waitFor(20) { hubLine.exists || hubCaption.exists }, "no words from the hub in the room: \(app.debugDescription)")
        try shot(app, "file-input")

        let end = app.buttons["recording-stop"].firstMatch
        end.tap()
        let confirm = app.buttons["结束并生成纪要"].firstMatch
        XCTAssertTrue(confirm.waitForExistence(timeout: 5), app.debugDescription)
        confirm.tap()
        XCTAssertTrue(element(app, "meeting-after-facts").waitForExistence(timeout: 20), app.debugDescription)
    }

    // MARK: Helpers

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) throws {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let appearance = env["PERSONA_SHOTS_APPEARANCE"] ?? "unknown"
        try app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/replay-\(appearance)-\(name).png"))
    }

    @MainActor private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: id).firstMatch
    }

    private func waitFor(_ seconds: TimeInterval, _ ok: () -> Bool) -> Bool {
        let deadline = Date().addingTimeInterval(seconds)
        while Date() < deadline {
            if ok() { return true }
            RunLoop.current.run(until: Date().addingTimeInterval(0.2))
        }
        return ok()
    }

    private func mockHub() throws -> String {
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else { throw XCTSkip("需要独立 mock hub") }
        return hub
    }

    private func pair(_ hub: String) -> String {
        "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
    }

    private func post(_ hub: String, _ path: String, _ body: [String: Any]) throws { try send("POST", hub, path, body) }
    private func put(_ hub: String, _ path: String, _ body: [String: Any]) throws { try send("PUT", hub, path, body) }

    private func send(_ method: String, _ hub: String, _ path: String, _ body: [String: Any]) throws {
        var request = URLRequest(url: URL(string: hub + path)!)
        request.httpMethod = method
        request.httpBody = try JSONSerialization.data(withJSONObject: body)
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        let done = expectation(description: path)
        URLSession.shared.dataTask(with: request) { _, _, _ in done.fulfill() }.resume()
        wait(for: [done], timeout: 5)
    }

    private func json(_ hub: String, _ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + path)!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// The recording's last upload as the mock stored it.
    private func upload(_ hub: String, _ id: String) throws -> [String: Any]? {
        (try json(hub, "/mock/recordings")["recordings"] as? [String: Any])?[id] as? [String: Any]
    }

    /// The mock's hub mode by recording: frames, bytes, timeline, whether a socket is up.
    private func transcriptions(_ hub: String) throws -> [String: [String: Any]] {
        (try json(hub, "/mock/meeting_transcription")["recordings"] as? [String: [String: Any]]) ?? [:]
    }

    private func transcription(_ hub: String, _ id: String) throws -> [String: Any]? { try transcriptions(hub)[id] }
}
