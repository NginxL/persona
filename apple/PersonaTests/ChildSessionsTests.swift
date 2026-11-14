import Foundation
import PersonaWire
import Testing
@testable import Persona

/// Child sessions folded under their parent (`ChildSessions`, docs/protocol/protocol.md "Session hierarchy").
@MainActor struct ChildSessionsTests {
    private func sessionRow(_ id: String, machine: String = "box", state: String = "idle", group: String? = nil,
                            extra: [String: Any] = [:]) throws -> Session {
        var fields: [String: Any] = ["machine_id": machine, "session_id": id, "title": id, "cwd": "/code/app",
                                     "status": "idle", "state": state]
        if let group { fields["app_hub_group"] = group }
        fields.merge(extra) { _, new in new }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: fields))
    }

    private func childRow(_ id: String, under: String, state: String = "working", machine: String = "box",
                       group: String? = nil, extra: [String: Any] = [:]) throws -> Session {
        var fields = extra
        fields["folded_under"] = under
        return try sessionRow(id, machine: machine, state: state, group: group, extra: fields)
    }

    private func interaction(_ id: String, session: String, under: String? = nil, question: String = "Which?") throws -> Interaction {
        var fields: [String: Any] = ["id": id, "machine_id": "box", "session_id": session, "kind": "question",
                                     "session_title": session, "questions": [["question": question]]]
        if let under { fields["folded_under"] = under }
        return try JSON.decoder.decode(Interaction.self, from: JSONSerialization.data(withJSONObject: fields))
    }

    private func item(_ id: String, seq: Int, role: String, kind: String, text: String? = nil, tool: String? = nil) -> Item {
        Item(id: id, seq: seq, ts: nil, role: role, kind: ItemKind(rawValue: kind) ?? .unknown, text: text,
             toolUseID: nil, toolName: tool, toolInput: nil, isError: nil, image: nil)
    }

    @Test func hierarchyFieldsDecodeAndABadRollupDoesNotDropTheSession() throws {
        let row = try sessionRow("parent", extra: [
            "asking_parent_session_id": "lead", "folded_under": "lead", "folded_as": "worker",
            "started_at": "2026-11-01T00:00:00Z", "status": "interrupted",
            "children": ["total": 2, "needs_you": 1],
        ])
        #expect(row.askingParentSessionID == "lead")
        #expect(row.foldedUnder == "lead" && row.foldedAs == "worker")
        #expect(row.wasInterrupted)
        #expect(row.children?.total == 2 && row.children?.working == 0 && row.children?.needsYou == 1)
        #expect(row.startedAt != nil)
        let odd = try sessionRow("odd", extra: ["children": "nope", "folded_under": ""])
        #expect(odd.children == nil && odd.foldedUnder == nil)
    }

    @Test func foldedStatePrefersNeedsYouThenWorkingUnlessTheReplyWaits() throws {
        let needs = try sessionRow("p", state: "idle", extra: ["children": ["needs_you": 1]])
        #expect(ChildSessions.foldedState(needs) == .needsYou)
        let working = try sessionRow("p", state: "idle", extra: ["children": ["working": 2]])
        #expect(ChildSessions.foldedState(working) == .working)
        let unread = try sessionRow("p", state: "unread", extra: ["children": ["working": 2]])
        #expect(ChildSessions.foldedState(unread) == .unread)
        let awaits = try sessionRow("p", state: "idle", extra: ["children": ["working": 2], "awaits_reply": true])
        #expect(ChildSessions.foldedState(awaits) == .idle)
        let own = try sessionRow("p", state: "needs_you", extra: ["children": ["working": 2]])
        #expect(ChildSessions.foldedState(own) == .needsYou)
    }

    @Test func aParentWaitingInTheBackgroundIsNotOneWhoseChildrenWork() throws {
        let quiet = try sessionRow("p", state: "working", extra: ["background_waiting": true])
        #expect(quiet.waitsInBackground)
        let busy = try sessionRow("p", state: "working", extra: ["background_waiting": true, "children": ["working": 1]])
        #expect(!busy.waitsInBackground)
        #expect(busy.countedState == .working)
    }

    @Test func runningChildrenAreCountedOnce() throws {
        let both = try sessionRow("p", extra: ["running_subagents": 2, "children": ["working": 2]])
        #expect(both.shownRunningChildren == 2)
        let more = try sessionRow("p", extra: ["running_subagents": 1, "children": ["working": 3]])
        #expect(more.shownRunningChildren == 3)
    }

    @Test func aFoldedChildIsNotARowAndAnOrphanCycleOrOtherMachineIs() throws {
        let parent = try sessionRow("parent", state: "idle", extra: ["children": ["total": 1, "needs_you": 1]])
        let waiting = try childRow("child", under: "parent", state: "needs_you")
        let orphan = try childRow("orphan", under: "missing")
        let cycle = try childRow("cycle", under: "cycle")
        let otherMachine = try childRow("remote", under: "parent", machine: "other")
        let otherHub = try childRow("hub", under: "parent", group: "other")
        let rows = [parent, waiting, orphan, cycle, otherMachine, otherHub]
        let folded = ChildSessions.fold(rows, asks: [parent.key: "Allow the deploy?"])
        #expect(folded.map(\.sessionID).sorted() == ["cycle", "hub", "orphan", "parent", "remote"])
        #expect(!folded.map(\.sessionID).contains("child"))
        let shown = folded.first { $0.sessionID == "parent" }
        #expect(shown?.state == .needsYou)
        #expect(shown?.attentionReason == "Allow the deploy?")
        #expect(folded.first { $0.sessionID == "orphan" } != nil)
        #expect(folded.first { $0.sessionID == "cycle" } != nil)
        #expect(folded.first { $0.sessionID == "remote" } != nil)
        #expect(folded.first { $0.sessionID == "hub" } != nil)
    }

    @Test func theListTheDockAndTheCountsLeaveOutAFoldedChild() throws {
        let parent = try sessionRow("parent", state: "idle", extra: ["children": ["total": 1, "working": 1]])
        let child = try childRow("child", under: "parent", state: "working")
        let list = AgentListProjection.build(.init(sessions: [parent, child], machines: [], settings: AgentListProjection.Settings()))
        #expect(list.base.map(\.sessionID) == ["parent"])
        #expect(list.filterCounts[.working] == 1)
        let dock = DockAgent.going([parent, child])
        #expect(dock.map(\.key.sessionID) == ["parent"])
        #expect(dock.first?.state == .working)
        let waitingParent = try sessionRow("parent", state: "idle", extra: ["children": ["needs_you": 1]])
        let waitingChild = try childRow("child", under: "parent", state: "needs_you")
        let needs = AgentsNeedsYou.waiting([waitingParent, waitingChild])
        #expect(needs.map(\.sessionID) == ["parent"])
    }

    @Test func aChildRequestIsShownOnTheParentAndAnsweredAsItself() throws {
        let childCard = try interaction("ix-child", session: "child", under: "parent", question: "Allow it?")
        let own = try interaction("ix-own", session: "parent", question: "Parent asks")
        #expect(childCard.requestHome == "parent")
        #expect(childCard.sessionID == "child")
        #expect(childCard.id == "ix-child")
        let parent = try sessionRow("parent", state: "needs_you")
        let rows = AgentsNeedsYou.rows(cards: [childCard, own], waiting: [parent])
        #expect(rows.map(\.key.sessionID) == ["parent"])
        #expect(rows.first?.cards.map(\.id).sorted() == ["ix-child", "ix-own"])
        let asks = ChildSessions.asks([childCard, own])
        #expect(asks[parent.key] == "Parent asks")
        let onlyChild = ChildSessions.asks([childCard])
        #expect(onlyChild[parent.key] == "Allow it?")
        #expect(childCard.canAnswer(session: nil))
        let plain = try interaction("ix-plain", session: "child")
        #expect(!plain.canAnswer(session: nil))
        let blocked = try sessionRow("child", extra: ["source_kind": "codex_history"])
        #expect(!childCard.canAnswer(session: blocked))
    }

    @Test func aLinkOpensTheParentOnlyWhenThatRowExists() throws {
        let child = try childRow("child", under: "parent")
        let parent = try sessionRow("parent")
        let requested = child.key
        let moved = ChildSessions.home(requested: requested, row: child, parent: parent)
        #expect(moved.key == parent.key && moved.childID == "child")
        let gone = ChildSessions.home(requested: requested, row: child, parent: nil)
        #expect(gone.key == requested && gone.childID == nil)
        let ordinary = try sessionRow("plain")
        let stayed = ChildSessions.home(requested: ordinary.key, row: ordinary, parent: nil)
        #expect(stayed.key == ordinary.key && stayed.childID == nil)
    }

    @Test func aChildWithNoStartStaysWhereItWasFirstSeen() throws {
        let first = try childRow("child", under: "parent", extra: ["updated_at": "2026-11-01T00:00:00Z"])
        var seen: [String: Date?] = [:]
        let placed = ChildSessions.places([first], seen: &seen)
        let later = try childRow("child", under: "parent", extra: ["updated_at": "2026-11-02T00:00:00Z"])
        let again = ChildSessions.places([later], seen: &seen)
        #expect(again == placed)
        let started = try childRow("child", under: "parent", extra: ["started_at": "2026-11-03T00:00:00Z",
                                                                  "updated_at": "2026-11-04T00:00:00Z"])
        #expect(ChildSessions.places([started], seen: &seen).first?.time == started.startedAt)
    }

    @Test func aChildSitsInTheExchangeThatHadStartedAndWaitsForHistory() {
        let early = Date(timeIntervalSince1970: 10)
        let late = Date(timeIntervalSince1970: 30)
        let exchanges = [ChildSessions.Exchange(id: "a", start: Date(timeIntervalSince1970: 20)),
                         ChildSessions.Exchange(id: "b", start: late)]
        let before = ChildSessions.Place(sessionID: "early", time: early)
        #expect(ChildSessions.assign(exchanges, [before], complete: false)["a"] == nil)
        #expect(ChildSessions.assign(exchanges, [before], complete: true)["a"]?.map(\.sessionID) == ["early"])
        let during = ChildSessions.Place(sessionID: "mid", time: Date(timeIntervalSince1970: 25))
        #expect(ChildSessions.assign(exchanges, [during], complete: false)["a"]?.map(\.sessionID) == ["mid"])
        let none = ChildSessions.Place(sessionID: "new", time: nil)
        #expect(ChildSessions.assign(exchanges, [none], complete: false)["b"]?.map(\.sessionID) == ["new"])
    }

    @Test func aChildPageMergesAndStopsWhenNothingIsWorking() throws {
        let kept = try childRow("kept", under: "parent", state: "idle")
        let gone = try childRow("gone", under: "parent")
        let newer = try childRow("kept", under: "parent", state: "idle", extra: ["rev": 2])
        let merged = ChildSessions.merge(held: [kept, gone], changes: [newer], removals: [gone.key], baseline: false)
        #expect(merged.map(\.sessionID) == ["kept"])
        #expect(merged.first?.rev == 2)
        let replaced = ChildSessions.merge(held: [kept], changes: [gone], removals: [], baseline: true)
        #expect(replaced.map(\.sessionID) == ["gone"])
        let parent = try sessionRow("parent", extra: ["children": ["total": 1]])
        #expect(!ChildSessions.needsRead(parent: parent, known: [kept]))
        let working = try sessionRow("parent", extra: ["children": ["total": 1, "working": 1]])
        #expect(ChildSessions.needsRead(parent: working, known: [kept]))
        let missing = try sessionRow("parent", extra: ["children": ["total": 2]])
        #expect(ChildSessions.needsRead(parent: missing, known: [kept]))
        let none = try sessionRow("parent")
        #expect(!ChildSessions.needsRead(parent: none, known: []))
        let finishing = try childRow("kept", under: "parent", state: "working")
        #expect(ChildSessions.needsRead(parent: parent, known: [finishing]))
    }

    @Test func rootsGoOnTheWindowOnly() {
        let window = SessionListQuery()
        #expect(window.wireParameters(hierarchy: true)["roots"] == "1")
        #expect(window.wireParameters(hierarchy: false)["roots"] == nil)
        var legacy = SessionListQuery()
        legacy.legacy = true
        #expect(legacy.wireParameters(hierarchy: true)["roots"] == nil)
        let parent = SessionListQuery(machine: "box", parentSessionID: "parent")
        let sent = parent.wireParameters(hierarchy: true)
        #expect(sent["roots"] == nil)
        #expect(sent["parent_session_id"] == "parent")
        #expect(sent["machine_id"] == "box")
    }

    @Test func anOpenParentsChildrenDoNotFollowEveryOtherAgent() throws {
        let model = AppModel()
        let parent = try sessionRow("parent")
        let folded = try childRow("child", under: "parent", state: "working")
        let other = try sessionRow("other", machine: "elsewhere")
        model.applySessions([parent.machineRef: [parent, folded], other.machineRef: [other]])
        let first = model.childRows(parent.key)
        #expect(first.map(\.sessionID) == ["child"])
        let moved = try sessionRow("other", machine: "elsewhere", extra: ["last_text": "moved"])
        model.applySessions([other.machineRef: [moved]])
        #expect(model.childRows(parent.key) == first)
    }

    @Test func aHintThatArrivesDuringALookupRunsOnceTheLookupEnds() {
        var gate = FetchHint()
        let first = gate.start("a")
        let replaced = gate.start("b")
        let latest = gate.start(nil)
        #expect(first)
        #expect(!replaced)
        #expect(!latest)
        let pending = gate.finish()
        guard case .hint(let next) = pending else {
            Issue.record("the hint that arrived during the lookup was dropped")
            return
        }
        #expect(next == nil)
        let again = gate.start(next)
        let done = gate.finish()
        #expect(again)
        #expect(done == .none)
    }

    @Test func aSecondLinkToTheSameChildRevealsItAgain() {
        let model = AppModel()
        let key = SessionKey(group: "g", machineID: "box", sessionID: "parent")
        model.open(key, reveal: "child")
        let first = model.revealChild
        model.open(key, reveal: "child")
        #expect(model.revealChild?.sessionID == "child")
        #expect(model.revealChild != first)
        model.open(key)
        #expect(model.revealChild == nil)
    }

    /// The Agents list's complete copy (`legacy`, no `roots`) walks every cursor page. A parent past the first 200
    /// stays, so its child still folds. The windowed list is what sends `roots=1`.
    @Test func theCompleteAgentsListWalksEveryPageSoALaterParentStillFoldsItsChild() async throws {
        let child = try childRow("child", under: "s200", state: "working")
        let parent = try sessionRow("s200")
        var fillers: [Session] = []
        for n in 1..<200 { fillers.append(try sessionRow("s\(n)")) }
        var pages = 0
        let list = SessionListModel<Session>(
            id: { $0.id }, removalID: { _ in nil },
            fetch: { query, cursor, _ in
                pages += 1
                #expect(query.legacy)
                #expect(query.wireParameters(hierarchy: true)["roots"] == nil)
                let start = Int(cursor ?? "0") ?? 0
                let end = min(start + 200, 201)
                let rows = start == 0 ? [child] + fillers : [parent]
                let more = end < 201
                return SessionListPage(sessions: rows, remove: [], hasMore: more, nextCursor: more ? "200" : nil,
                                        revision: "r", total: 201)
            },
            counts: { _ in SessionListCounts(total: 0, machine: [:], project: [:], group: [:], state: [:], revision: "") },
            waitForHints: {})
        var query = SessionListQuery()
        query.legacy = true
        await list.select(query)
        #expect(pages == 2)
        #expect(list.complete)
        #expect(!list.paged)
        #expect(list.rows.contains { $0.sessionID == "s200" })
        let folded = ChildSessions.fold(list.rows)
        #expect(folded.contains { $0.sessionID == "s200" })
        #expect(!folded.contains { $0.sessionID == "child" })
    }

    @Test func theExpandedCardReadsThePromptTheReplyAndTheFoldTail() {
        let head = [item("p", seq: 1, role: "user", kind: "text", text: "Do the thing")]
        let latest = [
            item("t", seq: 50, role: "assistant", kind: "tool_use", tool: "Read"),
            item("f", seq: 80, role: "assistant", kind: "fold"),
            item("a", seq: 90, role: "assistant", kind: "text", text: "Done"),
        ]
        let tail = [item("bash", seq: 70, role: "assistant", kind: "tool_use", tool: "Bash")]
        let detail = ChildSessions.detail(head: head, latest: latest, tail: tail)
        #expect(detail.prompt == "Do the thing")
        #expect(detail.result == "Done")
        #expect(detail.lastTool == "Bash")
    }
}
