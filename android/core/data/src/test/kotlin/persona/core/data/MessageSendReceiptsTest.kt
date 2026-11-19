package persona.core.data

import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*
import persona.core.contracts.api.*
import java.time.Instant

@OptIn(ExperimentalCoroutinesApi::class)
class MessageSendReceiptsTest {
    @get:org.junit.Rule val shadowParity = ShadowParityRule()
    private val key = SessionKey("computer", "chat")
    private val at = Instant.parse("2026-11-12T10:00:00Z")
    private val row = Session(key.machineId, key.sessionId, status = "idle")
    private fun event(state: String = "processing", id: String = "operation", nanos: Long = 0) =
        MessageSendStateEvent(key.machineId, key.sessionId, id, state, at.plusNanos(nanos).toString(), socketGeneration = 1)
    private fun reducer() = MessageSendReceiptState { at.toEpochMilli() }

    @Test fun aRemoteReceiptNeedsNoLocalOutboxAndDeliveryKeepsProcessingUntilRuntimeTakesOver() {
        val state = reducer()
        state.sessions(listOf(row))
        state.event(event())
        assertTrue(state.receipts.value.single().processing)
        state.event(event("delivered", nanos = 1))
        assertTrue(state.receipts.value.single().processing)
        state.sessions(listOf(row.copy(rev = 2))) // Late unchanged Idle snapshot, not runtime evidence.
        assertTrue(state.receipts.value.single().processing)
        state.sessions(listOf(row.copy(status = "working", state = "working")))
        assertTrue(state.receipts.value.isEmpty())
        state.sessions(listOf(row))
        state.event(event(nanos = 2))
        assertTrue("late receipts cannot resurrect a completed projection", state.receipts.value.isEmpty())
        state.event(event(id = "new-operation", nanos = 3))
        assertTrue(state.receipts.value.single().processing)
    }

    @Test fun keptReceiptIsTerminalWithoutProcessingOrDelivery() {
        val state = reducer()
        state.sessions(listOf(row))
        state.event(event("kept"))
        val kept = state.receipts.value.single()
        assertEquals("kept", kept.state)
        assertFalse(kept.processing)
        state.sessions(listOf(row.copy(status = "working", state = "working")))
        assertTrue(state.receipts.value.isEmpty())
    }

