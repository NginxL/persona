import XCTest

/// Outside reporting directions (#8236) in the real iPhone drawer, against mock_hub.py with
/// MOCK_OUTSIDE_DIRECTIONS=1 (PERSONA_OUTSIDE_DIRECTIONS_HUB). Conversation-backed and outside rows share one ordered
/// list, but only the conversation has conversation actions; a bound outside row opens its exact Agent target and a
/// targetless one stays inert. PERSONA_SHOTS saves the light / dark, default / AX5 captures.
final class OutsideDirectionsUITests: XCTestCase {
    private var hub: String!
    private var app: XCUIApplication!

    private let conversationName = "照片同步" // i18n-ignore: synthetic hub data
    private let boundName = "终端发布与周末迁移的长期方向" // i18n-ignore: synthetic hub data
    private let targetlessName = "只做进度汇报的长期调研方向" // i18n-ignore: synthetic hub data
    private let target = "server/a1f3c2d4-0000-4000-8000-000000000004"

    override func setUpWithError() throws {
        continueAfterFailure = false
        guard let hub = ProcessInfo.processInfo.environment["PERSONA_OUTSIDE_DIRECTIONS_HUB"] else {
            throw XCTSkip("needs MOCK_OUTSIDE_DIRECTIONS=1 mock_hub.py (PERSONA_OUTSIDE_DIRECTIONS_HUB)")
        }
        self.hub = hub
    }

