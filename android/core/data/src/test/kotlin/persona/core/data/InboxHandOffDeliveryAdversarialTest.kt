package persona.core.data

import persona.core.contracts.GroupKeys
import persona.core.contracts.HubScope
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageEvidence
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult
import persona.core.contracts.api.SessionsRepository
import persona.core.inbox.InboxHandOffRequest
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.nio.file.Files

/** Adversarial delivery boundaries using the real repository and outbox, with only transports and storage faked. */
@OptIn(ExperimentalCoroutinesApi::class)
class InboxHandOffDeliveryAdversarialTest {
    @Test fun acceptedAgentOnAnotherHubGroupRecordsOnTheOriginatingInbox() = runTest {
        val target = SessionKey("laptop~g12345678", "agent")
        val fixture = Fixture(
            owner = backgroundScope,
            outboxHub = HubScope("agent-hub", "generation"),
            inboxScope = "inbox-hub/generation",
            target = target,
            routeMergedKeys = true,
            online = true,
            operationId = "cross-group-accepted",
        )
        try {
            fixture.messages.answers += MessageTransportResult.Accepted
            val handOff = async { fixture.repository.handOff(fixture.request) }
            runCurrent()

            assertEquals(target, handOff.await())
            assertEquals(GroupKeys.rawKey(target), fixture.messages.requests.single().session)
            assertEquals(OutboxState.Sent, fixture.store.state(fixture.operationId))
            assertEquals(
                "the selected inbox must record an actually accepted cross-group Agent hand-off",
                listOf(fixture.operationId), fixture.inbox.records,
            )
        } finally {
            fixture.sender.close()
        }
    }

    @Test fun offlineDeliveryAfterRepositoryRecreationRecordsOnce() = runTest {
        val store = IdempotentOutboxStore()
        val inbox = RecordingInboxTransport("inbox-hub/generation")
        val pairing = HubScope("inbox-hub", "generation")
        val firstJob = SupervisorJob(coroutineContext[Job])
        val firstOwner = CoroutineScope(coroutineContext + firstJob)
        var secondJob: Job? = null
        var second: Fixture? = null
        try {
            val first = Fixture(
                owner = firstOwner,
                outboxHub = pairing,
                store = store,
                inbox = inbox,
                online = false,
                operationId = "offline-across-recreation",
            )
            val handOff = async { first.repository.handOff(first.request) }
            runCurrent()
            assertEquals(first.target, handOff.await())
            assertEquals(OutboxState.WaitingForNetwork, store.state(first.operationId))
            assertTrue("local persistence is not delivery", inbox.records.isEmpty())
            assertTrue(fixtureRequests(first).isEmpty())

            // The durable outbox survives its owner; the repository's in-memory pending hand-off does not.
            firstJob.cancelAndJoin()
            secondJob = SupervisorJob(coroutineContext[Job])
            second = Fixture(
                owner = CoroutineScope(coroutineContext + checkNotNull(secondJob)),
                outboxHub = pairing,
                store = store,
                inbox = inbox,
                online = false,
                operationId = first.operationId,
            )
            second.messages.answers += MessageTransportResult.Accepted
            second.messages.readyToSend.value = true
            runCurrent()

            assertEquals(OutboxState.Sent, store.state(first.operationId))
            assertEquals(1, second.messages.requests.size)
            assertEquals(
                "later real outbox success must record the hand-off even after owner recreation",
                listOf(first.operationId), inbox.records,
            )
        } finally {
            secondJob?.cancelAndJoin()
            firstJob.cancelAndJoin()
        }
    }

    @Test fun acceptedReconciliationReceiptRecordsAfterManualConfirmationWithoutResending() = runTest {
        val fixture = Fixture(
            owner = backgroundScope,
            outboxHub = HubScope("inbox-hub", "generation"),
            online = true,
            operationId = "accepted-after-manual-confirmation",
        )
        try {
            fixture.messages.answers += MessageTransportResult.NoReceipt
            val handOff = async { fixture.repository.handOff(fixture.request) }
            runCurrent()
            assertEquals(fixture.target, handOff.await())
            assertEquals(OutboxState.Unknown, fixture.store.state(fixture.operationId))

            assertTrue(fixture.sender.confirm(fixture.operationId, delivered = true))
            runCurrent()
            assertEquals(OutboxState.Sent, fixture.store.state(fixture.operationId))
            assertTrue("manual confirmation alone is not hub evidence", fixture.inbox.records.isEmpty())

            fixture.messages.evidence[fixture.operationId] =
                MessageEvidence.Receipt(MessageTransportResult.Accepted)
            fixture.messages.readyToSend.value = false
            runCurrent()
            fixture.messages.readyToSend.value = true
            runCurrent()

            assertEquals("an Accepted receipt reconciles without another send", 1, fixture.messages.requests.size)
            assertEquals(
                "the later authoritative Accepted receipt must record once",
                listOf(fixture.operationId), fixture.inbox.records,
            )
        } finally {
            fixture.sender.close()
        }
    }

