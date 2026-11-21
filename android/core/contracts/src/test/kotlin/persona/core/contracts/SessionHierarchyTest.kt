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
}
