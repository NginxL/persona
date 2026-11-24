package persona.feature.inbox

import persona.core.chatlist.*
import persona.core.contracts.api.InboxAssistantTransport
import kotlinx.serialization.json.*
import persona.core.inbox.*

internal data class LarkListNavigation(val readPosition: Long? = null, val unread: Boolean = false, val mention: Long? = null)
internal data class LarkListTarget(val around: Long?, val entry: Long?, val restore: Boolean = false, val request: Int = 0)

internal suspend fun larkListNavigation(transport: InboxAssistantTransport, feed: String): LarkListNavigation {
    var cursor: String? = null
    val seen = mutableSetOf<String>()
    do {
        val page = transport.larkConversations(cursor)
        val row = (page["conversations"] as? JsonArray).orEmpty().filterIsInstance<JsonObject>().firstOrNull { it.text("id") == feed }
        if (row != null) return LarkListNavigation(row.number("read_position"), (row.number("unread") ?: 0) > 0, row.number("mention_position"))
        cursor = page.cursor("next_cursor")
        check(cursor == null || seen.add(cursor)) { "Conversation cursor did not advance" }
    } while (cursor != null)
    return LarkListNavigation()
}

internal fun larkListTarget(navigation: LarkListNavigation, start: Long?, saved: ChatReadingPosition?, savedPosition: Long?): LarkListTarget {
    val unread = navigation.readPosition?.takeIf { navigation.unread && it < Long.MAX_VALUE }?.plus(1)
    if (unread != null) return LarkListTarget(unread, unread)
    if (saved != null && !saved.followsEnd && saved.rowId != null && savedPosition != null)
        return LarkListTarget(savedPosition, null, restore = true)
    if (start != null) return LarkListTarget(start, start)
    // Unknown read state must never turn opening a conversation into an implicit tail request.
    return if (navigation.readPosition != null && !navigation.unread) LarkListTarget(null, null)
        else LarkListTarget(0, 0)
}

internal val larkListOrder: Comparator<LarkMessage> = compareBy<LarkMessage> { it.position ?: Long.MAX_VALUE }.thenBy { it.listKey() }
internal fun larkListWindow() = LarkListWindow()

/** Pending bubbles have no server position and must not become the frozen server boundary. */
internal class LarkListWindow {
    private val window = ChatListWindow<LarkMessage>({ it.listKey() }, larkListOrder,
        { !it.mine && !it.system && !it.isRead && it.delivery == LarkDelivery.Sent })
    private var pending: List<LarkMessage>? = null

    fun freeze(messages: List<LarkMessage>) {
        val positioned = messages.filter { it.position != null }
        if (window.isFrozen || positioned.isEmpty()) return
        pending = messages.filter { it.position == null }
        window.freeze(positioned)
    }
    fun follow() { window.follow(); pending = null }
    fun items(messages: List<LarkMessage>): List<LarkMessage> {
        val local = pending ?: messages.filter { it.position == null }
        val localKeys = local.map { it.listKey() }.toSet()
        return window.items(messages.filter { it.position != null }).filterNot { it.listKey() in localKeys } + local
    }
    fun newMessageCount(messages: List<LarkMessage>, below: LarkMessage? = null, lastRead: LarkMessage? = null): Int =
        window.newMessageCount(messages.filter { it.position != null }, below?.takeIf { it.position != null }, lastRead)
}

internal fun larkListEntry(messages: List<LarkMessage>, position: Long?): ChatListEntry? = position?.let { start ->
    messages.firstOrNull { (it.position ?: Long.MIN_VALUE) >= start }?.let { ChatListEntry(it.listKey(), alignTop = true) }
}
