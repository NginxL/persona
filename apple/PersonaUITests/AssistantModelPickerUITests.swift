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
        if hide.waitForExistence(timeout: 5) { hide.tap() }
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
