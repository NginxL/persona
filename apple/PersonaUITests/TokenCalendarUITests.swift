import XCTest

/// The token calendar on the assistant's home (#9506): in the demo (DemoHub's made-up days and hours), the card is there
/// where the computers' chips were, a tapped day shows its own tokens and kinds, the line opens Token Usage. Its
/// attachments are the PR's screenshots, light and dark, and at the largest interface text size.
final class TokenCalendarUITests: XCTestCase {
    private var app: XCUIApplication!

    override func setUp() {
        continueAfterFailure = false
    }

    @MainActor private func launch(_ scheme: String, _ extra: [String] = [], textSize: String? = nil) {
        app = XCUIApplication()
        app.launchArguments = ["-demo.on", "YES", "-app.tab", "assistant", "-dev.openChat", "", "-showHomeState", "empty",
                               "-newAssistant", "YES", "-ui.scheme", scheme, "-showSameIcon", "YES"] + extra
            + (textSize.map { ["-UIPreferredContentSizeCategoryName", $0] } ?? [])
        app.launchPinned()
    }

    @MainActor private func shot(_ name: String) {
        let a = XCTAttachment(screenshot: app.screenshot())
        a.name = "\(name) \(UITestStyle.name)"
        a.lifetime = .keepAlways
        add(a)
    }

    @MainActor private var card: XCUIElement { app.otherElements["token-calendar"].firstMatch }
    /// The card's line: today's tokens, or the picked day's (a button that opens Token Usage).
    @MainActor private var line: XCUIElement { app.buttons["token-calendar-line"].firstMatch }

    /// The card under the greeting, today's and the last 30 days' tokens on its line; a tapped day shows its date and
    /// its four kinds; tapped again, today's line is back.
    @MainActor func testTheCardShowsADayWhenTapped() {
        for scheme in ["light", "dark"] {
            launch(scheme)
            XCTAssertTrue(line.waitForExistence(timeout: 30), "\(scheme): no token calendar on the home")
            XCTAssertTrue(line.label.hasPrefix("今天") && line.label.contains("近 30 天"), "\(scheme): the line is \(line.label)")
            let reach = app.staticTexts.matching(NSPredicate(format: "label CONTAINS %@", "我能看你的电脑")).firstMatch
            XCTAssertLessThan(reach.frame.maxY, card.frame.minY + 1, "\(scheme): the card is not under the welcome's words")
            shot("home \(scheme)")

            let grid = app.descendants(matching: .any)["token-calendar-grid"].firstMatch
            XCTAssertTrue(grid.exists && grid.label == "每天的 token 用量", "\(scheme): no grid")
            let spot = grid.coordinate(withNormalizedOffset: CGVector(dx: 0.8, dy: 0.3)) // a few weeks back, early in a week
            spot.tap()
            let picked = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label CONTAINS %@", "token-calendar-line", "缓存读")).firstMatch
            XCTAssertTrue(picked.waitForExistence(timeout: 3), "\(scheme): a tapped day shows no kinds: \(line.label)")
            XCTAssertFalse(line.label.hasPrefix("今天"), "\(scheme): today's line stayed after a tap")
            shot("day picked \(scheme)")
            spot.tap()
            let back = app.buttons.matching(NSPredicate(format: "identifier == %@ AND label BEGINSWITH %@", "token-calendar-line", "今天")).firstMatch
            XCTAssertTrue(back.waitForExistence(timeout: 3), "\(scheme): tapped again, today's line is not back")
            app.terminate()
        }
    }

    /// The line opens Token Usage: the totals, the calendar with its key, the last week by the hour, by computer, by
    /// agent and by kind.
    @MainActor func testTheLineOpensTokenUsage() {
        for scheme in ["light", "dark"] {
            launch(scheme)
            XCTAssertTrue(line.waitForExistence(timeout: 30), "\(scheme): no token calendar on the home")
            line.tap()
            XCTAssertTrue(app.navigationBars["Token 用量"].waitForExistence(timeout: 10), "\(scheme): no Token Usage page")
            XCTAssertTrue(app.staticTexts["最近 7 天 · 每小时"].waitForExistence(timeout: 10), "\(scheme): no hours")
            shot("page \(scheme)")
            app.swipeUp()
            XCTAssertTrue(app.staticTexts["按电脑 · 最近 7 天"].waitForExistence(timeout: 5), "\(scheme): no computers")
            XCTAssertTrue(app.staticTexts["按类别 · 最近 7 天"].exists, "\(scheme): no kinds")
            shot("page lower \(scheme)")
            // By Agent (#10015): the demo's agents, and the usage kept before the hub counted agents apart.
            XCTAssertTrue(app.staticTexts["按 Agent · 最近 7 天"].exists, "\(scheme): no agents")
            XCTAssertTrue(app.staticTexts["Claude Code"].exists && app.staticTexts["未区分（更新前）"].exists,
                          "\(scheme): the agents' names")
            app.terminate()
        }
    }

    /// By Agent at the largest text size the interface reaches (xxxLarge): every row on one line, within the screen.
    @MainActor func testTokenUsageByAgentAtTheLargestInterfaceText() {
        launch("light", textSize: "UICTContentSizeCategoryXXXL")
        XCTAssertTrue(line.waitForExistence(timeout: 30), "no token calendar on the home")
        line.tap()
        XCTAssertTrue(app.navigationBars["Token 用量"].waitForExistence(timeout: 10), "no Token Usage page")
        let heading = app.staticTexts["按 Agent · 最近 7 天"]
        for _ in 0..<4 where !heading.isHittable { app.swipeUp() }
        XCTAssertTrue(heading.waitForExistence(timeout: 5), "no agents")
        let window = app.windows.firstMatch.frame
        let name = app.staticTexts["未区分（更新前）"]
        XCTAssertTrue(name.exists && name.frame.maxX <= window.maxX, "the not-split row runs off the screen: \(name.frame)")
        shot("page agents xxxLarge")
    }

    /// At the largest text size the interface reaches (xxxLarge), the card keeps its squares and fits the screen.
    @MainActor func testTheCardAtTheLargestInterfaceText() {
        launch("light", textSize: "UICTContentSizeCategoryXXXL")
        XCTAssertTrue(line.waitForExistence(timeout: 30), "no token calendar on the home")
        let window = app.windows.firstMatch.frame
        XCTAssertTrue(window.contains(card.frame) || card.frame.maxX <= window.maxX, "the card is wider than the screen: \(card.frame)")
        shot("home xxxLarge")
    }
}
