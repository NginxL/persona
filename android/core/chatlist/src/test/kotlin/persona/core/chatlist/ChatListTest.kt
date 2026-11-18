package persona.core.chatlist

import org.junit.Assert.*
import org.junit.Test

class ChatListTest {
    private data class Message(val id: String, val seq: Int, val text: String = "原文", val incoming: Boolean = true)

    @Test fun frozenWindowAdmitsPagesAndInteriorHolesButDefersNewIncomingMessages() {
        val window = ChatListWindow<Message>({ it.id }, compareBy { it.seq }, { it.incoming })
        val first = Message("first", 10)
        val last = Message("last", 30)
        window.freeze(listOf(first, last))
        val old = Message("old", 1)
        val hole = Message("hole", 20)
        val new = Message("new", 31)
        val own = Message("own", 32, incoming = false)
        val live = listOf(old, first.copy(text = "修改"), hole, last, new, new, own)
        assertEquals(listOf(old, first, hole, last), window.items(live))
        assertEquals(1, window.newMessageCount(live))
        assertEquals(2, window.newMessageCount(live, below = hole))
        assertEquals(1, window.newMessageCount(live, below = hole, lastRead = last))
        window.freeze(live)
        assertFalse(window.items(live).any { it.id == "new" })
        assertTrue(window.items(listOf(last)).any { it.id == "hole" })
        window.follow()
        assertEquals(live, window.items(live))
        assertEquals(0, window.newMessageCount(live))
    }

    @Test fun deferredStreamingCountDoesNotRescanAndSettlesWithOriginalRule() {
        var idCalls = 0
        var unreadCalls = 0
        val window = ChatListWindow<Message>(
            { idCalls++; it.id },
            compareBy { it.seq },
            { unreadCalls++; it.incoming },
        )
        val last = Message("last", 10)
        window.freeze(listOf(last))
        val draft = Message("draft", 11)
        assertEquals(1, window.newMessageCount(listOf(last, draft, draft)))
        idCalls = 0
        unreadCalls = 0

        val firstChunk = listOf(last, draft.copy(text = "一"), draft.copy(text = "一"))
        val secondChunk = listOf(
            last,
            draft.copy(text = "一二"),
            draft.copy(text = "一二"),
            Message("own", 12, incoming = false),
        )
        val settled = listOf(
            last,
            draft.copy(text = "完成"),
            draft.copy(text = "完成"),
            Message("new", 12),
            Message("own", 13, incoming = false),
            Message("own", 13, incoming = false),
        )
        listOf(firstChunk, secondChunk, settled).forEach { update ->
            assertEquals(1, window.newMessageCount(update, defer = true))
            assertEquals(0, idCalls)
            assertEquals(0, unreadCalls)
        }

        // The oracle is the original rule: de-duplicate by ID, then count unread rows after the boundary.
        val expected = settled.distinctBy { it.id }.count { it.seq > last.seq && it.incoming }
        assertEquals(2, expected)
        assertEquals(expected, window.newMessageCount(settled, defer = false))
        assertTrue(idCalls > 0)
        assertTrue(unreadCalls > 0)

        window.follow()
        assertEquals(0, window.newMessageCount(settled, defer = true))
        window.freeze(listOf(last))
        idCalls = 0
        unreadCalls = 0
        assertEquals(expected, window.newMessageCount(settled, defer = true))
        assertTrue(idCalls > 0)
        assertTrue(unreadCalls > 0)
    }

    @Test fun prefetchRequiresUserIntentWhileLastLineUsesMeasuredExtent() {
        assertFalse(shouldPrefetch(false, true, false, 95, 100, 5))
        assertFalse(shouldPrefetch(true, true, true, 95, 100, 5))
        assertFalse(shouldPrefetch(true, false, false, 95, 100, 5))
        assertFalse(shouldPrefetch(true, true, false, 93, 100, 5))
        assertTrue(shouldPrefetch(true, true, false, 94, 100, 5))
        assertEquals(-672, lastLineScrollOffset(700, 28))
        assertEquals(0, lastLineScrollOffset(20, 28))
        // Under a 100px top bar the last line sits right below it.
        assertEquals(-572, lastLineScrollOffset(700, 28, topPadding = 100))
        assertEquals(0, lastLineScrollOffset(120, 28, topPadding = 100))
    }
}
