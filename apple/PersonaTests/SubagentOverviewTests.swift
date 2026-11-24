import Foundation
import PersonaWire
import Testing
@testable import Persona

/// Unified Session Overview Subagents aggregation, grouping, progressive origin chip, nesting cap,
/// and overview-to-detail round-trip state (#27, #14322).
@MainActor struct SubagentOverviewTests {
    private func sessionRow(_ id: String, machine: String = "box-a", agent: String = "claude",
                            state: String = "idle", extra: [String: Any] = [:]) throws -> Session {
        var fields: [String: Any] = [
            "machine_id": machine, "session_id": id, "title": id, "cwd": "/code/app",
            "status": state == "working" ? "working" : "idle", "state": state, "agent": agent,
        ]
        fields.merge(extra) { _, new in new }
        return try JSON.decoder.decode(Session.self, from: JSONSerialization.data(withJSONObject: fields))
    }

    @Test func progressiveOriginChipShowsOnlyDifferingMachineOrHarness() {
        #expect(SubagentOverview.originChip(
            childMachineID: "box-a", childMachineTitle: "MacBook", childHarness: "Claude",
            parentMachineID: "box-a", parentHarness: "Claude"
        ) == nil)

        #expect(SubagentOverview.originChip(
            childMachineID: "box-b", childMachineTitle: "87K-itx", childHarness: "Claude",
            parentMachineID: "box-a", parentHarness: "Claude"
        ) == "87K-itx")

        #expect(SubagentOverview.originChip(
            childMachineID: "box-a", childMachineTitle: "MacBook", childHarness: "Codex",
            parentMachineID: "box-a", parentHarness: "Claude"
        ) == "Codex")

        #expect(SubagentOverview.originChip(
            childMachineID: "box-b", childMachineTitle: "87K-itx", childHarness: "Codex",
            parentMachineID: "box-a", parentHarness: "Claude"
        ) == "87K-itx · Codex")
    }

    @Test func aggregatesEmbeddedSameMachineAndCrossMachineSubagentsWithGroupingAndOrder() throws {
        let parent = try sessionRow("parent", machine: "box-a", agent: "claude", state: "working")
        let t1 = Date(timeIntervalSince1970: 1000)
        let t2 = Date(timeIntervalSince1970: 2000)
        let t3 = Date(timeIntervalSince1970: 3000)

        let embeddedRunning = Subagent(
            id: "a1", sessionID: "parent@a1", type: "Explore", description: "Scan repo",
            toolUseID: "call-1", running: true, parentID: nil, fork: nil, background: false,
            startedAt: t1, updatedAt: t1, lastText: nil, steps: 3,
            lastTool: Subagent.LastTool(id: "t1", name: "Grep", summary: "Searching Swift files")
        )
        let embeddedEnded = Subagent(
            id: "a2", sessionID: "parent@a2", type: "Plan", description: "Draft plan",
            toolUseID: "call-2", running: false, parentID: nil, fork: nil, background: false,
            startedAt: t1, updatedAt: t2, lastText: "Done", steps: 4,
            lastTool: Subagent.LastTool(id: "t2", name: "Read", summary: "Should not appear on ended")
        )

        let sameMachineWorking = try sessionRow("child-local", machine: "box-a", agent: "codex", state: "working", extra: [
            "folded_under": "parent", "is_subagent": true, "parent_session_id": "parent",
            "started_at": "2026-11-01T00:00:20Z", "activity": "Running tests",
        ])
        let crossMachineNeedsYou = try sessionRow("child-a2a", machine: "box-b", agent: "codex", state: "needs_you", extra: [
            "folded_under": "parent", "folded_under_machine_id": "box-a",
            "parent_machine_id": "box-a", "parent_session_id": "parent",
            "is_subagent": true, "subagent_origin": "a2a", "a2a_task_id": "task-9",
            "started_at": "2026-11-01T00:00:05Z", "activity": "Waiting for approval",
        ])
        let failedChild = try sessionRow("child-failed", machine: "box-b", agent: "opencode", state: "idle", extra: [
            "folded_under": "parent", "folded_under_machine_id": "box-a",
            "is_subagent": true, "turn_error": "Rate limited",
            "updated_at": "2026-11-01T00:00:30Z", "activity": "Hidden on ended",
        ])
        let sideSession = try sessionRow("side-1", machine: "box-a", agent: "claude", state: "working", extra: [
            "folded_under": "parent", "folded_as": SideFold.side,
        ])

        let overview = SubagentOverview.build(
            parent: parent,
            parentKey: parent.key,
            embedded: [embeddedRunning, embeddedEnded],
            foldedChildren: [sameMachineWorking, crossMachineNeedsYou, failedChild, sideSession],
            machineName: { ref in ref.id == "box-a" ? "MacBook" : "87K-itx" }
        )

        // Side sessions are excluded from Subagents.
        #expect(!overview.running.contains { $0.target.sessionKey == sideSession.key })
        #expect(!overview.ended.contains { $0.target.sessionKey == sideSession.key })

        // Running: Needs you pinned first, then newest start descending.
        #expect(overview.running.map(\.title) == ["child-a2a", "child-local", "Scan repo"])
        #expect(overview.running[0].status == .needsYou)
        #expect(overview.running[0].originChip == "87K-itx · Codex")
        #expect(overview.running[0].toolSummary == "Waiting for approval")
        #expect(overview.running[1].originChip == "Codex")
        #expect(overview.running[1].toolSummary == "Running tests")
        #expect(overview.running[2].originChip == nil)
        #expect(overview.running[2].toolSummary == "Searching Swift files")

        // Ended: failed keeps .failed status, and ended rows carry NO second-line toolSummary.
        #expect(overview.ended.count == 2)
        #expect(overview.ended[0].title == "child-failed")
        #expect(overview.ended[0].status == .failed)
        #expect(overview.ended[0].originChip == "87K-itx · opencode")
        #expect(overview.ended[0].toolSummary == nil)
        #expect(overview.ended[1].title == "Draft plan")
        #expect(overview.ended[1].status == .ended)
        #expect(overview.ended[1].toolSummary == nil)

        // Stacked preview marks capped at 4 and reflect ended harnesses.
        #expect(overview.endedPreviewHarnesses.count == 2)
        _ = t3
    }

    @Test func nestingDepthIsCappedAtTwoLevelsAndEndedMarksCapAtFour() throws {
        let parent = try sessionRow("parent", machine: "box-a", agent: "claude", state: "working")
        let level0 = Subagent(id: "d0", sessionID: "parent@d0", type: nil, description: "L0", toolUseID: nil,
                              running: true, parentID: nil, fork: nil, background: nil, startedAt: Date(timeIntervalSince1970: 10))
        let level1 = Subagent(id: "d1", sessionID: "parent@d1", type: nil, description: "L1", toolUseID: nil,
                              running: true, parentID: "d0", fork: nil, background: nil, startedAt: Date(timeIntervalSince1970: 20))
        let level2 = Subagent(id: "d2", sessionID: "parent@d2", type: nil, description: "L2", toolUseID: nil,
                              running: true, parentID: "d1", fork: nil, background: nil, startedAt: Date(timeIntervalSince1970: 30))
        let level3 = Subagent(id: "d3", sessionID: "parent@d3", type: nil, description: "L3", toolUseID: nil,
                              running: true, parentID: "d2", fork: nil, background: nil, startedAt: Date(timeIntervalSince1970: 40))

        let endedAgents = (1...6).map { i in
            Subagent(id: "e\(i)", sessionID: "parent@e\(i)", type: nil, description: "Ended \(i)", toolUseID: nil,
                     running: false, parentID: nil, fork: nil, background: nil, startedAt: Date(timeIntervalSince1970: Double(i)))
        }

        let overview = SubagentOverview.build(
            parent: parent,
            parentKey: parent.key,
            embedded: [level0, level1, level2, level3] + endedAgents,
            foldedChildren: []
        )

        #expect(overview.running.map { "\($0.title)@\($0.depth)" } == ["L0@0", "L1@1", "L2@2", "L3@2"])
        #expect(overview.ended.count == 6)
        #expect(overview.endedPreviewHarnesses.count == 4)
    }

    @Test func overviewUIRestoresOverviewSheetWhenReturningFromSubagentDetail() {
        let ui = SessionOverviewUI()
        let key = SessionKey(group: "", machineID: "box-b", sessionID: "child-1")

        ui.openOverview(focus: .subagents)
        ui.endedExpanded = true
        #expect(ui.showOverview)
        #expect(ui.focusSection == .subagents)

        ui.openDetail(.session(key), fromOverview: true)
        #expect(!ui.showOverview)
        #expect(ui.detailTarget == .session(key))
        #expect(ui.reopenOverviewOnReturn)

        ui.detailDismissed()
        #expect(ui.detailTarget == nil)
        #expect(ui.showOverview)
        #expect(ui.focusSection == .subagents)
        #expect(ui.endedExpanded)
        #expect(!ui.reopenOverviewOnReturn)
    }

    @Test func inputStampInvalidatesWhenEmbeddedTaskStatusTransitionsToCompletedOrFailed() throws {
        let parent = try sessionRow("parent", machine: "box-a", agent: "claude", state: "working", extra: [
            "subagents": [[
                "id": "a1",
                "session_id": "parent@a1",
                "description": "Audit tests",
                "tool_use_id": "call-1",
                "running": true,
            ]],
        ])
        let runningTasks = TaskIndex()
        let stampRunning = SubagentOverview.inputStamp(session: parent, children: [], pending: [], tasks: runningTasks)

        var completedTasks = TaskIndex()
        completedTasks.agentStatuses["call-1"] = "completed"
        let stampCompleted = SubagentOverview.inputStamp(session: parent, children: [], pending: [], tasks: completedTasks)
        #expect(stampCompleted != stampRunning)

        var failedTasks = TaskIndex()
        failedTasks.agentStatuses["call-1"] = "failed"
        let stampFailed = SubagentOverview.inputStamp(session: parent, children: [], pending: [], tasks: failedTasks)
        #expect(stampFailed != stampRunning)
        #expect(stampFailed != stampCompleted)

        // Verify the rebuilt overview reflects the failed status immediately using the same tasks state.
        let embeddedShown = (parent.subagents ?? []).map { $0.shown(in: failedTasks, sessionWorks: true) }
        let overview = SubagentOverview.build(
            parent: parent,
            parentKey: parent.key,
            embedded: embeddedShown,
            foldedChildren: [],
            agentStatuses: failedTasks.agentStatuses
        )
        #expect(overview.running.isEmpty)
        #expect(overview.ended.count == 1)
        #expect(overview.ended[0].status == .failed)

        // Also verify synchronous tool_result completion (`tasks.answered`) invalidates the stamp even when
        // `session.subagents` still reports `running == true`.
        var answeredTasks = TaskIndex()
        answeredTasks.answered.insert("call-1")
        let stampAnswered = SubagentOverview.inputStamp(session: parent, children: [], pending: [], tasks: answeredTasks)
        #expect(stampAnswered != stampRunning)
    }
}
