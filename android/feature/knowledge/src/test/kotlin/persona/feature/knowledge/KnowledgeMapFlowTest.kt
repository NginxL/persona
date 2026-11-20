@file:OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)

package persona.feature.knowledge

import persona.core.contracts.HubScope
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import java.io.IOException
import java.util.concurrent.atomic.AtomicInteger

class KnowledgeMapFlowTest {
    private class Fake(topics: Int = 10, val broken: Set<String> = emptySet()) : KnowledgeRepository {
        override val scope = HubScope("demo", "map")
        val topicEntries = (1..topics).map { KnowledgeEntry("topics", "t$it", "Topic $it", extra = "1") }
        val projectEntries = listOf(
            KnowledgeEntry("projects", "p1", "Alpha", importance = "P0", extra = "short · active"),
            KnowledgeEntry("projects", "p2", "Beta", extra = "short · ignored"),
            KnowledgeEntry("projects", "p3", "Gamma", extra = "short · merged"),
        )
        var failList: Throwable? = null
        val named = mutableListOf<String?>()
        val listed = mutableListOf<KnowledgeKind>()
        val read = mutableListOf<String>()
        private val running = AtomicInteger(0)
        var peak = 0
        override suspend fun list(kind: KnowledgeKind, tag: String?): KnowledgeList {
            listed += kind
            named += currentCoroutineContext()[ManagerPins]?.manager(listOf("kb", kind.wire), "hub")
            failList?.let { throw it }
            return KnowledgeList(if (kind == KnowledgeKind.Topics) topicEntries else projectEntries)
        }
        override suspend fun detail(kind: KnowledgeKind, id: String): KnowledgeDetail {
            named += currentCoroutineContext()[ManagerPins]?.manager(listOf("kb", kind.wire, id), "hub")
            val now = running.incrementAndGet()
            peak = maxOf(peak, now)
            try {
                delay(10)
                read += id
                if (id in broken) throw IOException("down")
                return KnowledgeDetail(kind.wire, id, "data/${kind.wire}/$id",
                    points = if (kind == KnowledgeKind.Topics) listOf(TopicPoint("1", null, "fact", "A point of $id")) else null)
            } finally { running.decrementAndGet() }
        }
        override suspend fun search(query: String) = emptyList<KnowledgeHit>()
    }

    private fun TestScope.withMap(fake: Fake?, owner: String? = null, block: TestScope.(KnowledgeMapViewModel, MutableStateFlow<KnowledgeRepository?>) -> Unit) {
        val connection = MutableStateFlow<KnowledgeRepository?>(fake)
        Dispatchers.setMain(StandardTestDispatcher(testScheduler))
        val store = androidx.lifecycle.ViewModelStore()
        try {
            val model = KnowledgeMapViewModel(object : KnowledgeConnection { override val current = connection }, StandardTestDispatcher(testScheduler), owner)
            store.put("map", model)
            backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect() }
            runCurrent()
            block(model, connection)
        } finally {
            backgroundScope.cancel(); store.clear(); runCurrent(); Dispatchers.resetMain()
        }
    }

    @Test fun topicsListFirstThenTheirDetailsAtMostSixAtATimeAndCountTheOnesThatFail() = runTest {
        val fake = Fake(topics = 10, broken = setOf("t3"))
        withMap(fake) { model, _ ->
            // The list answers at once; its entries show before any detail is read.
            assertTrue(model.page.value.loaded)
            assertEquals(10, model.page.value.entries.size)
            assertTrue(model.page.value.loading)
            advanceUntilIdle()
            val page = model.page.value
            assertFalse(page.loading)
            assertEquals(MapSide.Topics, page.side)
            assertEquals(9, page.details.size)
            assertEquals(1, page.failed)
            assertNull(page.message)
            assertTrue("t3" !in page.details)
            assertTrue("peak ${fake.peak}", fake.peak in 2..MAP_DETAIL_PARALLEL)
        }
    }

    @Test fun projectsSkipTheOnesThatAreNotProjectsAndNeverReadTheirDetails() = runTest {
        val fake = Fake()
        withMap(fake) { model, _ ->
            advanceUntilIdle()
            model.side(MapSide.Projects)
            advanceUntilIdle()
            assertEquals(MapSide.Projects, model.page.value.side)
            assertEquals(listOf("p1"), fake.read.filter { it.startsWith("p") })
            assertEquals(setOf("p1"), model.page.value.details.keys)
            assertEquals(3, model.page.value.entries.size)
        }
    }

    @Test fun goingBackToASideShowsWhatItHadWhileItReloads() = runTest {
        val fake = Fake(topics = 3)
        withMap(fake) { model, _ ->
            advanceUntilIdle()
            model.side(MapSide.Projects); advanceUntilIdle()
            model.side(MapSide.Topics); runCurrent()
            val page = model.page.value
            assertTrue(page.loading)
            assertEquals(3, page.entries.size)
            assertEquals(3, page.details.size)
            assertTrue(page.loaded)
            advanceUntilIdle()
            assertFalse(model.page.value.loading)
        }
    }

    @Test fun aFailedListIsAMessageAndAFailedReloadKeepsWhatWasShown() = runTest {
        val fake = Fake(topics = 2)
        withMap(fake) { model, _ ->
            advanceUntilIdle()
            fake.failList = IOException("offline")
            model.refresh(); advanceUntilIdle()
            val page = model.page.value
            assertNotNull(page.message)
            assertFalse(page.loading)
            assertEquals(2, page.entries.size)
            assertEquals(2, page.details.size)
            fake.failList = null
            model.refresh(); advanceUntilIdle()
            assertNull(model.page.value.message)
        }
    }

    @Test fun anUnpairedHubIsNotLoadingAndAnotherHubStartsFresh() = runTest {
        withMap(null) { model, connection ->
            advanceUntilIdle()
            assertFalse(model.page.value.paired)
            assertFalse(model.page.value.loading)
            connection.value = Fake(topics = 2); advanceUntilIdle()
            assertTrue(model.page.value.paired)
            assertEquals(2, model.page.value.details.size)
            connection.value = null; advanceUntilIdle()
            assertFalse(model.page.value.paired)
            assertTrue(model.page.value.entries.isEmpty())
        }
    }

    @Test fun theMapReadsTheAssistantItWasOpenedForWhoeverIsCurrentLater() = runTest {
        AssistantManagerChoice.choose("hub", "m-work", "assistant")
        try {
            val fake = Fake(topics = 3)
            withMap(fake, owner = "m-work") { model, _ ->
                advanceUntilIdle()
                // Another assistant made current while the map is open: its list, its details and its reload are still m-work's.
                AssistantManagerChoice.choose("hub", null, "assistant")
                model.refresh(); advanceUntilIdle()
                assertEquals(3, model.page.value.details.size)
                assertTrue(fake.named.isNotEmpty())
                assertTrue("${fake.named}", fake.named.all { it == "m-work" })
            }
        } finally { AssistantManagerChoice.choose("hub", null, "assistant") }
    }
}
