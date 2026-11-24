package persona.core.designsystem

import persona.core.contracts.api.ConnectionState
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.channels.BufferOverflow
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.asSharedFlow
import kotlinx.coroutines.flow.map

/**
 * Folds repeated failure notices. [localize] turns an [OperationFeedback.text] into the app's language (the app
 * passes `{ it.resolve(context) }`).
 */
class FeedbackGate(
    private val connection: () -> ConnectionState,
    private val clock: () -> Long = { System.nanoTime() / 1_000_000 },
    private val localize: (UiText) -> String,
) : AppFeedback {
    private val queue = MutableSharedFlow<FeedbackNotice>(extraBufferCapacity = 1, onBufferOverflow = BufferOverflow.DROP_OLDEST)
    /** What to show, with its button ("Undo") when it has one. */
    val notices = queue.asSharedFlow()
    val messages = notices.map { it.message }
    private val lastOffline = mutableMapOf<String, Long>()
    private val lastNotices = mutableMapOf<String, Long>()

    @Synchronized
    override fun failure(error: Throwable, operation: Boolean): Boolean {
        if (error is CancellationException || !operation) return true
        val feedback = operationFeedback(error)
        val message = localize(feedback.text)
        val offline = connection() != ConnectionState.Online
        if (offline) {
            val now = clock()
            if (lastOffline[message]?.let { now - it < 4_000 } == true) return true
            lastOffline[message] = now
        }
        // As iOS: a failure is an error toast; one that only waits for the hub is info with a nudge (Haptic.Attention).
        return if (offline) emit(message, Haptic.Attention, FeedbackStyle.Info) else emit(message, null, FeedbackStyle.Error)
    }

    @Synchronized
    override fun notice(message: String): Boolean = emit(message, null, FeedbackStyle.Info)

    @Synchronized
    override fun notice(message: String, style: FeedbackStyle): Boolean = emit(message, null, style)

    private fun emit(message: String, haptic: Haptic?, style: FeedbackStyle): Boolean {
        val now = clock()
        lastNotices.entries.removeAll { now - it.value >= 4_000 }
        if (lastNotices[message]?.let { now - it < 4_000 } == true) return true
        lastNotices[message] = now
        queue.tryEmit(FeedbackNotice(message, haptic = haptic, style = style))
        return true
    }

    /** Never folded into an earlier one: each carries its own "Undo". */
    @Synchronized
    override fun notice(message: String, action: FeedbackAction): Boolean = notice(message, action, FeedbackStyle.Info)

    @Synchronized
    override fun notice(message: String, action: FeedbackAction, style: FeedbackStyle): Boolean {
        queue.tryEmit(FeedbackNotice(message, action, style = style))
        return true
    }
}
