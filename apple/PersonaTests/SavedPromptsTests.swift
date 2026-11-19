import Foundation
import Testing
@testable import Persona

/// Saved prompts on iOS (#9436): the shared store against a FakeHub (read, an old hub, a failed read keeping the list, save
/// and the hub's refusal, delete and move at once with their rollback, a list changed elsewhere, a change answering after
/// the assistant changed, a delete during a move), which assistant an event is about, the hub's rules said before asking
/// it, and the rows the composers' "/" lists get.
@MainActor
struct SavedPromptsTests {
    private static let list = #"""
        {"prompts":[{"id":"p1","name":"review","text":"Review this diff for security issues.\nList each finding."},
                    {"id":"p2","name":"commit","text":"Commit with a conventional message."},
                    {"id":"p3","name":"规范","text":"按团队规范检查。"}]}
        """#

    private final class Sink {
        var errors: [String] = []
        var toasts: [String] = []
    }

    private func make(_ hub: FakeHub, _ sink: Sink = Sink()) -> SavedPromptStore {
        SavedPromptStore(deps: .init(api: { hub.client }, report: { sink.errors.append($0.localizedDescription) },
                                     toast: { sink.toasts.append($0.message) }, open: { _ in }))
    }

    private func hub() -> FakeHub {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/saved-prompts", json: Self.list)
        return hub
    }

    private func body(_ r: LocalRequest) -> [String: Any] {
        (try? JSONSerialization.jsonObject(with: r.body ?? Data())) as? [String: Any] ?? [:]
    }

    @Test func readsTheListInItsOrder() async {
        let store = make(hub())
        #expect(!store.loaded)
        await store.load()
        #expect(store.loaded && store.supported && store.error == nil)
        #expect(store.prompts.map(\.name) == ["review", "commit", "规范"])
    }

    @Test func anOldAssistantSaysItHasNone() async {
        let hub = FakeHub()
        hub.on("GET", "/api/v1/assistant/saved-prompts") { _ in throw APIError(status: 404, message: "not found") }
        let store = make(hub)
        await store.load()
        #expect(store.loaded && !store.supported && store.error == nil && store.prompts.isEmpty)
    }

    @Test func aFailedReadKeepsTheListAlreadyRead() async {
        let hub = hub()
        let store = make(hub)
        await store.load()
        hub.on("GET", "/api/v1/assistant/saved-prompts") { _ in throw APIError(status: 500, message: "hub down") }
        await store.load()
        #expect(store.error != nil)
        #expect(store.prompts.count == 3)
    }

    @Test func savingAddsAtTheEndOrChangesInPlace() async throws {
        let hub = hub()
        hub.on("POST", "/api/v1/assistant/saved-prompts", json: #"{"prompt":{"id":"p4","name":"plan","text":"Plan first."}}"#)
        hub.on("PUT", "/api/v1/assistant/saved-prompts/p2", json: #"{"prompt":{"id":"p2","name":"ship","text":"Commit and push."}}"#)
        let store = make(hub)
        await store.load()
        try await store.save(id: nil, name: " /plan ", text: "Plan first.")
        #expect(body(hub.sent("POST", "/api/v1/assistant/saved-prompts")[0])["name"] as? String == "plan", "the / and spaces are dropped")
        try await store.save(id: "p2", name: "ship", text: "Commit and push.")
        #expect(store.prompts.map(\.name) == ["review", "ship", "规范", "plan"])
    }

    @Test func theHubsRefusalReachesTheEditorAndChangesNothing() async {
        let hub = hub()
        hub.on("POST", "/api/v1/assistant/saved-prompts") { _ in throw APIError(status: 409, message: "a prompt named review exists") }
        let store = make(hub)
        await store.load()
        await #expect(throws: APIError.self) { try await store.save(id: nil, name: "Review", text: "x") }
        #expect(store.prompts.count == 3)
    }

    @Test func deleteIsGoneAtOnceAndComesBackWhenRefused() async {
        let hub = hub()
        hub.on("DELETE", "/api/v1/assistant/saved-prompts/p2") { _ in throw APIError(status: 500, message: "hub down") }
        let sink = Sink()
        let store = make(hub, sink)
        await store.load()
        await store.delete("p2")
        #expect(store.prompts.map(\.id) == ["p1", "p2", "p3"], "back in its place")
        #expect(sink.errors.count == 1)
        hub.on("DELETE", "/api/v1/assistant/saved-prompts/p2", json: "{}")
        await store.delete("p2")
        #expect(store.prompts.map(\.id) == ["p1", "p3"])
    }

    @Test func moveSendsTheWholeOrderAndKeepsTheHubsAnswer() async {
        let hub = hub()
        hub.on("PUT", "/api/v1/assistant/saved-prompts/order") { r in
            let ids = (try? JSONSerialization.jsonObject(with: r.body ?? Data()) as? [String: [String]])?["ids"] ?? []
            #expect(ids == ["p2", "p1", "p3"])
            return Data(#"{"prompts":[{"id":"p2","name":"commit","text":"c"},{"id":"p1","name":"review","text":"r"},{"id":"p3","name":"规范","text":"g"}]}"#.utf8)
        }
        let store = make(hub)
        await store.load()
        await store.move("p2", by: -1)
        #expect(store.prompts.map(\.id) == ["p2", "p1", "p3"])
        await store.move("p2", by: -1) // already first: nothing goes to the hub
        #expect(hub.sent("PUT", "/api/v1/assistant/saved-prompts/order").count == 1)
    }

    @Test func aListChangedElsewhereIsReadAgainWithAWord() async {
        let hub = hub()
        hub.on("PUT", "/api/v1/assistant/saved-prompts/order") { _ in throw APIError(status: 409, message: "changed") }
        let sink = Sink()
        let store = make(hub, sink)
        await store.load()
        await store.move("p1", by: 1)
        #expect(store.prompts.map(\.id) == ["p1", "p2", "p3"])
        #expect(sink.toasts.count == 1 && sink.errors.isEmpty)
        #expect(hub.sent("GET", "/api/v1/assistant/saved-prompts").count == 2)
    }

    @Test func aChangeAnsweringAfterTheAssistantChangedLeavesTheNewListAlone() async throws {
        let hub = hub()
        let sink = Sink()
        let store = make(hub, sink)
        await store.load()
        hub.on("PUT", "/api/v1/assistant/saved-prompts/order") { _ in
            store.forget() // another assistant became the current one while the move was on its way
            return Data(#"{"prompts":[{"id":"p2","name":"commit","text":"c"},{"id":"p1","name":"review","text":"r"},{"id":"p3","name":"规范","text":"g"}]}"#.utf8)
        }
        await store.move("p2", by: -1)
        #expect(store.prompts.isEmpty && store.pending.isEmpty)

        await store.load()
        hub.on("DELETE", "/api/v1/assistant/saved-prompts/p2") { _ in store.forget(); throw APIError(status: 500, message: "hub down") }
        await store.delete("p2")
        #expect(store.prompts.isEmpty, "no rollback into the new assistant's list")

        await store.load()
        hub.on("POST", "/api/v1/assistant/saved-prompts") { _ in
            store.forget()
            return Data(#"{"prompt":{"id":"p4","name":"plan","text":"Plan first."}}"#.utf8)
        }
        let saved = try await store.save(id: nil, name: "plan", text: "Plan first.")
        #expect(saved.id == "p4", "the editor still gets the hub's answer")
        #expect(store.prompts.isEmpty)
        #expect(sink.errors.isEmpty && sink.toasts.isEmpty)
    }

    @Test(arguments: [false, true]) func aDeleteDuringAMoveStaysDeleted(moveFails: Bool) async {
        let hub = hub()
        hub.on("DELETE", "/api/v1/assistant/saved-prompts/p3", json: "{}")
        let store = make(hub)
        await store.load()
        hub.onAsync("PUT", "/api/v1/assistant/saved-prompts/order") { _ in
            await store.delete("p3") // the user deletes another row while the move is on its way
            if moveFails { throw APIError(status: 500, message: "hub down") }
            // the order was sent with the ids from before the delete, so the answer still has p3
            return Data(#"{"prompts":[{"id":"p2","name":"commit","text":"c"},{"id":"p1","name":"review","text":"r"},{"id":"p3","name":"规范","text":"g"}]}"#.utf8)
        }
        await store.move("p1", by: 1)
        #expect(store.prompts.map(\.id) == (moveFails ? ["p1", "p2"] : ["p2", "p1"]))
    }

    @Test func forgetLeavesNothingOfTheAssistantBefore() async {
        let store = make(hub())
        await store.load()
        store.forget()
        #expect(store.prompts.isEmpty && !store.loaded && store.supported)
    }

    @Test func anEventCountsOnlyForTheAssistantShown() {
        #expect(SavedPromptStore.concerns(eventManager: nil, shownManager: nil))
        #expect(SavedPromptStore.concerns(eventManager: "", shownManager: nil))
        #expect(SavedPromptStore.concerns(eventManager: "work", shownManager: "work"))
        #expect(!SavedPromptStore.concerns(eventManager: "work", shownManager: nil))
        #expect(!SavedPromptStore.concerns(eventManager: nil, shownManager: "work"))
    }

    @Test func theHubsRulesBeforeAskingIt() {
        let others = [SavedPrompt(id: "p1", name: "Review", text: "x")]
        #expect(SavedPromptRules.nameProblem("  ", others: []) == .nameEmpty)
        #expect(SavedPromptRules.nameProblem("/", others: []) == .nameEmpty)
        #expect(SavedPromptRules.nameProblem("review", others: others) == .nameTaken, "unique ignoring case")
        #expect(SavedPromptRules.nameProblem("two words", others: []) == .nameCharacters)
        #expect(SavedPromptRules.nameProblem(String(repeating: "a", count: 33), others: []) == .nameTooLong)
        #expect(SavedPromptRules.nameProblem("/规范-v1.2_x", others: []) == nil, "any script, - _ .")
        #expect(SavedPromptRules.textProblem(" \n ") == .textEmpty)
        #expect(SavedPromptRules.textProblem(String(repeating: "a", count: 8001)) == .textTooLong)
        #expect(SavedPromptRules.preview("a\n\nb\nc") == "a\nb")
    }

    @Test func slashRowsListSavedPromptsAfterTheChatsOwnAndInChatsWithoutCommands() {
        let saved = [SavedPrompt(id: "p1", name: "review", text: "Review this."), SavedPrompt(id: "p2", name: "commit", text: "Mine.")]
        let agent = SlashMenuRow.slashRows(draft: "/", picked: nil, showsList: true, model: false, loop: false, agentClear: false, mainClear: false, saved: saved)
        #expect(agent.suffix(2).map(\.word) == ["/review", "/commit"])
        // a saved /commit beside the chat's own: both are rows, told apart by id
        #expect(Set(agent.map(\.id)).count == agent.count)
        #expect(agent.last?.text == "Mine." && agent.last?.preview == "Mine.")
        let plain = SlashMenuRow.slashRows(draft: "/re", picked: nil, showsList: false, model: false, loop: false, agentClear: false, mainClear: false, saved: saved)
        #expect(plain.map(\.word) == ["/review"])
        #expect(SlashMenuRow.slashRows(draft: "/", picked: nil, showsList: false, model: false, loop: false, agentClear: false, mainClear: false).isEmpty)
    }
}
