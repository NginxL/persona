package persona.core.chatlist

/** Items must belong to one verified, continuous window, ordered oldest to newest. */
class ChatListWindow<T>(
    private val id: (T) -> String,
    private val order: Comparator<T>,
    private val countsAsUnread: (T) -> Boolean,
) {
    private var frozen: LinkedHashMap<String, T>? = null
    private var boundary: T? = null
    private var heldCount: Int? = null

    val isFrozen: Boolean get() = frozen != null

    @Synchronized fun freeze(items: List<T>) {
        if (frozen != null) return
        frozen = items.associateByTo(linkedMapOf(), id)
        boundary = items.maxWithOrNull(order)
    }

    @Synchronized fun follow() { frozen = null; boundary = null; heldCount = null }

    /** Preserve frozen values while admitting older pages and holes anywhere before the boundary. */
    @Synchronized fun items(live: List<T>): List<T> {
        val snapshot = frozen ?: return live
        val end = boundary
        if (end != null) live.filter { order.compare(it, end) <= 0 }.forEach { snapshot.putIfAbsent(id(it), it) }
        return snapshot.values.sortedWith(order)
    }

    /**
     * A measured visible boundary also counts messages already loaded below the current viewport. While [defer] holds,
     * keep the last count: streaming deltas must not rescan the transcript on the main thread. The next settled call,
     * or [follow], restores the exact count under the original rule.
     */
    @Synchronized fun newMessageCount(live: List<T>, below: T? = null, lastRead: T? = null, defer: Boolean = false): Int {
        if (frozen == null) return 0
        if (defer) heldCount?.let { return it }
        val end = listOfNotNull(below ?: boundary, lastRead).maxWithOrNull(order) ?: return 0
        return live.distinctBy(id).count { order.compare(it, end) > 0 && countsAsUnread(it) }.also { heldCount = it }
    }
}

data class ChatReadingKey(val scope: String, val conversation: String)

data class ChatReadingPosition(
    val rowId: String? = null,
    val offsetPx: Int = 0,
    val followsEnd: Boolean = true,
    val expanded: Set<String> = emptySet(),
)

/** Supply a scope-owned cache with ordered durable writes; these calls must not block the UI thread. */
interface ChatAnchorStore {
    fun read(key: ChatReadingKey): ChatReadingPosition?
    fun write(key: ChatReadingKey, position: ChatReadingPosition)
}

data class ChatListEntry(val rowId: String, val lastLineAtTop: Boolean = false, val offsetPx: Int = 0, val alignTop: Boolean = false)

internal fun shouldPrefetch(dragged: Boolean, hasOlder: Boolean, loading: Boolean, lastVisible: Int, rows: Int, distance: Int): Boolean =
    dragged && hasOlder && !loading && rows > 0 && lastVisible >= (rows - 1 - distance).coerceAtLeast(0)

/**
 * Reverse layout coordinates grow from the latest end towards history. [topPadding] is the list's content padding at
 * the top (the bar the page floats over it): the last line goes right under it, not behind it.
 */
internal fun lastLineScrollOffset(viewportEnd: Int, lastLineExtent: Int, topPadding: Int = 0): Int =
    -(viewportEnd - topPadding - lastLineExtent).coerceAtLeast(0)
