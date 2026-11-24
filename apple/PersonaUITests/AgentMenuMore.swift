import XCTest

extension XCUIApplication {
    /// The Agent menu's More (#13396): its top level keeps Interrupt, Mark as Seen, Rename, Pin and Close, and every other
    /// item of a row's long-press menu and the chat's ⋯ is in More. Opens it, the menu being open; its items replace the
    /// top level's. Not the chat's own ⋯ (`chat.more`), which has the same name.
    @MainActor func openAgentMenuMore(file: StaticString = #filePath, line: UInt = #line) {
        let byID = buttons["agent-menu-more"].firstMatch
        let more = byID.waitForExistence(timeout: 3) ? byID
            : buttons.matching(NSPredicate(format: "label IN %@ AND identifier != %@", ["更多", "More", "その他"], "chat.more")).firstMatch
        XCTAssertTrue(more.waitForExistence(timeout: 5), "no More in the Agent menu:\n\(debugDescription)", file: file, line: line)
        more.tap()
    }
}
