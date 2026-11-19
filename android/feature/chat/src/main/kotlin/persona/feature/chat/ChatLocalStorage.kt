package persona.feature.chat

import coil3.ImageLoader
import persona.core.chatlist.ChatAnchorStore
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.StateFlow
import java.io.File

/** Optional local capabilities supplied by the app's pairing-scoped repository facade. */
interface ChatLocalStorage {
    fun replyImages(session: SessionKey): kotlinx.coroutines.flow.Flow<persona.core.contracts.api.ReplyImageSource?> = kotlinx.coroutines.flow.flowOf(null)
    val localStorage: StateFlow<ChatStorageSnapshot?>
    fun heldMessages(session: SessionKey): persona.core.contracts.api.HeldAgentActions? = null
    suspend fun imageFile(session: SessionKey, item: Item, width: Int, height: Int): File
    /** A pairing-owned decoder; the page must not shut it down. Null outside paired hub storage. */
    fun imageLoader(session: SessionKey): ImageLoader? = null
}

data class ChatStorageSnapshot(val scope: String, val anchors: ChatAnchorStore)

/** One decoded-image cache per pairing data generation; pages only borrow these loaders. */
class ChatImageLoaders {
    private data class Entry(val generation: Any, val loader: ImageLoader)
    private val loaders = mutableMapOf<String, Entry>()

    @Synchronized
    fun get(scope: String, generation: Any, create: () -> ImageLoader): ImageLoader {
        loaders[scope]?.let { current ->
            if (current.generation === generation) return current.loader
            current.loader.shutdown()
        }
        return create().also { loaders[scope] = Entry(generation, it) }
    }

    @Synchronized
    fun retain(generations: Map<String, Any>) {
        val gone = loaders.filter { (scope, entry) -> generations[scope] !== entry.generation }
        gone.forEach { (scope, entry) ->
            loaders.remove(scope)
            entry.loader.shutdown()
        }
    }
}
