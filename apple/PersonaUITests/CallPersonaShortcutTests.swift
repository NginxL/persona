import XCTest

/// Call Persona and Talk to Persona (no car) as the system runs them (#9290, docs/apple/siri.md「Calling Persona」), against
/// scripts/mock/mock_hub.py with MOCK_VOICE=1 (scripts/mock/voice_mock.py):
///   MOCK_VOICE=1 PORT=<port> python3 scripts/mock/mock_hub.py
///   TEST_RUNNER_PERSONA_VOICE_HUB=http://127.0.0.1:<port>
///   TEST_RUNNER_PERSONA_SHOTS=<dir>   screenshots of Spotlight and the call
/// Persona paired and in the background, each App Shortcut run from Spotlight's Top Hit: the app comes to the front and the
/// call to Persona starts on the main conversation, which the mock sees as a start frame with `surface: "call"`
/// (`GET /mock/voice2`). A simulator without the microphone granted shows the prompt: it is allowed.
/// Left on Settings (the shell's sheet) or on an Agent's conversation, the call's page comes up over it all the same
/// (the call's own window, `CallWindow`: nothing the owner was on closes), Minimise goes back to that page with the bar
/// to the call over it; a refused call (the microphone not allowed) says why in the app.
/// Not Siri: the Simulator's Siri runs no App Shortcut ("Talk to Persona": "I didn't get that"), and takes "Call
/// Persona" or "Start a call with Persona" as a phone call of its own ("Sorry, I can't make a phone call"). A device
/// check (docs/apple/siri.md, "Verification on a phone").
final class CallPersonaShortcutTests: XCTestCase {
    private var hub: String {
        get throws {
            guard let hub = ProcessInfo.processInfo.environment["PERSONA_VOICE_HUB"] else {
                throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_VOICE=1 (PERSONA_VOICE_HUB)")
            }
            return hub
        }
    }

    @MainActor private var springboard: XCUIApplication { XCUIApplication(bundleIdentifier: "com.apple.springboard") }

    /// The shortcut's answer card outlives the app, its uninstall and the suite: left up, it covers the top of the next
    /// test's screen (VoiceModeTests' Back tapped it instead and the call outlived its page). Every test leaves none.
    override func tearDown() {
        MainActor.assumeIsolated { Self.dismissShortcutsAnswer(within: 3, quiet: true) }
        super.tearDown()
    }

