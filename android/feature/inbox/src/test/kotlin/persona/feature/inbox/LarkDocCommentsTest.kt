package persona.feature.inbox

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.hub.HubError
import persona.core.hub.HubFailure

/** Comments in the Feishu document reader (#2964, iOS FeishuDocsCommentsTests): the JSON, where "Send" goes, retries and resolving. */
class LarkDocCommentsTest {
    private val doc = "https://example.feishu.cn/docx/D1"

    internal class FakeApi(var threads: JsonObject = threads()) : LarkDocCommentsApi {
        val writes = mutableListOf<Pair<String?, JsonObject>>()
        val resolves = mutableListOf<Pair<String, Boolean>>()
        var reads = 0
        var fail: Exception? = null
        /** What a write answers; null: `{ok:true}` without threads (then the model reads them again). */
        var answer: JsonObject? = null
        /** A read answers the threads as they were when it was asked, once this lets it through. */
        var hold: CompletableDeferred<Unit>? = null
        override suspend fun threads(url: String): JsonObject {
            reads++
            val now = threads
            hold?.await()
            return now
        }
        override suspend fun write(thread: String?, body: JsonObject): JsonObject {
            writes += thread to body
            fail?.let { throw it }
            return answer ?: buildJsonObject { put("ok", true) }
        }
        override suspend fun resolve(thread: String, url: String, resolved: Boolean): JsonObject {
            resolves += thread to resolved
            fail?.let { throw it }
            return buildJsonObject { put("ok", true) }
        }
        override suspend fun people(url: String, query: String) =
            listOf(LarkDocPerson("u-li", "李雷"), LarkDocPerson("u-han", "韩梅梅")).filter { query in it.name }
    }

    companion object {
        fun threads(): JsonObject = Json.parseToJsonElement("""{"threads":[
            {"id":"t-old","whole":false,"resolved":false,"quote":"灰度方案","createdAt":"2026-11-01T08:00:00Z","replies":[
              {"id":"r1","author":{"name":"李雷"},"content":[{"kind":"text","text":"这段要改"}],"createdAt":"2026-11-01T08:00:00Z"}]},
            {"id":"t-done","whole":true,"resolved":true,"quote":"","replies":[
              {"id":"r2","author":{"name":"韩梅梅"},"content":[{"kind":"mention","text":"@李雷"},{"kind":"text","text":" 好了"}],"createdAt":"2026-11-04T08:00:00Z","edited":true,"imageCount":2}]},
            {"id":"t-new","whole":true,"resolved":false,"quote":"","visibility":"partial","replies":[
              {"id":"r3","author":{},"content":[{"kind":"text","text":"整体不错"}],"createdAt":"2026-11-03T08:00:00Z"}]},
            {"whole":true}
        ]}""").jsonObject
    }

    @Test fun threadsReadOpenFirstNewestFirst() {
        val parsed = parseLarkDocComments(threads(), unnamed = "飞书用户")
        assertEquals(listOf("t-new", "t-old", "t-done"), parsed.map { it.id })
        assertEquals("飞书用户", parsed[0].replies.single().author)
        assertTrue(parsed[0].partial)
        val done = parsed[2]
        assertEquals("@李雷 好了", done.replies.single().text)
        assertTrue(done.replies.single().edited)
        assertEquals(2, done.replies.single().imageCount)
        assertTrue(parseLarkDocComments(JsonObject(emptyMap())).isEmpty())
    }

    @Test fun aDocumentCommentCarriesOnlyTheMentionsItsTextStillHolds() = runTest {
        val api = FakeApi()
        val model = LarkDocCommentsModel(doc, api)
        model.load()
        model.draft = "请 @李"
        assertEquals("李", larkDocMentionQuery(model.draft))
        model.mention(model.people("李").single())
        assertEquals("请 @李雷 ", model.draft)
        model.draft += "看看"
        model.mentions = model.mentions + LarkDocPerson("u-han", "韩梅梅") // picked, then deleted from the text
        model.send()
        val (thread, body) = api.writes.single()
        assertNull(thread)
        assertEquals(doc, body["url"]!!.jsonPrimitive.content)
        assertEquals("请 @李雷 看看", body["content"]!!.jsonPrimitive.content)
        assertEquals(listOf("u-li"), body["mentions"]!!.jsonArray.map { it.jsonObject["user_id"]!!.jsonPrimitive.content })
        assertFalse("quote" in body)
        assertEquals("", model.draft)
        assertTrue(model.mentions.isEmpty())
        assertEquals(2, api.reads) // no threads in the answer: read again
    }