    @Test fun anEchoBeforeHttpKeepsReceiptWithoutRestoringTheOutboxBubble() = runTest {
        val connection = FakeConnection().apply {
            lastHello = Hello(hubId = "hub", features = listOf("message_send_state"), messageSendState = 1)
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val sessions = MutableStateFlow(listOf(row))
        val http = CompletableDeferred<MessageTransportResult>()
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest) = http.await()
        }
        val store = MemoryOutboxStore()
        val sender = OutboxMessageSender(HubScope("hub", "generation"), store, transport, backgroundScope) { at.toEpochMilli() }
        sender.connectSendReceipts(connection, sessions)
        sender.send(key, "hello", "operation")
        runCurrent()
        connection.events.emit(event())
        runCurrent()
        assertEquals(OutboxState.Sending, store.state("operation"))
        assertTrue("receipt is not durable delivery", sender.sendReceipts.value.single().processing)
        sender.observeTranscript(key, listOf(Item("runtime-user", 1, role = "user", kind = "text", text = "hello",
            clientMsgId = "operation", ts = at.toString())))
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("operation"))
        assertTrue(sender.outbox.value.isEmpty())
        assertTrue(sender.sendReceipts.value.single().processing)
        http.complete(MessageTransportResult.Accepted)
        runCurrent()
        assertEquals(OutboxState.Shown, store.state("operation"))
        assertTrue(sender.sendReceipts.value.single().processing)
        sessions.value = listOf(row.copy(status = "working", state = "working"))
        runCurrent()
        assertTrue(sender.sendReceipts.value.isEmpty())
        sender.close()
    }

    @Test fun httpFailureAndUnknownClearOnlyTheirOwnAttemptAndKeepDurableReview() = runTest {
        val connection = FakeConnection().apply {
            lastHello = Hello(hubId = "hub", features = listOf("message_send_state"), messageSendState = 1)
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val http = CompletableDeferred<MessageTransportResult>()
        val store = MemoryOutboxStore()
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest) = http.await()
        }
        val sender = OutboxMessageSender(HubScope("hub", "generation"), store, transport, backgroundScope) { at.toEpochMilli() }
        sender.connectSendReceipts(connection, MutableStateFlow(listOf(row)))
        sender.send(key, "hello", "operation")
        runCurrent()
        connection.events.emit(event())
        runCurrent()
        http.complete(MessageTransportResult.NoReceipt)
        runCurrent()
        assertEquals(OutboxState.Unknown, store.state("operation"))
        assertTrue(sender.sendReceipts.value.isEmpty())
        connection.events.emit(event(nanos = 10))
        runCurrent()
        assertEquals(OutboxState.Unknown, store.state("operation"))
        assertTrue(sender.sendReceipts.value.isEmpty())
        sender.close()
    }

    @Test fun nanosecondOrderingFailureAndRetryIdsDoNotCrossAttempts() {
        val state = reducer()
        val first = MessageRequest(HubScope("hub", "generation"), key, "hello", "client", opId = "first")
        val second = first.copy(opId = "second")
        state.begin(first, true)
        state.event(event(id = "first", nanos = 2))
        assertEquals("client", state.receipts.value.single().clientMessageId)
        state.event(event("unknown", "first", nanos = 3))
        state.event(event(id = "first", nanos = 1))
        assertEquals("unknown", state.receipts.value.single().state)
        state.result(first, MessageTransportResult.NoReceipt)
        state.begin(second, true)
        state.event(event(id = "second", nanos = 4))
        state.result(first, MessageTransportResult.HubError("invalid", "none"))
        assertTrue(state.receipts.value.single().processing)
        state.discard("client")
        state.event(event("delivered", "second", nanos = 5))
        assertTrue(state.receipts.value.isEmpty())
    }

    @Test fun cachedCompletedIdsStayCompletedAcrossReconnectButNewRemoteIdsStillShow() {
        val state = reducer()
        state.transcript(key, listOf(
            Item("user", 1, role = "user", kind = "text", clientMsgId = "old"),
            Item("answer", 2, role = "assistant", kind = "text", text = "done"),
        ))
        state.reconnect()
        state.event(event(id = "old"))
        assertTrue(state.receipts.value.isEmpty())
        state.event(event(id = "remote"))
        assertTrue(state.receipts.value.single().processing)
        state.event(event("delivered", "remote", nanos = 1))
        state.finishTurn(key)
        state.event(event("delivered", "remote", nanos = 2))
        assertTrue(state.receipts.value.isEmpty())
    }

    @Test fun waitingQuestionsAndCompletionSupersedeReceiptsWithoutChangingSessionState() {
        val state = reducer()
        state.sessions(listOf(row))
        state.event(event())
        val waiting = row.copy(status = "working", state = "needs_you")
        state.sessions(listOf(waiting))
        assertTrue(state.receipts.value.isEmpty())
        assertEquals(AgentState.NeedsYou, waiting.agentState)
        state.event(event(id = "another"))
        state.finishTurn(key)
        assertTrue("the preceding turn's Stop cannot finish hub preflight", state.receipts.value.single().processing)
        state.event(event("delivered", "another", nanos = 1))
        state.finishTurn(key)
        assertTrue(state.receipts.value.isEmpty())
    }

    @Test fun aLateProcessingReceiptAfterHttpStillShowsUntilGenuineRuntimeEvidence() {
        val state = reducer()
        val request = MessageRequest(HubScope("hub", "generation"), key, "hello", "operation")
        state.sessions(listOf(row))
        state.begin(request, false)
        state.result(request, MessageTransportResult.Accepted)
        assertTrue(state.receipts.value.isEmpty())
        state.event(event())
        assertTrue(state.receipts.value.single().processing)
        state.sessions(listOf(row.copy(status = "working", state = "working", stateSince = at.minusSeconds(1).toString())))
        assertTrue("a stale earlier Working snapshot is not this request", state.receipts.value.single().processing)
        state.sessions(listOf(row.copy(status = "working", state = "working", stateSince = at.plusSeconds(1).toString())))
        assertTrue(state.receipts.value.isEmpty())
    }

    @Test fun aFirstThinkingOrTextSegmentDoesNotRemoveProcessingBeforeRuntimeCatchesUp() {
        val state = reducer()
        val request = MessageRequest(HubScope("hub", "generation"), key, "hello", "operation")
        state.begin(request, false)
        state.event(event())
        val user = Item("user", 1, role = "user", kind = "text", clientMsgId = "operation")
        state.transcript(key, listOf(user, Item("thinking", 2, role = "assistant", kind = "thinking")))
        assertTrue(state.receipts.value.single().processing)
        state.transcript(key, listOf(user, Item("text", 3, role = "assistant", kind = "text", text = "Starting")))
        assertTrue(state.receipts.value.single().processing)
        state.finishTurn(key)
        assertTrue(state.receipts.value.isEmpty())
    }

    @Test fun unresolvedMainCreationStaysLocallySendingWithoutClaimingHubReceipt() = runTest {
        val connection = FakeConnection().apply {
            lastHello = Hello(hubId = "hub", features = listOf("message_send_state"), messageSendState = 1)
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val store = MemoryOutboxStore()
        val target = CompletableDeferred<SessionKey?>()
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest) = MessageTransportResult.Accepted
        }
        val sender = OutboxMessageSender(HubScope("hub", "generation"), store, transport, backgroundScope)
        sender.connectSendReceipts(connection, MutableStateFlow(listOf(row)))
        sender.connectMainConversation(object : MainConversationResolver {
            override suspend fun findMainConversation() = target.await()
            override suspend fun createMainConversation() = key
        })
        sender.sendToMain("hello", "main-message")
        runCurrent()
        assertEquals(OutboxState.Queued, store.state("main-message"))
        val preparing = sender.sendReceipts.value.single()
        assertEquals(PendingMainConversation, preparing.session)
        assertEquals("sending", preparing.state)
        assertFalse(preparing.processing)
        target.complete(key)
        runCurrent()
        assertEquals(OutboxState.Sent, store.state("main-message"))
        assertTrue("no event means no fabricated Processing", sender.sendReceipts.value.isEmpty())
        sender.close()
    }

    @Test fun onlyEachOwningHubsAuthenticatedCapabilityEnablesReceipts() = runTest {
        val old = FakeConnection().apply {
            lastHello = Hello(hubId = "legacy", features = listOf("message_send_state"))
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val modern = FakeConnection().apply {
            lastHello = Hello(hubId = "modern", features = listOf("message_send_state"), messageSendState = 1)
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest) = MessageTransportResult.Accepted
        }
        val legacy = OutboxMessageSender(HubScope("legacy", "generation"), MemoryOutboxStore(), transport, backgroundScope)
        val trusted = OutboxMessageSender(HubScope("modern", "generation"), MemoryOutboxStore(), transport, backgroundScope)
        legacy.connectSendReceipts(old, MutableStateFlow(listOf(row)))
        trusted.connectSendReceipts(modern, MutableStateFlow(listOf(row)))
        // An event that looks like a hello is not the connection's authenticated, identity-checked lastHello.
        old.events.emit(Hello(hubId = "legacy", features = listOf("message_send_state")))
        old.events.emit(event())
        modern.events.emit(event())
        runCurrent()
        assertTrue(legacy.sendReceipts.value.isEmpty())
        assertTrue(trusted.sendReceipts.value.single().processing)
        old.lastHello = null
        old.currentHello.value = null
        old.events.emit(event(id = "before-hello"))
        runCurrent()
        assertTrue(legacy.sendReceipts.value.isEmpty())
        assertTrue(legacy.outbox.value.isEmpty())
        assertTrue(trusted.outbox.value.isEmpty())
        // UI Online and cached lastHello survive grace, but authentication for the lost socket does not.
        modern.currentHello.value = null
        modern.events.emit(event(id = "during-grace"))
        runCurrent()
        assertEquals(ConnectionState.Online, modern.state.value)
        assertEquals(1, modern.lastHello?.messageSendState)
        assertTrue(trusted.sendReceipts.value.isEmpty())
        modern.currentHello.value = SocketHello(2, Hello(hubId = "modern", features = listOf("message_send_state")))
        modern.events.emit(event(id = "legacy-replacement").copy(socketGeneration = 2))
        runCurrent()
        assertTrue(trusted.sendReceipts.value.isEmpty())
        modern.currentHello.value = SocketHello(3, Hello(hubId = "modern", messageSendState = 1))
        modern.events.emit(event(id = "stale-socket"))
        runCurrent()
        assertTrue(trusted.sendReceipts.value.isEmpty())
        modern.events.emit(event(id = "current-socket").copy(socketGeneration = 3))
        runCurrent()
        assertTrue(trusted.sendReceipts.value.single().processing)
        modern.currentHello.value = SocketHello(4, Hello(hubId = "modern", messageSendState = 2))
        modern.events.emit(event(id = "unsupported-version").copy(socketGeneration = 4))
        runCurrent()
        assertTrue(trusted.sendReceipts.value.isEmpty())
        legacy.send(key, "hello", "legacy-message")
        runCurrent()
        assertEquals(DeliveryState.Sent, legacy.outbox.value.single().state)
        assertTrue("old hubs retain local/HTTP fallback without a fabricated receipt", legacy.sendReceipts.value.isEmpty())
        legacy.close(); trusted.close()
    }

    @Test fun aPreviousWorkingTurnBecomingDoneCannotCloseUndeliveredPreflight() {
        for (since in listOf(null, at.plusSeconds(1).toString())) {
            for (delivery in listOf("none", "echo", "http")) {
                val state = reducer()
                val working = row.copy(status = "working", state = "working")
                state.sessions(listOf(working))
                val request = MessageRequest(HubScope("hub", "generation"), key, "hello", "operation")
                state.begin(request, false)
                state.event(event())
                if (delivery == "echo") state.transcript(key, listOf(Item("user", 1, role = "user", kind = "text", clientMsgId = "operation")))
                if (delivery == "http") state.result(request, MessageTransportResult.Accepted)
                val done = row.copy(status = "done", state = null, stateSince = since)
                state.sessions(listOf(done))
                assertEquals("Done is not delivery: $delivery / $since", delivery == "none", state.receipts.value.isNotEmpty())
                assertEquals(AgentState.Unread, done.agentState)
                if (delivery == "none") {
                    state.sessions(listOf(working.copy(stateSince = at.plusSeconds(2).toString())))
                    assertTrue("new Working evidence still takes over", state.receipts.value.isEmpty())
                }
            }
        }
    }

    @Test fun aFirstTranscriptSegmentBeforeTheSlowReceiptDoesNotCompleteTheActiveRequest() {
        for (kind in listOf("thinking", "text")) {
            val state = reducer()
            state.sessions(listOf(row))
            val request = MessageRequest(HubScope("hub", "generation"), key, "hello", "operation")
            state.begin(request, false)
            state.result(request, MessageTransportResult.Accepted)
            val user = Item("user", 1, role = "user", kind = "text", clientMsgId = "operation")
            val first = Item("first", 2, role = "assistant", kind = kind, text = "Starting")
            state.transcript(key, listOf(user, first))
            state.event(event())
            assertTrue("a first $kind segment is not completion while runtime is Idle", state.receipts.value.single().processing)
            state.transcript(key, listOf(user, first, Item("duration", 3, kind = "turn_duration")))
            assertTrue(state.receipts.value.isEmpty())
            state.event(event(nanos = 1))
            assertTrue("true completion still fences late receipts", state.receipts.value.isEmpty())
        }
    }

    @Test fun composerRetriesBindTheirNewOperationToTheOriginalVisibleMessage() = runTest {
        val connection = FakeConnection().apply {
            lastHello = Hello(hubId = "hub", messageSendState = 1)
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val sessions = MutableStateFlow(listOf(row))
        val store = MemoryOutboxStore()
        val transport = object : MessageTransport {
            override val readyToSend = MutableStateFlow(true)
            override val supportsClientOps = false
            override suspend fun send(request: MessageRequest) = MessageTransportResult.HubError("invalid", "none")
        }
        val sender = OutboxMessageSender(HubScope("hub", "generation"), store, transport, backgroundScope) { at.toEpochMilli() }
        sender.connectSendReceipts(connection, sessions)
        sender.send(key, "hello", "client")
        runCurrent()
        assertEquals(OutboxState.Refused, store.state("client"))
        sender.recordComposerDelivery("client", ComposerDelivery(DeliveryState.Sending), "retry-operation")
        assertEquals("sending", sender.sendReceipts.value.single().state)
        connection.events.emit(event(id = "retry-operation"))
        runCurrent()
        assertEquals("client", sender.sendReceipts.value.single().clientMessageId)
        assertTrue(sender.sendReceipts.value.single().processing)
        assertEquals(OutboxState.Sending, store.state("client"))
        sender.recordComposerDelivery("client", ComposerDelivery(DeliveryState.Sent), "retry-operation")
        assertEquals(OutboxState.Sent, store.state("client"))
        assertTrue(sender.sendReceipts.value.single().processing)
        sessions.value = listOf(row.copy(status = "working", state = "working"))
        runCurrent()
        assertTrue(sender.sendReceipts.value.isEmpty())

        sender.send(key, "another", "another-client")
        runCurrent()
        sender.recordComposerDelivery("another-client", ComposerDelivery(DeliveryState.Sending), "unknown-retry")
        connection.events.emit(event(id = "unknown-retry"))
        runCurrent()
        sender.recordComposerDelivery("another-client", ComposerDelivery(DeliveryState.Unknown), "unknown-retry")
        assertEquals(OutboxState.Unknown, store.state("another-client"))
        assertTrue(sender.sendReceipts.value.isEmpty())
        connection.events.emit(event(id = "unknown-retry", nanos = 1))
        runCurrent()
        assertTrue(sender.sendReceipts.value.isEmpty())
        sender.close()
    }

    @Test fun aSendWaitsForItsSubscriptionBeforePostAndUsesTheLegacyBoundedFallback() = runTest {
        val connection = FakeConnection().apply {
            lastHello = Hello(hubId = "hub", features = listOf("message_send_state"), messageSendState = 1)
            currentHello.value = SocketHello(1, lastHello!!)
        }
        val sessions = FakeSessions()
        val transport = FakeChatTransport().apply { fetch = { ItemsResponse(emptyList()) } }
        val repository = HubChatRepository(transport, connection, FakeItemsCache(CachedChat()), sessions, backgroundScope)
        var posted = false
        val sending = async { repository.withSendSubscription(key) { posted = true; MessageTransportResult.Accepted } }
        runCurrent()
        assertTrue(connection.sent.any { it["type"].toString() == "\"subscribe\"" })
        assertFalse(posted)
        connection.events.emit(ItemsEvent(key.machineId, key.sessionId, backlog = true))
        runCurrent()
        assertEquals(MessageTransportResult.Accepted, sending.await())
        assertTrue(posted)
        val missing = async { repository.withSendSubscription(SessionKey("computer", "legacy")) { MessageTransportResult.Accepted } }
        runCurrent()
        advanceTimeBy(3_001)
        runCurrent()
        assertEquals(MessageTransportResult.Accepted, missing.await())
    }
}
