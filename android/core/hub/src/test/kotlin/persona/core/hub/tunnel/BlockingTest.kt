package persona.core.hub.tunnel

import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.delay
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.yield
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.SshHostKey
import persona.core.contracts.api.TunnelException
import persona.core.contracts.api.TunnelProblem
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executor
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

/** #3844: work handed to another thread for a caller that is gone by then is not started (a superseded start does not dial). */
class BlockingTest {
    /** Runs what it is given only when [drain] is called, so the order of cancel and pickup is fixed. */
    private class Queue : Executor {
        val tasks = ArrayDeque<Runnable>()
        override fun execute(command: Runnable) { tasks += command }
        fun drain() { while (tasks.isNotEmpty()) tasks.removeFirst().run() }
    }

    @Test fun aCallerCancelledBeforeThePickupNeverStartsTheWork() = runBlocking(Dispatchers.Unconfined) {
        val queue = Queue()
        var dialled = 0
        var interrupted = 0
        val call = async(start = CoroutineStart.UNDISPATCHED) { blocking({ interrupted++ }, executor = queue) { dialled++ } }
        call.cancel()
        queue.drain()
        assertEquals("the dial never ran", 0, dialled)
        assertEquals("the interrupt still runs", 1, interrupted)
        assertTrue(call.isCancelled)
    }

    @Test fun aWaitingCallerGetsTheResult() = runBlocking(Dispatchers.Unconfined) {
        val queue = Queue()
        val call = async(start = CoroutineStart.UNDISPATCHED) { blocking({}, executor = queue) { 42 } }
        queue.drain()
        yield()
        assertEquals(42, call.await())
    }

    @Test fun aFailureAfterCancellationIsStillHandled() = runBlocking(Dispatchers.Unconfined) {
        val executor = Executors.newSingleThreadExecutor()
        try {
            val started = CountDownLatch(1)
            val finish = CountDownLatch(1)
            var failed: Throwable? = null
            val call = async(start = CoroutineStart.UNDISPATCHED) {
                blocking({}, onFailure = { failed = it }, executor = executor) {
                    started.countDown()
                    finish.await()
                    throw TunnelException(TunnelProblem.HostKeyUnknown(SshHostKey("SHA256:new", "ED25519")))
                }
            }
            assertTrue(started.await(5, TimeUnit.SECONDS))
            call.cancel()
            finish.countDown()
            withTimeout(5000) { while (failed == null) delay(1) }
            assertEquals(TunnelProblem.HostKeyUnknown(SshHostKey("SHA256:new", "ED25519")), (failed as TunnelException).problem)
            assertTrue(call.isCancelled)
        } finally {
            executor.shutdownNow()
        }
    }

    @Test fun aLateSuccessIsNotReportedAsAFailure() = runBlocking(Dispatchers.Unconfined) {
        val executor = Executors.newSingleThreadExecutor()
        try {
            val started = CountDownLatch(1)
            val finish = CountDownLatch(1)
            var failed = 0
            var closed = 0
            val call = async(start = CoroutineStart.UNDISPATCHED) {
                blocking({}, onLate = { closed++ }, onFailure = { failed++ }, executor = executor) {
                    started.countDown()
                    finish.await()
                    42
                }
            }
            assertTrue(started.await(5, TimeUnit.SECONDS))
            call.cancel()
            finish.countDown()
            withTimeout(5000) { while (closed == 0) delay(1) }
            assertEquals(0, failed)
            assertTrue(call.isCancelled)
        } finally {
            executor.shutdownNow()
        }
    }
}
