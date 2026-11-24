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
            "asking_parent_session_id": "lead", "folded_under": "lead", "folded_under_machine_id": "box-a",
            "parent_machine_id": "box-a", "a2a_task_id": "a2a-42", "subagent_origin": "a2a",
            "folded_as": "worker",
            "started_at": "2026-11-01T00:00:00Z", "status": "interrupted",
            "children": ["total": 2, "needs_you": 1],
        ])
        #expect(row.askingParentSessionID == "lead")
        #expect(row.foldedUnder == "lead" && row.foldedUnderMachineID == "box-a")
        #expect(row.parentMachineID == "box-a" && row.a2aTaskID == "a2a-42" && row.subagentOrigin == "a2a")
        #expect(row.foldedAs == "worker")
        #expect(row.wasInterrupted)
        #expect(row.children?.total == 2 && row.children?.working == 0 && row.children?.needsYou == 1)
        #expect(row.startedAt != nil)
        let odd = try sessionRow("odd", extra: [
            "children": "nope", "folded_under": "", "folded_under_machine_id": 7,
            "parent_machine_id": "", "a2a_task_id": false, "subagent_origin": "",
        ])
        #expect(odd.children == nil && odd.foldedUnder == nil && odd.foldedUnderMachineID == nil)
        #expect(odd.parentMachineID == nil && odd.a2aTaskID == nil && odd.subagentOrigin == nil)
    }

    @Test func crossMachineFoldAndOrphanSubagentFallbackFilter() throws {
        let parent = try sessionRow("parent", machine: "box-a", state: "idle",
                                    extra: ["children": ["total": 1, "needs_you": 1]])
        let remoteChild = try childRow("remote-child", under: "parent", state: "needs_you", machine: "box-b",
                                       extra: ["folded_under_machine_id": "box-a", "is_subagent": true,
                                               "parent_machine_id": "box-a", "parent_session_id": "parent",
                                               "subagent_origin": "a2a", "a2a_task_id": "task-1"])
        let orphanSubagent = try sessionRow("orphan-sub", machine: "box-b", state: "working",
                                            extra: ["is_subagent": true, "parent_session_id": "archived-parent"])
        let remoteAsk = try JSON.decoder.decode(Interaction.self, from: JSONSerialization.data(withJSONObject: [
            "id": "ix-remote", "machine_id": "box-b", "session_id": "remote-child",
            "folded_under": "parent", "folded_under_machine_id": "box-a",
            "kind": "question", "session_title": "remote-child",
            "questions": [["question": "Approve cross-machine deploy?"]],
        ]))

        #expect(ChildSessions.parentKey(remoteChild) == parent.key)
        #expect(remoteAsk.foldedUnderMachineID == "box-a")
        #expect(remoteAsk.requestHomeMachineID == "box-a")
        #expect(remoteAsk.requestHome == "parent")
        #expect(remoteAsk.requestHomeKey == parent.key)

        let asks = ChildSessions.asks([remoteAsk])
        #expect(asks[parent.key] == "Approve cross-machine deploy?")

        let folded = ChildSessions.fold([parent, remoteChild, orphanSubagent], asks: asks)
        #expect(folded.map(\.sessionID) == ["parent"])
        #expect(folded.first?.state == .needsYou)
        #expect(folded.first?.attentionReason == "Approve cross-machine deploy?")

        // Cross-machine child lookup returns remote folded children as well as archived-parent orphans.
        let kids = ChildSessions.children(of: parent.key, in: [parent, remoteChild, orphanSubagent])
        #expect(kids.map(\.key) == [remoteChild.key])
        let archivedParent = SessionKey(group: "", machineID: "box-b", sessionID: "archived-parent")
        let archivedKids = ChildSessions.children(of: archivedParent, in: [orphanSubagent])
        #expect(archivedKids.map(\.key) == [orphanSubagent.key])

        // Cross-machine interaction folds under parent in Needs You; orphan subagent interaction stays reachable.
        let orphanAsk = try JSON.decoder.decode(Interaction.self, from: JSONSerialization.data(withJSONObject: [
            "id": "ix-orphan", "machine_id": "box-b", "session_id": "orphan-sub",
            "kind": "question", "session_title": "orphan-sub",
            "questions": [["question": "Need confirmation"]],
        ]))
        let waiting = AgentsNeedsYou.waiting([parent, remoteChild, orphanSubagent])
        #expect(waiting.map(\.key) == [parent.key])
        let needsRows = AgentsNeedsYou.rows(cards: [remoteAsk, orphanAsk], waiting: waiting)
        #expect(needsRows.map(\.key) == [parent.key, orphanSubagent.key])
        #expect(needsRows.first?.cards.map(\.id) == ["ix-remote"])
        #expect(needsRows.last?.cards.map(\.id) == ["ix-orphan"])
    }

    @Test func childRollupPrefersNeedsYouThenRunningThenQuiet() throws {
        let needs = try sessionRow("p1", extra: ["running_subagents": 2, "children": ["total": 3, "working": 2, "needs_you": 1]])
        #expect(needs.childRollup.kind == .needsYou(1))
        #expect(needs.childRollup.subtitle != nil)

        let running = try sessionRow("p2", extra: ["running_subagents": 2, "children": ["total": 2, "working": 2]])
        #expect(running.childRollup.kind == .running(2))
        #expect(running.childRollup.subtitle != nil)

        let ended = try sessionRow("p3", extra: ["children": ["total": 4, "working": 0, "needs_you": 0]])
        #expect(ended.childRollup.kind == .quiet)
        #expect(ended.childRollup.isQuiet)
        #expect(ended.childRollup.subtitle == nil)

        // Side sessions are counted on their own Side Sessions chip and do not trigger subagent rollup.
        let sidesOnly = try sessionRow("p4", extra: ["children": ["total": 1, "working": 1, "needs_you": 1, "sides": 1, "sides_working": 1, "sides_needs_you": 1]])
        #expect(sidesOnly.childRollup.kind == .quiet)
    }

    @Test func crossMachineHierarchyWireParametersAndChildQueryFallback() async throws {
        let window = SessionListQuery()
        let modern = window.wireParameters(hierarchy: true, crossMachineHierarchy: true)
        #expect(modern["roots"] == "1")
        #expect(modern["cross_machine_hierarchy"] == "1")
        let legacy = window.wireParameters(hierarchy: true, crossMachineHierarchy: false)
        #expect(legacy["roots"] == "1")
        #expect(legacy["cross_machine_hierarchy"] == nil)

        let crossParent = SessionListQuery(parentMachineID: "box-a", parentSessionID: "parent")
        let crossParams = crossParent.wireParameters(hierarchy: false, crossMachineHierarchy: true)
        #expect(crossParams["parent_machine_id"] == "box-a")
        #expect(crossParams["parent_session_id"] == "parent")
        #expect(crossParams["machine_id"] == nil)
        #expect(crossParams["roots"] == nil)

        // Verify APIClient.childSessionPage sends parent_machine_id on modern hubs and falls back to machine_id on legacy hubs,
        // while preserving multi-hub group stamping.
        final class Capture: @unchecked Sendable {
            var queries: [[String: String]] = []
        }
        let capture = Capture()
        var client = APIClient(config: ServerConfig(url: URL(string: "http://127.0.0.1:18660")!, token: "t"), group: "hub-2")
        client.sessionsHierarchy = true
        client.sessionsCrossMachineHierarchy = true
        client.transport = { req in
            capture.queries.append(req.query)
            return Data(#"{"sessions":[{"machine_id":"box-b","session_id":"c1","title":"Child","cwd":"/tmp","status":"idle","state":"working","folded_under":"parent","folded_under_machine_id":"box-a"}],"remove":[],"has_more":false,"revision":"r1","total":1}"#.utf8)
        }
        let pageModern = try await client.childSessionPage(machine: "box-a", parent: "parent", cursor: nil, since: nil)
        #expect(capture.queries.last?["parent_machine_id"] == "box-a")
        #expect(capture.queries.last?["parent_session_id"] == "parent")
        #expect(capture.queries.last?["machine_id"] == nil)
        #expect(pageModern.sessions.first?.group == "hub-2")

        client.sessionsCrossMachineHierarchy = false
        _ = try await client.childSessionPage(machine: "box-a", parent: "parent", cursor: nil, since: nil)
        #expect(capture.queries.last?["machine_id"] == "box-a")
        #expect(capture.queries.last?["parent_session_id"] == "parent")
        #expect(capture.queries.last?["parent_machine_id"] == nil)
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

    @Test func crossMachineChildrenSharingSessionIDStayDistinctAcrossPlacementAndReveal() throws {
        let childA = try childRow("shared-id", under: "parent", state: "working", machine: "box-a", extra: [
            "folded_under_machine_id": "box-a",
            "updated_at": "2026-11-01T00:00:10Z",
        ])
        let childB = try childRow("shared-id", under: "parent", state: "idle", machine: "box-b", extra: [
            "folded_under_machine_id": "box-a",
            "updated_at": "2026-11-01T00:00:20Z",
        ])

        var seen: [SessionKey: Date?] = [:]
        let places = ChildSessions.places([childA, childB], seen: &seen)
        #expect(places.count == 2)
        #expect(places[0].key == childA.key && places[1].key == childB.key)
        #expect(places[0].time != places[1].time)

        let sessionsByKey = Dictionary([childA, childB].map { ($0.key, $0) }, uniquingKeysWith: { _, newer in newer })
        #expect(sessionsByKey.count == 2)

        let work = ChatLine.work(
            id: "w-1",
            items: [.step(.call(ToolCall(use: item("u1", seq: 1, role: "assistant", kind: "tool_use", tool: "Read"))))],
            live: false,
            start: Date(timeIntervalSince1970: 0),
            end: nil
        )
        let inserted = ChildSessionLines.insert([work], places: places, sessions: sessionsByKey, complete: true)
        let targetA = ChildSessionLines.target(of: childA.key, in: inserted)
        let targetB = ChildSessionLines.target(of: childB.key, in: inserted)
        #expect(targetA != nil && targetB != nil)
        #expect(targetA?.groupID == "w-1" && targetB?.groupID == "w-1")
        #expect(targetA?.stepID != targetB?.stepID)
        #expect(targetA?.lineID != targetB?.lineID)

        let revealB = ChildReveal(key: childB.key, token: 1)
        #expect(!revealB.matches(childA.key))
        #expect(revealB.matches(childB.key))
        #expect(ChildSessionLines.target(matching: revealB, in: inserted) == targetB)

        let parent = try sessionRow("parent", machine: "box-a")
        let homeB = ChildSessions.home(requested: childB.key, row: childB, parent: parent)
        #expect(homeB.key == parent.key && homeB.childKey == childB.key && homeB.childID == "shared-id")
    }

    @Test func summaryChildRefreshPreservesFullDetailOfOpenChildSession() async throws {
        let fullChild = try childRow("child-1", under: "parent", state: "working", machine: "box-b", extra: [
            "folded_under_machine_id": "box-a",
            "rev": 5,
            "permission_mode": "plan",
            "queued": ["follow up"],
            "queued_relayed": 1,
            "ports": [["port": 3000, "process": "node"]],
            "loop": ["kind": "interval", "prompt": "/loop check CI", "interval_seconds": 60],
            "background": [["id": "bg-1", "kind": "shell", "command": "swift test"]],
            "subagents": [[
                "id": "sub-1", "session_id": "child-1@sub-1", "description": "Deep audit", "running": true,
            ]],
        ])
        let summaryChild = try childRow("child-1", under: "parent", state: "idle", machine: "box-b", extra: [
            "folded_under_machine_id": "box-a",
            "rev": 8,
            "waiting": true,
            "background_count": 1,
            "running_subagents": 1,
            "activity": "Finished step",
            "loop": ["kind": "interval", "prompt": "", "interval_seconds": 60],
        ])

        // 1. Pure merge preserves full detail fields on baseline and delta summary refreshes.
        let mergedDelta = ChildSessions.merge(held: [fullChild], changes: [summaryChild], removals: [], baseline: false)
        let mergedBaseline = ChildSessions.merge(held: [fullChild], changes: [summaryChild], removals: [], baseline: true)
        for merged in [mergedDelta.first, mergedBaseline.first] {
            #expect(merged?.rev == 8)
            #expect(merged?.state == .idle)
            #expect(merged?.activity == "Finished step")
            #expect(merged?.subagents?.map(\.id) == ["sub-1"])
            #expect(merged?.background?.map(\.id) == ["bg-1"])
            #expect(merged?.queued == ["follow up"])
            #expect(merged?.queuedRelayed == 1)
            #expect(merged?.permissionMode == "plan")
            #expect(merged?.ports?.first?.port == 3000)
            #expect(merged?.loop?.prompt == "/loop check CI")
        }

        // 2. End-to-end through AppModel's child refresh path: full detail already in sessionIndex stays intact.
        let model = AppModel()
        let parentKey = SessionKey(group: "", machineID: "box-a", sessionID: "parent")
        model.childIndex.hold(fullChild)
        model.applySessions([fullChild.machineRef: [fullChild]])
        #expect(model.session(fullChild.key)?.subagents?.count == 1)

        var api = APIClient(config: ServerConfig(url: URL(string: "http://127.0.0.1:18660")!, token: "t"), group: "")
        api.sessionsHierarchy = true
        api.sessionsCrossMachineHierarchy = true
        api.transport = { _ in
            Data(#"{"sessions":[{"machine_id":"box-b","session_id":"child-1","title":"child-1","cwd":"/code/app","status":"idle","state":"idle","rev":9,"waiting":true,"background_count":1,"running_subagents":1,"folded_under":"parent","folded_under_machine_id":"box-a","loop":{"kind":"interval","prompt":"","interval_seconds":60}}],"remove":[],"has_more":false,"revision":"r2","total":1}"#.utf8)
        }
        await model.runChildRefresh(parentKey, api: api, hint: nil, force: true)
        let after = model.session(fullChild.key)
        #expect(after?.rev == 9)
        #expect(after?.state == .idle)
        #expect(after?.subagents?.map(\.id) == ["sub-1"])
        #expect(after?.background?.map(\.id) == ["bg-1"])
        #expect(after?.queued == ["follow up"])
        #expect(after?.permissionMode == "plan")
        #expect(after?.loop?.prompt == "/loop check CI")
    }

    @Test func overviewChildRefreshSerializesThroughChildHintsWithoutRollingBackCheckpoint() async throws {
        final class Tracker: @unchecked Sendable {
            var inFlight = 0
            var maxInFlight = 0
            var requests: [[String: String]] = []
            var firstRelease: CheckedContinuation<Void, Never>?
            var firstStarted: CheckedContinuation<Void, Never>?
        }
        let tracker = Tracker()
        let model = AppModel()
        let parentKey = SessionKey(group: "", machineID: "box-a", sessionID: "parent")

        var api = APIClient(config: ServerConfig(url: URL(string: "http://127.0.0.1:18660")!, token: "t"), group: "")
        api.sessionsHierarchy = true
        api.sessionsCrossMachineHierarchy = true
        api.transport = { req in
            tracker.inFlight += 1
            tracker.maxInFlight = max(tracker.maxInFlight, tracker.inFlight)
            let call = tracker.requests.count + 1
            tracker.requests.append(req.query)
            if call == 1 {
                tracker.firstStarted?.resume()
                tracker.firstStarted = nil
                await withCheckedContinuation { cont in
                    tracker.firstRelease = cont
                }
                tracker.inFlight -= 1
                return Data(#"{"sessions":[{"machine_id":"box-b","session_id":"c1","title":"C1","cwd":"/tmp","status":"idle","state":"working","rev":1,"waiting":true,"folded_under":"parent","folded_under_machine_id":"box-a"}],"remove":[],"has_more":false,"revision":"r1","total":1}"#.utf8)
            }
            tracker.inFlight -= 1
            return Data(#"{"sessions":[{"machine_id":"box-b","session_id":"c1","title":"C1","cwd":"/tmp","status":"idle","state":"idle","rev":2,"waiting":false,"folded_under":"parent","folded_under_machine_id":"box-a"}],"remove":[],"has_more":false,"revision":"r2","total":1}"#.utf8)
        }

        let started = Task { @MainActor in
            await withCheckedContinuation { cont in
                tracker.firstStarted = cont
            }
        }
        let first = Task { @MainActor in
            await model.runChildRefresh(parentKey, api: api, hint: "r1", force: false)
        }
        await started.value

        // Trigger Overview forced refresh while the first read is still in-flight.
        let overview = Task { @MainActor in
            await model.runChildRefresh(parentKey, api: api, hint: nil, force: true)
        }
        await Task.yield()
        #expect(tracker.maxInFlight == 1)

        tracker.firstRelease?.resume()
        tracker.firstRelease = nil
        await first.value
        await overview.value

        #expect(tracker.maxInFlight == 1)
        #expect(tracker.requests.count == 2)
        #expect(tracker.requests[0]["since"] == nil)
        #expect(tracker.requests[1]["since"] == "r1")
        #expect(model.childIndex.checkpoint(parentKey) == "r2")
        #expect(model.childRows(parentKey).first?.rev == 2)
    }

    @Test func cancellingOverviewWaiterDoesNotCancelInFlightOrQueuedChildRefresh() async throws {
        final class Tracker: @unchecked Sendable {
            var requests: [[String: String]] = []
            var cancelledDuringRequest: [Bool] = []
            var firstStarted: CheckedContinuation<Void, Never>?
            var firstRelease: CheckedContinuation<Void, Never>?
        }
        let tracker = Tracker()
        let model = AppModel()
        let parent = try sessionRow("parent", machine: "box-a", extra: ["children": ["total": 1, "working": 1]])
        model.applySessions([parent.machineRef: [parent]])

        var api = APIClient(config: ServerConfig(url: URL(string: "http://127.0.0.1:18660")!, token: "t"), group: "")
        api.sessionsHierarchy = true
        api.sessionsCrossMachineHierarchy = true
        api.transport = { req in
            let call = tracker.requests.count + 1
            tracker.requests.append(req.query)
            if call == 1 {
                tracker.firstStarted?.resume()
                tracker.firstStarted = nil
                await withCheckedContinuation { cont in
                    tracker.firstRelease = cont
                }
                tracker.cancelledDuringRequest.append(Task.isCancelled)
                return Data(#"{"sessions":[{"machine_id":"box-b","session_id":"c1","title":"C1","cwd":"/tmp","status":"idle","state":"working","rev":1,"waiting":true,"folded_under":"parent","folded_under_machine_id":"box-a"}],"remove":[],"has_more":false,"revision":"r1","total":1}"#.utf8)
            }
            tracker.cancelledDuringRequest.append(Task.isCancelled)
            return Data(#"{"sessions":[{"machine_id":"box-b","session_id":"c1","title":"C1","cwd":"/tmp","status":"idle","state":"idle","rev":2,"waiting":false,"folded_under":"parent","folded_under_machine_id":"box-a"}],"remove":[],"has_more":false,"revision":"r2","total":1}"#.utf8)
        }

        let started = Task { @MainActor in
            await withCheckedContinuation { cont in
                tracker.firstStarted = cont
            }
        }
        // Overview sheet opens and starts the model-owned per-parent refresh runner.
        let overviewTask = Task { @MainActor in
            await model.runChildRefresh(parent.key, api: api, hint: nil, force: true)
        }
        await started.value

        // A `sessions_list` hint queues a follow-up refresh while the first read is in flight.
        await model.runChildRefresh(parent.key, api: api, hint: "r2", force: false)

        // Closing SessionOverviewSheet cancels its `.task`: ONLY its waiter detaches; the runner stays alive.
        overviewTask.cancel()
        await overviewTask.value
        #expect(tracker.requests.count == 1)
        let activeRunner = model.childRefreshTasks[parent.key]
        #expect(activeRunner != nil)

        // Releasing request #1 completes it and drains the queued `sessions_list` follow-up ("r2") uncancelled.
        tracker.firstRelease?.resume()
        tracker.firstRelease = nil
        await activeRunner?.value

        #expect(tracker.requests.count == 2)
        #expect(tracker.requests[0]["since"] == nil)
        #expect(tracker.requests[1]["since"] == "r1")
        #expect(tracker.cancelledDuringRequest == [false, false])
        #expect(model.childIndex.checkpoint(parent.key) == "r2")
        #expect(model.childRows(parent.key).first?.rev == 2)
        #expect(model.childRefreshTasks[parent.key] == nil)
    }

    @Test func ordinaryToolCallWithMachineAndSessionIDIsNotMatchedAsChildCardTarget() throws {
        let child = try childRow("child-1", under: "parent", state: "working", machine: "box-b", extra: [
            "folded_under_machine_id": "box-a",
            "started_at": "2026-11-01T00:00:10Z",
        ])
        let ordinaryItem = Item(
            id: "toolu_01ordinary",
            seq: 1,
            ts: Date(timeIntervalSince1970: 5),
            role: "assistant",
            kind: .toolUse,
            text: nil,
            toolUseID: nil,
            toolName: "mcp__persona__inspect_session",
            toolInput: .object([
                "machine_id": .string("box-b"),
                "session_id": .string("child-1"),
            ]),
            isError: nil,
            image: nil
        )
        let ordinaryCall = ToolCall(use: ordinaryItem)
        #expect(ChildSessionLines.childKey(of: ordinaryCall) == nil)

        let barePrefixCall = ToolCall(use: Item(
            id: ChildSessions.callPrefix,
            seq: 2,
            ts: Date(timeIntervalSince1970: 6),
            role: "assistant",
            kind: .toolUse,
            text: nil,
            toolName: "Child",
            toolInput: .object(["machine_id": .string("box-b"), "session_id": .string("child-1")]),
            isError: nil,
            image: nil
        ))
        #expect(ChildSessionLines.childKey(of: barePrefixCall) == nil)

        let workWithoutChildCard = ChatLine.work(
            id: "w-1",
            items: [.step(.call(ordinaryCall))],
            live: false,
            start: Date(timeIntervalSince1970: 0),
            end: nil
        )
        #expect(ChildSessionLines.target(of: child.key, in: [workWithoutChildCard]) == nil)
        #expect(ChildSessionLines.target(matching: ChildReveal(key: child.key, token: 1), in: [workWithoutChildCard]) == nil)
        #expect(ChildSessionLines.target(matching: ChildReveal(sessionID: child.sessionID, token: 2), in: [workWithoutChildCard]) == nil)

        // Once the real synthetic `child:...` card is inserted beside the ordinary tool call, targeting resolves
        // strictly to the synthetic child card and never the ordinary tool call.
        var seen: [SessionKey: Date?] = [:]
        let places = ChildSessions.places([child], seen: &seen)
        let inserted = ChildSessionLines.insert([workWithoutChildCard], places: places, sessions: [child.key: child], complete: true)
        let target = ChildSessionLines.target(of: child.key, in: inserted)
        #expect(target?.groupID == "w-1")
        #expect(target?.stepID == ChildSessions.callID(for: child.key))
        #expect(target?.stepID != ordinaryItem.id)
        #expect(ChildSessionLines.target(matching: ChildReveal(key: child.key, token: 3), in: inserted) == target)
        #expect(ChildSessionLines.target(matching: ChildReveal(sessionID: child.sessionID, token: 4), in: inserted) == target)
    }

    @Test func childRollupDeduplicatesEmbeddedAndIndependentSubagentsAndFallsBackNonAdditivelyWhenUnloaded() throws {
        // 1. Loaded children: 1 embedded running + 1 distinct cross-machine A2A child running => 2 running & 2 total.
        let parent = try sessionRow("parent", machine: "box-a", extra: [
            "subagents": [[
                "id": "a1", "session_id": "parent@a1", "description": "Explore", "running": true,
            ]],
            "children": ["total": 1, "working": 1],
        ])
        let remoteA2A = try childRow("remote-1", under: "parent", state: "working", machine: "box-b", extra: [
            "folded_under_machine_id": "box-a", "is_subagent": true, "subagent_origin": "a2a",
        ])
        #expect(ChildSessions.shownRunning(parent, heldChildren: [remoteA2A]) == 2)
        #expect(ChildSessions.childRollup(parent, heldChildren: [remoteA2A]).kind == .running(2))
        #expect(ChildSessions.totalSubagentCount(parent, heldChildren: [remoteA2A]) == 2)

        // 2. Loaded overlapping child: present in both `subagents` and `heldChildren` => counted ONCE.
        let overlappingParent = try sessionRow("parent", machine: "box-a", extra: [
            "subagents": [[
                "id": "local-1", "session_id": "local-1", "description": "Local child", "running": true,
            ]],
            "children": ["total": 1, "working": 1],
        ])
        let localChild = try childRow("local-1", under: "parent", state: "working", machine: "box-a")
        #expect(ChildSessions.shownRunning(overlappingParent, heldChildren: [localChild]) == 1)
        #expect(ChildSessions.childRollup(overlappingParent, heldChildren: [localChild]).kind == .running(1))
        #expect(ChildSessions.totalSubagentCount(overlappingParent, heldChildren: [localChild]) == 1)

        // 3. Unloaded children (`heldChildren` omitted, e.g. menu or Agents list row): non-additive `max` fallback so
        //    an overlapping subagent reported in both `subagents` and `children` is NEVER double-counted as 2.
        #expect(ChildSessions.shownRunning(overlappingParent) == 1)
        #expect(overlappingParent.childRollup.kind == .running(1))
        #expect(ChildSessions.totalSubagentCount(overlappingParent) == 1)
        #expect(ChildSessions.shownRunning(parent) == 1)
        #expect(parent.childRollup.kind == .running(1))
        #expect(ChildSessions.totalSubagentCount(parent) == 1)

        // Unloaded asymmetric counts pick the larger side without adding.
        let moreChildren = try sessionRow("parent", machine: "box-a", extra: [
            "subagents": [[
                "id": "a1", "session_id": "parent@a1", "description": "Explore", "running": true,
            ]],
            "children": ["total": 3, "working": 3],
        ])
        #expect(ChildSessions.shownRunning(moreChildren) == 3)
        #expect(ChildSessions.totalSubagentCount(moreChildren) == 3)

        // Partial load (1 distinct held child + 1 embedded + 1 more unloaded child): `max(1+1, 2) == 2`, never 3.
        let partialParent = try sessionRow("parent", machine: "box-a", extra: [
            "subagents": [[
                "id": "a1", "session_id": "parent@a1", "description": "Explore", "running": true,
            ]],
            "children": ["total": 2, "working": 2],
        ])
        #expect(ChildSessions.shownRunning(partialParent, heldChildren: [remoteA2A]) == 2)
        #expect(ChildSessions.totalSubagentCount(partialParent, heldChildren: [remoteA2A]) == 2)
    }

    @Test func summaryRowPrefersAuthoritativeRunningSubagentsScalarOverStaleCachedSubagents() throws {
        let full = try sessionRow("parent", machine: "box-a", state: "working", extra: [
            "subagents": [[
                "id": "a1", "session_id": "parent@a1", "description": "Deep audit", "running": true,
            ]],
        ])
        #expect(ChildSessions.shownRunning(full) == 1)
        #expect(full.childRollup.kind == .running(1))

        // Fresh `summary=1` row with `waiting: false` and omitted `running_subagents` (omitted => 0 per protocol.md):
        // `preservingDetails` keeps cached `subagents` for inspection, while `shownRunning` trusts the summary scalar (0).
        let summaryEnded = try sessionRow("parent", machine: "box-a", state: "idle", extra: [
            "waiting": false,
            "background_tasks_count": 0,
        ]).preservingDetails(from: full)
        #expect(summaryEnded.subagents?.first?.running == true)
        #expect(ChildSessions.shownRunning(summaryEnded) == 0)
        #expect(summaryEnded.shownRunningChildren == 0)
        #expect(summaryEnded.childRollup.kind == .quiet)
        // Cached ended subagent remains discoverable in totalSubagentCount (e.g. for Session Overview menu entry).
        #expect(ChildSessions.totalSubagentCount(summaryEnded) == 1)

        // Explicit `running_subagents: 0` on a waiting summary row (e.g. waiting on a loop/shell) also reports 0 running subagents.
        let summaryExplicitZero = try sessionRow("parent", machine: "box-a", state: "working", extra: [
            "waiting": true,
            "running_subagents": 0,
            "background_tasks_count": 1,
        ]).preservingDetails(from: full)
        #expect(summaryExplicitZero.subagents?.first?.running == true)
        #expect(ChildSessions.shownRunning(summaryExplicitZero) == 0)
        #expect(summaryExplicitZero.childRollup.kind == .quiet)

        // Summary row with higher authoritative `running_subagents: 2` overrides stale cached 1-entry array.
        let summaryTwoRunning = try sessionRow("parent", machine: "box-a", state: "working", extra: [
            "waiting": true,
            "running_subagents": 2,
            "background_tasks_count": 0,
        ]).preservingDetails(from: full)
        #expect(ChildSessions.shownRunning(summaryTwoRunning) == 2)
        #expect(summaryTwoRunning.childRollup.kind == .running(2))
        #expect(ChildSessions.totalSubagentCount(summaryTwoRunning) == 2)
    }
}
