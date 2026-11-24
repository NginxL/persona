package persona.core.domain.sort

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*

class SubagentOverviewTest {
    private val parent = Session(
        machineId = "mac",
        sessionId = "parent-1",
        title = "Ship feature",
        agent = "claude",
        state = "working",
        subagents = listOf(
            Subagent(
                id = "emb-run",
                sessionId = "parent-1@emb-run",
                description = "Inspect codebase",
                running = true,
                startedAt = "2026-11-24T01:00:00Z",
                lastTool = WorkflowTool(name = "Grep", summary = "Searching routes"),
            ),
            Subagent(
                id = "emb-done",
                sessionId = "parent-1@emb-done",
                description = "Audit docs",
                running = false,
                startedAt = "2026-11-24T00:30:00Z",
                updatedAt = "2026-11-24T00:40:00Z",
                lastTool = WorkflowTool(name = "Read", summary = "Should not show for ended"),
            ),
        ),
    )

    @Test fun aggregatesEmbeddedSameMachineAndCrossMachineChildrenWithPriorityAndOriginChip() {
        val sameMachineCodex = Session(
            machineId = "mac",
            sessionId = "child-codex",
            title = "Write tests",
            agent = "codex",
            state = "working",
            activity = "Running gradle test",
            isSubagent = true,
            foldedUnder = "parent-1",
            startedAt = "2026-11-24T01:05:00Z",
        )
        val crossMachineNeedsYou = Session(
            machineId = "linux-box",
            sessionId = "child-a2a",
            title = "Deploy staging",
            agent = "codex",
            state = "needs_you",
            attentionReason = "Approve deploy script",
            isSubagent = true,
            parentMachineId = "mac",
            parentSessionId = "parent-1",
            foldedUnder = "parent-1",
            foldedUnderMachineId = "mac",
            a2aTaskId = "a2a-9",
            subagentOrigin = "a2a",
            startedAt = "2026-11-24T00:50:00Z",
            capabilities = SessionCapabilities(read = true, send = true),
        )
        val crossMachineSameHarness = Session(
            machineId = "linux-box",
            sessionId = "child-claude-remote",
            title = "Failed check",
            agent = "claude",
            state = "idle",
            turnError = "Command failed",
            isSubagent = true,
            parentMachineId = "mac",
            parentSessionId = "parent-1",
            foldedUnder = "parent-1",
            foldedUnderMachineId = "mac",
            startedAt = "2026-11-24T00:10:00Z",
            updatedAt = "2026-11-24T00:45:00Z",
        )
        val side = Session(
            machineId = "mac",
            sessionId = "side-1",
            title = "Side chat",
            state = "working",
            foldedUnder = "parent-1",
            foldedAs = FoldedAs.SIDE,
        )
        val worker = Session(
            machineId = "mac",
            sessionId = "worker-1",
            title = "Tab worker",
            state = "working",
            foldedUnder = "parent-1",
            foldedAs = FoldedAs.WORKER,
        )

        val result = buildOverviewSubagents(
            parent = parent,
            childRows = listOf(sameMachineCodex, crossMachineNeedsYou, crossMachineSameHarness, side, worker),
            machineNames = mapOf("mac" to "MacBook", "linux-box" to "87K-itx"),
        )

        // Running group: NeedsYou pinned first, then newest start (child-codex 01:05 > emb-run 01:00); side/worker excluded.
        assertEquals(listOf("child-a2a", "child-codex", "parent-1@emb-run"), result.running.map { it.targetKey.sessionId })
        assertEquals(OverviewSubagentStatus.NeedsYou, result.running[0].status)
        assertEquals("87K-itx · Codex", result.running[0].originChip)
        assertEquals("Approve deploy script", result.running[0].activityLine)
        assertTrue(result.running[0].canSend)

        assertEquals("Codex", result.running[1].originChip)
        assertEquals("Running gradle test", result.running[1].activityLine)

        assertNull(result.running[2].originChip)
        assertEquals("Searching routes", result.running[2].activityLine)
        assertFalse(result.running[2].canSend)

        // Ended group: newest first, Failed preserved, no 2nd-line activity, progressive origin chip.
        assertEquals(listOf("child-claude-remote", "parent-1@emb-done"), result.ended.map { it.targetKey.sessionId })
        assertEquals(OverviewSubagentStatus.Failed, result.ended[0].status)
        assertEquals("87K-itx", result.ended[0].originChip)
        assertNull(result.ended[0].activityLine)

        assertEquals(OverviewSubagentStatus.Ended, result.ended[1].status)
        assertNull(result.ended[1].originChip)
        assertNull(result.ended[1].activityLine)
    }

