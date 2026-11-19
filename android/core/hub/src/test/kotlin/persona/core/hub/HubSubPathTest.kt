package persona.core.hub

import persona.core.contracts.Endpoint
import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import java.io.ByteArrayOutputStream
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.Assert.*
import org.junit.Test

/** A hub behind a reverse proxy at a sub-path, without its own TLS (no `fp`): docs/operations/network.md "Reverse-proxy subpaths". */
class HubSubPathTest {
    private val session = SessionKey("machine", "session/encoded")
    private val pairing = HubScope("test-hub", "generation")

    @Test fun apiUrlsKeepTheBasePathPrefix() {
        for (root in listOf("https://hub.example.com", "https://hub.example.com/")) {
            assertEquals("https://hub.example.com/api/v1/health", (root.toHttpUrl().hubUrl("/api/v1/health").toString()).zhAny())
        }
        for (prefixed in listOf("https://hub.example.com/persona", "https://hub.example.com/persona/")) {
            val base = prefixed.toHttpUrl()
            assertEquals("https://hub.example.com/persona/api/v1/health", (base.hubUrl("/api/v1/health").toString()).zhAny())
            assertEquals("https://hub.example.com/persona/api/v1/ws?event_seq=3", (base.hubUrl("/api/v1/ws?event_seq=3").toString()).zhAny())
            assertEquals("/api/v1/lark/media/a", (base.hubPath("https://hub.example.com/persona/api/v1/lark/media/a".toHttpUrl())).zhAny())
            // Outside the prefix, another port or host is not this hub.
            assertNull(base.hubPath("https://hub.example.com/api/v1/lark/media/a".toHttpUrl()))
            assertNull(base.hubPath("https://hub.example.com:8443/persona/api/v1/x".toHttpUrl()))
            assertNull(base.hubPath("https://other.example.com/persona/api/v1/x".toHttpUrl()))
            // The hub's own references are absolute paths: they go under the prefix.
            assertEquals("https://hub.example.com/persona/api/v1/app/android/apk",
                (base.hubResolve("/api/v1/app/android/apk").toString()).zhAny())
            assertEquals("https://hub.example.com/persona/api/v1/app/android/apk",
                (base.hubResolve("https://hub.example.com/persona/api/v1/app/android/apk").toString()).zhAny())
            assertNull(base.hubResolve("https://other.example.com/persona/api/v1/app/android/apk"))
            assertNull(base.hubResolve("//other.example.com/api/v1/app/android/apk"))
        }
        assertThrows(IllegalArgumentException::class.java) { "https://hub.example.com/persona".toHttpUrl().hubUrl("api/v1/x") }
        assertEquals("https://hub.example.com/persona", (Endpoint("https://hub.example.com/persona").baseUrl().toString()).zhAny())
    }

    @Test fun endpointKeysKeepAPrefixAndRootKeysStayTheSame() {
        assertEquals("https://hub.example.com:443", (endpointKey("https://hub.example.com/")).zhAny())
        assertEquals("https://hub.example.com:443", (endpointKey("https://hub.example.com:443")).zhAny())
        assertEquals("https://hub.example.com:443/persona", (endpointKey("https://hub.example.com/persona")).zhAny())
        assertEquals(endpointKey("https://hub.example.com/persona/"), endpointKey("https://hub.example.com:443/persona"))
    }

    @Test fun aSubPathLinkWithoutFingerprintIsAccepted() {
        val request = PairingRequest("https://hub.example.com/persona", "pairing-code", hub = "test-hub",
            alt = listOf("http://192.0.2.10:18660"))
        assertEquals(listOf("https://hub.example.com/persona", "http://192.0.2.10:18660/"), (HubPairingClient.addresses(request)).zhAny())
    }

    @Test fun withoutFingerprintHttpsUsesSystemTrustAndTheHostName() = runBlocking {
        val certificate = HeldCertificate.Builder().addSubjectAlternativeName(loopback).build()
        val wrongName = HeldCertificate.Builder().addSubjectAlternativeName("hub.example.com").build()
        tlsServer(certificate, Channel(Channel.UNLIMITED)).use { server ->
            val endpoint = endpoint(server)
            HubHttpClient("test-hub", null, "test", systemTrust = { trusting(certificate) }).use { client ->
                assertTrue(client.systemTrusted)
                assertEquals("test-hub", (client.health(endpoint).hubId).zhAny())
            }
            // A certificate the system does not trust is refused.
            HubHttpClient("test-hub", null, "test", systemTrust = { trusting(wrongName) }).use { client ->
                assertEquals(HubError.CertificateMismatch, failure { client.health(endpoint) }.kind)
            }
        }
        tlsServer(wrongName, Channel(Channel.UNLIMITED)).use { server ->
            // A trusted certificate for another name is refused by the host name check.
            HubHttpClient("test-hub", null, "test", systemTrust = { trusting(wrongName) }).use { client ->
                assertEquals(HubError.CertificateMismatch, failure { client.health(endpoint(server)) }.kind)
            }
        }
    }

