import XCTest

/// The main conversation's model (#6542, #7857): the composer's model button names the effective model, its menu's
/// Change Model… opens the conversation's model sheet, and after a pick there the button names the new model. Against
/// scripts/mock/mock_hub.py with MOCK_MODEL_PICK=1 (PERSONA_MOCK_HUB): the main conversation runs Opus, which is also
/// Main Chat's default alias (the sheet's catalog row reads "Opus 5.5"), until Haiku (sheet row "Haiku 5.5") is picked;
/// every pick is dropped before each launch and after the test (`/mock/model_pick?reset=1`), and the typed draft after
/// it. With PERSONA_SHOTS set it writes `model-picker-<1-entry|2-menu|3-sheet|4-picked>-<light|dark>.png` there.
final class AssistantModelPickerUITests: XCTestCase {
    /// The mock's main conversation (`ASSISTANT_CONFIG["main"]`), on the hub's own machine.
    private static let session = "b2e4d6f8-0000-4000-8000-000000000001"
    /// The short draft that keeps the composer's open row (and its model button) on screen after the keyboard is put away.
    private static let draft = "Hi"
    private var hub: String!

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs scripts/mock/mock_hub.py with MOCK_MODEL_PICK=1 (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        _ = try mock("hits") // every mock answers this: an unreachable hub fails here rather than skipping
        // Without MOCK_MODEL_PICK the mock has no /mock/model_pick, and the main conversation offers no pick.
        guard (try? mock("model_pick?reset=1"))?["requests"] != nil else {
            throw XCTSkip("the mock hub runs without MOCK_MODEL_PICK=1")
        }
        dropDraftsAfterwards()
    }

    override func tearDownWithError() throws {
        if hub != nil { _ = try? mock("model_pick?reset=1") }
    }

    /// Each step's screenshot is taken before its checks, so a failing run still shows where it stopped.
    @MainActor func testChangeModelFromTheComposer() throws {
        for scheme in ["light", "dark"] {
            _ = try mock("model_pick?reset=1")
            let app = launch(scheme)
            let modes = modelButton(app, scheme)
            // The mock's main conversation runs Opus and Main Chat's default is Opus too, so this names Opus either way.
            let namesOpus = waitForValue(modes, containing: "Opus")
            try shot(app, scheme, "1-entry")
            XCTAssertTrue(namesOpus, "\(scheme): the button does not name Opus: \(String(describing: modes.value))")

            // Change Model… is offered once the picker knows the main conversation; a menu opened before then is opened
            // again once.
            let change = changeModel(app)
            var listed = false
            for attempt in 0..<2 where !listed {
                if attempt > 0 {
                    app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap() // closes the menu
                    sleep(2)
                }
                modes.tap()
                listed = change.waitForExistence(timeout: 5)
            }
            sleep(1) // the menu finishes opening
            try shot(app, scheme, "2-menu")
            XCTAssertTrue(listed, "\(scheme): no Change Model… in the model menu:\n\(app.debugDescription)")

            change.tap()
            let haiku = app.buttons["assistantModel.claude.haiku"].firstMatch
            let opened = haiku.waitForExistence(timeout: 15)
            sleep(1) // the sheet settles at its medium height
            try shot(app, scheme, "3-sheet")
            XCTAssertTrue(opened, "\(scheme): the model sheet does not list Haiku 5.5:\n\(app.debugDescription)")

            let before = try haikuPicks()
            reveal(haiku, app)
            haiku.tap()
            let closed = haiku.waitForNonExistence(timeout: 10)
            let renamed = waitForValue(modes, containing: "Haiku")
            sleep(1)
            try shot(app, scheme, "4-picked")
            XCTAssertTrue(closed, "\(scheme): the sheet stayed open after the pick:\n\(app.debugDescription)")
            // The menu's Change Model… row reads the same effective name as the button (ChatView `assistantEffectiveName`).
            XCTAssertTrue(renamed, "\(scheme): the button does not name the pick: \(String(describing: modes.value))")
            XCTAssertEqual(try haikuPicks(), before + 1,
                           "\(scheme): the hub has no new PUT claude/haiku: \((try? mock("model_pick")) ?? [:])")

            app.terminate()
        }
    }

