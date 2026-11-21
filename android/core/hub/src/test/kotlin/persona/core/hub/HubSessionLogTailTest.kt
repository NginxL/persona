package persona.core.hub

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeout
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.SocketPolicy
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.ManagerPins
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.SessionLogTail
import java.net.InetAddress
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

class HubSessionLogTailTest {
    private fun response(path: String, code: Int, header: String?, bytes: ByteArray) = Response.Builder()
        .request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
        .code(code).message("fixture").apply { header?.let { header("Content-Range", it) } }
        .body(bytes.toResponseBody()).build()

    @Test fun logRequestsUseTheOwningMachinePathManagerAndBoundedRange(): Unit = runBlocking {
        val bytes = "finished\n".encodeToByteArray()
        val hub = HubSessionFiles(request = { error("a log must use the unbuffered stream") }, range = { path, headers ->
            val url = ("https://example.com" + path).toHttpUrl()
            assertEquals("/api/v1/machines/box%2Ftwo/file/download", url.encodedPath)
            assertEquals("/workspace/a & b", url.queryParameter("cwd"))
            assertEquals("/task logs/job #1.txt", url.queryParameter("path"))
            assertEquals("own-manager", url.queryParameter("manager"))
            assertEquals("bytes=-65536", headers["Range"])
            assertEquals("no-store", headers["Cache-Control"])
            response(path, 206, "bytes 0-8/9", bytes).use(::readSessionLogTail)
        })
        assertEquals(SessionLogTail("finished\n", false), hub.readLogTail("box/two", "/workspace/a & b", "/task logs/job #1.txt", "own-manager"))
    }

    @Test fun defaultAndNamedLogOwnersOverrideTheAmbientManagerOnTheWire(): Unit = runBlocking {
        val observed = java.util.concurrent.LinkedBlockingQueue<Pair<String?, String?>>()
        LoopbackHub(api = { request ->
            assertEquals("Bearer fixture-device", request.getHeader("Authorization"))
            assertEquals("bytes=-65536", request.getHeader("Range"))
            observed.add(request.requestUrl?.queryParameter(ManagerQuery) to request.getHeader(ManagerHeader))
            MockResponse().setResponseCode(206).setHeader("Content-Range", "bytes 0-1/2").setBody("ok")
        }).use { hub ->
            val config = HubConnectionConfig(hub.hubId, null, "fixture-device", listOf(hub.endpoint("lan")), "fixture")
            OkHttpHubConnection(config, HubNetwork("loopback")).use { connection ->
                withTimeout(5_000) { connection.currentHello.first { it != null } }
                val files = HubSessionFiles(connection)
                val page = ManagerPins.of(listOf(listOf("machines")), "ambient-page")!!
                withContext(page) {
                    for (owner in listOf(null, "", " ", "owner")) {
                        assertEquals(SessionLogTail("ok", false), files.readLogTail("box", "/work", "/log.txt", owner))
                        assertEquals(owner?.takeUnless(String::isBlank) to null, observed.remove())
                    }
                    withContext(RequestManager("ambient-item")) {
                        assertEquals(SessionLogTail("ok", false), files.readLogTail("box", "/work", "/log.txt", null))
                        assertEquals(null to null, observed.remove())
                        assertEquals(SessionLogTail("ok", false), files.readLogTail("box", "/work", "/log.txt", "owner"))
                        assertEquals("owner" to null, observed.remove())
                    }
                }
                assertTrue(observed.isEmpty())
            }
        }
    }

    @Test fun aSuffixDropsTheLeadingPartialUtf8Line() {
        val bytes = ByteArray(LOG_TAIL_BYTES) { 'a'.code.toByte() }
        val opening = byteArrayOf(0x80.toByte()) + "cut\nStarting checks\n".encodeToByteArray()
        opening.copyInto(bytes)
        val tail = response("/test", 206, "bytes 21-65556/65557", bytes).use(::readSessionLogTail)
        assertTrue(tail.suffix)
        assertTrue(tail.text.startsWith("Starting checks\n"))
        assertFalse(tail.text.contains('\uFFFD'))
    }

