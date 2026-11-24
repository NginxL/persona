package persona.feature.inbox

import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.test.runTest
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.inbox.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class LarkBotAvatarTest {
    private class Transport : InboxAssistantTransport {
        val media = mutableListOf<String>()
        val avatars = mutableListOf<String>()
        override val scope = "hub/a"
        override suspend fun inbox() = JsonObject(emptyMap())
        override suspend fun setDone(itemId: String, done: Boolean) = JsonObject(emptyMap())
        override suspend fun reply(itemId: String?, feedId: String?, text: String, clientId: String) = JsonObject(emptyMap())
        override suspend fun messages(feedId: String, around: Long?, before: String?, after: String?) = JsonObject(emptyMap())
        override suspend fun assistant() = JsonObject(emptyMap())
        override suspend fun createConversation(main: Boolean) = JsonObject(emptyMap())
        override suspend fun pins() = JsonObject(emptyMap())
        override suspend fun mutes() = JsonObject(emptyMap())
        override suspend fun media(ref: String) = byteArrayOf(1).also { media += ref }
        override suspend fun avatar(ref: String) = byteArrayOf(2).also { avatars += ref }
    }

    private fun message(avatar: String) = Json.parseToJsonElement("""{"id":"m","sender_id":"b","sender_name":"Bot",
        "sender_type":"bot","sender_avatar":"$avatar","position":1,"parts":[{"kind":"text","text":"hi"}]}""").jsonObject

    @Test fun aSendersPictureIsReadAsAnAvatarWhereverItIs() = runTest {
        val transport = Transport()
        val bot = larkMessage(message("https://cdn.example.com/avatar/bot.jpg"), transport, "feed").avatar!!
        // A full URL is left as it is (no hub thumbnail size); the key still names the pairing.
        assertEquals("hub/a:https://cdn.example.com/avatar/bot.jpg", bot.cacheKey)
        assertArrayEquals(byteArrayOf(2), bot.read())
        val person = larkMessage(message("/api/v1/lark/media/k"), transport, "feed").avatar!!
        assertEquals("hub/a:/api/v1/lark/media/k?size=144", person.cacheKey)
        val _ = person.read()
        assertEquals(listOf("https://cdn.example.com/avatar/bot.jpg", "/api/v1/lark/media/k?size=144"), transport.avatars)
        assertEquals(emptyList<String>(), transport.media)
    }
}
