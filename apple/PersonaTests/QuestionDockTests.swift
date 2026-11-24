import Foundation
import Testing
@testable import Persona

/// The assistant's pager over the composer (#12831): the order of its cards, which one shows after an answer, when it
/// stays collapsed, the entry's "+N", the dots' window and which cards grey their Skip.
struct QuestionDockTests {
    private let t0 = Date(timeIntervalSince1970: 1_800_000_000)
    private func item(_ id: String, _ minutes: Double?) -> QuestionDockLogic.Item {
        .init(id: id, createdAt: minutes.map { t0 + $0 * 60 })
    }

    @Test func theConversationsOwnCardsComeFirstThenTheTasksOldestFirst() {
        let order = QuestionDockLogic.order(own: [item("own2", 9), item("own1", 1)],
                                            tasks: [item("t3", 5), item("t1", 2), item("t2", 2), item("old", nil)])
        // own as the chat lists them; tasks by age, a tie keeping their order, one without a time first
        #expect(order == ["own2", "own1", "old", "t1", "t2", "t3"])
    }

    @Test func aCardInBothPlacesShowsOnce() {
        #expect(QuestionDockLogic.order(own: [item("a", 1)], tasks: [item("a", 1), item("b", 2)]) == ["a", "b"])
    }

    @Test func answeringACardMovesOnToTheNext() {
        // on the second of three; it is answered: the one after it takes its place
        #expect(QuestionDockLogic.shown(["a", "c"], current: "b", index: 1) == 1)
        // the last answered: the one before
        #expect(QuestionDockLogic.shown(["a", "b"], current: "c", index: 2) == 1)
        // the one shown stays when another goes or comes
        #expect(QuestionDockLogic.shown(["new", "a", "b"], current: "b", index: 1) == 2)
        #expect(QuestionDockLogic.shown([], current: "a", index: 0) == nil)
        #expect(QuestionDockLogic.shown(["a"], current: nil, index: 0) == 0)
    }

    @Test func collapsedStaysCollapsedUntilAFreshRound() {
        let seen: Set<String> = ["a", "b"]
        #expect(QuestionDockLogic.collapsed(["a", "b"], folded: true, seen: seen))
        // a new card while one the owner put away still waits: still collapsed, the entry says +1
        #expect(QuestionDockLogic.collapsed(["b", "c"], folded: true, seen: seen))
        #expect(QuestionDockLogic.arrived(["b", "c"], seen: seen) == 1)
        // all of those gone, a fresh one: open again
        #expect(!QuestionDockLogic.collapsed(["c"], folded: true, seen: seen))
        #expect(!QuestionDockLogic.collapsed([], folded: true, seen: seen))
        #expect(!QuestionDockLogic.collapsed(["a"], folded: false, seen: seen))
    }

    @MainActor @Test func theStateOpensFoldsAndSteps() {
        let state = QuestionDockState()
        let ids = ["a", "b", "c"]
        #expect(state.showing(ids))
        state.fold(ids)
        #expect(!state.showing(ids))
        state.show("c")
        #expect(state.showing(ids) && state.current == "c")
        state.go(to: 1, in: ids)
        #expect(state.current == "b" && state.index == 1)
        state.go(to: 5, in: ids)
        #expect(state.current == "b")
        state.trayOpen = true
        #expect(!state.showing(ids))
        #expect(!QuestionDockState().showing([]))
    }

    @Test func theDotsShowAWindowAroundTheShownCard() {
        #expect(QuestionDockLogic.dots(count: 5, shown: 4) == 0..<5)
        #expect(QuestionDockLogic.dots(count: 12, shown: 0) == 0..<7)
        #expect(QuestionDockLogic.dots(count: 12, shown: 6) == 3..<10)
        #expect(QuestionDockLogic.dots(count: 12, shown: 11) == 5..<12)
    }

    @Test func skipIsGreyedForTerminalQuestionsPermissionsAndPlans() {
        #expect(QuestionDockLogic.noSkip(kind: .screen, command: false, canSkip: false) == .terminal)
        #expect(QuestionDockLogic.noSkip(kind: .screen, command: true, canSkip: false) == .permission)
        #expect(QuestionDockLogic.noSkip(kind: .permission, command: false, canSkip: false) == .permission)
        #expect(QuestionDockLogic.noSkip(kind: .plan, command: false, canSkip: false) == .plan)
        // the rest as today: Skip where the request can be cancelled, nothing otherwise
        #expect(QuestionDockLogic.noSkip(kind: .question, command: false, canSkip: false) == nil)
        #expect(QuestionDockLogic.noSkip(kind: .question, command: false, canSkip: true) == nil)
        #expect(QuestionDockLogic.noSkip(kind: .elicitation, command: false, canSkip: false) == nil)
        #expect(QuestionDockLogic.noSkip(kind: .screen, command: false, canSkip: true) == nil)
    }
}