    @Test fun nestingDepthIsCappedAtTwoLevels() {
        val root = Session(
            machineId = "mac",
            sessionId = "p",
            subagents = listOf(
                Subagent(id = "a", sessionId = "p@a", description = "L0", running = true, startedAt = "2026-11-24T01:00:00Z"),
                Subagent(id = "b", sessionId = "p@b", parentId = "a", description = "L1", running = true, startedAt = "2026-11-24T01:01:00Z"),
                Subagent(id = "c", sessionId = "p@c", parentId = "b", description = "L2", running = true, startedAt = "2026-11-24T01:02:00Z"),
                Subagent(id = "d", sessionId = "p@d", parentId = "c", description = "L3 flattened", running = true, startedAt = "2026-11-24T01:03:00Z"),
            ),
        )
        val result = buildOverviewSubagents(root, emptyList())
        assertEquals(listOf("L0", "L1", "L2", "L3 flattened"), result.running.map { it.title })
        assertEquals(listOf(0, 1, 2, 2), result.running.map { it.depth })
    }

    @Test fun sameSessionIdAcrossDifferentMachinesDoesNotCollideOrMisnest() {
        val lead = Session(machineId = "mac", sessionId = "lead", agent = "claude", state = "working")
        val workerA = Session(
            machineId = "box-a", sessionId = "worker-1", title = "Worker on A", agent = "codex", state = "working",
            isSubagent = true, parentMachineId = "mac", parentSessionId = "lead",
            foldedUnder = "lead", foldedUnderMachineId = "mac", startedAt = "2026-11-24T01:05:00Z",
        )
        val workerB = Session(
            machineId = "box-b", sessionId = "worker-1", title = "Worker on B", agent = "codex", state = "working",
            isSubagent = true, parentMachineId = "mac", parentSessionId = "lead",
            foldedUnder = "lead", foldedUnderMachineId = "mac", startedAt = "2026-11-24T01:04:00Z",
        )
        val grandA = Session(
            machineId = "box-a", sessionId = "grand-1", title = "Grandchild on A", agent = "codex", state = "working",
            isSubagent = true, parentMachineId = "box-a", parentSessionId = "worker-1",
            foldedUnder = "lead", foldedUnderMachineId = "mac", startedAt = "2026-11-24T01:06:00Z",
        )
        val grandB = Session(
            machineId = "box-b", sessionId = "grand-1", title = "Grandchild on B", agent = "codex", state = "working",
            isSubagent = true, parentMachineId = "box-b", parentSessionId = "worker-1",
            foldedUnder = "lead", foldedUnderMachineId = "mac", startedAt = "2026-11-24T01:07:00Z",
        )
        val result = buildOverviewSubagents(
            parent = lead,
            childRows = listOf(workerA, workerB, grandA, grandB),
            machineNames = mapOf("mac" to "Mac", "box-a" to "Alpha", "box-b" to "Beta"),
        )
        assertEquals(4, result.running.size)
        assertEquals(4, result.running.map { it.id }.toSet().size)
        assertEquals(
            listOf("Worker on A" to 0, "Grandchild on A" to 1, "Worker on B" to 0, "Grandchild on B" to 1),
            result.running.map { it.title to it.depth },
        )
        assertEquals("Alpha · Codex", result.running[0].originChip)
        assertEquals("Beta · Codex", result.running[2].originChip)
    }
}