    @Test fun aFingerprintIsStillPinnedAndNeverFallsBackToSystemTrust() = runBlocking {
        val certificate = HeldCertificate.Builder().addSubjectAlternativeName(loopback).build()
        val other = HeldCertificate.Builder().addSubjectAlternativeName(loopback).build()
        tlsServer(certificate, Channel(Channel.UNLIMITED)).use { server ->
            // The system would trust the certificate; the pairing pinned another key.
            HubHttpClient("test-hub", fingerprint(other), "test", systemTrust = { trusting(certificate) }).use { client ->
                assertFalse(client.systemTrusted)
                assertEquals(HubError.CertificateMismatch, failure { client.health(endpoint(server)) }.kind)
            }
            HubHttpClient("test-hub", fingerprint(certificate), "test", systemTrust = { trusting(other) }).use { client ->
                assertEquals("test-hub", (client.health(endpoint(server)).hubId).zhAny())
            }
        }
    }

    @Test fun everyTransportGoesThroughThePrefixOverSystemTrust() = runBlocking {
        val certificate = HeldCertificate.Builder().addSubjectAlternativeName(loopback).build()
        val requests = Channel<RecordedRequest>(Channel.UNLIMITED)
        tlsServer(certificate, requests).use { server ->
            val jobs = CoroutineScope(SupervisorJob() + Dispatchers.Default)
            lateinit var sender: HttpMessageTransport
            val http = HubHttpClient("test-hub", null, "test", systemTrust = { trusting(certificate) })
            OkHttpHubConnection(HubConnectionConfig("test-hub", null, "test-token", listOf(endpoint(server)), "test"),
                HubNetwork("offline", available = false), http = http, onReady = { sender.onReady() }).use { connection ->
                try {
                    sender = HttpMessageTransport(pairing, connection, jobs)
                    val chat = HubChatTransport(connection)
                    val inbox = HttpInboxAssistantTransport("scope", connection)
                    connection.updateNetwork(HubNetwork("online"))
                    withTimeout(5000) { sender.readyToSend.first { it } }
                    HubPairingClient(connection).use { settings ->
                        chat.items(session, ItemsQuery(afterSeq = 2))
                        assertArrayEquals("media".toByteArray(), inbox.media("/api/v1/lark/media/image?size=120"))
                        settings.json("/api/v1/devices/test/sessions", "token")
                        val update = settings.checkUpdate("token")!!
                        val apk = ByteArrayOutputStream()
                        settings.download("token", update, apk)
                        assertEquals("apk", (apk.toString()).zhAny())
                        assertEquals(MessageTransportResult.Accepted, sender.send(MessageRequest(pairing, session, "hello", "id", null)))
                    }
                    val received = withTimeout(5000) { List(6) { requests.receive() } }.map { it.path!! }
                    assertEquals("/persona/api/v1/sessions/machine/session%2Fencoded/items?lazy_images=1&after_seq=2", (received[0]).zhAny())
                    assertEquals("/persona/api/v1/lark/media/image?size=120", (received[1]).zhAny())
                    assertEquals("/persona/api/v1/devices/test/sessions", (received[2]).zhAny())
                    assertEquals("/persona/api/v1/app/android/latest", (received[3]).zhAny())
                    assertEquals("/persona/api/v1/app/android/apk", (received[4]).zhAny())
                    assertEquals("/persona/api/v1/sessions/machine/session%2Fencoded/messages", (received[5]).zhAny())
                    assertEquals(ConnectionState.Online, connection.state.value)
                } finally { jobs.cancel() }
            }
        }
    }

    @Test fun agentsDisplaySettingsAndFilteredPagesUseTheAuthenticatedPairing() = runBlocking {
        val requests = Channel<RecordedRequest>(Channel.UNLIMITED)
        server(requests).use { server ->
            val config = HubConnectionConfig("test-hub", null, "display-fixture", listOf(endpoint(server)), "test")
            OkHttpHubConnection(config, HubNetwork("online")).use { connection ->
                withTimeout(5000) { connection.currentHello.first { it != null } }
                val api = HttpHubPreferencesTransport(connection)
                assertEquals(AgentsDisplaySettings(true, false), api.read())
                assertEquals(AgentsDisplaySettings(false, true), api.migrateHidden())
                val chat = HubChatTransport(connection)
                val filter = SessionListFilter(hiddenBotmuxHubs = setOf("g11111111")).forHub("g11111111")
                assertEquals(3, chat.sessionCounts(filter).paneSource["terminal"])
                chat.sessionPage(filter)
                chat.sessionPage(filter.forHub("g22222222"))
                val seen = withTimeout(5000) {
                    buildList {
                        while (size < 5) {
                            val request = requests.receive()
                            if (request.requestUrl!!.encodedPath.endsWith("/agents/display-settings") ||
                                request.requestUrl!!.encodedPath.endsWith("/sessions")) add(request)
                        }
                    }
                }
                assertTrue(seen.all { it.getHeader("Authorization") == "Bearer display-fixture" })
                assertEquals(listOf("GET", "PUT"), seen.take(2).map { it.method })
                assertEquals("""{"show_botmux":false,"only_if_unset":true}""", seen[1].body.readUtf8())
                assertEquals(listOf("feishu", "feishu", null), seen.drop(2).map { it.requestUrl!!.queryParameter("exclude_pane_source") })
            }
        }
        server(requests, displaySupported = false).use { server ->
            val config = HubConnectionConfig("test-hub", null, "display-fixture", listOf(endpoint(server)), "test")
            OkHttpHubConnection(config, HubNetwork("online")).use { connection ->
                withTimeout(5000) { connection.currentHello.first { it != null } }
                assertEquals(404, failure { HttpHubPreferencesTransport(connection).read() }.status)
            }
        }
    }

