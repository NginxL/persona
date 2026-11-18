package persona.core.data

import persona.core.contracts.DeltaEvent
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.ToolCallEvent
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Test
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.assertSame
import org.junit.Assert.assertNotSame

class ChatRealtimeStateTest {
    private val key = SessionKey("m1", "s1")

    @Test fun unchangedTranscriptReusesRowsAndEveryOverlayChangeInvalidatesThem() {
        val state = ChatRealtimeState(key)
        val transcript = listOf(Item("user#0", 1, role = "user", kind = "text", text = "Question"),
            Item("tool#0", 2, role = "assistant", kind = "tool_use", toolUseId = "call"))
        val first = state.items(transcript)
        assertSame(transcript.first(), first.first())
        repeat(100) { assertSame(first, state.items(transcript)) }
        state.tool(ToolCallEvent("call", "example_tool"), transcript)
        val named = state.items(transcript)
        assertNotSame(first, named)
        assertEquals("example_tool", named.last().toolName)
        state.delta(DeltaEvent("m1", "s1", "Reply"), transcript)
        assertEquals("Reply", state.items(transcript).last().text)
        state.delta(DeltaEvent("m1", "s1", "Updated reply"), transcript)
        assertEquals("Updated reply", state.items(transcript).last().text)
        state.reset()
        assertEquals(transcript, state.items(transcript))
    }

    @Test
    fun `ordinary text deltas keep transcript projection identity and update only live text`() {
        val state = ChatRealtimeState(key)
        val transcript = List(200) { index ->
            Item("history#$index", index, role = if (index % 2 == 0) "user" else "assistant",
                kind = "text", text = "settled $index")
        }
        state.delta(DeltaEvent("m1", "s1", "hello"), transcript)
        val before = state.items(transcript)
        val settled = before.dropLast(1)

        state.delta(DeltaEvent("m1", "s1", "hello world"), transcript)
        val after = state.items(transcript)

        assertSame(before, after)
        assertEquals(settled, after.dropLast(1))
        assertEquals("hello world", after.last().text)
        assertEquals(transcript, state.items(transcript).dropLast(1))
    }

    @Test fun aToolCallForAnotherSessionKeepsTheProjection() {
        val state = ChatRealtimeState(key)
        val transcript = listOf(Item("tool#0", 1, role = "assistant", kind = "tool_use", toolUseId = "mine"))
        val first = state.items(transcript)
        assertFalse(state.tool(ToolCallEvent("elsewhere", "example_tool"), transcript))
        assertSame(first, state.items(transcript))
    }

    @Test
    fun `a streamed reply the hub cut matches its transcript item once`() {
        val state = ChatRealtimeState(key)
        val user = Item("u1#0", 1, role = "user", kind = "text", text = "写长一点")
        val whole = "x".repeat(30_000)
        state.delta(DeltaEvent("m1", "s1", whole), listOf(user))
        state.delta(DeltaEvent("m1", "s1", ""), listOf(user))
        val cut = Item("a1#0", 2, role = "assistant", kind = "text",
            text = whole.take(24 * 1024) + "\n…（已截断，共 30000 字节）", fullSize = 30_000)

        val rows = state.items(listOf(user, cut)).filter { it.role == "assistant" && it.kind == "text" }

        assertEquals(listOf(cut.text), rows.map { it.text })
    }

    @Test
    fun `a tool call redraws only the session that shows its row`() {
        val state = ChatRealtimeState(key)
        val other = Item("t1#0", 1, role = "assistant", kind = "tool_use", toolUseId = "other-call")
        val args = buildJsonObject { put("query", "example") }

        // Another session's call: kept for a row that may still come, nothing here to redraw.
        assertFalse(state.tool(ToolCallEvent("call", "phone_example", args), listOf(other)))
        val row = Item("t2#0", 2, role = "assistant", kind = "tool_use", toolUseId = "call")
        assertEquals("phone_example", state.items(listOf(other, row)).last().toolName)

        // This session's call names a row already on screen.
        assertTrue(state.tool(ToolCallEvent("call", "phone_updated", args), listOf(other, row)))
        assertEquals("phone_updated", state.items(listOf(other, row)).last().toolName)
        // A text item with the same id is not the call's row.
        assertFalse(state.tool(ToolCallEvent("t3", "x"), listOf(Item("t3#0", 3, role = "assistant", kind = "text", toolUseId = "t3"))))
    }
}
