package persona.core.data

import java.util.concurrent.ConcurrentLinkedQueue
import java.util.concurrent.atomic.AtomicReference
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.domain.ContinuitySync.Action
import persona.core.rustcore.CoreHost
import persona.core.rustcore.RustCore
import persona.core.rustcore.wire.ChatEvent
import persona.core.rustcore.wire.Config
import persona.core.rustcore.wire.Effect
import persona.core.rustcore.wire.Request
import persona.core.rustcore.wire.Action as CoreAction

/** Worker completion, frame application and each UI operation are independently controlled. */
class ContinuityOperationTest {
    private val key = SessionKey("fixture", "operations")
    private fun item(seq: Int) = Item("$seq#0", seq, role = "assistant", kind = "tool_use")
    private fun fold(seq: Int, through: Int = seq + 1) = item(seq).copy(id = "fold:$seq", kind = "fold",
        fold = Fold(items = through - seq + 1, throughSeq = through, throughId = "$through#0"))

    @Test fun closingBeforeTheCreationFrameCancelsWaitersWithoutStartingTheCore() = runBlocking {
        val scheduled = CompletableDeferred<() -> Unit>()
        var creates = 0
        val driver = RustContinuityDriver(object : ContinuityFrameScheduler {
            override fun schedule(frame: () -> Unit) { scheduled.complete(frame) }
        }) { creates++; error("A closed driver must not initialize the core") }
        val adapter = RustContinuityAdapter("uninitialized", driver)
        val waiter = async { adapter.accept(ContinuityAdapter.Input.ContinueFill) }
        try {
            val firstFrame = scheduled.await()
            driver.close()
            withTimeout(1_000) { waiter.join() }
            assertTrue(waiter.isCancelled)
            firstFrame()
            assertEquals(0, creates)
        } finally { waiter.cancelAndJoin(); adapter.close(); driver.close() }
    }

    @Test fun oldHistoryResponseIsDiscardedBeforeTheNewHistoryFrameArrives() = runBlocking {
        Harness(listOf(item(10)), hasMore = true).use { h ->
            h.open()
            val oldPage = CompletableDeferred<ItemsResponse>()
            var olderSubmitted = false
            h.beforeAccept = { input -> if (input is ContinuityAdapter.Input.Older) olderSubmitted = true }
            h.transport.fetch = { query ->
                assertEquals(10, query.beforeSeq)
                oldPage.await()
            }
            val loading = h.scope.async { h.repository.loadOlder(key) }
            h.frames.run()
            h.connection.events.emit(ItemsEvent(key.machineId, key.sessionId, listOf(item(20)),
                history = "replacement", toSeq = 20, backlog = true, hasMore = true))
            // The worker has already changed history, but the repository's applied snapshot is still old.
            assertEquals("h", h.adapter.state.history)
            assertEquals(listOf(10), h.snapshot.get().items.map { it.seq })
            oldPage.complete(ItemsResponse(listOf(item(9)), history = "h", hasMore = false))
            pump(h.frames) { loading.isCompleted && h.adapter.state.history == "replacement" }
            loading.await()
            assertFalse("An old history response must not reach the new core history", olderSubmitted)
            assertEquals("replacement", h.adapter.state.history)
            assertEquals(listOf(20), h.adapter.state.items.map { it.seq })
        }
    }

