package persona.core.hub.dev

import java.io.IOException
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import persona.core.contracts.DevProbe
import persona.core.contracts.DevProbeSink
import persona.core.contracts.DevScrub

class DevHttpInterceptorTest {
    private val server = MockWebServer()
    private val sink = Sink()
    private val client = OkHttpClient.Builder().addInterceptor(DevHttpInterceptor()).build()
    @Before fun setUp() { server.start(); DevProbe.install(sink) }
    @After fun cleanUp() { DevProbe.install(null); server.shutdown(); client.dispatcher.executorService.shutdown() }

    @Test fun successfulRequestsMeasureTheActualBodyBytesAndStatus() {
        server.enqueue(MockResponse().setBody("hello").setHeader("Content-Type", "text/plain"))
        val url = server.url("/api/v1/machines/m1/items?token=secret")
        client.newCall(Request.Builder().url(url).post("abc".toRequestBody("text/plain".toMediaType())).build()).execute().use { response ->
            assertEquals("hello", response.body!!.string())
        }

        assertEquals(1, sink.requests.size)
        val request = sink.requests.single()
        assertEquals("POST", request.method)
        assertEquals(url.toString(), request.url)
        assertEquals(200, request.status)
        assertEquals(5, request.bytesIn)
        assertEquals(3, request.bytesOut)
        assertTrue(request.millis >= 0)
        assertEquals("/machines/:id/items", DevScrub.route(request.url!!))
    }

    @Test fun httpErrorsAreCountedAndWebSocketUpgradesAreNotDoubleCounted() {
        server.enqueue(MockResponse().setResponseCode(502).setBody("bad"))
        client.newCall(Request.Builder().url(server.url("/api/v1/sessions")).build()).execute().use { it.body!!.string() }
        server.enqueue(MockResponse().setResponseCode(200).setBody("ordinary response"))
        client.newCall(Request.Builder().url(server.url("/api/v1/ws")).header("Upgrade", "websocket").build()).execute().use { it.body!!.string() }

        assertEquals(1, sink.requests.size)
        assertEquals(502, sink.requests.single().status)
        assertEquals(3, sink.requests.single().bytesIn)
    }

    @Test fun transportFailuresHaveNoHttpStatus() {
        server.shutdown()
        var failed = false
        try {
            client.newCall(Request.Builder().url(server.url("/api/v1/sessions")).build()).execute().close()
        } catch (_: IOException) { failed = true }

        assertTrue(failed)
        assertEquals(1, sink.requests.size)
        val request = sink.requests.single()
        assertEquals(null, request.status)
        assertEquals(0, request.bytesIn)
    }

    private data class RequestMetric(val method: String, val url: String?, val status: Int?, val bytesIn: Long, val bytesOut: Long, val millis: Long)
    private class Sink : DevProbeSink {
        val requests = mutableListOf<RequestMetric>()
        override fun page(name: String) = Unit
        override fun request(method: String, url: String?, status: Int?, bytesIn: Long, bytesOut: Long, millis: Long) {
            requests += RequestMetric(method, url, status, bytesIn, bytesOut, millis)
        }
        override fun frame(type: String, bytes: Long) = Unit
        override fun socket(event: String) = Unit
    }
}
