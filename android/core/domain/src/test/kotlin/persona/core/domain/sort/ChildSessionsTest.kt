package persona.core.domain.sort

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.domain.answer.firstBySession
import persona.core.domain.answer.askingLine
import persona.core.domain.tasks.*

/** Mirrors web child-sessions/folded-rows/child-counts cases, including a waiting runtime child. */
class ChildSessionsTest {
    private val parent = Session("m", "p", title = "Lead", state = "idle", children = SessionChildren(3, working = 1, needsYou = 1))
    private val working = Session("m", "work", title = "Review", state = "working", foldedUnder = "p")
    private val asking = Session("m", "ask", state = "needs_you", askingParentSessionId = "p", foldedUnder = "p")
    private val grandchild = Session("m", "leaf", state = "idle", parentSessionId = "work", foldedUnder = "p")
    private val rows = listOf(parent, working, asking, grandchild)
    private val card = Interaction("q", machineId = "m", sessionId = "ask", kind = "question", foldedUnder = "p",
        sessionTitle = "Choose a target", questions = listOf(Question("Which target?")))

    @Test fun foldedStateKeepsOwnUnreadAndUnansweredReplyButNeedsYouAlwaysWins() {
        val childWorks = SessionChildren(1, working = 1)
        for (own in listOf("idle", "offline", "unknown", "working"))
            assertEquals(own, AgentState.Working, parent.copy(state = own, children = childWorks).foldedState)
        assertEquals(AgentState.Unread, parent.copy(state = "unread", children = childWorks).foldedState)
        assertEquals(AgentState.Idle, parent.copy(awaitsReply = true, children = childWorks).foldedState)
        assertEquals(AgentState.NeedsYou, parent.copy(state = "unread").foldedState)
        assertEquals(AgentState.NeedsYou, parent.copy(state = "needs_you", children = null).foldedState)
        assertEquals("idle", parent.state)
    }

    @Test fun everyDepthAndWaitingChildDisappearOnlyUnderAListedParentOnTheSameMachine() {
        assertEquals(listOf("p"), foldedSessions(rows).map { it.sessionId })
        assertEquals(AgentState.NeedsYou, foldedSessions(rows).single().agentState)
        assertEquals(listOf("work", "ask", "leaf"), foldedSessions(rows.drop(1)).map { it.sessionId })
        assertEquals(2, foldedSessions(listOf(parent.copy(machineId = "other"), working)).size)
        assertEquals(2, foldedSessions(listOf(parent, working.copy(foldedUnder = null, parentSessionId = "p"))).size)
        assertEquals(3, childSessions(rows, parent.key).size)
        val orphan = Session("m", "orphan", agent = "codex", sourceKind = "codex_app_server", isSubagent = true,
            parentSessionId = "gone", state = "working")
        val listed = buildAgentList(listOf(AgentSession(orphan)), settings = AgentListSettings(hierarchy = true))
        assertEquals(0, listed.counts[AgentQuickFilter.Working])
        assertTrue(listed.groups.flatMap { it.rows }.isEmpty())
    }

    @Test fun listsDockCountsProjectsAndRequestsHaveOneParentHome() {
        // By Machine only sections computers from the hub's machine list (iOS machineOrder); pass the parent's host.
        val hosts = listOf(Machine("m", "Example"))
        for (grouping in AgentGrouping.selectable) {
            val list = buildAgentList(rows.map(::AgentSession), hosts, settings = AgentListSettings(grouping = grouping))
            assertEquals(grouping.name, 1, list.counts[AgentQuickFilter.NeedsYou])
            assertEquals(grouping.name, 0, list.counts[AgentQuickFilter.Working])
            assertTrue(list.children.isEmpty())
            assertEquals(setOf("p"), list.groups.flatMap { it.rows }.map { it.session.sessionId }.toSet())
        }
        assertEquals(listOf("p"), dockAgents("main", "main", rows, emptyList()).map { it.sessionId })
        assertEquals(StateTally(needsYou = 1), rows.map(::AgentSession).stateTally())
        assertEquals(1, machineProjects(rows.map(::AgentSession), "m", AgentPreferences(), AgentPreferences()).agents)
        val own = card.copy(id = "own", sessionId = "p", foldedUnder = null)
        val waiting = needsYouRows(listOf(card, own, card), needsYouWaiting(rows))
        assertEquals(1, waiting.size)
        assertEquals(parent.key, waiting.single().key)
        assertEquals(2, waiting.single().cards.size)
        assertEquals(listOf(parent.key), needsYouRows(listOf(card), needsYouWaiting(listOf(asking))).map { it.key })
        assertEquals("ask", card.sessionKey.sessionId)
        assertEquals("own", listOf(card, own).firstBySession()[parent.key]?.id)
        assertNotNull(parent.askingLine(listOf(card).firstBySession()[parent.key]))
        val entries = assistantEntries("main", "main", emptyList(), rows, listOf(card), emptyList(), emptyList(), 0L)
        assertTrue(entries.running.isEmpty())
        assertEquals(listOf(card), (entries.needsYou.single() as NeedsYouEntry.Agent).asks)
    }

