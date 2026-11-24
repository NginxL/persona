package persona.core.data

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.SessionKey

// The health space's chats (GET /api/v1/assistant?space=health, docs/assistant/health.md): each one is the space's and
// on the list's machine, even where the hub leaves them out.
class SpaceConversationsTest {
    private fun json(text: String): JsonObject = Json.parseToJsonElement(text).jsonObject

    @Test fun eachChatIsTheSpacesAndOnTheListsMachine() {
        val chats = spaceConversationsOf(json("""{"machine_id": "mac", "conversations": [
            {"session_id": "a", "assistant": true},
            {"machine_id": "other", "session_id": "b", "space": "health", "updated_at": "2026-11-10T08:00:00Z"}
        ]}"""), "health")
        assertEquals(listOf(SessionKey("mac", "a"), SessionKey("other", "b")), chats.map { it.key })
        assertTrue(chats.all { it.isHealth })
    }

    @Test fun aPushedSessionCarriesItsSpace() {
        val pushed = JsonCodec.decodeFromJsonElement<Session>(json("""{"machine_id": "mac", "session_id": "h", "space": "health"}"""))
        assertEquals("health", pushed.space)
        assertTrue(pushed.isHealth)
    }

    // A domain space's chats (#13500): the space's, never health chats, and kept off the assistant's own list.
    @Test fun aDomainSpacesChatsAreThatSpacesNotHealths() {
        val chats = spaceConversationsOf(json("""{"machine_id": "mac", "conversations": [{"session_id": "f", "assistant": true}]}"""), "qa-finance")
        assertEquals("qa-finance", chats.single().space)
        assertTrue(chats.single().isSpace)
        assertFalse(chats.single().isHealth)
        val pushed = JsonCodec.decodeFromJsonElement<Session>(json("""{"machine_id": "mac", "session_id": "p", "space": "qa-plain"}"""))
        assertTrue(pushed.isSpace)
        assertFalse(JsonCodec.decodeFromJsonElement<Session>(json("""{"machine_id": "mac", "session_id": "m"}""")).isSpace)
    }
}
