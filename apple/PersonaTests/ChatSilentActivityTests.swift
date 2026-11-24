import Foundation
import Testing
@testable import Persona

/// Main-conversation silent rounds: default timeline and Background activity are two projections of the same full
/// transcript. Every item and feed card here is synthetic.
@MainActor struct ChatSilentActivityTests {
    private let epoch = Date(timeIntervalSince1970: 1_800_000_000)

    private func item(_ seq: Int, role: String = "assistant", kind: ItemKind = .text,
                      text: String = "Synthetic answer") -> Item {
        Item(id: "quiet-\(seq)#0", seq: seq, ts: epoch.addingTimeInterval(TimeInterval(seq * 10)),
             role: role, kind: kind, text: text)
    }

    private func input(_ seq: Int = 1) -> Item {
        var value = item(seq, role: "user", text: "Synthetic supervision input")
        value.injected = Injected(kind: "system", state: "supervise", line: "Synthetic supervision", level: "triage")
        return value
    }

    private func call(_ seq: Int, _ name: String, id: String? = nil, verdict: String? = nil) -> Item {
        var value = item(seq, kind: .toolUse)
        value.toolName = name
        value.toolUseID = id ?? "call-\(seq)"
        if let verdict { value.toolInput = .object(["verdict": .string(verdict)]) }
        return value
    }

    private func result(_ seq: Int, _ call: Item, text: String = "done", failed: Bool = false) -> Item {
        var value = item(seq, role: "user", kind: .toolResult, text: text)
        value.toolUseID = call.toolUseID
        value.isError = failed
        return value
    }

