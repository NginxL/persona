package persona.core.contracts

import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test

class DevProbeTest {
    @Before fun reset() = DevProbe.resetForTest()
    @After fun cleanUp() = DevProbe.resetForTest()

    @Test fun pagesRestoreAndMetricsFlowOnlyWhileInstalled() {
        DevProbe.tab("assistant")
        DevProbe.request("GET", "https://private.example.com/api/v1/sessions?token=off", 200, 1, 1, 1)
        val sink = RecordingSink()
        DevProbe.install(sink)
        assertEquals(listOf("assistant"), sink.pages)

        val chat = DevProbe.enter("chat")
        val settings = DevProbe.enter("settings")
        DevProbe.leave(chat)
        DevProbe.leave(settings)
        assertEquals(listOf("assistant", "chat", "settings", "settings", "assistant"), sink.pages)

        DevProbe.request("GET", "/api/v1/sessions", 200, 10, 20, 30)
        DevProbe.frame("sessions", 40)
        DevProbe.socket("connected")
        assertEquals(listOf("GET /api/v1/sessions 200 10 20 30"), sink.requests)
        assertEquals(listOf("sessions 40"), sink.frames)
        assertEquals(listOf("connected"), sink.sockets)

        DevProbe.install(null)
        DevProbe.request("GET", "/api/v1/sessions", 500, 0, 0, 1)
        DevProbe.frame("sessions", 1)
        DevProbe.socket("dropped")
        assertEquals(1, sink.requests.size)
        assertEquals(1, sink.frames.size)
        assertEquals(1, sink.sockets.size)
        assertTrue(chat != 0L && settings != 0L && chat != settings)
    }

    private class RecordingSink : DevProbeSink {
        val pages = mutableListOf<String>()
        val requests = mutableListOf<String>()
        val frames = mutableListOf<String>()
        val sockets = mutableListOf<String>()
        override fun page(name: String) { pages += name }
        override fun request(method: String, url: String?, status: Int?, bytesIn: Long, bytesOut: Long, millis: Long) {
            requests += "$method $url $status $bytesIn $bytesOut $millis"
        }
        override fun frame(type: String, bytes: Long) { frames += "$type $bytes" }
        override fun socket(event: String) { sockets += event }
    }
}