    @Test fun cancellationBetweenFoldReservationAndHistoryReadReleasesTheReservation() = runBlocking {
        Harness(listOf(fold(2))).use { h ->
            h.open()
            val entries = h.repository.javaClass.getDeclaredField("entries").apply { isAccessible = true }
                .get(h.repository) as Map<*, *>
            val entry = requireNotNull(entries[key])
            val lock = entry.javaClass.getDeclaredField("lock").apply { isAccessible = true }
                .get(entry) as kotlinx.coroutines.sync.Mutex
            val active = entry.javaClass.getDeclaredField("activeFolds").apply { isAccessible = true }
                .get(entry) as Set<*>
            val held = CompletableDeferred<Unit>()
            val release = CompletableDeferred<Unit>()
            lock.lock()
            val first = h.scope.async { h.repository.expandFold(key, 2, 3) }
            val holder = h.scope.launch { lock.lock(); try { held.complete(Unit); release.await() } finally { lock.unlock() } }
            try {
                // The first lookup wins the lock, then the holder blocks the history read after reservation.
                lock.unlock()
                held.await()
                assertTrue(active.contains(fold(2).id))
                first.cancelAndJoin()
                assertFalse(active.contains(fold(2).id))
            } finally { release.complete(Unit); holder.join(); first.cancelAndJoin() }
            h.transport.fetch = { ItemsResponse(listOf(item(2), item(3)), history = "h") }
            val retry = h.scope.async { h.repository.expandFold(key, 2, 3) }
            pump(h.frames) { retry.isCompleted }
            assertEquals(listOf(2, 3), retry.await().map { it.seq })
        }
    }

    @Test fun effectOnlyAndAcknowledgedNoOpBothReachTheirOwners() = runBlocking {
        val frames = Frames()
        val host = AtomicReference<CoreHost>()
        val coreOwner = AtomicReference<String>()
        val driver = RustContinuityDriver(frames) { delegate ->
            host.set(delegate)
            RustCore(Config(), object : CoreHost by delegate {
                override fun run(effect: Effect) {
                    if (effect is Effect.Fetch) coreOwner.set(effect.chat)
                    delegate.run(effect)
                }
            })
        }
        val views = mutableListOf<ContinuityAdapter.Applied>()
        val adapter = RustContinuityAdapter("effects", driver, views::add)
        try {
            adapter.accept(ContinuityAdapter.Input.Restore(ContinuityAdapter.State(listOf(item(1)), "h", 1, false, true)))
            frames.run()
            adapter.accept(ContinuityAdapter.Input.ContinueFill)
            frames.run()
            views.clear()
            // No patch change accompanies this callback.
            host.get().run(Effect.Fetch(coreOwner.get(), Request.FetchAfter(7)))
            frames.run()
            assertEquals(listOf(Action.FetchAfter(7)), views.single().requests)
            views.clear()
            adapter.accept(ContinuityAdapter.Input.LoadOlder, 41)
            frames.run()
            assertEquals(listOf(ContinuityAdapter.Operation(41, emptyList())), views.single().operations)
        } finally { adapter.close(); driver.close() }
    }