    @Test fun backgroundWaitAndSubagentCountsDoNotHideOrDoubleCountChildren() {
        val s = parent.copy(state = "working", backgroundWaiting = true, runningSubagents = 3, waiting = false)
        assertFalse(AgentSession(s).waitsInBackground)
        assertEquals(3, s.backgroundWork()?.subagents)
        assertEquals(4, s.copy(children = SessionChildren(4, working = 4)).backgroundWork()?.subagents)
        assertEquals(1, parent.copy(children = SessionChildren(1, working = 1)).backgroundWork()?.subagents)
    }

    @Test fun anAuthoritativeGroupDoesNotDisableAnotherHubsLegacyHierarchy() {
        val legacyParent = Session("old", "lead", state = "working")
        val legacyChild = Session("old", "child", sourceKind = "runtime", agent = "claude", isSubagent = true,
            parentSessionId = "lead", state = "working")
        val modern = parent.copy(machineId = GroupKeys.machine("m", "g12345678"))
        val list = buildAgentList(listOf(modern, legacyParent, legacyChild).map(::AgentSession))
        assertEquals(setOf(modern.key, legacyParent.key), list.groups.flatMap { it.rows }.map { it.session.key }.toSet())
        assertEquals(listOf(legacyChild.key), list.children[legacyParent.key.path].orEmpty().map { it.session.key })
        val cycle = listOf(legacyParent.copy(parentSessionId = "child", sourceKind = "runtime", agent = "claude", isSubagent = true), legacyChild)
        assertEquals(0, dockAgents("main", "main", cycle, emptyList(), hierarchy = true).size)
        assertEquals(1, dockAgents("main", "main", cycle, emptyList(), hierarchy = false).size)
    }

    @Test fun crossMachineFoldingHidesRemoteChildrenAndClientFallbackBlocksSubagentLeaks() {
        val parentA = Session("box-a", "p", title = "Lead", state = "idle", children = SessionChildren(2, working = 1, needsYou = 1))
        val remoteWorking = Session(
            "box-b", "sub-work", title = "Remote Codex", state = "working", agent = "codex",
            isSubagent = true, parentMachineId = "box-a", parentSessionId = "p",
            foldedUnder = "p", foldedUnderMachineId = "box-a", a2aTaskId = "a2a-1", subagentOrigin = "a2a",
        )
        val remoteAsking = Session(
            "box-c", "sub-ask", title = "Remote Review", state = "needs_you",
            isSubagent = true, parentMachineId = "box-a", parentSessionId = "p",
            foldedUnder = "p", foldedUnderMachineId = "box-a", subagentOrigin = "a2a",
        )
        val orphanA2a = Session(
            "box-d", "orphan-a2a", title = "Orphan A2A", state = "working",
            isSubagent = true, parentMachineId = "box-a", parentSessionId = "archived-parent", subagentOrigin = "a2a",
        )
        val unrelatedSameId = Session("box-b", "other-child", state = "working", foldedUnder = "p")
        val all = listOf(parentA, remoteWorking, remoteAsking, orphanA2a, unrelatedSameId)

        val folded = foldedSessions(all, hierarchy = true)
        assertEquals(setOf(parentA.key, unrelatedSameId.key), folded.map { it.key }.toSet())

        val kids = childSessions(all, parentA.key)
        assertEquals(setOf(remoteWorking.key, remoteAsking.key), kids.map { it.key }.toSet())
        assertTrue(childSessions(listOf(orphanA2a), SessionKey("box-a", "archived-parent")).single().key == orphanA2a.key)

        // Cross-machine NeedsYou interaction resolves to parent on box-a.
        val remoteCard = Interaction(
            "q-remote", machineId = "box-c", sessionId = "sub-ask", kind = "permission",
            foldedUnder = "p", foldedUnderMachineId = "box-a",
        )
        assertEquals(listOf(parentA.key), needsYouRows(listOf(remoteCard), needsYouWaiting(listOf(remoteAsking))).map { it.key })
    }

