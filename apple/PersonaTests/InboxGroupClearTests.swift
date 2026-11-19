import XCTest
@testable import Persona
import PersonaWire

/// A group title's pull-down (#10422, docs/assistant/inbox.md "Group titles"): FYI's Mark All Seen and Collapsed's Clear
/// All for the group or the whole section, with the counts the titles show and, under a filter or a search, what "all"
/// covers. None for Needs You and In Progress. The web's InboxList.group-clear.test.tsx checks the same rules.
final class InboxGroupClearTests: XCTestCase {
    private func item(_ id: String, source: String = "lark") throws -> InboxItem {
        let json = """
        {"id":"\(id)","source":"\(source)","kind":"dm","account":"a","account_name":"A","title":"\(id)","preview":"","count":1}
        """
        return try JSON.decoder.decode(InboxItem.self, from: Data(json.utf8))
    }

    private func setup() throws -> (group: InboxGroups.Group, section: [InboxItem]) {
        let mail = [try item("m1", source: "mail"), try item("m2", source: "mail")]
        let section = mail + [try item("l1"), try item("l2"), try item("l3")]
        return (InboxGroups.Group(key: "mail:a", title: "Mail · me@example.com", items: mail), section)
    }

    func testFYIOffersTheGroupThenTheWholeSection() throws {
        let (group, section) = try setup()
        let clear = try XCTUnwrap(InboxGroupClear.make(.fyi, group: group, section: section, filters: [], query: ""))
        XCTAssertEqual(clear.label, InboxPage.clearTitle(.fyi), "the section's own words on the button")
        XCTAssertEqual(clear.choices.map(\.id), ["group", "section"])
        XCTAssertEqual(clear.choices[0].items.map(\.id), ["m1", "m2"], "the group's items only")
        XCTAssertEqual(clear.choices[1].items.map(\.id), ["m1", "m2", "l1", "l2", "l3"], "every item the section shows")
        XCTAssertEqual(clear.choices[0].title, InboxGroupClear.title(.fyi, count: 2, in: "Mail · me@example.com"))
        XCTAssertEqual(clear.choices[1].title, InboxGroupClear.title(.fyi, count: 5, in: InboxItem.Tier.fyi.title))
        XCTAssertNil(clear.scope, "the whole Inbox: nothing to say under the choices")
    }

    func testTheTitlesNameTheCountAndTheGroup() throws {
        let fyi = try XCTUnwrap(InboxGroupClear.title(.fyi, count: 4, in: "Assistant"))
        XCTAssertTrue(fyi.contains("4") && fyi.contains("Assistant"), fyi)
        let noise = try XCTUnwrap(InboxGroupClear.title(.noise, count: 12, in: "Lark"))
        XCTAssertTrue(noise.contains("12") && noise.contains("Lark"), noise)
        XCTAssertNotEqual(fyi, InboxGroupClear.title(.noise, count: 4, in: "Assistant"), "each section its own verb")
    }

    func testCollapsedClearsInsteadOfMarkingSeen() throws {
        let (group, section) = try setup()
        let clear = try XCTUnwrap(InboxGroupClear.make(.noise, group: group, section: section, filters: [], query: ""))
        XCTAssertEqual(clear.label, InboxPage.clearTitle(.noise))
        XCTAssertEqual(clear.choices[1].title, InboxGroupClear.title(.noise, count: 5, in: InboxItem.Tier.noise.title))
    }

    func testNeedsYouAndInProgressHaveNone() throws {
        let (group, section) = try setup()
        XCTAssertNil(InboxGroupClear.make(.action, group: group, section: section, filters: [], query: ""), "Needs You is handled one by one")
        XCTAssertNil(InboxGroupClear.make(.working, group: group, section: section, filters: [], query: ""), "the AI is on In Progress")
        XCTAssertNil(InboxGroupClear.title(.action, count: 2, in: "x"))
    }

    func testAGroupThatIsTheWholeSectionHasNone() throws {
        let (group, _) = try setup()
        XCTAssertNil(InboxGroupClear.make(.fyi, group: group, section: group.items, filters: [], query: ""),
                     "one choice: the section's own button acts at once")
        let empty = InboxGroups.Group(key: "lark", title: "Lark", items: [])
        XCTAssertNil(InboxGroupClear.make(.fyi, group: empty, section: try setup().section, filters: [], query: ""))
    }

    func testFiltersAndSearchSayWhatAllCovers() throws {
        let (group, section) = try setup()
        let filtered = try XCTUnwrap(InboxGroupClear.make(.fyi, group: group, section: section, filters: ["@Me"], query: ""))
        XCTAssertEqual(filtered.scope, InboxBulk.scope(filters: ["@Me"], query: ""), "Clean up all's words for the same scope")
        let searched = try XCTUnwrap(InboxGroupClear.make(.fyi, group: group, section: section, filters: [], query: " report "))
        XCTAssertEqual(searched.scope, InboxBulk.scope(filters: [], query: "report"))
        let blank = try XCTUnwrap(InboxGroupClear.make(.fyi, group: group, section: section, filters: [""], query: "  "))
        XCTAssertNil(blank.scope, "blank filters and a blank search are the whole Inbox")
    }
}
