package persona.core.hub

import java.util.concurrent.LinkedBlockingQueue
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredSnapshot

class HttpStarredMessagesTest {
    @Test fun requestsScopeToCapturedAssistantAndHandleOlderHub404() {
        runBlocking {
        val responses = LinkedBlockingQueue<MockResponse>()
        val requests = LinkedBlockingQueue<RecordedRequest>()
        val base = "/api/v1/assistant/starred"
        val starJson = """{"id":"sm01/a?","machine_id":"server","session_id":"s1","item_id":"m1","conversation":"assistant","role":"assistant","title":"Chat","speaker":"Persona","text":"Saved note","starred_at":"2026-11-22T01:00:00Z"}"""
        LoopbackHub(api = { request ->
            requests.add(request)
            responses.remove()
        }).use { hub ->
            OkHttpHubConnection(
                HubConnectionConfig(hub.hubId, null, "fixture-device", listOf(hub.endpoint("lan")), "test"),
                HubNetwork("loopback"),
            ).use { connection ->
                withTimeout(8000) { connection.currentHello.first { it != null } }
                val api = HttpStarredMessages(connection, "work")
                val default = HttpStarredMessages(connection, null)
                AssistantManagerChoice.choose(hub.hubId, "elsewhere", "assistant")
                try {
                    fun asked(method: String, path: String, manager: String? = "work", index: String? = null): RecordedRequest {
                        val req = requests.remove()
                        assertEquals(method, req.method)
                        assertEquals(path, req.requestUrl!!.encodedPath)
                        assertEquals(manager, req.requestUrl!!.queryParameter("manager"))
                        assertEquals(index, req.requestUrl!!.queryParameter("index"))
                        assertNull(req.getHeader(ManagerHeader))
                        assertEquals("Bearer fixture-device", req.getHeader("Authorization"))
                        return req
                    }

                    responses.add(MockResponse().setBody("""{"refs":[{"id":"sm01/a?","machine_id":"server","session_id":"s1","item_id":"m1","conversation":"assistant"}],"can_write":true}"""))
                    val idx = api.index()!!
                    assertTrue(idx.canWrite)
                    assertEquals("sm01/a?", idx.refs.single().id)
                    asked("GET", base, index = "1")

                    responses.add(MockResponse().setBody("""{"stars":[$starJson],"can_write":true}"""))
                    val list = api.list()!!
                    assertEquals("Saved note", list.stars.single().text)
                    asked("GET", base)

                    responses.add(MockResponse().setBody("""{"star":$starJson}"""))
                    val added = api.add(
                        StarredSnapshot(
                            machineId = "server",
                            sessionId = "s1",
                            itemId = "m1",
                            conversation = StarredConversation.Assistant,
                            role = "assistant",
                            title = "Chat",
                            speaker = "Persona",
                            text = "  Saved note  ",
                        ),
                    )
                    assertEquals("sm01/a?", added.id)
                    val postReq = asked("POST", base)
                    assertTrue(postReq.body.readUtf8().contains(""""text":"Saved note""""))

                    responses.add(MockResponse().setBody("""{"removed":$starJson}"""))
                    assertEquals("sm01/a?", api.remove("sm01/a?")?.id)
                    asked("DELETE", "$base/sm01%2Fa%3F")

                    responses.add(MockResponse().setResponseCode(404))
                    assertNull(default.index())
                    asked("GET", base, manager = null, index = "1")

                    responses.add(MockResponse().setResponseCode(404))
                    assertNull(default.list())
                    asked("GET", base, manager = null)
                } finally {
                    AssistantManagerChoice.reset()
                }
            }
        }
        }
    }
}
