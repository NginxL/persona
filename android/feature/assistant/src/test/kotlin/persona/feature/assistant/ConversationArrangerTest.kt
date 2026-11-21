package persona.feature.assistant

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceUntilIdle
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ConversationArrangerTest {
    @Test fun payloadKeepsUnknownSavedIdsAfterLoadedAndNewRows() {
        assertEquals(
            listOf("b", "a", "new", "not-loaded", "also-unknown"),
            conversationOrderPayload(
                activityIds = listOf("a", "new", "b"),
                arrangedIds = listOf("b", "a"),
                savedOrder = listOf("not-loaded", "a", "also-unknown", "not-loaded"),
            ),
        )
    }

    @Test fun moveIsOptimisticAndSerializesRapidWrites() = runTest {
        val writes = GatedWrites()
        val snapshots = mutableListOf<List<String>?>()
        val arranger = arranger(writes, snapshots)
        arranger.start(listOf("a", "b", "unknown"))

        val first = async { arranger.moveDown("a", listOf("a", "b")) }
        runCurrent()
        val second = async { arranger.moveDown("b", listOf("a", "b")) }
        runCurrent()

        assertEquals(listOf("a", "b", "unknown"), arranger.ui.value.order)
        assertEquals(listOf("a", "b", "unknown"), snapshots.last())
        assertEquals(listOf(listOf("b", "a", "unknown")), writes.requests.map { it.order })
        writes.requests[0].answer.complete(listOf("b", "a", "unknown"))
        runCurrent()
        assertEquals(listOf(listOf("b", "a", "unknown"), listOf("a", "b", "unknown")), writes.requests.map { it.order })
        // The first response is stale: the second optimistic move still stands.
        assertEquals(listOf("a", "b", "unknown"), arranger.ui.value.order)
        writes.requests[1].answer.complete(listOf("a", "b", "unknown"))
        advanceUntilIdle()
        first.await(); second.await()
        assertEquals(0, arranger.ui.value.pending)
        assertEquals(listOf("a", "b", "unknown"), arranger.ui.value.order)
    }

    @Test fun latestFailureRollsBackToLatestConfirmedOrder() = runTest {
        val writes = GatedWrites()
        val snapshots = mutableListOf<List<String>?>()
        val arranger = arranger(writes, snapshots)
        val failures = mutableListOf<Exception>()
        val collecting = launch { arranger.events.collect { if (it is ConversationArrangeEvent.Failed) failures += it.error } }
        arranger.start(listOf("a", "b", "unknown"))

        val first = async { arranger.moveDown("a", listOf("a", "b")) }
        runCurrent()
        val second = async { arranger.moveDown("b", listOf("a", "b")) }
        runCurrent()
        writes.requests[0].answer.complete(listOf("b", "a", "unknown"))
        runCurrent()
        writes.requests[1].answer.completeExceptionally(IllegalStateException("offline"))
        advanceUntilIdle()
        first.await(); second.await()

        assertEquals(listOf("b", "a", "unknown"), arranger.ui.value.order)
        assertEquals(listOf("b", "a", "unknown"), snapshots.last())
        assertEquals(1, failures.size)
        assertEquals("offline", failures.single().message)
        collecting.cancel()
    }

    @Test fun missingAcknowledgmentRollsBackMovesButAcceptsReset() = runTest {
        val snapshots = mutableListOf<List<String>?>()
        val arranger = ConversationArranger(prepare = { { snapshots += it } }, write = { null })
        val failures = mutableListOf<Exception>()
        val collecting = launch { arranger.events.collect { if (it is ConversationArrangeEvent.Failed) failures += it.error } }
        runCurrent()
        arranger.start(listOf("a", "b", "unknown"))

        arranger.moveDown("a", listOf("a", "b"))
        runCurrent()
        assertEquals(listOf("a", "b", "unknown"), arranger.ui.value.order)
        assertEquals(listOf("a", "b", "unknown"), snapshots.last())
        assertEquals(0, arranger.ui.value.pending)
        assertEquals(1, failures.size)

        arranger.reset(listOf("a", "b"))
        runCurrent()
        assertEquals(emptyList<String>(), arranger.ui.value.order)
        assertEquals(emptyList<String>(), snapshots.last())
        assertEquals(0, arranger.ui.value.pending)
        assertEquals(1, failures.size)
        collecting.cancel()
    }

    @Test fun staleFailureDoesNotClobberANewerMove() = runTest {
        val writes = GatedWrites()
        val snapshots = mutableListOf<List<String>?>()
        val arranger = arranger(writes, snapshots)
        val failures = mutableListOf<Exception>()
        val collecting = launch { arranger.events.collect { if (it is ConversationArrangeEvent.Failed) failures += it.error } }
        arranger.start(listOf("a", "b", "unknown"))

        val first = async { arranger.moveDown("a", listOf("a", "b")) }
        runCurrent()
        val second = async { arranger.moveDown("b", listOf("a", "b")) }
        runCurrent()
        writes.requests[0].answer.completeExceptionally(IllegalStateException("old failure"))
        runCurrent()
        assertEquals(listOf("a", "b", "unknown"), arranger.ui.value.order)
        assertTrue(failures.isEmpty())
        writes.requests[1].answer.complete(listOf("a", "b", "unknown"))
        advanceUntilIdle()
        first.await(); second.await()
        assertEquals(listOf("a", "b", "unknown"), arranger.ui.value.order)
        collecting.cancel()
    }

    @Test fun resetOffersUndoImmediatelyAndUndoKeepsUnknownAndNewIds() = runTest {
        val writes = GatedWrites()
        val snapshots = mutableListOf<List<String>?>()
        val arranger = arranger(writes, snapshots)
        var resetRevision: Long? = null
        val collecting = launch {
            arranger.events.collect { if (it is ConversationArrangeEvent.Reset) resetRevision = it.revision }
        }
        arranger.start(listOf("b", "a", "unknown"))

        val reset = async { arranger.reset(listOf("a", "b", "new")) }
        runCurrent()
        assertEquals(emptyList<String>(), arranger.ui.value.order)
        val revision = resetRevision
        assertTrue(revision != null)
        val undo = async { arranger.undoReset(requireNotNull(revision), listOf("a", "b", "new")) }
        runCurrent()
        assertEquals(listOf("b", "a", "new", "unknown"), arranger.ui.value.order)
        writes.requests[0].answer.complete(emptyList())
        runCurrent()
        assertEquals(listOf(emptyList(), listOf("b", "a", "new", "unknown")), writes.requests.map { it.order })
        writes.requests[1].answer.complete(listOf("b", "a", "new", "unknown"))
        advanceUntilIdle()
        reset.await(); undo.await()
        assertEquals(listOf("b", "a", "new", "unknown"), arranger.ui.value.order)
        collecting.cancel()
    }

    @Test fun oldResetCannotUndoOverANewerMove() = runTest {
        val writes = GatedWrites()
        val snapshots = mutableListOf<List<String>?>()
        val arranger = arranger(writes, snapshots)
        var resetRevision: Long? = null
        val collecting = launch {
            arranger.events.collect { if (it is ConversationArrangeEvent.Reset) resetRevision = it.revision }
        }
        arranger.start(listOf("a", "b"))
        val reset = async { arranger.reset(listOf("a", "b")) }
        runCurrent()
        val move = async { arranger.moveDown("a", listOf("a", "b")) }
        runCurrent()
        arranger.undoReset(requireNotNull(resetRevision), listOf("a", "b"))
        assertEquals(listOf("b", "a"), arranger.ui.value.order)
        writes.requests[0].answer.complete(emptyList())
        runCurrent()
        writes.requests[1].answer.complete(listOf("b", "a"))
        advanceUntilIdle()
        reset.await(); move.await()
        assertEquals(2, writes.requests.size)
        assertEquals(listOf("b", "a"), arranger.ui.value.order)
        collecting.cancel()
    }

    @Test fun dragCanCancelOrDropAndMoveButtonsRespectBoundaries() = runTest {
        val writes = GatedWrites()
        val snapshots = mutableListOf<List<String>?>()
        val arranger = arranger(writes, snapshots)
        arranger.start(listOf("a", "b", "unknown"))
        arranger.moveUp("a", listOf("a", "b"))
        arranger.startDrag("a", listOf("a", "b"))
        arranger.dragTo(1)
        assertEquals(listOf("b", "a"), arranger.ui.value.drag?.order)
        arranger.cancelDrag()
        assertNull(arranger.ui.value.drag)
        assertTrue(writes.requests.isEmpty())

        arranger.startDrag("a", listOf("a", "b"))
        arranger.dragTo(1)
        val drop = async { arranger.endDrag(listOf("a", "b")) }
        runCurrent()
        assertEquals(listOf("b", "a", "unknown"), arranger.ui.value.order)
        assertEquals(listOf("b", "a", "unknown"), writes.requests.single().order)
        writes.requests.single().answer.complete(listOf("b", "a", "unknown"))
        advanceUntilIdle()
        drop.await()
        assertNull(arranger.ui.value.drag)
    }

    @Test fun suspendingStaleSnapshotUpdateCannotRestoreItsOldOrder() = runTest {
        val snapshots = mutableListOf<List<String>?>()
        val confirmedSnapshot = CompletableDeferred<Unit>()
        var calls = 0
        val arranger = ConversationArranger(
            prepare = {
                {
                    calls++
                    if (calls == 2) confirmedSnapshot.await()
                    snapshots += it
                }
            },
            write = { it },
        )
        arranger.start(listOf("a", "b"))
        val first = async { arranger.moveDown("a", listOf("a", "b")) }
        runCurrent()
        assertEquals(2, calls) // The first write returned; its confirmed snapshot update is suspended.
        val second = async { arranger.moveDown("b", listOf("a", "b")) }
        runCurrent()
        assertEquals(listOf("a", "b"), arranger.ui.value.order)
        confirmedSnapshot.complete(Unit)
        advanceUntilIdle()
        first.await(); second.await()
        assertEquals(listOf("a", "b"), arranger.ui.value.order)
        assertEquals(listOf("a", "b"), snapshots.last())
    }

    @Test fun dragUsesRowMiddlesAndRejectsStaleLayouts() {
        val rows = listOf(
            ConversationRowSpan("a", 0, 100), ConversationRowSpan("b", 100, 100), ConversationRowSpan("c", 200, 100),
        )
        val order = listOf("a", "b", "c")
        assertNull(conversationDragMove(order, rows, "a", 0f))
        assertEquals(1, conversationDragMove(order, rows, "a", 110f))
        assertEquals(2, conversationDragMove(order, rows, "a", 210f))
        assertNull(conversationDragMove(order, rows.reversed(), "a", 110f))
    }

    @Test fun edgeScrollOnlyRunsTowardRowsStillOffscreen() {
        assertEquals(400f, conversationEdgeScrollVelocity(625f, 675f, 0f, 700f, 50f, 800f, 1, 0f, 800f))
        assertEquals(-400f, conversationEdgeScrollVelocity(25f, 125f, 0f, 700f, 50f, 800f, -1, -100f, 800f))
        assertEquals(0f, conversationEdgeScrollVelocity(650f, 750f, 0f, 700f, 50f, 800f, 1, 0f, 700f))
        assertEquals(0f, conversationEdgeScrollVelocity(650f, 750f, 0f, 700f, 50f, 800f, -1, 0f, 800f))
    }

    private fun arranger(writes: GatedWrites, snapshots: MutableList<List<String>?>) = ConversationArranger(
        prepare = { { snapshots += it } },
        write = { order ->
            val answer = CompletableDeferred<List<String>>()
            writes.requests += GatedWrite(order, answer)
            answer.await()
        },
    )

    private class GatedWrites {
        val requests = mutableListOf<GatedWrite>()
    }

    private data class GatedWrite(val order: List<String>, val answer: CompletableDeferred<List<String>>)
}