    @Test fun rejectionDoesNotCancelAnotherChatsFillOrFoldAndReopenStillFetches() = runBlocking {
        val frames = Frames()
        val core = AtomicReference<RustCore>()
        val deadOwner = AtomicReference<String>()
        val driver = RustContinuityDriver(frames) { delegate ->
            RustCore(Config(), object : CoreHost by delegate {
                override fun run(effect: Effect) {
                    if (effect is Effect.Fetch && effect.chat.startsWith("closed:")) deadOwner.set(effect.chat)
                    delegate.run(effect)
                }
            }).also(core::set)
        }
        val otherViews = mutableListOf<ContinuityAdapter.Applied>()
        val dead = RustContinuityAdapter("closed", driver)
        val other = RustContinuityAdapter("other", driver, otherViews::add)
        var reopened: RustContinuityAdapter? = null
        try {
            dead.accept(ContinuityAdapter.Input.Restore(ContinuityAdapter.State(listOf(item(1)), "h", 1, true, true)))
            dead.accept(ContinuityAdapter.Input.ContinueFill)
            other.accept(ContinuityAdapter.Input.Restore(ContinuityAdapter.State(listOf(item(1), fold(2)), "h", 3, true, true)))
            other.accept(ContinuityAdapter.Input.ContinueFill)
            other.accept(ContinuityAdapter.Input.OpenFold(fold(2).id), 1)
            dead.close()
            check(core.get().dispatch(CoreAction.Chat(deadOwner.get(), ChatEvent.LoadOlder)))
            check(core.get().flush(5_000))
            frames.run()
            assertEquals(1L, driver.rejections.count)
            assertTrue(driver.rejections.lastReason.orEmpty().contains("not open"))
            assertTrue(otherViews.any { it.rejected })
            assertEquals(1, otherViews.flatMap { it.requests }.filterIsInstance<Action.FetchAfter>().size)
            assertEquals(1, otherViews.flatMap { it.requests }.filterIsInstance<Action.FetchRange>().size)
            otherViews.clear()
            // Diagnostics did not release either of the other chat's gates.
            other.accept(ContinuityAdapter.Input.ContinueFill)
            other.accept(ContinuityAdapter.Input.OpenFold(fold(2).id), 2)
            frames.run()
            assertTrue(otherViews.flatMap { it.requests }.isEmpty())
            other.accept(ContinuityAdapter.Input.After(ContinuityAdapter.Batch(listOf(item(4)), "h", 3, 4)))
            other.accept(ContinuityAdapter.Input.Range(listOf(item(2), item(3)), foldId = fold(2).id), 3)
            frames.run()
            assertEquals(listOf(1, 2, 3, 4), other.state.items.map { it.seq })
            assertTrue(otherViews.last().verified)
            val reopenedViews = mutableListOf<ContinuityAdapter.Applied>()
            reopened = RustContinuityAdapter("closed", driver, reopenedViews::add)
            reopened.accept(ContinuityAdapter.Input.Restore(ContinuityAdapter.State(listOf(item(1)), "h", 1, false, true)))
            reopened.accept(ContinuityAdapter.Input.ContinueFill)
            frames.run()
            assertEquals(listOf(Action.FetchAfter(1)), reopenedViews.flatMap { it.requests })
        } finally { dead.close(); other.close(); reopened?.close(); driver.close() }
    }

    @Test fun timedOutOlderEffectCannotSatisfyTheNextOperation() = runBlocking {
        Harness(listOf(item(10)), hasMore = true).use { h ->
            h.open()
            val queries = mutableListOf<Int>()
            h.transport.fetch = { query ->
                queries += requireNotNull(query.beforeSeq)
                ItemsResponse(listOf(item(19)), history = "h", hasMore = false)
            }
            val first = h.scope.async { h.repository.loadOlder(key) }
            // The old FetchOlder(10) and Ack exist in the core, but their frame is deliberately withheld.
            withTimeout(4_000) { first.await() }
            assertTrue(queries.isEmpty())
            h.adapter.accept(ContinuityAdapter.Input.Reload(listOf(item(20)), "h", 20, true))
            val second = h.scope.async { h.repository.loadOlder(key) }
            pump(h.frames) { second.isCompleted }
            second.await()
            assertEquals(listOf(20), queries)
        }
    }

    @Test fun timedOutFoldEffectCannotSatisfyAnotherFoldAndRetryIsNotSuppressed() = runBlocking {
        Harness(listOf(fold(2), fold(5))).use { h ->
            h.open()
            val ranges = mutableListOf<Int>()
            h.transport.fetch = { query ->
                val from = requireNotNull(query.fromSeq)
                ranges += from
                ItemsResponse(listOf(item(from), item(from + 1)), history = "h")
            }
            val first = h.scope.async { h.repository.expandFold(key, 2, 3) }
            assertEquals(emptyList<Item>(), withTimeout(4_000) { first.await() })
            val second = h.scope.async { h.repository.expandFold(key, 5, 6) }
            pump(h.frames) { second.isCompleted }
            assertEquals(listOf(5, 6), second.await().map { it.seq })
            assertEquals(listOf(5), ranges)
            val retry = h.scope.async { h.repository.expandFold(key, 2, 3) }
            pump(h.frames) { retry.isCompleted }
            assertEquals(listOf(2, 3), retry.await().map { it.seq })
            assertEquals(listOf(5, 2), ranges)
        }
    }