    @Test fun childRollupPrioritizesNeedsYouOverRunningAndStaysQuietWhenAllEnded() {
        val base = Session("box-a", "p", state = "idle")
        // All ended -> quiet (null)
        assertNull(childRollup(base.copy(subagents = listOf(Subagent("s1", running = false)), children = SessionChildren(total = 2))))

        // NeedsYou wins over Running (embedded + folded)
        val needs = base.copy(
            runningSubagents = 3,
            children = SessionChildren(total = 4, working = 2, needsYou = 1),
        )
        assertEquals(ChildRollup(ChildRollupKind.NeedsYou, 1), childRollup(needs))

        // Running combines embedded + folded non-side working without counting side sessions
        val runningOnly = base.copy(
            runningSubagents = 2,
            children = SessionChildren(total = 4, working = 3, sides = 1, sidesWorking = 1),
        )
        assertEquals(ChildRollup(ChildRollupKind.Running, 4), childRollup(runningOnly))

        // Sides needing you do not trigger subagent rollup
        val sideNeeds = base.copy(
            children = SessionChildren(total = 1, needsYou = 1, sides = 1, sidesNeedsYou = 1),
        )
        assertNull(childRollup(sideNeeds))

        // Held cross-machine children also drive NeedsYou rollup
        val remoteAsk = Session("box-b", "sub-1", state = "needs_you", isSubagent = true, foldedUnder = "p", foldedUnderMachineId = "box-a")
        assertEquals(ChildRollup(ChildRollupKind.NeedsYou, 1), childRollup(base, listOf(remoteAsk)))

        // Deduplicates a subagent present in both parent.subagents and heldChildren by SessionKey, while summing distinct ones.
        val sameKeyChild = Session("box-a", "p@emb1", state = "working", isSubagent = true, foldedUnder = "p")
        val remoteWorkingChild = Session(
            "box-b", "a2a-1", state = "working", isSubagent = true,
            parentMachineId = "box-a", parentSessionId = "p", foldedUnder = "p", foldedUnderMachineId = "box-a",
        )
        val parentWithEmbedded = base.copy(
            subagents = listOf(
                Subagent("emb1", sessionId = "p@emb1", running = true),
                Subagent("emb2", sessionId = "p@emb2", running = true),
            ),
        )
        // p@emb1 is in both embedded and held (counted once), p@emb2 is embedded-only (+1), box-b/a2a-1 is remote (+1) => 3 total.
        assertEquals(ChildRollup(ChildRollupKind.Running, 3), childRollup(parentWithEmbedded, listOf(sameKeyChild, remoteWorkingChild)))
        assertEquals(3, buildOverviewSubagents(parentWithEmbedded, listOf(sameKeyChild, remoteWorkingChild)).running.size)

        // Conflict case: embedded says running=true while authoritative standalone child row says idle -> both agree it is NOT running.
        val idleStandalone = Session("box-a", "p@emb1", state = "idle", isSubagent = true, foldedUnder = "p")
        val conflictParent = base.copy(subagents = listOf(Subagent("emb1", sessionId = "p@emb1", running = true)))
        assertNull(childRollup(conflictParent, listOf(idleStandalone)))
        val conflictOverview = buildOverviewSubagents(conflictParent, listOf(idleStandalone))
        assertTrue(conflictOverview.running.isEmpty())
        assertEquals(1, conflictOverview.ended.size)

        // Reverse conflict: embedded says running=false while standalone child row says working -> both agree it IS running.
        val workingStandalone = Session("box-a", "p@emb1", state = "working", isSubagent = true, foldedUnder = "p")
        val reverseParent = base.copy(subagents = listOf(Subagent("emb1", sessionId = "p@emb1", running = false)))
        assertEquals(ChildRollup(ChildRollupKind.Running, 1), childRollup(reverseParent, listOf(workingStandalone)))
        assertEquals(1, buildOverviewSubagents(reverseParent, listOf(workingStandalone)).running.size)
    }
}
