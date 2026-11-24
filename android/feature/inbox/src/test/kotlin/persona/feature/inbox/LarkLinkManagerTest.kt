package persona.feature.inbox

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonArray
import kotlinx.serialization.json.putJsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.RequestManager
import persona.core.inbox.*

/** Notes each read with the manager its request names: no [RequestManager] (null), the default's (""), or an id. */
internal class ManagerReads(override val scope: String = "pairing") : InboxAssistantTransport {
    private val notes = mutableListOf<Pair<String, String?>>()
    val named: List<Pair<String, String?>> get() = synchronized(notes) { notes.toList() }
    private suspend fun note(what: String): String? = currentCoroutineContext()[RequestManager]?.id.also { synchronized(notes) { notes += what to it } }
    override suspend fun readDocument(url: String): JsonObject {
        val manager = note("read")
        return buildJsonObject { put("title", manager ?: "unnamed"); putJsonObject("content") { put("type", "doc") } }
    }
    override suspend fun documentExtra(path: String, url: String): JsonObject { note(path); return buildJsonObject { } }
    override suspend fun documentMedia(ref: String): ByteArray { note("documentMedia"); return byteArrayOf(1) }
    override suspend fun media(ref: String): ByteArray { note("media"); return byteArrayOf(1) }
    override suspend fun documentComments(url: String): JsonObject { note("comments"); return buildJsonObject { putJsonArray("threads") { } } }
    override suspend fun writeDocumentComment(thread: String?, body: JsonObject): JsonObject { note("write"); return buildJsonObject { put("ok", true) } }
    override suspend fun resolveDocumentComment(thread: String, url: String, resolved: Boolean): JsonObject { note("resolve"); return buildJsonObject { put("ok", true) } }
    override suspend fun documentPeople(url: String, query: String): JsonObject { note("people"); return buildJsonObject { putJsonArray("members") { } } }
    override suspend fun inbox() = error("unused")
    override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
    override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
    override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
    override suspend fun assistant() = error("unused")
    override suspend fun createConversation(main: Boolean) = error("unused")
    override suspend fun pins() = error("unused")
    override suspend fun mutes() = error("unused")
}

/**
 * A document linked in another manager's chat or inbox item is read through that manager (docs/assistant/managers.md
 * "Clients"): every read the reader makes names it, the default's names none, and the same link read by two managers
 * is two cached documents.
 */
class LarkLinkManagerTest {
    private val url = "https://example.feishu.cn/docx/D1"

    @After fun reset() = AssistantManagerChoice.reset()

    private suspend fun LarkPart.Image.load() = (model as AuthenticatedLarkImage).read()

    @Test fun everyReadOfADocumentLinkedInAnotherManagersChatNamesThatManager() = runBlocking {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val reads = ManagerReads()
        val source = LarkContentSource(reads).forManager("m-2")
        val cache = LarkDocumentCache(scope = CoroutineScope(Dispatchers.Default))
        assertEquals("m-2", cache.load(source, url).await().title)
        source.details(url)
        source.image("/api/v1/lark/raw/documents/media/img1", document = true).load()
        source.image("/api/v1/lark/media/img2").load()
        val comments = LarkDocCommentsTransport(source)
        comments.threads(url)
        comments.write(null, buildJsonObject { put("url", url); put("content", "ok") })
        comments.resolve("t1", url, true)
        comments.people(url, "a")
        assertEquals(listOf("ai-summary", "comments", "documentMedia", "header", "media", "people", "read", "reader/info", "resolve", "write"),
            reads.named.map { it.first }.sorted())
        assertTrue(reads.named.toString(), reads.named.all { it.second == "m-2" })
    }

    @Test fun theDefaultManagerNamesNoneAndNoManagerLeavesTheReadsAsTheyWere() = runBlocking {
        // A roster whose default is not the hub's own assistant.
        AssistantManagerChoice.choose("hub", null, "m-1")
        val reads = ManagerReads()
        val plain = LarkContentSource(reads)
        assertSame(plain, plain.forManager(null))
        val default = plain.forManager("m-1")
        assertEquals("pairing", default.scope)
        assertEquals("pairing", plain.forManager("").scope)
        assertEquals("pairing#assistant", plain.forManager("assistant").scope)
        plain.document(url)
        default.document(url)
        default.image("/api/v1/lark/media/img").load()
        // No manager: nothing on the read at all (a page's pin still applies). The default's: "", which the client sends
        // as no manager, whatever another page pinned.
        assertEquals(listOf("read" to null, "read" to "", "media" to ""), reads.named)
    }

    @Test fun theSameLinkUnderTwoManagersIsTwoCachedDocumentsAndPictures() = runBlocking {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val reads = ManagerReads()
        val plain = LarkContentSource(reads)
        val theirs = plain.forManager("m-2")
        val cache = LarkDocumentCache(scope = CoroutineScope(Dispatchers.Default))
        assertEquals("unnamed", cache.load(plain, url).await().title)
        // A read ahead in m-2's chat does not answer from the default's copy, nor the other way round.
        cache.prefetch(theirs, url)
        assertEquals("m-2", cache.load(LarkContentSource(reads, "m-2"), url).await().title)
        assertEquals("unnamed", cache.load(plain, url).await().title)
        assertEquals(listOf("read" to null, "read" to "m-2"), reads.named)
        assertTrue(cache.contains(theirs, url))
        // The default's chat reads what the default read: one entry, as before managers.
        assertTrue(cache.contains(plain.forManager("assistant"), url))
        val ref = "/api/v1/lark/raw/documents/media/img"
        assertNotEquals(plain.image(ref, document = true).model, theirs.image(ref, document = true).model)
        assertEquals(plain.image(ref, document = true).model, plain.forManager("assistant").image(ref, document = true).model)
    }
}
