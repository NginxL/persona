package persona.feature.inbox

import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.LarkThreadTransport
import persona.core.hub.LarkThreadReplyTransport
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.serialization.json.JsonObject

data class LarkThreadPage(
    val messages: List<LarkMessage>,
    val hasOlder: Boolean,
    val olderCursor: String?,
)

fun interface LarkThreadSource {
    suspend fun page(before: String?): LarkThreadPage
    val canReply: Boolean get() = false
    suspend fun reply(request: ReplyRequest): Boolean = error("Thread replies are unavailable")
}

/** Carry the pairing-bound reader, and the [manager] the chat is read as, with the message through existing repository wrappers. */
fun larkThreadSource(data: JsonObject, transport: InboxAssistantTransport, feed: String, manager: String? = null): LarkThreadSource? {
    val reader = transport as? LarkThreadTransport ?: return null
    val root = data.text("id").takeIf { it.isNotBlank() } ?: return null
    val source = LarkContentSource(transport, manager)
    return object : LarkThreadSource {
        override val canReply = transport is LarkThreadReplyTransport
        override suspend fun reply(request: ReplyRequest): Boolean =
            source.read { (transport as LarkThreadReplyTransport).replyInThread(root, request.text, request.clientId) }.flag("ok")
        override suspend fun page(before: String?): LarkThreadPage {
            val page = source.read { reader.thread(root, before) }
            return LarkThreadPage(page.messages().map { larkMessage(it, transport, feed, manager) },
                page.flag("has_older"), page.cursor("older_cursor"))
        }
    }
}

data class LarkThreadState(
    val messages: List<LarkMessage> = emptyList(),
    val loading: Boolean = false,
    val loaded: Boolean = false,
    val hasOlder: Boolean = false,
    @androidx.annotation.StringRes val error: Int? = null,
    val failedOlder: Boolean = false,
    val revision: Int = 0,
)

/** Keep server order: thread positions can be negative and are not conversation cursors. */
class LarkThreadLoader(private val rootId: String, private val source: LarkThreadSource) {
    private val mutable = MutableStateFlow(LarkThreadState())
    val state = mutable.asStateFlow()
    private val lock = Mutex()
    private var cursor: String? = null
    private val usedCursors = mutableSetOf<String>()

    suspend fun load(older: Boolean = false) {
        // A second older page is not asked for while a read runs; the newest page (after a reply, "latest") waits for it
        // and then is read, so what it brings is never dropped (#9937).
        if (!older) lock.lock() else if (!lock.tryLock()) return
        val previous = mutable.value
        try {
            if (older && (!previous.loaded || !previous.hasOlder)) return
            mutable.value = previous.copy(loading = true, error = null)
            val before = if (older) requireNotNull(cursor) else null
            val page = source.page(before)
            check(!page.hasOlder || !page.olderCursor.isNullOrBlank()) { "Missing thread cursor" }
            check(!older || !page.hasOlder || page.olderCursor != before && page.olderCursor !in usedCursors) {
                "Thread cursor did not advance"
            }
            val incoming = page.messages.filter { it.id != rootId }.distinctBy { it.id }
            val updated = incoming.associateBy { it.id }
            val messages = if (older) incoming.filter { row -> previous.messages.none { it.id == row.id } } +
                previous.messages.map { updated[it.id] ?: it } else incoming
            if (!older) usedCursors.clear()
            before?.let(usedCursors::add)
            cursor = page.olderCursor
            mutable.value = LarkThreadState(messages = messages, loaded = true, hasOlder = page.hasOlder,
                revision = previous.revision + if (older) 0 else 1)
        } catch (e: CancellationException) {
            mutable.value = previous
            throw e
        } catch (_: Exception) {
            mutable.value = previous.copy(error = R.string.inbox_lark_thread_failed, failedOlder = older)
        } finally {
            lock.unlock()
        }
    }
}