    private func silentRound() -> [Item] {
        let read = call(2, "Bash", id: "read")
        let finish = call(4, "mcp__persona__supervision_finish", id: "finish")
        return [input(), read, result(3, read, text: "Synthetic tool output"), finish,
                result(5, finish, text: #"{"visibility":"silent"}"#), item(6)]
    }

    private func projection(_ items: [Item], main: Bool = true, live: Bool = false,
                            openAll: Bool = false, openSupervision: Set<String> = []) -> ChatLines.Projection {
        let blocks = ChatStore.rebuild(items, settled: nil).blocks
        let liveID = live ? blocks.last.flatMap { if case .turn(let turn) = $0 { turn.id } else { nil } } : nil
        return ChatLines(openWork: [], openAll: openAll, foldSupervision: true, main: main,
                         openSupervision: openSupervision, active: live)
            .projection(blocks, live: liveID)
    }

    private func quiet(_ lines: [ChatLine]) -> ChatLine? {
        lines.first { if case .supervision = $0.content { true } else { false } }
    }

    private func feed(_ id: String, _ kind: FeedItem.Kind, second: Int) -> FeedItem {
        FeedItem(id: id, conversation: "main", at: epoch.addingTimeInterval(TimeInterval(second)), kind: kind,
                 ref: nil, body: nil)
    }

    @Test func completedSilentRoundLeavesMainBeforeAnyExpansion() {
        let items = silentRound()
        let initial = projection(items)
        #expect(initial.timeline.isEmpty)
        #expect(initial.activity.count == 1)
        #expect(initial.intervals.count == 1)
        guard let header = quiet(initial.activity) else { Issue.record("missing activity round"); return }

        // Opening the round and every work line expands only Background activity. None of those flags can leak a tool
        // row into the default timeline, because the split happened before `unfold`.
        let opened = projection(items, openAll: true, openSupervision: [header.id])
        #expect(opened.timeline.isEmpty)
        #expect(!opened.timeline.contains { if case .workStep = $0.content { true } else { false } })
        #expect(opened.activity.contains { if case .supervisionInput = $0.content { true } else { false } })
        #expect(opened.activity.contains { if case .workStep(_, .step(.call(let call)), _) = $0.content { call.use.toolName == "Bash" } else { false } })
        #expect(opened.activity.contains { $0.holds("quiet-6#0") })
    }

    @Test func onlyMainAndOnlyCanonicalSuccessfulSilentReceiptsMove() {
        #expect(projection(silentRound(), main: false).timeline.count == 1)
        #expect(projection(silentRound(), main: false).activity.isEmpty)
        #expect(projection(silentRound(), live: true).timeline.count == 1)
        #expect(projection(silentRound(), live: true).activity.isEmpty)

        let finish = call(4, "mcp__persona__supervision_finish", id: "finish")
        for receipt in ["not JSON", "null", #"{"visibility":"user"}"#, #"{"level":"silent"}"#] {
            let items = [input(), finish, result(3, finish, text: receipt), item(6)]
            #expect(projection(items).activity.isEmpty)
            #expect(!projection(items).timeline.isEmpty)
        }
        let failed = [input(), finish, result(3, finish, text: #"{"visibility":"silent"}"#, failed: true), item(6)]
        #expect(projection(failed).activity.isEmpty)
        #expect(!projection(failed).timeline.isEmpty)

        // A legacy quiet round without a canonical receipt keeps its existing quiet line instead of disappearing.
        var narration = item(2, text: "Legacy quiet narration")
        narration.narration = true
        let legacy = projection([input(), narration])
        #expect(quiet(legacy.timeline) != nil)
        #expect(legacy.activity.isEmpty)
    }

    @Test func everyPromotionKeepsTheWholeRoundOnMain() {
        let notify = call(2, "mcp__persona__notify_user", id: "notify")
        let ask = call(2, "mcp__persona__ask_user", id: "ask")
        let accepted = call(2, "mcp__persona__task_review", id: "review", verdict: "accepted")
        let needsUser = call(2, "mcp__persona__task_review", id: "review", verdict: "needs_user")
        let finish = call(4, "supervision_finish", id: "finish")
        for promoted in [notify, ask, accepted, needsUser] {
            let items = [input(), promoted, result(3, promoted), finish,
                         result(5, finish, text: #"{"visibility":"silent"}"#), item(6)]
            #expect(projection(items).activity.isEmpty)
            #expect(quiet(projection(items).timeline) == nil)
        }
        let userFinish = call(4, "supervision_finish", id: "finish")
        let user = [input(), userFinish, result(5, userFinish, text: #"{"visibility":"user"}"#), item(6)]
        #expect(projection(user).activity.isEmpty)
        #expect(quiet(projection(user).timeline) == nil)

        var retained = item(2)
        retained.supervisionUser = true
        let marked = [input(), retained, finish, result(5, finish, text: #"{"visibility":"silent"}"#), item(6)]
        #expect(projection(marked).activity.isEmpty)
        #expect(quiet(projection(marked).timeline) == nil)
    }

    @Test func roundFeedUsesTheSameHalfOpenIntervalOnBothSides() {
        var realInput = item(7, role: "user", text: "A real next request")
        realInput.clientMsgID = "real-next-request"
        let items = silentRound() + [realInput, item(8)]
        let split = projection(items)
        let cards = [
            feed("task-inside", .task, second: 10), feed("routed-inside", .routed, second: 30),
            feed("answered-inside", .answered, second: 65), feed("task-next-input", .task, second: 70),
            feed("plan", .plan, second: 20), feed("message", .message, second: 20),
            feed("notice", .notice, second: 20), feed("brief", .brief, second: 20),
        ]
        let mainIDs = split.feed(cards, for: .timeline).map(\.id)
        let activityIDs = split.feed(cards, for: .backgroundActivity).map(\.id)
        #expect(mainIDs == ["task-next-input", "plan", "message", "notice", "brief"])
        #expect(activityIDs == ["task-inside", "routed-inside", "answered-inside"])
        #expect(ChatLines.mergeFeed(split.timeline, split.feed(cards, for: .timeline)).contains { $0.holds("quiet-7#0") })
        let activity = ChatLines.mergeActivityFeed(split.activity, split.feed(cards, for: .backgroundActivity))
        #expect(activity.contains { if case .feed(let item) = $0.content { item.id == "task-inside" } else { false } })
        #expect(activity.contains { if case .feed(let item) = $0.content { item.id == "answered-inside" } else { false } })
    }

    @Test func anOpenEndedRoundKeepsLaterLegacyFeedInActivity() {
        let split = projection(silentRound())
        let later = feed("legacy-later", .task, second: 999)
        let earlier = feed("too-early", .task, second: -10)
        #expect(split.feed([later, earlier], for: .backgroundActivity).map(\.id) == ["legacy-later"])
        #expect(split.feed([later, earlier], for: .timeline).map(\.id) == ["too-early"])
    }
}
