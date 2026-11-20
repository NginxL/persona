package persona.core.hub

import java.util.concurrent.LinkedBlockingQueue
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.RecordedRequest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.SavedPromptsEvent
import persona.core.contracts.ServerEvent
import persona.core.contracts.api.AssistantManagerChoice

class HttpSavedPromptsTest {
    @Test fun requestsUseTheCapturedAssistantAndEncodeIdsAsOneSegment() = runBlocking {
        val responses = LinkedBlockingQueue<MockResponse>()
        val requests = LinkedBlockingQueue<RecordedRequest>()
        val path = "/api/v1/assistant/saved-prompts"
        val first = """{"id":"first","name":"review","text":"Review this.","created_at":"2026-11-19T00:00:00Z"}"""
        val second = """{"id":"folder/item?","name":"notes","text":"Write notes."}"""
        LoopbackHub(api = { request ->
            requests.add(request)
            responses.remove()
        }).use { hub ->
            OkHttpHubConnection(HubConnectionConfig(hub.hubId, null, "fixture-device", listOf(hub.endpoint("lan")), "test"),
                HubNetwork("loopback")).use { connection ->
                withTimeout(8000) { connection.currentHello.first { it != null } }
                val api = HttpSavedPrompts(connection, "work")
                val default = HttpSavedPrompts(connection, null)
                AssistantManagerChoice.choose(hub.hubId, "elsewhere", "assistant")
                try {
                    fun asked(method: String, url: String, body: String = "", manager: String? = "work") {
                        val request = requests.remove()
                        assertEquals(method, request.method)
                        assertEquals(url, request.requestUrl!!.encodedPath)
                        assertEquals(manager, request.requestUrl!!.queryParameter("manager"))
                        assertNull(request.getHeader(ManagerHeader))
                        assertEquals("Bearer fixture-device", request.getHeader("Authorization"))
                        assertEquals(body, request.body.readUtf8())
                    }
                    responses.add(MockResponse().setBody("""{"prompts":[$second,$first]}"""))
                    val list = api.list()!!
                    assertEquals(listOf("folder/item?", "first"), list.map { it.id })
                    assertEquals("2026-11-19T00:00:00Z", list.last().createdAt)
                    asked("GET", path)

                    responses.add(MockResponse().setBody("""{"prompt":$second}"""))
                    assertEquals("folder/item?", api.save(null, "notes", "Write notes.").id)
                    asked("POST", path, """{"name":"notes","text":"Write notes."}""")

                    responses.add(MockResponse().setBody("""{"prompt":$second}"""))
                    api.save("folder/item?", "notes", "Write notes.")
                    asked("PUT", "$path/folder%2Fitem%3F", """{"name":"notes","text":"Write notes."}""")

                    responses.add(MockResponse().setBody("""{"prompts":[$first,$second]}"""))
                    assertEquals(list.reversed(), api.reorder(list.reversed().map { it.id }))
                    asked("PUT", "$path/order", """{"ids":["first","folder/item?"]}""")

                    responses.add(MockResponse().setResponseCode(204))
                    api.delete("folder/item?")
                    asked("DELETE", "$path/folder%2Fitem%3F")

                    responses.add(MockResponse().setResponseCode(404))
                    assertNull(default.list())
                    asked("GET", path, manager = null)

                    responses.add(MockResponse().setResponseCode(404).setBody("""{"error":"Prompt gone"}"""))
                    val gone = runCatching { api.save("folder/item?", "notes", "Write notes.") }.exceptionOrNull() as HubFailure
                    assertEquals(404, gone.status)
                    assertEquals("Prompt gone", gone.message)
                    asked("PUT", "$path/folder%2Fitem%3F", """{"name":"notes","text":"Write notes."}""")
                } finally {
                    AssistantManagerChoice.reset()
                }
            }
        }
    }

    @Test fun savedPromptsPushKeepsTheOptionalManager() {
        assertEquals(SavedPromptsEvent(), ServerEvent.decode("""{"type":"assistant_saved_prompts"}"""))
        val event: ServerEvent = SavedPromptsEvent("work")
        assertEquals(event, ServerEvent.decode(JsonCodec.encodeToString(event)))
    }
}
