package persona.feature.inbox

import persona.core.chatlist.*
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.serialization.json.*
import persona.core.inbox.*

/** The pairing owns the writer; the storage serializes writes with pairing erasure. */
class LarkReadingStore private constructor(
    private val storage: InboxStateStorage,
    private val scope: String,
    private val owner: CoroutineScope,
    document: JsonObject,
) : ChatAnchorStore {
    private val records = document.toMutableMap()
    private val positions = mutableMapOf<String, Long>()
    private val writes = Channel<JsonObject>(Channel.CONFLATED)

    init {
        owner.launch {
            try {
                for (snapshot in writes) {
                    ensureActive()
                    try { storage.write(snapshot.toString()) }
                    catch (e: CancellationException) { throw e }
                    catch (_: Exception) { /* A failed anchor write must not interrupt reading. */ }
                }
            } finally { writes.close() }
        }
    }

    @Synchronized fun observe(messages: List<LarkMessage>) {
        messages.forEach { message -> message.position?.let { positions[message.listKey()] = it } }
    }
    @Synchronized fun around(key: ChatReadingKey): Long? = record(key)?.number("position")
    private fun record(key: ChatReadingKey) = if (key.scope == scope) records[key.conversation] as? JsonObject else null
    @Synchronized override fun read(key: ChatReadingKey): ChatReadingPosition? = record(key)?.let {
        ChatReadingPosition(it.cursor("row"), it.number("offset")?.toInt() ?: 0, it.flag("follows"),
            (it["expanded"] as? JsonArray).orEmpty().mapNotNull { value -> (value as? JsonPrimitive)?.contentOrNull }.toSet())
    }
    @Synchronized override fun write(key: ChatReadingKey, position: ChatReadingPosition) {
        if (key.scope != scope || owner.coroutineContext[Job]?.isActive != true) return
        val around = positions[position.rowId] ?: record(key)?.number("position")
        records[key.conversation] = buildJsonObject {
            position.rowId?.let { put("row", it) }
            around?.let { put("position", it) }
            put("offset", position.offsetPx); put("follows", position.followsEnd)
            putJsonArray("expanded") { position.expanded.forEach { add(it) } }
        }
        writes.trySend(JsonObject(records.toMap()))
    }
    companion object {
        suspend fun open(storage: InboxStateStorage, scope: String, owner: CoroutineScope): LarkReadingStore {
            owner.coroutineContext.ensureActive()
            val document = try { storage.read()?.let { Json.parseToJsonElement(it).jsonObject } }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { null }
            owner.coroutineContext.ensureActive()
            return LarkReadingStore(storage, scope, owner, document ?: JsonObject(emptyMap()))
        }
    }
}
