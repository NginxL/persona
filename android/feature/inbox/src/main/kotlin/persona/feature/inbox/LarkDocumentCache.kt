package persona.feature.inbox

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.serialization.json.JsonObject
import persona.core.inbox.*

/**
 * The documents read lately, so one opened again (or read ahead while its link waited in a reply) shows at once, as
 * on the web (`feishu-doc.ts` loadDoc): the last [limit] for each pairing and manager ([LarkContentSource.scope]: the same
 * link read by two managers is two documents), the most recently used kept. Only the text
 * is kept: what is shown around it (counts, summary) is read on every open, as on the web and iOS. A failed read is
 * not kept; `fresh` reads again.
 */
class LarkDocumentCache(private val limit: Int = 12, private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Default)) {
    /** Per pairing and manager, its reads by URL, oldest use first. */
    private val pairings = HashMap<String, LinkedHashMap<String, Deferred<LarkDocument>>>()

    fun load(source: LarkContentSource, url: String, fresh: Boolean = false): Deferred<LarkDocument> = synchronized(this) {
        val reads = pairings.getOrPut(source.scope) { LinkedHashMap(16, 0.75f, true) }
        reads[url]?.takeIf { !fresh }?.let { return it }
        val read = scope.async { source.document(url) }
        reads[url] = read
        read.invokeOnCompletion { failure -> if (failure != null) synchronized(this) { if (reads[url] === read) reads.remove(url) } }
        while (reads.size > limit) reads.remove(reads.keys.first())
        read
    }

    /** Starts reading [url] unless it is read already (a link that may be tapped soon); only the text, no extras. */
    fun prefetch(source: LarkContentSource, url: String) { load(source, url) }

    fun contains(source: LarkContentSource, url: String): Boolean = synchronized(this) { pairings[source.scope]?.containsKey(url) == true }

    companion object {
        /** The app's one cache: the reader in the inbox and the conversation's link card share it. */
        val shared = LarkDocumentCache()
    }
}

/** The three extras at once; each one that fails is left out. */
internal suspend fun LarkContentSource.details(url: String): LarkDocumentDetails = coroutineScope {
    suspend fun extra(path: String): JsonObject? = try { read { documentExtra(path, url) } }
        catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { null }
    val info = async { extra("reader/info") }
    val header = async { extra("header") }
    val summary = async { extra("ai-summary") }
    parseLarkDocumentDetails(info.await(), header.await(), summary.await())
}
