import SwiftUI
import Testing
@testable import Persona
import PersonaWire

/// The action bar above the assistant's composer (#3393): what its trays, Needs You and In Progress, list and count (each
/// thing in one of them at most, finished things in neither), what Finished lists (#10922), and its entries drawn exactly
/// as low as the dock's capsules they replace.
@MainActor
struct AssistantActionBarTests {
    private let now = Date(timeIntervalSince1970: 1_800_000_000)
    private func iso(_ minutesAgo: Double) -> String { (now - minutesAgo * 60).formatted(.iso8601) }

    private func decode<T: Decodable>(_ json: [String: Any]) -> T {
        try! JSON.decoder.decode(T.self, from: JSONSerialization.data(withJSONObject: json))
    }

    /// A task of the main conversation with one agent (`agent`), as the hub sends it.
    private func todo(_ id: String, agent: String? = nil, origin: String = "main", done: Bool = false, supervision: String? = nil,
                      ask: [String: Any]? = nil, at: Double = 30) -> TodoItem {
        var t: [String: Any] = ["id": id, "title": "task \(id)", "origin": origin, "created_at": iso(at), "updated_at": iso(at)]
        if let agent { t["agents"] = [["machine_id": "mac", "session_id": agent, "linked_at": iso(at)]] }
        if done { t["done_at"] = iso(1) }
        if let supervision { t["supervision"] = ["supervisor": "main", "state": supervision] }
        if let ask { t["ask"] = ask }
        return decode(t)
    }

    private func session(_ id: String, _ state: String, todo: String? = nil) -> Session {
        var s: [String: Any] = ["machine_id": "mac", "session_id": id, "title": id, "cwd": "/home/user/project", "state": state,
                                "status": state, "updated_at": iso(5), "state_since": iso(5)]
        if let todo { s["todo"] = ["id": todo, "title": "task \(todo)"] }
        return decode(s)
    }

    private func ask(_ id: String, session: String, at: Double = 3) -> Interaction {
        decode(["id": id, "machine_id": "mac", "session_id": session, "kind": "question", "created_at": iso(at),
                "questions": [["question": "Which one?", "options": [["label": "A"], ["label": "B"]]]]])
    }

    private func split(_ todos: [TodoItem], _ sessions: [Session] = [], asks: [Interaction] = [], plans: [TaskPlan] = [],
                       feed: [FeedItem] = [], conversation: String = "main", waits: ((FeedItem) -> Bool)? = nil) -> AssistantEntries.Result {
        AssistantEntries.split(conversation: conversation, main: "main", todos: todos, sessions: sessions, interactions: asks,
                               plans: Dictionary(uniqueKeysWithValues: plans.map { ($0.id, $0) }), feed: feed, now: now,
                               messageWaits: waits ?? AssistantEntries.messageWaits)
    }

    private func ids(_ rows: [AssistantEntries.Row]) -> [String] { rows.map(\.id) }

    /// Needs You and In Progress always; Finished only while it lists something, or while its tray is open (#10922).
    @Test func finishedIsTheThirdEntryOnlyWhileItListsSomething() {
        #expect(ActionTray.allCases == [.needsYou, .running, .finished])
        let empty = ActionTrayContent(split([]))
        #expect(empty.entries(open: nil) == [.needsYou, .running])
        #expect(empty.entries(open: .running) == [.needsYou, .running])
        #expect(empty.entries(open: .finished) == [.needsYou, .running, .finished])
        let finished = ActionTrayContent(split([todo("done", done: true)]))
        #expect(finished.entries(open: nil) == [.needsYou, .running, .finished])
    }

