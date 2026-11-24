import Foundation
@testable import Persona
import PersonaWire
import Testing

/// The message a pending question card is about (#9392, `CardSource`): View in Chat goes there.
struct CardSourceTests {
    private func item(_ seq: Int, _ role: String, _ kind: ItemKind, _ text: String? = nil, call: String? = nil,
                      tool: String? = nil, input: JSONValue? = nil) -> Item {
        Item(id: "\(seq)#0", seq: seq, ts: nil, role: role, kind: kind, text: text, toolUseID: call, toolName: tool,
             toolInput: input, isError: nil, image: nil)
    }

    private func asking(_ question: String) -> JSONValue {
        .object(["questions": .array([.object(["question": .string(question)])])])
    }

    private func card(_ question: String, call: String? = nil) -> Interaction {
        var ix = Interaction(id: "ix", machineID: "m", sessionID: "s", kind: .question)
        ix.toolUseID = call
        ix.questions = [Question(question: question, header: nil, multiSelect: false, options: [])]
        return ix
    }

    @Test func anAgentsCardGoesToTheWordsBeforeItsCall() {
        let items = [
            item(1, "user", .text, "Add tests"),
            item(2, "assistant", .text, "Which framework?"),
            item(3, "assistant", .toolUse, call: "toolu_a", tool: "Bash"),
            item(4, "user", .toolResult, call: "toolu_a"),
            item(5, "assistant", .text, "Before I start, one question."),
            item(6, "assistant", .toolUse, call: "toolu_q", tool: "AskUserQuestion", input: asking("Which framework?")),
        ]
        #expect(CardSource.item(for: card("Which framework?", call: "toolu_q"), in: items) == "5#0")
    }

    @Test func theAssistantsCardIsFoundByItsQuestion() {
        let items = [
            item(1, "user", .text, "Why is the build stuck?"),
            item(2, "assistant", .text, "It stopped at packaging."),
            item(3, "assistant", .toolUse, tool: "mcp__persona__ask_user", input: asking("Something else?")),
            item(4, "user", .text, "And now?"),
            item(5, "assistant", .text, "Two ways to check."),
            item(6, "assistant", .toolUse, tool: "mcp__persona__ask_user", input: asking("How do we go on?")),
        ]
        #expect(CardSource.item(for: card("How do we go on?"), in: items) == "5#0")
        // The older call asking another question leads to its own turn's words.
        #expect(CardSource.item(for: card("Something else?"), in: items) == "2#0")
    }

    @Test func aCallWithNoWordsBeforeItGoesToTheTurnsMessage() {
        let items = [
            item(1, "user", .text, "Pick one for me"),
            item(2, "assistant", .text, "   "),
            item(3, "assistant", .toolUse, call: "toolu_q", tool: "AskUserQuestion", input: asking("Which one?")),
        ]
        #expect(CardSource.item(for: card("Which one?", call: "toolu_q"), in: items) == "1#0")
    }

    @Test func noCallNoSource() {
        let items = [item(1, "user", .text, "Hi"), item(2, "assistant", .text, "Hello")]
        #expect(CardSource.item(for: card("Which one?", call: "toolu_missing"), in: items) == nil)
        #expect(CardSource.item(for: card("Which one?"), in: items) == nil)
        var permission = card("Which one?")
        permission.kind = .permission
        #expect(CardSource.item(for: permission, in: items) == nil)
    }

    // View in Chat from the assistant's pager into a task's Agent chat (#12625, `CardSource.source`).

    @Test func aTasksApprovalIsFoundByItsCall() {
        let items = [
            item(1, "user", .text, "Run the widget tests"),
            item(2, "assistant", .text, "Running them now."),
            item(3, "assistant", .toolUse, call: "toolu_bash", tool: "Bash"),
        ]
        var ix = Interaction(id: "ix", machineID: "m", sessionID: "s", kind: .permission)
        ix.toolUseID = "toolu_bash"
        #expect(CardSource.source(for: ix, in: items) == "2#0")
        // `item(for:)` stays for questions alone (the chat's own cards)
        #expect(CardSource.item(for: ix, in: items) == nil)
    }

    @Test func aTerminalCardGoesToTheNewestQuestion() {
        let items = [
            item(1, "assistant", .text, "First, one thing."),
            item(2, "assistant", .toolUse, call: "toolu_1", tool: "AskUserQuestion", input: asking("Old?")),
            item(3, "user", .text, "Go on"),
            item(4, "assistant", .text, "One more question."),
            item(5, "assistant", .toolUse, call: "toolu_2", tool: "AskUserQuestion", input: asking("New?")),
            item(6, "assistant", .toolUse, call: "toolu_3", tool: "Bash"),
        ]
        let screen = Interaction(id: "scr", machineID: "m", sessionID: "s", kind: .screen)
        #expect(CardSource.source(for: screen, in: items) == "4#0")
        // A chat that never asked: none, the chat opens at its newest message.
        #expect(CardSource.source(for: screen, in: [item(1, "user", .text, "hi")]) == nil)
    }

    @Test func aCardWhoseCallIsNotLoadedHasNoSourceYet() {
        let items = [item(1, "assistant", .text, "Hello")]
        #expect(CardSource.source(for: card("Which?", call: "toolu_gone"), in: items) == nil)
    }
}
