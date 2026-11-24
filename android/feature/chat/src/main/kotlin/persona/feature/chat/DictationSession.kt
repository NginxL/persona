package persona.feature.chat

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.yield

/** One user's tap or hold can span more than one phrase from the phone's recognizer. */
internal class DictationSession(
    private val scope: CoroutineScope,
    private val open: (Listener) -> Input,
    private val hasPermission: () -> Boolean,
    private val askPermission: ((Boolean) -> Unit) -> Unit,
    private val keep: (String) -> Unit,
    private val error: (permission: Boolean) -> Unit,
) {
    interface Input { fun start(); fun stop(); fun close() }
    interface Listener { fun partial(text: String); fun result(text: String); fun failed(quiet: Boolean) }
    private enum class Mode { Tap, Hold, Finishing }
    private var mode by mutableStateOf<Mode?>(null)
    private var requested: Mode? = null
    private var input: Input? = null
    private var generation = 0
    private var phrases = ""
    var heard by mutableStateOf("")
        private set
    val listening get() = mode == Mode.Tap || mode == Mode.Hold
    val holding get() = mode == Mode.Hold
    val active get() = mode != null
    private var finalTimeout: Job? = null
    private var restart: Job? = null

    fun tap() {
        when {
            listening -> stop()
            active -> return // The service still owes the previous recording its final words.
            requested != null -> requested = null
            else -> begin(Mode.Tap)
        }
    }

    fun stopListening() {
        requested = null
        if (listening) stop()
    }

    fun cover() {
        requested = null
        // Once the mic has stopped, its outstanding final still belongs to this draft.
        if (listening) finish(true)
    }

    fun hold() { if (!active && requested == null) begin(Mode.Hold) }

    fun release(cancel: Boolean) {
        if (requested == Mode.Hold) requested = null
        if (holding) { if (cancel) finish(false) else stop() }
    }

    private fun begin(next: Mode) {
        if (hasPermission()) start(next) else {
            requested = next
            askPermission(::permission)
        }
    }

    fun permission(granted: Boolean) {
        val next = requested
        requested = null
        if (next != null) {
            if (granted) start(next) else error(true)
        }
    }

    private fun start(next: Mode) {
        val own = ++generation
        mode = next
        val listener = object : Listener {
            override fun partial(text: String) {
                if (generation == own && text.isNotBlank()) heard = withDictation(phrases, text)
            }
            override fun result(text: String) {
                if (generation != own) return
                partial(text)
                phrases = heard
                if (listening) nextPhrase(own, quiet = text.isBlank()) else finish(true)
            }
            override fun failed(quiet: Boolean) {
                if (generation != own) return
                if (quiet && listening) {
                    phrases = heard
                    nextPhrase(own, quiet = true)
                } else {
                    val announce = !quiet && listening
                    finish(true)
                    if (announce) error(false)
                }
            }
        }
        try { input = open(listener); startPhrase() }
        catch (_: RuntimeException) { listener.failed(false) }
    }

    private fun nextPhrase(own: Int, quiet: Boolean) {
        // A pause can end the system recognizer's phrase before the user stops the mic. Let the callback return first.
        restart?.cancel()
        restart = scope.launch {
            if (quiet) delay(400) else yield()
            restart = null
            if (generation == own && listening) startPhrase()
        }
    }

    private fun startPhrase() {
        try { input?.start() }
        catch (_: RuntimeException) { finish(true); error(false) }
    }

    private fun stop() {
        if (restart != null) { finish(true); return }
        mode = Mode.Finishing
        // Some services never return a final result after stopListening. Keep the latest partial in that case.
        finalTimeout = scope.launch { delay(1500); finish(true) }
        try { input?.stop() } catch (_: RuntimeException) { finish(true) }
    }

    fun finish(keepWords: Boolean) {
        requested = null
        ++generation
        restart?.cancel()
        restart = null
        finalTimeout?.cancel()
        finalTimeout = null
        val words = heard
        val previous = input
        mode = null
        input = null
        heard = ""
        phrases = ""
        val _ = runCatching { previous?.close() }
        if (keepWords && words.isNotBlank()) keep(words)
    }
}