    /// A task waiting for a decision or review is only in Needs You; once answered it is back in In Progress.
    @Test func taskWaitingForADecisionIsOnlyInNeedsYou() {
        let waiting = split([todo("review", agent: "w", supervision: "pending_review"),
                             todo("decide", agent: "d", ask: ["verdict": "Pick a cache", "asked_at": iso(2), "outcome": "decide"])],
                            [session("w", "idle"), session("d", "idle")])
        #expect(Set(ids(waiting.needsYou)) == ["task:review", "task:decide"])
        #expect(waiting.running.isEmpty)
        let answered = split([todo("review", agent: "w", supervision: "")], [session("w", "working")])
        #expect(answered.needsYou.isEmpty)
        #expect(ids(answered.running) == ["task:review"])
    }

    /// A running task is only in In Progress, and its agent is not a row of its own.
    @Test func runningTaskWithoutItsAgent() {
        let r = split([todo("run", agent: "w")], [session("w", "working", todo: "run")])
        #expect(ids(r.running) == ["task:run"])
        #expect(r.needsYou.isEmpty)
        #expect(r.running.first == .task(id: "run", status: .working, agents: 1))
    }

    /// A task's agent asking puts the task (not the agent) in Needs You, and only there.
    @Test func taskAgentQuestionIsTheTasksRow() {
        let r = split([todo("t", agent: "w")], [session("w", "needs_you", todo: "t")], asks: [ask("ix", session: "w")])
        #expect(ids(r.needsYou) == ["task:t"])
        #expect(r.running.isEmpty)
    }

    /// One home per request (docs/design/DESIGN_SYSTEM.md 4.8, #7532, the web's #7490): an Agent several to-dos list asks
    /// under the to-do the hub ties its session to; the others are In Progress, so one approval is one row, counted once.
    @Test func agentSeveralTodosListAsksUnderItsTiedTodo() {
        let r = split([todo("cache", agent: "w"), todo("release", agent: "w"), todo("tests", agent: "w")],
                      [session("w", "needs_you", todo: "release")], asks: [ask("ix", session: "w")])
        #expect(ids(r.needsYou) == ["task:release"])
        #expect(Set(ids(r.running)) == ["task:cache", "task:tests"])
        #expect(r.running.allSatisfy { if case .task(_, let status, _) = $0 { status == .working } else { false } })
        #expect(ActionTrayContent(r).count(.needsYou) == 1)
    }

    /// With no to-do tied, the first that lists the Agent in the web's slot order (oldest card first) is its home, though
    /// the bar passes the to-dos newest first.
    @Test func agentSeveralTodosListAsksUnderTheFirstWhenNoneIsTied() {
        let r = split([todo("tests", agent: "w", at: 10), todo("cache", agent: "w", at: 40)], [session("w", "needs_you")],
                      asks: [ask("ix", session: "w")])
        #expect(ids(r.needsYou) == ["task:cache"])
        #expect(ids(r.running) == ["task:tests"])
    }

    /// Another to-do that lists the Agent stays in Needs You for a decision of its own, without the Agent's card.
    @Test func otherListerKeepsItsOwnDecision() {
        let decide = todo("tests", agent: "w", ask: ["verdict": "Which framework?", "asked_at": iso(2), "outcome": "decide"])
        let r = split([todo("release", agent: "w"), decide], [session("w", "needs_you", todo: "release")],
                      asks: [ask("ix", session: "w")])
        // the Agent asks under its home; the other row is the to-do's decision, not waiting on the Agent
        #expect(r.needsYou == [.task(id: "release", status: .needsYou, agents: 1), .task(id: "tests", status: .working, agents: 1)])
        #expect(r.running.isEmpty)
        #expect(ActionTrayContent(r).count(.needsYou) == 2)
    }

    /// A manual Agent (no task) asking is only in Needs You; one at work only in In Progress; elsewhere than the main
    /// conversation neither is a row.
    @Test func manualAgentsAreRowsOfTheMainConversation() {
        let sessions = [session("m-ask", "needs_you"), session("m-work", "working"), session("m-idle", "idle")]
        let r = split([], sessions, asks: [ask("ix", session: "m-ask")])
        #expect(ids(r.needsYou) == ["agent:mac/m-ask"])
        #expect(ids(r.running) == ["agent:mac/m-work"])
        let side = split([], sessions, asks: [ask("ix", session: "m-ask")], conversation: "side")
        #expect(side.needsYou.isEmpty && side.running.isEmpty)
    }

