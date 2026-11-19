package persona.core.hub.tunnel

import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.asExecutor
import kotlinx.coroutines.suspendCancellableCoroutine
import java.util.concurrent.Executor
import java.util.concurrent.atomic.AtomicBoolean

/**
 * Runs [block] (a dial, a login, a listen) on another thread, so the caller can be cancelled while it waits: [onCancel]
 * then interrupts it, and a result that comes after is handed to [onLate]. A caller cancelled before the thread picks the
 * block up never starts it (#3844): a superseded tunnel start must not dial the server, least of all one whose key
 * changed and is waiting for the user.
 */
internal suspend fun <T> blocking(
    onCancel: () -> Unit,
    onLate: (T) -> Unit = { (it as? AutoCloseable)?.close() },
    /** A late failure too: a cancelled caller still owns the socket's rejection and cleanup. */
    onFailure: (Throwable) -> Unit = {},
    executor: Executor = Dispatchers.IO.asExecutor(),
    block: () -> T,
): T = suspendCancellableCoroutine { continuation ->
    val late = AtomicBoolean(false)
    continuation.invokeOnCancellation {
        late.set(true)
        executor.execute(onCancel)
    }
    executor.execute {
        if (late.get() || !continuation.isActive) return@execute
        val result = runCatching(block)
        result.fold(
            { value ->
                if (late.get()) onLate(value)
                else continuation.resume(value) { _, value, _ -> onLate(value) }
            },
            { error ->
                onFailure(error)
                if (continuation.isActive) continuation.resumeWith(Result.failure(error))
            },
        )
    }
}
