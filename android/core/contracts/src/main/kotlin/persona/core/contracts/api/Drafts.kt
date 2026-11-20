package persona.core.contracts.api

import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.Flow
import kotlinx.serialization.Serializable

/** Keys are local to one hub and pairing generation. Keep share IDs stable across recreation. */
sealed interface DraftKey {
    data class Chat(val session: SessionKey) : DraftKey
    data class Assistant(val conversation: String = "main") : DraftKey
    data class Share(val id: String) : DraftKey
    data class LarkReply(val feedId: String, val threadId: String = "") : DraftKey
    data class InboxReply(val itemId: String) : DraftKey
}

/** [Video]: a video from the photo picker, sent as a `<persona-file kind="video">` tag (docs/protocol/protocol.md "Sending videos"). */
@Serializable
enum class DraftAttachmentType { Image, File, Video }

@Serializable
enum class DraftUploadState { Pending, Uploading, Uploaded, Failed, Invalid }

@Serializable
data class DraftAttachment(
    val id: String,
    val type: DraftAttachmentType,
    val uri: String,
    val name: String,
    val size: Long? = null,
    val uploadState: DraftUploadState = DraftUploadState.Pending,
    val uploadedPath: String? = null,
    val error: String? = null,
    /** A video's length in seconds, when the phone could read it. */
    val duration: Double? = null,
)

/** History changes on transcript replacement; list rev changes on ordinary messages too. */
@Serializable
data class DraftSessionVersion(val history: String? = null, val instance: String? = null, val replaced: Boolean = false) {
    fun differsFrom(current: DraftSessionVersion): Boolean =
        (history != null && current.history != null && history != current.history) ||
            (instance != null && current.instance != null && instance != current.instance)
}

data class DraftSnapshot(
    val scope: HubScope, val key: DraftKey, val text: String = "", val version: Long = 0,
    val attachments: List<DraftAttachment> = emptyList(),
    val sessionVersion: DraftSessionVersion? = null,
    val hasPickedSavedPrompt: Boolean = false,
)

interface DraftRepository {
    /** Whether attachments and session versions are stored as structured fields rather than in the text. */
    val structured: Boolean get() = false
    fun observe(key: DraftKey): Flow<DraftSnapshot>
    suspend fun read(key: DraftKey): DraftSnapshot
    /** Compare-and-set, including clears. Returns only after the write commits. */
    suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot
    suspend fun save(
        key: DraftKey, text: String, expectedVersion: Long,
        attachments: List<DraftAttachment>, sessionVersion: DraftSessionVersion?,
    ): DraftSnapshot {
        check(attachments.isEmpty() && sessionVersion == null) { "Structured drafts are not supported" }
        return save(key, text, expectedVersion)
    }
    /** Structured stores can also keep the provenance of an unchanged saved-prompt body. */
    suspend fun save(
        key: DraftKey, text: String, expectedVersion: Long,
        attachments: List<DraftAttachment>, sessionVersion: DraftSessionVersion?, hasPickedSavedPrompt: Boolean,
    ): DraftSnapshot = save(key, text, expectedVersion, attachments, sessionVersion)
}

/** Callers clear only the submitted editor revision, and only after this call succeeds. */
interface DraftMessageSender {
    suspend fun sendDraft(session: SessionKey, draft: DraftSnapshot, clientMessageId: String)
}
