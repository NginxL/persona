package persona.feature.inbox

import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.LarkMention
import persona.core.inbox.*

/** #576 G122: the iOS LarkMentions contract for Android's group and topic reply boxes. */
class LarkMentionsTest {
    private val tom = LarkMention("u-tom", "Tom")
    private val tommy = LarkMention("u-tommy", "Tommy")
    private val zhou = LarkMention("u-zhou", "Zhou")

    @Test fun onlyGroupAndTopicComposersEnableTheMemberPicker() {
        assertEquals("feed-group", larkMentionFeed("feed-group", LarkConversation(isGroup = true)))
        assertEquals("feed-topic", larkMentionFeed("feed-topic", LarkConversation(isTopic = true)))
        assertNull(larkMentionFeed("feed-direct", LarkConversation(isGroup = false)))
        assertNull("unknown chat metadata must not turn a direct chat into a group", larkMentionFeed("feed-unknown", LarkConversation()))
    }

    @Test fun typingAtSuppliesTheTypedQueryAndOrdinaryTextOrAnEmailDoesNot() {
        assertEquals("", larkMentionQuery("@"))
        assertEquals("小", larkMentionQuery("Saturday @小"))
        assertEquals("To", larkMentionQuery("@Zhou together @To"))
        assertEquals("To", larkMentionQuery("Saturday,@To"))
        assertNull(larkMentionQuery("Saturday"))
        assertNull(larkMentionQuery("mail me@example"))
        assertNull(larkMentionQuery("@Tom "))
        assertNull(larkMentionQuery("@Tom\n"))
        assertNull(larkMentionQuery("@" + "x".repeat(21)))
    }

    @Test fun pickingAPersonOrEveryoneWritesTheDisplayNameAndKeepsItsWireIdentity() {
        assertEquals("Please @Tom ", larkInsertMention("Please @To", tom))
        val everyone = LarkMention("all", "Everyone")
        assertEquals("@Everyone ", larkInsertMention("@", everyone))
        val wire = JsonCodec.encodeToJsonElement(LarkMention.serializer(), everyone).jsonObject
        assertEquals("all", wire["user_id"]!!.jsonPrimitive.content)
        assertEquals("Everyone", wire["name"]!!.jsonPrimitive.content)
        val personWire = JsonCodec.encodeToJsonElement(LarkMention.serializer(), tom).jsonObject
        assertEquals("u-tom", personWire["user_id"]!!.jsonPrimitive.content)
        assertEquals("Tom", personWire["name"]!!.jsonPrimitive.content)
    }

    @Test fun pruningDropsDeletedNamesKeepsSurvivorsAndDoesNotConfusePrefixNames() {
        assertEquals(listOf(tom), larkMentions("@Tom Saturday", picked = listOf(zhou, tom)))
        assertEquals(emptyList<LarkMention>(), larkMentions("@To Saturday", picked = listOf(tom)))
        assertEquals(listOf(tommy, zhou), larkMentions("@Tommy @Zhou Saturday", picked = listOf(tom, tommy, zhou)))
        assertEquals(listOf(zhou), larkMentions("@Zhou Saturday", picked = listOf(zhou, tom)))
        assertEquals(emptyList<LarkMention>(), larkMentions("Saturday", picked = listOf(zhou, tom)))
    }
}
