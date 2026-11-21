package persona.core.data

import java.io.IOException
import java.nio.file.Files
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.After
import org.junit.Assert.*
import org.junit.Before
import org.junit.Test
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*

@OptIn(ExperimentalCoroutinesApi::class)
class AssistantSwitchIsolationTest {
    @Before fun before() {
        AssistantManagerChoice.forgetItems()
        AssistantManagerChoice.choose("hub", null, "assistant")
    }
    @After fun after() {
        AssistantManagerChoice.reset()
        AssistantManagerChoice.forgetItems()
    }

    @Test fun switchingBackRestoresItsOwnHistoryEvenWhenOffline() = runTest {
        val transport = Transport()
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        assertEquals("assistant-main", repository.assistant.value.mainSession?.sessionId)
        AssistantManagerChoice.pick("work")
        runCurrent()
        assertEquals("work-main", repository.assistant.value.mainSession?.sessionId)
        transport.read = { throw IOException("offline") }
        AssistantManagerChoice.pick(null)
        runCurrent()
        assertEquals(listOf("assistant-main"), repository.assistant.value.conversations.map { it.sessionId })
        AssistantManagerChoice.pick("work")
        runCurrent()
        assertEquals(listOf("work-main"), repository.assistant.value.conversations.map { it.sessionId })
    }

    @Test fun uncancellableLateResponseDoesNotBlockSwitchOrOverwriteNewManager() = runTest {
        val oldResponse = CompletableDeferred<JsonObject>()
        val transport = Transport().apply {
            read = { manager ->
                if (manager == null) withContext(NonCancellable) { oldResponse.await() } else config(manager)
            }
        }
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        AssistantManagerChoice.pick("work")
        runCurrent()
        assertEquals("work-main", repository.assistant.value.mainSession?.sessionId)
        oldResponse.complete(config(null))
        runCurrent()
        assertEquals("work", repository.assistant.value.manager)
        assertEquals("work-main", repository.assistant.value.mainSession?.sessionId)
    }

    @Test fun olderRefreshForSameManagerCannotReplaceNewerHistory() = runTest {
        val transport = Transport()
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        val oldResponse = CompletableDeferred<JsonObject>()
        transport.read = { oldResponse.await() }
        val old = async { repository.refresh() }
        runCurrent()
        transport.read = { config(null, "new-main") }
        repository.refresh()
        oldResponse.complete(config(null, "old-main"))
        old.await()
        assertEquals("new-main", repository.assistant.value.mainSession?.sessionId)
    }

    @Test fun conversationTagFiltersReadAbsentNullEmptyAndExactOrder() = runTest {
        val transport = Transport()
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        assertEquals(ConversationTagFilters.Unsupported, repository.assistant.value.conversationTagFilters)

        fun response(value: JsonElement) = JsonObject(config(null).toMutableMap().apply {
            put("conversation_tag_filters", value)
        })
        transport.read = { response(JsonNull) }
        repository.refresh()
        assertTrue(repository.assistant.value.conversationTagFilters.supported)
        assertNull(repository.assistant.value.conversationTagFilters.entries)

        transport.read = { response(JsonArray(emptyList())) }
        repository.refresh()
        assertEquals(ConversationTagFilters(emptyList()), repository.assistant.value.conversationTagFilters)

        transport.read = { response(JsonArray(listOf(JsonPrimitive("second"), JsonPrimitive("first")))) }
        repository.refresh()
        assertEquals(listOf("second", "first"), repository.assistant.value.conversationTagFilters.entries)
    }

    @Test fun recentTaskChatsApplyLocallyAndFollowTheNextRefresh() = runTest {
        val transport = Transport()
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        assertNull(repository.assistant.value.recentTaskChats)
        repository.updateRecentTaskChats(true)
        assertEquals(true, repository.assistant.value.recentTaskChats)
        transport.recentTaskChats = false
        repository.refresh()
        assertEquals(false, repository.assistant.value.recentTaskChats)
        transport.recentTaskChats = null
        repository.refresh()
        assertNull(repository.assistant.value.recentTaskChats)
    }

    @Test fun lateRefreshCannotEraseRecentTaskChatsUpdate() = runTest {
        val transport = Transport().apply { recentTaskChats = false }
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        val response = CompletableDeferred<JsonObject>()
        transport.read = { response.await() }
        val refresh = async { repository.refresh() }
        runCurrent()
        repository.updateRecentTaskChats(true)
        response.complete(config(null, recentTaskChats = false))
        refresh.await()
        assertEquals(true, repository.assistant.value.recentTaskChats)
    }