    @Test fun continuationTimeoutIsNonThrowingAndCancellationReleasesTheFold() = runBlocking {
        Harness(listOf(fold(2, 4))).use { h ->
            h.open()
            val ranges = mutableListOf<Int>()
            h.transport.fetch = { query ->
                ranges += requireNotNull(query.fromSeq)
                ItemsResponse(listOf(item(2)), history = "h", more = true)
            }
            val first = h.scope.async { h.repository.expandFold(key, 2, 4) }
            // OpenFold's applied Ack can need a later frame in Release; bound the wait for its first HTTP page.
            pump(h.frames) { ranges == listOf(2) }
            assertEquals(listOf(2), ranges)
            assertEquals(emptyList<Item>(), withTimeout(4_000) { first.await() })
            val cancelled = h.scope.async { h.repository.expandFold(key, 2, 4) }
            cancelled.cancelAndJoin()
            h.transport.fetch = { query ->
                assertEquals(2, query.fromSeq)
                ItemsResponse(listOf(item(2), item(3), item(4)), history = "h")
            }
            val retry = h.scope.async { h.repository.expandFold(key, 2, 4) }
            pump(h.frames) { retry.isCompleted }
            assertEquals(listOf(2, 3, 4), retry.await().map { it.seq })
        }
    }

    @Test fun differentFoldsFetchInParallelAndLateCancelledRangesCannotReplaceThem() = runBlocking {
        Harness(listOf(fold(2), fold(5))).use { h ->
            h.open()
            val firstPage = CompletableDeferred<ItemsResponse>()
            val ranges = mutableListOf<Int>()
            h.transport.fetch = { query ->
                ranges += requireNotNull(query.fromSeq)
                if (query.fromSeq == 2) firstPage.await()
                else ItemsResponse(listOf(item(5), item(6)), history = "h")
            }
            val first = h.scope.async { h.repository.expandFold(key, 2, 3) }
            val second = h.scope.async { h.repository.expandFold(key, 5, 6) }
            pump(h.frames) { second.isCompleted }
            assertEquals(listOf(2, 5), ranges)
            assertFalse(first.isCompleted)
            assertEquals(listOf(5, 6), second.await().map { it.seq })
            first.cancelAndJoin()
            // A late response for the cancelled fold is a core no-op, not work for the other fold.
            h.adapter.accept(ContinuityAdapter.Input.Range(listOf(item(2), item(3)), foldId = fold(2).id), 99)
            h.frames.run()
            assertTrue(h.adapter.state.items.any { it.id == fold(2).id })
            assertEquals(listOf(2, 5, 6), h.adapter.state.items.map { it.seq })
        }
    }

    @Test fun aChangedFoldRefetchesItsOwnBoundsAndDiscardsTheOldRange() = runBlocking {
        Harness(listOf(fold(2))).use { h ->
            h.open()
            val oldPage = CompletableDeferred<ItemsResponse>()
            val queries = mutableListOf<Pair<Int?, Int?>>()
            h.transport.fetch = { query ->
                queries += query.fromSeq to query.throughSeq
                if (query.throughSeq == 3) oldPage.await()
                else ItemsResponse(listOf(item(2), item(3), item(4)), history = "h")
            }
            val opening = h.scope.async { h.repository.expandFold(key, 2, 3) }
            h.frames.run()
            assertEquals(listOf(2 to 3), queries)
            h.adapter.accept(ContinuityAdapter.Input.Push(ContinuityAdapter.Batch(listOf(fold(2, 4)), "h", 3, 4)))
            h.frames.run()
            oldPage.complete(ItemsResponse(listOf(item(2).copy(text = "stale"), item(3)), history = "h"))
            pump(h.frames) { opening.isCompleted }
            assertEquals(listOf(2 to 3, 2 to 4), queries)
            assertEquals(listOf(item(2), item(3), item(4)), opening.await())
        }
    }

