package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import okio.Buffer
import org.junit.Assert.*
import org.junit.Test

class PublicImageReaderTest {
    private val certificate = HeldCertificate.Builder().addSubjectAlternativeName("localhost").build()
    private val serverCertificates = HandshakeCertificates.Builder().heldCertificate(certificate).build()
    private val clientCertificates = HandshakeCertificates.Builder().addTrustedCertificate(certificate.certificate).build()

    /** A reader as the app's, except that it trusts the test server's certificate. */
    private fun reader(maxBytes: Long = PublicImageReader.MaxBytes) = PublicImageReader(
        OkHttpClient.Builder().sslSocketFactory(clientCertificates.sslSocketFactory(), clientCertificates.trustManager)
            .followSslRedirects(false).build(), maxBytes)

    private fun MockWebServer.https() = apply { useHttps(serverCertificates.sslSocketFactory(), false) }
    private fun MockWebServer.image(path: String) = url(path).newBuilder().host("localhost").build().toString()

    @Test fun onlyFullHttpsUrlsWithoutUserInfoArePublic() {
        assertNotNull(PublicImageReader.publicImageUrl("https://cdn.example.com/avatar/a.jpg?x=1"))
        assertNotNull(PublicImageReader.publicImageUrl("HTTPS://cdn.example.com/a"))
        assertNull(PublicImageReader.publicImageUrl("http://cdn.example.com/a.jpg"))
        assertNull(PublicImageReader.publicImageUrl("ftp://cdn.example.com/a.jpg"))
        assertNull(PublicImageReader.publicImageUrl("file:///etc/passwd"))
        assertNull(PublicImageReader.publicImageUrl("//cdn.example.com/a.jpg"))
        assertNull(PublicImageReader.publicImageUrl("/api/v1/lark/media/k"))
        assertNull(PublicImageReader.publicImageUrl("https://user:pass@cdn.example.com/a.jpg"))
        assertNull(PublicImageReader.publicImageUrl("https://user@cdn.example.com/a.jpg"))
    }

    @Test fun aBotsAvatarIsReadStraightFromItsHostWithoutTheHubsCredentials() = runBlocking {
        MockWebServer().use { hub ->
            MockWebServer().https().use { cdn ->
                val transport = HttpInboxAssistantTransport("scope", hub.url("/").toString(), "test-token", OkHttpClient(), reader())
                cdn.enqueue(MockResponse().setBody("bot"))
                assertArrayEquals("bot".toByteArray(), transport.avatar(cdn.image("/avatar/bot.jpg")))
                val request = cdn.takeRequest()
                assertEquals("/avatar/bot.jpg", request.path)
                assertNull(request.getHeader("Authorization"))
                assertNull(request.getHeader("Cookie"))
                assertTrue(request.headers.names().none { it.startsWith("X-", ignoreCase = true) })
                assertEquals(0, hub.requestCount)

                // A chat's avatar on the hub still goes through the pairing, at its thumbnail size.
                hub.enqueue(MockResponse().setBody("chat"))
                assertArrayEquals("chat".toByteArray(), transport.avatar("/api/v1/lark/media/k?size=144"))
                val chat = hub.takeRequest()
                assertEquals("/api/v1/lark/media/k?size=144", chat.path)
                assertEquals("Bearer test-token", chat.getHeader("Authorization"))
                assertEquals(1, cdn.requestCount)
            }
        }
    }

    @Test fun aConnectorAvatarIsAnAuthenticatedHubGetAtItsExactPath() = runBlocking {
        MockWebServer().use { hub ->
            val transport = HttpInboxAssistantTransport("scope", hub.url("/").toString(), "test-token", OkHttpClient(), reader())
            val ref = "/api/v1/avatars/telegram/YS51MS41MDE?size=144"
            hub.enqueue(MockResponse().setBody("connector"))
            assertArrayEquals("connector".toByteArray(), transport.avatar(ref))
            val request = hub.takeRequest()
            assertEquals("GET", request.method)
            assertEquals(ref, request.path)
            assertEquals("Bearer test-token", request.getHeader("Authorization"))
        }
    }

    @Test fun aFullUrlOnTheHubStillGoesThroughThePairing() = runBlocking {
        MockWebServer().use { hub ->
            val transport = HttpInboxAssistantTransport("scope", hub.url("/").toString(), "test-token", OkHttpClient(), reader())
            hub.enqueue(MockResponse().setBody("chat"))
            assertArrayEquals("chat".toByteArray(), transport.avatar(hub.url("/api/v1/lark/media/k").toString()))
            assertEquals("Bearer test-token", hub.takeRequest().getHeader("Authorization"))
        }
    }

    @Test fun httpOtherSchemesAndUserInfoAreRefused() = runBlocking {
        MockWebServer().use { hub ->
            MockWebServer().use { plain ->
                val transport = HttpInboxAssistantTransport("scope", hub.url("/").toString(), "test-token", OkHttpClient(), reader())
                for (ref in listOf(plain.url("/a.jpg").toString(), "ftp://cdn.example.com/a.jpg",
                    "https://user:pass@cdn.example.com/a.jpg", "data:image/png;base64,AAAA")) {
                    try { transport.avatar(ref); fail("$ref must be refused") } catch (_: IllegalArgumentException) { }
                }
                try { reader().read(plain.url("/a.jpg").toString()); fail("http must be refused") }
                catch (_: IllegalArgumentException) { }
                assertEquals(0, hub.requestCount)
                assertEquals(0, plain.requestCount)
            }
        }
    }

    @Test fun aPictureLargerThanTheCapIsRefused() = runBlocking {
        MockWebServer().https().use { cdn ->
            val reader = reader(maxBytes = 4)
            cdn.enqueue(MockResponse().setBody("1234"))
            assertArrayEquals("1234".toByteArray(), reader.read(cdn.image("/fits")))
            cdn.enqueue(MockResponse().setBody("12345"))
            try { reader.read(cdn.image("/declared")); fail("oversize must be refused") } catch (_: java.io.IOException) { }
            // Without a length up front (chunked) it is cut off while reading.
            cdn.enqueue(MockResponse().setChunkedBody(Buffer().writeUtf8("123456789"), 2))
            try { reader.read(cdn.image("/chunked")); fail("oversize must be refused") } catch (_: java.io.IOException) { }
        }
    }

    @Test fun aFailedOrRedirectedDownHttpPictureIsAnError() = runBlocking {
        MockWebServer().https().use { cdn ->
            cdn.enqueue(MockResponse().setResponseCode(302).setHeader("Location", "http://cdn.example.com/a.jpg"))
            try { reader().read(cdn.image("/moved")); fail("a redirect down to http must not be followed") }
            catch (_: java.io.IOException) { }
            cdn.enqueue(MockResponse().setResponseCode(404))
            try { reader().read(cdn.image("/missing")); fail("404 must fail") } catch (_: java.io.IOException) { }
            assertEquals(2, cdn.requestCount)
        }
    }
}