    @Test fun validatesRangeAndRejectsAnIgnoringServerWithoutTakingItsBody() {
        val cases = listOf(
            Triple(200, null, byteArrayOf(1)), // a nonempty whole file
            Triple(206, null, "x".encodeToByteArray()),
            Triple(206, "bytes 0--1/0", byteArrayOf(1)),
            Triple(206, "bytes 1-2/3", "ab".encodeToByteArray()), // server returns a different suffix
            Triple(206, "bytes 0-3/4", "abc".encodeToByteArray()), // short body
            Triple(206, "bytes 0-2/3", "abcd".encodeToByteArray()), // long body
            Triple(206, "bytes 0-0/1", byteArrayOf(0)), // binary
            Triple(206, "bytes 0-65536/65537", ByteArray(LOG_TAIL_BYTES + 1)),
            Triple(206, "bytes 0-9223372036854775808/9223372036854775809", byteArrayOf(1)),
        )
        for ((code, range, bytes) in cases) {
            val failure = assertThrows(HubFailure::class.java) { response("/test", code, range, bytes).use(::readSessionLogTail) }
            assertEquals(HubError.Protocol, failure.kind)
        }
        assertEquals(SessionLogTail("", false), response("/test", 200, null, byteArrayOf()).use(::readSessionLogTail))
        assertEquals(SessionLogTail("", false), response("/test", 206, "bytes 0--1/0", byteArrayOf()).use(::readSessionLogTail))
    }

    @Test fun cancellationAfterHeadersInterruptsABlockedTailBody(): Unit = runBlocking {
        MockWebServer().use { server ->
            server.start(InetAddress.getLoopbackAddress(), 0)
            server.enqueue(MockResponse().setResponseCode(206).setHeader("Content-Range", "bytes 0-8/9")
                .setBody("finished\n").setBodyDelay(3, TimeUnit.SECONDS))
            val call = OkHttpClient().newCall(Request.Builder().url(server.url("/tail")).build())
            val entered = CompletableDeferred<Unit>()
            val exited = CompletableDeferred<Unit>()
            val read = launch {
                call.awaitConsumedResponse {
                    entered.complete(Unit)
                    try { readSessionLogTail(it) }
                    finally { exited.complete(Unit) }
                }
            }
            withTimeout(2_000) { entered.await() }
            assertFalse("the headers arrived but the body is withheld", exited.isCompleted)
            read.cancelAndJoin()
            assertTrue("cancelling the detail read cancels the underlying call", call.isCanceled())
            withTimeout(2_000) { exited.await() }
        }
    }

