package persona.core.data

import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.CoreText.Key
import persona.core.contracts.api.*
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.rustcore.RustCore

/** #11451: a node whose history is gone answers 404/not_found in the hub's words, not a retryable empty history. */
class ChatHistoryNotFoundTest {
    private val key = SessionKey("computer-a", "missing-history")
    private val hubSentence = "This conversation’s history is no longer on this computer."

    private enum class Adapter { Kotlin, Rust }

    private class Frames : ContinuityFrameScheduler {
        private val frames = ConcurrentLinkedQueue<() -> Unit>()
        override fun afterFirstFrame(start: () -> Unit) = start()
        override fun schedule(frame: () -> Unit) { frames.add(frame) }
        override suspend fun submitted(core: RustCore) { check(core.flush(5_000)) }
        fun run() {
            val count = frames.size
            repeat(count) { frames.poll()?.invoke() }
        }
    }

    private inner class Fixture(
        val repository: HubChatRepository,
        val connection: FakeConnection,
        val scope: CoroutineScope,
        val frames: Frames?,
        val queries: ConcurrentLinkedQueue<ItemsQuery>,
        val snapshot: AtomicReference<persona.core.contracts.api.ChatSnapshot>,
    ) {
        suspend fun pump(done: () -> Boolean) = withTimeout(5_000) {
            while (!done()) {
                frames?.run()
                delay(1)
            }
        }

        suspend fun older() {
            val read = scope.async { repository.loadOlder(key) }
            pump { read.isCompleted }
            read.await()
        }

        fun olderReads() = queries.count { it.beforeSeq != null }
    }

