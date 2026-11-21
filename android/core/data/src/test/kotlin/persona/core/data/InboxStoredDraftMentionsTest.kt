package persona.core.data

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.SessionsRepository
import persona.feature.inbox.ReplyDraft
import persona.feature.inbox.ReplyRequest
import persona.feature.inbox.ReplyTarget
import java.io.IOException
import java.nio.file.Files

/** #576 G122: RealInboxRepository's stored-draft and thread-pending recovery must not strip picked mentions. */
@OptIn(ExperimentalCoroutinesApi::class)
class InboxStoredDraftMentionsTest {
    @Test fun threadPendingStoredDraftRecoveryKeepsMentionsThroughTheRealRepository() = runTest {
        val root = Files.createTempDirectory("inbox-stored-draft-mentions").toFile()
        try {
            val scope = "hub/generation"
            val directory = root.resolve(scope)
            check(directory.mkdirs())
            val target = ReplyTarget.Lark("thread-pending:root-1")
            val mentions = JsonArray(listOf(mention("all", "Everyone"), mention("u-tom", "Tom")))
            val document = InboxReplyDocument(drafts = mapOf(target.storageKey() to
                InboxStoredDraft("@Everyone @Tom thread", "client-thread")))
            val encoded = JsonCodec.encodeToJsonElement(InboxReplyDocument.serializer(), document).jsonObject
            val drafts = encoded.getValue("drafts").jsonObject
            val stored = drafts.getValue(target.storageKey()).jsonObject
            val withMentions = JsonObject(encoded + ("drafts" to JsonObject(drafts +
                (target.storageKey() to JsonObject(stored + ("mentions" to mentions))))))
            directory.resolve("replies.json").writeText(withMentions.toString())
            val journal = InboxReplyJournal(directory, scope)
            val transport = FakeTransport(scope)
            val repository = repository(transport, journal, backgroundScope)
            runCurrent()

            // Startup turns a legacy thread-pending stored draft into an immutable outbox request.
            val recovered = journal.read(scope).outbox.single { it.request.clientId == "client-thread" }
            val recoveredJson = JsonCodec.encodeToJsonElement(ReplyRequest.serializer(), recovered.request).jsonObject
            assertEquals(mentions, recoveredJson["mentions"])

            // Reading and saving the same draft goes through InboxStoredDraft -> ReplyDraft -> InboxStoredDraft.
            val restored = repository.readDraft(target)
            assertNotNull(restored)
            val restoredJson = JsonCodec.encodeToJsonElement(ReplyDraft.serializer(), restored!!).jsonObject
            assertEquals(mentions, restoredJson["mentions"])
            repository.saveDraft(target, restored)
            val saved = journal.read(scope).drafts.getValue(target.storageKey())
            val savedJson = JsonCodec.encodeToJsonElement(InboxStoredDraft.serializer(), saved).jsonObject
            assertEquals(mentions, savedJson["mentions"])
        } finally {
            root.deleteRecursively()
        }
    }

    private fun repository(transport: InboxAssistantTransport, journal: InboxReplyJournal, scope: CoroutineScope): RealInboxRepository {
        val connection = object : InboxAssistantConnection {
            override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        return RealInboxRepository(connection, journal, sessions, interactions, scope) { _, _ -> error("not used") }
    }

    private class FakeTransport(override val scope: String) : InboxAssistantTransport {
        override suspend fun inbox() = JsonObject(emptyMap())
        override suspend fun setDone(itemId: String, done: Boolean) = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String): JsonObject =
            throw IOException("Synthetic offline recovery")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = JsonObject(emptyMap())
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = JsonObject(emptyMap())
        override suspend fun media(ref: String) = ByteArray(0)
    }

    private fun mention(id: String, name: String) = buildJsonObject {
        put("user_id", id)
        put("name", name)
    }
}
