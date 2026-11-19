package persona.android.data

import persona.android.AppText
import persona.android.R
import android.app.Application
import android.util.Base64
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringPreferencesKey
import persona.android.di.ConnectionRuntime
import persona.core.contracts.GroupKeys
import persona.core.chatlist.*
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.data.ScopedHubData
import persona.core.hub.HubChatTransport
import persona.feature.chat.ChatImageLoaders
import persona.feature.chat.ChatLocalStorage
import persona.feature.chat.ChatStorageSnapshot
import persona.feature.chat.scopedChatImageLoader
import kotlinx.coroutines.*
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.*
import kotlinx.serialization.json.*
import java.io.File
import java.util.concurrent.ConcurrentHashMap

@OptIn(ExperimentalCoroutinesApi::class)
class ScopedChatStorage(application: Application, private val runtime: ConnectionRuntime, scope: CoroutineScope) : ChatLocalStorage {
    private val context = application.applicationContext
    private val imageLoaders = ChatImageLoaders()

    init {
        scope.launch {
            runtime.groups.collect { groups ->
                imageLoaders.retain(groups.mapNotNull { group ->
                    group.data?.let { data -> data.scope.path to data }
                }.toMap())
            }
        }
    }

    /** Reading positions live with the first group (its data outlives a change of the selected group). */
    override val localStorage = runtime.groups.map { groups -> groups.firstOrNull()?.data }.distinctUntilChanged().mapLatest { data ->
        data?.let { ChatStorageSnapshot(it.scope.path, ScopedChatAnchors.open(it)) }
    }.stateIn(scope, SharingStarted.Eagerly, null)

    override fun heldMessages(session: SessionKey): persona.core.contracts.api.HeldAgentActions? {
        val group = runtime.groupOf(session.machineId)?.takeIf { it.active } ?: return null
        return group.heldActions?.invoke(GroupKeys.rawKey(session))
    }

    override fun replyImages(session: SessionKey): Flow<persona.core.contracts.api.ReplyImageSource?> =
        runtime.groups.map { runtime.groupOf(session.machineId)?.let { group -> group.data?.let { group to it } } }
            .distinctUntilChanged().map { owner ->
                owner?.let { (group, data) ->
                    persona.core.hub.HubReplyImages(runtime.connectionFor(data)) { group.active && group.data === data }
                }
            }

    override fun imageLoader(session: SessionKey): coil3.ImageLoader? {
        val data = runtime.groupOf(session.machineId)?.data ?: return null
        return imageLoaders.get(data.scope.path, data) { scopedChatImageLoader(context, this, data.scope.path) }
    }

    /** Images are cached with the group the session belongs to. */
    override suspend fun imageFile(session: SessionKey, item: Item, width: Int, height: Int): File {
        val group = runtime.groupOf(session.machineId)
        val data = checkNotNull(group?.data) { AppText.get(R.string.app_error_pair_first) }
        val connection = runtime.connectionFor(data)
        val plain = GroupKeys.rawKey(session)
        return data.coroutineScope.async(Dispatchers.IO) {
            check(group.data === data && group.active) { AppText.get(R.string.app_error_switched) }
            data.cachedImage(plain, item.id, width, height) {
                item.image?.data?.takeIf(String::isNotBlank)?.let { Base64.decode(it, Base64.DEFAULT) }
                    ?: HubChatTransport(connection).image(plain, item.id)
            }
        }.await()
    }
}

/** Read from memory during first layout; serialize writes in the pairing's lifetime. */
private class ScopedChatAnchors(private val data: ScopedHubData, initial: Map<String, ChatReadingPosition>) : ChatAnchorStore {
    private val positions = ConcurrentHashMap(initial)
    private val writes = Channel<Pair<String, ChatReadingPosition>>(Channel.UNLIMITED)
    init {
        data.coroutineScope.launch {
            for ((conversation, position) in writes) {
                data.readingPositions.edit { it[stringPreferencesKey(conversation)] = encode(position) }
            }
        }
    }
    override fun read(key: ChatReadingKey) = positions[key.conversation].takeIf { key.scope == data.scope.path }
    override fun write(key: ChatReadingKey, position: ChatReadingPosition) {
        if (key.scope != data.scope.path || data.coroutineScope.coroutineContext[Job]?.isActive != true) return
        positions[key.conversation] = position
        writes.trySend(key.conversation to position)
    }
    companion object {
        suspend fun open(data: ScopedHubData): ScopedChatAnchors {
            val initial = data.readingPositions.data.first().asMap().mapNotNull { (key, value) ->
                (value as? String)?.let { raw ->
                    try { key.name to decode(raw) } catch (_: IllegalArgumentException) { null }
                }
            }.toMap()
            return ScopedChatAnchors(data, initial)
        }
        private fun encode(position: ChatReadingPosition) = buildJsonObject {
            position.rowId?.let { put("row", it) }
            put("offset", position.offsetPx); put("end", position.followsEnd)
            put("expanded", JsonArray(position.expanded.map(::JsonPrimitive)))
        }.toString()
        private fun decode(raw: String): ChatReadingPosition {
            val value = JsonCodec.parseToJsonElement(raw).jsonObject
            return ChatReadingPosition(value["row"]?.jsonPrimitive?.contentOrNull,
                value["offset"]?.jsonPrimitive?.intOrNull ?: 0,
                value["end"]?.jsonPrimitive?.booleanOrNull ?: true,
                (value["expanded"] as? JsonArray).orEmpty().map { it.jsonPrimitive.content }.toSet())
        }
    }
}
