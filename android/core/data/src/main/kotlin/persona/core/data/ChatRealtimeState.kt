package persona.core.data

import persona.core.contracts.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/** Ephemeral rows never enter transcript coverage or the disk cache. Callers hold the entry lock. */
internal class ChatRealtimeState(private val key: SessionKey) {
    private data class Draft(
        var item: Item,
        val after: Item?,
        var finalId: String? = null,
        val liveText: MutableStateFlow<String> = MutableStateFlow(item.text.orEmpty()),
    )
    private class Projection(val structural: List<Item>, val live: List<Item>)
    private val drafts = mutableListOf<Draft>()
    private var active: Draft? = null
    private var nextId = 0L
    private val aliases = mutableMapOf<String, String>()
    private val tools = LinkedHashMap<String, ToolCallEvent>()
    private var screen: Item? = null
    private var screenAfter: String? = null
    private var projectedTranscript: List<Item>? = null
    private var projectedItems: Projection? = null
    private var exposedDraft: Draft? = null
    private var exposedStreamingIds: Set<String> = emptySet()
    private var exposedLiveText: Map<String, StateFlow<String>> = emptyMap()

    private inner class LiveItemList(
        private val base: List<Item>,
        private val transcript: List<Item>,
    ) : AbstractList<Item>() {
        override val size: Int get() = base.size
        override fun get(index: Int): Item {
            val item = base[index]
            val draft = active ?: return item
            if (item.id != draft.item.id) return item
            val text = draft.shownText(transcript)
            return if (item.text.orEmpty() == text) item else item.copy(text = text)
        }
    }

    private fun invalidate() { projectedTranscript = null; projectedItems = null }

    fun delta(event: DeltaEvent, items: List<Item>) {
        if (event.turnFinished) {
            finish()
            return
        }
        val draft = active ?: Draft(Item("realtime-delta:${key.path}:${++nextId}#0",
            nextSeq(items), role = "assistant", kind = "text"), items.lastOrNull()).also {
            drafts += it
            active = it
            // Only a new row is structural. Later deltas travel through that row's stable live-list view.
            invalidate()
        }
        draft.item = draft.item.copy(text = event.text)
    }

    fun finish() { active = null; screen = null; invalidate() }

    fun screen(event: ScreenEvent, items: List<Item>) {
        invalidate()
        screenAfter = items.lastOrNull()?.id
        screen = event.text.takeIf { it.isNotEmpty() }?.let {
            Item("realtime-screen:${key.path}#0", screen?.seq ?: nextSeq(items),
                ts = event.ts.takeIf { parseTime(it) != null }, role = "system", kind = "notice", text = it)
        }
    }

    /**
     * Records a call's name and arguments; true when [transcript] already has its row, so the session must be redrawn.
     * Calls have no session identity and may precede their transcript row: then the row picks them up when it arrives.
     */
    fun tool(event: ToolCallEvent, transcript: List<Item>): Boolean {
        tools[event.callId] = event
        if (tools.size > 128) tools.remove(tools.keys.first())
        // Without its row nothing shown changes; the row arrives in a new transcript list, which misses the cache.
        return transcript.any { it.kind == "tool_use" && it.toolUseId == event.callId }.also { if (it) invalidate() }
    }

    fun reset() {
        drafts.clear(); active = null; aliases.clear(); tools.clear(); screen = null; screenAfter = null
        invalidate()
    }

    fun canonicalId(id: String): String = aliases.entries.firstOrNull { it.value == id }?.key ?: id
    fun displayId(id: String): String = aliases[id] ?: id

    private fun syncExposure() {
        val draft = active
        if (exposedDraft === draft) return
        exposedDraft = draft
        exposedStreamingIds = setOfNotNull(draft?.item?.id)
        exposedLiveText = draft?.let { mapOf(it.item.id to it.liveText) } ?: emptyMap()
    }

    fun streamingItemIds(): Set<String> {
        syncExposure()
        return exposedStreamingIds
    }

    fun liveText(): Map<String, StateFlow<String>> {
        syncExposure()
        return exposedLiveText
    }

