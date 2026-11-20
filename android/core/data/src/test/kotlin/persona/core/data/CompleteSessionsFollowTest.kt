package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import org.junit.Assert.*
import org.junit.Test

/**
 * The complete Agents view on a `sessions_list` hub: one baseline of complete pages, then `since` reads on hints. Pins
 * how many complete reads a minute of hints costs (it was one complete list and the machines per hint).
 */
@OptIn(ExperimentalCoroutinesApi::class)
class CompleteSessionsFollowTest {
    private val complete = SessionListFilter(requiresCompleteMembership = true)

    @Test fun onlyChangedHeldDecisionsInManagerDeltasReachTheReceiptBeforeTheRowIsPublished() = runTest {
        val hub = JournalHub(0)
        val failed = HeldAgentMessage("kept", state = "failed", error = "Not sent", computer = "Demo Mac")
        val session = Session("demo", "chat", undelivered = listOf(failed))
        hub.add(session)
        val connection = Connection()
        val transitions = mutableListOf<Pair<String, String>>()
        lateinit var repository: HubSessionsRepository
        repository = HubSessionsRepository(hub, connection, backgroundScope, reconcileHeldRetries = { previous, next ->
            assertEquals(previous, repository.sessions.value.single())
            transitions += previous.undelivered!!.single().state to next.undelivered!!.single().state
        })
        connection.events.emit(Hello(features = listOf("sessions_list")))
        repository.setListFilter(complete)
        connection.state.value = ConnectionState.Online
        runCurrent()
        for (row in listOf(failed, failed.copy(state = "waiting", error = "Sending…"), failed)) {
            hub.add(session.copy(undelivered = listOf(row), rev = (hub.deltaReads + 1).toLong()))
            connection.events.emit(SessionsListEvent(hub.revision()))
            advanceTimeBy(250); runCurrent()
        }
        assertEquals(listOf("failed" to "waiting", "waiting" to "failed"), transitions)
    }

    @Test fun aMinuteOfHintsReadsOneBaselineAndThenOnlyChanges() = runTest {
        val hub = JournalHub(1000)
        val connection = Connection()
        val repository = HubSessionsRepository(hub, connection, backgroundScope)
        connection.events.emit(Hello(features = listOf("sessions_list")))
        repository.setListFilter(complete)
        assertEquals(1, hub.baselines)
        assertEquals(5, hub.pages) // 1,000 rows in pages of 200, never the legacy list
        assertEquals(1, hub.machineReads)
        assertEquals(hub.rows.values.toSet(), repository.sessions.value.toSet())
        assertFalse(repository.listStatus.value.paged)
        assertEquals(1000, repository.listStatus.value.total)

        // The connect hint names the revision the baseline already holds.
        connection.state.value = ConnectionState.Online; runCurrent()
        val reconnectReads = hub.deltaReads
        connection.events.emit(SessionsListEvent(hub.revision())); advanceTimeBy(100); runCurrent()
        assertEquals(reconnectReads, hub.deltaReads)
        assertEquals(setOf("demo"), repository.verifiedSessions().keys)

        // A minute of hints, two a second: a row changes before each; every tenth adds a session, every 25th removes one.
        var distinct = 0
        repeat(120) { tick ->
            when {
                tick % 25 == 24 -> hub.remove(SessionKey("demo", "session/$tick"))
                tick % 10 == 9 -> hub.add(Session("demo", "new/$tick", title = "new"))
                else -> hub.touch(SessionKey("demo", "session/${tick % 1000}"))
            }
            distinct++
            connection.events.emit(SessionsListEvent(hub.revision()))
            advanceTimeBy(250); runCurrent()
            // A repeated hint for a revision already held starts no read.
            connection.events.emit(SessionsListEvent(hub.revision()))
            advanceTimeBy(250); runCurrent()
        }
        assertEquals("complete reads during a minute of hints", 1, hub.baselines)
        assertEquals("legacy full lists during a minute of hints", 0, hub.legacyReads)
        assertEquals("machine lists during a minute of hints", 1, hub.machineReads)
        assertEquals("one changes-only read per new revision", reconnectReads + distinct, hub.deltaReads)
        assertEquals(hub.rows.values.toSet(), repository.sessions.value.toSet())
        assertEquals(hub.rows.size, repository.listStatus.value.total)
        assertEquals(hub.rows.size, repository.verifiedSessions().getValue("demo").size)

        // Narrowing the view filters locally: the baseline stays.
        repository.setListFilter(complete.copy(state = "working", query = "build"))
        runCurrent()
        assertEquals(1, hub.baselines)
        assertEquals(hub.rows.size, repository.sessions.value.size)

        // The hub restarted: the checkpoint expired, so exactly one new baseline.
        hub.restart()
        hub.touch(SessionKey("demo", "session/1"))
        connection.events.emit(SessionsListEvent(hub.revision())); advanceTimeBy(100); runCurrent()
        assertEquals(2, hub.baselines)
        assertEquals(hub.rows.values.toSet(), repository.sessions.value.toSet())

        // Pull to refresh takes a new baseline too.
        repository.refresh()
        assertEquals(3, hub.baselines)
        assertEquals(0, hub.legacyReads)
    }

    @Test fun theConnectionsLastHelloDecidesBeforeTheRepositorySeesOne() = runTest {
        val hub = JournalHub(10)
        val connection = Connection().apply { lastHello = Hello(features = listOf("sessions_list")) }
        val repository = HubSessionsRepository(hub, connection, backgroundScope)
        repository.setListFilter(complete)
        assertEquals(1, hub.baselines)
        assertEquals(0, hub.legacyReads)
        assertEquals(10, repository.sessions.value.size)
    }

