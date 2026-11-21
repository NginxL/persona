package persona.core.contracts

import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantConfigUpdate
import persona.core.contracts.api.ConversationTagFilters

class AssistantSetupTest {
    @Test fun recentTaskChatsIsOptionalAndWritesOnlyItself() {
        assertNull(JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject { }).recentTaskChats)
        val read = JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject { put("recent_task_chats", true) })
        assertEquals(true, read.recentTaskChats)
        val written = JsonCodec.encodeToJsonElement(AssistantConfigUpdate(recentTaskChats = true)).jsonObject
        assertEquals(setOf("recent_task_chats"), written.keys)
        assertEquals(true, written.getValue("recent_task_chats").jsonPrimitive.boolean)
    }

    @Test fun conversationOrderDistinguishesUnchangedResetAndSavedIds() {
        assertNull(JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject { }).conversationOrder)
        val read = JsonCodec.decodeFromJsonElement<AssistantConfig>(
            JsonCodec.parseToJsonElement("""{"conversation_order":["b","unknown","a"]}"""),
        )
        assertEquals(listOf("b", "unknown", "a"), read.conversationOrder)
        assertTrue("null is omitted and leaves the setting unchanged",
            "conversation_order" !in JsonCodec.encodeToJsonElement(AssistantConfigUpdate()).jsonObject)
        val reset = JsonCodec.encodeToJsonElement(AssistantConfigUpdate(conversationOrder = emptyList())).jsonObject
        assertEquals(setOf("conversation_order"), reset.keys)
        assertEquals(emptyList<Nothing>(), reset.getValue("conversation_order").jsonArray)
        val written = JsonCodec.encodeToJsonElement(
            AssistantConfigUpdate(conversationOrder = listOf("b", "unknown", "a")),
        ).jsonObject.getValue("conversation_order").jsonArray
        assertEquals(listOf("b", "unknown", "a"), written.map { it.jsonPrimitive.content })
    }

    @Test fun conversationTagFiltersDistinguishAbsentNullEmptyAndOrderedArrays() {
        val absent = JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject { }).conversationTagFilters
        assertFalse(absent.supported)
        assertNull(absent.entries)

        val automatic = JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject {
            put("conversation_tag_filters", JsonNull)
        }).conversationTagFilters
        assertTrue(automatic.supported)
        assertNull(automatic.entries)

        val empty = JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject {
            put("conversation_tag_filters", buildJsonArray { })
        }).conversationTagFilters
        assertEquals(ConversationTagFilters(emptyList()), empty)
        assertTrue(empty.customized)

        val ordered = JsonCodec.decodeFromJsonElement<AssistantConfig>(buildJsonObject {
            put("conversation_tag_filters", buildJsonArray {
                add(kotlinx.serialization.json.JsonPrimitive("second"))
                add(kotlinx.serialization.json.JsonPrimitive("first"))
            })
        }).conversationTagFilters
        assertEquals(listOf("second", "first"), ordered.entries)
    }

    @Test fun conversationTagFilterUpdatesWriteExactNullEmptyAndOrderedBodies() {
        assertFalse(JsonCodec.encodeToJsonElement(AssistantConfigUpdate()).jsonObject
            .containsKey("conversation_tag_filters"))
        assertEquals(JsonNull, JsonCodec.encodeToJsonElement(AssistantConfigUpdate(
            conversationTagFilters = ConversationTagFilters(null),
        )).jsonObject.getValue("conversation_tag_filters"))
        assertEquals("""{"conversation_tag_filters":[]}""", JsonCodec.encodeToString(
            AssistantConfigUpdate(conversationTagFilters = ConversationTagFilters(emptyList())),
        ))
        assertEquals("""{"conversation_tag_filters":["second","first"]}""", JsonCodec.encodeToString(
            AssistantConfigUpdate(conversationTagFilters = ConversationTagFilters(listOf("second", "first"))),
        ))
    }
}
