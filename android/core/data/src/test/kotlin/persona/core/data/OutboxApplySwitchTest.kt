package persona.core.data

import androidx.room.Room
import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageRequest
import persona.core.contracts.api.MessageTransport
import persona.core.contracts.api.MessageTransportResult

/**
 * The user's message that carries out a launcher switch waiting on its session (#5055, `apply_switch`): kept with the
 * queued message across a restart, and only on that message.
 */
@OptIn(ExperimentalCoroutinesApi::class)
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class OutboxApplySwitchTest {
    private val scope = HubScope("hub", "switch-test")
    private val session = SessionKey("machine", "session")
    private lateinit var database: PersistentDatabase

    @Before fun open() {
        database = Room.inMemoryDatabaseBuilder(RuntimeEnvironment.getApplication(), PersistentDatabase::class.java).allowMainThreadQueries().build()
    }
    @After fun close() = database.close()

    private class Hub(online: Boolean) : MessageTransport {
        override val readyToSend = MutableStateFlow(online)
        override val supportsClientOps = false
        val requests = mutableListOf<MessageRequest>()
        override suspend fun send(request: MessageRequest): MessageTransportResult {
            requests += request
            return MessageTransportResult.Accepted
        }
    }

    @Test fun theDatabaseKeepsWhichMessageCarriesTheSwitch() = runTest {
        val sender = OutboxMessageSender(scope, RoomOutboxStore(database, scope), Hub(online = false), backgroundScope)
        sender.sendApplyingSwitch(session, "go on", "switching")
        sender.send(session, "and this", "plain")
        // queued while offline: what is read back after a restart says which one carries the switch
        val kept = RoomOutboxStore(database, scope).entries.first { it.size == 2 }.associate { it.clientMessageId to it.applySwitch }
        assertEquals(mapOf("switching" to true, "plain" to false), kept)
        sender.close()
    }

    @Test fun theDatabaseKeepsUnknownProvenanceAndRejectsAChangedDuplicate() = runTest {
        val store = RoomOutboxStore(database, scope)
        val entry = OutboxEntry("shared", session, "shared text", createdAt = 1, via = "future-source")
        store.enqueue(entry)
        assertEquals("future-source", RoomOutboxStore(database, scope).entries.first().single().via)
        val changed = runCatching { store.enqueue(entry.copy(via = "other-source")) }.exceptionOrNull()
        assertTrue(changed is IllegalArgumentException)
        assertTrue(changed?.message?.contains("Operation id reused with different parameters") == true)

    }

    @Test fun onlyThatMessageGoesWithApplySwitch() = runTest {
        val hub = Hub(online = false)
        val sender = OutboxMessageSender(scope, MemoryOutboxStore(), hub, backgroundScope)
        sender.sendApplyingSwitch(session, "go on", "switching")
        runCurrent()
        hub.readyToSend.value = true
        runCurrent()
        sender.send(session, "and this", "plain")
        runCurrent()
        assertEquals(listOf("switching" to true, "plain" to false), hub.requests.map { it.clientMessageId to it.applySwitch })
        assertTrue(hub.requests.first().applySwitch)
        assertFalse(hub.requests.last().applySwitch)
        sender.close()
    }
}
