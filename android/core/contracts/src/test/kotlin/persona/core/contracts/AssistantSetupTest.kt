package persona.core.contracts

import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantConfigUpdate

class AssistantSetupTest {
    @Test fun recentTaskChatsIsOptionalAndWritesOnlyItself() {
        assertNull(JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject { }).recentTaskChats)
        val read = JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject { put("recent_task_chats", true) })
        assertEquals(true, read.recentTaskChats)
        val written = JsonCodec.encodeToJsonElement(AssistantConfigUpdate(recentTaskChats = true)).jsonObject
        assertEquals(setOf("recent_task_chats"), written.keys)
        assertEquals(true, written.getValue("recent_task_chats").jsonPrimitive.boolean)
    }
}
