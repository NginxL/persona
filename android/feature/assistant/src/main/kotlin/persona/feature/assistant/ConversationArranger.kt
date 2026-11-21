package persona.feature.assistant

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.withContext
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

internal data class ConversationDrag(val id: String, val order: List<String>)

internal data class ConversationArrangeUi(
    val arranging: Boolean = false,
    /** Latest optimistic payload, including saved ids this client has not loaded. */
    val order: List<String> = emptyList(),
    val drag: ConversationDrag? = null,
    val pending: Int = 0,
)

internal sealed interface ConversationArrangeEvent {
    data class Failed(val error: Exception) : ConversationArrangeEvent
    data class Reset(val revision: Long) : ConversationArrangeEvent
}

/**
 * Ordinary-chat ordering behind the drawer's progressive-disclosure Arrange mode. Local moves are visible before their
 * hub writes; writes run one at a time, and only the newest revision may change the visible order or roll it back.
 */
internal class ConversationArranger(
    private val prepare: suspend () -> suspend (List<String>?) -> Unit,
    private val write: suspend (List<String>) -> List<String>?,
) {
    private val mutable = MutableStateFlow(ConversationArrangeUi())
    val ui: StateFlow<ConversationArrangeUi> = mutable.asStateFlow()
    private val mutableEvents = MutableSharedFlow<ConversationArrangeEvent>(extraBufferCapacity = 8)
    val events: SharedFlow<ConversationArrangeEvent> = mutableEvents.asSharedFlow()
    private val writes = Mutex()
    private val snapshotWrites = Mutex()
    private var updateSnapshot: suspend (List<String>?) -> Unit = {}
    private var revision = 0L
    private var confirmedOrder = emptyList<String>()
    private var resetUndo: Pair<Long, List<String>>? = null

    /** Bind the selected assistant for every optimistic update and later outcome, then enter Arrange without writing. */
    suspend fun start(savedOrder: List<String>?) {
        if (mutable.value.arranging) return
        updateSnapshot = try {
            prepare()
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutableEvents.tryEmit(ConversationArrangeEvent.Failed(error))
            return
        }
        if (mutable.value.pending == 0) {
            confirmedOrder = savedOrder?.distinct().orEmpty()
            mutable.update { it.copy(arranging = true, order = confirmedOrder, drag = null) }
        } else {
            mutable.update { it.copy(arranging = true, drag = null) }
        }
        resetUndo = null
    }

    fun done() = mutable.update { it.copy(arranging = false, drag = null) }

    fun shownOrder(activityIds: List<String>): List<String> {
        val state = mutable.value
        return orderedLoadedIds(activityIds, state.drag?.order ?: state.order)
    }

    fun startDrag(id: String, activityIds: List<String>) {
        val state = mutable.value
        if (!state.arranging || state.drag != null) return
        val order = shownOrder(activityIds)
        if (id in order) mutable.update { it.copy(drag = ConversationDrag(id, order)) }
    }

    fun dragTo(index: Int) = mutable.update { state ->
        val drag = state.drag ?: return@update state
        val from = drag.order.indexOf(drag.id)
        val to = index.coerceIn(0, drag.order.lastIndex)
        if (from < 0 || from == to) state else state.copy(drag = drag.copy(order = drag.order.moved(from, to)))
    }

    fun cancelDrag() = mutable.update { it.copy(drag = null) }

    suspend fun endDrag(activityIds: List<String>) {
        val drag = mutable.value.drag ?: return
        commit(drag.order, activityIds)
    }

    suspend fun moveUp(id: String, activityIds: List<String>) = move(id, -1, activityIds)
    suspend fun moveDown(id: String, activityIds: List<String>) = move(id, 1, activityIds)

    private suspend fun move(id: String, by: Int, activityIds: List<String>) {
        val state = mutable.value
        if (!state.arranging || state.drag != null) return
        val order = shownOrder(activityIds)
        val from = order.indexOf(id)
        if (from < 0 || from + by !in order.indices) return
        commit(order.moved(from, from + by), activityIds)
    }

    /** [] means activity time on the wire. Undo is tied to this revision, never to a later move or reset. */
    suspend fun reset(activityIds: List<String>) {
        val state = mutable.value
        if (!state.arranging || state.drag != null || state.order.isEmpty()) return
        val before = state.order
        commit(emptyList(), activityIds, keepUnknown = false) { id ->
            resetUndo = id to before
            mutableEvents.tryEmit(ConversationArrangeEvent.Reset(id))
        }
    }

    suspend fun undoReset(resetRevision: Long, activityIds: List<String>) {
        val undo = resetUndo
        if (undo == null || undo.first != resetRevision || revision != resetRevision) return
        resetUndo = null
        val restored = orderedLoadedIds(activityIds, undo.second)
        commit(restored, activityIds, savedOrder = undo.second)
    }

    /** Returns its revision so reset can scope Undo even while the serialized write is still running. */
    private suspend fun commit(
        arrangedIds: List<String>,
        activityIds: List<String>,
        keepUnknown: Boolean = true,
        savedOrder: List<String> = mutable.value.order,
        onOptimistic: (Long) -> Unit = {},
    ): Long {
        val payload = if (keepUnknown) conversationOrderPayload(activityIds, arrangedIds, savedOrder) else emptyList()
        val id = ++revision
        resetUndo = null
        mutable.update { it.copy(order = payload, drag = null, pending = it.pending + 1) }
        onOptimistic(id)
        try {
            applySnapshot(id, payload)
            writes.withLock {
                val confirmed = write(payload)?.distinct()
                    ?: if (payload.isEmpty()) emptyList()
                    else throw IllegalStateException("Hub did not acknowledge the saved conversation order")
                confirmedOrder = confirmed
                if (revision == id && applySnapshot(id, confirmed)) {
                    mutable.update { it.copy(order = confirmed) }
                }
            }
        } catch (cancelled: CancellationException) {
            withContext(NonCancellable) { rollback(id) }
            throw cancelled
        } catch (error: Exception) {
            if (rollback(id)) mutableEvents.tryEmit(ConversationArrangeEvent.Failed(error))
        } finally {
            mutable.update { it.copy(pending = (it.pending - 1).coerceAtLeast(0)) }
        }
        return id
    }

    /** False when a newer revision took over before or during the snapshot update; its own update follows this one. */
    private suspend fun applySnapshot(id: Long, order: List<String>): Boolean = snapshotWrites.withLock {
        if (revision != id) return@withLock false
        updateSnapshot(order)
        revision == id
    }

    private suspend fun rollback(id: Long): Boolean {
        if (revision != id) return false
        resetUndo = null
        val applied = applySnapshot(id, confirmedOrder)
        if (applied) mutable.update { it.copy(order = confirmedOrder, drag = null) }
        return applied
    }
}

/** Listed loaded ids first, then newly arrived loaded ids, then every still-unknown saved id in its saved order. */
internal fun conversationOrderPayload(
    activityIds: List<String>,
    arrangedIds: List<String>,
    savedOrder: List<String>?,
): List<String> {
    val loaded = orderedLoadedIds(activityIds, arrangedIds)
    val seen = loaded.toHashSet()
    return loaded + savedOrder.orEmpty().distinct().filterNot { it in seen }
}

private fun orderedLoadedIds(activityIds: List<String>, order: List<String>): List<String> {
    val available = activityIds.toHashSet()
    val listed = order.distinct().filter { it in available }
    val seen = listed.toHashSet()
    return listed + activityIds.distinct().filterNot { it in seen }
}

private fun <T> List<T>.moved(from: Int, to: Int): List<T> = toMutableList().apply { add(to, removeAt(from)) }
