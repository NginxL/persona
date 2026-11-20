package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.HubScope
import persona.core.contracts.api.MeetingHandoffRequest
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer

/** POST /api/v1/recordings/{id}/handoff (docs/assistant/meeting-minutes.md, #10092). */
class MeetingHandoffTransportTest {
    @Test fun handOffPostsTheWholeChoiceAndReadsTheHubConfirmedResult() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"handoff":{"id":"h1","at":"2026-11-19T12:00:00Z",
                "machine_id":"mac","session_id":"site","title":"Website redesign","transcript":true,
                "state":"failed","problem":"unavailable","future":1}}"""))

            val kept = repository.handOff("r 1/x#?", MeetingHandoffRequest(
                machineId = "mac", sessionId = "site", title = "Website redesign",
                text = "Fix the export", transcript = true,
            ))

            assertEquals("h1", kept.id)
            assertEquals("2026-11-19T12:00:00Z", kept.at)
            assertEquals("mac", kept.machineId)
            assertEquals("site", kept.sessionId)
            assertEquals("Website redesign", kept.title)
            assertEquals(true, kept.transcript)
            assertEquals("failed", kept.state)
            assertEquals("unavailable", kept.problem)
            val request = server.takeRequest()
            assertEquals("POST", request.method)
            assertEquals("/api/v1/recordings/r%201%2Fx%23%3F/handoff", request.path)
            assertEquals("Bearer fixture-token", request.getHeader("Authorization"))
            assertEquals("""{"machine_id":"mac","session_id":"site","title":"Website redesign","text":"Fix the export","transcript":true}""",
                request.body.readUtf8())
        }
    }

    @Test fun handOffAlwaysPostsTextAndTranscriptAndAllowsOptionalReceiptFields() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"handoff":{"id":"h2","at":"2026-11-19T12:01:00Z",
                "machine_id":"mac","session_id":"board","state":"sent"}}"""))

            val kept = repository.handOff("r1", MeetingHandoffRequest("mac", "board", "Data board", "", false))

            assertEquals("sent", kept.state)
            assertNull(kept.title)
            assertNull(kept.transcript)
            assertNull(kept.problem)
            assertEquals("""{"machine_id":"mac","session_id":"board","title":"Data board","text":"","transcript":false}""",
                server.takeRequest().body.readUtf8())
        }
    }

    @Test fun handOffKeepsMeetingRefusalsForTheSheet() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            for (code in listOf("meeting_running", "no_material")) {
                server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"refused","code":"$code"}"""))

                val failure = runCatching {
                    repository.handOff("r1", MeetingHandoffRequest("mac", "site", "Website redesign", "", false))
                }.exceptionOrNull() as HubFailure

                assertEquals(409, failure.status)
                assertEquals(code, failure.code)
                assertEquals("/api/v1/recordings/r1/handoff", server.takeRequest().path)
            }
        }
    }
}
