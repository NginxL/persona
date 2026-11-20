package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertThrows
import org.junit.Test
import persona.core.contracts.SessionKey
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** GET …/mode-changes: one read-only route, all four records, and no lines invented from a malformed answer. */
class HttpModeChangesTest {
    private fun transport(server: MockWebServer): HubChatTransport {
        val client = OkHttpClient.Builder().retryOnConnectionFailure(false).build()
        return HubChatTransport({ path, method, body, headers ->
            client.newCall(Request.Builder().url(server.url(path)).method(method, body).apply {
                headers.forEach { (key, value) -> header(key, value) }
            }.build()).execute()
        })
    }

    private val key = SessionKey("machine", "session")

    @Test fun readsTheSessionRouteAndDecodesEveryKindAndField() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""
                {"changes":[
                  {"kind":"set","from":"default","to":"bypassPermissions","applied":"restart","actor_kind":"device","device_name":"Phone","at":"2026-11-18T09:01:00Z"},
                  {"kind":"restart_done","to":"bypassPermissions","actor_kind":"hub","reason":"already_in","at":"2026-11-18T09:02:00Z"},
                  {"kind":"restart_failed","to":"bypassPermissions","actor_kind":"extension","device_name":"CI","reason":"start_failed","at":"2026-11-18T09:03:00Z"},
                  {"kind":"restart_dropped","from":"default","to":"bypassPermissions","applied":"now","actor_kind":"device","device_name":"Tablet","reason":"process_exited","at":"2026-11-18T09:04:00Z"}
                ]}
            """.trimIndent()))

            val changes = transport(server).modeChanges(key)

            val request = server.takeRequest()
            assertEquals("GET", request.method)
            assertEquals("/api/v1/sessions/machine/session/mode-changes", request.requestUrl!!.encodedPath)
            assertNull(request.requestUrl!!.encodedQuery)
            assertEquals(listOf("set", "restart_done", "restart_failed", "restart_dropped"), changes.map { it.kind })
            assertEquals("default", changes[0].from)
            assertEquals("bypassPermissions", changes[0].to)
            assertEquals("restart", changes[0].applied)
            assertEquals("device", changes[0].actorKind)
            assertEquals("Phone", changes[0].deviceName)
            assertNull(changes[0].reason)
            assertEquals("2026-11-18T09:01:00Z", changes[0].at)
            assertEquals("hub", changes[1].actorKind)
            assertEquals("already_in", changes[1].reason)
            assertEquals("2026-11-18T09:02:00Z", changes[1].at)
            assertEquals("extension", changes[2].actorKind)
            assertEquals("CI", changes[2].deviceName)
            assertEquals("start_failed", changes[2].reason)
            assertEquals("2026-11-18T09:03:00Z", changes[2].at)
            assertEquals("default", changes[3].from)
            assertEquals("now", changes[3].applied)
            assertEquals("Tablet", changes[3].deviceName)
            assertEquals("process_exited", changes[3].reason)
            assertEquals("2026-11-18T09:04:00Z", changes[3].at)
        }
    }

    @Test fun keepsWellFormedRecordsAndDropsMalformedOrUnknownOnes() = runBlocking {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setBody("""
                {"changes":[
                  {"kind":"set","from":"default","to":"acceptEdits","applied":"now","actor_kind":"device","device_name":"Phone","at":"2026-11-18T09:01:00Z"},
                  {"kind":"made_up","at":"2026-11-18T09:02:00Z"},
                  {"kind":"set","at":"never"},
                  {"kind":"restart_done"},
                  "not a record"
                ]}
            """.trimIndent()))

            val changes = transport(server).modeChanges(key)

            assertEquals(1, changes.size)
            assertEquals("set", changes.single().kind)
            assertEquals("acceptEdits", changes.single().to)
            assertEquals("2026-11-18T09:01:00Z", changes.single().at)
        }
    }

    @Test fun emptyOrAnotherEnvelopeHasNoChangesAndMalformedJsonIsAProtocolFailure() = runBlocking {
        MockWebServer().use { server ->
            val api = transport(server)
            for (body in listOf("""{"changes":[]}""", "{}", """{"items":[]}""", """{"changes":null}""")) {
                server.enqueue(MockResponse().setBody(body))
                assertEquals(body, emptyList<persona.core.contracts.ModeChange>(), api.modeChanges(key))
            }
            server.enqueue(MockResponse().setBody("not json"))
            val failure = assertThrows(HubFailure::class.java) { runBlocking { api.modeChanges(key) } }
            assertEquals(HubError.Protocol, failure.kind)
        }
    }
}
