package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import androidx.datastore.preferences.core.PreferenceDataStoreFactory
import androidx.datastore.preferences.core.edit
import androidx.datastore.preferences.core.stringSetPreferencesKey
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import persona.core.hub.HeldAgentRetryReceipts
import persona.core.hub.HttpHeldAgentActions
import java.io.File

@OptIn(ExperimentalCoroutinesApi::class)
class CompleteSessionsRepositoryTest {
    @get:Rule val temporary = TemporaryFolder()
    private val rows = (0 until 8000).map { Session("demo", "session/$it") }
    private fun counts() = SessionListCounts(total = 8000)

    @Test fun aRestoredSessionSnapshotCannotRepeatTheAcceptedHeldDecision() = runTest {
        val connection = Connection()
        val snapshots = ListSnapshotStore(temporary.newFolder())
        val managerStatus = "Might not have arrived. Retry checks the chat first."
        fun row(status: String, state: String = "failed") = requireNotNull(ServerEvent.decode(
            """{"type":"session","session":{"machine_id":"demo","session_id":"chat","undelivered":[
                {"id":"kept","text":"Synthetic note","images":1,"at":"2026-06-06T12:00:00Z",
                "computer":"Demo Mac","state":"$state","error":"$status"}]}}""")) as SessionRowEvent
        val original = row(managerStatus).session
        snapshots.save("sessions", listOf(original))
        val keys = stringSetPreferencesKey("held_agent_retry_receipts")
        val file = File(temporary.newFolder(), "held-retries.preferences_pb")
        var posts = 0
        fun open(scope: CoroutineScope, manager: Connection): Pair<HubSessionsRepository, HttpHeldAgentActions> {
            val prefs = PreferenceDataStoreFactory.create(scope = scope) { file }
            val receipts = HeldAgentRetryReceipts(prefs.data.map { it[keys].orEmpty() }) { update ->
                prefs.edit { it[keys] = update(it[keys].orEmpty()) }
            }
            val repository = HubSessionsRepository(Transport(), manager, backgroundScope, snapshots, receipts::reconcile)
            return repository to HttpHeldAgentActions(original.key, MutableStateFlow(setOf(AgentMessageHoldFeature)),
                receipts, scope, { _, _, _ ->
                    posts++
                    Response.Builder().request(Request.Builder().url("https://example.com/").build())
                        .protocol(Protocol.HTTP_1_1).code(200).message("OK").body("{}".toResponseBody()).build()
                }, { id, retry -> repository.heldActionCompleted(original.key, id, retry) })
        }
        val first = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        open(first, Connection()).second.retry(original.undelivered!!.single())
        first.coroutineContext.job.cancelAndJoin()

        val restarted = CoroutineScope(SupervisorJob() + Dispatchers.IO)
        try {
            val (repository, actions) = open(restarted, connection)
            val cached = repository.sessions.first { it.isNotEmpty() }.single().undelivered!!.single()
            actions.retry(cached)
            connection.events.emit(SessionRowEvent(original.copy(waiting = false, undelivered = null)))
            runCurrent()
            assertEquals(listOf(cached), repository.sessions.value.single().undelivered)
            suspend fun apply(status: String, state: String = "failed", machineList: Boolean = false): HeldAgentMessage {
                val event = row(status, state)
                connection.events.emit(if (machineList) SessionsEvent(original.machineId, listOf(event.session)) else event)
                return repository.sessions.first { it.singleOrNull()?.undelivered?.singleOrNull()?.error == status }
                    .single().undelivered!!.single()
            }
            actions.retry(apply(managerStatus))
            assertEquals(1, posts)
            val unconfirmed = apply("Couldn't confirm it arrived. Retry to send it anyway, or delete it.")
            assertTrue(actions.canRetry(unconfirmed).first())
            actions.retry(unconfirmed)
            actions.retry(cached)
            assertEquals(2, posts)
            apply("Will send when Demo Mac is back", "waiting", machineList = true)
            actions.retry(cached) // The screen can still have its old check-only row.
            actions.retry(apply(unconfirmed.error!!, machineList = true))
            assertEquals(3, posts)
            assertFalse(file.readBytes().decodeToString().contains("Synthetic note"))
            assertFalse(file.readBytes().decodeToString().contains(managerStatus))
        } finally {
            restarted.coroutineContext.job.cancelAndJoin()
        }
    }