    private fun fixtureRequests(fixture: Fixture) = fixture.messages.requests

    private class Fixture(
        owner: CoroutineScope,
        outboxHub: HubScope,
        inboxScope: String = outboxHub.path,
        val target: SessionKey = SessionKey("laptop", "agent"),
        routeMergedKeys: Boolean = false,
        online: Boolean,
        val operationId: String,
        val store: IdempotentOutboxStore = IdempotentOutboxStore(),
        val inbox: RecordingInboxTransport = RecordingInboxTransport(inboxScope),
    ) {
        val request = InboxHandOffRequest(
            "item", "Handle this", target.machineId, target.sessionId, operationId, targetTitle = "Example agent",
        )
        val messages = ScriptedMessageTransport(online)
        val sender = OutboxMessageSender(outboxHub, store, messages, owner)
        val repository = RealInboxRepository(
            object : InboxAssistantConnection {
                override val current = MutableStateFlow<InboxAssistantTransport?>(inbox)
            },
            InboxReplyJournal(Files.createTempDirectory("inbox-handoff-adversarial").toFile(), inboxScope),
            EmptySessions,
            EmptyInteractionCenter,
            owner,
            messageSender = if (routeMergedKeys) RawKeyRouter(sender) else sender,
        ) { _, _ -> error("unused") }
    }

    /** The production grouped facade routes by merged machine id and removes its namespace before the real outbox. */
    private class RawKeyRouter(private val sender: OutboxMessageSender) : MessageSender by sender {
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) =
            sender.send(GroupKeys.rawKey(session), text, clientMessageId, clearDraft)
    }

    private class ScriptedMessageTransport(online: Boolean) : MessageTransport {
        override val readyToSend = MutableStateFlow(online)
        override val supportsClientOps = false
        val requests = mutableListOf<MessageRequest>()
        val answers = ArrayDeque<MessageTransportResult>()
        val evidence = mutableMapOf<String, MessageEvidence>()

        override suspend fun reconcile(request: MessageRequest, createdAt: Long, allowTextMatch: Boolean): MessageEvidence? =
            evidence[request.clientMessageId]

        override suspend fun send(request: MessageRequest): MessageTransportResult {
            requests += request
            return answers.removeFirst()
        }
    }

    private class RecordingInboxTransport(override val scope: String) : InboxAssistantTransport {
        val records = mutableListOf<String>()

        override suspend fun recordAgentHandOff(
            itemId: String,
            session: SessionKey,
            title: String,
            clientId: String,
        ): JsonObject {
            records += clientId
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

    /** RoomOutboxStore's client-id idempotence over storage that can outlive a repository owner. */
    private class IdempotentOutboxStore : OutboxStore {
        private val delegate = MemoryOutboxStore()
        override val entries = delegate.entries

        fun state(id: String) = delegate.state(id)

        override suspend fun enqueue(entry: OutboxEntry) {
            val existing = entries.value.firstOrNull { it.clientMessageId == entry.clientMessageId }
            if (existing == null) {
                delegate.enqueue(entry)
                return
            }
            require(existing.mainTarget == entry.mainTarget &&
                (entry.mainTarget || existing.session == entry.session) &&
                existing.text == entry.text && existing.clearDraft == entry.clearDraft &&
                existing.launcher == entry.launcher && existing.model == entry.model) {
                "Operation id reused with different parameters"
            }
        }

        override suspend fun mainConversation() = delegate.mainConversation()
        override suspend fun claimMainCreation() = delegate.claimMainCreation()
        override suspend fun bindMainConversation(session: SessionKey) = delegate.bindMainConversation(session)
        override suspend fun releaseMainCreation() = delegate.releaseMainCreation()
        override suspend fun forgetMainConversation(session: SessionKey) = delegate.forgetMainConversation(session)
        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?) =
            delegate.transition(id, from, to, error)
        override suspend fun transition(id: String, from: Set<OutboxState>, to: OutboxState, error: String?, opId: String) =
            delegate.transition(id, from, to, error, opId)
        override suspend fun recoverInterrupted() = delegate.recoverInterrupted()
    }

    private object EmptySessions : SessionsRepository {
        override val machines = MutableStateFlow<List<Machine>>(emptyList())
        override val sessions = MutableStateFlow<List<Session>>(emptyList())
        override suspend fun refresh() = Unit
    }

    private object EmptyInteractionCenter : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
}
