package persona.core.hub

import java.io.IOException
import java.net.InetAddress
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.ReplyImageSource

class HubReplyImagesTest {
    @Test fun usesTheSelectedHubsAuthenticatedEndpointAndKeepsTheOriginOutOfTheRequest() = runBlocking {
        MockWebServer().use { origin ->
            origin.start(InetAddress.getLoopbackAddress(), 0)
            val target = origin.url("/landscape?palette=blue%2Bgreen&level=3").toString()
            var fail = false
            val received = java.util.concurrent.LinkedBlockingQueue<okhttp3.mockwebserver.RecordedRequest>()
            LoopbackHub(features = listOf(ReplyImageSource.FEATURE), api = {
                received.offer(it)
                if (fail) MockResponse().setResponseCode(403).setBody("""{"error":"forbidden"}""")
                else MockResponse().setBody("synthetic image bytes")
            }).use { hub ->
                OkHttpHubConnection(HubConnectionConfig("test-hub", null, "fixture-device", listOf(hub.endpoint("lan")), "fixture"),
                    HubNetwork("loopback")).use { connection ->
                    withTimeout(5000) { connection.serverFeatures.first { ReplyImageSource.FEATURE in it } }
                    val images = HubReplyImages(connection) { true }
                    assertEquals("synthetic image bytes", images.load(target, 512, allowDirect = false).decodeToString())
                    val request = received.take()
                    assertEquals("/api/v1/remote-image", request.requestUrl!!.encodedPath)
                    assertEquals(target, request.requestUrl!!.queryParameter("url"))
                    assertEquals("512", request.requestUrl!!.queryParameter("size"))
                    assertEquals("Bearer fixture-device", request.getHeader("Authorization"))
                    fail = true
                    assertTrue(runCatching { images.load(target, 2048, allowDirect = true) }.isFailure)
                    assertEquals("2048", received.take().requestUrl!!.queryParameter("size"))
                    assertEquals(0, origin.requestCount)
                }
            }
        }
    }

    @Test fun directAccessRequiresConsentAndPairingChangesDiscardResults() = runBlocking {
        val features = MutableStateFlow(emptySet<String>())
        val origin = "https://images.example/sample.png"
        val visited = mutableListOf<String>()
        var current = true
        val images = HubReplyImages(features, { error("No hub access expected") }, {
            visited.add(it)
            "fixture".encodeToByteArray()
        }, { current })
        assertTrue(runCatching { images.load(origin, 256, allowDirect = false) }.isFailure)
        assertTrue(visited.isEmpty())
        assertEquals("fixture", images.load(origin, 256, allowDirect = true).decodeToString())
        assertEquals(listOf(origin), visited)
        assertTrue(runCatching { images.load("https://account@images.example/p", 256, true) }.isFailure)
        current = false
        assertTrue(runCatching { images.load(origin, 256, true) }.exceptionOrNull() is CancellationException)
        assertEquals(listOf(origin), visited)
        current = true
        val changedDuringRead = HubReplyImages(features, { error("No hub access expected") }, {
            current = false
            byteArrayOf(1)
        }, { current })
        assertTrue(runCatching { changedDuringRead.load(origin, 256, true) }.exceptionOrNull() is CancellationException)
    }
}
