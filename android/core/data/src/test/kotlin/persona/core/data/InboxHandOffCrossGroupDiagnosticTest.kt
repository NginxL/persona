package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.feature.inbox.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.nio.file.Files

/**
 * Recording an Agent hand-off on its item (docs/android/android.md "Hand to AI", design 9) happens after the message is
 * accepted, and never on the caller's clock: the sheet closes and opens the Agent's chat at once, however slow or gone
 * the hub is.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class InboxHandOffCrossGroupDiagnosticTest {
    /**
     * The hub holds the recording open: the hand-off still answers the Agent's key, so the sheet closes and its
     * conversation opens. The recording itself finishes later and tells the item's manager about the item and the
     * session; the listed row is left as it is (its page picks the chain up at the next refresh).
     */
    @Test fun aHangingRecordingDoesNotHoldTheAcceptedHandOff() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-record-hang").toFile()
        try {
            val transport = RecordTransport(buildJsonArray {
                add(buildJsonObject { put("id", "item"); put("kind", "mail"); put("title", "Example") })
            })
            val recording = CompletableDeferred<JsonObject>()
            val started = CompletableDeferred<Unit>()
            val record = mutableListOf<List<Any?>>()
            transport.onRecord = { item, session, title, client ->
                record += listOf(item, session, title, client)
                started.complete(Unit)
                recording.await()
            }
            val agents = RecordSender()
            val repository = recordTestRepository(transport, directory, backgroundScope, agents)
            repository.snapshot.first { it.contentSource != null }
            repository.refresh()
            val id = repository.snapshot.value.records.single().item.id
            val key = SessionKey("laptop", "agent")

            // The key comes back while the hub has not answered the recording yet (a slow or gone hub must not hold the sheet).
            val result = withTimeout(1_000) {
                repository.handOff(InboxHandOffRequest(id, "Handle this", "laptop", "agent", "op-1",
                    targetTitle = "Example agent"))
            }
            assertEquals(key, result)
            assertEquals(listOf(Triple(key, "Handle this", "op-1")), agents.sent)
            runCurrent()
            assertTrue("the recording should have started", started.isCompleted)
            assertEquals(listOf<List<Any?>>(listOf("item", key, "Example agent", "op-1")), record)

            recording.complete(buildJsonObject {
                put("item", buildJsonObject { put("id", "item"); put("kind", "mail"); put("title", "Recorded") })
            })
            advanceUntilIdle()
            // The answer does not replace the listed row: the next inbox refresh carries the item's update.
            assertEquals("Example", repository.snapshot.value.records.single().item.title)
        } finally {
            // The directory stays: a background recording may still be writing the reply journal when the test ends,
            // and deleting it under that write fails the test with its NoSuchFileException instead of an assertion.
        }
    }

    /** A failing recording — old hub, offline, a refused request — still leaves the accepted hand-off as it answered. */
    @Test fun aFailedRecordingChangesNeitherTheAnswerNorTheQueuedMessage() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-record-failure").toFile()
        try {
            val transport = RecordTransport(JsonArray(emptyList()))
            val agents = RecordSender()
            val repository = recordTestRepository(transport, directory, backgroundScope, agents)
            val key = SessionKey("laptop", "agent")
            val finished = CompletableDeferred<Unit>()
            for ((index, failure) in listOf(RecordFailure(400), java.io.IOException("offline"), RecordFailure(502),
                UnsupportedOperationException("old transport")).withIndex()) {
                transport.onRecord = { _, _, _, _ ->
                    assertEquals(index + 1, agents.sent.size) // Recording starts only after the outbox accepted the message.
                    try { throw failure } finally { if (index == 3) finished.complete(Unit) }
                }
                assertEquals(key, withTimeout(1_000) {
                    repository.handOff(InboxHandOffRequest("item", "Handle this", "laptop", "agent",
                        "op-$index", targetTitle = "Example agent"))
                })
                while (transport.records < index + 1) yield()
            }
            // The last failure's coroutine ends after its call returned: let it.
            yield()
            assertEquals(4, agents.sent.size)
            assertEquals(4, transport.records)
            assertEquals(agents.sent.map { it.third }, listOf("op-0", "op-1", "op-2", "op-3"))
        } finally {
            // The directory stays: a background recording may still be writing the reply journal when the test ends,
            // and deleting it under that write fails the test with its NoSuchFileException instead of an assertion.
        }
    }

    /**
     * The recording answers with the item as the hub had it before the user marked it done in the meantime. That
     * answer is not applied to the list at all, so the row stays done: nothing reopens it, and a later Done sends
     * no stale version.
     */
    @Test fun aRecordingAnswerNeverReopensADoneItem() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-record-done").toFile()
        try {
            val transport = RecordTransport(buildJsonArray {
                add(buildJsonObject { put("id", "item"); put("kind", "mail"); put("title", "Example") })
            })
            val recording = CompletableDeferred<JsonObject>()
            val started = CompletableDeferred<Unit>()
            transport.onRecord = { _, _, _, _ ->
                started.complete(Unit)
                recording.await()
            }
            transport.onSetDone = { done ->
                buildJsonObject {
                    put("id", "item"); put("kind", "mail"); put("title", "Example")
                    if (done) put("done_at", "2026-11-13T05:00:00Z")
                }
            }
            val agents = RecordSender()
            val repository = recordTestRepository(transport, directory, backgroundScope, agents)
            repository.snapshot.first { it.contentSource != null }
            repository.refresh()
            val id = repository.snapshot.value.records.single().item.id
            withTimeout(1_000) {
                repository.handOff(InboxHandOffRequest(id, "Handle this", "laptop", "agent", "op-1", targetTitle = "Example agent"))
            }
            started.await()

            // The user marks it done while the recording is still on its way.
            repository.setDone(id, true)
            assertNotNull("the item should read done at once", repository.snapshot.value.records.single().item.doneAt)

            // The recording's answer was built before that: no done_at in it.
            recording.complete(buildJsonObject {
                put("item", buildJsonObject { put("id", "item"); put("kind", "mail"); put("title", "Recorded") })
            })
            advanceUntilIdle()
            assertNotNull("a recording answer must not reopen a done item",
                repository.snapshot.value.records.single().item.doneAt)
        } finally {
            // The directory stays: a background recording may still be writing the reply journal when the test ends,
            // and deleting it under that write fails the test with its NoSuchFileException instead of an assertion.
        }
    }

    /** Persona's own hand-off never records an Agent: the main-chat route is the record, and it keeps its receipt. */
    @Test fun aMainChatHandOffRecordsNoAgent() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-record-main").toFile()
        try {
            val transport = RecordTransport(JsonArray(emptyList()))
            transport.onMainHandOff = { _, _, _ -> buildJsonObject {
                put("session", buildJsonObject { put("machine_id", "hub-machine"); put("session_id", "main") })
            } }
            val agents = RecordSender()
            val repository = recordTestRepository(transport, directory, backgroundScope, agents)
            assertEquals(SessionKey("hub-machine", "main"),
                repository.handOff(InboxHandOffRequest("item", "Summarize", operationId = "op-main")))
            advanceUntilIdle()
            assertEquals(0, transport.records)
            assertTrue(agents.sent.isEmpty())
        } finally {
            // The directory stays: a background recording may still be writing the reply journal when the test ends,
            // and deleting it under that write fails the test with its NoSuchFileException instead of an assertion.
        }
    }

    @Test fun deliveredRecordReplaysAfterOwnerDiesAfterHubSuccess() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-record-replay").toFile()
        val scope = "hub/generation"
        val key = SessionKey("laptop", "agent")
        val request = InboxHandOffRequest(InboxItemKey.of("item", "m-2"), "Handle this", key.machineId,
            key.sessionId, "replay-client", targetTitle = "Example agent")
        val delivered = InboxAgentHandOff(scope, request, "item", manager = "m-2", delivered = true)
        InboxReplyJournal(directory, scope).update(scope) { it.enqueue(delivered) }
        var secondJob: Job? = null
        val firstJob = SupervisorJob(coroutineContext[Job])
        try {
            val first = RecordTransport(JsonArray(emptyList()))
            val firstCalled = CompletableDeferred<Unit>()
            first.onRecord = { item, session, title, client ->
                assertEquals("item", item)
                assertEquals(key, session)
                assertEquals("Example agent", title)
                assertEquals("replay-client", client)
                assertEquals("m-2", currentCoroutineContext()[RequestManager]?.id)
                firstCalled.complete(Unit)
                firstJob.cancel() // The hub succeeded, but the owner dies before its local completion write.
                JsonObject(emptyMap())
            }
            recordTestRepository(first, directory, CoroutineScope(coroutineContext + firstJob), RecordSender())
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { firstCalled.await(); firstJob.join() }
            }
            assertEquals(1, first.records)
            val interrupted = InboxReplyJournal(directory, scope).read(scope).agentHandOffs.single()
            assertTrue(interrupted.delivered)
            assertFalse(interrupted.recorded)

            val second = RecordTransport(JsonArray(emptyList()))
            val secondCalled = CompletableDeferred<Unit>()
            second.onRecord = { _, _, _, client ->
                assertEquals("replay-client", client)
                assertEquals("m-2", currentCoroutineContext()[RequestManager]?.id)
                secondCalled.complete(Unit)
                JsonObject(emptyMap())
            }
            secondJob = SupervisorJob(coroutineContext[Job])
            recordTestRepository(second, directory,
                CoroutineScope(coroutineContext + checkNotNull(secondJob)), RecordSender())
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) {
                    secondCalled.await()
                    val journal = InboxReplyJournal(directory, scope)
                    while (!journal.read(scope).agentHandOffs.single().recorded) delay(10)
                }
            }
            assertEquals(1, second.records)

            AuthoritativeMessageDeliveries.publish(AuthoritativeMessageDelivery(scope, request.operationId, key, request.text))
            runCurrent()
            assertEquals("the durable completion tombstone suppresses later evidence", 1, second.records)
        } finally {
            secondJob?.cancelAndJoin()
            firstJob.cancelAndJoin()
            // Replay and completion writes use real IO; leave the temporary directory for any final background write.
        }
    }

    @Test fun crossGroupAcceptedWhileOriginOwnerIsClosedReplaysOnOriginReopen() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-cross-group-replay").toFile()
        val scope = "hub/generation"
        val target = SessionKey("laptop~g12345678", "agent")
        val request = InboxHandOffRequest("item", "Handle this", target.machineId, target.sessionId,
            "cross-group-replay", targetTitle = "Example agent")
        val pending = InboxAgentHandOff(scope, request, "item", manager = null, delivered = false)
        val journal = InboxReplyJournal(directory, scope)
        journal.update(scope) { it.enqueue(pending) }

        // The target hub commits and publishes while no origin repository exists. The stream has no replay, and the
        // target journal cannot be read by the origin journal owner. This is the durable state left by that acceptance.
        AuthoritativeMessageDeliveries.publish(AuthoritativeMessageDelivery(
            "agent-hub/generation", request.operationId, target, request.text,
            pending.copy(delivered = true), originCommitted = false,
        ))

        val transport = RecordTransport(JsonArray(emptyList()))
        recordTestRepository(transport, directory, backgroundScope, RecordSender())
        runCurrent()

        assertEquals("the origin must recover target-committed acceptance after reopening", 1, transport.records)
    }

    @Test fun crossGroupFailedRecordIsNotRepeatedByStartupJournalReplay() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-cross-group-ordered-replay").toFile()
        val scope = "hub/generation"
        val target = SessionKey("laptop~g12345678", "agent")
        val request = InboxHandOffRequest("item", "Handle this", target.machineId, target.sessionId,
            "cross-group-ordered-replay", targetTitle = "Example agent")
        val pending = InboxAgentHandOff(scope, request, "item", manager = null, delivered = false)
        val journal = InboxReplyJournal(directory, scope)
        journal.update(scope) {
            it.enqueue(pending).enqueue(InboxPendingReply(
                ReplyTarget.Lark("recovery-feed"), ReplyRequest("Recovery probe", "recovery-probe"), state = "unknown",
            ))
        }

        AuthoritativeMessageDeliveries.publish(AuthoritativeMessageDelivery(
            "agent-hub/generation", request.operationId, target, request.text,
            pending.copy(delivered = true), originCommitted = false,
        ))

        // Hold the reopen's startup journal write, not the recording. Buffered evidence can finish its failed record;
        // only then may the same opening's online journal replay inspect the still-unrecorded hand-off.
        val journalLocked = CompletableDeferred<Unit>()
        val releaseJournal = CompletableDeferred<Unit>()
        val lock = backgroundScope.launch(Dispatchers.IO) {
            journal.update(scope) {
                journalLocked.complete(Unit)
                runBlocking { releaseJournal.await() }
                it
            }
        }
        try {
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { journalLocked.await() }
            }
            val transport = RecordTransport(JsonArray(emptyList()))
            val firstAttempt = CompletableDeferred<Unit>()
            val recovered = CompletableDeferred<Unit>()
            transport.onRecord = { item, session, title, client ->
                assertEquals("item", item)
                assertEquals(target, session)
                assertEquals("Example agent", title)
                assertEquals(request.operationId, client)
                firstAttempt.complete(Unit)
                throw UnsupportedOperationException("Inbox Agent hand-off route unavailable")
            }
            recordTestRepository(transport, directory, backgroundScope, RecordSender(), lookupReply = { target, id ->
                assertEquals(ReplyTarget.Lark("recovery-feed"), target)
                assertEquals("recovery-probe", id)
                recovered.complete(Unit)
                null
            })
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { firstAttempt.await() }
            }
            runCurrent()
            assertEquals("buffered evidence has made its one recording attempt", 1, transport.records)

            releaseJournal.complete(Unit)
            // Reply recovery follows hand-off replay. Its lookup proves that replay has scheduled all its work without
            // requiring a duplicate attempt that a correct implementation will never make.
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { recovered.await() }
            }
            runCurrent()
            assertEquals("startup journal replay must not repeat the same recording attempt", 1, transport.records)
        } finally {
            releaseJournal.complete(Unit)
            lock.cancelAndJoin()
            // Recording and journal completion use real IO; leave the temporary directory for any final write.
        }
    }

    @Test fun failedJournalRecordRetriesOnLaterReconnect() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-reconnect-retry").toFile()
        val scope = "hub/generation"
        val target = SessionKey("laptop~g12345678", "agent")
        val request = InboxHandOffRequest("item", "Handle this", target.machineId, target.sessionId,
            "cross-group-reconnect-retry", targetTitle = "Example agent")
        val pending = InboxAgentHandOff(scope, request, "item", manager = null, delivered = true)
        val journal = InboxReplyJournal(directory, scope)
        journal.update(scope) { it.enqueue(pending) }
        val states = MutableStateFlow(ConnectionState.Online)
        val hub = object : HubConnection {
            override val state = states
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = false
        }
        val transport = RecordTransport(JsonArray(emptyList()))
        val firstAttempt = CompletableDeferred<Unit>()
        val secondAttempt = CompletableDeferred<Unit>()
        transport.onRecord = { _, _, _, _ ->
            when (transport.records) {
                1 -> {
                    firstAttempt.complete(Unit)
                    throw java.io.IOException("temporary recording failure")
                }
                2 -> {
                    secondAttempt.complete(Unit)
                    JsonObject(emptyMap())
                }
                else -> error("unexpected recording attempt")
            }
        }
        recordTestRepository(transport, directory, backgroundScope, RecordSender(), hubConnection = hub)
        withContext(Dispatchers.Default.limitedParallelism(1)) {
            withTimeout(5_000) { firstAttempt.await() }
        }
        runCurrent()
        assertEquals("the first journal replay makes one attempt", 1, transport.records)

        states.value = ConnectionState.Connecting
        runCurrent()
        states.value = ConnectionState.Online
        runCurrent()
        withContext(Dispatchers.Default.limitedParallelism(1)) {
            withTimeout(5_000) {
                secondAttempt.await()
                while (!journal.read(scope).agentHandOffs.single().recorded) yield()
            }
        }
        assertEquals("a later reconnect retries the delivered but unrecorded hand-off", 2, transport.records)
        // Recording and journal completion use real IO; leave the temporary directory for any final write.
    }

    @Test fun reconnectDuringStartupAllowsJournalRetry() = runTest {
        val directory = Files.createTempDirectory("inbox-handoff-startup-reconnect").toFile()
        val scope = "hub/generation"
        val target = SessionKey("laptop~g12345678", "agent")
        val request = InboxHandOffRequest("item", "Handle this", target.machineId, target.sessionId,
            "cross-group-startup-reconnect", targetTitle = "Example agent")
        val pending = InboxAgentHandOff(scope, request, "item", manager = null, delivered = false)
        val journal = InboxReplyJournal(directory, scope)
        journal.update(scope) {
            it.enqueue(pending).enqueue(InboxPendingReply(
                ReplyTarget.Lark("startup-reconnect-feed"), ReplyRequest("Recovery probe", "startup-reconnect-probe"),
                state = "unknown",
            ))
        }
        AuthoritativeMessageDeliveries.publish(AuthoritativeMessageDelivery(
            "agent-hub/generation", request.operationId, target, request.text,
            pending.copy(delivered = true), originCommitted = false,
        ))
        val states = MutableStateFlow(ConnectionState.Connecting)
        val hub = object : HubConnection {
            override val state = states
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = false
        }
        val journalLocked = CompletableDeferred<Unit>()
        val releaseJournal = CompletableDeferred<Unit>()
        val lock = backgroundScope.launch(Dispatchers.IO) {
            journal.update(scope) {
                journalLocked.complete(Unit)
                runBlocking { releaseJournal.await() }
                it
            }
        }
        try {
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { journalLocked.await() }
            }
            val transport = RecordTransport(JsonArray(emptyList()))
            val firstAttempt = CompletableDeferred<Unit>()
            val recovered = CompletableDeferred<Unit>()
            transport.onRecord = { _, _, _, _ ->
                if (transport.records == 1) {
                    firstAttempt.complete(Unit)
                    throw java.io.IOException("temporary recording failure")
                }
                JsonObject(emptyMap())
            }
            recordTestRepository(transport, directory, backgroundScope, RecordSender(), hubConnection = hub,
                lookupReply = { target, id ->
                    assertEquals(ReplyTarget.Lark("startup-reconnect-feed"), target)
                    assertEquals("startup-reconnect-probe", id)
                    recovered.complete(Unit)
                    null
                })
            runCurrent()
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { firstAttempt.await() }
            }
            runCurrent()
            assertEquals("buffered evidence has made its one recording attempt", 1, transport.records)

            // The startup write has not subscribed the recovery collector yet. The state watcher must nevertheless
            // observe this Online → Connecting → Online cycle; subscribing only to its final value would conceal it.
            states.value = ConnectionState.Online
            runCurrent()
            states.value = ConnectionState.Connecting
            runCurrent()
            states.value = ConnectionState.Online
            runCurrent()
            releaseJournal.complete(Unit)
            withContext(Dispatchers.Default.limitedParallelism(1)) {
                withTimeout(5_000) { recovered.await() }
            }
            runCurrent()
            assertEquals("a reconnect during startup must allow the journal retry", 2, transport.records)
        } finally {
            releaseJournal.complete(Unit)
            lock.cancelAndJoin()
            // Recording and journal completion use real IO; leave the temporary directory for any final write.
        }
    }

    private fun recordTestRepository(
        transport: RecordTransport, directory: java.io.File, owner: CoroutineScope, agents: MessageSender,
        hubConnection: HubConnection? = null,
        lookupReply: suspend (ReplyTarget, String) -> InboxReplyReceipt? = { _, _ -> null },
    ) = RealInboxRepository(
        object : InboxAssistantConnection { override val current = MutableStateFlow<InboxAssistantTransport?>(transport) },
        InboxReplyJournal(directory, "hub/generation"),
        object : SessionsRepository {
            override val machines = MutableStateFlow<List<Machine>>(emptyList())
            override val sessions = MutableStateFlow<List<Session>>(emptyList())
            override suspend fun refresh() = Unit
        },
        object : InteractionCenter {
            override val pending = MutableStateFlow<List<Interaction>>(emptyList())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }, owner, messageSender = agents, hubConnection = hubConnection, lookupReply = lookupReply,
    ) { _, _ -> error("unused") }

    private class RecordFailure(override val status: Int) : java.io.IOException("HTTP $status"), HubCodedFailure {
        override val code: String? = null
    }

    private class RecordSender : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Triple<SessionKey, String, String>>()
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += Triple(session, text, clientMessageId)
            // This fake's hub returned Accepted; local enqueue or a Sent outbox row would not be such evidence.
            AuthoritativeMessageDeliveries.publish(AuthoritativeMessageDelivery("hub/generation", clientMessageId, session, text))
        }
    }

    private class RecordTransport(private val items: JsonArray, override val scope: String = "hub/generation") : InboxAssistantTransport {
        var records = 0
        var onRecord: suspend (String, SessionKey, String, String) -> JsonObject = { _, _, _, _ ->
            throw UnsupportedOperationException("Inbox Agent hand-off route unavailable")
        }
        override suspend fun recordAgentHandOff(itemId: String, session: SessionKey, title: String, clientId: String): JsonObject {
            records++
            return onRecord(itemId, session, title, clientId)
        }
        var onMainHandOff: suspend (String, String, String) -> JsonObject = { _, _, _ ->
            throw UnsupportedOperationException("Inbox hand-off route unavailable")
        }
        override suspend fun handOff(itemId: String, text: String, clientId: String) = onMainHandOff(itemId, text, clientId)
        override suspend fun inbox() = buildJsonObject { put("items", items) }
        var onSetDone: suspend (Boolean) -> JsonObject = { JsonObject(emptyMap()) }
        override suspend fun setDone(itemId: String, done: Boolean) = onSetDone(done)
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = buildJsonObject {
            put("messages", JsonArray(emptyList())); put("sync_state", "complete")
        }
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = buildJsonObject { put("spaces", JsonArray(emptyList())); put("sessions", JsonArray(emptyList())) }
        override suspend fun media(ref: String) = byteArrayOf()
    }
}
