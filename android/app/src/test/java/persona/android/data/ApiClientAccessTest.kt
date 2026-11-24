package persona.android.data

import kotlinx.coroutines.runBlocking
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import org.junit.Assert.assertEquals
import org.junit.Test

/** A 401 from Cloudflare Access is about the service token: it must not end the pairing like the hub's own 401 (#3502). */
class ApiClientAccessTest {
    @Test fun onlyTheHubsOwn401EndsThePairing() = runBlocking {
        MockWebServer().use { hub ->
            hub.enqueue(MockResponse().setResponseCode(401).setHeader("WWW-Authenticate", "Cloudflare-Access")
                .setHeader("Content-Type", "text/html").setBody("<html>Forbidden</html>"))
            hub.enqueue(MockResponse().setResponseCode(401).setHeader("Content-Type", "application/json")
                .setBody("""{"error":"unauthorized"}"""))
            var revoked = 0
            val api = ApiClient(hub.url("/").toString(), "t", onUnauthorized = { revoked++ })
            val _ = runCatching { api.raw("GET", "/api/v1/sessions") }
            assertEquals(0, revoked)
            val _ = runCatching { api.raw("GET", "/api/v1/sessions") }
            assertEquals(1, revoked)
        }
    }
}
