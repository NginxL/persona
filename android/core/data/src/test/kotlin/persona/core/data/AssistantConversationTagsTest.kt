package persona.core.data

import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.ModelPick
import persona.core.contracts.Session
import persona.core.contracts.api.InboxAssistantConnection
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.SessionsRepository

@OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
class AssistantConversationTagsTest {
    private class Transport : InboxAssistantTransport {
        override val scope = "scope"
        override suspend fun assistant() = buildJsonObject {
            put("machine_id", "mac")
            put("main", "main")
            put("conversations", buildJsonArray {
                add(JsonCodec.parseToJsonElement(
                    """{"machine_id":"mac","session_id":"c1","assistant":true,"tags":["before"]}"""))
            })
        }
        override suspend fun createConversation(main: Boolean) = error("unused")
        override suspend fun createConversationOf(manager: String?, main: Boolean, pick: ModelPick) = error("unused")
        override suspend fun pins() = buildJsonObject { put("sessions", buildJsonArray { }) }
        override suspend fun inbox() = error("unused")
        override suspend fun setDone(itemId: String, done: Boolean) = error("unused")
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = error("unused")
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = error("unused")
        override suspend fun mutes() = error("unused")
        override suspend fun media(ref: String) = error("unused")
    }

    private class Sessions : SessionsRepository {
        override val machines: StateFlow<List<Machine>> = MutableStateFlow(emptyList())
        override val sessions = MutableStateFlow<List<Session>>(emptyList())
        override suspend fun refresh() {}
    }

    @Test fun anotherClientsReplacementAndClearArriveOnTheExistingSessionsPush() = runTest {
        val sessions = Sessions()
        val connection = object : InboxAssistantConnection {
            override val current = MutableStateFlow<InboxAssistantTransport?>(Transport())
        }
        val repository = RealAssistantRepository(connection, sessions, backgroundScope)
        runCurrent()
        repository.refresh()
        runCurrent()
        fun conversation() = repository.assistant.value.conversations.single { it.sessionId == "c1" }
        assertEquals(listOf("before"), conversation().tags)

        sessions.sessions.value = listOf(JsonCodec.decodeFromString(
            """{"machine_id":"mac","session_id":"c1","assistant":true,"waiting":false,"tags":["after","trip"]}"""))
        runCurrent()
        assertEquals(listOf("after", "trip"), conversation().tags)

        // Empty tag sets are omitted by the hub's JSON, so their pushed row carries no tags field.
        sessions.sessions.value = listOf(JsonCodec.decodeFromString(
            """{"machine_id":"mac","session_id":"c1","assistant":true,"waiting":false}"""))
        runCurrent()
        assertNull(conversation().tags)
    }
}