    @Test fun managerQueueAppearsOnceUpdatesOnAnotherDeviceAndClearsFromAnAuthoritativeRow() = runTest {
        val connection = Connection()
        val repository = HubSessionsRepository(Transport(), connection, backgroundScope)
        val key = SessionKey("demo", "chat")
        repository.adopt(Session(key.machineId, key.sessionId, title = "Chat"))
        val waiting = HeldAgentMessage("held", "Hello", computer = "Laptop", error = "Waiting for Laptop")
        repository.showHeld(key, waiting)
        repository.showHeld(key, waiting)
        assertEquals(listOf(waiting), repository.sessions.value.single().undelivered)

        suspend fun push(raw: String) {
            connection.events.emit(requireNotNull(ServerEvent.decode(raw)))
            runCurrent()
        }
        push("""{"type":"session","session":{"machine_id":"demo","session_id":"chat","undelivered":[{"id":"held","text":"Hello","computer":"Laptop","state":"failed","error":"Check the chat first"}]}}""")
        val failed = repository.sessions.value.single().undelivered!!.single()
        assertEquals("failed", failed.state)
        assertEquals("Check the chat first", failed.error)
        repository.heldActionCompleted(key, failed.id, retry = true)
        assertEquals(failed, repository.sessions.value.single().undelivered!!.single())
        push("""{"type":"session","session":{"machine_id":"other","session_id":"chat"}}""")
        assertEquals("held", repository.sessions.value.single().undelivered!!.single().id)
        push("""{"type":"session","session":{"machine_id":"demo","session_id":"chat"}}""")
        assertTrue(repository.sessions.value.single().undelivered.isNullOrEmpty())
        push("""{"type":"sessions","machine_id":"demo","sessions":[{"session_id":"chat","undelivered":[{"id":"new","computer":"Laptop","state":"failed","delete_only":true}]}]}""")
        assertTrue(repository.sessions.value.single().undelivered!!.single().deleteOnly)
        repository.heldActionCompleted(key, "new", retry = false)
        assertTrue(repository.sessions.value.single().undelivered.isNullOrEmpty())
    }

    @Test fun firstCompleteMembershipRequestKeepsRestoredRowsUntilTheResponse() = runTest {
        val cached = listOf(Session("demo", "cached"))
        val fresh = listOf(Session("demo", "fresh"))
        val store = ListSnapshotStore(temporary.newFolder())
        store.save("sessions", cached)
        val requested = CompletableDeferred<Unit>()
        val response = CompletableDeferred<List<Session>>()
        val transport = Transport().apply {
            legacyFetch = {
                requested.complete(Unit)
                response.await()
            }
        }
        val connection = Connection()
        val repository = HubSessionsRepository(transport, connection, backgroundScope, store)
        repository.sessions.first { it == cached }

        val refresh = backgroundScope.launch { repository.setListFilter(SessionListFilter(requiresCompleteMembership = true)) }
        requested.await()
        assertEquals(cached, repository.sessions.value)
        assertTrue(repository.listStatus.value.loading)
        assertFalse(repository.listStatus.value.loaded)

        connection.resumed = true
        connection.state.value = ConnectionState.Online
        runCurrent()
        assertTrue(repository.verifiedSessions().isEmpty())

        response.complete(fresh)
        refresh.join()
        assertEquals(fresh, repository.sessions.value)
        assertTrue(repository.listStatus.value.loaded)
        assertEquals(mapOf("demo" to fresh), repository.verifiedSessions())
    }

    @Test fun completeMembershipRetainsDeepRowsRefreshesNoticesAndCanReturnToPaging() = runTest {
        val connection = Connection()
        val transport = Transport().apply { fetch = { _, _, _ -> SessionListPage(rows.take(50), total = 8000) } }
        val repository = HubSessionsRepository(transport, connection, backgroundScope)
        repository.refresh()
        assertTrue(repository.listStatus.value.paged)
        repository.setListFilter(SessionListFilter(requiresCompleteMembership = true))
        assertFalse(repository.listStatus.value.paged)
        assertEquals(rows, repository.sessions.value)
        assertEquals(8000, repository.listStatus.value.total)
        val requests = transport.requests.size
        transport.legacyFetch = { rows.dropLast(1) }
        connection.events.emit(SessionsListEvent("archive-revision"))
        advanceTimeBy(100); runCurrent()
        assertEquals(rows.dropLast(1), repository.sessions.value)
        assertEquals(requests, transport.requests.size)
        transport.legacyFetch = { rows }
        connection.events.emit(SessionsListEvent("restore-revision"))
        advanceTimeBy(100); runCurrent()
        assertEquals(rows, repository.sessions.value)
        repository.setListFilter(SessionListFilter())
        assertTrue(repository.listStatus.value.paged)
        assertEquals(50, repository.sessions.value.size)
    }

