package persona.core.domain.sort

import persona.core.contracts.CoreText
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.WorkState
import persona.core.domain.answer.rowLine
import persona.core.domain.ja
import persona.core.domain.zh
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant

/**
 * Just finished (#9751, #9786; docs/protocol/agent-states.md "Just finished"): a seen agent whose turn the hub's triage found
 * done before anybody looked (`work.just_finished`). It ranks after Working and Waiting in the background and before
 * Waiting, Muted and Seen, as the web's `justFinished` (`lib/agent-groups.ts`) does; a muted one is plain Seen.
 */
class JustFinishedTest {
    private val machines = listOf(Machine("m", online = true))
    private val now = Instant.parse("2026-01-05T00:00:00Z")

    private fun row(id: String, state: String, finished: Boolean = false, space: String = "space", awaits: Boolean = false,
                    background: Boolean = false, work: WorkState? = null, loop: Boolean = false) =
        AgentSession(Session(machineId = "m", sessionId = id, spaceId = space, spaceName = space, state = state, title = id,
            paneId = "w1:p$id", updatedAt = "2026-01-01T00:00:00Z", awaitsReply = awaits, backgroundWaiting = background,
            work = work ?: if (finished) WorkState("done", line = "Merged", justFinished = true) else null,
            loop = if (loop) persona.core.contracts.LoopInfo(kind = "cron") else null))

    private val all = listOf(
        row("idle", "idle"), row("finished", "idle", finished = true), row("working", "working"),
        row("background", "working", background = true), row("waiting", "idle", loop = true),
        row("muted-finished", "idle", finished = true), row("muted-working", "working"),
        row("unread", "unread"), row("needs", "needs_you"), row("awaiting", "idle", awaits = true),
    )
    private val mutes = AgentPreferences(sessions = listOf("m/muted-finished", "m/muted-working"))

    @Test fun aSeenAgentTheHubMarksJustFinishedUnlessMuted() {
        assertTrue(row("a", "idle", finished = true).justFinished(AgentPreferences()))
        assertTrue(row("a", "idle", finished = true).justFinished(mutes))
        // Only a seen one: an Unread one with the flag is Unread.
        assertFalse(row("a", "unread", finished = true).justFinished(AgentPreferences()))
        assertFalse(row("a", "idle").justFinished(AgentPreferences()))
        assertFalse(row("a", "idle", work = WorkState("done", line = "Merged")).justFinished(AgentPreferences()))
        // Muted, by the session or by its space: plain Seen.
        assertFalse(row("muted-finished", "idle", finished = true).justFinished(mutes))
        assertFalse(row("a", "idle", finished = true, space = "quiet").justFinished(AgentPreferences(spaces = listOf("m/quiet"))))
        // Finished and nobody has seen it (what Mark all as read marks): Unread, or Just finished, muted or not.
        assertTrue(row("a", "idle", finished = true).unseen)
        assertTrue(row("a", "unread").unseen)
        assertFalse(row("a", "idle").unseen)
    }

    @Test fun byStatusHasItsOwnSectionAfterTheWorkStillGoingAndBeforeWaiting() {
        val list = buildAgentList(all, machines, mutes = mutes, settings = AgentListSettings(grouping = AgentGrouping.State))
        assertEquals(listOf("needs", "unread", "awaiting", "working", "background", "just_finished", "waiting", "muted", "idle").map { "state/$it" },
            list.groups.map { it.id })
        val finished = list.groups.single { it.id == "state/just_finished" }
        assertEquals(CoreText.Key.StateJustFinished, finished.title.key)
        assertEquals("刚做完待看", finished.title.zh())
        assertEquals("完了したばかり", finished.title.ja())
        assertEquals("Just finished", finished.title.english())
        assertEquals(listOf("m/finished"), finished.visibleRows.map { it.id })
        assertFalse(finished.folded)
        // A muted one is Seen, folded with the seen ones.
        assertEquals(listOf("m/idle", "m/muted-finished"), list.groups.single { it.id == "state/idle" }.rows.map { it.id })
        assertEquals(AgentStateSections.map { "state/${it.first}" }.indexOf("state/just_finished"), agentRank(all[1], mutes))
    }

