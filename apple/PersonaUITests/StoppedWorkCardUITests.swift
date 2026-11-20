import XCTest

/// A stopped Agent's manager work card keeps its local defer choices and dismiss usable, while a choice that would
/// message the Agent and the custom-answer field stay unavailable (#6839). Runs against the plain mock hub.
final class StoppedWorkCardUITests: XCTestCase {
    private var app: XCUIApplication!
    private static let chat = "macbook/a1f3c2d4-0000-4000-8000-000000000007"
    private static let cardID = "sample-work"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"] != nil else {
            throw XCTSkip("needs scripts/mock/mock_hub.py (PERSONA_MOCK_HUB)")
        }
    }

    private func launch(style: String, scheme: String = "light", largest: Bool = false) {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        _ = try? Data(contentsOf: URL(string: hub + "/mock/reset")!)
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        app.launchArguments = ["-pairURL", pair, "-app.tab", "chats", "-dev.openChat", Self.chat, "-tab", "dev",
                               "-openAgentsFresh", "YES", "-openChatsFresh", "YES", "-showSampleStoppedWork", "YES",
                               "-showSameIcon", "YES", "-ui.style", style, "-ui.scheme", scheme]
        if largest { app.launchArguments += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchPinned()
        XCTAssertTrue(app.staticTexts["interaction-local-only"].waitForExistence(timeout: 20),
                      "the stopped work card and its explanation are visible (\(style), \(scheme), largest: \(largest))")
    }

    private func option(_ label: String) -> XCUIElement {
        app.buttons.matching(NSPredicate(format: "label BEGINSWITH %@", label)).firstMatch
    }

    private func resolutions() -> [[String: Any]] {
        let hub = ProcessInfo.processInfo.environment["PERSONA_MOCK_HUB"]!
        guard let data = try? Data(contentsOf: URL(string: hub + "/mock/resolved")!),
              let object = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let rows = object["resolved"] as? [[String: Any]] else { return [] }
        return rows.filter { $0["id"] as? String == Self.cardID }
    }

    private func waitForResolution(after count: Int, file: StaticString = #filePath, line: UInt = #line) -> [String: Any]? {
        let deadline = Date().addingTimeInterval(15)
        while Date() < deadline {
            let rows = resolutions()
            if rows.count > count { return rows.last }
            RunLoop.current.run(until: Date().addingTimeInterval(0.1))
        }
        XCTFail("the stopped work card sent no new resolution", file: file, line: line)
        return nil
    }

    private func shot(_ name: String) {
        let attachment = XCTAttachment(screenshot: app.screenshot())
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? app.screenshot().pngRepresentation.write(to: URL(fileURLWithPath: dir).appending(path: name + ".png"))
        }
    }

    func testStoppedWorkControlsInBothStylesAndLooks() {
        continueAfterFailure = true
        for style in ["glass", "beautiful"] {
            for (scheme, largest) in [("light", false), ("dark", false), ("light", true), ("dark", true)] {
                let look = scheme + (largest ? "-largest" : "-default")
                launch(style: style, scheme: scheme, largest: largest)
                XCTAssertTrue(option("继续").exists, "Continue is still shown, but disabled")
                XCTAssertFalse(option("继续").isEnabled, "Continue would message the stopped Agent")
                XCTAssertTrue(option("稍后再说").isEnabled, "the first defer choice is enabled")
                XCTAssertTrue(option("不用管了").isEnabled, "the second defer choice is enabled")
                let custom = app.descendants(matching: .any).matching(NSPredicate(
                    format: "label BEGINSWITH %@ OR placeholderValue BEGINSWITH %@", "其他回答", "其他回答")).firstMatch
                XCTAssertFalse(custom.exists, "a typed answer would message the stopped Agent")
                shot("stopped-work-\(style)-\(look)")
                app.terminate()
            }
        }
    }

    func testDeferAndDismissReachTheHubInBothStyles() {
        for style in ["glass", "beautiful"] {
            launch(style: style)
            let beforeDefer = resolutions().count
            option("稍后再说").tap()
            let deferred = waitForResolution(after: beforeDefer)
            let resolution = deferred?["resolution"] as? [String: Any]
            let answers = resolution?["answers"] as? [String: Any]
            XCTAssertEqual(answers?["模型请求失败了，要怎么处理？"] as? String, "稍后再说")
            app.terminate()

            launch(style: style)
            let beforeDismiss = resolutions().count
            app.buttons["interaction-menu"].tap()
            app.buttons["交给电脑上的终端处理"].tap()
            let dismissed = waitForResolution(after: beforeDismiss)
            let body = dismissed?["resolution"] as? [String: Any]
            XCTAssertEqual(body?["dismiss"] as? Bool, true)
            app.terminate()
        }
    }
}