    @Test fun failedCompleteReadKeepsPreviousRecordsAndPropagatesFailure() = runTest {
        val transport = Transport().apply { fetch = { _, _, _ -> SessionListPage(rows.take(50), total = 8000) } }
        val connection = Connection()
        val repository = HubSessionsRepository(transport, connection, backgroundScope)
        repository.setListFilter(SessionListFilter(requiresCompleteMembership = true))
        assertEquals(rows, repository.sessions.value)
        transport.legacyFetch = { throw java.io.IOException("Read unavailable") }
        assertThrows(java.io.IOException::class.java) { runBlocking { repository.refresh() } }
        assertEquals(rows, repository.sessions.value)
        assertFalse(repository.listStatus.value.paged)
        assertFalse(repository.listStatus.value.loading)
        assertEquals(0, transport.requests.size)
    }

    @Test fun revisionPushDuringCompleteRefreshKeepsMembershipAndNewRevision() = runTest {
        val connection = Connection()
        val requested = CompletableDeferred<Unit>()
        val response = CompletableDeferred<List<Session>>()
        val transport = Transport().apply { legacyFetch = { requested.complete(Unit); response.await() } }
        val repository = HubSessionsRepository(transport, connection, backgroundScope)
        val refresh = backgroundScope.launch { repository.setListFilter(SessionListFilter(requiresCompleteMembership = true)) }
        requested.await()
        connection.events.emit(RevsEvent("demo", mapOf("session/0" to 9L)))
        runCurrent()
        response.complete(rows)
        refresh.join()
        assertEquals(rows.map { it.key }, repository.sessions.value.map { it.key })
        assertEquals(9L, repository.sessions.value.first().rev)
    }

    @Test fun firstPaintSnapshotRetainsFoldedSideParentsWithinWindowLimit() {
        val side = Session("demo", "side-top", foldedUnder = "deep-parent", foldedAs = FoldedAs.SIDE, status = "working")
        val fillers = (0 until 60).map { Session("demo", "filler-$it", status = "idle") }
        val deepParent = Session("demo", "deep-parent", status = "idle")
        val saved = firstPaintSnapshot(listOf(side) + fillers + deepParent, limit = 50)
        assertEquals(50, saved.size)
        assertTrue(saved.any { it.sessionId == "side-top" })
        assertTrue(saved.any { it.sessionId == "deep-parent" })
        // Folded on cold first paint, side-top folds into deep-parent instead of flashing as Row #1
        val folded = persona.core.domain.sort.foldedSessions(saved, hierarchy = true)
        assertTrue(folded.none { it.sessionId == "side-top" })
        assertTrue(folded.any { it.sessionId == "deep-parent" })
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override var resumed = false
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private inner class Transport : ChatTransport {
        var fetch: suspend (SessionListFilter, String?, String?) -> SessionListPage = { _, _, _ -> error("Unexpected page") }
        val requests = mutableListOf<Triple<SessionListFilter, String?, String?>>()
        val countFilters = mutableListOf<SessionListFilter>()
        var legacyCalls = 0
        var legacyFetch: (suspend () -> List<Session>)? = null
        override suspend fun sessionPage(filter: SessionListFilter, cursor: String?, since: String?): SessionListPage {
            requests += Triple(filter, cursor, since)
            return fetch(filter, cursor, since)
        }
        override suspend fun sessionCounts(filter: SessionListFilter): SessionListCounts { countFilters += filter; return counts() }
        override suspend fun machines() = listOf(Machine("demo", online = true))
        override suspend fun sessions(): List<Session> { legacyCalls++; return legacyFetch?.invoke() ?: rows }
        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("Unexpected items")
        override suspend fun interactions() = emptyList<Interaction>()
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }
}
