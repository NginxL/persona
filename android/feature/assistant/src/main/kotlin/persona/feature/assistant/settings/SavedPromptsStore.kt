package persona.feature.assistant.settings

import androidx.compose.runtime.staticCompositionLocalOf
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import persona.core.contracts.api.SavedPrompt
import persona.core.contracts.api.SavedPromptsApi
import persona.core.contracts.api.hubCodedFailure

data class SavedPromptsState(
    val prompts: List<SavedPrompt> = emptyList(),
    val available: Boolean? = null,
    val refreshing: Boolean = false,
    val writing: Boolean = false,
    val loadFailure: Exception? = null,
    val writeFailure: Exception? = null,
)

/** One assistant on one hub. Call from the UI dispatcher and replace the store when either changes. */
class SavedPromptsStore(private val api: SavedPromptsApi) {
    private val mutable = MutableStateFlow(SavedPromptsState())
    val state = mutable.asStateFlow()
    private var read = 0L
    private var refreshPending = false

    suspend fun refresh() {
        if (state.value.writing) {
            refreshPending = true
            return
        }
        val request = ++read
        mutable.value = state.value.copy(refreshing = true)
        try {
            val prompts = api.list()
            currentCoroutineContext().ensureActive()
            if (request == read) mutable.value = state.value.copy(
                prompts = prompts.orEmpty(), available = prompts != null, loadFailure = null)
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            if (request == read) mutable.value = state.value.copy(loadFailure = error)
        } finally {
            if (request == read) mutable.value = state.value.copy(refreshing = false)
        }
    }

    fun clearWriteFailure() {
        mutable.value = state.value.copy(writeFailure = null)
    }

    internal suspend fun save(id: String?, name: String, text: String): Boolean {
        val prompts = state.value.prompts
        return write(prompts) {
            val saved = api.save(id, name, text)
            if (prompts.any { it.id == saved.id }) prompts.map { if (it.id == saved.id) saved else it }
            else prompts + saved
        }
    }

    internal suspend fun move(id: String, step: Int): Boolean {
        val prompts = state.value.prompts
        val from = prompts.indexOfFirst { it.id == id }
        val to = from + step
        if (from == -1 || to !in prompts.indices || from == to) return false
        val next = prompts.toMutableList().apply { add(to, removeAt(from)) }
        return write(next) { api.reorder(next.map { it.id }) }
    }

    internal suspend fun delete(id: String): Boolean {
        val next = state.value.prompts.filterNot { it.id == id }
        if (next.size == state.value.prompts.size) return false
        return write(next) { api.delete(id); next }
    }

    private suspend fun write(optimistic: List<SavedPrompt>, run: suspend () -> List<SavedPrompt>): Boolean {
        val before = state.value
        if (before.available != true || before.writing) return false
        ++read
        refreshPending = refreshPending || before.refreshing
        mutable.value = before.copy(prompts = optimistic, refreshing = false, writing = true, writeFailure = null)
        return try {
            val prompts = run()
            currentCoroutineContext().ensureActive()
            mutable.value = state.value.copy(prompts = prompts)
            true
        } catch (cancelled: CancellationException) {
            mutable.value = state.value.copy(prompts = before.prompts)
            throw cancelled
        } catch (error: Exception) {
            mutable.value = state.value.copy(prompts = before.prompts, writeFailure = error)
            // A conflict or a missing prompt can mean the list changed elsewhere.
            if (hubCodedFailure(error)?.status in setOf(404, 409)) refreshPending = true
            false
        } finally {
            mutable.value = state.value.copy(writing = false)
            if (refreshPending) {
                refreshPending = false
                refresh()
            }
        }
    }
}

val LocalSavedPromptsStore = staticCompositionLocalOf<SavedPromptsStore?> { null }
