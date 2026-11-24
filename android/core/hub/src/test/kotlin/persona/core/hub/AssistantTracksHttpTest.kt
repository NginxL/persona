package persona.core.hub

import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ExtensionTrust
import persona.core.contracts.api.TrackChange
import persona.core.contracts.api.TrackCreate
import persona.core.contracts.api.TracksUnsupported
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** The domain spaces' routes (#13500, docs/protocol/protocol.md): paths, methods, bodies, and what an older hub answers. */
class AssistantTracksHttpTest {
    private fun body(request: okhttp3.mockwebserver.RecordedRequest) = JsonCodec.parseToJsonElement(request.body.readUtf8()).jsonObject

    @Test fun readsTracksAndKindsToleratingUnknownFields() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"tracks":[{"slug":"qa-plain","kind":"space","title":"Plain","future":1,
                "config":{"schema":1,"sidebar":{"group":"spaces","order":2},"new_field":[1]}},{"slug":"qa-finance"}]}"""))
            val tracks = api.tracks()
            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/assistant/tracks", request.requestUrl?.encodedPath)
            assertEquals(listOf("qa-plain", "qa-finance"), tracks.map { it.slug })
            assertEquals("spaces", tracks[0].config.sidebar.group)
            assertEquals("", tracks[1].config.sidebar.group)

            server.enqueue(MockResponse().setBody("""{"kinds":[{"slug":"space","title":"Space"},{"slug":"x","disabled":"schema"}]}"""))
            val kinds = api.trackKinds()
            assertEquals("/api/v1/assistant/track-kinds", server.takeRequest().requestUrl?.encodedPath)
            assertEquals(listOf("", "schema"), kinds.map { it.disabled })
        }
    }

    @Test fun anAssistantWithoutTracksSaysSo() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{}"""))
            assertTrue(runCatching { api.tracks() }.exceptionOrNull() is TracksUnsupported)
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not found"}"""))
            assertTrue(runCatching { api.tracks() }.exceptionOrNull() is TracksUnsupported)
            server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"offline"}"""))
            assertEquals(503, (runCatching { api.tracks() }.exceptionOrNull() as HubFailure).status)
            server.enqueue(MockResponse().setBody("""{}"""))
            assertNull(api.extensionTrust())
        }
    }

    @Test fun createsAndArchivesWithOnlyTheFieldsGiven() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"slug":"qa-plain","kind":"space","title":"Plain","revision":1}"""))
            val created = api.createTrack(TrackCreate("qa-plain", "space", "Plain", "book", "", listOf("finance")))
            val post = server.takeRequest()
            assertEquals("POST", post.method)
            assertEquals("/api/v1/assistant/tracks", post.requestUrl?.encodedPath)
            val sent = body(post)
            assertEquals("qa-plain", sent.getValue("slug").jsonPrimitive.content)
            assertEquals("finance", sent.getValue("default_categories").jsonArray.single().jsonPrimitive.content)
            assertEquals(1L, created.revision)

            server.enqueue(MockResponse().setBody("""{"slug":"qa-plain","archived":true,"revision":2}"""))
            assertTrue(api.changeTrack("qa-plain", TrackChange(revision = 1, archived = true)).archived)
            val put = server.takeRequest()
            assertEquals("PUT", put.method)
            assertEquals("/api/v1/assistant/tracks/qa-plain", put.requestUrl?.encodedPath)
            assertEquals(setOf("revision", "archived"), body(put).keys)
        }
    }

    @Test fun spaceMdSendsTheBaseDigestAndKeepsTheStatus() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            server.enqueue(MockResponse().setBody("""{"content":"# Plain\n","digest":"d1","state":"s"}"""))
            assertEquals("d1", api.spaceMd("qa-plain").digest)
            assertEquals("/api/v1/assistant/tracks/qa-plain/space-md", server.takeRequest().requestUrl?.encodedPath)

            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"stale"}"""))
            val failure = runCatching { api.writeSpaceMd("qa-plain", "new", "d1") }.exceptionOrNull() as HubFailure
            assertEquals(409, failure.status)
            val put = server.takeRequest()
            assertEquals("PUT", put.method)
            val sent = body(put)
            assertEquals("new", sent.getValue("content").jsonPrimitive.content)
            assertEquals("d1", sent.getValue("base_digest").jsonPrimitive.content)
        }
    }

    @Test fun categoriesAndTrust() = runBlocking {
        MockWebServer().use { server ->
            val api = HttpAssistantManagement(server.url("/").toString(), "test-token", OkHttpClient())
            val chat = SessionKey("mac", "c1")
            server.enqueue(MockResponse().setBody("""{"categories":["finance"],"floor":["finance"],"removed":["health"]}"""))
            assertEquals(listOf("health"), api.conversationCategories(chat).removed)
            assertEquals("/api/v1/assistant/conversations/c1/categories", server.takeRequest().requestUrl?.encodedPath)
            server.enqueue(MockResponse().setBody("""{"categories":["finance","health"]}"""))
            api.changeConversationCategories(chat, add = listOf("health"))
            val change = body(server.takeRequest())
            assertEquals(setOf("add"), change.keys)

            server.enqueue(MockResponse().setBody("""{"items":[{"extension":"notes","trust":"owner","categories":[]}]}"""))
            assertTrue(api.extensionTrust()!!.single().owner)
            assertEquals("/api/v1/assistant/extension-trust", server.takeRequest().requestUrl?.encodedPath)
            server.enqueue(MockResponse().setBody("""{"extension":"notes","trust":"third_party","categories":[]}"""))
            assertFalse(api.setExtensionTrust("notes", ExtensionTrust("notes", ExtensionTrust.THIRD_PARTY, emptyList())).owner)
            val put = server.takeRequest()
            assertEquals("/api/v1/assistant/extension-trust/notes", put.requestUrl?.encodedPath)
            // An empty grant is sent as an empty list, never left out.
            assertEquals(0, body(put).getValue("categories").jsonArray.size)
        }
    }
}
