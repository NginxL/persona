package persona.feature.inbox

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import java.io.IOException
import persona.core.inbox.*

/** #576 G122: picked mentions belong to the durable reply, not just to the text field currently on screen. */
@OptIn(ExperimentalCoroutinesApi::class)
class LarkMentionReplyStateTest {
    private val everyone = mention("all", "Everyone")
    private val tom = mention("u-tom", "Tom")
    private val zhou = mention("u-zhou", "Zhou")
    private val picked = JsonArray(listOf(everyone, tom, zhou))
    private val text = "@Everyone @Tom @Zhou standup"

    @Test fun deletingANamePrunesItFromTheSavedDraftAndRestoringTextCannotResurrectIt() = runTest {
        val repository = MemoryReplyRepository(savedDraft(text, picked))
        val composer = ReplyComposer(repository, ReplyTarget.Lark("feed-group"), backgroundScope)
        runCurrent()

        composer.update("@Everyone @Zhou standup")
        runCurrent()
        assertEquals(JsonArray(listOf(everyone, zhou)), JsonCodec.encodeToJsonElement(ReplyDraft.serializer(), repository.stored).jsonObject["mentions"])

        // Undoing the edit restores characters only. It must not put the pruned person back into the notification payload.
        composer.update(text)
        runCurrent()
        assertEquals(JsonArray(listOf(everyone, zhou)), JsonCodec.encodeToJsonElement(ReplyDraft.serializer(), repository.stored).jsonObject["mentions"])
        composer.send()
        runCurrent()

        val sent = repository.requests.single()
        assertEquals(text, sent["text"]!!.jsonPrimitive.content)
        assertEquals(JsonArray(listOf(everyone, zhou)), sent["mentions"])
    }

    @Test fun anInitialSendAndItsRetryKeepTheSameRequestAfterADraftJsonRoundTrip() = runTest {
        val repository = MemoryReplyRepository(savedDraft(text, picked), failFirst = true)
        val target = ReplyTarget.Lark("feed-group")
        val initial = ReplyComposer(repository, target, backgroundScope)
        runCurrent()
        initial.send()
        runCurrent()
        assertEquals(1, repository.requests.size)

        repository.stored = JsonCodec.decodeFromString(JsonCodec.encodeToString(repository.stored))
        val retry = ReplyComposer(repository, target, backgroundScope)
        runCurrent()
        retry.send()
        runCurrent()

        assertEquals(2, repository.requests.size)
        assertEquals(repository.requests[0], repository.requests[1])
        assertEquals("client-mention", repository.requests[0]["client_id"]!!.jsonPrimitive.content)
        assertEquals(text, repository.requests[0]["text"]!!.jsonPrimitive.content)
        assertEquals(picked, repository.requests[0]["mentions"])
    }

    @Test fun aThreadReplyRetryKeepsMentionsAfterItsPendingDraftJsonRoundTrip() = runTest {
        val repository = ThreadDraftRepository()
        val source = object : LarkThreadSource {
            val requests = mutableListOf<JsonObject>()
            override suspend fun page(before: String?) = LarkThreadPage(emptyList(), false, null)
            override suspend fun reply(request: ReplyRequest): Boolean {
                requests += JsonCodec.encodeToJsonElement(ReplyRequest.serializer(), request).jsonObject
                if (requests.size == 1) throw IOException("Synthetic lost thread response")
                return true
            }
        }
        val request = JsonCodec.decodeFromString<ReplyRequest>(buildJsonObject {
            put("text", text)
            put("client_id", "client-thread-mention")
            put("mentions", picked)
        }.toString())
        val initial = LarkThreadReplyRepository(repository, "root-1", source)
        val failed = runCatching { initial.reply(initial.target, request) }
        assertTrue(failed.exceptionOrNull() is IOException)

        val pendingTarget = ReplyTarget.Lark("thread-pending:root-1")
        repository.drafts[pendingTarget] = JsonCodec.decodeFromString(JsonCodec.encodeToString(repository.drafts[pendingTarget]))
        val restored = LarkThreadReplyRepository(repository, "root-1", source)
        restored.reply(restored.target, request)

        assertEquals(2, source.requests.size)
        assertEquals(source.requests[0], source.requests[1])
        assertEquals("client-thread-mention", source.requests[0]["client_id"]!!.jsonPrimitive.content)
        assertEquals(text, source.requests[0]["text"]!!.jsonPrimitive.content)
        assertEquals(picked, source.requests[0]["mentions"])
    }

    private fun savedDraft(text: String, mentions: JsonArray): ReplyDraft = JsonCodec.decodeFromString(buildJsonObject {
        put("text", text)
        put("clientId", "client-mention")
        put("mentions", mentions)
    }.toString())

    private class MemoryReplyRepository(initial: ReplyDraft, private val failFirst: Boolean = false) : InboxRepository by FakeInboxRepository() {
        var stored = initial
        val requests = mutableListOf<JsonObject>()
        override suspend fun readDraft(target: ReplyTarget) = stored
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) { stored = draft }
        override suspend fun replyStatus(target: ReplyTarget, clientId: String) = ReplyDelivery.Ready
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) {
            requests += JsonCodec.encodeToJsonElement(ReplyRequest.serializer(), request).jsonObject
            if (failFirst && requests.size == 1) throw IOException("Synthetic lost chat response")
        }
    }

    private class ThreadDraftRepository : InboxRepository by FakeInboxRepository() {
        val drafts = mutableMapOf<ReplyTarget, ReplyDraft>()
        override suspend fun readDraft(target: ReplyTarget) = drafts[target]
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) { drafts[target] = draft }
    }

    private companion object {
        fun mention(id: String, name: String): JsonObject = buildJsonObject {
            put("user_id", id)
            put("name", name)
        }
    }
}
