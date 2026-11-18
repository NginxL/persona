import XCTest

/// The call to Persona's screen (#9290, docs/design/DESIGN_SYSTEM.md "Call screen"), against scripts/mock/mock_hub.py
/// with MOCK_VOICE=1 (scripts/mock/voice_mock.py):
///   MOCK_VOICE=1 PORT=<port> python3 scripts/mock/mock_hub.py
///   TEST_RUNNER_PERSONA_VOICE_HUB=http://127.0.0.1:<port>
///   TEST_RUNNER_PERSONA_SHOTS=<dir>     screenshots, one file per state (call-connected.png, …), for the PR
/// The appearance is the simulator's (`simctl ui <udid> appearance light|dark`); a fresh mock and a fresh install per run.
/// The sample tests open each `-showCallSample` state (no system, no audio); `testACallGoesOnAwayFromTheApp` takes the
/// real path on the Simulator's stand-in for the system's calls (`SimulatedCallSystem`: the Simulator has no call
/// service), with the real socket to the mock's voice.
final class CallScreenTests: XCTestCase {
    private static let main = ["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", "server/b2e4d6f8-0000-4000-8000-000000000001"]
    private static let agent = ["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001"]

    override func setUpWithError() throws {
        continueAfterFailure = false
    }

    private var hub: String {
        get throws {
            guard let hub = ProcessInfo.processInfo.environment["PERSONA_VOICE_HUB"] else {
                throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_VOICE=1 (PERSONA_VOICE_HUB)")
            }
            return hub
        }
    }