    @MainActor private func launch(look: String = "light", largestText: Bool = false) {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        var args = ["-pairURL", pair, "-tab", "assistant", "-newAssistant", "YES", "-dev.openChat", "",
                    "-ui.scheme", look, "-ui.style", "glass"]
        if largestText { args += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchArguments = args
        app.launchEnvironment["PERSONA_UI_TEST_SESSION"] = "1"
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.alerts.buttons["允许"].firstMatch // i18n-ignore: system notification prompt
        if allow.waitForExistence(timeout: 2) { allow.tap() }
        let end = Date().addingTimeInterval(30)
        while !bound.exists && Date() < end {
            if let menu = app.buttons.matching(identifier: "assistant-sidebar-button").allElementsBoundByIndex.first(where: \.isHittable) {
                menu.tapCenter()
                _ = bound.waitForExistence(timeout: 5)
            } else {
                usleep(500_000)
            }
        }
        XCTAssertTrue(bound.exists, "the bound outside direction is missing:\n\(app.debugDescription)")
        let treeDeadline = Date().addingTimeInterval(15)
        while !bound.label.contains("等你 2") && Date() < treeDeadline { usleep(200_000) } // i18n-ignore
        if !bound.label.contains("等你 2") { shot("outside-tree-timeout") } // i18n-ignore
        XCTAssertTrue(bound.label.contains("等你 2"), "the direction tree did not land: \(bound.label)\n\(app.debugDescription)") // i18n-ignore
    }

    @MainActor private func outside(_ name: String) -> XCUIElement {
        app.descendants(matching: .any).matching(identifier: "sidebar-outside-direction")
            .matching(NSPredicate(format: "label BEGINSWITH %@", name)).firstMatch
    }

    @MainActor private var bound: XCUIElement {
        app.buttons.matching(identifier: "sidebar-outside-direction")
            .matching(NSPredicate(format: "label BEGINSWITH %@", boundName)).firstMatch
    }

    @MainActor private var targetless: XCUIElement { outside(targetlessName) }

    @MainActor private var conversation: XCUIElement {
        app.buttons.matching(identifier: "sidebar-conversation")
            .matching(NSPredicate(format: "label BEGINSWITH %@", conversationName)).firstMatch
    }

    @MainActor private func toggle(_ name: String) -> XCUIElement {
        app.buttons.matching(identifier: "sidebar-direction-toggle")
            .matching(NSPredicate(format: "label BEGINSWITH %@", name)).firstMatch
    }

    @MainActor private func shot(_ name: String) {
        // The scheme-driven icon change is SpringBoard's; never capture its alert over the direction content.
        HomeScreen.dismissIconNote()
        let screen = app.screenshot()
        let attachment = XCTAttachment(screenshot: screen)
        attachment.name = name
        attachment.lifetime = .keepAlways
        add(attachment)
        if let dir = ProcessInfo.processInfo.environment["PERSONA_SHOTS"] {
            try? screen.pngRepresentation.write(to: URL(fileURLWithPath: dir).appendingPathComponent(name + ".png"))
        }
    }

    @MainActor private func showTargetless() {
        let scroller = app.collectionViews.matching(identifier: "assistant-sidebar").firstMatch
        XCTAssertTrue(scroller.exists, "the sidebar's collection view is missing")
        for _ in 0..<8 where !targetless.exists {
            scroller.swipeUp(velocity: .slow)
            usleep(300_000)
        }
        XCTAssertTrue(targetless.waitForExistence(timeout: 5), "the targetless row did not come into view:\n\(app.debugDescription)")
    }

    @MainActor private func dismissConversationMenu() {
        let rename = app.buttons["重命名…"].firstMatch // i18n-ignore: existing localized menu item
        app.windows.firstMatch.coordinate(withNormalizedOffset: CGVector(dx: 0.95, dy: 0.08)).tap()
        XCTAssertTrue(rename.waitForNonExistence(timeout: 5), "the conversation menu did not close")
    }

    @MainActor private func assertNoConversationMenu(_ what: String) {
        let rename = app.buttons["重命名…"].firstMatch // i18n-ignore: existing localized menu item
        XCTAssertFalse(rename.waitForExistence(timeout: 1), "\(what) opened Rename:\n\(app.debugDescription)")
        for label in ["归档", "拷贝会话 ID", "拷贝诊断", "取消常驻", "设为常驻", "结束…"] { // i18n-ignore
            XCTAssertFalse(app.buttons[label].exists, "\(what) exposed \(label)")
        }
    }

    @MainActor func testMixedRowsAndNoConversationActions() {
        launch()
        XCTAssertTrue(conversation.exists)
        XCTAssertTrue(targetless.exists)
        XCTAssertLessThan(conversation.frame.minY, bound.frame.minY, "the hub's conversation → bound → targetless order changed")
        XCTAssertLessThan(bound.frame.minY, targetless.frame.minY, "the targetless direction left the hub order")
        XCTAssertTrue(bound.label.contains("等你 2"), "the tree's needs-you count is missing: \(bound.label)") // i18n-ignore
        XCTAssertTrue(bound.label.contains("工作中"), "the tree's running mark is not spoken: \(bound.label)") // i18n-ignore
        XCTAssertFalse(bound.label.contains("在做"), "a working count appeared beside the spinner: \(bound.label)") // i18n-ignore
        XCTAssertNotEqual(targetless.elementType, .button, "a targetless direction became a button")
        XCTAssertFalse(toggle(targetlessName).exists, "a targetless direction has a chevron")
        XCTAssertTrue(toggle(boundName).exists, "a bound direction lost its tree disclosure")
        for row in [bound, targetless] {
            XCTAssertFalse(row.label.contains("dir_mock"), "a raw direction id is visible: \(row.label)")
            XCTAssertFalse(row.label.contains(target), "a raw Agent target is visible: \(row.label)")
        }

        // Control: the conversation-backed row really does have the menu the two outside kinds must not inherit.
        conversation.press(forDuration: 1.2)
        XCTAssertTrue(app.buttons["重命名…"].firstMatch.waitForExistence(timeout: 3), // i18n-ignore
                      "the conversation-menu control did not open:\n\(app.debugDescription)")
        XCTAssertTrue(app.buttons["取消常驻"].exists, "the control did not expose Unpin") // i18n-ignore
        XCTAssertTrue(app.buttons["拷贝会话 ID"].exists, "the control did not expose Copy Session ID") // i18n-ignore
        dismissConversationMenu()

        targetless.press(forDuration: 1.2)
        assertNoConversationMenu("the targetless direction")

        let welcome = app.descendants(matching: .any)["assistant-welcome"].firstMatch
        XCTAssertTrue(welcome.exists)
        targetless.tap()
        sleep(1)
        XCTAssertTrue(welcome.exists, "a targetless tap opened a conversation")
        XCTAssertTrue(targetless.exists, "a targetless tap dismissed the drawer")
        bound.press(forDuration: 1.2)
        assertNoConversationMenu("the bound direction")
    }

    @MainActor func testBoundTapOpensTheExistingAgent() {
        launch()
        bound.tap()
        let title = app.buttons.matching(identifier: "chat.title").firstMatch
        XCTAssertTrue(title.waitForExistence(timeout: 15), "the bound Agent did not open:\n\(app.debugDescription)")
        let end = Date().addingTimeInterval(10)
        while (title.value as? String) != target && Date() < end { usleep(200_000) }
        XCTAssertEqual(title.value as? String, target, "the row opened a Persona conversation or the wrong Agent")
    }

    @MainActor private func launchRoute(_ route: [String], look: String, largestText: Bool) {
        app?.terminate()
        app = XCUIApplication()
        let pair = "persona://pair?url=" + hub.addingPercentEncoding(withAllowedCharacters: .alphanumerics)! + "&token=mock"
        var args = ["-pairURL", pair, "-dev.openChat", "", "-ui.scheme", look, "-ui.style", "glass"] + route
        if largestText { args += ["-UIPreferredContentSizeCategoryName", "UICTContentSizeCategoryAccessibilityXXXL"] }
        app.launchArguments = args
        app.launchPinned()
        let springboard = XCUIApplication(bundleIdentifier: "com.apple.springboard")
        let allow = springboard.alerts.buttons["允许"].firstMatch // i18n-ignore: system notification prompt
        if allow.waitForExistence(timeout: 2) { allow.tap() }
    }

    @MainActor private func directionHeader(_ name: String, id: String) -> XCUIElement {
        let all = app.descendants(matching: .any)
        let rows = id.isEmpty ? all : all.matching(identifier: id)
        return rows.matching(NSPredicate(format: "label BEGINSWITH %@", name)).firstMatch
    }

    @MainActor private func revealDirection(_ name: String, id: String) {
        let header = directionHeader(name, id: id)
        _ = header.waitForExistence(timeout: 15)
        for _ in 0..<8 where !header.exists {
            app.swipeUp(velocity: .slow)
            _ = header.waitForExistence(timeout: 1)
        }
        XCTAssertTrue(header.exists, "the \(name) direction section is missing:\n\(app.debugDescription)")
    }

    @MainActor func testLightDarkAndLargestTextCaptures() {
        for look in ["light", "dark"] {
            for largest in [false, true] {
                launch(look: look, largestText: largest)
                let variant = look + (largest ? "-ax5" : "-default")
                if largest {
                    shot("outside-directions-" + variant + "-top")
                    showTargetless()
                }
                shot("outside-directions-" + variant)
                XCTAssertTrue(conversation.exists)
                XCTAssertTrue(targetless.exists)
                XCTAssertNotEqual(targetless.elementType, .button)
                XCTAssertFalse(toggle(targetlessName).exists)
            }
        }
    }

    @MainActor func testOtherDirectionListCaptures() {
        let pages: [(name: String, id: String, route: [String])] = [
            ("todos", "todos.direction", ["-tab", "assistant", "-openAssistantPage", "todos",
                                          "-todos.view", "list", "-todos.grouping", "direction"]),
            ("agents", "", ["-app.tab", "chats", "-tab", "dev", "-dev.grouping", "direction",
                             "-openAgentsFresh", "YES"]),
        ]
        for page in pages {
            for look in ["light", "dark"] {
                for largest in [false, true] {
                    launchRoute(page.route, look: look, largestText: largest)
                    let variant = look + (largest ? "-ax5" : "-default")
                    shot("outside-" + page.name + "-" + variant + "-top")
                    revealDirection(boundName, id: page.id)
                    shot("outside-" + page.name + "-" + variant)
                    let boundHeader = directionHeader(boundName, id: page.id)
                    XCTAssertTrue(boundHeader.label.contains("等你 2"), "the section lost the tree count: \(boundHeader.label)") // i18n-ignore
                    XCTAssertFalse(directionHeader(targetlessName, id: page.id).exists,
                                   "an empty targetless direction created an empty task section")
                }
            }
        }
    }
}
