package persona.core.contracts.api

import persona.core.contracts.Item
import persona.core.contracts.ItemsResponse
import persona.core.contracts.ModeChange
import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow

data class ChatSnapshot(
    val items: List<Item> = emptyList(),
    val loading: Boolean = true,
    val hasOlder: Boolean = false,
    val loadingOlder: Boolean = false,
    val error: String? = null,
    val ended: Boolean = false,
    val replaced: Boolean = false,
    val stopping: Boolean = false,
    val verified: Boolean = true,
    val lastSeenMessageId: String? = null,
    val turnDurationSeconds: Long? = null,
    val streamingItemIds: Set<String> = emptySet(),
    /** Current text for streaming items. Structural observation leaves their row's Item at the last structural event. */
    val liveText: Map<String, StateFlow<String>> = emptyMap(),
    val history: String? = null,
    val recordedTurnDurations: Map<String, Long> = emptyMap(),
    /** The oldest page boundary can survive in the cache before a fresh subscription verifies the latest items. */
    val olderBoundaryKnown: Boolean = verified,
)

interface ChatRepository {
    /** Complete current text in every snapshot, including each ordinary streaming delta. */
    fun observe(session: SessionKey): Flow<ChatSnapshot>

    suspend fun loadOlder(session: SessionKey)
    /** One read-only page of an earlier engine's transcript. Null only when the hub explicitly reports it gone. */
    suspend fun earlierEnginePage(session: SessionKey, beforeSeq: Int?): ItemsResponse? =
        throw UnsupportedOperationException("Earlier engine history unavailable")

    /** Read-only mode history, joined at presentation time rather than added to [ChatSnapshot.items]. */
    suspend fun modeChanges(session: SessionKey): List<ModeChange> = emptyList()
    suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item>

    /**
     * Items in `[fromSeq, throughSeq]` for search in chat, without opening the fold in the transcript
     * (`GET …/items?from_seq=&through_seq=`, the same pages [expandFold] reads). Empty where a repository
     * cannot read a range on its own.
     */
    suspend fun loadRange(session: SessionKey, fromSeq: Int, throughSeq: Int): List<Item> = emptyList()

    /** Implementations fetch authenticated bytes; presentation owns decoding and caching. */
    suspend fun loadImage(session: SessionKey, itemId: String): ByteArray =
        throw UnsupportedOperationException("Image unavailable")

    suspend fun fullText(session: SessionKey, itemId: String): String =
        throw UnsupportedOperationException("Full text unavailable")

    /** Called only after foreground, visibility and verified-window gates pass. */
    suspend fun markSeen(session: SessionKey, itemId: String) = Unit
}

/** A repository with a separate structural projection and per-item live text holders. */
interface StructuralChatRepository {
    fun observeStructure(session: SessionKey): Flow<ChatSnapshot>
}

/**
 * Chat presentation's structural view. Repositories without the optional capability, including delegating wrappers
 * that override only [ChatRepository.observe], retain their complete-text behavior.
 */
fun ChatRepository.observeStructure(session: SessionKey): Flow<ChatSnapshot> =
    (this as? StructuralChatRepository)?.observeStructure(session) ?: observe(session)
