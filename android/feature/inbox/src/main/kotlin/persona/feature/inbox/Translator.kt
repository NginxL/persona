package persona.feature.inbox

import androidx.compose.runtime.staticCompositionLocalOf
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import persona.core.contracts.api.MailTranslation
import persona.core.contracts.api.TranslationPrefs
import persona.core.contracts.api.TranslationTransport
import persona.core.domain.translate.LanguageGuess
import persona.core.inbox.*

/** Settings › Translation for the inbox; null where nothing is translated (demo mode, previews). */
val LocalTranslator = staticCompositionLocalOf<Translator?> { null }

/**
 * The translations the app has (docs/android/android.md "Translation", the iOS Translator): Feishu messages asked for while they show
 * (batched, [batch] at a time), mails by message. Kept for this run; the hub keeps its own copy, so the next is quick.
 * [transport] is the selected hub's (null while there is none).
 */
class Translator(
    val prefs: StateFlow<TranslationPrefs>,
    private val transport: () -> TranslationTransport?,
    private val scope: CoroutineScope,
    private val batchDelayMillis: Long = 300,
    private val batch: Int = 30,
) {
    private val done = MutableStateFlow<Map<String, String>>(emptyMap())
    /** Short texts translated so far, by [key]; rows read [translation] and redraw when this changes. */
    val texts: StateFlow<Map<String, String>> = done.asStateFlow()

    private val lock = Mutex()
    private val queued = LinkedHashMap<String, Pair<String, String>>()
    private val failed = HashSet<String>()
    private val foreign = HashMap<String, Boolean>()
    private val mails = HashMap<String, MailTranslation>()
    private var flushing: Job? = null

    internal fun key(text: String, target: String) = target + "\u0000" + text

    /** Whether [text] needs translating into [target], remembered per text (rows recompose often). */
    fun isForeign(text: String, target: String): Boolean = synchronized(foreign) {
        val k = key(text, target)
        foreign[k] ?: LanguageGuess.isForeign(text, target).also {
            if (foreign.size > 2000) foreign.clear()
            foreign[k] = it
        }
    }

    fun translation(text: String, target: String): String? = done.value[key(text, target)]

    /** Asks for a short text's translation; texts asked for within [batchDelayMillis] go in one request. */
    fun request(text: String, target: String) {
        val k = key(text, target)
        scope.launch {
            lock.withLock {
                if (k in done.value || k in queued || k in failed) return@launch
                queued[k] = text to target
                if (flushing == null) flushing = scope.launch {
                    try { delay(batchDelayMillis); flush() }
                    finally { if (coroutineContext[Job]?.isCancelled == true) lock.withLock { flushing = null } }
                }
            }
        }
    }

    private suspend fun flush() {
        while (true) {
            val picked = lock.withLock {
                // Done: say so under the lock, so a text queued from here on starts the next flush.
                val target = queued.values.firstOrNull()?.second ?: run { flushing = null; return }
                queued.entries.filter { it.value.second == target }.take(batch).map { it.key to it.value }
                    .also { list -> list.forEach { queued.remove(it.first) } }
            }
            val target = picked.first().second.second
            // No hub yet (starting, switching groups): not a failure, the next time the text shows asks again.
            val api = transport() ?: continue
            val out = try { api.translate(picked.map { it.second.first }, target) }
            catch (e: CancellationException) { throw e }
            catch (_: Exception) { null }
            lock.withLock {
                // Not again this run when it failed: the original stays readable.
                if (out == null) failed += picked.map { it.first }
                else done.update { now ->
                    now + picked.mapIndexedNotNull { i, (k, _) -> out.getOrNull(i)?.takeIf(String::isNotBlank)?.let { k to it } }
                }
                if (out != null) picked.forEachIndexed { i, (k, _) -> if (out.getOrNull(i).isNullOrBlank()) failed += k }
            }
        }
    }

    /** A mail's translation, asked once per message and target (a failure can be asked again). */
    suspend fun mail(account: String, uid: Long, mailbox: String, target: String): MailTranslation {
        val k = listOf(account, mailbox, uid.toString(), target).joinToString("\u0000")
        lock.withLock { mails[k] }?.let { return it }
        val api = transport() ?: throw IllegalStateException("no hub")
        return api.translateMail(account, uid, mailbox, target).also { lock.withLock { mails[k] = it } }
    }
}
