import Foundation
import Testing
@testable import Persona

/// SPACE.md in the editor (#13500, `SpaceMarkdown`, the web's lib/space-md.test.ts): the user's parts are fields, Persona's
/// blocks go back byte for byte, whatever the text's line ends or normalization.
struct SpaceMarkdownTests {
    private static let state = "<!-- persona:managed:state:begin -->\nTaxes filed; waiting for the refund.\n<!-- persona:managed:state:end -->"
    private static let file = "# Money\n\nWhat this space is for.\n\n\(state)\n"

    private static func bytes(_ s: String) -> [UInt8] { Array(s.utf8) }

    @Test func splitsAndJoinsBackUnchanged() {
        let parts = SpaceMarkdown.split(Self.file)
        #expect(parts.map(\.isManaged) == [false, true, false])
        #expect(parts[1] == .managed(name: "state", text: Self.state, body: "\nTaxes filed; waiting for the refund.\n"))
        #expect(Self.bytes(SpaceMarkdown.join(parts)) == Self.bytes(Self.file))
    }

    @Test func aBlockWithoutItsEndRunsToTheEnd() {
        let file = "notes\n<!-- persona:managed:state:begin -->\nhalf"
        let parts = SpaceMarkdown.split(file)
        #expect(parts.map(\.isManaged) == [false, true])
        #expect(Self.bytes(SpaceMarkdown.join(parts)) == Self.bytes(file))
    }

    @Test func aMarkerWithoutANameIsText() {
        let file = "<!-- persona:managed::begin -->x"
        #expect(SpaceMarkdown.split(file) == [.human(file)])
    }

    @Test func thePartsWithWordsAreFields() {
        #expect(SpaceMarkdown.editableParts(SpaceMarkdown.split(Self.file)).editable == [0])
        let onlyBlock = SpaceMarkdown.editableParts(SpaceMarkdown.split(Self.state + "\n"))
        #expect(onlyBlock.editable == [0] && onlyBlock.parts[0] == .human(""))
        #expect(SpaceMarkdown.editableParts(SpaceMarkdown.split("")).parts == [.human("")])
    }

    @Test func editsGoInAndBlocksComeBackByteForByte() {
        let parts = SpaceMarkdown.split(Self.file)
        let out = SpaceMarkdown.assemble(parts, edits: [0: "# Money\n\nBudget and taxes.\n\n"])
        #expect(Self.bytes(out) == Self.bytes("# Money\n\nBudget and taxes.\n\n\(Self.state)\n"))
        #expect(SpaceMarkdown.sameManagedBlocks(Self.file, out))
    }

    @Test func aFieldRightBeforeABlockKeepsTheMarkerOnItsOwnLine() {
        let shape = SpaceMarkdown.editableParts(SpaceMarkdown.split(Self.state))
        #expect(Self.bytes(SpaceMarkdown.assemble(shape.parts, edits: [0: "Family notes"])) == Self.bytes("Family notes\n\n\(Self.state)"))
        // a field ending in CRLF already ends its line: nothing added
        #expect(Self.bytes(SpaceMarkdown.assemble(shape.parts, edits: [0: "Family notes\r\n"])) == Self.bytes("Family notes\r\n\(Self.state)"))
    }

    @Test func aChangedBlockOrATypedMarkerIsNoticed() {
        #expect(!SpaceMarkdown.sameManagedBlocks(Self.file, Self.file.replacingOccurrences(of: "refund", with: "check")))
        #expect(!SpaceMarkdown.sameManagedBlocks(Self.file, "<!-- persona:managed:state:begin -->x<!-- persona:managed:state:end -->" + Self.file))
        #expect(!SpaceMarkdown.sameManagedBlocks(Self.file, Self.file.replacingOccurrences(of: Self.state, with: "")))
    }

    @Test func crlfAndDecomposedTextComeBackAsTheyWere() {
        // "é" as e + combining acute (NFD), and CRLF line ends: Swift's String == would call an NFC copy equal.
        let nfd = "Caf\u{65}\u{301}\r\n\r\n"
        let block = "<!-- persona:managed:state:begin -->\r\nnoted caf\u{65}\u{301}\r\n<!-- persona:managed:state:end -->"
        let file = nfd + block + "\r\n"
        let parts = SpaceMarkdown.split(file)
        #expect(Self.bytes(SpaceMarkdown.join(parts)) == Self.bytes(file))
        #expect(Self.bytes(SpaceMarkdown.assemble(parts, edits: [:])) == Self.bytes(file))
        let nfc = file.precomposedStringWithCanonicalMapping
        #expect(nfc == file, "Swift calls them equal")
        #expect(!SpaceMarkdown.sameManagedBlocks(file, nfc), "the bytes differ, as the hub sees them")
    }

    @Test func theHomeShowsOnlyWhatTheUserWrote() {
        #expect(SpaceMarkdown.human(Self.file) == "# Money\n\nWhat this space is for.")
        #expect(SpaceMarkdown.human(Self.state + "\n") == "")
        #expect(SpaceMarkdown.human("Before\n\n\(Self.state)\n\nAfter\n") == "Before\n\nAfter")
    }

    @Test func aLeadingTitleThatRepeatsTheNameIsDropped() {
        #expect(SpaceMarkdown.body("# Money\n\nWhat this space is for.\n", title: "money") == "What this space is for.")
        #expect(SpaceMarkdown.body("# Bills\n\nWhat this space is for.\n", title: "Money") == "# Bills\n\nWhat this space is for.")
        #expect(SpaceMarkdown.body("## Money\n\nText\n", title: "Money") == "## Money\n\nText")
        #expect(SpaceMarkdown.body("# Money\n", title: "Money") == "")
        #expect(SpaceMarkdown.body("# Money ##\n\nText", title: "Money") == "Text")
        #expect(SpaceMarkdown.body("#Money\n\nText", title: "Money") == "#Money\n\nText")
        // as the web's spaceMdBody, whose `.` stops at a carriage return: a CRLF heading line is kept
        #expect(SpaceMarkdown.body("# Money\r\n\r\nText", title: "Money") == "# Money\r\n\r\nText")
    }
}
