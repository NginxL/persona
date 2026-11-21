import Foundation
import Testing
@testable import Persona
import PersonaWire

struct DispatchedTasksTests {
    private func task(_ title: String, id: String? = nil, error: String? = nil) -> PlanTask {
        PlanTask(title: title, prompt: "Work", machine: "computer", cwd: "/home/user/project", todoID: id, error: error)
    }

    private func plan(_ tasks: [PlanTask], conversation: String = "main", state: TaskPlan.State = .started) -> TaskPlan {
        TaskPlan(id: "plan", conversation: conversation, tasks: tasks, state: state)
    }

    private func feed(_ kind: FeedItem.Kind, ref: String = "plan", body: JSONValue? = nil) -> FeedItem {
        FeedItem(id: "feed", conversation: "main", at: .now, kind: kind, ref: ref, body: body)
    }

    @Test func macDetailSelectionOpensTheRequestedSessionNotAnotherLead() {
        let first = SessionKey(group: "hub", machineID: "computer", sessionID: "first")
        let second = SessionKey(group: "hub", machineID: "computer", sessionID: "second")
        #expect(DispatchedTasks.selectedAgent(first, available: [first, second], lead: second) == first)
        #expect(DispatchedTasks.selectedAgent(second, available: [first, second], lead: first) == second)
    }

    @Test func macDetailSelectionRejectsAStaleOrOtherHubTarget() {
        let current = SessionKey(group: "hub", machineID: "computer", sessionID: "same-id")
        let otherHub = SessionKey(group: "other-hub", machineID: "computer", sessionID: "same-id")
        #expect(DispatchedTasks.selectedAgent(otherHub, available: [current], lead: current) == current)
        #expect(DispatchedTasks.selectedAgent(otherHub, available: [current], lead: otherHub) == nil)
    }

    @Test func macDetailSelectionWithoutAnAgentStaysOnTaskDetails() {
        let missing = SessionKey(machineID: "computer", sessionID: "missing")
        #expect(DispatchedTasks.selectedAgent(missing, available: [], lead: missing) == nil)
    }

