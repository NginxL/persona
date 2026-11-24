package persona.core.data

import android.content.Context
import android.content.ContextWrapper
import androidx.datastore.core.DataStore
import androidx.room.Room
import java.io.File
import java.nio.file.Files
import java.util.UUID
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.cancel
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ComposerDelivery
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.EndpointOverrides
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageEvidence
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.inbox.InboxHandOffRequest

/** Independent counterexamples for the four recovery boundaries; each uses a different path from its diagnostic test. */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class InboxHandOffRecoveryIndependentTest {
    @Test fun originReopenReconcilesTargetJournalWithoutBufferedEvent() = runTest {
        val root = Files.createTempDirectory("handoff-independent-origin-replay").toFile()
        val origin = uniqueScope("origin-replay")
        val target = uniqueScope("target-replay")
        val operationId = "replay-${UUID.randomUUID()}"
        val request = request(operationId)
        val pending = InboxAgentHandOff(origin.path, request, "hub-item", manager = "manager",
            deliveryScopes = setOf(target.path))
        val originJournal = journal(root, origin)
        val targetJournal = journal(root, target)
        originJournal.update(origin.path) { it.enqueue(pending) }
        // This is the durable state after a target commits Accepted but the process dies before its in-memory event
        // can be replayed. No AuthoritativeMessageDeliveries.publish call is made in this test.
        targetJournal.update(target.path) {
            it.enqueue(pending.copy(delivered = true, deliveryScopes = setOf(target.path)))
        }
        val transport = RecordingInboxTransport(origin.path)
        repository(transport, originJournal, backgroundScope)

        withContext(Dispatchers.Default.limitedParallelism(1)) {
            withTimeout(5_000) { transport.recordVersions.first { it > 0 } }
        }
        assertEquals(listOf(operationId), transport.records.map { it.clientId })
        assertTrue(originJournal.read(origin.path).agentHandOffs.single().delivered)
    }

    @Test fun cancelledSameOriginPublicationIsDurableOnLaterAccepted() = runTest {
        val root = Files.createTempDirectory("handoff-independent-cancel").toFile()
        val pairing = uniqueScope("cancel")
        val operationId = "cancel-${UUID.randomUUID()}"
        val handOff = InboxAgentHandOff(pairing.path, request(operationId), "hub-item")
        val journal = journal(root, pairing)
        val base = JournalOutboxStore(journal, pairing.path)
        val blocking = BlockingDeliveryStore(base)
        val transport = ScriptedTransport()
        val sender = OutboxMessageSender(pairing, blocking, transport, backgroundScope)
        val publications = mutableListOf<AuthoritativeMessageDelivery>()
        backgroundScope.launch(start = CoroutineStart.UNDISPATCHED) {
            AuthoritativeMessageDeliveries.events.collect { if (it.clientMessageId == operationId) publications += it }
        }
        var attempt: Job? = null
        try {
            withContext(InboxAgentHandOffContext(handOff)) {
                sender.send(handOff.session, handOff.request.text, operationId)
            }
            runCurrent()
            assertTrue(base.transition(operationId, setOf(OutboxState.WaitingForNetwork), OutboxState.Refused))
            sender.recordComposerDelivery(
                operationId, ComposerDelivery(DeliveryState.Sending), "retry-${UUID.randomUUID()}", null,
            )
            attempt = async {
                sender.recordComposerDelivery(
                    operationId, ComposerDelivery(DeliveryState.Sent), null, MessageTransportResult.Accepted,
                )
            }
            runCurrent()
            withTimeout(1_000) { blocking.started.await() }
            attempt.cancelAndJoin()

            transport.evidence[operationId] = MessageEvidence.Receipt(MessageTransportResult.Accepted)
            transport.readyToSend.value = true
            runCurrent()
            var waited = 0
            while (publications.isEmpty() && waited++ < 500) {
                runCurrent()
                withContext(Dispatchers.Default) { delay(10) }
            }
            runCurrent()

            assertEquals("later Accepted must publish again", 1, publications.size)
            assertTrue(
                "a store cancelled before its commit must not be reported as origin-committed",
                journal.read(pairing.path).agentHandOffs.single().delivered,
            )
        } finally {
            attempt?.cancelAndJoin()
            sender.close()
        }
    }

    @Test fun restoredShownRowAcceptsDirectTranscriptEcho() = runTest {
        val root = Files.createTempDirectory("handoff-independent-shown").toFile()
        val pairing = uniqueScope("shown")
        val operationId = "shown-${UUID.randomUUID()}"
        val handOff = InboxAgentHandOff(pairing.path, request(operationId), "hub-item")
        val store = JournalOutboxStore(journal(root, pairing), pairing.path)
        val transport = ScriptedTransport()
        val sender = OutboxMessageSender(pairing, store, transport, backgroundScope)
        val publications = mutableListOf<AuthoritativeMessageDelivery>()
        backgroundScope.launch(start = CoroutineStart.UNDISPATCHED) {
            AuthoritativeMessageDeliveries.events.collect { if (it.clientMessageId == operationId) publications += it }
        }
        try {
            withContext(InboxAgentHandOffContext(handOff)) {
                sender.send(handOff.session, handOff.request.text, operationId)
            }
            runCurrent()
            assertTrue(store.transition(operationId, setOf(OutboxState.WaitingForNetwork), OutboxState.Shown))
            runCurrent()
            assertTrue(publications.isEmpty())

            // Unlike the diagnostic's MessageEvidence.Shown reconciliation, this is the real transcript observer.
            sender.observeTranscript(handOff.session, listOf(
                Item("fresh-echo", 1, role = "user", kind = "text", text = handOff.request.text,
                    clientMsgId = operationId),
            ))
            runCurrent()

            assertEquals("a fresh echo must not be filtered out by the restored Shown row", 1, publications.size)
        } finally {
            sender.close()
        }
    }

    @Test fun targetGroupMergeKeepsExternalOriginReplayableAfterAccepted() = runBlocking {
        val base = RuntimeEnvironment.getApplication()
        val root = File(base.cacheDir, "handoff-independent-merge-${UUID.randomUUID()}").apply { mkdirs() }
        val context = object : ContextWrapper(base) {
            override fun getApplicationContext(): Context = this
            override fun getFilesDir() = File(root, "files").apply { mkdirs() }
            override fun getCacheDir() = File(root, "cache").apply { mkdirs() }
            override fun getNoBackupFilesDir() = File(root, "no-backup").apply { mkdirs() }
        }
        val parent = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        val store = HubDataStore(context, parent) { _, _ -> MergeHub() }
        val origin = uniqueScope("external-origin")
        val absorbed = uniqueScope("absorbed-target")
        val destination = uniqueScope("merge-destination")
        val operationId = "merge-${UUID.randomUUID()}"
        val pending = InboxAgentHandOff(origin.path, request(operationId), "hub-item", manager = "manager")
        val originJournal = journal(context.noBackupFilesDir, origin)
        val absorbedJournal = journal(context.noBackupFilesDir, absorbed)
        val destinationJournal = journal(context.noBackupFilesDir, destination)
        originJournal.update(origin.path) { it.enqueue(pending) }
        var source: PersistentDatabase? = null
        var target: ScopedHubData? = null
        val targetDatabase = Room.inMemoryDatabaseBuilder(base, PersistentDatabase::class.java)
            .allowMainThreadQueries().build()
        val cacheDatabase = Room.inMemoryDatabaseBuilder(base, ItemsDatabase::class.java)
            .allowMainThreadQueries().build()
        try {
            val sourceFile = File(context.filesDir, "hub-data/${absorbed.path}/data.db").apply {
                parentFile!!.mkdirs()
            }
            source = Room.databaseBuilder(base, PersistentDatabase::class.java, sourceFile.absolutePath)
                .allowMainThreadQueries().build()
            withContext(InboxAgentHandOffContext(pending, CompletableDeferred(Unit))) {
                RoomOutboxStore(checkNotNull(source), absorbed, absorbedJournal).enqueue(OutboxEntry(
                    operationId, pending.session, pending.request.text, createdAt = 1,
                ).withAgentHandOff(pending))
            }
            source.close()
            source = null

            val unsent = store.unsentAt(absorbed)
            assertEquals(1, unsent.messageCount)
            val hub = MergeHub()
            val owner = CoroutineScope(parent.coroutineContext + SupervisorJob(parent.coroutineContext[Job]))
            val targetStore = RoomOutboxStore(targetDatabase, destination, destinationJournal)
            target = ScopedHubData(destination, credentials(destination.hubId),
                OutboxMessageSender(destination, targetStore, hub, owner), DraftStore(targetDatabase, destination),
                SessionItemsCache(cacheDatabase, destination), owner, targetDatabase, cacheDatabase,
                File(root, "target-files").apply { mkdirs() }, File(root, "target-cache").apply { mkdirs() },
                File(root, "target-no-backup").apply { mkdirs() },
                EndpointOverridesStore(EmptyEndpointStore(), EndpointOverrides()), inboxReplies = destinationJournal)
            checkNotNull(target).adopt(unsent)

            hub.readyToSend.value = true
            withTimeout(5_000) {
                while (!destinationJournal.read(destination.path).agentHandOffs.single().delivered) delay(10)
            }
            // Target evidence is committed before the cross-pairing origin credit; await the second durable commit.
            val original = withTimeout(5_000) {
                var saved = originJournal.read(origin.path).agentHandOffs.single()
                while (!saved.delivered) {
                    delay(10)
                    saved = originJournal.read(origin.path).agentHandOffs.single()
                }
                saved
            }
            assertEquals("merging a target must not re-parent the hand-off to the target group", origin.path, original.scope)
            assertTrue("the new target journal must be linked into the origin's replay set",
                destination.path in original.deliveryScopes)
            assertTrue("Accepted on the adopted target must credit the original origin", original.delivered)
        } finally {
            source?.close()
            target?.close() ?: run {
                targetDatabase.close()
                cacheDatabase.close()
            }
            store.close()
            parent.cancel()
        }
    }

    private fun uniqueScope(prefix: String) = HubScope("$prefix-${UUID.randomUUID()}", "generation")

    private fun request(operationId: String) = InboxHandOffRequest(
        "item", "Handle this", "laptop", "agent", operationId, targetTitle = "Example agent",
    )

    private fun journal(root: File, scope: HubScope) = InboxReplyJournal(
        File(root, "hub-data/${scope.path}/inbox-replies"), scope.path,
    )

    private fun repository(
        transport: InboxAssistantTransport,
        journal: InboxReplyJournal,
        owner: CoroutineScope,
    ) = RealInboxRepository(
        object : InboxAssistantConnection {
            override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
        },
        journal,
        object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        },
        object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        },
        owner,
        messageSender = object : MessageSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        },
    ) { _, _ -> error("unused") }

    private class JournalOutboxStore(
        private val journal: InboxReplyJournal,
        private val scope: String,
        private val delegate: MemoryOutboxStore = MemoryOutboxStore(),
    ) : OutboxStore by delegate {
        override suspend fun enqueue(entry: OutboxEntry) {
            entry.agentHandOff?.let { handOff -> journal.update(scope) { it.enqueue(handOff) } }
            delegate.enqueue(entry)
        }

        override suspend fun persistAgentHandOffDelivery(entry: OutboxEntry): OutboxEntry {
            val handOff = entry.agentHandOff ?: return entry
            val delivered = entry.copyForDelivery(agentHandOff = handOff.copy(delivered = true))
            journal.update(scope) { it.commitAgentHandOffDelivery(checkNotNull(delivered.agentHandOff)) }
            return delivered
        }
    }

    private class BlockingDeliveryStore(delegate: OutboxStore) : OutboxStore by delegate {
        val started = CompletableDeferred<Unit>()
        override suspend fun persistAgentHandOffDelivery(entry: OutboxEntry): OutboxEntry {
            started.complete(Unit)
            awaitCancellation()
        }
    }

    private class ScriptedTransport : MessageTransport {
        override val readyToSend = MutableStateFlow(false)
        override val supportsClientOps = false
        val evidence = mutableMapOf<String, MessageEvidence>()
        override suspend fun reconcile(request: MessageRequest, createdAt: Long, allowTextMatch: Boolean): MessageEvidence? =
            evidence[request.clientMessageId]
        override suspend fun send(request: MessageRequest): MessageTransportResult = MessageTransportResult.NoReceipt
    }

    private data class RecordedHandOff(val clientId: String)

    private class RecordingInboxTransport(override val scope: String) : InboxAssistantTransport {
        val records = mutableListOf<RecordedHandOff>()
        val recordVersions = MutableStateFlow(0)
        override suspend fun recordAgentHandOff(
            itemId: String,
            session: SessionKey,
            title: String,
            clientId: String,
        ): JsonObject {
            records += RecordedHandOff(clientId)
            recordVersions.value = records.size
            return JsonObject(emptyMap())
        }
        override suspend fun inbox() = buildJsonObject { put("items", JsonArray(emptyList())) }
        override suspend fun setDone(itemId: String, done: Boolean) = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = buildJsonObject {
            put("spaces", JsonArray(emptyList()))
            put("sessions", JsonArray(emptyList()))
        }
        override suspend fun media(ref: String) = byteArrayOf()
    }

    private fun credentials(hub: String) = HubCredentials("https://$hub.example.com", hub, null, emptyList(), "token")

    private class EmptyEndpointStore : DataStore<EndpointOverridesDocument> {
        override val data: Flow<EndpointOverridesDocument> = flowOf(EndpointOverridesDocument())
        override suspend fun updateData(
            transform: suspend (EndpointOverridesDocument) -> EndpointOverridesDocument,
        ) = transform(EndpointOverridesDocument())
    }

    private class MergeHub : MessageTransport {
        override val readyToSend = MutableStateFlow(false)
        override val supportsClientOps = false
        override suspend fun send(request: MessageRequest): MessageTransportResult = MessageTransportResult.Accepted
    }
}
