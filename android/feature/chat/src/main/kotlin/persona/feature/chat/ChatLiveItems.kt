package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.collect
import kotlinx.coroutines.launch
import persona.core.contracts.Item

/**
 * Complete-text consumers' view of a structural chat snapshot. A holder update changes one value, not the base list;
 * [Revision.items] applies those values only when an item is read. The page itself must not collect [revisions] as state.
 */
internal class ChatLiveItems(
    base: List<Item> = emptyList(),
    streamingItemIds: Set<String> = emptySet(),
) {
    internal class Revision internal constructor(
        val base: List<Item>,
        val values: Map<String, String>,
        val changedItemId: String?,
        val structural: Boolean,
        val structureGeneration: Long,
        val lastItemId: String?,
    ) {
        /** A lazy, immutable overlay: later revisions cannot change what an in-flight consumer reads. */
        val items: List<Item> by lazy {
            if (values.isEmpty()) base else LiveItemList(base, values)
        }

        /** True only when the revision proves that the last item's text, and nothing else's structure, changed. */
        val lastTextOnly: Boolean = !structural && lastItemId != null && lastItemId == changedItemId

        /** Materialize the current overlay for a consumer that must own a frozen, complete-text list. */
        fun current(): List<Item> = if (values.isEmpty()) base else items.toList()
    }

    private class LiveItemList(
        private val base: List<Item>,
        private val values: Map<String, String>,
    ) : AbstractList<Item>() {
        override val size: Int get() = base.size
        override fun get(index: Int): Item {
            val item = base[index]
            val text = values[item.id] ?: return item
            return if (item.text.orEmpty() == text) item else item.copy(text = text)
        }
    }

    private var byId = base.associateBy { it.id }
    private var streamingItemIds = streamingItemIds
    private var textUpdates = emptyMap<String, ChatTextUpdate>()
    private var holders = emptyMap<String, StateFlow<String>>()
    private val mutableRevisions = MutableStateFlow(Revision(base, emptyMap(), changedItemId = null,
        structural = true, structureGeneration = 0L, lastItemId = base.lastOrNull()?.id))

    val revisions: StateFlow<Revision> = mutableRevisions
    val generation: Long get() = mutableRevisions.value.structureGeneration
    val structuralItems: List<Item> get() = mutableRevisions.value.base

    /** Read one row at its latest text without rebuilding or indexing the projection. */
    fun item(id: String): Item? {
        val item = byId[id] ?: return null
        val text = mutableRevisions.value.values[id] ?: return item
        return if (item.text.orEmpty() == text) item else item.copy(text = text)
    }

    /** A text-only revision redraws its row and leaves both the structural generation and base identity untouched. */
    fun updateText(id: String, update: ChatTextUpdate) {
        val base = byId[id] ?: return
        val previous = mutableRevisions.value
        val oldText = previous.values[id] ?: base.text.orEmpty()
        if (textUpdates[id] != update) textUpdates = textUpdates + (id to update)
        if (oldText == update.text) return
        mutableRevisions.value = Revision(previous.base, previous.values + (id to update.text), changedItemId = id,
            structural = false, structureGeneration = previous.structureGeneration, lastItemId = previous.lastItemId)
    }

    /** User actions own a complete, frozen list; explicit host updates win without mutating the streaming holders. */
    fun currentItems(explicit: Map<String, ChatTextUpdate> = emptyMap()): List<Item> {
        val revision = mutableRevisions.value
        if (explicit.isEmpty()) return revision.current()
        val values = revision.values + explicit.mapValues { it.value.text }
        if (values.isEmpty()) return revision.base
        return LiveItemList(revision.base, values).toList()
    }

    /** Explicit updates also win for row-oriented consumers, while the synthetic stream state remains independent. */
    fun rowTextUpdates(explicit: Map<String, ChatTextUpdate> = emptyMap()): Map<String, ChatTextUpdate> =
        if (explicit.isEmpty()) textUpdates else textUpdates + explicit

    /** A structural snapshot is the only operation that indexes the conversation or changes its generation. */
    fun reset(items: List<Item>, streamingItemIds: Set<String>) {
        reset(items, streamingItemIds, emptyMap())
    }

    internal fun reset(items: List<Item>, streamingItemIds: Set<String>, liveText: Map<String, StateFlow<String>>) {
        if (structuralItems === items && this.streamingItemIds == streamingItemIds && holders == liveText) return
        byId = items.associateBy { it.id }
        this.streamingItemIds = streamingItemIds
        holders = liveText
        textUpdates = emptyMap()
        mutableRevisions.value = Revision(items, emptyMap(), changedItemId = null,
            structural = true, structureGeneration = generation + 1, lastItemId = items.lastOrNull()?.id)
    }

    /** Ignore a cancelled collector from an older holder for the same row. */
    internal fun update(id: String, holder: StateFlow<String>, text: String) {
        if (holders[id] !== holder) return
        updateText(id, ChatTextUpdate(text, streaming = true))
    }
}

@Composable
internal fun rememberChatLiveItems(
    items: List<Item>,
    liveText: Map<String, StateFlow<String>>,
    streamingItemIds: Set<String>,
): ChatLiveItems {
    val live = remember { ChatLiveItems() }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(live, lifecycle, items, liveText, streamingItemIds) {
        live.reset(items, streamingItemIds, liveText)
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            coroutineScope {
                liveText.forEach { (id, holder) ->
                    launch { holder.collect { live.update(id, holder, it) } }
                }
                awaitCancellation()
            }
        }
    }
    return live
}