    @Test fun statusFirstRanksItAfterWorkingAndBeforeWaitingMutedAndSeen() {
        val ranks = listOf("needs", "unread", "awaiting", "working", "background", "finished", "waiting", "muted-working", "idle")
        val byId = all.associateBy { it.session.sessionId }
        assertEquals((0..8).toList(), ranks.map { agentRank(byId.getValue(it), mutes) })
        assertEquals(8, agentRank(byId.getValue("muted-finished"), mutes))
        val list = buildAgentList(all, machines, mutes = mutes, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace))
        val space = list.groups.single()
        assertEquals(listOf("needs", "unread", "awaiting", "working", "background", "finished", "waiting", "muted-working", "idle",
            "muted-finished").map { "m/$it" }, space.rows.map { it.id })
        // It stays out of "N more idle" with the busy ones; the muted one folds with the seen ones.
        assertEquals(listOf("needs", "unread", "awaiting", "working", "background", "finished", "waiting", "muted-working").map { "m/$it" },
            space.visibleRows.map { it.id })
    }

    @Test fun waitingWinsForARowThatIsBoth() {
        val both = row("both", "idle", finished = true, loop = true)
        assertTrue(both.isWaiting)
        assertEquals(listOf("state/waiting"),
            buildAgentList(listOf(both), machines, settings = AgentListSettings(grouping = AgentGrouping.State)).groups.map { it.id })
        // Awaiting reply wins too, if a hub ever sent both.
        assertEquals(2, agentRank(row("asks", "idle", finished = true, awaits = true)))
    }

    @Test fun itAndItsSpaceStayOutOfTheIdleFolds() {
        val finished = row("finished", "idle", finished = true, space = "a")
        assertTrue(finished.stays(AgentPreferences()))
        assertFalse(finished.stays(AgentPreferences(sessions = listOf("m/finished"))))
        val rows = listOf(finished, row("seen", "idle", space = "a"), row("done", "idle", space = "b"))
        assertFalse(agentSpaces(rows).single { it.name == "a" }.quiet(AgentPreferences()))
        assertTrue(agentSpaces(rows).single { it.name == "a" }.quiet(AgentPreferences(sessions = listOf("m/finished"))))
        val list = buildAgentList(rows, machines, settings = AgentListSettings(grouping = AgentGrouping.MachineSpace))
        val space = list.groups.single { it.id == "m/a" }
        assertNull(space.parent)
        assertFalse(space.folded)
        assertEquals(listOf("m/finished"), space.visibleRows.map { it.id })
        assertEquals("quiet", list.groups.single { it.id == "m/b" }.parent)
        assertEquals(listOf("m/a", "m/b"), list.groups.map { it.id })
        // Muted: a quiet space again.
        val muted = buildAgentList(rows, machines, mutes = AgentPreferences(sessions = listOf("m/finished")),
            settings = AgentListSettings(grouping = AgentGrouping.MachineSpace))
        assertEquals("quiet", muted.groups.single { it.id == "m/a" }.parent)
        // By Repository: its repository stays open.
        val repo = buildAgentList(listOf(row("finished", "idle", finished = true).let { it.copy(session = it.session.copy(cwd = "/src/alpha")) }), machines)
        val group = repo.groups.single { it.rows.isNotEmpty() }
        assertNull(group.parent)
        assertEquals(listOf("m/finished"), group.visibleRows.map { it.id })
    }

    @Test fun theRowHeldWhileItIsOpenKeepsJustFinishedsPlaceDrawnSeen() {
        // Opened: this phone's seen took the flag off (AgentsState.listSession), the shell held it as Just finished.
        val held = row("finished", "idle").heldAsFinished()
        assertTrue(held.justFinished)
        assertFalse(held.showsJustFinished)
        assertTrue(held.justFinished(AgentPreferences()))
        assertFalse(held.unseen)
        assertEquals(AgentStateSections.map { it.first }.indexOf("just_finished"), agentRank(held))
        assertTrue(held.stays(AgentPreferences()))
        assertFalse(agentSpaces(listOf(held)).single().quiet(AgentPreferences()))
        val list = buildAgentList(listOf(held, row("seen", "idle")), machines, settings = AgentListSettings(grouping = AgentGrouping.State))
        assertEquals(listOf("state/just_finished", "state/idle"), list.groups.map { it.id })
        // Drawn seen: no Just finished word, Seen's word.
        assertNull(held.rowStateWord(muted = false))
        assertEquals("Seen", held.stateWord(muted = false).english())
        assertNull(held.cardStateWord(muted = false))
        // Unlike an Unread hold, it does not go to Unread's place.
        assertEquals(1, agentRank(row("unread-held", "idle").copy(heldInList = true)))
    }

    @Test fun aTabSpeaksForItAfterAwaitingReplyAndBeforeTheSeenOnes() {
        fun tab(vararg rows: AgentSession, mutes: AgentPreferences = AgentPreferences()) =
            agentTabs(rows.mapIndexed { i, r -> r.copy(session = r.session.copy(slotId = "t1", paneIndex = i)) }, mutes).values.single()
        val seen = row("seen", "idle")
        val finished = row("finished", "idle", finished = true)
        assertEquals("finished", tab(seen, finished).lead.session.sessionId)
        assertEquals("seen", tab(seen, finished, mutes = AgentPreferences(sessions = listOf("m/finished"))).lead.session.sessionId)
        assertEquals("asks", tab(finished, row("asks", "idle", awaits = true)).lead.session.sessionId)
        listOf("unread", "working", "needs_you").forEach { state ->
            assertEquals(state, "other", tab(finished, row("other", state)).lead.session.sessionId)
        }
    }

    @Test fun theRowSaysJustFinishedInTheUnreadToneAndKeepsTheTriagesConclusion() {
        val finished = row("finished", "idle", finished = true)
        assertEquals("Just finished", finished.rowStateWord(muted = false)?.english())
        assertEquals("Just finished", finished.stateWord(muted = false).english())
        assertEquals("Just finished", finished.cardStateWord(muted = false)?.english())
        assertEquals("Just finished", finished.rowContent().stateWord?.english())
        // Under its own section the row does not repeat it.
        assertNull(finished.rowContent(AgentRowContext(stateSection = true)).stateWord)
        // Muted: plain Seen.
        assertNull(finished.rowStateWord(muted = true))
        assertEquals("Seen", finished.stateWord(muted = true).english())
        assertNull(finished.cardStateWord(muted = true))
        // Awaiting reply's word wins; the plain state stays Seen (a chat's header).
        assertEquals("Awaiting reply", row("both", "idle", finished = true, awaits = true).stateWord(muted = false).english())
        assertEquals("Seen", finished.stateLabel.english())
        // The second line is the triage's "✓ conclusion".
        assertEquals("✓ Merged", finished.session.rowLine(null, now)?.english())
    }
}