    @Test fun aHubWithoutListHintsKeepsTheLegacyListAndMovesToFollowingOnHello() = runTest {
        val hub = JournalHub(10)
        val connection = Connection()
        val repository = HubSessionsRepository(hub, connection, backgroundScope)
        connection.events.emit(Hello(features = emptyList()))
        repository.setListFilter(complete)
        assertEquals(1, hub.legacyReads)
        assertEquals(0, hub.baselines)
        // The same hub upgraded and now hints: the next hello moves the view to a baseline and `since` reads.
        connection.events.emit(Hello(features = listOf("sessions_list"))); advanceTimeBy(100); runCurrent()
        assertEquals(1, hub.baselines)
        assertEquals(10, repository.sessions.value.size)
        hub.touch(SessionKey("demo", "session/3"))
        connection.events.emit(SessionsListEvent(hub.revision())); advanceTimeBy(100); runCurrent()
        assertEquals(1, hub.baselines)
        assertEquals(1, hub.legacyReads)
        assertEquals("t1", repository.sessions.value.single { it.sessionId == "session/3" }.title)
    }

    @Test fun supersededFirstReadDoesNotStopApplyingTheFilterToOtherHubs() = runTest {
        val legacyReadStarted = CompletableDeferred<Unit>()
        val firstHub = JournalHub(1) {
            legacyReadStarted.complete(Unit)
            awaitCancellation()
        }
        val firstConnection = Connection()
        val firstRepository = HubSessionsRepository(firstHub, firstConnection, backgroundScope)
        val secondHub = JournalHub(2)
        val hello = Hello(features = listOf("sessions_list"))
        val secondConnection = Connection().apply { lastHello = hello }
        val secondRepository = HubSessionsRepository(secondHub, secondConnection, backgroundScope)
        val applied = async {
            firstRepository.setListFilter(complete)
            secondRepository.setListFilter(complete)
        }

        legacyReadStarted.await()
        firstConnection.events.emit(hello)
        applied.await()
        assertEquals(secondHub.rows.values.toList(), secondRepository.sessions.value)

        advanceTimeBy(100)
        runCurrent()
        assertEquals(1, firstHub.baselines)
        assertEquals(firstHub.rows.values.toList(), firstRepository.sessions.value)
        assertTrue(firstRepository.listStatus.value.loaded)
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override var resumed = false
        override var lastHello: Hello? = null
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    /** A hub's complete collection with a small delta journal (`since`) and read counters. */
    private class JournalHub(count: Int, private val beforeLegacyRead: suspend () -> Unit = {}) : ChatTransport {
        val rows = linkedMapOf<SessionKey, Session>()
        private val changedAt = mutableMapOf<SessionKey, Int>()
        private var epoch = 0
        private var seq = 0
        var baselines = 0; var pages = 0; var deltaReads = 0; var legacyReads = 0; var machineReads = 0

        init { repeat(count) { rows[SessionKey("demo", "session/$it")] = Session("demo", "session/$it", title = "t0") } }

        fun revision() = "e$epoch-$seq"
        fun touch(key: SessionKey) { seq++; rows[key] = rows.getValue(key).copy(title = "t$seq"); changedAt[key] = seq }
        fun add(row: Session) { seq++; rows[row.key] = row; changedAt[row.key] = seq }
        fun remove(key: SessionKey) { seq++; rows.remove(key); changedAt[key] = seq }
        /** A restarted hub keeps no journal: every earlier revision has expired. */
        fun restart() { epoch++; changedAt.clear() }

        override suspend fun sessionPage(filter: SessionListFilter, cursor: String?, since: String?): SessionListPage {
            assertEquals("the complete view asks for the whole collection", SessionListFilter(requiresCompleteMembership = true), filter)
            if (since != null) {
                deltaReads++
                val (sinceEpoch, sinceSeq) = since.removePrefix("e").split("-").map(String::toInt)
                if (sinceEpoch != epoch) throw SessionListExpired()
                val keys = changedAt.filterValues { it > sinceSeq }.keys
                return SessionListPage(
                    sessions = keys.mapNotNull { rows[it] },
                    remove = keys.filter { it !in rows }.map { JsonArray(listOf(JsonPrimitive(it.machineId), JsonPrimitive(it.sessionId))).toString() },
                    revision = revision(), total = rows.size,
                )
            }
            if (cursor == null) baselines++
            pages++
            val all = rows.values.toList()
            val offset = cursor?.toInt() ?: 0
            val end = minOf(offset + 200, all.size)
            return SessionListPage(all.subList(offset, end), hasMore = end < all.size,
                nextCursor = end.takeIf { it < all.size }?.toString(), revision = revision(), total = all.size)
        }
        override suspend fun sessionCounts(filter: SessionListFilter): SessionListCounts = error("Counts are local in the complete view")
        override suspend fun machines(): List<Machine> { machineReads++; return listOf(Machine("demo", online = true)) }
        override suspend fun sessions(): List<Session> {
            legacyReads++
            beforeLegacyRead()
            return rows.values.toList()
        }
        override suspend fun items(session: SessionKey, query: ItemsQuery): ItemsResponse = error("Unexpected items")
        override suspend fun interactions() = emptyList<Interaction>()
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
        override suspend fun image(session: SessionKey, itemId: String) = byteArrayOf()
        override suspend fun fullItem(session: SessionKey, itemId: String) = Item(itemId)
    }
}
