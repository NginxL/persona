package persona.core.data

import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.QueueDispatcher
import okhttp3.mockwebserver.RecordedRequest

/** Keeps the resident node's exact port-discovery request out of an API response queue and its request assertions. */
internal class PortDiscoveryMockServer {
    private val requests = AtomicInteger()
    private val recorded = LinkedBlockingQueue<RecordedRequest>()
    private val queue = object : QueueDispatcher() {
        @Volatile var api: Dispatcher? = null

        override fun dispatch(request: RecordedRequest): MockResponse {
            if (portDiscoveryRequest(request)) return MockResponse().setResponseCode(404)
            requests.incrementAndGet()
            recorded.add(request)
            return api?.dispatch(request) ?: super.dispatch(request)
        }
    }
    private val server = MockWebServer().apply { dispatcher = queue }

    val requestCount: Int get() = requests.get()

    /** A custom API dispatcher still passes through the same discovery guard and request recording. */
    var dispatcher: Dispatcher
        get() = queue.api ?: queue
        set(value) { queue.api = value }

    fun url(path: String) = server.url(path)
    fun enqueue(response: MockResponse) = server.enqueue(response)
    fun start() = server.start()
    fun shutdown() = server.shutdown()
    fun takeRequest(timeout: Long, unit: TimeUnit): RecordedRequest? = recorded.poll(timeout, unit)
}

/** Installed nodes from either naming generation send this exact diagnostic request, never an API call. */
private fun portDiscoveryRequest(request: RecordedRequest): Boolean = request.method == "GET" && request.path == "/" &&
    request.getHeader("User-Agent") in setOf("jarvis-ports", "persona-ports")