    /// An Agent linked to a to-do made by hand (no origin) is that to-do's row in the main conversation, never a second
    /// row of its own; once the to-do is done the Agent is a row again.
    @Test func agentOfAHandMadeTodoIsFoldedIntoItsRow() {
        let manual = todo("photos", agent: "p", origin: "")
        let r = split([manual], [session("p", "needs_you", todo: "photos")], asks: [ask("ix", session: "p")])
        #expect(ids(r.needsYou) == ["task:photos"])
        #expect(r.running.isEmpty)
        let working = split([manual], [session("p", "working", todo: "photos")])
        #expect(ids(working.running) == ["task:photos"])
        let done = split([todo("photos", agent: "p", origin: "", done: true)], [session("p", "working", todo: "photos")])
        #expect(ids(done.running) == ["agent:mac/p"])
    }

    /// Finished things are in neither entry; the ones handed out in the last day are under Finished (#10922).
    @Test func finishedThingsAreInNeither() {
        let r = split([todo("done", agent: "a", done: true), todo("accepted", agent: "b", done: true, supervision: "accepted")],
                      [session("a", "idle"), session("b", "idle")],
                      plans: [plan("started", .started, tasks: 2, at: 5), plan("dropped", .dismissed, tasks: 1, at: 4)])
        #expect(r.needsYou.isEmpty)
        #expect(r.running.isEmpty)
        #expect(r.shelved.isEmpty)
        // the same moment: by id, as on the web and Android
        #expect(r.finished.map(\.id) == ["task:accepted", "task:done"])
    }

    /// Finished (#10922) lists the handed-out tasks that finished in the last day, seen or not, newest finished first, and
    /// counts them; the ones nobody opened since they finished are new. One closed 24 hours ago or more, a to-do made by
    /// hand and another conversation's work are not in it, and none of it is in Needs You or In Progress.
    @Test func finishedListsTheLastDaySeenOrNot() {
        var older = todo("older", agent: "a", done: true)
        older.doneAt = now - 3 * 60
        let newer = todo("newer", agent: "b", done: true)
        var seen = todo("seen", agent: "c", done: true)
        seen.seenAt = now
        var stale = todo("stale", agent: "d", done: true)
        stale.doneAt = now - 48 * 3600
        var day = todo("day", agent: "f", done: true)
        day.doneAt = now - 24 * 3600
        var almost = todo("almost", agent: "g", done: true)
        almost.doneAt = now - 24 * 3600 + 1
        let byHand = todo("byHand", agent: "e", origin: "", done: true)
        let r = split([older, newer, seen, stale, day, almost, byHand], ["a", "b", "c", "d", "e", "f", "g"].map { session($0, "idle") })
        #expect(r.finished.map(\.id) == ["task:newer", "task:seen", "task:older", "task:almost"])
        #expect(r.finished.map(\.fresh) == [true, false, true, true])
        #expect(r.finished.first?.at == newer.doneAt)
        #expect(r.needsYou.isEmpty && r.running.isEmpty && r.shelved.isEmpty)
        let content = ActionTrayContent(r)
        #expect(content.finished.map(\.id) == ["task:newer", "task:seen", "task:older", "task:almost"])
        #expect(content.count(.finished) == 4)
        #expect(content.count(.running) == 0)
        // opening the new one keeps it, as seen
        var opened = newer
        opened.seenAt = now
        #expect(split([opened], [session("b", "idle")]).finished.map { [$0.id, "\($0.fresh)"] } == [["task:newer", "false"]])
        // another conversation lists only the finished work it handed out
        #expect(split([newer], [session("b", "idle")], conversation: "side").finished.isEmpty)
    }