    private func starts(_ hub: String) throws -> [[String: Any]] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/voice2")!)
        let log = try XCTUnwrap(JSONSerialization.jsonObject(with: data) as? [String: Any])
        return log["starts"] as? [[String: Any]] ?? []
    }

    @MainActor private func shot(_ name: String) {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        try? XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir + "/call-persona-\(name).png"))
    }

    /// Persona paired with the mock, its main conversation read, then in the background with no call on. `args` leave it
    /// on another page, which `page` (when given) finds before it goes. Returns how many starts the mock had seen.
    @MainActor private func personaInTheBackground(hub: String, args: [String] = [], page: ((XCUIApplication) -> XCUIElement)? = nil,
                                                   microphone reset: Bool = false) throws -> (XCUIApplication, Int) {
        let app = XCUIApplication()
        if reset { app.resetAuthorizationStatus(for: .microphone) }
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-AppleLanguages", "(zh-Hans)"] + args
        app.launchPinned()
        XCTAssertTrue(app.wait(for: .runningForeground, timeout: 20))
        Thread.sleep(forTimeInterval: 6) // the hub and its main conversation
        if let page { XCTAssertTrue(page(app).waitForExistence(timeout: 20), "not on the page to leave it on\n\(app.debugDescription)") }
        let before = try starts(hub).count
        XCUIDevice.shared.press(.home)
        XCTAssertTrue(springboard.wait(for: .runningForeground, timeout: 10), "no home screen")
        Thread.sleep(forTimeInterval: 1)
        return (app, before)
    }

    /// The call to Persona the mock saw after `before` starts, waiting up to `seconds` (allowing the microphone).
    @MainActor private func call(_ hub: String, after before: Int, seconds: Double) throws -> [String: Any]? {
        let allow = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Allow", "允许"])).firstMatch
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if allow.exists { allow.tap() }
            if let call = try starts(hub).dropFirst(before).last(where: { $0["surface"] as? String == "call" }) { return call }
            Thread.sleep(forTimeInterval: 1)
        }
        return nil
    }

    /// `query` typed into Spotlight and its Top Hit run (Return; the field reads "<query> — Open"), as Shortcuts and the
    /// Action button run an App Shortcut.
    @MainActor private func runFromSpotlight(_ query: String, shots name: String) {
        // Spotlight, its own app, with the keyboard up; a swipe made as the Home Screen settles can go unseen.
        let spotlight = XCUIApplication(bundleIdentifier: "com.apple.Spotlight")
        for attempt in 0..<3 where spotlight.state != .runningForeground {
            if attempt > 0 { Thread.sleep(forTimeInterval: 1) }
            springboard.swipeDown()
            _ = spotlight.wait(for: .runningForeground, timeout: 5)
        }
        XCTAssertEqual(spotlight.state, .runningForeground, "no Spotlight")
        Thread.sleep(forTimeInterval: 1)
        spotlight.typeText(query)
        Thread.sleep(forTimeInterval: 3)
        shot(name + "-spotlight")
        print("call-persona: Spotlight shows\n\(spotlight.debugDescription)")
        spotlight.typeText("\n")
    }

    /// `query` run from Spotlight with Persona in the background: the system brings Persona to the front and the call
    /// starts.
    @MainActor private func spotlightCalls(_ query: String, shots name: String) throws {
        let hub = try hub
        let (app, before) = try personaInTheBackground(hub: hub)
        runFromSpotlight(query, shots: name)
        let started = try call(hub, after: before, seconds: 40)
        shot(name + "-call")
        let seen = try starts(hub)
        let call = try XCTUnwrap(started, "no call to Persona started; starts: \(seen)")
        XCTAssertEqual(app.state, .runningForeground, "Persona is not in front for the call")
        XCTAssertEqual(call["surface"] as? String, "call")
        XCTAssertEqual(call["protocol"] as? Int, 2)
        app.terminate()
    }

    /// Call Persona (`CallPersonaIntent`, in front from the start).
    @MainActor func testSpotlightCallsPersona() throws {
        try spotlightCalls("Call Persona", shots: "call")
    }

    /// Talk to Persona with no car connected (`TalkToPersonaIntent`): run in the background, it comes to the front
    /// (`continueInForeground`) and calls Persona instead of saying to connect CarPlay.
    @MainActor func testSpotlightTalkToPersonaCallsWithoutACar() throws {
        try spotlightCalls("Talk to Persona", shots: "talk")
    }

    // MARK: Over the page the app was left on

    private func element(_ app: XCUIApplication, _ id: String) -> XCUIElement {
        app.descendants(matching: .any)[id].firstMatch
    }

    /// The shortcut's answer ("Calling Persona" and Done, the system's) stays over the top of the screen, the call
    /// page's Minimise included, until Done.
    @MainActor private static func dismissShortcutsAnswer(within seconds: TimeInterval = 8, quiet: Bool = false) {
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let owners = [springboard, XCUIApplication(bundleIdentifier: "com.apple.Spotlight")]
        let done = NSPredicate(format: "label IN %@", ["Done", "完成"])
        let deadline = Date.now.addingTimeInterval(seconds)
        while Date.now < deadline {
            if let button = owners.lazy.map({ $0.buttons.matching(done).firstMatch }).first(where: \.exists) {
                button.tap()
                Thread.sleep(forTimeInterval: 1)
                return
            }
            Thread.sleep(forTimeInterval: 0.5)
        }
        if !quiet { print("call-persona: no answer to dismiss\n\(springboard.debugDescription)") }
    }

    /// Call Persona with the app left on `args`' page: the call's page comes up over it and connects; Minimise goes back
    /// to the page `under` finds (Settings' sheet stays open under the call), with the bar at the top that opens the call
    /// again, and what `clear` finds (Settings' Close) not covered by it; End ends it.
    @MainActor private func callOver(_ args: [String], page: @escaping (XCUIApplication) -> XCUIElement,
                                     under: (XCUIApplication) -> XCUIElement, clear: ((XCUIApplication) -> XCUIElement)?,
                                     shots name: String) throws {
        let hub = try hub
        let (app, before) = try personaInTheBackground(hub: hub, args: args, page: page)
        runFromSpotlight("Call Persona", shots: name)
        let started = try call(hub, after: before, seconds: 40)
        let seen = try starts(hub)
        XCTAssertNotNil(started, "no call to Persona started; starts: \(seen)")
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 15), "no call page over the page it was left on\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-duration").waitForExistence(timeout: 30), "the call never connected\n\(app.debugDescription)")
        Thread.sleep(forTimeInterval: 1.5)
        shot(name + "-call")
        Self.dismissShortcutsAnswer()
        app.buttons["call-minimise"].tap()
        let bar = app.buttons["call-return-bar"]
        XCTAssertTrue(bar.waitForExistence(timeout: 10), "no bar back to the call\n\(app.debugDescription)")
        XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 5), "the call page stayed")
        XCTAssertTrue(under(app).waitForExistence(timeout: 10), "not back on the page it was left on\n\(app.debugDescription)")
        XCTAssertTrue(bar.isHittable, "the bar is not there over the page\n\(app.debugDescription)")
        if let clear { XCTAssertTrue(clear(app).isHittable, "the bar covers the sheet's control\n\(app.debugDescription)") }
        Thread.sleep(forTimeInterval: 1)
        shot(name + "-minimised")
        bar.tap()
        XCTAssertTrue(element(app, "call-screen").waitForExistence(timeout: 10), "the bar did not open the call")
        app.buttons["call-end"].tap()
        XCTAssertTrue(element(app, "call-screen").waitForNonExistence(timeout: 10), "the ended call stayed")
        XCTAssertFalse(app.buttons["call-return-bar"].exists, "a bar for an ended call")
        app.terminate()
    }

    /// Left on Settings (on the iPhone, the shell's sheet): the call's page comes up over it, and Minimise goes back to
    /// Settings, still open, the bar over it and its Close clear of the bar.
    @MainActor func testSpotlightCallFromSettingsShowsTheCallPage() throws {
        try callOver(["-tab", "settings"], page: { $0.buttons["settings-close"] }, under: { $0.buttons["settings-close"] },
                     clear: { $0.buttons["settings-close"] }, shots: "from-settings")
    }

    /// Left on an Agent's conversation: the call's page over it, and Minimise goes back to that conversation.
    @MainActor func testSpotlightCallFromAnAgentsConversationShowsTheCallPage() throws {
        let agent: (XCUIApplication) -> XCUIElement = {
            $0.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "给天气 App 加主屏幕小组件")).firstMatch
        }
        try callOver(["-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000001"], page: agent, under: agent, clear: nil,
                     shots: "from-agent")
    }

    /// The microphone not allowed (its prompt answered Don't Allow): the call is refused, its page goes and the app says
    /// why (the voice alert, with Open Settings), after Siri already said "Calling Persona". No call reaches the hub.
    @MainActor func testSpotlightCallRefusedSaysWhy() throws {
        let hub = try hub
        let (app, before) = try personaInTheBackground(hub: hub, microphone: true)
        defer { app.resetAuthorizationStatus(for: .microphone) } // the next run's prompt is allowed again
        runFromSpotlight("Call Persona", shots: "refused")
        let deny = springboard.buttons.matching(NSPredicate(format: "label IN %@", ["Don’t Allow", "Don't Allow", "不允许"])).firstMatch
        XCTAssertTrue(deny.waitForExistence(timeout: 40), "no microphone prompt\n\(springboard.debugDescription)")
        deny.tap()
        let alert = app.alerts["需要麦克风权限"]
        XCTAssertTrue(alert.waitForExistence(timeout: 15), "the refusal says nothing\n\(app.debugDescription)")
        XCTAssertTrue(alert.buttons["去设置"].exists, "no Open Settings on the refusal")
        Thread.sleep(forTimeInterval: 1)
        shot("refused-alert")
        XCTAssertFalse(element(app, "call-screen").exists, "the refused call's page stayed")
        XCTAssertFalse(app.buttons["call-return-bar"].exists, "a bar for a refused call")
        let calls = try starts(hub).dropFirst(before).filter { $0["surface"] as? String == "call" }
        XCTAssertTrue(calls.isEmpty, "a refused call reached the hub: \(calls)")
        alert.buttons["好"].tap()
        XCTAssertTrue(alert.waitForNonExistence(timeout: 5), "OK did not close the refusal")
        app.terminate()
    }
}
