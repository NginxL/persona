package persona.module.voice

import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.MockResponse
import persona.module.voice.ProtocolCompatibilityMockServer as MockWebServer
import okio.ByteString
import okio.ByteString.Companion.toByteString
import org.junit.Assert.*
import org.junit.Test

class HttpVoiceExtensionsTest {
    // A hub from before #1844 (/api/v1/plugins, the key `plugins`), unless [legacy] says otherwise.
    private fun api(server: MockWebServer, legacy: Boolean = true, current: () -> Boolean = { true }) =
        HttpVoiceExtensions(server.url("/").toString(), "fixture-token", OkHttpClient(), current, legacy = { legacy })

    @Test fun aNewHubAnswersUnderExtensions() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server, legacy = false)
            server.enqueue(MockResponse().setBody("""{"extensions":[{"id":"voice","name":"语音","kind":"voice","status":"running"}]}"""))
            assertEquals("voice", api.voiceExtension()!!.id)
            assertEquals("/api/v1/extensions", server.takeRequest().path)
            server.enqueue(MockResponse().setBody("""{"voices":[]}"""))
            api.voices("voice", null)
            assertEquals("/api/v1/extensions/voice/voices", server.takeRequest().path)
            // The model configuration's purpose kept its name.
            server.enqueue(MockResponse().setResponseCode(404))
            assertNull(api.modelChoice("voice"))
            assertEquals("/api/v1/model-config/purposes/plugin.voice", server.takeRequest().path)
        }
    }

    @Test fun theExtensionListYieldsTheFirstRunningVoiceExtensionAndAnOldHubHasNone() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server)
            server.enqueue(MockResponse().setBody(
                """{"plugins":[{"id":"notes","kind":"notes","status":"running"},{"id":"v0","kind":"voice","status":"starting"},""" +
                    """{"id":"voice","name":"语音","kind":"voice","status":"running"},{"id":"v2","kind":"voice","status":"running"}]}""",
            ))
            val extension = api.voiceExtension()!!
            assertEquals("voice" to "语音", extension.id to extension.name)
            val request = server.takeRequest()
            assertEquals("/api/v1/plugins", request.path)
            assertEquals("Bearer fixture-token", request.getHeader("Authorization"))
            server.enqueue(MockResponse().setBody("""{"plugins":[]}"""))
            assertNull(api.voiceExtension())
            server.enqueue(MockResponse().setResponseCode(404))
            assertNull("A hub from before plugins has none", api.voiceExtension())
            server.enqueue(MockResponse().setResponseCode(500).setBody("""{"error":"boom"}"""))
            try { api.voiceExtension(); fail("A failing hub is not one without plugins") } catch (error: HubFailure) { assertEquals(500, error.status) }
        }
    }

    @Test fun voicesFollowTheModelAndAExtensionWithoutModelsHasOnlyItsOwn() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server)
            server.enqueue(MockResponse().setBody(
                """{"voices":[{"id":"soft","name":"温柔","gender":"female","languages":["zh","en"]},{"id":"calm","name":"沉稳"}],"default":"calm"}""",
            ))
            val voices = api.voices("voice", "model a")
            assertEquals(listOf("soft", "calm"), voices.voices.map { it.id })
            assertEquals(listOf("zh", "en"), voices.voices.first().languages)
            assertEquals("calm", voices.default)
            assertEquals("/api/v1/plugins/voice/voices?model=model%20a", server.takeRequest().path)
            server.enqueue(MockResponse().setBody("""{"voices":[]}"""))
            api.voices("voice", null)
            assertEquals("No model: the plugin's default", "/api/v1/plugins/voice/voices", server.takeRequest().path)
            server.enqueue(MockResponse().setResponseCode(404))
            try { api.voices("voice", null); fail("Voices of a missing plugin are an error") } catch (error: HubFailure) { assertEquals(404, error.status) }
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"models":[{"id":"fast","name":"快"},{"id":"deep"}],"default":"deep"}"""))
            val models = api.models("voice")
            assertEquals(listOf("快", "deep"), models.models.map { it.label })
            assertEquals("deep", models.default)
            assertEquals("/api/v1/plugins/voice/models", server.takeRequest().path)
            server.enqueue(MockResponse().setResponseCode(404))
            assertTrue(api.models("voice").models.isEmpty())
            server.enqueue(MockResponse().setBody("not json"))
            try { api.models("voice"); fail("A broken body is not an empty list") } catch (error: HubFailure) { assertEquals(HubError.Protocol, error.kind) }
        }
    }

    @Test fun theModelConfigurationIsReadAndOnlyAnOldHubCountsAsNone() = runBlocking {
        MockWebServer().use { server ->
            val api = api(server)
            server.enqueue(MockResponse().setBody("""{"purpose":{"id":"plugin.voice","effective":{"provider":"plugin.voice","model":"deep"},"status":"未验证"},"providers":[]}"""))
            assertEquals(VoiceModelChoice("deep", null), api.modelChoice("voice"))
            assertEquals("/api/v1/model-config/purposes/plugin.voice", server.takeRequest().path)
            server.enqueue(MockResponse().setBody("""{"purpose":{"id":"plugin.voice","effective":{},"status":" 读不到扩展模型列表 "}}"""))
            assertEquals(VoiceModelChoice(null, "读不到扩展模型列表"), api.modelChoice("voice"))
            server.enqueue(MockResponse().setBody("""{"purpose":{"id":"plugin.voice","effective":{"model":""},"status":""}}"""))
            assertEquals(VoiceModelChoice(null, null), api.modelChoice("voice"))
            server.enqueue(MockResponse().setResponseCode(404))
            assertNull("An older hub has no model configuration", api.modelChoice("voice"))
            server.enqueue(MockResponse().setResponseCode(502).setBody("""{"error":"upstream"}"""))
            try { api.modelChoice("voice"); fail("A newer hub that cannot be read is not one without configuration") } catch (error: HubFailure) { assertEquals(502, error.status) }
            server.enqueue(MockResponse().setBody("""{"providers":[]}"""))
            try { api.modelChoice("voice"); fail("An answer without the purpose is not none") } catch (error: HubFailure) { assertEquals(HubError.Protocol, error.kind) }
        }
    }

    @Test fun aChangedPairingCancelsAndOddIdsNeverBecomePaths() = runBlocking {
        MockWebServer().use { server ->
            var current = true
            val api = api(server) { current }
            try { api.models("../metrics"); fail("Not a plugin id") } catch (_: IllegalArgumentException) { }
            current = false
            try { api.voiceExtension(); fail("Old pairing must be cancelled") } catch (_: CancellationException) { }
            try { api.connect("voice", Recorder()); fail("Old pairing must be cancelled") } catch (_: CancellationException) { }
            assertEquals(0, server.requestCount)
        }
    }

    @Test fun theSocketCarriesTextAndAudioBothWaysAndReportsTheExtensionsCloseOnce() {
        MockWebServer().use { server ->
            val received = LinkedBlockingQueue<Any>()
            val hub = LinkedBlockingQueue<WebSocket>()
            server.enqueue(MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) { hub.add(webSocket) }
                override fun onMessage(webSocket: WebSocket, text: String) { received.add(text) }
                override fun onMessage(webSocket: WebSocket, bytes: ByteString) { received.add(bytes) }
            }))
            val events = Recorder()
            val socket = api(server).connect("voice", events)
            assertEquals("open", events.next())
            val request = server.takeRequest()
            assertEquals("/api/v1/plugins/voice/ws", request.path)
            assertEquals("Bearer fixture-token", request.getHeader("Authorization"))
            assertTrue(socket.send("""{"type":"start"}"""))
            assertTrue(socket.send(byteArrayOf(9, 1, 2, 3, 9), 1, 3))
            assertEquals("""{"type":"start"}""", received.poll(5, TimeUnit.SECONDS))
            assertEquals(byteArrayOf(1, 2, 3).toByteString(), received.poll(5, TimeUnit.SECONDS))
            val end = hub.poll(5, TimeUnit.SECONDS)!!
            end.send("""{"type":"ready"}""")
            end.send(byteArrayOf(7, 7).toByteString())
            assertEquals("text {\"type\":\"ready\"}", events.next())
            assertEquals("binary 2", events.next())
            end.close(1001, "restarting")
            assertEquals("closed 1001 restarting", events.next())
            assertNull("One ending only", events.events.poll(500, TimeUnit.MILLISECONDS))
        }
    }

    @Test fun aRefusedUpgradeSaysWhyAndAClosedSocketStaysQuiet() {
        MockWebServer().use { server ->
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"no such plugin"}"""))
            val refused = Recorder()
            api(server).connect("voice", refused)
            val failure = refused.next()
            assertEquals("failure 404 no such plugin", failure)

            val hub = LinkedBlockingQueue<WebSocket>()
            val closing = LinkedBlockingQueue<Int>()
            server.enqueue(MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                override fun onOpen(webSocket: WebSocket, response: Response) { hub.add(webSocket) }
                override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                    webSocket.send("""{"type":"transcript"}""")
                    closing.add(code)
                    webSocket.close(code, null)
                }
            }))
            val events = Recorder()
            val socket = api(server).connect("voice", events)
            assertEquals("open", events.next())
            hub.poll(5, TimeUnit.SECONDS)!!
            socket.close(1000, "bye")
            assertEquals(1000, closing.poll(5, TimeUnit.SECONDS))
            assertNull("Nothing after the page closed it", events.events.poll(500, TimeUnit.MILLISECONDS))
        }
    }

    private class Recorder : VoiceSocketListener {
        val events = LinkedBlockingQueue<String>()
        fun next(): String = events.poll(5, TimeUnit.SECONDS) ?: error("No event")
        override fun onOpen() { events.add("open") }
        override fun onText(text: String) { events.add("text $text") }
        override fun onBinary(bytes: ByteArray) { events.add("binary ${bytes.size}") }
        override fun onClosed(code: Int, reason: String) { events.add("closed $code $reason") }
        override fun onFailure(error: Throwable, status: Int?) { events.add("failure $status ${error.message}") }
    }
}
