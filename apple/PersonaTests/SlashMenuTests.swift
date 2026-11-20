import BeautifulUI
import Testing
@testable import Persona

/// The "/" list over the composer (`SlashMenuRow`): the web's rows in the web's order, filtered as the web filters them.
@MainActor
struct SlashMenuTests {
    @Test func rowsFollowTheWebsOrder() {
        let words = SlashMenuRow.rows(model: true, clear: true).map(\.word)
        #expect(words == ["/继续", "/提交", "/汇报", "/compact", "/context", "/model", "/clear"])
        #expect(SlashMenuRow.rows(model: false, clear: false).map(\.word) == ["/继续", "/提交", "/汇报", "/compact", "/context"])
        // /loop between /model and /clear, as on the web, only where it is offered (a Claude agent)
        #expect(SlashMenuRow.rows(model: true, loop: true, clear: true).map(\.word)
            == ["/继续", "/提交", "/汇报", "/compact", "/context", "/model", "/loop", "/clear"])
        #expect(SlashMenuRow.matching("/lo", in: SlashMenuRow.rows(model: true, loop: true, clear: true)).map(\.text) == ["/loop "])
    }

    @Test func onlyALoneSlashWordOpensTheList() {
        let rows = SlashMenuRow.rows(model: true, clear: true)
        #expect(SlashMenuRow.matching("/", in: rows).count == rows.count)
        #expect(SlashMenuRow.matching("", in: rows).isEmpty)
        #expect(SlashMenuRow.matching("看下 /co", in: rows).isEmpty)
        #expect(SlashMenuRow.matching("/model sonnet", in: rows).isEmpty)
        #expect(SlashMenuRow.matching("/nothing", in: rows).isEmpty)
    }

    @Test func filtersByTheWordOrItsDescription() {
        let rows = SlashMenuRow.rows(model: true, clear: true)
        #expect(SlashMenuRow.matching("/co", in: rows).map(\.word) == ["/compact", "/context"])
        #expect(SlashMenuRow.matching("/CL", in: rows).map(\.word) == ["/clear"])
        #expect(SlashMenuRow.matching("/汇", in: rows).map(\.word) == ["/汇报"])
        #expect(SlashMenuRow.matching("/上下文", in: rows).map(\.word) == ["/compact", "/context"])
    }

    @Test func theMainChatKeepsItsCommandsAndAddsClear() {
        let had = SlashMenuRow.rows(model: false, loop: false, clear: false).map(\.word)
        let rows = SlashMenuRow.slashRows(draft: "/", picked: nil, showsList: true, model: false, loop: false, agentClear: false, mainClear: true)
        #expect(rows.map(\.word) == had + ["/clear"])
        #expect(rows.contains { $0.word == "/clear" })
        #expect(had.allSatisfy { word in rows.contains { $0.word == word } })
        let withLoop = SlashMenuRow.slashRows(draft: "/", picked: nil, showsList: true, model: false, loop: true, agentClear: false, mainClear: true)
        #expect(withLoop.map(\.word) == SlashMenuRow.rows(model: false, loop: true, clear: false).map(\.word) + ["/clear"])
        #expect(SlashMenuRow.slashRows(draft: "/cl", picked: nil, showsList: true, model: false, loop: false, agentClear: false, mainClear: true).map(\.word) == ["/clear"])
        #expect(SlashMenuRow.slashRows(draft: "/clear now", picked: nil, showsList: true, model: false, loop: false, agentClear: false, mainClear: true).isEmpty)
        #expect(SlashMenuRow.slashRows(draft: "/", picked: "/", showsList: true, model: false, loop: false, agentClear: false, mainClear: true).isEmpty)
        // The prompt bar has its own menu, so this list stays down.
        #expect(SlashMenuRow.slashRows(draft: "/", picked: nil, showsList: false, model: false, loop: false, agentClear: false, mainClear: true).isEmpty)
        #expect(SlashMenuRow.slashRows(draft: "/", picked: nil, showsList: true, model: true, loop: true, agentClear: true, mainClear: false).map(\.word)
            == ["/继续", "/提交", "/汇报", "/compact", "/context", "/model", "/loop", "/clear"])
    }

    @Test func aQuickMessageShowsWhatItSends() {
        let rows = SlashMenuRow.rows(model: true, clear: true)
        #expect(rows.first { $0.word == "/提交" }?.preview == "把当前改动按规范提交并推送")
        #expect(rows.first { $0.word == "/compact" }?.preview == nil)
        #expect(SlashMenuRow.model.preview == nil)
    }

    /// The prompt bar runs Persona's own commands by id (#11410): a saved prompt named like one types its text, and
    /// `/permissions` opens the mode list only where it is offered.
    @Test func theBarRunsOnlyPersonasOwnCommands() throws {
        let own = PromptItems.commands(clear: true, loop: true, permissions: true)
        func row(_ name: String) throws -> PromptCommand { try #require(own.first { $0.name == name }) }
        #expect(PromptItems.ownCommand(try row("/loop"), permissions: true) == .loop)
        #expect(PromptItems.ownCommand(try row("/permissions"), permissions: true) == .permissions)
        #expect(PromptItems.ownCommand(try row("/permissions"), permissions: false) == nil)
        #expect(PromptItems.ownCommand(try row("/clear"), permissions: true) == .clear)
        #expect(PromptItems.ownCommand(PromptCommand(MainClearCommand.word, detail: ""), permissions: true) == .clear)
        #expect(PromptItems.ownCommand(try row("/compact"), permissions: true) == nil) // a quick message types its text
        let saved = PromptItems.saved([SavedPrompt(id: "p1", name: "permissions", text: "Mine."),
                                       SavedPrompt(id: "p2", name: "loop", text: "Mine."),
                                       SavedPrompt(id: "p3", name: "clear", text: "Mine.")])
        #expect(saved.map(\.name) == ["/permissions", "/loop", "/clear"])
        #expect(saved.allSatisfy { $0.insert == "Mine." })
        for command in saved {
            #expect(PromptItems.ownCommand(command, permissions: true) == nil)
            #expect(PromptItems.ownCommand(command, permissions: false) == nil)
        }
    }
}
