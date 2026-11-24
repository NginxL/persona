package persona.module.voice

import java.io.Closeable
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import okhttp3.HttpUrl
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.QueueDispatcher
import okhttp3.mockwebserver.RecordedRequest

/** A copy of core:hub's test fixture (test sources are not shared). A queue fixture must not hand an API response to the resident node's unrelated port-discovery GET. */
internal class ProtocolCompatibilityMockServer : Closeable {
    private val requests = AtomicInteger()
    private val recorded = LinkedBlockingQueue<RecordedRequest>()
    private val server = MockWebServer().apply {
        dispatcher = object : QueueDispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                if (portDiscoveryRequest(request)) return MockResponse().setResponseCode(404)
                requests.incrementAndGet()
                recorded.add(request)
                return super.dispatch(request)
            }
        }
    }
    val requestCount: Int get() = requests.get()
    fun url(path: String): HttpUrl = server.url(path)
    fun enqueue(response: MockResponse) = server.enqueue(response)
    fun useHttps(factory: javax.net.ssl.SSLSocketFactory, tunnelProxy: Boolean) = server.useHttps(factory, tunnelProxy)
    fun start(address: java.net.InetAddress, port: Int) = server.start(address, port)

    /** Only known discovery requests are omitted; every ordinary request remains observable and asserted. */
    fun takeRequest(): RecordedRequest = takeRequest(10, TimeUnit.SECONDS)
        ?: error("No fixture request arrived within 10 seconds")

    /** Matches MockWebServer's timed read, including a non-blocking read when the timeout is zero. */
    fun takeRequest(timeout: Long, unit: TimeUnit): RecordedRequest? = recorded.poll(timeout, unit)
    override fun close() = server.close()
}

/** Installed nodes from either naming generation send this exact diagnostic request, never an API call. */
internal fun portDiscoveryRequest(request: RecordedRequest): Boolean = request.method == "GET" && request.path == "/" &&
    request.getHeader("User-Agent") in setOf("jarvis-ports", "persona-ports")
