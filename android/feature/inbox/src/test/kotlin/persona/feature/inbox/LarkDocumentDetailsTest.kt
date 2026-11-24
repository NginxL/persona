package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.InboxAssistantTransport
import java.time.Instant
import persona.core.inbox.*

/** What the native reader shows around a document (G187) and the cache that makes a second open instant (G193). */
class LarkDocumentDetailsTest {
    private fun json(text: String) = JsonCodec.parseToJsonElement(text).jsonObject

    @Test fun detailsReadOwnerEditorTimeCountsAndAReadySummary() {
        val details = parseLarkDocumentDetails(
            json("""{"owner":{"id":"u1","name":"示例同事"},"updatedAt":"2026-11-01T08:00:00Z","views":12,"visitors":5,"likes":0,"comments":3}"""),
            json("""{"editedAt":"2026-11-04T09:30:00+08:00","editorName":"另一位同事"}"""),
            json("""{"state":"ready","summary":" 讲了排期和风险。 "}"""),
        )
        assertEquals("示例同事", details.owner)
        assertEquals("另一位同事", details.editor)
        // The header's edit time wins over the info's.
        assertEquals(Instant.parse("2026-11-04T01:30:00Z"), details.updatedAt)
        assertEquals(12L, details.views); assertEquals(5L, details.visitors); assertEquals(0L, details.likes); assertEquals(3L, details.comments)
        assertEquals("讲了排期和风险。", details.summary)
    }

    @Test fun theOwnerAsEditorIsNotRepeatedAndAPendingSummaryIsNotShown() {
        val details = parseLarkDocumentDetails(json("""{"owner":{"name":"示例同事"},"updatedAt":1730700000}"""),
            json("""{"editorName":"示例同事"}"""), json("""{"state":"generating","summary":"…"}"""))
        assertEquals("", details.editor)
        assertEquals(Instant.ofEpochSecond(1730700000), details.updatedAt)
        assertEquals("", details.summary)
        assertTrue(parseLarkDocumentDetails(null, null, null).isEmpty)
        assertEquals(Instant.ofEpochMilli(1730700000123), larkInstant(JsonCodec.parseToJsonElement("1730700000123")))
    }

    @Test fun theOutlineIsTheHeadingsWithTheirBlockIndex() {
        val doc = LarkDocument("排期", listOf(LarkDocumentBlock("heading", "背景", 1), LarkDocumentBlock("paragraph", "正文"),
            LarkDocumentBlock("heading", " ", 2), LarkDocumentBlock("heading", "风险", 2)), partial = false, copyAllowed = true)
        assertEquals(listOf(0 to "背景", 3 to "风险"), doc.outline().map { it.first to it.second.text })
    }

    private class Docs(override val scope: String = "pairing") : InboxAssistantTransport {
        val reads = mutableListOf<String>()
        val extras = mutableListOf<String>()
        var fail = false
        var gate: CompletableDeferred<Unit>? = null
        override suspend fun readDocument(url: String): JsonObject {
            reads += url
            gate?.await()
            if (fail) error("offline")
            return buildJsonObject { put("title", url.substringAfterLast('/')); putJsonObject("content") { put("type", "doc") } }
        }
        override suspend fun documentExtra(path: String, url: String): JsonObject {
            extras += path
            if (path == "header") error("no header")
            return if (path == "reader/info") buildJsonObject { put("views", 7) } else buildJsonObject { put("state", "ready"); put("summary", "摘要") }
        }
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun assistant() = error("unused")
        override suspend fun createConversation(main: Boolean) = error("unused")
        override suspend fun pins() = error("unused")
        override suspend fun mutes() = error("unused")
        override suspend fun media(ref: String) = error("unused")
    }

