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
class InboxHandOffRecordTest {
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

    private fun recordTestRepository(
        transport: RecordTransport, directory: java.io.File, owner: CoroutineScope, agents: MessageSender,
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
        }, owner, messageSender = agents,
    ) { _, _ -> error("unused") }

    private class RecordFailure(override val status: Int) : java.io.IOException("HTTP $status"), HubCodedFailure {
        override val code: String? = null
    }

    private class RecordSender : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val sent = mutableListOf<Triple<SessionKey, String, String>>()
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
            sent += Triple(session, text, clientMessageId)
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