    @Test fun recentTaskChatsUpdaterKeepsRollbackWithOriginalManager() = runTest {
        val transport = Transport().apply { read = { config(it, recentTaskChats = it != null) } }
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        assertEquals(false, repository.assistant.value.recentTaskChats)
        val update = repository.recentTaskChatsUpdater()
        update(true)
        AssistantManagerChoice.pick("work")
        runCurrent()
        assertEquals(true, repository.assistant.value.recentTaskChats)

        update(false)

        assertEquals("A's rollback must not rewrite B", true, repository.assistant.value.recentTaskChats)
        transport.read = { manager -> if (manager == null) throw IOException("offline") else config(manager, recentTaskChats = true) }
        AssistantManagerChoice.pick(null)
        runCurrent()
        assertEquals("A's cached optimistic value is rolled back too", false, repository.assistant.value.recentTaskChats)
    }

    @Test fun mainLookupPinsTheReturnedConversationBeforePageSwitchAndSend() = runTest {
        val repository = RealAssistantRepository(Connection(Transport()), Sessions(), backgroundScope)
        assertEquals(SessionKey("mac", "work-main"), repository.mainConversationOf("work"))
        AssistantManagerChoice.pick("another")
        assertEquals("work", header("work-main"))
    }

    @Test fun queuedCreationKeepsTheManagerCapturedBeforeWaitingForOtherCreation() = runTest {
        val firstConfig = CompletableDeferred<JsonObject>()
        val transport = Transport().apply {
            read = { manager -> if (manager == "busy") firstConfig.await() else config(manager, "") }
        }
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        val first = async { repository.mainConversationOf("busy") }
        runCurrent()
        AssistantManagerChoice.pick("work")
        val second = async { repository.startConversation() }
        runCurrent()
        AssistantManagerChoice.pick("another")
        runCurrent()
        firstConfig.complete(config("busy"))
        first.await()
        val key = second.await()
        assertEquals(listOf("work"), transport.creates)
        assertEquals("work", header(key.sessionId))
        assertEquals("another", repository.assistant.value.manager)
        assertFalse(repository.assistant.value.conversations.any { it.key == key })
    }

    @Test fun sessionUpdatesFromAnotherManagerCannotReplaceCurrentManagersRows() = runTest {
        val transport = Transport().apply { read = { config(it, "shared-id") } }
        val sessions = Sessions()
        val repository = RealAssistantRepository(Connection(transport), sessions, backgroundScope)
        AssistantManagerChoice.pick("work")
        runCurrent()
        sessions.sessions.value = listOf(
            Session(machineId = "mac", sessionId = "shared-id", title = "wrong", assistant = true, manager = "assistant"),
            Session(machineId = "mac", sessionId = "other", assistant = true, manager = "assistant"),
        )
        runCurrent()
        assertEquals(listOf("shared-id"), repository.assistant.value.conversations.map { it.sessionId })
        assertNotEquals("wrong", repository.assistant.value.conversations.single().title)
    }

