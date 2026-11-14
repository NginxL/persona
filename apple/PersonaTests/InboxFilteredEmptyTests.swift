import XCTest
@testable import Persona

/// What the Inbox says with nothing open (#7551, the web's rule since #7478): the seal and All Caught Up only for the
/// whole Inbox; under a source or kind chip the neutral "Nothing open matches these filters" with Clear Filters, and under
/// a search alone "No matches" with Clear Search.
final class InboxFilteredEmptyTests: XCTestCase {
    func testTheWholeInboxIsCaughtUp() {
        XCTAssertEqual(InboxPage.emptyKind(source: "", kind: "", query: ""), .caughtUp)
    }

    func testAChipLeavingNothingOpenIsFiltered() {
        XCTAssertEqual(InboxPage.emptyKind(source: "acc1", kind: "group", query: ""), .filtered, "a mailbox with Group chat")
        XCTAssertEqual(InboxPage.emptyKind(source: "feishu", kind: "", query: ""), .filtered, "a source alone")
        XCTAssertEqual(InboxPage.emptyKind(source: "", kind: "mention", query: ""), .filtered, "a kind alone")
        XCTAssertEqual(InboxPage.emptyKind(source: "", kind: "mail", query: "周报"), .filtered, "a chip and a search: the filters")
    }

    func testASearchAloneHasNoMatches() {
        XCTAssertEqual(InboxPage.emptyKind(source: "", kind: "", query: "周报"), .noMatch)
    }
}
