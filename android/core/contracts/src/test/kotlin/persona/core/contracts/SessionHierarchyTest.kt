package persona.core.contracts

import org.junit.Assert.*
import org.junit.Test

class SessionHierarchyTest {
    @Test fun fullAndSummaryRowsDecodeAdditiveHierarchyAndOldRowsKeepTheirOwnState() {
        val json = """{"machine_id":"m","session_id":"p","state":"idle","children":{"total":3,"working":1,"needs_you":1,"workers":1},"started_at":"2026-11-14T10:00:00Z","asking_parent_session_id":"ancestor","folded_under":"ancestor","folded_as":"worker"}"""
        val row = JsonCodec.decodeFromString(Session.serializer(), json)
        assertEquals(SessionChildren(3, 1, 1, 1), row.children)
        assertEquals("ancestor", row.foldedUnder)
        assertEquals("ancestor", row.askingParentSessionId)
        assertEquals("worker", row.foldedAs)
        assertNotNull(parseTime(row.startedAt))
        assertEquals(AgentState.NeedsYou, row.foldedState)
        assertEquals(AgentState.Idle, row.agentState)
        val old = JsonCodec.decodeFromString(Session.serializer(), """{"session_id":"old","state":"unread"}""")
        assertNull(old.children)
        assertEquals(old.agentState, old.foldedState)
    }

    @Test fun historyLegsDecodeInOrderWithoutRequiringTheNewFieldOnOlderRows() {
        val row = JsonCodec.decodeFromString(Session.serializer(), """{"machine_id":"m","session_id":"latest",
            "engine_history":[{"session_id":"first","agent":"claude","until":"2026-11-20T01:00:00Z"},
            {"session_id":"second","agent":"codex","until":"2026-11-20T02:00:00Z"}]}""")
        assertEquals(listOf("first", "second"), row.engineHistory.map { it.sessionId })
        assertEquals(listOf("claude", "codex"), row.engineHistory.map { it.agent })
        assertNotNull(parseTime(row.engineHistory.first().until))
        assertTrue(JsonCodec.decodeFromString(Session.serializer(), """{"session_id":"old"}""").engineHistory.isEmpty())
    }

    @Test fun interactionsHaveAParentHomeAndKeepTheOriginalAnswerTarget() {
        val row = JsonCodec.decodeFromString(Interaction.serializer(), """{"id":"q","machine_id":"m","session_id":"child","folded_under":"parent","session_title":"Review"}""")
        assertEquals(SessionKey("m", "parent"), row.homeKey)
        assertEquals(SessionKey("m", "child"), row.sessionKey)
        assertEquals("q", row.id)
    }

    @Test fun sideSessionFieldsDecodeAndOnlyASideUnreadMakesTheParentUnread() {
        val parent = JsonCodec.decodeFromString(Session.serializer(), """{"machine_id":"m","session_id":"p","state":"idle",
            "children":{"total":3,"working":1,"sides":2,"sides_working":1,"sides_needs_you":0,"sides_unread":1}}""")
        assertEquals(SessionChildren(3, working = 1, sides = 2, sidesWorking = 1, sidesUnread = 1), parent.children)
        assertEquals(AgentState.Working, parent.foldedState)
        assertEquals(AgentState.Unread, parent.copy(children = parent.children?.copy(working = 0, sidesWorking = 0)).foldedState)
        assertEquals(AgentState.Idle, parent.agentState)
        val side = JsonCodec.decodeFromString(Session.serializer(),
            """{"machine_id":"m","session_id":"s","state":"unread","parent_agent_id":"p","folded_under":"p","folded_as":"side"}""")
        assertEquals("p", side.parentAgentId)
        assertEquals(FoldedAs.SIDE, side.foldedAs)
        assertEquals(AgentState.Unread, side.agentState)
        val sub = JsonCodec.decodeFromString(Session.serializer(), """{"machine_id":"m","session_id":"c","folded_under":"p","folded_side":"s"}""")
        assertEquals("s", sub.foldedSide)
        val card = JsonCodec.decodeFromString(Interaction.serializer(),
            """{"id":"q","machine_id":"m","session_id":"c","folded_under":"p","folded_as":"","folded_side":"s"}""")
        assertEquals("s", card.foldedSide)
        assertEquals(SessionKey("m", "p"), card.homeKey)
        val old = JsonCodec.decodeFromString(Session.serializer(), """{"session_id":"old","children":{"total":1}}""")
        assertNull(old.foldedSide)
        assertNull(old.parentAgentId)
        assertEquals(0, old.children?.sidesUnread)
    }

    @Test fun crossMachineHierarchyFieldsDecodeAndResolveParentHomesWithDefaults() {
        val childJson = """{"machine_id":"box-b","session_id":"sub-1","state":"working","is_subagent":true,"parent_session_id":"p1","parent_machine_id":"box-a","folded_under":"p1","folded_under_machine_id":"box-a","a2a_task_id":"task-42","subagent_origin":"a2a"}"""
        val child = JsonCodec.decodeFromString(Session.serializer(), childJson)
        assertEquals("box-a", child.parentMachineId)
        assertEquals("box-a", child.foldedUnderMachineId)
        assertEquals("task-42", child.a2aTaskId)
        assertEquals("a2a", child.subagentOrigin)
        assertEquals(SessionKey("box-a", "p1"), child.foldedUnderKey)
        assertEquals(SessionKey("box-a", "p1"), child.parentKey)

        val cardJson = """{"id":"q-remote","machine_id":"box-b","session_id":"sub-1","folded_under":"p1","folded_under_machine_id":"box-a","kind":"permission"}"""
        val card = JsonCodec.decodeFromString(Interaction.serializer(), cardJson)
        assertEquals("box-a", card.foldedUnderMachineId)
        assertEquals(SessionKey("box-a", "p1"), card.homeKey)
        assertEquals(SessionKey("box-b", "sub-1"), card.sessionKey)

        // Fault-tolerant defaults when omitted on older hubs.
        val legacy = JsonCodec.decodeFromString(Session.serializer(), """{"machine_id":"m","session_id":"s","folded_under":"p"}""")
        assertNull(legacy.parentMachineId)
        assertNull(legacy.foldedUnderMachineId)
        assertNull(legacy.a2aTaskId)
        assertNull(legacy.subagentOrigin)
        assertEquals(SessionKey("m", "p"), legacy.foldedUnderKey)

        // Interaction without folded_under ignores folded_under_machine_id for homeKey.
        val unfoldedCard = JsonCodec.decodeFromString(Interaction.serializer(), """{"id":"q-own","machine_id":"box-b","session_id":"sub-1","folded_under_machine_id":"box-a"}""")
        assertEquals(SessionKey("box-b", "sub-1"), unfoldedCard.homeKey)
    }
}
