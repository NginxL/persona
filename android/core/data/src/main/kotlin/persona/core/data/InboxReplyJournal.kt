package persona.core.data

import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.inbox.ReplyTarget
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.decodeFromJsonElement
import java.io.File
import java.io.FileOutputStream
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import java.util.concurrent.ConcurrentHashMap

/** Private, non-backed-up storage. Commit the request before any network side effect. */
class InboxReplyJournal(private val directory: File, private val pairingScope: String) {
    init {
        val parts = pairingScope.split('/')
        require(parts.size == 2) { "Reply journal requires a hub and pairing generation" }
        HubScope(parts[0], parts[1])
        locations[pairingScope] = directory
    }
    /* Several short-lived owners may commit different scopes' journals; instances of one file share its lock. */
    private val mutex = locks.computeIfAbsent(directory.absolutePath) { Mutex() }
    private var closed = false
    private fun file(scope: String): File {
        check(!closed) { "Reply journal is closed" }
        require(scope == pairingScope) { "Reply journal belongs to another pairing" }
        return File(directory, "replies.json")
    }
    suspend fun close() = mutex.withLock { closed = true }
    /** Pairing cleanup shares the same lock as reads and atomic writes. */
    suspend fun clear(scope: String) = withContext(Dispatchers.IO) { mutex.withLock {
        val destination = file(scope)
        check(!destination.exists() || destination.delete()) { "Cannot clear reply journal" }
    } }
    internal suspend fun read(scope: String): InboxReplyDocument = withContext(Dispatchers.IO) {
        mutex.withLock { readLocked(scope) }
    }
    internal suspend fun update(scope: String, change: (InboxReplyDocument) -> InboxReplyDocument): InboxReplyDocument =
        withContext(Dispatchers.IO) { mutex.withLock {
            val updated = change(readLocked(scope))
            check(directory.isDirectory || directory.mkdirs())
            val destination = file(scope)
            val temp = File.createTempFile("reply-", ".tmp", directory)
            try {
                FileOutputStream(temp).use { stream ->
                    stream.write(JsonCodec.encodeToString(updated).toByteArray())
                    stream.fd.sync()
                }
                Files.move(temp.toPath(), destination.toPath(), StandardCopyOption.ATOMIC_MOVE, StandardCopyOption.REPLACE_EXISTING)
            } finally { temp.delete() }
            updated
        } }
    private fun readLocked(scope: String): InboxReplyDocument = file(scope).let {
        if (it.exists()) decodeReplyDocument(it.readText()) else InboxReplyDocument()
    }

    /** Reconcile only target-journal delivery evidence, never an outbox row's Sent or Shown state. */
    internal suspend fun reconcileAgentHandOffDeliveries(scope: String) {
        val pending = read(scope).agentHandOffs.filter { !it.delivered && !it.recorded }
        for (handOff in pending) for (targetScope in handOff.deliveryScopes) {
            val target = journal(targetScope, this) ?: continue
            val carried = try {
                target.read(targetScope).agentHandOffs.firstOrNull {
                    it.scope == handOff.scope && it.request == handOff.request &&
                        it.hubItemId == handOff.hubItemId && it.manager == handOff.manager && it.delivered
                }
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                null
            }
            if (carried != null) {
                commitAgentHandOffDelivery(carried.copy(deliveryScopes = carried.deliveryScopes + targetScope),
                    targetScope, this)
                break
            }
        }
    }

    private fun siblingDirectory(scope: String): File? {
        // Production layout: <no-backup>/hub-data/<hub>/<generation>/inbox-replies. Tests register temporary layouts.
        val generation = directory.parentFile ?: return null
        val hub = generation.parentFile ?: return null
        val hubData = hub.parentFile?.takeIf { it.name == "hub-data" } ?: return null
        val root = hubData.parentFile ?: return null
        val parts = scope.split('/').takeIf { it.size == 2 } ?: return null
        return File(root, "hub-data/${parts[0]}/${parts[1]}/inbox-replies")
    }

    companion object {
        private val locations = ConcurrentHashMap<String, File>()
        private val locks = ConcurrentHashMap<String, Mutex>()

        private fun journal(scope: String, relativeTo: InboxReplyJournal?): InboxReplyJournal? {
            val directory = locations[scope] ?: relativeTo?.siblingDirectory(scope) ?: return null
            if (relativeTo?.pairingScope == scope && relativeTo.directory == directory) return relativeTo
            return InboxReplyJournal(directory, scope)
        }

        /** Link the origin before the target row can send; a later origin replay can then read target evidence. */
        internal suspend fun linkAgentHandOffTarget(
            handOff: InboxAgentHandOff,
            targetScope: String,
            relativeTo: InboxReplyJournal? = null,
        ): Boolean {
            val origin = journal(handOff.scope, relativeTo) ?: return false
            origin.update(handOff.scope) {
                it.linkAgentHandOffTarget(handOff.request.operationId, targetScope)
            }
            return true
        }

        /**
         * Commit the origin even when its repository is closed. Failure stays quiet for message delivery: a live origin
         * can finish the same idempotent write, and a closed one reconciles the target journal on its next replay.
         */
        internal suspend fun commitAgentHandOffDelivery(
            handOff: InboxAgentHandOff,
            targetScope: String,
            relativeTo: InboxReplyJournal? = null,
        ): Boolean {
            val origin = journal(handOff.scope, relativeTo) ?: return false
            return try {
                origin.update(handOff.scope) {
                    it.commitAgentHandOffDelivery(handOff.copy(deliveryScopes = handOff.deliveryScopes + targetScope))
                }
                true
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (_: Exception) {
                false
            }
        }
    }
}

internal fun ReplyTarget.storageKey(): String = when (this) {
    is ReplyTarget.Inbox -> "inbox:$itemId"
    is ReplyTarget.Lark -> "lark:$feedId"
}

/**
 * Decodes `replies.json`, also accepting `persona.core.inbox.ReplyTarget.*` discriminators written during the window
 * after #14075 moved `ReplyTarget` to `:core:inbox` before its original `@SerialName` was restored.
 */
internal fun decodeReplyDocument(raw: String): InboxReplyDocument {
    if ("persona.core.inbox.ReplyTarget." !in raw) return JsonCodec.decodeFromString(raw)
    val root = JsonCodec.parseToJsonElement(raw) as? JsonObject ?: return JsonCodec.decodeFromString(raw)
    val outbox = root["outbox"] as? JsonArray ?: return JsonCodec.decodeFromJsonElement(root)
    val migrated = JsonArray(outbox.map { entry ->
        val obj = entry as? JsonObject ?: return@map entry
        val target = obj["target"] as? JsonObject ?: return@map entry
        val mapped = when ((target["type"] as? JsonPrimitive)?.contentOrNull) {
            "persona.core.inbox.ReplyTarget.Inbox" -> "persona.feature.inbox.ReplyTarget.Inbox"
            "persona.core.inbox.ReplyTarget.Lark" -> "persona.feature.inbox.ReplyTarget.Lark"
            else -> null
        } ?: return@map entry
        JsonObject(obj + ("target" to JsonObject(target + ("type" to JsonPrimitive(mapped)))))
    })
    return JsonCodec.decodeFromJsonElement(JsonObject(root + ("outbox" to migrated)))
}