    @Test fun offlineRestartRestoresOnlyTheSelectedManagersDiskSnapshot() = runTest {
        val directory = Files.createTempDirectory("assistant-switch").toFile()
        try {
            val snapshots = ListSnapshotStore(directory)
            snapshots.save("scope:assistant", config(null))
            snapshots.save("scope:assistant@work", config("work"))
            AssistantManagerChoice.pick("work")
            val transport = Transport().apply { read = { throw IOException("offline") } }
            val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope, snapshots)
            try {
                repository.refresh()
                fail("offline refresh should report failure")
            } catch (e: IOException) {
                assertEquals("offline", e.message)
            }
            assertEquals("work", repository.assistant.value.manager)
            assertEquals("work-main", repository.assistant.value.mainSession?.sessionId)
        } finally {
            directory.deleteRecursively()
        }
    }

    @Test fun pairingChangeDuringMainLookupFailsWithoutCreatingOrPinningInTheNewHub() = runTest {
        val response = CompletableDeferred<JsonObject>()
        val transport = Transport().apply { read = { response.await() } }
        val connection = Connection(transport)
        val repository = RealAssistantRepository(connection, Sessions(), backgroundScope)
        val lookup = async {
            try {
                repository.mainConversationOf("work")
                fail("pairing changed")
            } catch (e: IllegalStateException) {
                assertTrue(e.message.orEmpty().isNotBlank())
            }
        }
        runCurrent()
        connection.current.value = Transport()
        AssistantManagerChoice.choose("new-hub", "work", "assistant")
        response.complete(config("work", ""))
        lookup.await()
        assertTrue(transport.creates.isEmpty())
        assertNull(AssistantManagerChoice.itemHeader(listOf("assistant", "conversations", "work-main"), "new-hub"))
    }

    @Test fun lateRefreshCannotEraseConversationCreatedWhileItWasInFlight() = runTest {
        val transport = Transport()
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        val response = CompletableDeferred<JsonObject>()
        transport.read = { response.await() }
        val refresh = async { repository.refresh() }
        runCurrent()
        transport.read = { config(null) }
        val created = repository.startConversation()
        response.complete(config(null))
        refresh.await()
        assertTrue(repository.assistant.value.conversations.any { it.key == created })
    }

    @Test fun creatingBeforePageLoadsPreservesConversationsReturnedByTheManager() = runTest {
        AssistantManagerChoice.pick("work")
        val transport = Transport().apply {
            read = { buildJsonObject {
                put("machine_id", "mac")
                put("main", "")
                put("conversations", buildJsonArray { add(session("work", "existing")) })
            } }
        }
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        val created = repository.startConversation()
        assertEquals(listOf(created.sessionId, "existing"), repository.assistant.value.conversations.map { it.sessionId })
    }

    @Test fun changingDefaultClearsOldMainBeforeNewResponseAndRestoresByConcreteOwner() = runTest {
        val transport = Transport()
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        runCurrent()
        assertEquals("assistant", repository.assistant.value.managerId)
        val workResponse = CompletableDeferred<JsonObject>()
        transport.read = { manager -> if (manager == "work") workResponse.await() else config(manager) }
        AssistantManagerChoice.choose("hub", null, "work")
        runCurrent()
        assertEquals("work", repository.assistant.value.managerId)
        assertNull(repository.assistant.value.mainSession)
        workResponse.complete(config("work"))
        runCurrent()
        assertEquals("work-main", repository.assistant.value.mainSession?.sessionId)
        assertEquals("work", transport.readOwners.last())
        transport.read = { throw IOException("offline") }
        AssistantManagerChoice.choose("hub", "assistant", "work")
        runCurrent()
        assertEquals("assistant-main", repository.assistant.value.mainSession?.sessionId)
        assertEquals("assistant", repository.assistant.value.managerId)
        assertEquals("assistant", repository.assistant.value.manager)
        AssistantManagerChoice.choose("hub", null, "work")
        runCurrent()
        assertEquals("work-main", repository.assistant.value.mainSession?.sessionId)
    }

    @Test fun creationCapturedBeforeDefaultChangesNeverUsesTheNewDefault() = runTest {
        val configGate = CompletableDeferred<JsonObject>()
        val transport = Transport().apply { read = { configGate.await() } }
        val repository = RealAssistantRepository(Connection(transport), Sessions(), backgroundScope)
        val creation = async { repository.startConversation() }
        runCurrent()
        AssistantManagerChoice.choose("hub", null, "work")
        runCurrent()
        configGate.complete(config(null, ""))
        creation.await()
        assertEquals(listOf("assistant"), transport.createOwners)
        assertEquals("work", repository.assistant.value.managerId)
        assertFalse(repository.assistant.value.conversations.any { it.sessionId == "null-created" })
    }

    private class Connection(transport: InboxAssistantTransport) : InboxAssistantConnection {
        override val current = MutableStateFlow<InboxAssistantTransport?>(transport)
    }
    private class Sessions : SessionsRepository {
        override val machines = MutableStateFlow<List<Machine>>(emptyList())
        override val sessions = MutableStateFlow<List<Session>>(emptyList())
        override suspend fun refresh() = Unit
    }
    private class Transport : InboxAssistantTransport {
        override val scope = "scope"
        var read: suspend (String?) -> JsonObject = { config(it, recentTaskChats = recentTaskChats) }
        var recentTaskChats: Boolean? = null
        val creates = mutableListOf<String?>()
        val readOwners = mutableListOf<String?>()
        val createOwners = mutableListOf<String?>()
        override suspend fun assistantOf(manager: String?): JsonObject {
            readOwners += manager
            return read(manager?.takeUnless { it == "assistant" })
        }
        override suspend fun assistant() = read(null)
        override suspend fun createConversationOf(manager: String?, main: Boolean, launcher: String?): JsonObject {
            createOwners += manager
            val normalized = manager?.takeUnless { it == "assistant" }
            creates += normalized
            return buildJsonObject { put("session", session(normalized, "${normalized}-created")) }
        }
        override suspend fun createConversation(main: Boolean): JsonObject = error("use captured manager")
        override suspend fun pins() = buildJsonObject { put("sessions", buildJsonArray { }) }
        override suspend fun inbox(): JsonObject = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean): JsonObject = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String): JsonObject = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?): JsonObject = error("unused")
        override suspend fun mutes(): JsonObject = error("unused")
        override suspend fun media(ref: String): ByteArray = error("unused")
    }
    companion object {
        private fun header(id: String) = AssistantManagerChoice.itemHeader(listOf("assistant", "conversations", id, "messages"), "hub")
        private fun config(manager: String?, main: String = "${manager ?: "assistant"}-main", recentTaskChats: Boolean? = null) = buildJsonObject {
            put("machine_id", "mac")
            put("main", main)
            recentTaskChats?.let { put("recent_task_chats", it) }
            put("conversations", buildJsonArray { if (main.isNotEmpty()) add(session(manager, main)) })
        }
        private fun session(manager: String?, id: String) = buildJsonObject {
            put("machine_id", "mac")
            put("session_id", id)
            put("assistant", true)
            manager?.let { put("manager", it) }
        }
    }
}
