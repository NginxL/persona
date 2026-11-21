package persona.core.hub

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.contracts.api.LarkMention
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** #576 G122: member lookup and the two existing Lark send routes carry the same ordered mentions. */
class LarkMentionTransportTest {
    @Test fun memberLookupUsesTheConversationRouteWithTheTypedQueryAndLimit30() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"members":[{"user_id":"u-tom","name":"Tom"}]}"""))
            transport.larkMembers("feed/with#separator", "Tom & Zhou")
            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/lark/conversations/feed%2Fwith%23separator/members", request.requestUrl?.encodedPath)
            assertEquals("Tom & Zhou", request.requestUrl?.queryParameter("q"))
            assertEquals("30", request.requestUrl?.queryParameter("limit"))
            assertEquals("Bearer test-token", request.getHeader("Authorization"))
        }
    }

    @Test fun aNormalLarkSendSerializesEveryoneAndPeopleWithoutChangingItsRouteOrFields() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            transport.reply(null, "feed/with#separator", "@Everyone @Tom standup", "client-chat",
                listOf(LarkMention("all", "Everyone"), LarkMention("u-tom", "Tom")))
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/lark/conversations/feed%2Fwith%23separator/messages", request.requestUrl?.encodedPath)
            assertEquals(buildJsonObject {
                put("text", "@Everyone @Tom standup")
                put("client_id", "client-chat")
                put("mentions", JsonArray(listOf(mention("all", "Everyone"), mention("u-tom", "Tom"))))
            }.toString(), request.body.readUtf8())
        }
    }

    @Test fun aThreadReplySerializesPrunedMentionsWithoutChangingItsRouteOrFields() = runBlocking {
        MockWebServer().use { server ->
            val transport = HttpInboxAssistantTransport("scope", server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"ok":true}"""))
            transport.replyInThread("root/with#separator", "@Tom ready", "client-thread", listOf(LarkMention("u-tom", "Tom")))
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/lark/messages/root%2Fwith%23separator/replies", request.requestUrl?.encodedPath)
            assertEquals(buildJsonObject {
                put("text", "@Tom ready")
                put("thread", true)
                put("client_id", "client-thread")
                put("mentions", JsonArray(listOf(mention("u-tom", "Tom"))))
            }.toString(), request.body.readUtf8())
        }
    }

    private fun mention(id: String, name: String) = buildJsonObject {
        put("user_id", id)
        put("name", name)
    }
}
