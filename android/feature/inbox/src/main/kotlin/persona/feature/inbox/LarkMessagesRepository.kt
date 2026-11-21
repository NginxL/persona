package persona.feature.inbox

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.designsystem.UiText

/** One direct or group chat in the optional Messages list. This is summary metadata, never a prefetched message window. */
data class LarkChatSummary(
    val id: String,
    val name: String,
    val avatarRef: String? = null,
    val isGroup: Boolean = false,
    val isTopic: Boolean = false,
    val unread: Int = 0,
    val markedUnread: Boolean = false,
    val muted: Boolean = false,
    val preview: String = "",
    val previewSender: String? = null,
    val previewMine: Boolean = false,
    val updatedAt: String? = null,
    val readPosition: Long? = null,
    val mentionPosition: Long? = null,
)

/** The loaded window. A refresh reads the same number of pages again and replaces it only after every page succeeds. */
data class LarkMessagesState(
    val rows: List<LarkChatSummary> = emptyList(),
    val nextCursor: String? = null,
    val loaded: Boolean = false,
    val loading: Boolean = true,
    val loadingMore: Boolean = false,
    val failure: UiText? = null,
    val moreFailure: UiText? = null,
)

/** The default Lark account's conversation list for one immutable pairing. */
interface LarkMessagesRepository {
    val scope: String
    val state: StateFlow<LarkMessagesState>
    suspend fun refresh()
    suspend fun loadMore()
    fun avatar(ref: String?): AuthenticatedLarkImage?
}

class RealLarkMessagesRepository(
    override val scope: String,
    private val transport: InboxAssistantTransport,
) : LarkMessagesRepository {
    private val mutable = MutableStateFlow(LarkMessagesState())
    override val state = mutable.asStateFlow()
    private val operation = Mutex()
    private var loadedPages = 1
    private var seenCursors = mutableSetOf<String>()

    override suspend fun refresh() = operation.withLock {
        mutable.update { it.copy(loading = true, failure = null, moreFailure = null) }
        try {
            val window = readWindow(loadedPages)
            loadedPages = window.pages
            seenCursors = window.cursors
            mutable.value = LarkMessagesState(
                rows = window.rows,
                nextCursor = window.next,
                loaded = true,
                loading = false,
            )
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(failure = failureReason(error)) }
        } finally {
            mutable.update { it.copy(loading = false) }
        }
    }

    override suspend fun loadMore() {
        if (state.value.loading || state.value.loadingMore || state.value.nextCursor == null) return
        operation.withLock {
            val cursor = state.value.nextCursor ?: return@withLock
            if (state.value.loading || state.value.loadingMore) return@withLock
            mutable.update { it.copy(loadingMore = true, moreFailure = null) }
            try {
                val page = readPage(cursor)
                val next = advance(page.nextCursor, seenCursors)
                mutable.update {
                    it.copy(
                        rows = mergeLarkChats(it.rows, page.rows),
                        nextCursor = next,
                        loaded = true,
                        loadingMore = false,
                    )
                }
                loadedPages += 1
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                mutable.update { it.copy(moreFailure = failureReason(error)) }
            } finally {
                mutable.update { it.copy(loadingMore = false) }
            }
        }
    }

    override fun avatar(ref: String?): AuthenticatedLarkImage? = larkAvatar(ref, scope, transport::avatar)

    private suspend fun readWindow(pages: Int): LoadedWindow {
        var rows = emptyList<LarkChatSummary>()
        var cursor: String? = null
        val cursors = mutableSetOf<String>()
        var read = 0
        do {
            val page = readPage(cursor)
            rows = mergeLarkChats(rows, page.rows)
            read += 1
            cursor = advance(page.nextCursor, cursors)
        } while (cursor != null && read < pages)
        return LoadedWindow(rows, cursor, read, cursors)
    }

    private suspend fun readPage(cursor: String?): LarkChatPage {
        val body = transport.larkConversations(cursor)
        val page = JsonCodec.decodeFromJsonElement<LarkConversationPageDto>(body)
        return LarkChatPage(page.conversations.mapNotNull { it.summary() }, page.nextCursor)
    }

    /** A repeated cursor ends the list instead of requesting the same page forever. */
    private fun advance(raw: String?, seen: MutableSet<String>): String? {
        val next = raw?.takeIf { it.isNotBlank() } ?: return null
        return next.takeIf { seen.add(it) }
    }

    private data class LoadedWindow(
        val rows: List<LarkChatSummary>,
        val next: String?,
        val pages: Int,
        val cursors: MutableSet<String>,
    )
}

private data class LarkChatPage(val rows: List<LarkChatSummary>, val nextCursor: String?)

/** One copy per id in server order; a later page never moves an existing row. */
internal fun mergeLarkChats(vararg batches: List<LarkChatSummary>): List<LarkChatSummary> {
    val seen = mutableSetOf<String>()
    return buildList {
        batches.forEach { batch -> batch.forEach { row ->
            if (row.id.isNotBlank() && seen.add(row.id)) add(row)
        } }
    }
}

@Serializable
private data class LarkConversationPageDto(
    val conversations: List<LarkConversationDto> = emptyList(),
    @SerialName("next_cursor") val nextCursor: String? = null,
)

@Serializable
private data class LarkConversationDto(
    val id: String = "",
    val name: String = "",
    val avatar: String? = null,
    @SerialName("avatar_url") val avatarUrl: String? = null,
    @SerialName("is_group") val isGroup: Boolean = false,
    @SerialName("is_topic") val isTopic: Boolean = false,
    val unread: Int = 0,
    @SerialName("marked_unread") val markedUnread: Boolean = false,
    val muted: Boolean = false,
    val preview: String = "",
    @SerialName("preview_sender") val previewSender: String? = null,
    @SerialName("is_mine") val previewMine: Boolean = false,
    @SerialName("updated_at") val updatedAt: String? = null,
    @SerialName("read_position") val readPosition: String? = null,
    @SerialName("mention_position") val mentionPosition: String? = null,
)

private fun LarkConversationDto.summary(): LarkChatSummary? = id.takeIf { it.isNotBlank() }?.let {
    LarkChatSummary(
        id = it,
        name = name,
        avatarRef = avatarUrl ?: avatar,
        isGroup = isGroup,
        isTopic = isTopic,
        unread = unread.coerceAtLeast(0),
        markedUnread = markedUnread,
        muted = muted,
        preview = preview,
        previewSender = previewSender,
        previewMine = previewMine,
        updatedAt = updatedAt,
        readPosition = readPosition?.toLongOrNull(),
        mentionPosition = mentionPosition?.toLongOrNull(),
    )
}