    /** The latest arrival for diagnostics; unlike the UI holder, this does not wait for a publish window. */
    fun currentText(transcript: List<Item>): String? = active?.shownText(transcript)

    /** Advancing holders is deliberately a publish operation, not part of delta arrival. */
    fun publishLiveText(transcript: List<Item>) {
        active?.let { draft -> draft.liveText.value = draft.shownText(transcript) }
    }

    fun items(transcript: List<Item>): List<Item> = projection(transcript).live

    /** Immutable structural rows for a published snapshot; ordinary text stays in the holders/view instead. */
    fun structuralItems(transcript: List<Item>): List<Item> = projection(transcript).structural

    private fun projection(transcript: List<Item>): Projection {
        if (projectedTranscript === transcript) projectedItems?.let { return it }
        val claimed = aliases.keys.toMutableSet()
        val pending = mutableListOf<Item>()
        val overlays = mutableMapOf<String, String>()
        val retained = mutableListOf<Draft>()
        for (draft in drafts) {
            val nextUser = transcript.firstOrNull { item ->
                item.role == "user" && item.queued != true &&
                    (draft.after == null || order.compare(item, draft.after) > 0)
            }
            val final = draft.finalId?.let { id -> transcript.firstOrNull { it.id == id } } ?: transcript.firstOrNull { item ->
                // A reply the hub cut is matched by its start.
                val text = MessageEcho.uncut(item)
                item.id !in claimed && item.role == "assistant" && item.kind == "text" &&
                    (draft.after == null || order.compare(item, draft.after) > 0) &&
                    (nextUser == null || order.compare(item, nextUser) < 0) &&
                    text.isNotEmpty() && (text == draft.item.text ||
                    text.startsWith(draft.item.text.orEmpty()) || draft.item.text.orEmpty().startsWith(text))
            }
            if (final != null) {
                draft.finalId = final.id
                aliases[final.id] = draft.item.id
                claimed += final.id
                // A shorter transcript must not erase the draft while the final record catches up.
                if (draft.item.text.orEmpty().startsWith(final.text.orEmpty()) &&
                    draft.item.text.orEmpty().length > final.text.orEmpty().length) {
                    overlays[final.id] = draft.item.text.orEmpty()
                    retained += draft
                } else if (draft === active) retained += draft
            } else if (nextUser == null) {
                pending += draft.item.copy(seq = nextSeq(transcript))
                retained += draft
            }
            if (nextUser != null && draft === active) active = null
        }
        drafts.clear(); drafts += retained
        if (screenAfter != transcript.lastOrNull()?.id) screen = null
        val structural = (transcript.map { item ->
            val tool = item.toolUseId?.let(tools::get).takeIf { item.kind == "tool_use" }
            val mapped = if (tool == null) item else item.copy(toolName = tool.name, toolInput = tool.args)
            overlays[item.id]?.let { mapped.copy(text = it) } ?: mapped
        } + pending + listOfNotNull(screen?.copy(seq = nextSeq(transcript))))
            .sortedWith(order).map { item ->
                val id = displayId(item.id)
                if (id == item.id) item else item.copy(id = id)
            }
        val result = Projection(structural, LiveItemList(structural, transcript))
        projectedTranscript = transcript
        projectedItems = result
        return result
    }

    private fun Draft.shownText(transcript: List<Item>): String {
        val current = item.text.orEmpty()
        val recorded = finalId?.let { id -> transcript.firstOrNull { it.id == id } }?.text?.orEmpty() ?: return current
        return if (current.startsWith(recorded) && current.length > recorded.length) current else recorded
    }

    private fun nextSeq(items: List<Item>): Int =
        (items.maxOfOrNull { maxOf(it.seq, it.fold?.throughSeq ?: it.seq) } ?: 0).let {
            if (it == Int.MAX_VALUE) it else it + 1
        }

    companion object {
        private val order = compareBy<Item> { it.seq }
            .thenBy { it.id.substringAfterLast('#').toIntOrNull() ?: 0 }
    }
}
