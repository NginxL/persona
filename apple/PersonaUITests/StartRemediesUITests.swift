import XCTest

/// Visual and interaction evidence for the #10365 remedies, against scripts/mock/mock_hub.py.
/// PERSONA_MOCK_HUB selects the isolated mock and PERSONA_SHOTS keeps the PNGs outside the repository.
/// Run the task/session/tool-search tests with the populated mock; run testModelRouteAndSavedAlias with
/// MOCK_READINESS=model_route MOCK_MODEL_PICK=1 MOCK_MODEL_GATEWAY=1 and a saved `opus` pick on its main chat.
final class StartRemediesUITests: XCTestCase {
    private var hub = ""
    private let populatedMain = "server/b2e4d6f8-0000-4000-8000-000000000001"
    private let readinessMain = "mbp/b2e4d6f8-0000-4000-8000-0000000000aa"
    private let sizes = [
        ("default", [String]()),
        ("largest", ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"]),
    ]

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] else {
            throw XCTSkip("needs an isolated scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
        self.hub = hub
        _ = try Data(contentsOf: URL(string: hub + "/mock/hits")!)
    }

    @MainActor func testLauncherFailureAndAlternativeAtBothTextSizes() throws {
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                let app = launchTask("todo_remedy_launcher", scheme: scheme, sizeArgs: sizeArgs)
                XCTAssertTrue(app.buttons["task.retry-launcher"].waitForExistence(timeout: 25), app.debugDescription)
                XCTAssertTrue(app.buttons["task.retry"].exists, app.debugDescription)
                reveal(app.staticTexts["The agent didn't start"], in: app)
                try shot(app, "launcher-task", scheme: scheme, size: size)
                app.terminate()
            }
        }
    }

    @MainActor func testWorktreeFailureWithoutPlanIDAtBothTextSizes() throws {
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                let app = launchTask("todo_remedy_worktree", scheme: scheme, sizeArgs: sizeArgs)
                XCTAssertTrue(app.buttons["task.retry"].waitForExistence(timeout: 25), app.debugDescription)
                XCTAssertFalse(app.buttons["task.retry-launcher"].exists, app.debugDescription)
                reveal(app.staticTexts["A workspace from an earlier try is still in the way"], in: app)
                try shot(app, "worktree-task", scheme: scheme, size: size)
                app.terminate()
            }
        }
    }

    @MainActor func testTaskStartupQuestionAtBothTextSizes() throws {
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                let app = launchTask("todo_remedy_question", scheme: scheme, sizeArgs: sizeArgs)
                let answer = app.buttons["readiness-choice-q"].firstMatch
                XCTAssertTrue(answer.waitForExistence(timeout: 25), app.debugDescription)
                reveal(answer, in: app)
                try shot(app, "task-question", scheme: scheme, size: size)
                app.terminate()
            }
        }
    }

    @MainActor func testSessionStartupQuestionAtBothTextSizes() throws {
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                let app = launch(["-app.tab", "dev", "-tab", "dev", "-openSession", "macbook/a1f3c2d4-0000-4000-8000-000000000104"],
                                 scheme: scheme, sizeArgs: sizeArgs)
                let answer = app.buttons["readiness-choice-no"].firstMatch
                XCTAssertTrue(answer.waitForExistence(timeout: 30), app.debugDescription)
                XCTAssertTrue(app.buttons["readiness-choice-yes"].exists, app.debugDescription)
                try shot(app, "session-question", scheme: scheme, size: size)
                app.terminate()
            }
        }
    }

    @MainActor func testToolSearchRetryCardAtBothTextSizes() throws {
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                let app = launch(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", populatedMain], scheme: scheme, sizeArgs: sizeArgs)
                let title = app.staticTexts["Model not connected: this gateway can't load tools on demand"].firstMatch
                XCTAssertTrue(title.waitForExistence(timeout: 30), app.debugDescription)
                reveal(title, in: app)
                XCTAssertTrue(app.buttons["Retry"].firstMatch.exists, app.debugDescription)
                try shot(app, "tool-search", scheme: scheme, size: size)
                app.terminate()
            }
        }
    }

    @MainActor func testModelRouteAndSavedAliasAtBothTextSizes() throws {
        for (size, sizeArgs) in sizes {
            for scheme in ["light", "dark"] {
                let app = launch(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", readinessMain], scheme: scheme, sizeArgs: sizeArgs)
                let fix = app.buttons["Fix it"].firstMatch
                XCTAssertTrue(fix.waitForExistence(timeout: 30), app.debugDescription)
                XCTAssertTrue(app.buttons["Follow default"].firstMatch.exists, app.debugDescription)
                reveal(fix, in: app)
                try shot(app, "model-route-card", scheme: scheme, size: size)
                fix.tap()
                let folded = app.buttons["assistantModel.gateway-api.claude-opus-5-5"].firstMatch
                XCTAssertTrue(folded.waitForExistence(timeout: 20), app.debugDescription)
                XCTAssertTrue(app.buttons["Follow default"].firstMatch.exists, app.debugDescription)
                reveal(folded, in: app)
                RunLoop.current.run(until: Date().addingTimeInterval(0.8)) // Let the sheet finish opening before the capture.
                try shot(app, "model-route-alias-picker", scheme: scheme, size: size)
                app.terminate()
            }
        }
    }

    @MainActor private func launchTask(_ id: String, scheme: String, sizeArgs: [String]) -> XCUIApplication {
        launch(["-app.tab", "assistant", "-tab", "assistant", "-openAssistant", populatedMain, "-showTaskDetail", id], scheme: scheme, sizeArgs: sizeArgs)
    }

    @MainActor private func launch(_ arguments: [String], scheme: String, sizeArgs: [String]) -> XCUIApplication {
        let app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-dev.openChat", "", "-openDraftsFresh", "YES",
                               "-AppleLanguages", "(en)", "-AppleLocale", "en_US", "-ui.scheme", scheme]
            + arguments + sizeArgs
        app.launchPinned()
        dismissSystemAlerts()
        return app
    }

    @MainActor private func reveal(_ element: XCUIElement, in app: XCUIApplication) {
        for _ in 0..<6 where !(element.exists && element.isHittable) { app.swipeUp() }
    }

    @MainActor private func dismissSystemAlerts() {
        let board = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        guard board.alerts.firstMatch.waitForExistence(timeout: 2) else { return }
        for label in ["OK", "好", "Don't Allow", "Don’t Allow", "不允许"] where board.alerts.buttons[label].exists { // i18n-ignore: the system's own buttons
            board.alerts.buttons[label].tap()
            RunLoop.current.run(until: Date().addingTimeInterval(0.4)) // Let the system's dismissal finish before a capture.
            return
        }
    }

    @MainActor private func shot(_ app: XCUIApplication, _ name: String, scheme: String, size: String) throws {
        dismissSystemAlerts() // Some system permission/icon alerts arrive after launch and before the remedy is visible.
        let image = XCUIScreen.main.screenshot()
        let attachment = XCTAttachment(screenshot: image)
        attachment.name = "\(name)-\(scheme)-\(size)"
        attachment.lifetime = .keepAlways
        add(attachment)
        guard let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] else { return }
        try image.pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: "\(name)-\(scheme)-\(size).png"))
    }
}
