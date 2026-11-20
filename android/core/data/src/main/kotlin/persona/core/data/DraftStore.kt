package persona.core.data

import androidx.room.withTransaction
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.DraftAttachment
import persona.core.contracts.api.DraftSessionVersion
import persona.core.contracts.api.DraftKey
import persona.core.contracts.api.DraftRepository
import persona.core.contracts.api.DraftSnapshot
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.map
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonObject

class DraftStore(private val database: PersistentDatabase, private val scope: HubScope) : DraftRepository {
    override val structured = true
    override fun observe(key: DraftKey): Flow<DraftSnapshot> {
        val storage = key.storageKey()
        return database.records().observeDraft(scope.path, storage.machineId, storage.sessionId)
            .map { it.snapshot(key) }
    }

    override suspend fun read(key: DraftKey): DraftSnapshot {
        val storage = key.storageKey()
        val row = database.records().draft(scope.path, storage.machineId, storage.sessionId)
        return row.snapshot(key)
    }

    override suspend fun save(key: DraftKey, text: String, expectedVersion: Long): DraftSnapshot =
        save(key, text, expectedVersion, emptyList(), null)

    override suspend fun save(
        key: DraftKey, text: String, expectedVersion: Long,
        attachments: List<DraftAttachment>, sessionVersion: DraftSessionVersion?,
    ): DraftSnapshot = save(key, text, expectedVersion, attachments, sessionVersion, false)

    override suspend fun save(
        key: DraftKey, text: String, expectedVersion: Long,
        attachments: List<DraftAttachment>, sessionVersion: DraftSessionVersion?, hasPickedSavedPrompt: Boolean,
    ): DraftSnapshot = database.withTransaction {
        val storage = key.storageKey()
        val old = read(key)
        check(old.version == expectedVersion) { "Draft version changed" }
        val next = old.copy(text = text, version = Math.addExact(expectedVersion, 1),
            attachments = attachments, sessionVersion = sessionVersion, hasPickedSavedPrompt = hasPickedSavedPrompt)
        database.records().putDraft(DraftRow(scope.path, storage.machineId, storage.sessionId, text, next.version,
            JsonCodec.encodeToString(attachments), next.storedMetadata()))
        next
    }

    private fun DraftRow?.snapshot(key: DraftKey): DraftSnapshot {
        val metadata = this?.session_version?.let { JsonCodec.parseToJsonElement(it).jsonObject }
        val picked = metadata?.get(SAVED_PROMPT) == JsonPrimitive(true)
        return DraftSnapshot(scope, key, this?.text.orEmpty(), this?.version ?: 0,
            this?.let { JsonCodec.decodeFromString<List<DraftAttachment>>(it.attachments) }.orEmpty(),
            metadata?.takeUnless { picked && it.size == 1 }?.let { JsonCodec.decodeFromJsonElement<DraftSessionVersion>(it) },
            hasPickedSavedPrompt = picked)
    }

    // Preserve the original chat-only API and its on-disk keys.
    fun observe(session: SessionKey): Flow<DraftRow?> =
        database.records().observeDraft(scope.path, session.machineId, session.sessionId)

    suspend fun save(session: SessionKey, text: String, expectedVersion: Long): DraftRow {
        val draft = save(DraftKey.Chat(session), text, expectedVersion)
        return DraftRow(scope.path, session.machineId, session.sessionId, draft.text, draft.version)
    }
}

private const val SAVED_PROMPT = "hasPickedSavedPrompt"
private val completeDraftVersionJson = Json(JsonCodec) { encodeDefaults = true }

private fun DraftSnapshot.storedMetadata(): String? {
    if (!hasPickedSavedPrompt) return sessionVersion?.let { JsonCodec.encodeToString(it) }
    // Keep the session version's existing JSON keys. With provenance, encode defaults so an empty version stays distinct from null.
    val version = sessionVersion?.let { completeDraftVersionJson.encodeToJsonElement(it).jsonObject }
    return JsonObject(version.orEmpty() + (SAVED_PROMPT to JsonPrimitive(true))).toString()
}

/** An empty machine is reserved for local composers; length prefixes prevent key collisions. */
internal fun DraftKey.storageKey(): SessionKey = when (this) {
    is DraftKey.Chat -> session.also { require(it.machineId.isNotBlank() && it.sessionId.isNotBlank()) }
    is DraftKey.Assistant -> SessionKey("", "assistant:$conversation")
    is DraftKey.Share -> SessionKey("", "share:$id")
    is DraftKey.LarkReply -> SessionKey("", "lark:${feedId.length}:$feedId$threadId")
    is DraftKey.InboxReply -> SessionKey("", "inbox:$itemId")
}