    /// MOCK_ASSISTANT_LAUNCHER_FIXTURE=1: the shared candidates in an English existing-conversation sheet, at the
    /// default and largest accessibility text sizes, light and dark. A team-api pick keeps the conversation's
    /// scope visible when the sheet reopens; no older-node placeholder or one-off label appears.
    @MainActor func testSharedLauncherChoicesInEnglishAtDefaultAndLargestText() throws {
        let sizes = [
            ("default", [String]()),
            ("largest", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]),
        ]
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                _ = try mock("model_pick?reset=1")
                let app = launch(scheme, ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"] + sizeArgs)
                let modes = modelButton(app, scheme)
                let change = changeModel(app)
                var listed = false
                for attempt in 0..<2 where !listed {
                    if attempt > 0 {
                        app.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.15)).tap()
                        sleep(2)
                    }
                    modes.tap()
                    listed = change.waitForExistence(timeout: 5)
                }
                XCTAssertTrue(listed, "\(size)/\(scheme): no Change Model…:\n\(app.debugDescription)")
                change.tap()

                let runtime = app.buttons["assistantModel.runtime.default"].firstMatch
                XCTAssertTrue(runtime.waitForExistence(timeout: 15),
                              "\(size)/\(scheme): runtime missing; needs MOCK_ASSISTANT_LAUNCHER_FIXTURE=1:\n\(app.debugDescription)")
                reveal(runtime, app)
                XCTAssertTrue(runtime.isEnabled, "\(size)/\(scheme): runtime is not eligible")
                let team = app.buttons["assistantModel.team-api.claude-sonnet-4-5"].firstMatch
                XCTAssertTrue(team.waitForExistence(timeout: 5), "\(size)/\(scheme): team-api missing:\n\(app.debugDescription)")
                reveal(team, app)
                XCTAssertTrue(team.isEnabled, "\(size)/\(scheme): team-api is not eligible")
                let codex = app.descendants(matching: .any)["assistantModel.unavailable.codex-gw"].firstMatch
                XCTAssertTrue(codex.waitForExistence(timeout: 5), "\(size)/\(scheme): codex-gw missing:\n\(app.debugDescription)")
                for words in ["默认", "This time only", "Just Once"] {
                    let match = app.descendants(matching: .any).matching(NSPredicate(
                        format: "label CONTAINS %@ OR value CONTAINS %@", words, words)).firstMatch
                    XCTAssertFalse(match.exists, "\(size)/\(scheme): legacy or one-off wording \(words):\n\(app.debugDescription)")
                }

                team.tap()
                XCTAssertTrue(team.waitForNonExistence(timeout: 10), "\(size)/\(scheme): team-api pick did not close the sheet")
                modes.tap()
                XCTAssertTrue(change.waitForExistence(timeout: 5), "\(size)/\(scheme): cannot reopen the model sheet")
                change.tap()
                let scope = "This conversation has its own model. Following the default makes its next messages use Model configuration › Main chat again."
                XCTAssertTrue(app.staticTexts[scope].waitForExistence(timeout: 10),
                              "\(size)/\(scheme): persistent scope wording missing:\n\(app.debugDescription)")
                sleep(1)
                try shot(app, scheme, "5-shared-\(size)")
                app.terminate()
            }
        }
    }

    // MARK: Codex speed (#10244)

    /// Standard → Fast → Standard in the main conversation. Merely opening Speed sends nothing; each pick is its own
    /// POST …/modes with only service_tier, and the session's acknowledged value owns the checkmark.
    @MainActor func testCodexSpeedStandardFastStandardInLightAndDark() async throws {
        for scheme in ["light", "dark"] {
            _ = try await mockAsync("model_pick?reset=1&speed=idle")
            let app = launch(scheme, english)
            defer { app.terminate() }
            let modes = modelButton(app, scheme)
            XCTAssertTrue(waitForValue(modes, containing: "5.5"), "\(scheme): not the Codex speed fixture")
            XCTAssertTrue(waitForValue(modes, containing: "Medium"), "\(scheme): the fixture starts at Medium effort")
            let before = try await modeRequests().count
            var rows = openSpeed(app, modes)
            assertSpeedSelected(rows.standard, true, scheme)
            XCTAssertTrue(menuText(app, "uses more quota").exists, "\(scheme): Fast has no quota note:\n\(app.debugDescription)")
            XCTAssertTrue(menuText(app, "next turn").exists, "\(scheme): no next-turn note:\n\(app.debugDescription)")
            try speedShot(app, scheme, "idle")
            let afterOpening = try await modeRequests()
            XCTAssertEqual(afterOpening.count, before, "\(scheme): opening the menu changed the session")

            rows.fast.tap()
            var sent = try await waitForModeRequests(before + 1)
            XCTAssertEqual(try speedBody(sent.last), ["service_tier": "priority"], "\(scheme): Fast payload")
            rows = openSpeed(app, modes)
            assertSpeedSelected(rows.fast, true, scheme)
            XCTAssertTrue(waitForValue(modes, containing: "5.5") && waitForValue(modes, containing: "Medium"),
                          "\(scheme): speed changed model or effort")

            rows.standard.tap()
            sent = try await waitForModeRequests(before + 2)
            XCTAssertEqual(try speedBody(sent.last), ["service_tier": "default"], "\(scheme): Standard payload")
            rows = openSpeed(app, modes)
            assertSpeedSelected(rows.standard, true, scheme)
            sent = try await modeRequests()
            XCTAssertEqual(sent.count, before + 2, "\(scheme): opening/reopening Speed issued extra requests")
            for request in sent.suffix(2) { try assertSpeedRequest(request, scheme) }
            app.terminate()
        }
    }

    /// A working conversation takes the change for its next turn. Changing effort afterwards is a separate payload and
    /// leaves the acknowledged Fast selection alone. Both appearances capture the working menu.
    @MainActor func testCodexSpeedWorkingMenuAndEffortIndependenceInLightAndDark() async throws {
        for scheme in ["light", "dark"] {
            _ = try await mockAsync("model_pick?reset=1&speed=working")
            let app = launch(scheme, english)
            defer { app.terminate() }
            let modes = modelButton(app, scheme)
            var rows = openSpeed(app, modes)
            XCTAssertTrue(menuText(app, "next turn").exists, "\(scheme): working Speed has no next-turn note")
            try speedShot(app, scheme, "working")
            rows.fast.tap()
            var sent = try await waitForModeRequests(1)
            XCTAssertEqual(try speedBody(sent.last), ["service_tier": "priority"])
            rows = openSpeed(app, modes)
            assertSpeedSelected(rows.fast, true, scheme)

            effortChoice(app, "High").tap()
            sent = try await waitForModeRequests(2)
            XCTAssertEqual(try speedBody(sent.last), ["effort": "high"], "\(scheme): effort carried a speed or model patch")
            rows = openSpeed(app, modes)
            assertSpeedSelected(rows.fast, true, scheme)
            sent = try await modeRequests()
            XCTAssertEqual(sent.count, 2, "\(scheme): reopening after effort changed speed again")
            app.terminate()
        }
    }

    /// The same menu remains reviewable at the interface's largest accessibility text size (#10244, UI change self-check).
    @MainActor func testCodexSpeedMenuAtLargestTextInLightAndDark() async throws {
        let largest = ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]
        for state in ["idle", "working"] {
            for scheme in ["light", "dark"] {
                _ = try await mockAsync("model_pick?reset=1&speed=\(state)")
                let app = launch(scheme, english + largest)
                defer { app.terminate() }
                let modes = modelButton(app, scheme)
                let rows = openSpeed(app, modes)
                assertSpeedSelected(rows.standard, true, "\(state)/\(scheme)/largest")
                try speedShot(app, scheme, state + "-largest")
            }
        }
    }

    @MainActor func testCodexSpeedIsAbsentWithoutAdvertisedTiers() async throws {
        _ = try await mockAsync("model_pick?reset=1&speed=no-tier")
        let app = launch("light", english)
        defer { app.terminate() }
        let modes = modelButton(app, "light")
        modes.tap()
        XCTAssertTrue(changeModel(app).waitForExistence(timeout: 5), "the composer menu did not open:\n\(app.debugDescription)")
        XCTAssertFalse(speedControl(app).waitForExistence(timeout: 2), "Speed was inferred without service_tiers:\n\(app.debugDescription)")
        let requests = try await modeRequests()
        XCTAssertEqual(requests.count, 0)
    }

    /// The `ack` scenario answers POST without pushing a new tier until `speed_ack=1`. The checkmark must not move early.
    @MainActor func testCodexSpeedSelectionWaitsForHubAcknowledgment() async throws {
        _ = try await mockAsync("model_pick?reset=1&speed=ack")
        let app = launch("light", english)
        defer { app.terminate() }
        let modes = modelButton(app, "light")
        var rows = openSpeed(app, modes)
        rows.fast.tap()
        let sent = try await waitForModeRequests(1)
        XCTAssertEqual(try speedBody(sent.last), ["service_tier": "priority"])
        rows = openSpeed(app, modes)
        assertSpeedSelected(rows.standard, true, "before acknowledgment")

        _ = try await mockAsync("model_pick?speed_ack=1")
        rows = openSpeed(app, modes)
        assertSpeedSelected(rows.fast, true, "after acknowledgment")
        let afterAcknowledgment = try await modeRequests()
        XCTAssertEqual(afterAcknowledgment.count, 1)
    }

    /// A request still waiting for its native answer disables the choices with a visible reason, not a silent gray row.
    @MainActor func testCodexSpeedPendingChangeSaysWhy() async throws {
        _ = try await mockAsync("model_pick?reset=1&speed=pending")
        let app = launch("light", english)
        defer { app.terminate() }
        let modes = modelButton(app, "light")
        var rows = openSpeed(app, modes)
        rows.fast.tap()
        let sent = try await waitForModeRequests(1)
        XCTAssertEqual(try speedBody(sent.last), ["service_tier": "priority"])
        rows = openSpeed(app, modes)
        XCTAssertTrue(menuText(app, "Changing Speed").exists, "pending Speed has no explanation:\n\(app.debugDescription)")
        assertSpeedSelected(rows.standard, true, "pending request")
        _ = try await mockAsync("model_pick?speed_ack=1")
        rows = openSpeed(app, modes)
        assertSpeedSelected(rows.fast, true, "acknowledged request")
    }

    @MainActor func testCodexSpeedRefusalAndTimeoutKeepStandardSelected() async throws {
        for failure in ["refusal", "timeout"] {
            _ = try await mockAsync("model_pick?reset=1&speed=\(failure)")
            let app = launch("light", english)
            defer { app.terminate() }
            let modes = modelButton(app, "light")
            var rows = openSpeed(app, modes)
            rows.fast.tap()
            let sent = try await waitForModeRequests(1)
            XCTAssertEqual(try speedBody(sent.last), ["service_tier": "priority"], "\(failure): request")
            let toast = app.descendants(matching: .any)["toast"].firstMatch
            XCTAssertTrue(toast.waitForExistence(timeout: 10), "\(failure): no visible error:\n\(app.debugDescription)")
            rows = openSpeed(app, modes)
            assertSpeedSelected(rows.standard, true, failure)
            let afterFailure = try await modeRequests()
            XCTAssertEqual(afterFailure.count, 1, "\(failure): the failed pick was retried implicitly")
            app.terminate()
        }
    }

    private var english: [String] { ["-AppleLanguages", "(en)", "-AppleLocale", "en_US"] }

    /// GET /mock/model_pick?reset=1&speed=<idle|working|no-tier|ack|pending|refusal|timeout>. The response's `speed_requests`
    /// records every POST …/modes for the fixture as {method, path, body}. `speed_ack=1` pushes its held acknowledgment and releases a pending POST.
    @MainActor private func mockAsync(_ path: String) async throws -> [String: Any] {
        let (data, _) = try await URLSession.shared.data(from: URL(string: hub + "/mock/" + path)!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    @MainActor private func modeRequests() async throws -> [[String: Any]] {
        try await mockAsync("model_pick")["speed_requests"] as? [[String: Any]] ?? []
    }

    @MainActor private func waitForModeRequests(_ count: Int, timeout: TimeInterval = 15) async throws -> [[String: Any]] {
        let deadline = ContinuousClock.now + .seconds(timeout)
        var requests: [[String: Any]] = []
        repeat {
            requests = try await modeRequests()
            if requests.count >= count { return requests }
            try await Task.sleep(for: .milliseconds(200))
        } while ContinuousClock.now < deadline
        return requests
    }

    private func speedBody(_ request: [String: Any]?) throws -> [String: String] {
        let body = try XCTUnwrap(request?["body"] as? [String: String], "missing recorded JSON body: \(String(describing: request))")
        return body
    }

    private func assertSpeedRequest(_ request: [String: Any], _ context: String) throws {
        XCTAssertEqual(request["method"] as? String, "POST", context)
        XCTAssertEqual(request["path"] as? String, "/api/v1/sessions/server/\(Self.session)/modes", context)
        let body = try speedBody(request)
        XCTAssertEqual(body.count, 1, "\(context): speed must be alone: \(body)")
        XCTAssertNotNil(body["service_tier"], context)
    }

    @MainActor private func speedControl(_ app: XCUIApplication) -> XCUIElement {
        let identified = app.descendants(matching: .any)["sessionModes.speed"].firstMatch
        if identified.exists { return identified }
        return app.descendants(matching: .any).matching(NSPredicate(
            format: "label == %@ OR label BEGINSWITH %@", "Speed", "Speed ")).firstMatch
    }

    @MainActor private func speedChoice(_ app: XCUIApplication, id: String, title: String) -> XCUIElement {
        let identified = app.descendants(matching: .any)[id].firstMatch
        if identified.exists { return identified }
        return app.descendants(matching: .any).matching(NSPredicate(
            format: "label == %@ OR label BEGINSWITH %@", title, title + " ")).firstMatch
    }

    @MainActor private func openSpeed(_ app: XCUIApplication, _ modes: XCUIElement)
        -> (speed: XCUIElement, standard: XCUIElement, fast: XCUIElement) {
        var speed = speedControl(app)
        var fast = speedChoice(app, id: "sessionModes.speed.priority", title: "Fast")
        if !speed.exists && !fast.exists { modes.tap() }
        _ = changeModel(app).waitForExistence(timeout: 5)
        speed = speedControl(app)
        fast = speedChoice(app, id: "sessionModes.speed.priority", title: "Fast")
        // Large text can leave Speed above the native menu's visible range. Drag its rows down as MenuSectionTitleTests does.
        if let menu = app.collectionViews.allElementsBoundByIndex.first(where: {
            $0.staticTexts["Thinking Effort"].exists
        }) {
            for _ in 0..<2 where !fast.isHittable { menu.swipeDown() }
        }
        XCTAssertTrue(speed.waitForExistence(timeout: 5) || fast.waitForExistence(timeout: 1),
                      "no Speed in the main conversation's composer menu:\n\(app.debugDescription)")
        let standard = speedChoice(app, id: "sessionModes.speed.default", title: "Standard")
        fast = speedChoice(app, id: "sessionModes.speed.priority", title: "Fast")
        XCTAssertTrue(standard.waitForExistence(timeout: 5) && standard.isHittable, "Standard is not visible:\n\(app.debugDescription)")
        XCTAssertTrue(fast.waitForExistence(timeout: 5) && fast.isHittable, "Fast is not visible:\n\(app.debugDescription)")
        return (speed, standard, fast)
    }

    @MainActor private func assertSpeedSelected(_ row: XCUIElement, _ selected: Bool, _ context: String,
                                                 timeout: TimeInterval = 10) {
        let deadline = Date.now.addingTimeInterval(timeout)
        repeat {
            if row.exists && row.isSelected == selected { return }
            usleep(200_000)
        } while Date.now < deadline
        XCTFail("\(context): expected selected \(selected): \(row.debugDescription)")
    }

    @MainActor private func menuText(_ app: XCUIApplication, _ text: String) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(
            format: "label CONTAINS[c] %@ OR value CONTAINS[c] %@", text, text)).firstMatch
    }

    @MainActor private func effortChoice(_ app: XCUIApplication, _ title: String) -> XCUIElement {
        let identified = app.descendants(matching: .any)["sessionModes.effort." + title.lowercased()].firstMatch
        if identified.exists { return identified }
        return app.descendants(matching: .any).matching(NSPredicate(
            format: "label == %@ OR label BEGINSWITH %@", title, title + " ")).firstMatch
    }

    @MainActor private func speedShot(_ app: XCUIApplication, _ scheme: String, _ name: String) throws {
        try shot(app, scheme, "speed-" + name)
        let attachment = XCTAttachment(screenshot: XCUIScreen.main.screenshot())
        attachment.name = "Codex Speed \(name) \(scheme)"
        attachment.lifetime = .keepAlways
        add(attachment)
    }

    /// The main conversation in `scheme` (`light` / `dark`), paired with the mock hub.
    @MainActor private func launch(_ scheme: String, _ additionalArguments: [String] = []) -> XCUIApplication {
        let app = XCUIApplication()
        let url = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", url, "-tab", "assistant", "-dev.openChat", "", "-ui.scheme", scheme,
                               "-openAssistant", "server/" + Self.session, "-openDraftsFresh", "YES"] + additionalArguments
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1" // the chat title's value names the open session
        app.launchPinned()
        closeSystemAlert(timeout: 3)
        return app
    }

    /// The composer's model button, in the box's open row: a short draft opens it, with the keyboard put away so the menu
    /// and the sheet have room (as `AttachMenuTests.testHealthModelMenu`). The launch uses `-openDraftsFresh`, so the box
    /// starts empty, and `dropDraftsAfterwards` takes the draft away again once the test ends.
    @MainActor private func modelButton(_ app: XCUIApplication, _ scheme: String) -> XCUIElement {
        let field = app.descendants(matching: .any)["shell-composer-field"].firstMatch
        XCTAssertTrue(field.waitForExistence(timeout: 40), "\(scheme): no composer in the main conversation:\n\(app.debugDescription)")
        sleep(2) // the page settles
        for _ in 0..<3 where !app.keyboards.firstMatch.exists {
            field.tap()
            _ = app.keyboards.firstMatch.waitForExistence(timeout: 4)
        }
        field.typeText(Self.draft)
        let hide = app.buttons["shell-composer-dismiss-keyboard"].firstMatch
        if hide.waitForExistence(timeout: 5) {
            // At the largest text size the mode button overlaps this one's center; its leading edge remains a clean tap.
            hide.coordinate(withNormalizedOffset: CGVector(dx: 0.1, dy: 0.5)).tap()
        }
        _ = app.keyboards.firstMatch.waitForNonExistence(timeout: 5)
        let modes = app.buttons["shell-composer-modes"].firstMatch
        XCTAssertTrue(modes.waitForExistence(timeout: 10), "\(scheme): no model button in the composer:\n\(app.debugDescription)")
        return modes
    }

    /// The draft typed into the main conversation is saved to disk, so a later suite that opens this chat without
    /// `-openDraftsFresh` would start with it in the box. After the test (a failed one too, unlike `defer`) a launch with
    /// that switch drops every draft and stays up for the debounced save of the empty list. Typing deletes is not enough:
    /// on a relaunch the caret can sit before the draft, and they delete nothing.
    private func dropDraftsAfterwards() {
        addTeardownBlock {
            await MainActor.run {
                let app = XCUIApplication()
                app.launchArguments = ["-openDraftsFresh", "YES", "-dev.openChat", ""]
                app.launchPinned()
                sleep(2) // drafts are written a second after they change
                app.terminate()
            }
        }
    }

    /// The menu's Change Model…, by its identifier or, as a menu's item may carry none, by its English or Chinese title.
    @MainActor private func changeModel(_ app: XCUIApplication) -> XCUIElement {
        app.descendants(matching: .any).matching(NSPredicate(
            format: "identifier == %@ OR label BEGINSWITH %@ OR label BEGINSWITH %@",
            "sessionModes.assistantModel", "Change Model", "更改模型")) // i18n-ignore: en/zh-Hans menu titles
            .firstMatch
    }

    /// Up the sheet's list until `row` takes a tap: the first drag grows the sheet to its large height.
    @MainActor private func reveal(_ row: XCUIElement, _ app: XCUIApplication) {
        let list = app.collectionViews.containing(.button, identifier: row.identifier).firstMatch
        for _ in 0..<3 where !row.isHittable && list.exists {
            list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.75))
                .press(forDuration: 0.1, thenDragTo: list.coordinate(withNormalizedOffset: CGVector(dx: 0.5, dy: 0.25)))
            sleep(1)
        }
    }

    /// Waits until `element`'s value (the model button's spoken title) contains `text`.
    @MainActor private func waitForValue(_ element: XCUIElement, containing text: String, timeout: TimeInterval = 15) -> Bool {
        let deadline = Date.now.addingTimeInterval(timeout)
        repeat {
            if element.exists, (element.value as? String)?.contains(text) == true { return true }
            usleep(200_000)
        } while Date.now < deadline
        return false
    }

    /// The app icon follows the color scheme, and the system says so in an alert over the page: closed, so it is not in a
    /// screenshot or under a tap. A permission request is left alone (docs/operations/testing.md, "Mock-data screenshots").
    @MainActor private func closeSystemAlert(timeout: TimeInterval) {
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        guard board.alerts.firstMatch.waitForExistence(timeout: timeout) else { return }
        for label in ["OK", "好"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own button
            board.alerts.buttons[label].tap(); sleep(1); return
        }
    }

    /// How many times the hub was asked to keep Haiku 5.5 for the main conversation (`GET /mock/model_pick` lists every
    /// PUT and DELETE since the mock started).
    private func haikuPicks() throws -> Int {
        let requests = try mock("model_pick")["requests"] as? [[String: Any]] ?? []
        return requests.filter {
            $0["method"] as? String == "PUT" && $0["session_id"] as? String == Self.session
                && $0["launcher"] as? String == "claude" && $0["model"] as? String == "haiku"
        }.count
    }

    /// `GET /mock/<path>` on the mock hub, as JSON.
    private func mock(_ path: String) throws -> [String: Any] {
        let data = try Data(contentsOf: URL(string: hub + "/mock/" + path)!)
        return try JSONSerialization.jsonObject(with: data) as? [String: Any] ?? [:]
    }

    /// The whole screen: a menu is drawn in its own window, so XCUIScreen (not the app alone) composites it (the
    /// convention AttachMenuTests uses for the same composer menu).
    @MainActor private func shot(_ app: XCUIApplication, _ scheme: String, _ name: String) throws {
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try? FileManager.default.createDirectory(atPath: dir, withIntermediateDirectories: true)
        closeSystemAlert(timeout: 1)
        try XCUIScreen.main.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: "\(dir)/model-picker-\(name)-\(scheme).png"))
    }
}
