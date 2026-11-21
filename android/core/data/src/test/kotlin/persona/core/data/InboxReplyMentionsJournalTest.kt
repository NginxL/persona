package persona.core.data

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.feature.inbox.ReplyRequest
import persona.feature.inbox.ReplyTarget
import java.nio.file.Files

/** #576 G122: the on-disk reply journal is part of the mention contract, including legacy thread recovery. */
class InboxReplyMentionsJournalTest {
    @Test fun chatAndThreadRequestsKeepOrderedMentionsThroughAJournalRoundTrip() = runBlocking {
        val root = Files.createTempDirectory("inbox-reply-mentions").toFile()
        try {
            val scope = "hub/generation"
            val directory = root.resolve(scope)
            check(directory.mkdirs())
            val mentions = JsonArray(listOf(mention("all", "Everyone"), mention("u-tom", "Tom")))
            val document = InboxReplyDocument(outbox = listOf(
                InboxPendingReply(ReplyTarget.Lark("feed-group"), ReplyRequest("@Everyone @Tom chat", "client-chat"), "feed-group", state = "unknown"),
                InboxPendingReply(ReplyTarget.Lark("thread:root-1"), ReplyRequest("@Everyone @Tom thread", "client-thread"), "feed-group", state = "unknown"),
            ))
            val encoded = JsonCodec.encodeToJsonElement(InboxReplyDocument.serializer(), document).jsonObject
            val withMentions = JsonObject(encoded + ("outbox" to JsonArray(encoded.getValue("outbox").jsonArray.map { pending ->
                val request = pending.jsonObject.getValue("request").jsonObject
                JsonObject(pending.jsonObject + ("request" to JsonObject(request + ("mentions" to mentions))))
            })))
            directory.resolve("replies.json").writeText(withMentions.toString())

            val journal = InboxReplyJournal(directory, scope)
            val restored = journal.read(scope)
            journal.update(scope) { restored }
            val readAgain = JsonCodec.encodeToJsonElement(InboxReplyDocument.serializer(), InboxReplyJournal(directory, scope).read(scope)).jsonObject
            val requests = readAgain.getValue("outbox").jsonArray.map { it.jsonObject.getValue("request").jsonObject }

            assertEquals(listOf("client-chat", "client-thread"), requests.map { it.getValue("client_id").toString().trim('"') })
            assertEquals(listOf("@Everyone @Tom chat", "@Everyone @Tom thread"), requests.map { it.getValue("text").toString().trim('"') })
            assertEquals(listOf(mentions, mentions), requests.map { it["mentions"] })
        } finally {
            root.deleteRecursively()
        }
    }

    private fun mention(id: String, name: String) = buildJsonObject {
        put("user_id", id)
        put("name", name)
    }
}
