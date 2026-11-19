package persona.core.data

import java.net.HttpURLConnection
import java.util.concurrent.TimeUnit
import okhttp3.mockwebserver.MockResponse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class PortDiscoveryMockServerTest {
    @Test fun discoveryCannotStealAnApiResponseOrItsRecordedRequest() {
        for (agent in listOf("jarvis-ports", "persona-ports")) {
            val server = PortDiscoveryMockServer().apply { start() }
            try {
                server.enqueue(MockResponse().setBody("API fixture"))
                assertEquals(404, get(server, "/", agent).first)
                // The same User-Agent on a real API path is not a discovery request.
                assertEquals(200 to "API fixture", get(server, "/api/v1/sessions", agent))
                assertEquals("/api/v1/sessions", server.takeRequest(1, TimeUnit.SECONDS)?.path)
                assertNull(server.takeRequest(50, TimeUnit.MILLISECONDS))
                assertEquals(1, server.requestCount)
            } finally {
                server.shutdown()
            }
        }
    }

    @Test fun ordinaryRootRequestRemainsVisible() {
        val server = PortDiscoveryMockServer().apply { start() }
        try {
            server.enqueue(MockResponse().setBody("ordinary"))
            assertEquals(200 to "ordinary", get(server, "/", "ordinary-client"))
            assertEquals("/", server.takeRequest(1, TimeUnit.SECONDS)?.path)
            assertEquals(1, server.requestCount)
        } finally {
            server.shutdown()
        }
    }

    private fun get(server: PortDiscoveryMockServer, path: String, agent: String): Pair<Int, String> {
        val connection = server.url(path).toUrl().openConnection() as HttpURLConnection
        connection.connectTimeout = 1_000
        connection.readTimeout = 1_000
        connection.setRequestProperty("User-Agent", agent)
        return try {
            val code = connection.responseCode
            val stream = if (code in 200..299) connection.inputStream else connection.errorStream
            code to (stream?.bufferedReader()?.use { it.readText() }.orEmpty())
        } finally {
            connection.disconnect()
        }
    }
}
