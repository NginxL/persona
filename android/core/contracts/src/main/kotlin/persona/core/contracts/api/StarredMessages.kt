package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import persona.core.contracts.Item
import persona.core.contracts.SessionKey

const val STARRED_MESSAGES_FEATURE = "starred_messages"

@Serializable
enum class StarredConversation {
    @SerialName("assistant") Assistant,
    @SerialName("agent") Agent,
}

/**
 * The compact index and the snapshot use the same identity (matching iOS `StarredMessages.swift`).
 * Positional IDs (`line<n>#<k>`) alone can be reused by a rewritten transcript, so they require a 32-hex fingerprint.
 */
data class StarredSource(
    val machineId: String,
    val sessionId: String,
    val itemId: String,
    val conversation: StarredConversation,
    val sourceFingerprint: String? = null,
) {
    val isPositional: Boolean
        get() {
            if (!itemId.startsWith("line")) return false
            val rest = itemId.removePrefix("line")
            val hash = rest.indexOf('#')
            if (hash <= 0 || hash == rest.lastIndex) return false
            val linePart = rest.substring(0, hash)
            val indexPart = rest.substring(hash + 1)
            return linePart.all { it in '0'..'9' } && indexPart.all { it in '0'..'9' }
        }

    val isVerified: Boolean
        get() {
            if (!isPositional) return true
            val proof = sourceFingerprint ?: return false
            return proof.length == 32 && proof.all { it in '0'..'9' || it in 'a'..'f' }
        }

    val sessionKey: SessionKey
        get() = SessionKey(machineId, sessionId)

    fun matches(item: Item): Boolean =
        item.id == itemId && isVerified && (!isPositional || item.sourceFingerprint == sourceFingerprint)
}

@Serializable
data class StarredReference(
    val id: String,
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    @SerialName("item_id") val itemId: String,
    val conversation: StarredConversation,
    @SerialName("source_fingerprint") val sourceFingerprint: String? = null,
) {
    val source: StarredSource
        get() = StarredSource(machineId, sessionId, itemId, conversation, sourceFingerprint)
}

@Serializable
data class StarredSnapshot(
    val id: String? = null,
    @SerialName("machine_id") val machineId: String,
    @SerialName("session_id") val sessionId: String,
    @SerialName("item_id") val itemId: String,
    val conversation: StarredConversation,
    @SerialName("source_fingerprint") val sourceFingerprint: String? = null,
    val role: String,
    val title: String,
    val speaker: String,
    @SerialName("source_manager") val sourceManager: String? = null,
    @SerialName("written_at") val writtenAt: String? = null,
    @SerialName("starred_at") val starredAt: String? = null,
    val text: String,
    @SerialName("omitted_chars") val omittedChars: Int? = null,
) {
    val source: StarredSource
        get() = StarredSource(machineId, sessionId, itemId, conversation, sourceFingerprint)

    /**
     * The server's limit counts Unicode scalars (code points), matching iOS `StarredSnapshot.bounded()`.
     */
    fun bounded(limit: Int = MAX_STARRED_CODE_POINTS): StarredSnapshot {
        val trimmed = text.trim()
        val totalCodePoints = trimmed.codePointCount(0, trimmed.length)
        val kept = if (totalCodePoints <= limit) {
            trimmed
        } else {
            val endIndex = trimmed.offsetByCodePoints(0, limit)
            trimmed.substring(0, endIndex)
        }
        val extraOmitted = (totalCodePoints - limit).coerceAtLeast(0)
        val totalOmitted = (omittedChars ?: 0) + extraOmitted
        return copy(
            id = null,
            starredAt = null,
            text = kept,
            omittedChars = totalOmitted,
        )
    }

    companion object {
        const val MAX_STARRED_CODE_POINTS = 4_000
    }
}

@Serializable
data class StarredIndex(
    val refs: List<StarredReference> = emptyList(),
    @SerialName("can_write") val canWrite: Boolean = false,
)

@Serializable
data class StarredList(
    val stars: List<StarredSnapshot> = emptyList(),
    @SerialName("can_write") val canWrite: Boolean = false,
)

/** Starred-message snapshot API for one assistant on a paired hub. */
interface StarredMessagesApi {
    /** Null means the hub answered 404 (older hub without starred_messages). */
    suspend fun index(): StarredIndex?

    /** Null means the hub answered 404 (older hub without starred_messages). */
    suspend fun list(): StarredList?

    suspend fun add(star: StarredSnapshot): StarredSnapshot

    suspend fun remove(id: String): StarredSnapshot?

    /** Reads the original transcript item to verify it still matches before jumping from Starred. */
    suspend fun fullItem(session: SessionKey, itemId: String): Item
}