    @Test fun aDocumentReadOnceIsServedFromTheCacheUntilAFreshRead() = runBlocking {
        val docs = Docs()
        val source = LarkContentSource(docs)
        val cache = LarkDocumentCache(limit = 2, scope = CoroutineScope(Dispatchers.Default))
        val first = cache.load(source, "https://example.feishu.cn/docx/a")
        assertEquals("a", first.await().title)
        // The cache reads only the text: the details are read on every open, never with it.
        assertTrue(docs.extras.isEmpty())
        assertSame(first, cache.load(source, "https://example.feishu.cn/docx/a"))
        assertEquals(1, docs.reads.size)
        val fresh = cache.load(source, "https://example.feishu.cn/docx/a", fresh = true)
        assertNotSame(first, fresh)
        fresh.await()
        assertEquals(2, docs.reads.size)
        // Another pairing reads for itself.
        cache.load(LarkContentSource(Docs("other")), "https://example.feishu.cn/docx/a").await()
        assertTrue(cache.contains(source, "https://example.feishu.cn/docx/a"))
    }

    @Test fun theCacheKeepsTheMostRecentlyUsedAndForgetsFailures() = runBlocking {
        val docs = Docs()
        val source = LarkContentSource(docs)
        val cache = LarkDocumentCache(limit = 2, scope = CoroutineScope(Dispatchers.Default))
        for (name in listOf("a", "b")) cache.load(source, "https://example.feishu.cn/docx/$name").await()
        cache.load(source, "https://example.feishu.cn/docx/a") // a is used again: b is the oldest now
        cache.load(source, "https://example.feishu.cn/docx/c").await()
        assertTrue(cache.contains(source, "https://example.feishu.cn/docx/a"))
        assertFalse(cache.contains(source, "https://example.feishu.cn/docx/b"))

        docs.fail = true
        val failed = cache.load(source, "https://example.feishu.cn/docx/d")
        try { failed.await(); fail("The read fails") } catch (_: IllegalStateException) { }
        failed.join()
        assertFalse(cache.contains(source, "https://example.feishu.cn/docx/d"))
    }

    @Test fun aPrefetchStillRunningIsTheReadTheReaderWaitsFor() = runBlocking {
        val docs = Docs().apply { gate = CompletableDeferred() }
        val source = LarkContentSource(docs)
        val cache = LarkDocumentCache(scope = CoroutineScope(Dispatchers.Default))
        cache.prefetch(source, "https://example.feishu.cn/docx/a")
        val read = cache.load(source, "https://example.feishu.cn/docx/a")
        docs.gate?.complete(Unit)
        assertEquals("a", read.await().title)
        assertEquals(1, docs.reads.size)
        // A read ahead fetches no details: nobody may open it.
        assertTrue(docs.extras.isEmpty())
    }

    @Test fun theDetailsLeaveOutAPartThatFails() = runBlocking {
        val docs = Docs()
        assertEquals(LarkDocumentDetails(views = 7, summary = "摘要"), LarkContentSource(docs).details("https://example.feishu.cn/docx/a"))
        assertEquals(setOf("reader/info", "header", "ai-summary"), docs.extras.toSet())
    }

    @Test fun eachPairingKeepsItsOwnLastDocuments() = runBlocking {
        val one = LarkContentSource(Docs("one"))
        val two = LarkContentSource(Docs("two"))
        val cache = LarkDocumentCache(limit = 2, scope = CoroutineScope(Dispatchers.Default))
        for (name in listOf("a", "b")) cache.load(one, "https://example.feishu.cn/docx/$name").await()
        // Reading in another pairing evicts nothing of the first one's.
        for (name in listOf("c", "d", "e")) cache.load(two, "https://example.feishu.cn/docx/$name").await()
        assertTrue(cache.contains(one, "https://example.feishu.cn/docx/a"))
        assertTrue(cache.contains(one, "https://example.feishu.cn/docx/b"))
        assertFalse(cache.contains(two, "https://example.feishu.cn/docx/c"))
        assertTrue(cache.contains(two, "https://example.feishu.cn/docx/e"))
    }
}
