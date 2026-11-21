package persona.feature.agents

import androidx.datastore.core.DataStore
import androidx.datastore.preferences.core.Preferences
import androidx.datastore.preferences.core.emptyPreferences
import androidx.lifecycle.viewModelScope
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.StandardTestDispatcher
import kotlinx.coroutines.test.TestScope
import kotlinx.coroutines.test.UnconfinedTestDispatcher
import kotlinx.coroutines.test.resetMain
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.test.setMain
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Before
import org.junit.Test
import persona.core.contracts.api.HubConnection
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubPreferences
import persona.core.contracts.api.HubPreferencesSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.ResolveOutcome
import persona.core.contracts.api.SessionsRepository
import persona.core.domain.answer.AnswerAction

/** Full-card answers preserve hand-off and exact owner-reply transport semantics in the Agents editor. */
@OptIn(ExperimentalCoroutinesApi::class)
class AgentsSecretHandoffAnswerTest {
    private lateinit var model: AgentsViewModel
    private val request = JsonCodec.decodeFromString<Interaction>(
        """{"id":"ix_secret","machine_id":"m","session_id":"s","kind":"permission","tool_name":"secret_handoff",
            "always_label":"this launcher on this computer","tool_input":{"variables":[{"name":"TOKEN"}]}}""",
    )
    private val preferences = AgentsPreferences(object : DataStore<Preferences> {
        override val data = MutableStateFlow<Preferences>(emptyPreferences())
        override suspend fun updateData(transform: suspend (Preferences) -> Preferences): Preferences =
            transform(data.value).also { data.value = it }
    }, object : HubPreferences {
        override val preferences = MutableStateFlow(HubPreferencesSnapshot())
        override suspend fun refresh() = Unit
        override suspend fun togglePin(path: String, space: Boolean) = Unit
        override suspend fun toggleMute(path: String, space: Boolean) = Unit
        override suspend fun markSeen(session: SessionKey, version: String) = Unit
    })

    @Before fun setMain() { Dispatchers.setMain(StandardTestDispatcher()) }
    @After fun tearDown() {
        if (::model.isInitialized) model.viewModelScope.cancel()
        Dispatchers.resetMain()
    }

    private fun TestScope.start(center: InteractionCenter) {
        model = AgentsViewModel(object : SessionsRepository {
            override val sessions = MutableStateFlow(listOf(Session("m", "s", state = "needs_input")))
            override val machines = MutableStateFlow(listOf(Machine("m", online = true)))
            override suspend fun refresh() = Unit
        }, center, object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.Online)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }, object : MessageSender {
            override val outbox = MutableStateFlow<List<OutboxMessage>>(emptyList())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }, preferences)
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { model.page.collect {} }
        runCurrent()
    }

    @Test fun ownerReplyUsesTheExactReplyEnvelopeInsteadOfOrdinaryResolution() = runTest {
        val ownerRequest = JsonCodec.decodeFromString<Interaction>(
            """{"id":"ix_owner","machine_id":"m","session_id":"s","kind":"permission","owner_proposal":true,
                "approval_batch_id":"batch","owner_approval_phrases":["yes"]}""",
        )
        val replies = mutableListOf<Pair<Interaction, String>>()
        val center = object : InteractionCenter {
            override val pending = MutableStateFlow(listOf(ownerRequest))
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = error("Exact replies do not use resolve")
            override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome =
                error("Exact replies do not use ordinary resolution")
            override suspend fun replyOwnerApproval(interaction: Interaction, phrase: String) {
                replies += interaction to phrase
            }
        }
        start(center)
        model.answer(ownerRequest, AnswerAction.OwnerReply)
        runCurrent()
        assertEquals(listOf(ownerRequest to "yes"), replies)
        assertEquals(RequestDelivery.Sent, model.delivery.value[ownerRequest.id])
    }

    @Test fun allowIsMarkedAndASecondTapWaitsForItsOutcome() = runTest {
        val started = CompletableDeferred<Unit>()
        val release = CompletableDeferred<Unit>()
        val calls = mutableListOf<Triple<String, Resolution, Boolean>>()
        val center = object : InteractionCenter {
            override val pending = MutableStateFlow(listOf(request))
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = error("A marked hand-off uses resolveOutcome")
            override suspend fun resolveOutcome(id: String, resolution: Resolution, handoffCard: Boolean): ResolveOutcome {
                calls += Triple(id, resolution, handoffCard)
                started.complete(Unit)
                release.await()
                return ResolveOutcome()
            }
        }
        start(center)
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        started.await()
        model.answer(request, AnswerAction.Allow)
        runCurrent()
        assertEquals(listOf(Triple(request.id, Resolution(allow = true), true)), calls)
        assertEquals(RequestDelivery.Sending, model.delivery.value[request.id])
        release.complete(Unit)
        runCurrent()
        assertEquals(RequestDelivery.Sent, model.delivery.value[request.id])
        assertEquals(1, calls.size)
    }
}