    @Test func oneTaskKeepsItsExactTodoTarget() throws {
        let entries = try #require(DispatchedTasks.entries(for: feed(.task, ref: "new"), plans: [:],
                                                          titleOf: { $0 == "new" ? "Current title" : nil }, fallback: "Task"))
        #expect(entries == [.init(id: "todo:new", todoID: "new", title: "Current title", conversation: "main")])
    }

    @Test func severalTasksKeepPlanOrderAndLiveTitles() throws {
        let p = plan([task("First", id: "one"), task("Second", id: "two"), task("Failed", error: "Unavailable")])
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p],
                                                          titleOf: { $0 == "two" ? "Renamed task" : nil }, fallback: "Task"))
        #expect(entries.map(\.title) == ["First", "Renamed task", "Failed"])
        #expect(entries.map(\.todoID) == ["one", "two", nil])
        #expect(entries.map(\.id) == ["plan:plan:0", "plan:plan:1", "plan:plan:2"])
    }

    @Test func planTasksHaveNoDuplicateFeedLink() {
        let p = plan([task("Planned", id: "one")])
        #expect(DispatchedTasks.entries(for: feed(.task, ref: "one"), plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == [])
    }

    @Test func anotherConversationDoesNotHideAStandaloneLink() throws {
        let p = plan([task("Elsewhere", id: "one")], conversation: "other")
        let entries = try #require(DispatchedTasks.entries(for: feed(.task, ref: "one"), plans: [p.id: p],
                                                          titleOf: { _ in "Standalone" }, fallback: "Task"))
        #expect(entries.map(\.todoID) == ["one"])
        #expect(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == nil)
    }

    @Test func aMissingTodoRetainsItsTargetUntilItLoads() throws {
        let entries = try #require(DispatchedTasks.entries(for: feed(.task, ref: "loading"), plans: [:], titleOf: { _ in nil }, fallback: "Task"))
        #expect(entries.first?.todoID == "loading")
        #expect(entries.first?.title == "Task")
    }

    @Test func aPlanInTheFeedBodyWorksBeforeItsEventArrives() throws {
        let p = plan([task("Body title", id: "one")])
        let value = try JSON.decoder.decode(JSONValue.self, from: JSONEncoder().encode(p))
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan, body: .object(["plan": value])), plans: [:],
                                                          titleOf: { _ in nil }, fallback: "Task"))
        #expect(entries.map(\.todoID) == ["one"])
    }

    @MainActor @Test func aDispatchDoesNotAbsorbItsRoutedHandoff() {
        let task = feed(.task, ref: "one")
        var routed = feed(.routed, ref: "one", body: .object(["from": .string("persona"), "text": .string("Work instructions")]))
        routed.id = "handoff"
        routed.at = task.at.addingTimeInterval(1)
        let lines = ChatLines.mergeFeed([], [task, routed])
        #expect(lines.count == 2)
        #expect(DispatchedTasks.entries(for: routed, plans: [:], titleOf: { _ in nil }, fallback: "Task") == nil)
    }

    private func agent(_ session: String, at: Date, error: String? = nil) -> TodoItem.Agent {
        TodoItem.Agent(machineID: "computer", sessionID: session, title: "Layout \(session)", cwd: nil, agent: nil,
                       startError: error, releasedAt: nil, linkedAt: at, source: nil, via: nil)
    }

    @Test func oneAgentIsOneCardThatOpensThatAgent() {
        let at = Date(timeIntervalSince1970: 1_700_000_000)
        let todo = TodoItem(id: "t", title: "Task", agents: [agent("agent", at: at)])
        let cards = DispatchedTasks.cards(of: .init(id: "todo:t", todoID: "t", title: "Task", wake: "created", conversation: "main"), todo: todo)
        #expect(cards.count == 1)
        #expect(cards[0].id == "computer/agent")
        #expect(cards[0].machineID == "computer")
        #expect(cards[0].sessionID == "agent")
        #expect(cards[0].agentTitle == "Layout agent")
        #expect(cards[0].wake == "created")
        #expect(cards[0].todoID == "t")
    }

    @Test func agentsLinkedTogetherAreEachACardAndALaterOneIsNot() {
        let first = Date(timeIntervalSince1970: 1_700_000_000)
        let todo = TodoItem(id: "t", title: "Task", agents: [
            agent("a", at: first),
            agent("b", at: first.addingTimeInterval(60)),
            agent("later", at: first.addingTimeInterval(180)),
        ])
        let cards = DispatchedTasks.cards(of: .init(id: "todo:t", todoID: "t", title: "Task"), todo: todo)
        #expect(cards.map(\.sessionID) == ["a", "b"])
    }

    @Test func anAgentThatDidNotStartLeavesTheTaskCard() {
        let todo = TodoItem(id: "t", title: "Task", agents: [agent("a", at: .now, error: "offline")])
        let entry = DispatchedTasks.Entry(id: "todo:t", todoID: "t", title: "Task", error: "offline")
        #expect(DispatchedTasks.cards(of: entry, todo: todo) == [entry])
        #expect(DispatchedTasks.cards(of: entry, todo: nil) == [entry])
    }

    @Test func theTitleIsTheSessionsThenTheAgentsThenTheTasks() {
        #expect(DispatchedTasks.cardTitle(sessionTitle: "Claude Code", agentTitle: "Layout", taskTitle: "Task") == "Claude Code")
        #expect(DispatchedTasks.cardTitle(sessionTitle: "  ", agentTitle: " Layout ", taskTitle: "Task") == "Layout")
        #expect(DispatchedTasks.cardTitle(sessionTitle: nil, agentTitle: " ", taskTitle: "Task") == "Task")
    }

    @Test func aPlanTasksStayNewWhenTheirTaskItemSaysOtherwise() throws {
        let p = plan([task("Planned", id: "one")])
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p],
                                                          titleOf: { _ in "Planned" }, fallback: "Task"))
        #expect(entries.allSatisfy { $0.wake == nil })
        let active = feed(.task, ref: "one", body: .object(["wake": .string("active")]))
        #expect(DispatchedTasks.entries(for: active, plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == [])
        let alone = try #require(DispatchedTasks.entries(for: feed(.task, ref: "other", body: .object(["wake": .string("resumed")])),
                                                         plans: [:], titleOf: { _ in "Other" }, fallback: "Task"))
        #expect(alone.first?.wake == "resumed")
    }

    @Test func aPlanIdentifiesContinuationWithoutLinkingToThePreviousTodo() throws {
        let continued = try JSON.decoder.decode(PlanTask.self, from: Data(#"{"title":"Carry on","prompt":"Work","machine":"computer","cwd":"/project","continues":"previous-task","error":"exit status 1"}"#.utf8))
        let p = plan([continued, task("New", id: "new-task"), task("Empty", id: "")])
        let entries = try #require(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p],
                                                          titleOf: { _ in nil }, fallback: "Task"))
        #expect(entries.map(\.wake) == ["continuation", nil, nil])
        #expect(entries.map(\.detailTodoID) == [nil, "new-task", nil])
    }

    @Test func decisionsAndDismissedPlansKeepTheirNormalCards() {
        for state in [TaskPlan.State.open, .dismissed, .unknown] {
            let p = plan([task("Proposed")], state: state)
            #expect(DispatchedTasks.entries(for: feed(.plan), plans: [p.id: p], titleOf: { _ in nil }, fallback: "Task") == nil)
        }
        #expect(DispatchedTasks.entries(for: feed(.message), plans: [:], titleOf: { _ in nil }, fallback: "Task") == nil)
    }
}