    private func plan(_ id: String, _ state: TaskPlan.State, tasks: Int, at: Double) -> TaskPlan {
        TaskPlan(id: id, conversation: "main", tasks: (0..<tasks).map { PlanTask(title: "task \($0)", prompt: "p", machine: "mac", cwd: "/demo") },
                 state: state, createdAt: now - at * 60)
    }

    /// A plan waiting for Start is one row whatever its task count; a message card waits until sent, filed or put off.
    @Test func plansAndMessagesNeedYou() {
        let card: FeedItem = decode(["id": "f1", "conversation": "main", "at": iso(2), "kind": "message", "ref": "lark:1",
                                     "body": ["text": "Can we ship today?", "draft": "Yes, after lunch."]])
        let r = split([], plans: [plan("p", .open, tasks: 3, at: 3)], feed: [card])
        #expect(ids(r.needsYou) == ["plan:p", "message:f1"])
        #expect(r.needsYou.last == .message(feedID: "f1", itemID: "lark:1"))
        let later = split([], plans: [plan("p", .open, tasks: 3, at: 3)], feed: [card], waits: { _ in false })
        #expect(ids(later.needsYou) == ["plan:p"])
    }

    /// The conversation's own cards come first in Needs You and are counted; the trays' counts are their rows.
    @Test func ownCardsComeFirstAndCount() {
        let r = split([todo("t", agent: "w")], [session("w", "needs_you", todo: "t")], asks: [ask("ix", session: "w")])
        let content = ActionTrayContent(r, asks: [.init(id: "own", title: "Send the mail?")])
        #expect(content.needsYou.map(\.id) == ["ask:own", "task:t"])
        #expect(content.count(.needsYou) == 2)
        #expect(content.count(.running) == 0)
    }

    @Test func emptyConversationCountsNothing() {
        let content = ActionTrayContent(split([]))
        #expect(content.finished.isEmpty)
        for tray in ActionTray.allCases {
            #expect(content.rows(tray).isEmpty)
            #expect(content.count(tray) == 0)
        }
    }

    private func height(_ view: some View, _ size: DynamicTypeSize) -> CGFloat {
        let r = ImageRenderer(content: view.fixedSize().environment(\.dynamicTypeSize, size))
        r.scale = 1
        return r.cgImage.map { CGFloat($0.height) } ?? 0
    }

    /// Every look of the bar's entries is exactly as tall as the dock's All and its pills, at every text size.
    @Test(arguments: [DynamicTypeSize.large, .xxxLarge, .accessibility2])
    func entriesAreAsLowAsTheDock(size: DynamicTypeSize) {
        let all = height(Text("All 7").dockCapsule(), size)
        let pill = height(DockPillLabel(title: "Fix the widget", state: .working, waiting: false) {
            StatusDot(state: .working, size: Theme.Size.dot)
        }, size)
        #expect(all > 0)
        #expect(abs(all - pill) <= 1, "All \(all) pt, a pill \(pill) pt")
        for look in [DockCapsule.Look.plain, .selected, .waiting(.needsYou)] {
            let entry = height(HStack(spacing: Theme.Space.xs) { Text("In Progress"); Text("3").monospacedDigit() }.dockCapsule(look), size)
            #expect(abs(entry - all) <= 1, "\(look) is \(entry) pt, All \(all) pt at \(size)")
        }
    }

    /// At the default text size the capsule is the caption's line (footnote, 13 pt) plus 6 pt above and below: 28 pt
    /// for Latin text, nowhere near the 44 pt tap-target buttons the bar must not become.
    @Test func defaultCapsuleIsTheCaptionLineAndItsPadding() {
        let h = height(Text("Needs You").dockCapsule(), .large)
        let line = UIFont.preferredFont(forTextStyle: .footnote, compatibleWith: UITraitCollection(preferredContentSizeCategory: .large)).lineHeight
        #expect(abs(h - (line + 2 * Theme.Space.sm)) <= 1, "\(h) pt for a \(line) pt line")
        #expect(h < Theme.Size.tapTarget)
    }
}
