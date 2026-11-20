package persona.feature.assistant.settings

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.async
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.SavedPrompt
import persona.core.contracts.api.SavedPromptsApi
import persona.core.hub.HubError
import persona.core.hub.HubFailure

@OptIn(ExperimentalCoroutinesApi::class)
class SavedPromptsStoreTest {
    private val first = SavedPrompt("a", "review", "Review these changes.")
    private val second = SavedPrompt("b", "notes", "Write notes.")
    private val list = listOf(first, second)

    private class Fake(initial: List<SavedPrompt>) : SavedPromptsApi {
        var reads = 0
        var load: suspend () -> List<SavedPrompt>? = { initial }
        var saving: suspend (String?, String, String) -> SavedPrompt = { _, _, _ -> error("Unexpected save") }
        var ordering: suspend (List<String>) -> List<SavedPrompt> = { error("Unexpected reorder") }
        var removing: suspend (String) -> Unit = { error("Unexpected delete") }

        override suspend fun list(): List<SavedPrompt>? { reads++; return load() }
        override suspend fun save(id: String?, name: String, text: String) = saving(id, name, text)
        override suspend fun reorder(ids: List<String>) = ordering(ids)
        override suspend fun delete(id: String) = removing(id)
    }

    @Test fun failedReadKeepsAPreviousListAndA404EmptiesIt() = runTest {
        val hub = Fake(list)
        val store = SavedPromptsStore(hub)
        store.refresh()
        val down = HubFailure(HubError.Server, status = 503)
        hub.load = { throw down }
        store.refresh()
        assertSame(down, store.state.value.loadFailure)
        assertEquals(list, store.state.value.prompts)
        hub.load = { null }
        store.refresh()
        assertEquals(false, store.state.value.available)
        assertTrue(store.state.value.prompts.isEmpty())
        assertNull(store.state.value.loadFailure)
        assertFalse(store.save(null, "review", "Text"))
    }

    @Test fun createAppendsAndEditKeepsItsPositionAndRefusalKeepsTheList() = runTest {
        val hub = Fake(list)
        val store = SavedPromptsStore(hub)
        store.refresh()
        val conflict = HubFailure(HubError.Server, "Another prompt has that name", status = 409)
        hub.saving = { _, _, _ -> throw conflict }
        assertFalse(store.save(null, "review", "Draft"))
        assertSame(conflict, store.state.value.writeFailure)
        assertEquals(list, store.state.value.prompts)
        assertEquals(2, hub.reads)

        val added = SavedPrompt("c", "check", "Check.")
        hub.saving = { id, name, text ->
            assertNull(id)
            assertEquals("check", name)
            assertEquals("Check.", text)
            added
        }
        assertTrue(store.save(null, "check", "Check."))
        assertEquals(list + added, store.state.value.prompts)
        val edited = first.copy(text = "Look again.")
        hub.saving = { id, _, _ -> assertEquals(first.id, id); edited }
        assertTrue(store.save(first.id, first.name, edited.text))
        assertEquals(listOf(edited, second, added), store.state.value.prompts)
    }

    @Test fun failedMoveAndDeleteRollBackAndOrderConflictRereads() = runTest {
        val hub = Fake(list)
        val store = SavedPromptsStore(hub)
        store.refresh()
        val reordered = CompletableDeferred<List<SavedPrompt>>()
        hub.ordering = { ids ->
            assertEquals(listOf(second.id, first.id), ids)
            reordered.await()
        }
        val move = async { store.move(second.id, -1) }
        runCurrent()
        assertEquals(list.reversed(), store.state.value.prompts)
        assertFalse(store.delete(first.id))
        hub.load = { listOf(second) }
        reordered.completeExceptionally(HubFailure(HubError.Server, "List changed", 409))
        assertFalse(move.await())
        assertEquals(listOf(second), store.state.value.prompts)
        assertEquals(2, hub.reads)

        val removed = CompletableDeferred<Unit>()
        hub.removing = { id -> assertEquals(second.id, id); removed.await() }
        val delete = async { store.delete(second.id) }
        runCurrent()
        assertTrue(store.state.value.prompts.isEmpty())
        removed.completeExceptionally(HubFailure(HubError.Server, "Unavailable", 503))
        assertFalse(delete.await())
        assertEquals(listOf(second), store.state.value.prompts)
        assertEquals("Unavailable", store.state.value.writeFailure?.message)
        hub.removing = { }
        assertTrue(store.delete(second.id))
        assertTrue(store.state.value.prompts.isEmpty())
    }

    @Test fun aLateReadCannotUndoAWriteAndAnEventDuringTheWriteIsReconciled() = runTest {
        val hub = Fake(list)
        val store = SavedPromptsStore(hub)
        store.refresh()
        val old = CompletableDeferred<List<SavedPrompt>>()
        hub.load = { old.await() }
        val obsolete = launch { store.refresh() }
        runCurrent()
        val moved = CompletableDeferred<List<SavedPrompt>>()
        hub.ordering = { moved.await() }
        val write = async { store.move(first.id, 1) }
        runCurrent()
        store.refresh()
        assertEquals(2, hub.reads)
        old.complete(list)
        obsolete.join()
        assertEquals(list.reversed(), store.state.value.prompts)
        val addedElsewhere = SavedPrompt("c", "plan", "Plan first.")
        hub.load = { list.reversed() + addedElsewhere }
        moved.complete(list.reversed())
        assertTrue(write.await())
        assertEquals(list.reversed() + addedElsewhere, store.state.value.prompts)
        assertEquals(3, hub.reads)
    }

    @Test fun validationUsesCodePointsAndNormalizesTheSubmittedLineBreaks() {
        assertEquals("校閲.v2", SavedPromptInput.name(" /校閲.v2 "))
        assertTrue(SavedPromptInput.validName("e\u0301"))
        assertTrue(SavedPromptInput.validName("𐐀".repeat(32)))
        assertFalse(SavedPromptInput.validName("𐐀".repeat(33)))
        assertFalse(SavedPromptInput.validName("two words"))
        assertTrue(SavedPromptInput.nameTaken("REVIEW", null, list))
        assertFalse(SavedPromptInput.nameTaken("REVIEW", first.id, list))
        assertEquals("a\nb\tc", SavedPromptInput.text(" a\r\nb\tc\r "))
        assertTrue(SavedPromptInput.validText("🙂".repeat(8000)))
        assertFalse(SavedPromptInput.validText("🙂".repeat(8001)))
        assertFalse(SavedPromptInput.validText("a\u0000b"))
    }
}
