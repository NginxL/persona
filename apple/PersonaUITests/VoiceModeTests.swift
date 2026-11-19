import XCTest

/// The voice mode in the main conversation and an agent's chat (docs/assistant/voice.md 6.2, 6.8, #3508), against
/// scripts/mock/mock_hub.py with MOCK_VOICE=1 (scripts/mock/voice_mock.py):
///   MOCK_VOICE=1 PORT=<port> python3 scripts/mock/mock_hub.py
///   TEST_RUNNER_PERSONA_VOICE_HUB=http://127.0.0.1:<port>
///   TEST_RUNNER_PERSONA_SHOTS=<dir>                 screenshots, named by style, appearance, page, text size, state
///   TEST_RUNNER_PERSONA_VOICE_APPEARANCE=light|dark  for the names only (`simctl ui <udid> appearance` sets it)
///   TEST_RUNNER_PERSONA_UI_STYLE=beautiful           the BeautifulUI style (`launchPinned`)
/// The matrix tests open each `-showVoiceSample` state (no socket, no microphone); `testOpenVoiceWalksTheScript` takes
/// the real path (`-openVoice silent`: the real socket and bar, no audio, which a simulator may not have).
final class VoiceModeTests: XCTestCase {
    private static let agent = ["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001"]
    private static let main = ["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
    /// The states the screenshot matrix covers (docs/assistant/voice.md 6.8), each with what proves it is on screen.
    private static let samples: [(name: String, shows: String)] = [
        ("listening", "voice-bar"), ("working", "voice-line-stop"), ("asking", "voice-asking"), ("approval", "voice-bar"),
        ("refused", "voice-line-clear"), ("plan_feedback", "voice-line-plan"), ("paused", "voice-bar"), ("speaking", "voice-bar"),
        ("ended", "voice-continue"), ("setup", "去设置"), ("unprompted", "voice-line-anyway"),
    ]

    private var hub: String {
        get throws {
            guard let hub = ProcessInfo.processInfo.environment["PERSONA_VOICE_HUB"] else {
                throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_VOICE=1 (PERSONA_VOICE_HUB)")
            }
            return hub
        }
    }

    private func launch(_ args: [String], large: Bool = false) throws -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + (try hub).addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(zh-Hans)",
                               "-UIPreferredContentSizeCategoryName", large ? "UICTContentSizeCategoryAccessibilityXL" : "UICTContentSizeCategoryL"] + args
        app.launchPinned()
        return app
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String) {
        let env = ProcessInfo.processInfo.environment
        guard let dir = env["PERSONA_SHOTS"] else { return }
        let look = [UITestStyle.name, env["PERSONA_VOICE_APPEARANCE"] ?? "light"].joined(separator: "-")
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir + "/voice-\(look)-\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    @MainActor private func matrix(page: String, args: [String], large: Bool) throws {
        for (sample, shows) in Self.samples {
            try XCTContext.runActivity(named: "\(page) \(large ? "AX" : "standard") \(sample)") { _ in
                let app = try launch(args + ["-showVoiceSample", sample], large: large)
                // At large text sizes Send Back as Feedback does not fit beside anything: it is in ⋯. The main conversation's mock
                // session is idle, so its working sample (a run at work) has no Stop to offer.
                let shown = large && sample == "plan_feedback" ? "voice-more" : page == "main" && sample == "working" ? "voice-line" : shows
                let mark = sample == "setup" ? app.buttons[shown].firstMatch : element(app, shown)
                XCTAssertTrue(mark.waitForExistence(timeout: 25), "\(sample): no \(shown)\n\(app.debugDescription)")
                if sample != "setup", sample != "ended" {
                    XCTAssertTrue(element(app, "voice-end").exists, "\(sample): no 结束")
                    XCTAssertTrue(element(app, "voice-tail").waitForExistence(timeout: 10), "\(sample): no captions in the thread")
                }
                Thread.sleep(forTimeInterval: 1.5) // the orb and the line settle
                shot(app, "\(page)-\(large ? "ax" : "std")-\(sample)")
                app.terminate()
            }
        }
    }

    @MainActor func testAgentStandard() throws { try matrix(page: "agent", args: Self.agent, large: false) }
    @MainActor func testAgentLargeText() throws { try matrix(page: "agent", args: Self.agent, large: true) }
    @MainActor func testMainStandard() throws { try matrix(page: "main", args: Self.main, large: false) }
    @MainActor func testMainLargeText() throws { try matrix(page: "main", args: Self.main, large: true) }

    private func log(_ hub: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/voice2")!)
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    /// The real path against the mock's script (docs/archive/assistant/voice-plan.md ("Acceptance", #3508)): connected →
    /// handed over → run → paused → asked → refused, Clear and Send → done → Stop. Then the phone icon where the system
    /// will not place the call (`-showCallFallback`: the Simulator's stand-in refuses the Start): the voice chat in the
    /// app takes over, with a line saying why (#9887 one button); the background no longer ends it, leaving its page
    /// leaves it with the bar back to it at the top, and the bar goes back to its conversation; Stop ends it and its
    /// socket.
    @MainActor func testOpenVoiceWalksTheScript() throws {
        let hub = try hub
        let app = try launch(Self.agent + ["-openVoice", "silent", "-showCallFallback", "YES"])
        // A simulator that never answered the microphone prompt shows it as the call starts (xcodebuild reinstalls the
        // app, which drops a `simctl privacy` grant), and the call waits behind it: allow it.
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons
            .matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 5) { allow.tap() }
        let status = element(app, "voice-status")
        XCTAssertTrue(status.waitForExistence(timeout: 30), "no voice bar:\n\(app.debugDescription)")
        XCTAssertTrue(app.staticTexts["帮我把登录页那个失败的测试修一下"].waitForExistence(timeout: 15), "no caption in the thread")
        shot(app, "walk-1-handed")
        let starts = try XCTUnwrap(log(hub)["starts"] as? [[String: Any]])
        XCTAssertEqual(starts.last?["protocol"] as? Int, 2)
        XCTAssertEqual((starts.last?["target"] as? [String: String])?["machine_id"], "macbook")
        XCTAssertTrue(app.buttons["voice-line-stop"].waitForExistence(timeout: 10), "no 停止 Claude while it works")
        XCTAssertTrue(app.staticTexts["语音已暂停"].waitForExistence(timeout: 10), "the upstream never paused")
        shot(app, "walk-2-paused")
        XCTAssertTrue(element(app, "voice-asking").waitForExistence(timeout: 10), "the asked card has no mark")
        shot(app, "walk-3-asking")
        let clear = app.buttons["voice-line-clear"]
        XCTAssertTrue(clear.waitForExistence(timeout: 20), "no 清空并发送 for the computer's draft")
        shot(app, "walk-4-refused")
        clear.tap()
        let deadline = Date.now.addingTimeInterval(5)
        var resends: [[String: Any]] = []
        while Date.now < deadline, resends.isEmpty {
            resends = (try log(hub)["resends"] as? [[String: Any]]) ?? []
            if resends.isEmpty { Thread.sleep(forTimeInterval: 0.2) }
        }
        XCTAssertEqual(resends.last?["clear_draft"] as? String, "fix it", "清空并发送 did not send the clearable text back")
        XCTAssertTrue(app.staticTexts["登录页的测试修好了，改了 Login.tsx 和 session.go 两处。"].firstMatch.waitForExistence(timeout: 20),
                      "the result was not read out")
        shot(app, "walk-5-done")
        element(app, "voice-end").tap()
        XCTAssertTrue(element(app, "voice-bar").waitForNonExistence(timeout: 5), "结束 left the bar")
        XCTAssertFalse(element(app, "voice-ended").exists, "结束 by the user leaves no 继续")
        let voice = element(app, "shell-composer-voice")
        XCTAssertTrue(voice.waitForExistence(timeout: 5), "the composer's voice button is not back")

        // The phone icon on the iPhone places a system call; refused, the voice chat in the app takes over, with a line.
        let startCount = try XCTUnwrap(log(hub)["starts"] as? [[String: Any]]).count
        voice.tap()
        XCTAssertTrue(status.waitForExistence(timeout: 20), "no voice chat after the refused call\n\(app.debugDescription)")
        let fallback = try XCTUnwrap((log(hub)["starts"] as? [[String: Any]])?.last)
        XCTAssertEqual(try XCTUnwrap(log(hub)["starts"] as? [[String: Any]]).count, startCount + 1, "no new start")
        XCTAssertNotEqual(fallback["surface"] as? String, "call", "the fallback is the voice chat in the app: \(fallback)")
        XCTAssertEqual((fallback["target"] as? [String: String])?["machine_id"], "macbook", "the fallback is not on this conversation")
        shot(app, "walk-6-fallback")

        // The background no longer ends it (#9887): back in the app it is still on, and its socket never closed.
        let closes = try XCTUnwrap(log(hub)["closes"] as? Int)
        XCUIDevice.shared.press(.home)
        Thread.sleep(forTimeInterval: 4)
        app.activate()
        XCTAssertTrue(status.waitForExistence(timeout: 10), "the voice chat ended in the background\n\(app.debugDescription)")
        XCTAssertFalse(element(app, "voice-ended").exists, "「语音已结束」 after the background")
        XCTAssertEqual(try log(hub)["closes"] as? Int, closes, "the voice chat's socket closed in the background")
        shot(app, "walk-7-after-background")

        // Leaving the page leaves it on, with the bar back to it; the bar goes back to its conversation.
        let back = app.buttons.matching(NSPredicate(format: "label == '返回'")).firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 5))
        back.tap()
        let bar = app.buttons["voice-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the voice chat\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 2)
        XCTAssertEqual(try log(hub)["closes"] as? Int, closes, "the voice chat's socket closed as its page went")
        shot(app, "walk-8-bar")
        bar.tap()
        XCTAssertTrue(status.waitForExistence(timeout: 10), "the bar did not go back to the conversation\n\(app.debugDescription)")
        XCTAssertTrue(bar.waitForNonExistence(timeout: 5), "the bar stayed on the voice chat's own page")

        // Stop ends it, and its socket.
        element(app, "voice-end").tap()
        let gone = Date.now.addingTimeInterval(5)
        while Date.now < gone, (try log(hub)["closes"] as? Int) == closes { Thread.sleep(forTimeInterval: 0.2) }
        XCTAssertEqual(try log(hub)["closes"] as? Int, closes + 1, "Stop did not end the voice chat")
    }

    /// A full-screen picture of the voice chat's own conversation (#9887 review). Where the viewer hides the page (it
    /// reports leaving), the voice chat goes on with the bar back to it over the viewer, and the bar's tap closes the
    /// viewer (the conversation is the one already open, so opening it again would not), the bar going once the page
    /// shows again. Where the page counts as shown under the viewer (iOS 26's zoom presentation), no bar shows over it,
    /// and closing the viewer finds the voice chat's own bar. Either way the voice chat goes on. The blog conversation's
    /// pictures need the mock with MOCK_TURN_IMAGES=many as well; skipped without.
    @MainActor func testTheBarBackClosesAPictureOverItsConversation() throws {
        let app = try launch(["-openSession", "studio/a1f3c2d4-0000-4000-8000-000000000003", "-openVoice", "silent"])
        let allow = XCUIApplication(bundleIdentifier: "com.apple.springboard").buttons
            .matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 5) { allow.tap() }
        let status = element(app, "voice-status")
        XCTAssertTrue(status.waitForExistence(timeout: 30), "no voice bar:\n\(app.debugDescription)")
        let picture = app.buttons["viewed-image-0"]
        guard picture.waitForExistence(timeout: 15) else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_VOICE=1 and MOCK_TURN_IMAGES=many")
        }
        picture.tap()
        let close = app.buttons["viewer-close"]
        XCTAssertTrue(close.waitForExistence(timeout: 10), "the picture did not open\n\(app.debugDescription)")
        let bar = app.buttons["voice-return-bar"]
        if bar.waitForExistence(timeout: 5) {
            shot(app, "picture-1-bar")
            bar.tap()
            XCTAssertTrue(close.waitForNonExistence(timeout: 5), "the bar left the picture over its conversation\n\(app.debugDescription)")
            XCTAssertTrue(bar.waitForNonExistence(timeout: 5), "the bar stayed with its conversation in front")
        } else {
            shot(app, "picture-1-no-bar")
            close.tap()
            XCTAssertTrue(close.waitForNonExistence(timeout: 5), "the viewer did not close")
            XCTAssertFalse(bar.exists, "a bar back to the voice chat on its own page")
        }
        XCTAssertTrue(status.waitForExistence(timeout: 5), "the voice chat's own bar is not back\n\(app.debugDescription)")
        shot(app, "picture-2-back")
        element(app, "voice-end").tap()
        XCTAssertTrue(element(app, "voice-bar").waitForNonExistence(timeout: 5), "结束 left the bar")
    }
}