    private func launch(_ args: [String], largest: Bool = false) throws -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + (try hub).addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(zh-Hans)", "-UIPreferredContentSizeCategoryName",
                               largest ? "UICTContentSizeCategoryAccessibilityXXXL" : "UICTContentSizeCategoryL"] + args
        app.launchPinned()
        // A fresh install asks once to send notifications; the prompt would cover the call screen in the shots.
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        for label in ["允许", "Allow"] where board.buttons[label].waitForExistence(timeout: 2) { // i18n-ignore: the system's own button
            board.buttons[label].tap()
        }
        return app
    }

    /// The app's own picture, or the whole screen's (another app, the Home Screen, the Lock Screen).
    @MainActor private func shot(_ name: String, _ app: XCUIApplication? = nil) {
        let image = app?.screenshot() ?? XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? image.pngRepresentation.write(to: URL(fileURLWithPath: dir + "/\(name).png"))
    }

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    @MainActor private func sample(_ state: String, largest: Bool = false, args: [String] = main) throws -> XCUIApplication {
        let app = try launch(args + ["-showCallSample", state], largest: largest)
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 30), "\(state): no call screen\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1.5) // the cover and the orb settle
        return app
    }

    // MARK: Each state, from the samples

    @MainActor func testTheStates() throws {
        let states: [(sample: String, name: String, check: (XCUIApplication) -> Void)] = [
            ("dialing", "call-calling", { app in
                XCTAssertTrue(app.staticTexts["正在呼叫…"].exists, "no 正在呼叫…\n\(app.debugDescription)")
            }),
            ("connected", "call-connected", { app in
                XCTAssertTrue(self.element(app, "call-duration").exists, "no duration")
                XCTAssertFalse(app.buttons["call-mute"].isSelected)
                XCTAssertTrue(app.buttons["call-speaker"].exists && !app.buttons["call-speaker"].isSelected, "the earpiece by default")
                XCTAssertFalse(app.buttons["call-resume"].exists)
            }),
            ("mutedSpeaker", "call-muted-speaker", { app in
                XCTAssertTrue(app.buttons["call-mute"].isSelected, "Mute is not on")
                XCTAssertTrue(app.buttons["call-speaker"].isSelected, "Speaker is not on")
            }),
            ("resume", "call-held-resume", { app in
                XCTAssertTrue(app.staticTexts["通话保持中"].exists, "no 通话保持中\n\(app.debugDescription)")
                XCTAssertTrue(app.buttons["call-resume"].exists, "no 恢复通话")
            }),
            ("reconnecting", "call-reconnecting", { app in
                XCTAssertTrue(app.staticTexts["重新连接中…"].exists, "no 重新连接中…\n\(app.debugDescription)")
            }),
            ("ended", "call-ended", { app in
                XCTAssertTrue(app.staticTexts["通话已结束 · 连不上 hub"].exists, "no ended note\n\(app.debugDescription)")
                XCTAssertFalse(app.buttons["call-end"].isEnabled, "End after the end")
            }),
            ("audioRoute", "call-audio-route", { app in
                XCTAssertTrue(self.element(app, "call-audio").exists, "no 音频 with AirPods there\n\(app.debugDescription)")
                XCTAssertFalse(app.buttons["call-speaker"].exists, "Speaker beside 音频")
            }),
        ]
        for (sample, name, check) in states {
            try XCTContext.runActivity(named: sample) { _ in
                let app = try self.sample(sample)
                check(app)
                shot(name, app)
                app.terminate()
            }
        }
    }

    /// The largest text size: the interface stops at xxxLarge, every label still on one line.
    @MainActor func testConnectedAtTheLargestTextSize() throws {
        let app = try sample("connected", largest: true)
        for id in ["call-mute", "call-speaker", "call-end", "call-minimise"] {
            XCTAssertTrue(app.buttons[id].isHittable, "\(id) is not on screen")
        }
        assertOnScreen(app, ["call-mute", "call-speaker", "call-end", "call-minimise"], "connected, largest")
        shot("call-connected-ax", app)
        app.terminate()
    }

    /// On hold with Resume at the largest text size, Persona's caption on two lines: every control inside the screen,
    /// frame and all, and the line (On Hold) and the caption in sight between Minimise and the controls. Run on a short
    /// phone too (an iPhone SE, 375 × 667), where the page was taller than the screen and Minimise and End went past its
    /// edges; there the orb gives way and the controls stay.
    @MainActor func testResumeAtTheLargestTextSize() throws {
        let app = try sample("resume", largest: true)
        let controls = ["call-mute", "call-speaker", "call-resume", "call-end"]
        assertOnScreen(app, ["call-minimise"] + controls, "resume, largest")
        let top = element(app, "call-minimise").frame.maxY - 0.5
        let bottom = controls.map { element(app, $0).frame.minY }.min()! + 0.5
        for id in ["call-state", "call-caption"] {
            let frame = element(app, id).frame
            XCTAssertTrue(element(app, id).exists && frame.minY >= top && frame.maxY <= bottom,
                          "\(id) at \(frame) is not between Minimise and the controls (\(top)…\(bottom))\n\(app.debugDescription)")
        }
        let screen = app.windows.firstMatch.frame
        shot("call-held-resume-ax-\(Int(screen.width))x\(Int(screen.height))", app)
        app.terminate()
    }

    /// Settings › Text Size reaches the call screen and the bar back to it, both drawn outside the app's root view: one
    /// step up (Large) grows the round controls as the text grows (body 17 → 19 pt), one step down (Small) shrinks them
    /// (UIFontMetrics' own curve: about 0.96 ×), and the bar grows with the text at the larger size. Without it both
    /// stayed the system size's (72 and 44 pt).
    @MainActor func testTheAppsTextSizeReachesTheCallScreenAndTheBar() throws {
        let large = try sample("connected", args: Self.main + ["-ui.textSize", "large"])
        XCTAssertEqual(large.buttons["call-end"].frame.width, Self.control * 19 / 17, accuracy: 1.5, "Large did not reach the call screen")
        large.buttons["call-minimise"].tap()
        let bar = large.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call\n\(large.debugDescription)")
        XCTAssertGreaterThanOrEqual(bar.frame.height, Self.tapTarget * 19 / 17 - 1, "Large did not reach the bar")
        Thread.sleep(forTimeInterval: 1)
        shot("call-minimised-bar-large", large)
        large.terminate()

        let small = try sample("connected", args: Self.main + ["-ui.textSize", "small"])
        let width = small.buttons["call-end"].frame.width
        XCTAssertTrue(width < Self.control - 1.5 && width > Self.control * 15 / 17, "Small did not reach the call screen: \(width)")
        small.terminate()
    }

    // MARK: Long texts

    /// An Agent session's long title, a long answer, a long reason and an urgent call's long title (#9887's polish), in
    /// the samples' places: `-showCallSampleName`, `-showCallSampleCaption`, `-showCallSampleReason`,
    /// `-showUrgentCallSampleTitle`.
    private static let longName = "重构支付服务的订单对账模块并补齐集成测试和回归用例"
    private static let longCaption = "我看了一下，对账模块的问题出在部分退款的金额汇总上，昨晚的批次里有十七笔对不上。"
        + "这几笔的流水号和订单号我已经整理好，放在会话里了。修复的办法是按支付渠道分别汇总，再补一组覆盖部分退款的集成测试。"
        + "回归用例我列了二十条，全部跑完大约要四十分钟。要不要我现在就开始改？"
    private static let longReason = "这台电脑上的语音扩展一直没有响应，已经自动重试了三次仍然失败，请确认电脑在线后再打一次"
    private static let longUrgentTitle = "生产环境支付服务的订单对账任务连续三次失败，需要你决定是否回滚到昨天的版本并暂停今晚的批量结算"
    private static let longTexts = ["-showCallSampleName", longName, "-showCallSampleCaption", longCaption,
                                    "-showCallSampleReason", longReason]

    /// The long texts on the connected call screen, the ended one, the bar over the main conversation and the urgent call's
    /// page, at the app's own text size and the largest (`long-<what>[-ax].png`): the screenshots before and after the
    /// layout's polish. Each whole for VoiceOver; a tap on the name opens the call's details with the whole name and the
    /// answer (`long-details[-ax].png`). The urgent page needs the mock's call too (MOCK_URGENT_CALL=1).
    @MainActor func testTheLongTexts() throws {
        for largest in [false, true] {
            let suffix = largest ? "-ax" : ""
            for state in ["connected", "ended"] {
                let app = try sample(state, largest: largest, args: Self.main + Self.longTexts)
                XCTAssertEqual(element(app, "call-title").label, Self.longName, "\(state): the name is not whole for VoiceOver")
                if state == "ended" {
                    XCTAssertTrue(element(app, "call-state").label.contains(Self.longReason), "the reason is not whole: \(element(app, "call-state").label)")
                } else {
                    XCTAssertEqual(element(app, "call-caption").label, Self.longCaption, "the caption is not whole for VoiceOver")
                }
                shot("long-\(state)\(suffix)", app)
                if state == "connected" {
                    element(app, "call-title").tap()
                    XCTAssertTrue(element(app, "call-details").waitForExistence(timeout: 5), "the name did not open the call's details\n\(app.debugDescription)")
                    XCTAssertEqual(element(app, "call-details-title").label, Self.longName, "the details do not have the whole name")
                    // The answer's row in the details (Persona's name and the words, one element), not the screen's caption.
                    let answer = app.descendants(matching: .any).matching(NSPredicate(format: "label CONTAINS %@ AND identifier != %@",
                                                                                          Self.longCaption, "call-caption")).firstMatch
                    XCTAssertTrue(answer.exists, "the details do not have the whole answer\n\(app.debugDescription)")
                    Thread.sleep(forTimeInterval: 1)
                    shot("long-details\(suffix)", app)
                    app.buttons["call-details-close"].tap()
                    XCTAssertTrue(element(app, "call-details").waitForNonExistence(timeout: 5), "the details did not close")
                    XCTAssertTrue(element(app, "call-screen").exists, "closing the details left the call screen")
                }
                app.terminate()
            }
            let app = try sample("connected", largest: largest, args: Self.main + Self.longTexts)
            app.buttons["call-minimise"].tap()
            let bar = app.buttons["call-return-bar"]
            XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call\n\(app.debugDescription)")
            XCTAssertTrue(bar.label.hasPrefix(Self.longName), "the bar's name is not whole for VoiceOver: \(bar.label)")
            Thread.sleep(forTimeInterval: 1.5)
            shot("long-bar\(suffix)", app)
            app.terminate()

            let urgent = try launch(Self.main + ["-showUrgentCallSample", "YES", "-showUrgentCallSampleTitle", Self.longUrgentTitle],
                                    largest: largest)
            XCTAssertTrue(urgent.buttons["urgentCall.hangUp"].waitForExistence(timeout: 30), "no urgent call page\n\(urgent.debugDescription)")
            XCTAssertEqual(element(urgent, "urgentCall.title").label, Self.longUrgentTitle, "the urgent call's title is not whole")
            Thread.sleep(forTimeInterval: 1.5)
            shot("long-urgent\(suffix)", urgent)
            urgent.terminate()
        }
    }

    /// `Theme.Size.callControl` and `tapTarget` at the default text size (the UI tests do not link the app's tokens).
    private static let control: CGFloat = 72
    private static let tapTarget: CGFloat = 44

    /// Each control inside the app's window, its whole frame: `isHittable` passes for one half off the screen.
    @MainActor private func assertOnScreen(_ app: XCUIApplication, _ ids: [String], _ what: String) {
        let screen = app.windows.firstMatch.frame.insetBy(dx: -0.5, dy: -0.5)
        for id in ids {
            let control = element(app, id)
            XCTAssertTrue(control.exists, "\(what): no \(id)\n\(app.debugDescription)")
            XCTAssertTrue(screen.contains(control.frame), "\(what): \(id) at \(control.frame) is outside the screen \(screen)")
        }
    }

    /// Minimise leaves the call for a bar at the top of the main conversation; a tap on it opens the call again.
    @MainActor func testMinimisedToTheBar() throws {
        let app = try sample("connected")
        app.buttons["call-minimise"].tap()
        let bar = app.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 5))
        Thread.sleep(forTimeInterval: 1.5)
        shot("call-minimised-bar", app)
        bar.tap()
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 10), "the bar did not open the call")
        app.terminate()
    }

    /// The composer's phone icon is the one voice entry (#9887 one button): + › More offers neither Voice Chat nor Call
    /// Persona, in the main conversation or an Agent's, and the icon is in both composers, saying it calls this
    /// conversation (`main-composer-phone.png`, `agent-composer-phone.png`).
    @MainActor func testThePhoneIconIsTheOneVoiceEntry() throws {
        let app = try launch(Self.main)
        let icon = app.descendants(matching: .any).matching(identifier: "shell-composer-voice").firstMatch
        XCTAssertTrue(icon.waitForExistence(timeout: 90), "no phone icon in the main conversation's composer\n\(app.debugDescription)")
        XCTAssertEqual(icon.label, "呼叫这段对话", "the phone icon does not say it calls this conversation")
        Thread.sleep(forTimeInterval: 2)
        shot("main-composer-phone", app)
        try openMore(app)
        XCTAssertFalse(app.buttons["composer-menu-voice"].exists, "Voice Chat in More")
        XCTAssertFalse(app.buttons["composer-menu-call"].exists, "Call Persona in More")
        shot("main-plus-no-voice-rows", app)
        // At the sheet's half height the list goes on under the floating tabs (Mention, partly under them): a drag up opens
        // the sheet to full, where the list's end is clear of them.
        let mention = app.buttons["composer-menu-mention"]
        XCTAssertTrue(mention.exists, "no Mention\n\(app.debugDescription)")
        let camera = app.buttons["composer-menu-camera"]
        camera.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.5))
            .press(forDuration: 0.2, thenDragTo: camera.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: -4)), withVelocity: .slow,
                   thenHoldForDuration: 0.3)
        sleep(1)
        XCTAssertLessThanOrEqual(mention.frame.maxY, app.buttons["attach-tab-more"].frame.minY + 0.5,
                                 "Mention stays under the tabs\n\(app.debugDescription)")
        app.terminate()

        let agent = try launch(Self.agent)
        let agentIcon = agent.descendants(matching: .any).matching(identifier: "shell-composer-voice").firstMatch
        XCTAssertTrue(agentIcon.waitForExistence(timeout: 40), "no phone icon in an Agent's composer\n\(agent.debugDescription)")
        XCTAssertEqual(agentIcon.label, "呼叫这段对话", "the Agent's phone icon does not say it calls this conversation")
        Thread.sleep(forTimeInterval: 2)
        shot("agent-composer-phone", agent)
        try openMore(agent)
        XCTAssertFalse(agent.buttons["composer-menu-voice"].exists, "Voice Chat in an Agent's More")
        XCTAssertFalse(agent.buttons["composer-menu-call"].exists, "Call Persona in an Agent's More")
        agent.terminate()
    }

    /// The composer's phone icon (the iPhone's: a system call to its conversation, #9887).
    @MainActor private func tapPhoneIcon(_ app: XCUIApplication) {
        let icon = app.descendants(matching: .any).matching(identifier: "shell-composer-voice").firstMatch
        // The first launch after an install can take a minute before the page answers.
        XCTAssertTrue(icon.waitForExistence(timeout: 90), "no phone icon:\n\(app.debugDescription)")
        sleep(2)
        icon.tap()
    }

    @MainActor private func openMore(_ app: XCUIApplication) throws {
        let plus = app.descendants(matching: .any).matching(identifier: "shell-composer-add").firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 40), "no + button:\n\(app.debugDescription)")
        sleep(2)
        plus.tap()
        let more = app.buttons["attach-tab-more"]
        XCTAssertTrue(more.waitForExistence(timeout: 10), "no panel:\n\(app.debugDescription)")
        more.tap()
        XCTAssertTrue(app.buttons["composer-menu-mention"].waitForExistence(timeout: 10), "no More:\n\(app.debugDescription)")
        sleep(1)
    }

    /// The urgent call's page has the call screen's Mute and Speaker (off: the earpiece by default); Mute on, it fills
    /// and the line says so.
    @MainActor func testTheUrgentCallsMute() throws {
        let app = try launch(Self.main + ["-showUrgentCallSample", "YES"])
        let mute = app.buttons["urgentCall.mute"]
        XCTAssertTrue(mute.waitForExistence(timeout: 30), "no Mute on the urgent call\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["urgentCall.hangUp"].exists, "no 挂断")
        XCTAssertTrue(app.buttons["urgentCall.speaker"].exists && !app.buttons["urgentCall.speaker"].isSelected,
                      "no 扬声器 off by default on the urgent call")
        mute.tap()
        XCTAssertTrue(mute.isSelected, "Mute did not turn on")
        XCTAssertTrue(app.staticTexts["已静音"].waitForExistence(timeout: 5), "the line does not say 已静音\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1)
        shot("urgent-call-mute", app)
        app.terminate()
    }

    /// On hold after an interruption its audio did not come back from: the urgent call's page says On Hold and offers
    /// Resume beside Mute, Speaker and Hang Up, all four inside the screen.
    @MainActor func testTheUrgentCallOnHold() throws {
        let app = try launch(Self.main + ["-showUrgentCallSample", "held"])
        let resume = app.buttons["urgentCall.resume"]
        XCTAssertTrue(resume.waitForExistence(timeout: 30), "no Resume on the held urgent call\n\(app.debugDescription)")
        let line = app.staticTexts.matching(identifier: "urgentCall.state").firstMatch // its text, not its icon
        XCTAssertTrue(line.exists && line.label.contains("通话保持中"), "the line does not say 通话保持中: \(line.label)\n\(app.debugDescription)")
        assertOnScreen(app, ["urgentCall.mute", "urgentCall.speaker", "urgentCall.resume", "urgentCall.hangUp"], "urgent, held")
        Thread.sleep(forTimeInterval: 1)
        shot("urgent-call-held", app)
        app.terminate()
    }

    // MARK: The real path

    private func log(_ hub: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/voice2")!)
        return try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
    }

    private func audio(_ hub: String) throws -> Int { try XCTUnwrap(log(hub)["audio"] as? Int) }

    /// The main conversation's phone icon; the call connects (its time runs); minimised (the bar back to it at the top), the Agents page and an
    /// Agent's conversation, the Home Screen, another app for 15 s, locked for 15 s: the audio keeps going up to the mock at
    /// every step; the Home Screen and the Lock Screen photographed (the Simulator has no call service, so neither shows the
    /// call); back in the app the call screen is up and still connected, the voice's socket never closed, and the mock's
    /// start was a system call's (`surface: call`); End ends it. The mock's audio count at each step goes to
    /// `real-flow.json` beside the screenshots (and the log: `real-flow:`), for the PR.
    @MainActor func testACallGoesOnAwayFromTheApp() throws {
        let hub = try hub
        var steps: [[String: Any]] = []
        func step(_ name: String) throws -> Int {
            let count = try audio(hub)
            steps.append(["step": name, "audio": count, "at": Date.now.timeIntervalSince1970])
            print("real-flow: \(name) audio=\(count)")
            return count
        }
        // The mock counts every socket closed in its run: this call's are those after now.
        let closedBefore = try XCTUnwrap(log(hub)["closes"] as? Int)
        let app = try launch(Self.main)
        tapPhoneIcon(app)
        // A fresh install asks for the microphone in the app first (the call asks before it starts).
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 5) { allow.tap() }
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 15), "no call screen\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").waitForExistence(timeout: 30), "the call never connected\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 3)
        shot("real-call-connected", app)
        let starts = try XCTUnwrap(log(hub)["starts"] as? [[String: Any]])
        XCTAssertEqual(starts.last?["surface"] as? String, "call", "the start was not a system call's: \(starts)")
        let connected = try step("connected")

        app.buttons["call-minimise"].tap()
        let bar = app.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call")
        // The main conversation under it: the voice bar in the composer's place, with Show Call beside Stop.
        XCTAssertTrue(app.buttons["voice-expand"].waitForExistence(timeout: 10), "no Show Call on the voice bar\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1)
        shot("real-call-minimised", app)
        let minimised = try step("minimised")
        XCTAssertGreaterThan(minimised, connected, "no audio after minimising (\(connected) → \(minimised))")

        // Elsewhere in the app: the Agents page, then an Agent's conversation, the bar over both.
        let agents = app.buttons["shell-agents"].firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 10), "no Agents switch\n\(app.debugDescription)")
        agents.tap()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no Agents page\n\(app.debugDescription)")
        XCTAssertTrue(bar.exists, "no bar back to the call over the Agents page")
        Thread.sleep(forTimeInterval: 2)
        shot("real-call-agents", app)
        let onAgents = try step("agents page")
        XCTAssertGreaterThan(onAgents, minimised, "the audio stopped on the Agents page (\(minimised) → \(onAgents))")
        row.tap()
        let back = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch
        XCTAssertTrue(back.waitForExistence(timeout: 15) || row.waitForNonExistence(timeout: 1), "no Agent's conversation\n\(app.debugDescription)")
        XCTAssertTrue(bar.exists, "no bar back to the call over an Agent's conversation")
        Thread.sleep(forTimeInterval: 3)
        shot("real-call-agent-conversation", app)
        let inAgent = try step("agent conversation")
        XCTAssertGreaterThan(inAgent, onAgents, "the audio stopped in an Agent's conversation (\(onAgents) → \(inAgent))")

        XCUIDevice.shared.press(.home)
        sleep(2)
        shot("home-screen")
        let home = try step("home screen")
        let settings = XCUIApplication(bundleIdentifier: "com.apple.Preferences")
        settings.launch()
        sleep(15)
        shot("settings-app")
        let away = try step("settings app 15 s")
        XCTAssertGreaterThan(away, home + 20, "the audio stopped while away (\(home) → \(away))")

        // Locked for 15 s, woken to the Lock Screen, then unlocked (a locked simulator hangs the next run).
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(15)
        let locked = try step("locked 15 s")
        XCTAssertGreaterThan(locked, away + 20, "the audio stopped while locked (\(away) → \(locked))")
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        shot("lock-screen")
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(2)
        let unlocked = try step("unlocked")
        XCTAssertGreaterThan(unlocked, locked, "the audio stopped after unlocking (\(locked) → \(unlocked))")

        // Back in the app (as from the Dynamic Island or the status bar's call): the call screen, still connected.
        app.activate()
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 10), "the call screen did not come back\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").exists, "the call is not connected after coming back")
        let closes = try XCTUnwrap(log(hub)["closes"] as? Int) - closedBefore
        XCTAssertEqual(closes, 0, "the voice's socket closed while away")
        shot("real-call-back", app)
        let returned = try step("back in Persona")
        XCTAssertGreaterThan(returned, unlocked, "no audio back in the app (\(unlocked) → \(returned))")

        app.buttons["call-end"].tap()
        XCTAssertTrue(app.staticTexts["通话已结束"].waitForExistence(timeout: 5), "no 通话已结束\n\(app.debugDescription)")
        shot("real-call-ended", app)
        XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 10), "the ended call stayed")
        XCTAssertFalse(app.buttons["call-return-bar"].exists, "a bar for an ended call")
        let after = try step("ended")
        sleep(3)
        let later = try step("3 s after the end")
        XCTAssertLessThanOrEqual(later - after, 5, "the audio went on after End")
        let surface = (try log(hub)["starts"] as? [[String: Any]])?.last?["surface"] as? String
        print("real-flow: starts[-1].surface=\(surface ?? "nil") closes=\(closes)")
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let record: [String: Any] = ["steps": steps, "surface": surface ?? NSNull(), "closes": closes]
            let data = try JSONSerialization.data(withJSONObject: record, options: [.prettyPrinted, .sortedKeys])
            try data.write(to: URL(fileURLWithPath: dir + "/real-flow.json"))
        }
        app.terminate()
    }

    /// The phone icon in an Agent's conversation (#9887 one button): a system call to that session (the mock's start has
    /// its target and `surface: call`), the call screen showing its title and its agent; the bar over its conversation and
    /// over the main conversation, whose phone icon asks before it ends the call (Cancel keeps it); the Home Screen,
    /// another app for 15 s, locked for 15 s, back: the audio kept going up and no voice socket closed; End. The counts go
    /// to `agent-call.json` beside the screenshots.
    @MainActor func testACallFromAnAgentsConversation() throws {
        let hub = try hub
        var steps: [[String: Any]] = []
        let closedBefore = try XCTUnwrap(log(hub)["closes"] as? Int)
        var last = 0
        @discardableResult func step(_ name: String, _ app: XCUIApplication? = nil, shot file: String? = nil, rise: Int = 1) throws -> Int {
            if let file { shot(file, app) }
            let count = try audio(hub)
            let closes = try XCTUnwrap(log(hub)["closes"] as? Int) - closedBefore
            steps.append(["step": name, "audio": count, "closes": closes, "at": Date.now.timeIntervalSince1970])
            print("agent-call: \(name) audio=\(count) closes=\(closes)")
            XCTAssertEqual(closes, 0, "\(name): the voice's socket closed")
            if last > 0 { XCTAssertGreaterThanOrEqual(count, last + rise, "\(name): the audio stopped (\(last) → \(count))") }
            last = count
            return count
        }
        let app = try launch(Self.agent)
        tapPhoneIcon(app)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 5) { allow.tap() }
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 15), "no call screen\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").waitForExistence(timeout: 30), "the call never connected\n\(app.debugDescription)")
        let start = try XCTUnwrap((log(hub)["starts"] as? [[String: Any]])?.last)
        XCTAssertEqual(start["surface"] as? String, "call", "the start was not a system call's: \(start)")
        let target = start["target"] as? [String: String]
        XCTAssertEqual(target?["machine_id"], "macbook", "the call is not the Agent's: \(start)")
        XCTAssertEqual(target?["session_id"], "a1f3c2d4-0000-4000-8000-000000000001", "the call is not the Agent's: \(start)")
        let titleElement = element(app, "call-title")
        XCTAssertTrue(titleElement.exists, "no title on the call screen\n\(app.debugDescription)")
        let title = titleElement.label
        XCTAssertNotEqual(title, "Persona", "the call screen does not name the Agent's conversation")
        XCTAssertTrue(element(app, "call-detail").exists, "no line saying whose conversation it is\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 3)
        try step("connected", app, shot: "agent-call-connected")

        app.buttons["call-minimise"].tap()
        let bar = app.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call")
        XCTAssertTrue(bar.label.contains(title), "the bar does not name the conversation \(title): \(bar.label)")
        Thread.sleep(forTimeInterval: 1.5)
        try step("minimised", app, shot: "agent-call-bar-own")

        // Another page, the main conversation: the bar over it names the Agent's conversation; its phone icon asks before
        // it ends this call (End and Call, naming both), and Cancel keeps the call.
        let back = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch
        if back.waitForExistence(timeout: 5) { back.tap() }
        let assistant = app.buttons["shell-assistant"].firstMatch
        XCTAssertTrue(assistant.waitForExistence(timeout: 10), "no Assistant switch\n\(app.debugDescription)")
        assistant.tap()
        let mainIcon = app.descendants(matching: .any).matching(identifier: "shell-composer-voice").firstMatch
        XCTAssertTrue(mainIcon.waitForExistence(timeout: 20), "no phone icon in the main conversation\n\(app.debugDescription)")
        XCTAssertTrue(bar.waitForExistence(timeout: 5) && bar.isHittable, "no bar over the main conversation\n\(app.debugDescription)")
        XCTAssertTrue(bar.label.contains(title), "the bar over the main conversation does not name \(title): \(bar.label)")
        Thread.sleep(forTimeInterval: 2)
        try step("main conversation", app, shot: "agent-call-bar")
        mainIcon.tap()
        try confirmAndCancel(app, shot: "agent-call-question", yes: "结束并呼叫", message: "当前通话会结束", callWith: title)
        try step("phone icon in the main conversation, Cancel", app)

        XCUIDevice.shared.press(.home)
        sleep(2)
        try step("home screen", shot: "agent-call-home")
        let settings = XCUIApplication(bundleIdentifier: "com.apple.Preferences")
        settings.launch()
        sleep(15)
        try step("settings app 15 s", shot: "agent-call-settings-app", rise: 20)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(15)
        try step("locked 15 s", rise: 20)
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(2)
        try step("unlocked")
        app.activate()
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 10), "the call screen did not come back\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").exists, "the call is not connected after coming back")
        try step("back in Persona", app, shot: "agent-call-back")

        app.buttons["call-end"].tap()
        XCTAssertTrue(app.staticTexts["通话已结束"].waitForExistence(timeout: 5), "no 通话已结束\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 10), "the ended call stayed")
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let record: [String: Any] = ["steps": steps, "surface": start["surface"] ?? NSNull(), "target": target ?? [:],
                                         "closes": (try XCTUnwrap(log(hub)["closes"] as? Int)) - closedBefore]
            let data = try JSONSerialization.data(withJSONObject: record, options: [.prettyPrinted, .sortedKeys])
            try data.write(to: URL(fileURLWithPath: dir + "/agent-call.json"))
        }
        app.terminate()
    }

    // MARK: Nothing in the app ends the call (#9290 follow-up)

    /// The assistant on the call screen: its look and the name the main conversation's top bar says (the mock's
    /// 「助手」 on a hub with several assistants, #9887), in each state the samples have, speaking with its ring swelling,
    /// on hold with the pause mark, ended greyed.
    @MainActor func testTheAssistantOnTheCallScreen() throws {
        for (sample, name) in [("connected", "call-avatar-connected"), ("speaking", "call-avatar-speaking"), ("held", "call-avatar-held"),
                               ("ended", "call-avatar-ended")] {
            try XCTContext.runActivity(named: sample) { _ in
                let app = try self.sample(sample)
                XCTAssertTrue(app.staticTexts["助手"].exists, "\(sample): not the top bar's name\n\(app.debugDescription)")
                shot(name, app)
                app.terminate()
            }
        }
    }

    /// The avatar in each of the call's states (#9887, docs/apple/calls.md "The avatar"): every row's sample on the call
    /// screen, and the bar's still face; a speaking sample has a made-up voice, so its bounce, ripples and mouth move
    /// (two shots a moment apart). `PERSONA_CALL_LOOKS` lists the pictures, comma-separated: `mochi` (the look on screen,
    /// the default), `agent` (a call to an Agent session, `-showCallSampleAgent`), `package`, `uploaded` or a look's id
    /// (`-showCallSampleLook`; the mock's MOCK_PERSONAS=builtin MOCK_LOOK=nwverse has a package). The shots are named
    /// `call-avatar-<look>-<state>.png`. The state line keeps the running time while the voice's upstream is paused
    /// (`paused`: listening, not on hold).
    /// `PERSONA_CALL_STATES` picks some of the states, and `PERSONA_CALL_HOLD` (seconds) keeps each on screen that long
    /// after its shot, for a screen recording (each state's start goes to the log as `avatar-state <name> <epoch>`).
    @MainActor func testTheAvatarsStates() throws {
        let env = ProcessInfo.processInfo.environment
        let looks = (env["PERSONA_CALL_LOOKS"] ?? "mochi").split(separator: ",").map(String.init)
        let all = ["dialing", "connected", "hearing", "thinking", "speaking", "working", "asking", "held", "muted", "mutedSpeaking",
                   "paused", "reconnecting", "ended"]
        let states = env["PERSONA_CALL_STATES"].map { $0.split(separator: ",").map(String.init) } ?? all
        let hold = env["PERSONA_CALL_HOLD"].flatMap(Double.init) ?? 0
        for look in looks {
            let extra: [String] = switch look {
            case "mochi": []
            case "agent": ["-showCallSampleAgent", "YES"]
            default: ["-showCallSampleLook", look]
            }
            for state in states {
                try XCTContext.runActivity(named: "\(look) \(state)") { _ in
                    let app = try self.sample(state, args: Self.main + extra)
                    // A look that waits to be on the phone comes a moment later.
                    if look != "mochi", look != "agent" { Thread.sleep(forTimeInterval: 2) }
                    if state == "paused" {
                        XCTAssertTrue(self.element(app, "call-duration").exists, "the voice's pause shows as a hold\n\(app.debugDescription)")
                        XCTAssertFalse(app.staticTexts["通话保持中"].exists, "the voice's pause shows On Hold")
                    }
                    print("avatar-state \(look)-\(state) \(Date.now.timeIntervalSince1970)")
                    shot("call-avatar-\(look)-\(state)", app)
                    if hold > 0 { Thread.sleep(forTimeInterval: hold) }
                    if state.hasSuffix("peaking") {
                        Thread.sleep(forTimeInterval: 0.35)
                        shot("call-avatar-\(look)-\(state)-2", app)
                    }
                    if ["thinking", "working", "asking", "held", "speaking"].contains(state) {
                        app.buttons["call-minimise"].tap()
                        XCTAssertTrue(app.buttons["call-return-bar"].waitForExistence(timeout: 10), "\(state): no bar")
                        Thread.sleep(forTimeInterval: 1)
                        shot("call-avatar-\(look)-\(state)-bar", app)
                    }
                    app.terminate()
                }
            }
        }
    }

    /// The avatar's states for VoiceOver (#9887 review): the avatar stays decorative (no element unless it is Interrupt
    /// Voice), and the call's line, which shows the running time, says what the avatar shows as its value: 「在听」 while
    /// nobody speaks, 「正在思考」, 「正在工作」, 「等你拿主意」 while a card waits, 「正在说」 while it speaks. The urgent call's header says the
    /// call's name, the main conversation's top bar's (the mock's 「助手」), beside its face.
    @MainActor func testTheAvatarsStatesForVoiceOver() throws {
        let rows = [("connected", "在听"), ("thinking", "正在思考"), ("working", "正在工作"), ("asking", "等你拿主意"),
                    ("speaking", "正在说")] // i18n-ignore: the catalog's words, as VoiceOver reads them
        for (state, words) in rows {
            try XCTContext.runActivity(named: state) { _ in
                let app = try self.sample(state)
                let line = self.element(app, "call-duration")
                XCTAssertTrue(line.exists, "\(state): no running time\n\(app.debugDescription)")
                XCTAssertEqual(line.value as? String, words, "\(state): the line's value\n\(app.debugDescription)")
                XCTAssertFalse(self.element(app, "call-avatar").exists, "\(state): the avatar is not decorative")
                app.terminate()
            }
        }
        let app = try launch(Self.main + ["-showUrgentCallSample", "YES"])
        let header = element(app, "urgentCall.header")
        XCTAssertTrue(header.waitForExistence(timeout: 30), "no urgent call header\n\(app.debugDescription)")
        for _ in 0..<20 where !header.label.contains("助手") { Thread.sleep(forTimeInterval: 0.5) } // i18n-ignore: the mock's name
        XCTAssertTrue(header.label.contains("助手"), "the header does not say the call's name: \(header.label)") // i18n-ignore: the mock's name
        Thread.sleep(forTimeInterval: 1)
        shot("urgent-call-header", app)
        app.terminate()
    }

    /// The owner walks the app with a call to Persona on, and nothing in it ends the call: minimised, the Agents page and
    /// an Agent's conversation with the bar over them; the + panel open and the bar over it (a tap shows the call screen
    /// over the panel, Minimize goes back to the panel); dictation refused with its note; Voice Chat from the panel and the
    /// composer's handset ask first, and Cancel keeps the call; a typed line in the main conversation; Settings open with
    /// the bar over it; the Home Screen, another app for 15 s, locked for 15 s; back in the app the call screen, still
    /// connected; End. At every step the mock's audio count went up and no voice socket closed; the counts go to
    /// `walk.json` beside the screenshots.
    @MainActor func testNothingInTheAppEndsTheCall() throws {
        let hub = try hub
        var steps: [[String: Any]] = []
        let closedBefore = try XCTUnwrap(log(hub)["closes"] as? Int)
        var last = 0
        @discardableResult func step(_ name: String, _ app: XCUIApplication? = nil, shot file: String? = nil) throws -> Int {
            if let file { shot(file, app) }
            let count = try audio(hub)
            let closes = try XCTUnwrap(log(hub)["closes"] as? Int) - closedBefore
            steps.append(["step": name, "audio": count, "closes": closes, "at": Date.now.timeIntervalSince1970])
            print("walk: \(name) audio=\(count) closes=\(closes)")
            XCTAssertEqual(closes, 0, "\(name): the voice's socket closed")
            if last > 0 { XCTAssertGreaterThan(count, last, "\(name): the audio stopped (\(last) → \(count))") }
            last = count
            return count
        }
        let app = try launch(Self.main)
        tapPhoneIcon(app)
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        if allow.waitForExistence(timeout: 5) { allow.tap() }
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 15), "no call screen\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").waitForExistence(timeout: 30), "the call never connected\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 3)
        try step("connected", app, shot: "walk-connected")

        app.buttons["call-minimise"].tap()
        let bar = app.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call")
        Thread.sleep(forTimeInterval: 1.5)
        try step("minimised", app, shot: "walk-bar-main")

        let agents = app.buttons["shell-agents"].firstMatch
        XCTAssertTrue(agents.waitForExistence(timeout: 10), "no Agents switch\n\(app.debugDescription)")
        agents.tap()
        let row = app.buttons.matching(identifier: "agents.row").firstMatch
        XCTAssertTrue(row.waitForExistence(timeout: 15), "no Agents page\n\(app.debugDescription)")
        XCTAssertTrue(bar.isHittable, "the bar is not there over the Agents page")
        Thread.sleep(forTimeInterval: 2)
        try step("agents page", app, shot: "walk-bar-agents")
        // An Agent at rest (its composer has the mic; a working one's has Stop in its place).
        let idle = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "agents.row", "博客迁移到 Astro")).firstMatch
        for _ in 0..<5 where !idle.isHittable { app.swipeUp(velocity: .slow) }
        XCTAssertTrue(idle.isHittable, "no Agent at rest on the Agents page\n\(app.debugDescription)")
        idle.tap()
        let plus = app.descendants(matching: .any).matching(identifier: "shell-composer-add").firstMatch
        XCTAssertTrue(plus.waitForExistence(timeout: 20), "no Agent's conversation\n\(app.debugDescription)")
        XCTAssertTrue(bar.isHittable, "the bar is not there over an Agent's conversation")
        Thread.sleep(forTimeInterval: 2)
        try step("agent conversation", app, shot: "walk-bar-conversation")

        // A sheet over it (the + panel, in a window of its own): the bar stays over it and opens the call screen over it.
        plus.tap()
        let more = app.buttons["attach-tab-more"]
        XCTAssertTrue(more.waitForExistence(timeout: 10), "no + panel\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1.5)
        // The first + panel of this install (#9887: the call no longer starts from it); a photo library prompt left
        // unanswered would cover it (grant photos before the run: docs/operations/testing.md).
        XCTAssertTrue(bar.waitForExistence(timeout: 5) && bar.isHittable, "the bar is not there over the + panel\n\(app.debugDescription)")
        try step("+ panel open", app, shot: "walk-bar-over-plus")
        bar.tap()
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 10), "the bar did not open the call over the + panel")
        Thread.sleep(forTimeInterval: 1)
        try step("call screen over the + panel", app, shot: "walk-call-over-plus")
        app.buttons["call-minimise"].tap()
        XCTAssertTrue(more.waitForExistence(timeout: 10), "the + panel went: the call screen dismissed what was open")
        // More has no voice row (#9887 one button): the phone icon is the one voice entry.
        more.tap()
        XCTAssertTrue(app.buttons["composer-menu-mention"].waitForExistence(timeout: 10), "no More\n\(app.debugDescription)")
        XCTAssertFalse(app.buttons["composer-menu-voice"].exists || app.buttons["composer-menu-call"].exists, "a voice row in More")
        let closePanel = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["attach-close", "attach-sheet-close"])).firstMatch
        if closePanel.exists { closePanel.tap() } else { app.swipeDown(velocity: .fast) }
        Thread.sleep(forTimeInterval: 1)
        try step("+ › More, no voice row", app)

        // Dictation: refused with a note, the call goes on.
        let mic = app.descendants(matching: .any).matching(identifier: "shell-composer-mic").firstMatch
        XCTAssertTrue(mic.waitForExistence(timeout: 10), "no mic\n\(app.debugDescription)")
        mic.tap()
        let note = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "通话结束后才能听写")).firstMatch
        XCTAssertTrue(note.waitForExistence(timeout: 5), "no dictation note\n\(app.debugDescription)")
        XCTAssertFalse(app.descendants(matching: .any)["dictation-bar"].exists, "the dictation bar opened")
        // The note sits below the conversation's top bar, which the call's bar moved down (it once lay on the title).
        let backButton = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch
        XCTAssertTrue(backButton.exists, "no Back in the Agent's conversation\n\(app.debugDescription)")
        let toast = app.descendants(matching: .any)["toast"].firstMatch
        XCTAssertTrue(toast.exists, "no toast around the note\n\(app.debugDescription)")
        XCTAssertGreaterThanOrEqual(toast.frame.minY, backButton.frame.maxY,
                                    "the note lies on the top bar: \(toast.frame) over Back \(backButton.frame)")
        try step("dictation refused", app, shot: "walk-dictation-refused")
        Thread.sleep(forTimeInterval: 3) // the note goes

        // The composer's phone icon in another conversation: asks first (End and Call); Cancel keeps the call.
        let handset = app.descendants(matching: .any).matching(identifier: "shell-composer-voice").firstMatch
        XCTAssertTrue(handset.waitForExistence(timeout: 10), "no phone icon\n\(app.debugDescription)")
        handset.tap()
        try confirmAndCancel(app, shot: "walk-call-confirm", yes: "结束并呼叫", message: "当前通话会结束")
        try step("phone icon elsewhere, Cancel", app)

        // A typed line in the main conversation, through the call's voice bar: back to the Agents page, then Assistant.
        let back = app.buttons.matching(NSPredicate(format: "label == %@", "返回")).firstMatch
        if back.waitForExistence(timeout: 5) { back.tap() }
        let assistant = app.buttons["shell-assistant"].firstMatch
        XCTAssertTrue(assistant.waitForExistence(timeout: 10), "no Assistant switch\n\(app.debugDescription)")
        assistant.tap()
        let moreOnBar = app.descendants(matching: .any)["voice-more"].firstMatch
        XCTAssertTrue(moreOnBar.waitForExistence(timeout: 15), "no voice bar in the main conversation\n\(app.debugDescription)")
        moreOnBar.tap()
        let typeLine = app.buttons["打字说一句"].firstMatch
        XCTAssertTrue(typeLine.waitForExistence(timeout: 5), "no Type a Line\n\(app.debugDescription)")
        typeLine.tap()
        let field = app.alerts.textFields.firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 5), "no line to type\n\(app.debugDescription)")
        field.typeText("明天上午有什么安排？")
        app.alerts.buttons["发送"].tap()
        Thread.sleep(forTimeInterval: 2)
        try step("typed line in the main conversation", app, shot: "walk-typed-line")

        // Settings (the shell's sheet): the bar over it.
        // The main conversation's drawer opens from the persona's face at the top left.
        let menu = app.buttons.matching(NSPredicate(format: "identifier IN %@", ["assistant-sidebar-button", "shell-place-sidebar"])).firstMatch
        XCTAssertTrue(menu.waitForExistence(timeout: 10), "no way to the drawer\n\(app.debugDescription)")
        menu.tap()
        let settingsRow = app.buttons.matching(NSPredicate(format: "identifier == %@ OR (identifier == %@ AND label == %@)",
                                                           "sidebar-settings", "assistant-sidebar", "设置")).firstMatch
        XCTAssertTrue(settingsRow.waitForExistence(timeout: 10), "no Settings in the drawer\n\(app.debugDescription)")
        settingsRow.tap()
        let close = element(app, "settings-close")
        XCTAssertTrue(close.waitForExistence(timeout: 15), "no Settings\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1.5)
        XCTAssertTrue(bar.isHittable, "the bar is not there over Settings")
        XCTAssertTrue(close.isHittable, "the bar covers Settings' Close")
        try step("Settings open", app, shot: "walk-bar-over-settings")
        close.tap()

        XCUIDevice.shared.press(.home)
        sleep(2)
        try step("home screen", shot: "walk-home")
        let settings = XCUIApplication(bundleIdentifier: "com.apple.Preferences")
        settings.launch()
        sleep(15)
        try step("settings app 15 s", shot: "walk-settings-app")
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(15)
        try step("locked 15 s")
        XCUIDevice.shared.perform(NSSelectorFromString("pressLockButton"))
        sleep(3)
        let bottom = springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.99))
        bottom.press(forDuration: 0.1, thenDragTo: springboard.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.3)))
        sleep(2)
        try step("unlocked")
        app.activate()
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 10), "the call screen did not come back\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").exists, "the call is not connected after coming back")
        try step("back in Persona", app, shot: "walk-back")

        app.buttons["call-end"].tap()
        XCTAssertTrue(app.staticTexts["通话已结束"].waitForExistence(timeout: 5), "no 通话已结束\n\(app.debugDescription)")
        shot("walk-ended", app)
        XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 10), "the ended call stayed")
        let surface = (try log(hub)["starts"] as? [[String: Any]])?.last?["surface"] as? String
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            let record: [String: Any] = ["steps": steps, "surface": surface ?? NSNull(),
                                         "closes": (try XCTUnwrap(log(hub)["closes"] as? Int)) - closedBefore]
            let data = try JSONSerialization.data(withJSONObject: record, options: [.prettyPrinted, .sortedKeys])
            try data.write(to: URL(fileURLWithPath: dir + "/walk.json"))
        }
        app.terminate()
    }

    /// The question before the phone icon (or a voice chat) ends the call: it names the assistant, and Cancel keeps the
    /// call. `yes`: its yes button (End and Call for the phone icon on the iPhone, #9887); `message`: words of its message;
    /// `callWith`: whom the call is with (the assistant's name as the main conversation's top bar says it, the mock's
    /// 「助手」, #9887; or an Agent's conversation's title).
    @MainActor private func confirmAndCancel(_ app: XCUIApplication, shot name: String, yes: String = "结束通话并开始",
                                             message words: String = "通话会结束", callWith: String = "助手") throws {
        let question = "结束与 \(callWith) 的通话"
        let ask = app.alerts.matching(NSPredicate(format: "label CONTAINS %@", question)).firstMatch
        XCTAssertTrue(ask.waitForExistence(timeout: 10), "no question before ending the call\n\(app.debugDescription)")
        let endAndStart = ask.buttons[yes].firstMatch
        XCTAssertTrue(endAndStart.exists, "no \(yes)\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1)
        // Its words are above its buttons: the bar's room once pushed the alert's title down and its message under them.
        let title = ask.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", question)).firstMatch
        let message = ask.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", words)).firstMatch
        XCTAssertTrue(message.exists, "no message under the question\n\(app.debugDescription)")
        XCTAssertLessThanOrEqual(title.frame.maxY, message.frame.minY + 1, "the message is not under the title")
        XCTAssertLessThanOrEqual(message.frame.maxY, endAndStart.frame.minY + 1,
                                 "the question's words reach under its buttons: \(message.frame) over \(endAndStart.frame)")
        XCTAssertGreaterThanOrEqual(title.frame.minY, ask.frame.minY, "the title starts above the alert")
        shot(name, app)
        ask.buttons["取消"].firstMatch.tap()
        XCTAssertTrue(ask.waitForNonExistence(timeout: 5), "Cancel did not close the question")
        XCTAssertTrue(app.buttons["call-return-bar"].waitForExistence(timeout: 5), "the call ended")
    }

    /// Another assistant (the mock's frog look, MOCK_PERSONAS=builtin MOCK_LOOK=nwverse MOCK_MANAGERS=1): the call carries
    /// its name (with one assistant on the hub the main conversation's top bar says the look's, #9887) and the call screen
    /// and the bar show its face. Skipped against a mock without it.
    @MainActor func testTheCallFollowsTheAssistant() throws {
        let hub = try hub
        var request = URLRequest(url: URL(string: hub + "/api/v1/looks")!)
        request.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let done = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var body: Data?
        URLSession.shared.dataTask(with: request) { data, _, _ in body = data; done.signal() }.resume()
        _ = done.wait(timeout: .now() + 10)
        let looks = body.flatMap { try? JSONSerialization.jsonObject(with: $0) as? [String: Any] }
        guard let current = looks?["current"] as? [String: Any], current["id"] as? String == "nwverse" else {
            throw XCTSkip("needs MOCK_PERSONAS=builtin MOCK_LOOK=nwverse")
        }
        var roster = URLRequest(url: URL(string: hub + "/api/v1/managers")!)
        roster.setValue("Bearer mock", forHTTPHeaderField: "Authorization")
        let listed = DispatchSemaphore(value: 0)
        nonisolated(unsafe) var managers: Data?
        URLSession.shared.dataTask(with: roster) { data, _, _ in managers = data; listed.signal() }.resume()
        _ = listed.wait(timeout: .now() + 10)
        let count = (managers.flatMap { try? JSONSerialization.jsonObject(with: $0) } as? [String: Any])?["managers"] as? [Any]
        guard (count?.count ?? 1) <= 1 else {
            throw XCTSkip("needs MOCK_MANAGERS=1: with several assistants the call says the current one's name, not the look's")
        }
        let app = try launch(Self.main)
        // The look is downloaded after pairing; a call takes the name the app has when it is made: until the look is
        // there, the call is the built-in's, and it is hung up and made again.
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        var named = false
        for attempt in 0..<5 where !named {
            Thread.sleep(forTimeInterval: attempt == 0 ? 8 : 5)
            tapPhoneIcon(app)
            if allow.waitForExistence(timeout: 3) { allow.tap() }
            XCTAssertTrue(element(app, "call-duration").waitForExistence(timeout: 30), "the call never connected\n\(app.debugDescription)")
            named = element(app, "call-screen").staticTexts["奶蛙"].exists
            if !named {
                app.buttons["call-end"].tap()
                XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 10), "the ended call stayed")
            }
        }
        XCTAssertTrue(named, "the call screen does not say the assistant's name\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 2)
        shot("call-other-assistant", app)
        app.buttons["call-minimise"].tap()
        let bar = app.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call")
        XCTAssertTrue(bar.label.contains("奶蛙"), "the bar does not say the assistant's name: \(bar.label)")
        Thread.sleep(forTimeInterval: 1)
        shot("call-other-assistant-bar", app)
        bar.tap()
        XCTAssertTrue(app.buttons["call-end"].waitForExistence(timeout: 10))
        app.buttons["call-end"].tap()
        app.terminate()
    }
}