    @Test fun pairingWorksAtASubPath() = runBlocking {
        val requests = Channel<RecordedRequest>(Channel.UNLIMITED)
        server(requests).use { server ->
            val address = server.url("/persona").newBuilder().host(loopback).build().toString()
            val request = PairingRequest(address, "pairing-code", hub = "test-hub")
            HubPairingClient("test-hub", null, HubPairingClient.addresses(request), "test").use { client ->
                val paired = client.pair("pairing-code", "phone") {}
                assertEquals("test-hub", (paired.hubId).zhAny())
                assertEquals("device-token", (paired.token).zhAny())
                assertTrue(paired.address.endsWith("/persona"))
            }
            val paths = generateSequence { requests.tryReceive().getOrNull() }.map { it.path!! }.toList()
            assertEquals(listOf("/persona/api/v1/auth/tokens", "/persona/api/v1/machines", "/persona/api/v1/endpoints"), (paths).zhAny())
        }
    }

    private val loopback = InetAddress.getLoopbackAddress().hostAddress!!

    private fun trusting(certificate: HeldCertificate) =
        HandshakeCertificates.Builder().addTrustedCertificate(certificate.certificate).build().trustManager

    private fun tlsServer(certificate: HeldCertificate, requests: Channel<RecordedRequest>) = server(requests) {
        useHttps(HandshakeCertificates.Builder().heldCertificate(certificate).build().sslSocketFactory(), false)
    }

    /** A hub reachable only under `/persona`, as a proxy that strips the prefix would forward it. */
    private fun server(requests: Channel<RecordedRequest>, displaySupported: Boolean = true, configure: MockWebServer.() -> Unit = {}) = MockWebServer().apply {
        configure()
        dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val path = request.path!!.takeIf { it.startsWith("/persona/") }?.removePrefix("/persona")
                    ?: return MockResponse().setResponseCode(404)
                if (path == "/api/v1/health") return MockResponse().setBody("""{"ok":true,"hub_id":"test-hub"}""")
                if (path.substringBefore('?') == "/api/v1/ws") return MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) {
                        webSocket.send("""{"type":"hello","hub_id":"test-hub","features":["client_ops"]}""")
                    }
                })
                requests.trySend(request)
                if (path == "/api/v1/agents/display-settings" && !displaySupported) return MockResponse().setResponseCode(404)
                return MockResponse().setBody(when {
                    path == "/api/v1/agents/display-settings" -> if (request.method == "PUT")
                        """{"show_botmux":false,"configured":true}""" else """{"show_botmux":true,"configured":false}"""
                    path.substringBefore('?') == "/api/v1/sessions" -> if (request.requestUrl!!.queryParameter("counts") == "1")
                        """{"total":3,"revision":"r1","machine":{},"project":{},"group":{},"state":{},"pane_source":{"terminal":3}}"""
                        else """{"sessions":[],"has_more":false,"total":3,"revision":"r1"}"""
                    path == "/api/v1/auth/tokens" -> """{"token":"device-token","id":"device"}"""
                    path == "/api/v1/machines" -> """{"machines":[]}"""
                    path == "/api/v1/endpoints" -> """{"hub_id":"test-hub","endpoints":[]}"""
                    path == "/api/v1/app/android/apk" -> "apk"
                    // The hub does not know the proxy's prefix: its references are absolute paths.
                    path == "/api/v1/app/android/latest" ->
                        """{"version_code":1,"version_name":"test","download_url":"/api/v1/app/android/apk","sha256":"${sha256("apk")}"}"""
                    path.substringBefore('?').endsWith("/image") -> "media"
                    else -> "{}"
                })
            }
        }
        start(InetAddress.getLoopbackAddress(), 0)
    }

    private fun endpoint(server: MockWebServer) = Endpoint(server.url("/persona").newBuilder().host(loopback).build().toString(), "name")

    private fun fingerprint(certificate: HeldCertificate) = Base64.getUrlEncoder().withoutPadding().encodeToString(
        MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))

    private fun sha256(text: String) = MessageDigest.getInstance("SHA-256").digest(text.toByteArray()).joinToString("") { "%02x".format(it) }

    private suspend fun failure(block: suspend () -> Unit): HubFailure {
        try { block() } catch (error: HubFailure) { return error }
        throw AssertionError("Expected HubFailure")
    }
}