    @Test fun aCodedDeniedTailDoesNotRevokeTheValidPairing(): Unit = runBlocking {
        val reads = AtomicInteger()
        LoopbackHub(api = { request ->
            assertEquals("Bearer fixture-device", request.getHeader("Authorization"))
            assertEquals("owner", request.requestUrl?.queryParameter("manager"))
            assertEquals("bytes=-65536", request.getHeader("Range"))
            if (reads.incrementAndGet() == 1)
                MockResponse().setResponseCode(403).setBody("""{"error":"Refused file access","code":"access_denied"}""")
            else MockResponse().setResponseCode(206).setHeader("Content-Range", "bytes 0--1/0").setBody("")
        }).use { hub ->
            val config = HubConnectionConfig(hub.hubId, null, "fixture-device", listOf(hub.endpoint("lan")), "fixture")
            OkHttpHubConnection(config, HubNetwork("loopback")).use { connection ->
                val initial = withTimeout(5_000) { connection.currentHello.first { it != null } }
                val files = HubSessionFiles(connection)
                val failure = runCatching { files.readLogTail("box", "/work", "/log.txt", "owner") }.exceptionOrNull() as HubFailure
                assertEquals("access_denied", failure.code)
                assertEquals(HubError.Server, failure.kind)
                assertEquals(ConnectionState.Online, connection.state.value)
                assertEquals(initial, connection.currentHello.value)
                assertEquals(SessionLogTail("", false), files.readLogTail("box", "/work", "/log.txt", "owner"))
            }
        }
    }
    @Test fun aTruncatedDenialKeepsThePairingAndACompleteOldRefusalStillRevokesIt(): Unit = runBlocking {
        val reads = AtomicInteger()
        LoopbackHub(api = { request ->
            assertEquals("Bearer fixture-device", request.getHeader("Authorization"))
            when (reads.incrementAndGet()) {
                1 -> MockResponse().setResponseCode(403)
                    .setBody("""{"error":"Refused file access","code":"access_denied"}""")
                    .setSocketPolicy(SocketPolicy.DISCONNECT_DURING_RESPONSE_BODY)
                2 -> MockResponse().setResponseCode(206).setHeader("Content-Range", "bytes 0-1/2").setBody("ok")
                else -> MockResponse().setResponseCode(403).setBody("Expired")
            }
        }).use { hub ->
            val config = HubConnectionConfig(hub.hubId, null, "fixture-device", listOf(hub.endpoint("lan")), "fixture")
            OkHttpHubConnection(config, HubNetwork("loopback")).use { connection ->
                val initial = withTimeout(5_000) { connection.currentHello.first { it != null } }
                val files = HubSessionFiles(connection)
                val interrupted = runCatching { files.readLogTail("box", "/work", "/log.txt", "owner") }.exceptionOrNull() as HubFailure
                assertEquals(HubError.Transport, interrupted.kind)
                assertEquals(initial, connection.currentHello.value)
                assertEquals(SessionLogTail("ok", false), files.readLogTail("box", "/work", "/log.txt", "owner"))

                val expired = runCatching { files.readLogTail("box", "/work", "/log.txt", "owner") }.exceptionOrNull() as HubFailure
                assertEquals(HubError.PairingExpired, expired.kind)
                assertEquals(ConnectionState.Revoked, connection.state.value)
            }
        }
    }

    @Test fun anOversizedOrExactlyCapTruncatedDenialCannotRevokePairing(): Unit = runBlocking {
        val reads = AtomicInteger()
        val largeDenial = """{"error":"Refused","code":"access_denied","detail":"${"x".repeat(64 * 1024)}"}"""
        LoopbackHub(api = {
            when (reads.incrementAndGet()) {
                1 -> MockResponse().setResponseCode(403).setBody(largeDenial)
                2 -> MockResponse().setResponseCode(403).setBody(largeDenial.take(64 * 1024))
                    .setHeader("Content-Length", 64 * 1024 + 1)
                    .setSocketPolicy(SocketPolicy.DISCONNECT_AT_END)
                else -> MockResponse().setResponseCode(206).setHeader("Content-Range", "bytes 0-1/2").setBody("ok")
            }
        }).use { hub ->
            val config = HubConnectionConfig(hub.hubId, null, "fixture-device", listOf(hub.endpoint("lan")), "fixture")
            OkHttpHubConnection(config, HubNetwork("loopback")).use { connection ->
                val initial = withTimeout(5_000) { connection.currentHello.first { it != null } }
                val files = HubSessionFiles(connection)
                for (kind in listOf(HubError.Protocol, HubError.Transport)) {
                    val interrupted = runCatching { files.readLogTail("box", "/work", "/log.txt", "owner") }.exceptionOrNull() as HubFailure
                    assertEquals(kind, interrupted.kind)
                    assertEquals(initial, connection.currentHello.value)
                }
                assertEquals(SessionLogTail("ok", false), files.readLogTail("box", "/work", "/log.txt", "owner"))
            }
        }
    }
}