    @Test fun mentionLookupsStopAtASpaceOrAnAtInsideAWord() {
        assertEquals("", larkDocMentionQuery("@"))
        assertNull(larkDocMentionQuery("@李 雷"))
        assertNull(larkDocMentionQuery("me@example"))
        assertNull(larkDocMentionQuery("没有"))
    }

    @Test fun aReplyGoesToItsThreadThenTheBoxReturnsToTheDocument() = runTest {
        val api = FakeApi()
        val model = LarkDocCommentsModel(doc, api)
        model.load()
        model.reply(model.threads(false).first { it.id == "t-old" })
        assertEquals(LarkDocCommentTarget.Thread("t-old", "李雷", "这段要改"), model.target)
        model.draft = "改好了"
        api.answer = threads()
        model.send()
        assertEquals("t-old", api.writes.single().first)
        assertEquals(LarkDocCommentTarget.Document, model.target)
        assertEquals(1, api.reads) // the answer had the threads
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun aSlowFirstReadDoesNotTakeBackTheCommentJustSent() = runTest {
        val api = FakeApi()
        val model = LarkDocCommentsModel(doc, api)
        val slow = CompletableDeferred<Unit>()
        api.hold = slow
        val first = launch { model.load() }
        runCurrent()
        // The comment is sent while the sheet's first read is still on its way; the write's answer has it.
        val mine = Json.parseToJsonElement("""{"id":"t-mine","whole":true,"resolved":false,"quote":"","replies":[
            {"id":"r9","author":{"name":"我"},"content":[{"kind":"text","text":"整体不错"}],"createdAt":"2026-11-05T08:00:00Z"}]}""")
        api.answer = buildJsonObject { put("ok", true); put("threads", JsonArray(threads()["threads"]!!.jsonArray + mine)) }
        model.draft = "整体不错"
        model.send()
        assertEquals("t-mine", model.threads(false).first().id)
        slow.complete(Unit)
        first.join()
        assertEquals("t-mine", model.threads(false).first().id)
    }

    @Test fun aPassageCommentCarriesItsQuoteAndBlock() = runTest {
        val api = FakeApi()
        val model = LarkDocCommentsModel(doc, api, LarkDocCommentTarget.Passage("灰度方案", "blk1"))
        model.draft = "这里再想想"
        model.send()
        val body = api.writes.single().second
        assertEquals("灰度方案", body["quote"]!!.jsonPrimitive.content)
        assertEquals("blk1", body["block_id"]!!.jsonPrimitive.content)
    }

    @Test fun aFailedSendKeepsTheDraftAndRetriesWithTheSameId() = runTest {
        val api = FakeApi()
        val model = LarkDocCommentsModel(doc, api)
        model.draft = "好"
        api.fail = HubFailure(HubError.Server, "飞书网关未登录飞书", status = 401)
        model.send()
        assertEquals("好", model.draft)
        assertEquals(LarkDocCommentFailure(R.string.inbox_lark_doc_comments_send_failed, "飞书网关未登录飞书"), model.failure)
        model.send()
        assertEquals(api.writes[0].second["client_id"], api.writes[1].second["client_id"])
        model.draft = "好的"
        model.send()
        assertNotEquals(api.writes[1].second["client_id"], api.writes[2].second["client_id"])
        api.fail = HubFailure(HubError.Server, status = 400, code = "comment_unsupported")
        model.send()
        assertTrue(model.failure!!.unsupported)
        assertNull(model.failure!!.reason) // a bare error kind says nothing
        api.fail = null
        model.draft = "  "
        model.send()
        assertEquals(4, api.writes.size) // blank is not sent
    }

    @Test fun resolveAndReopenAndAFailure() = runTest {
        val api = FakeApi()
        val model = LarkDocCommentsModel(doc, api)
        model.load()
        model.setResolved(model.threads(false).first(), true)
        model.setResolved(model.threads(true).first(), false)
        assertEquals(listOf("t-new" to true, "t-done" to false), api.resolves)
        api.fail = HubFailure(HubError.Server, "这种文档不能这样评论", status = 400)
        model.setResolved(model.threads(true).first(), false)
        assertEquals(R.string.inbox_lark_doc_comments_reopen_failed, model.failure?.action)
        assertTrue(model.resolving.isEmpty())
    }

    @Test fun aFailedReadSaysSoAndARetryReads() = runTest {
        val api = object : LarkDocCommentsApi by FakeApi() {
            var ok = false
            override suspend fun threads(url: String): JsonObject = if (ok) threads() else throw HubFailure(HubError.Unreachable)
        }
        val model = LarkDocCommentsModel(doc, api)
        model.load()
        assertTrue(model.loadFailed)
        assertNull(model.threads)
        api.ok = true
        model.load()
        assertFalse(model.loadFailed)
        assertEquals(2, model.threads(false).size)
    }
}
