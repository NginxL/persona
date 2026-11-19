package persona.feature.agents

import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.Job
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.setMain
import kotlinx.coroutines.withTimeout
import kotlinx.coroutines.withTimeoutOrNull
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import persona.core.contracts.Interaction
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.WorkState
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.HubPreferencesSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentSession

/**
 * Seeing a Just finished agent (#9786): Mark all as read and opening its chat take it out at once, as the seen this phone
 * has not had recorded yet; while its chat is open the row the shell held keeps Just finished's place, drawn as seen.
 */
@OptIn(ExperimentalCoroutinesApi::class)
class AgentsJustFinishedTest {
    private val dispatcher: CoroutineDispatcher = UnconfinedTestDispatcher()
    private var model: AgentsViewModel? = null
    private val done = WorkState("done", line = "Merged", justFinished = true)
    private val sessions = MutableStateFlow(listOf(
        session("finished", "idle").copy(work = done), session("working", "working"), session("seen", "idle"),
    ))
    private val hub = MutableStateFlow(HubPreferencesSnapshot())
    private val visible = MutableStateFlow<SessionKey?>(null)
    private val held = MutableStateFlow<SessionKey?>(null)
    private val finished = MutableStateFlow<SessionKey?>(null)

    @Before fun setMain() { Dispatchers.setMain(dispatcher) }
    @After fun tearDown() {
        model?.let { current -> runBlocking {
            withTimeout(10_000) { current.viewModelScope.coroutineContext[Job]?.cancelAndJoin() }
        } }
        Dispatchers.resetMain()
    }

    @Test fun openingItHoldsItsPlaceDrawnSeenUntilTheChatIsLeft() = runBlocking {
        model = model()
        awaitSection("state/just_finished", "machine/finished")
        assertTrue(row("machine/finished").showsJustFinished)

        // The shell captures it before the chat's seen.
        finished.value = SessionKey("machine", "finished")
        visible.value = SessionKey("machine", "finished")
        awaitSection("state/just_finished", "machine/finished")
        // The chat marks it seen here: seen at once, in the same place.
        preferences.markSeen(listOf(sessions.value.first()))
        withTimeout(10_000) { model!!.page.first { page -> page.list.groups.flatMap { it.rows }.any { it.id == "machine/finished" && !it.showsJustFinished } } }
        awaitSection("state/just_finished", "machine/finished")
        // The hub records it and lists it without the flag: still held there.
        sessions.value = sessions.value.map { if (it.sessionId == "finished") it.copy(work = done.copy(justFinished = false)) else it }
        awaitSection("state/just_finished", "machine/finished")
        assertFalse(model!!.page.value.spaces.single { space -> space.members.any { it.id == "machine/finished" } }.quiet(model!!.page.value.preferences.mutes))

        // Left: with the seen ones.
        finished.value = null
        visible.value = null
        awaitIdle("machine/finished", "machine/seen")
    }

    @Test fun markAllAsReadTakesItOutAtOnce() = runBlocking {
        model = model()
        awaitSection("state/just_finished", "machine/finished")
        val unseen = model!!.page.value.spaces.flatMap { it.members }.filter { it.unseen }
        assertEquals(listOf("machine/finished"), unseen.map { it.id })
        model!!.markRead(unseen)
        // Before the hub answers: the hub's list still has the flag.
        awaitIdle("machine/finished", "machine/seen")
        assertTrue(sessions.value.first().work!!.justFinished)
    }

    @Test fun aSeenChatHoldsNothingAndLeavesJustFinishedAsItIs() = runBlocking {
        model = model()
        visible.value = SessionKey("machine", "seen")
        awaitSection("state/just_finished", "machine/finished")
        awaitIdle("machine/seen", finishedLeft = false)
    }

    private val preferences = AgentsPreferences(object : androidx.datastore.core.DataStore<androidx.datastore.preferences.core.Preferences> {
        override val data = MutableStateFlow(androidx.datastore.preferences.core.emptyPreferences())
        override suspend fun updateData(transform: suspend (androidx.datastore.preferences.core.Preferences) -> androidx.datastore.preferences.core.Preferences) =
            transform(data.value).also { data.value = it }
    }, object : persona.core.contracts.api.HubPreferences {
        override val preferences = hub
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        // As HubPreferencesRepository: this phone's seen shows before the hub records it.
        override suspend fun markSeen(session: SessionKey, version: String) { hub.value = hub.value.copy(seen = hub.value.seen + (session.path to version)) }
    }).also { runBlocking {
        it.grouping(AgentGrouping.State)
        it.range("", AgentRange.All)
    } }

    private fun session(id: String, state: String) = Session("machine", id, title = "Example $id", state = state,
        cwd = "/home/user/project", spaceId = "$id-space", spaceName = "Project $id", updatedAt = "2026-01-01T00:00:00Z")

    private fun model(): AgentsViewModel = AgentsViewModel(object : SessionsRepository {
        override val sessions = this@AgentsJustFinishedTest.sessions
        override val machines = MutableStateFlow(listOf(Machine("machine", "Example computer", online = true)))
        override suspend fun refresh() = Unit
    }, object : InteractionCenter {
        override val pending = MutableStateFlow<List<Interaction>>(emptyList())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }, object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }, object : MessageSender {
        override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }, preferences, visibleChat = visible, heldChat = held, finishedChat = finished).also { model = it }

    private fun row(id: String): AgentSession = model!!.page.value.list.groups.flatMap { it.rows }.single { it.id == id }

    private suspend fun awaitSection(section: String, vararg ids: String) {
        val expected = ids.toList()
        val matched = withTimeoutOrNull(10_000) {
            model!!.page.first { page -> page.list.groups.singleOrNull { it.id == section }?.visibleRows?.map { it.id } == expected }
        }
        assertTrue("$section expected $expected; actual " + describe(), matched != null)
    }

    private suspend fun awaitIdle(vararg ids: String, finishedLeft: Boolean = true) {
        val matched = withTimeoutOrNull(10_000) {
            model!!.page.first { page ->
                val group = page.list.groups.singleOrNull { it.id == "state/idle" }
                group?.visibleRows?.isEmpty() == true && group.rows.map { it.id } == ids.toList() &&
                    (!finishedLeft || page.list.groups.none { it.id == "state/just_finished" })
            }
        }
        assertTrue("idle expected ${ids.toList()}; actual " + describe(), matched != null)
    }

    private fun describe() = model!!.page.value.list.groups.map {
        "${it.id}: rows=${it.rows.map(AgentSession::id)}, visible=${it.visibleRows.map(AgentSession::id)}"
    }
}