    @Test fun aPushedFoldMemberDoesNotDiscardDownloadedExpansionItems() = runBlocking {
        Harness(listOf(item(1), fold(2), item(4))).use { h ->
            h.open()
            val held = CompletableDeferred<ItemsResponse>()
            h.transport.fetch = { held.await() }
            val expansion = h.scope.async { h.repository.expandFold(key, 2, 3) }
            h.frames.run()
            // The push replaces the fold before the range response arrives, as in the synchronous parity test.
            h.adapter.accept(ContinuityAdapter.Input.Push(ContinuityAdapter.Batch(listOf(item(3)), "h", 3, 3)))
            h.frames.run()
            held.complete(ItemsResponse(listOf(item(2), item(3)), history = "h"))
            pump(h.frames) { expansion.isCompleted }
            assertEquals(listOf(item(2), item(3)), expansion.await())
        }
    }

    @Test fun suspendingInputDoesNotHoldTheAppliedStateLock() = runBlocking {
        Harness(listOf(fold(2))).use { h ->
            h.open()
            val blocked = CompletableDeferred<Unit>()
            val entered = CompletableDeferred<Unit>()
            h.beforeAccept = { input ->
                if (input == ContinuityAdapter.Input.HubChanged) { entered.complete(Unit); blocked.await() }
            }
            h.connection.events.emit(RevsEvent(key.machineId, mapOf(key.sessionId to 2)))
            entered.await()
            // An independent core patch must publish while repository input dispatch is suspended.
            h.adapter.accept(ContinuityAdapter.Input.Push(ContinuityAdapter.Batch(listOf(item(4)), "h", 3, 4)))
            h.frames.run()
            assertTrue(h.snapshot.get().items.any { it.seq == 4 })
            blocked.complete(Unit)
            Unit
        }
    }

    private inner class Harness(initial: List<Item>, hasMore: Boolean = false) : AutoCloseable {
        val frames = Frames()
        val driver = RustContinuityDriver(frames)
        val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
        val connection = FakeConnection()
        val transport = FakeChatTransport().also { t -> t.fetch = {
            ItemsResponse(initial, history = "h", toSeq = initial.maxOf { it.fold?.throughSeq ?: it.seq }, hasMore = hasMore)
        } }
        val snapshot = AtomicReference(ChatSnapshot())
        lateinit var adapter: RustContinuityAdapter
        var beforeAccept: suspend (ContinuityAdapter.Input) -> Unit = {}
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat()), FakeSessions(), scope,
            continuityFactory = { chat, applied ->
                RustContinuityAdapter(chat, driver, applied).also { adapter = it }.let { real ->
                    object : ContinuityAdapter by real {
                        override suspend fun accept(input: ContinuityAdapter.Input, operation: Long?): ContinuityAdapter.Applied? {
                            beforeAccept(input)
                            return real.accept(input, operation)
                        }
                    }
                }
            })
        suspend fun open() {
            scope.launch { repository.observe(key).collect { snapshot.set(it) } }
            pump(frames) { snapshot.get().verified }
            frames.run()
        }
        override fun close() { scope.cancel(); driver.close() }
    }

    private suspend fun pump(frames: Frames, done: () -> Boolean) = withTimeout(5_000) {
        while (!done()) { frames.run(); delay(1) }
    }
    private class Frames : ContinuityFrameScheduler {
        private val frames = ConcurrentLinkedQueue<() -> Unit>()
        override fun afterFirstFrame(start: () -> Unit) = start()
        override fun schedule(frame: () -> Unit) { frames.add(frame) }
        override suspend fun submitted(core: RustCore) { check(core.flush(5_000)) }
        fun run() { repeat(frames.size) { frames.poll()?.invoke() } }
    }
}
