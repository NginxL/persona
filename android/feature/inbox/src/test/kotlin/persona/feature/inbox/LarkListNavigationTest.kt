package persona.feature.inbox

import persona.core.chatlist.ChatReadingPosition
import org.junit.Assert.*
import org.junit.Test
import persona.core.inbox.*

class LarkListNavigationTest {
    @Test fun unreadEntryWinsAndSavedAnchorRestoresWithoutOpeningTail() {
        val saved = ChatReadingPosition("message:old", -18, followsEnd = false)
        assertEquals(LarkListTarget(18, 18), larkListTarget(LarkListNavigation(17, true, 19), 10, saved, 5))
        assertEquals(LarkListTarget(5, null, restore = true), larkListTarget(LarkListNavigation(30), 10, saved, 5))
        assertEquals(LarkListTarget(10, 10), larkListTarget(LarkListNavigation(), 10, null, null))
        assertEquals(LarkListTarget(0, 0), larkListTarget(LarkListNavigation(), null, null, null))
        val messages = listOf(message(17), message(19), message(21))
        assertEquals("message:m19", larkListEntry(messages, 18)?.rowId)
        assertTrue(larkListEntry(messages, 18)!!.alignTop)
        assertEquals("message:m21", larkListEntry(messages, 21)?.rowId)
    }

    @Test fun frozenLarkWindowAdmitsHistoryAndCountsOnlyUnreadIncomingBelowViewport() {
        val timeline = larkListWindow()
        val first = message(10)
        val last = message(30)
        val pending = message(40).copy(position = null, mine = true, clientId = "pending", delivery = LarkDelivery.Failed)
        timeline.freeze(listOf(first, last, pending))
        val hole = message(20)
        val incoming = message(31)
        val live = listOf(message(5), first.copy(parts = listOf(LarkPart.Text("编辑"))), hole, last, incoming,
            message(32).copy(mine = true), message(33).copy(system = true), message(34).copy(isRead = true), pending)
        assertEquals(listOf(5L, 10L, 20L, 30L, null), timeline.items(live).map { it.position })
        assertEquals(first, timeline.items(live)[1])
        assertEquals(1, timeline.newMessageCount(live))
        assertEquals(2, timeline.newMessageCount(live, below = hole))
        assertEquals(1, timeline.newMessageCount(live, below = hole, lastRead = last))
        val echoed = timeline.items(live + pending.copy(position = 25, delivery = LarkDelivery.Sent))
        assertEquals(1, echoed.count { it.listKey() == pending.listKey() })
        assertEquals(pending, echoed.last())
        timeline.follow()
        assertEquals(live, timeline.items(live))
        assertEquals(0, timeline.newMessageCount(live))
    }

    private fun message(position: Long) = LarkMessage("m$position", "sender", "发送者", position,
        parts = listOf(LarkPart.Text("消息")))
}