@MainActor
struct DispatchHandoffActivityTimeTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)

    @Test(arguments: [5.0, 0, -10, -44.999])
    func recentActivityReadsJustNow(offset: TimeInterval) {
        #expect(DispatchHandoffCardBody.activityTime(now.addingTimeInterval(offset), now: now) == String(localized: "Just now"))
    }

    @Test(arguments: [45.0, 180, 7200])
    func olderActivityKeepsTheShortRelativeTime(age: TimeInterval) {
        let at = now.addingTimeInterval(-age)
        let relative = RelativeDateTimeFormatter()
        relative.unitsStyle = .short
        #expect(DispatchHandoffCardBody.activityTime(at, now: now) == relative.localizedString(for: at, relativeTo: now))
    }
}

/// The hand-off card is the task's only card in the chat (#8237): a start that failed or a turn the model service cut off
/// shows on it, in place, with why.
@MainActor
struct DispatchHandoffFailureTests {
    private func session(_ fields: String) throws -> Session {
        try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"mac","session_id":"s","title":"s","cwd":"/p","updated_at":"2026-11-13T09:00:00Z",\#(fields)}"#.utf8))
    }

    @Test func aTurnCutOffShowsOnTheCardWithWhy() throws {
        let cut = try session(#""state":"idle","status":"idle","turn_error":"The model service did not answer""#)
        #expect(DispatchHandoffCard.interruption(cut, closed: false) == "The model service did not answer")
        #expect(DispatchHandoffCard.interruption(cut, closed: true) == nil)
        // a usage limit has its own words on the card; an Agent at work again, or with no error, was not cut off
        #expect(DispatchHandoffCard.interruption(try session(#""state":"idle","status":"idle","turn_error":"limit","turn_error_kind":"usage_limit""#), closed: false) == nil)
        #expect(DispatchHandoffCard.interruption(try session(#""state":"working","status":"working","turn_error":"boom""#), closed: false) == nil)
        #expect(DispatchHandoffCard.interruption(try session(#""state":"idle","status":"idle""#), closed: false) == nil)
    }

    /// A failed start's reason in plain words on the card and the row; the raw error is the task's details' (#8237).
    @Test func aFailedStartSaysWhyInPlainWords() {
        typealias W = StartFailureWords
        #expect(W.cause("rpc: connection closed") == .connectionLost)
        #expect(W.cause("和「MacBook」的连接断了，等了一阵也没重连上：rpc: connection closed") == .connectionLost)
        #expect(W.cause("unexpected EOF") == .connectionLost)
        #expect(W.cause("context deadline exceeded") == .timedOut)
        #expect(W.cause("dial tcp: connect: connection refused") == .offline)
        #expect(W.cause("ssh: handshake failed: permission denied (publickey)") == .notAllowed)
        #expect(W.cause("rpc: unknown method agent.start") == .needsUpdate)
        #expect(W.cause("exit status 1") == nil)
        // a sentence the hub wrote for people stays; a raw error nothing names says to open the task
        #expect(W.plain("这台电脑上没装 Claude Code，装好后再试") == "这台电脑上没装 Claude Code，装好后再试")
        let english = "Claude Code isn't installed on this computer. Install it, then retry."
        #expect(W.plain(english) == english)
        #expect(W.plain("exit status 1") == nil)
        #expect(W.plain("  ") == nil)
        #expect(W.rowLine("exit status 1") == W.details)
        #expect(W.rowLine("rpc: connection closed") == W.words(.connectionLost))
        #expect(W.cardLine("rpc: connection closed", canOpen: false) == String(localized: "Didn't start: \(W.words(.connectionLost)) [Tasks Store 38bbb7]"))
        #expect(W.cardLine("exit status 1", canOpen: true) == W.details)
        #expect(W.cardLine("exit status 1", canOpen: false) == String(localized: "Didn't start."))
    }

    @Test func aContinuationFailureKeepsTheReasonWithoutInternalIDs() {
        typealias W = StartFailureWords
        let stop = "continues: cannot stop agent timeout-computer/session on to-do prior-task:"
        let confirm = "continues: cannot confirm agent example-computer/session stopped:"
        let reasons = [
            "Close the previous session, then try again.",
            "请先关闭之前的会话，然后重试。", // i18n-ignore: synthetic hub text
            "前の会話を閉じてからやり直してください。", // i18n-ignore: synthetic hub text
        ]
        for reason in reasons { #expect(W.plain("\(stop) \(reason)") == reason) }
        #expect(W.plain("\(confirm) rpc: connection closed") == W.words(.connectionLost))
        #expect(W.plain("\(confirm) its computer is unavailable") == W.words(.offline))
        #expect(W.plain("\(stop) exit status 1") == nil)
        #expect(W.plain("continues: unrecognized wrapper with 内部 ID") == nil) // i18n-ignore: synthetic hub text
    }

    @Test func theInterruptedLineSaysWhyWhenItCan() {
        let word = String(localized: "Interrupted [Chat Layout]")
        #expect(DispatchHandoffCardBody.interruptedLine("") == word)
        #expect(DispatchHandoffCardBody.interruptedLine("Overloaded") == word + " · Overloaded")
    }

    @Test func theUsageLimitBranchNeedsAStoppedAgentAndAnOpenTaskThatDoesNotNeedYou() throws {
        let limited = try JSON.decoder.decode(Session.self, from: Data(#"{"machine_id":"mac","session_id":"s","title":"s","cwd":"/p","state":"idle","status":"idle","updated_at":"2026-11-13T09:00:00Z","turn_error":"You've hit your usage limit.","turn_error_kind":"usage_limit"}"#.utf8))
        #expect(DispatchHandoffCard.limited(state: .working, closed: false, lead: limited))
        #expect(!DispatchHandoffCard.limited(state: .needsYou, closed: false, lead: limited))
        #expect(!DispatchHandoffCard.limited(state: .working, closed: true, lead: limited))
    }
}
