package persona.core.data

import androidx.room.Room
import java.io.File
import java.security.MessageDigest
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.assertEquals
import org.junit.Assume.assumeTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.Endpoint
import persona.core.contracts.HubScope
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.core.hub.HttpInboxAssistantTransport
import persona.core.hub.HttpMessageTransport
import persona.core.hub.HubChatTransport
import persona.core.hub.HubConnectionConfig
import persona.core.hub.HubNetwork
import persona.core.hub.OkHttpHubConnection
import persona.feature.inbox.InboxHandOffRequest

/** Opt-in against the two real managers started by the live handoff fixture. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class NamedManagerInboxHandoffLiveTest {
    @get:Rule val temporary = TemporaryFolder()

    @Test fun sendsAndReplaysTheNamedManagersInboxItem() {
        val path = System.getenv("PERSONA_ANDROID_HANDOFF_FIXTURE")
        assumeTrue("Set PERSONA_ANDROID_HANDOFF_FIXTURE to run against the isolated hub", !path.isNullOrBlank())
        val file = File(requireNotNull(path))
        val fixture = JsonCodec.parseToJsonElement(file.readText()).jsonObject
        val base = fixture.string("base_url").toHttpUrl()
        require(base.host in setOf("127.0.0.1", "::1", "localhost")) { "Expected a loopback fixture hub" }
        val target = SessionKey(fixture.string("machine_id"), fixture.string("session_id"))
        val manager = fixture.string("manager")
        require(manager.isNotBlank() && manager != "assistant") { "Expected a named manager" }
        val config = HubConnectionConfig(fixture.string("hub_id"), null, fixture.string("token"),
            listOf(Endpoint(base.toString(), "lan")), "handoff-test")
        val itemId = fixture.string("item_id")
        val clientId = fixture.string("client_id")

        // An independent Room/journal for each attempt forces the identical operation across HTTP twice.
        val attempts = runBlocking {
            listOf("initial", "replay").map { name ->
                withTimeout(60_000) { attempt(config, name, manager, itemId, target, clientId) }
            }
        }
        assertEquals("replay must return the original handoff response", attempts[0].record, attempts[1].record)
        val proof = buildJsonObject {
            put("manager", manager)
            put("item_id", itemId)
            put("machine_id", target.machineId)
            put("session_id", target.sessionId)
            put("client_id", clientId)
            put("same_record_result", true)
            put("attempts", JsonArray(attempts.map { it.proof }))
        }
        file.resolveSibling("android-proof.json").writeText(proof.toString() + "\n")
    }

    private data class Attempt(val record: JsonObject, val proof: JsonObject)

    private suspend fun attempt(
        config: HubConnectionConfig, name: String, manager: String, itemId: String, target: SessionKey, clientId: String,
    ): Attempt {
        val pairing = HubScope(requireNotNull(config.hubId), name)
        val job = SupervisorJob()
        val scope = CoroutineScope(job + Dispatchers.IO)
        val database = Room.inMemoryDatabaseBuilder(RuntimeEnvironment.getApplication(), PersistentDatabase::class.java)
            .allowMainThreadQueries().build()
        val journal = InboxReplyJournal(temporary.newFolder(name), pairing.path)
        val store = RoomOutboxStore(database, pairing, journal)
        lateinit var messages: HttpMessageTransport
        val connection = OkHttpHubConnection(config, HubNetwork("loopback"), onReady = { messages.onReady() },
            connectOnCreation = false)
        try {
            messages = HttpMessageTransport(pairing, connection, scope)
            val sent = CompletableDeferred<Pair<MessageRequest, MessageTransportResult>>()
            val sender = OutboxMessageSender(pairing, store, object : MessageTransport by messages {
                override suspend fun send(request: MessageRequest): MessageTransportResult =
                    messages.send(request).also { sent.complete(request to it) }
            }, scope)
            val inbox = HttpInboxAssistantTransport(pairing.path, connection)
            val recorded = CompletableDeferred<JsonObject>()
            val transport = object : InboxAssistantTransport by inbox {
                override suspend fun recordAgentHandOff(itemId: String, session: SessionKey, title: String, clientId: String): JsonObject {
                    try {
                        return inbox.recordAgentHandOff(itemId, session, title, clientId).also { recorded.complete(it) }
                    } catch (error: Exception) {
                        recorded.completeExceptionally(error)
                        throw error
                    }
                }
            }
            val chat = HubChatTransport(connection)
            val repository = RealInboxRepository(object : InboxAssistantConnection {
                override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
            }, journal, HubSessionsRepository(chat, connection, scope), HubInteractionCenter(chat, connection, scope),
                scope, hubConnection = connection, messageSender = sender) { _, _ -> error("Unexpected attachment") }

            connection.reconnect()
            messages.readyToSend.first { it }
            repository.refresh()
            val item = checkNotNull(repository.snapshot.value.records.singleOrNull { it.hubItemId == itemId && it.manager == manager }) {
                "The merged Inbox must list exactly one $itemId owned by $manager"
            }
            val request = InboxHandOffRequest(item.item.id, "Route this fixture handoff.", target.machineId,
                target.sessionId, clientId, targetTitle = "Fixture agent")
            assertEquals(target, repository.handOff(request))
            val (submitted, result) = sent.await()
            assertEquals(clientId, submitted.clientMessageId)
            assertEquals(clientId, submitted.opId)
            assertEquals(request.text, submitted.text)
            assertEquals(target, submitted.session)
            assertEquals(MessageTransportResult.Accepted, result)
            val record = recorded.await()
            val returned = record.getValue("session").jsonObject
            assertEquals(target.machineId, returned.string("machine_id"))
            assertEquals(target.sessionId, returned.string("session_id"))
            val updated = record.getValue("item").jsonObject
            assertEquals(itemId, updated.string("id"))
            val handoff = updated.getValue("handoffs").jsonArray.single().jsonObject
            assertEquals(target.machineId, handoff.string("machine_id"))
            assertEquals(target.sessionId, handoff.string("session_id"))
            val chain = updated.getValue("chain").jsonArray.map { it.jsonObject }.single {
                it.string("kind") == "agent" && it["link"]?.jsonObject?.get("session_id")?.jsonPrimitive?.content == target.sessionId
            }
            assertEquals(target.machineId, chain.getValue("link").jsonObject.string("machine_id"))
            while (journal.read(pairing.path).agentHandOffs.none { it.request.operationId == clientId && it.recorded }) delay(20)

            val proof = buildJsonObject {
                put("message_result", "accepted")
                put("record_result", buildJsonObject {
                    put("item_id", updated.string("id"))
                    put("machine_id", returned.string("machine_id"))
                    put("session_id", returned.string("session_id"))
                    put("chain_id", chain.string("id"))
                    put("sha256", sha256(record.toString().toByteArray()))
                })
            }
            return Attempt(record, proof)
        } finally {
            withContext(NonCancellable) {
                job.cancelAndJoin()
                connection.close()
                journal.close()
                database.close()
            }
        }
    }

    private companion object {
        fun JsonObject.string(key: String): String = getValue(key).jsonPrimitive.content
        fun sha256(bytes: ByteArray): String = MessageDigest.getInstance("SHA-256").digest(bytes)
            .joinToString("") { "%02x".format(it) }
    }
}
