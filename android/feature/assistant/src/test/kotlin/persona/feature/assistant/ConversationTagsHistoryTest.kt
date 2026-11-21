package persona.feature.assistant

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantConfigUpdate
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.ConversationTagFilters

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class ConversationTagsHistoryTest {
    private val original = Session(machineId = "hub", sessionId = "one", title = "One", tags = listOf("before"))
    private val other = Session(machineId = "hub", sessionId = "two", title = "Two", tags = listOf("trip"))

    private class TagsApi(private val hub: HistoryHub) : AssistantManagement by hub {
        val writes = mutableListOf<Pair<SessionKey, List<String>>>()
        var refuse = false
        var entered: CompletableDeferred<Unit>? = null
        var release: CompletableDeferred<Unit>? = null

        override suspend fun setConversationTags(session: SessionKey, tags: List<String>) {
            writes += session to tags
            entered?.complete(Unit)
            release?.await()
            if (refuse) throw IllegalStateException("Hub refused tags")
        }
    }

    private class FiltersApi(hub: HistoryHub) : AssistantManagement by hub {
        val writes = mutableListOf<ConversationTagFilters>()
        var refuse = false
        var omitResponse = false
        var answer: ConversationTagFilters? = null
        var entered: CompletableDeferred<Unit>? = null
        var release: CompletableDeferred<Unit>? = null

        override suspend fun updateConfig(update: AssistantConfigUpdate): AssistantConfig {
            val value = requireNotNull(update.conversationTagFilters)
            writes += value
            entered?.complete(Unit)
            release?.await()
            if (refuse) throw IllegalStateException("Hub refused tag filters")
            return AssistantConfig(conversationTagFilters = when {
                omitResponse -> ConversationTagFilters.Unsupported
                else -> answer ?: value
            })
        }
    }

    @Test fun filterWritesAreOptimisticOrderedAndAuthoritativelyReconciled() = runTest {
        val hub = HistoryHub(original).apply {
            assistant.value = assistant.value.copy(conversationTagFilters = ConversationTagFilters(null))
        }
        val api = FiltersApi(hub).apply {
            entered = CompletableDeferred()
            release = CompletableDeferred()
            answer = ConversationTagFilters(listOf("first", "second", "normalized-by-hub"))
        }
        val history = ConversationHistory(api, hub)

        val saving = async { history.saveConversationTagFilters(listOf(" second ", "first", "second")) }
        api.entered!!.await()
        assertEquals(listOf("second", "first"), hub.assistant.value.conversationTagFilters.entries)
        assertEquals(listOf("second", "first"), api.writes.single().entries)
        assertTrue(history.tagFiltersBusy)

        api.release!!.complete(Unit)
        assertTrue(saving.await().isSuccess)
        assertEquals(listOf("first", "second", "normalized-by-hub"), hub.assistant.value.conversationTagFilters.entries)
        assertFalse(history.tagFiltersBusy)
    }

    @Test fun refusedFilterRemovalRestoresEntriesSelectionAndReloads() = runTest {
        val hub = HistoryHub(original).apply {
            assistant.value = assistant.value.copy(
                conversationTagFilters = ConversationTagFilters(listOf("before", "kept")))
        }
        val api = FiltersApi(hub).apply { refuse = true }
        val history = ConversationHistory(api, hub)
        history.selectTag("before")

        val result = history.saveConversationTagFilters(listOf("kept"))

        assertTrue(result.isFailure)
        assertEquals(listOf("before", "kept"), hub.assistant.value.conversationTagFilters.entries)
        assertEquals("before", history.activeTag)
        assertEquals(1, hub.refreshes)
        assertFalse(history.tagFiltersBusy)
    }

    @Test fun refusedResetRestoresAnUnusedSelectionClearedByAutomaticSettling() = runTest {
        val hub = HistoryHub(original).apply {
            assistant.value = assistant.value.copy(
                conversationTagFilters = ConversationTagFilters(listOf("unused")))
        }
        val api = FiltersApi(hub).apply {
            refuse = true
            entered = CompletableDeferred()
            release = CompletableDeferred()
        }
        val history = ConversationHistory(api, hub)
        history.selectTag("unused")

        val saving = launch { history.saveConversationTagFilters(null) }
        api.entered!!.await()
        history.settle(listOf(other))
        assertNull(history.activeTag)
        api.release!!.complete(Unit)
        saving.join()

        assertEquals(listOf("unused"), hub.assistant.value.conversationTagFilters.entries)
        assertEquals("unused", history.activeTag)
    }

    @Test fun missingFilterFieldInThePutAnswerIsAFailedWriteAndRollsBack() = runTest {
        val hub = HistoryHub(original).apply {
            assistant.value = assistant.value.copy(conversationTagFilters = ConversationTagFilters(null))
        }
        val api = FiltersApi(hub).apply { omitResponse = true }
        val history = ConversationHistory(api, hub)

        val result = history.saveConversationTagFilters(emptyList())

        assertTrue(result.isFailure)
        assertEquals(ConversationTagFilters(null), hub.assistant.value.conversationTagFilters)
        assertEquals(1, hub.refreshes)
    }

    @Test fun customSelectionSurvivesHavingNoMatchingConversationAndResetWritesNull() = runTest {
        val hub = HistoryHub(original).apply {
            assistant.value = assistant.value.copy(
                conversationTagFilters = ConversationTagFilters(listOf("unused", "before")))
        }
        val api = FiltersApi(hub)
        val history = ConversationHistory(api, hub)
        history.selectTag("unused")
        history.settle(listOf(other))
        assertEquals("unused", history.activeTag)

        assertTrue(history.saveConversationTagFilters(null).isSuccess)
        assertNull(api.writes.single().entries)
        assertTrue(hub.assistant.value.conversationTagFilters.supported)
        assertNull(hub.assistant.value.conversationTagFilters.entries)
    }

    @Test fun saveIsOptimisticAndSettlesOnlyWhenTheHubCarriesTheWholeSet() = runTest {
        val hub = HistoryHub(original, other)
        val api = TagsApi(hub)
        val history = ConversationHistory(api, hub)
        api.entered = CompletableDeferred()
        api.release = CompletableDeferred()

        val saving = launch { history.saveTags(original.key, listOf(" after ", "trip", "after")) }
        api.entered!!.await()
        assertEquals(listOf("after", "trip"), history.tagged(listOf(original, other)).first().tags)
        assertEquals(listOf("after", "trip"), api.writes.single().second)

        // The acknowledged read is still stale: the optimistic row stays visible.
        api.release!!.complete(Unit)
        saving.join()
        assertEquals(listOf("after", "trip"), history.tagged(listOf(original, other)).first().tags)

        val confirmed = original.copy(tags = listOf("after", "trip"))
        history.settle(listOf(confirmed, other))
        val elsewhere = original.copy(tags = listOf("elsewhere"))
        assertEquals(listOf("elsewhere"), history.tagged(listOf(elsewhere, other)).first().tags)
    }

    @Test fun aRefusedSaveRestoresThePreviousRowAndAnInvalidSetNeverWrites() = runTest {
        val hub = HistoryHub(original, other)
        val api = TagsApi(hub)
        val history = ConversationHistory(api, hub)
        api.refuse = true
        api.entered = CompletableDeferred()
        api.release = CompletableDeferred()

        val saving = launch { history.saveTags(original.key, listOf("optimistic")) }
        api.entered!!.await()
        assertEquals(listOf("optimistic"), history.tagged(listOf(original)).single().tags)
        api.release!!.complete(Unit)
        saving.join()
        assertEquals(listOf("before"), history.tagged(listOf(original)).single().tags)

        val result = history.saveTags(original.key, listOf("好".repeat(33)))
        assertTrue(result.isFailure)
        assertEquals(1, api.writes.size)
        assertEquals(listOf("before"), history.tagged(listOf(original)).single().tags)
    }

    @Test fun aTagThatDisappearsEverywhereClearsTheSelectionIncludingArchivedRows() = runTest {
        val hub = HistoryHub(original, other)
        val history = ConversationHistory(null, hub)
        val archived = original.copy(archived = true, tags = listOf("kept"))

        history.selectTag("kept")
        assertEquals("kept", history.activeTag)
        history.settle(listOf(other, archived))
        assertEquals("kept", history.activeTag)
        history.settle(listOf(other, archived.copy(tags = emptyList())))
        assertNull(history.activeTag)

        history.selectTag("trip")
        history.selectTag("trip")
        assertNull(history.activeTag)
        history.selectTag("trip")
        history.settle(listOf(other))
        assertEquals("trip", history.activeTag)
        history.settle(listOf(other.copy(tags = null)))
        assertNull(history.activeTag)
    }
}