    private suspend fun fixture(
        adapter: Adapter,
        transport: FakeChatTransport,
        queries: ConcurrentLinkedQueue<ItemsQuery>,
        test: suspend Fixture.() -> Unit,
    ) {
        val frames = if (adapter == Adapter.Rust) Frames() else null
        val driver = frames?.let(::RustContinuityDriver)
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.Unconfined)
        val connection = FakeConnection()
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat()), FakeSessions(), scope,
            continuityFactory = { chat, applied ->
                if (driver == null) KotlinContinuityAdapter(chat) else RustContinuityAdapter(chat, driver, applied)
            })
        val snapshot = AtomicReference(persona.core.contracts.api.ChatSnapshot())
        val observer = scope.launch { repository.observe(key).collect { snapshot.set(it) } }
        try {
            Fixture(repository, connection, scope, frames, queries, snapshot).test()
        } finally {
            observer.cancelAndJoin()
            job.cancelAndJoin()
            driver?.close()
        }
    }

    private fun item(seq: Int) = Item("$seq#0", seq, role = "assistant", kind = "text", text = "Message $seq")

    private fun transport(
        queries: ConcurrentLinkedQueue<ItemsQuery>,
        latest: suspend () -> ItemsResponse,
        older: suspend () -> ItemsResponse,
    ) = FakeChatTransport().apply {
        fetch = { query ->
            queries += query
            if (query.beforeSeq != null) older() else latest()
        }
    }

    private fun missingHistory() = HubFailure(HubError.Server, hubSentence,
        status = 404, code = "not_found", detail = "history_missing").also {
        // The transport's typed failure retains all three pieces the repository needs.
        assertEquals(404, it.status)
        assertEquals("not_found", it.code)
        assertEquals(hubSentence, it.message)
        assertEquals("history_missing", it.detail)
    }

    private suspend fun latestMissingHistory(adapter: Adapter) {
        val queries = ConcurrentLinkedQueue<ItemsQuery>()
        val transport = transport(queries, latest = { throw missingHistory() }, older = { error("No older page requested") })
        fixture(adapter, transport, queries) {
            pump { queries.size == 1 && !snapshot.get().loading }
            assertTrue("no cached items", snapshot.get().items.isEmpty())
            assertFalse("the refused latest page is not still loading", snapshot.get().loading)
            assertFalse("there is no older-page spinner", snapshot.get().loadingOlder)
            assertEquals("the hub's exact sentence", hubSentence, snapshot.get().error)
            assertEquals("a missing latest page is not retried", 1, queries.size)
        }
    }

    private suspend fun olderMissingHistory(adapter: Adapter) {
        val queries = ConcurrentLinkedQueue<ItemsQuery>()
        val recent = listOf(item(3), item(4))
        val transport = transport(queries,
            latest = { ItemsResponse(recent, hasMore = true, history = "h", toSeq = 4) },
            older = { throw missingHistory() })
        fixture(adapter, transport, queries) {
            pump { snapshot.get().verified && snapshot.get().hasOlder }
            older()
            pump { !snapshot.get().loadingOlder }
            assertEquals(recent, snapshot.get().items)
            assertFalse("404/not_found ends the available older history", snapshot.get().hasOlder)
            assertFalse("the older-page spinner ends", snapshot.get().loadingOlder)
            assertEquals("one older-page read", 1, olderReads())
            older()
            assertEquals("the exhausted history does not request another older page", 1, olderReads())
        }
    }

    private suspend fun olderFailuresKeepExistingBehavior(adapter: Adapter) {
        for (failure in listOf(
            HubFailure(HubError.Server, "HTTP 404", status = 404),
            HubFailure(HubError.Server, "The node could not read this page.", status = 503, code = "history_unavailable"),
        )) {
            val queries = ConcurrentLinkedQueue<ItemsQuery>()
            val recent = listOf(item(3), item(4))
            val transport = transport(queries,
                latest = { ItemsResponse(recent, hasMore = true, history = "h", toSeq = 4) },
                older = { throw failure })
            fixture(adapter, transport, queries) {
                pump { snapshot.get().verified && snapshot.get().hasOlder }
                older()
                pump { !snapshot.get().loadingOlder }
                assertEquals("status=${failure.status} code=${failure.code}", recent, snapshot.get().items)
                assertTrue("status=${failure.status} code=${failure.code} keeps older history available", snapshot.get().hasOlder)
                assertEquals("the existing older-page error", DataText.of(Key.ChatHistoryFailed), snapshot.get().error)
                assertEquals(1, olderReads())
            }
        }
    }

    private suspend fun latestPlain404KeepsExistingBehavior(adapter: Adapter) {
        val queries = ConcurrentLinkedQueue<ItemsQuery>()
        val failure = HubFailure(HubError.Server, "HTTP 404", status = 404)
        val transport = transport(queries, latest = { throw failure }, older = { error("No older page requested") })
        fixture(adapter, transport, queries) {
            pump { queries.size == 1 && !snapshot.get().loading }
            assertEquals(DataText.of(Key.ChatSyncFailed), snapshot.get().error)
            assertFalse(snapshot.get().loading)
        }
    }

    @Test fun kotlinOlderNotFoundFromThePreviousHistoryDoesNotEndItsReplacement() = runBlocking {
        val queries = ConcurrentLinkedQueue<ItemsQuery>()
        val failure = CompletableDeferred<Nothing>()
        val transport = transport(queries,
            latest = { ItemsResponse(listOf(item(3), item(4)), hasMore = true, history = "h", toSeq = 4) },
            older = { failure.await() })
        fixture(Adapter.Kotlin, transport, queries) {
            pump { snapshot.get().verified && snapshot.get().hasOlder }
            val loading = scope.async { repository.loadOlder(key) }
            pump { olderReads() == 1 }

            connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(item(20)),
                history = "replacement", fromSeq = 0, toSeq = 20, backlog = true, hasMore = true))
            pump { snapshot.get().items.map { it.seq } == listOf(20) && snapshot.get().hasOlder }
            failure.completeExceptionally(missingHistory())
            pump { loading.isCompleted }
            loading.await()

            assertTrue("a response for the replaced history must not close its replacement", snapshot.get().hasOlder)
            assertEquals(listOf(20), snapshot.get().items.map { it.seq })
            assertFalse(snapshot.get().loadingOlder)
        }
    }

    @Test fun kotlinLatestNotFoundShowsTheHubSentenceAndStopsLoading() = runBlocking {
        latestMissingHistory(Adapter.Kotlin)
    }

    @Test fun rustLatestNotFoundShowsTheHubSentenceAndStopsLoading() = runBlocking {
        latestMissingHistory(Adapter.Rust)
    }

    @Test fun kotlinOlderNotFoundEndsHistoryWithoutAnotherRead() = runBlocking {
        olderMissingHistory(Adapter.Kotlin)
    }

    @Test fun rustOlderNotFoundEndsHistoryWithoutAnotherRead() = runBlocking {
        olderMissingHistory(Adapter.Rust)
    }

    @Test fun kotlinOtherOlderFailuresKeepTheirExistingBehavior() = runBlocking {
        olderFailuresKeepExistingBehavior(Adapter.Kotlin)
    }

    @Test fun rustOtherOlderFailuresKeepTheirExistingBehavior() = runBlocking {
        olderFailuresKeepExistingBehavior(Adapter.Rust)
    }

    @Test fun kotlinLatestPlain404KeepsTheExistingSyncFailure() = runBlocking {
        latestPlain404KeepsExistingBehavior(Adapter.Kotlin)
    }

    @Test fun rustLatestPlain404KeepsTheExistingSyncFailure() = runBlocking {
        latestPlain404KeepsExistingBehavior(Adapter.Rust)
    }
}
