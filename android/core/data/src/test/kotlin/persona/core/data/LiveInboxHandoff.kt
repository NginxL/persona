package persona.core.data

import androidx.room.Room
import java.io.File
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.JsonObject
import org.robolectric.RuntimeEnvironment
import persona.core.contracts.HubScope
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

internal data class LiveInboxHandoff(
    val pairing: HubScope,
    val chat: HubChatTransport,
    val connection: OkHttpHubConnection,
    val repository: RealInboxRepository,
    val sender: OutboxMessageSender,
    val store: RoomOutboxStore,
    val journal: InboxReplyJournal,
    val sent: CompletableDeferred<Pair<MessageRequest, MessageTransportResult>>,
    val recorded: CompletableDeferred<JsonObject>,
)

internal suspend fun <T> withLiveInboxHandoff(
    config: HubConnectionConfig, name: String, journalDirectory: File,
    run: suspend LiveInboxHandoff.() -> T,
): T {
    val pairing = HubScope(requireNotNull(config.hubId), name)
    val job = SupervisorJob()
    val scope = CoroutineScope(job + Dispatchers.IO)
    val database = Room.inMemoryDatabaseBuilder(RuntimeEnvironment.getApplication(), PersistentDatabase::class.java)
        .allowMainThreadQueries().build()
    val journal = InboxReplyJournal(journalDirectory, pairing.path)
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
        return LiveInboxHandoff(pairing, chat, connection, repository, sender, store, journal, sent, recorded).run()
    } finally {
        withContext(NonCancellable) {
            job.cancelAndJoin()
            connection.close()
            journal.close()
            database.close()
        }
    }
}
