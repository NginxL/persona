package persona.core.hub

import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.ManagerPins
import persona.core.contracts.api.RequestManager
import java.net.InetAddress
import java.security.MessageDigest
import java.util.Base64
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.Request
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.tls.HandshakeCertificates
import okhttp3.tls.HeldCertificate
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/** The manager the assistant page shows names itself on the assistant's requests only (docs/assistant/managers.md step 4). */
class HubManagerHeaderTest {
    @After fun reset() { AssistantManagerChoice.reset(); AssistantManagerChoice.forgetItems() }

    @Test fun assistantRequestsNameTheChosenManager() {
        val certificate = HeldCertificate.Builder().commonName("example.com").build()
        val tls = HandshakeCertificates.Builder().heldCertificate(certificate).build()
        MockWebServer().use { server ->
            server.useHttps(tls.sslSocketFactory(), false)
            server.start(InetAddress.getLoopbackAddress(), 0)
            val pin = Base64.getUrlEncoder().withoutPadding()
                .encodeToString(MessageDigest.getInstance("SHA-256").digest(certificate.certificate.publicKey.encoded))
            val base = server.url("/").newBuilder().host(InetAddress.getLoopbackAddress().hostAddress!!).build()
            HubHttpClient("test-hub", pin, "test").use { hub ->
                fun header(path: String): String? {
                    server.enqueue(MockResponse().setBody("{}"))
                    hub.client.newCall(Request.Builder().url(base.resolve(path)!!).build()).execute().close()
                    return server.takeRequest().getHeader(ManagerHeader)
                }
                fun sent(path: String): Pair<String?, String?> {
                    server.enqueue(MockResponse().setBody("{}"))
                    hub.client.newCall(Request.Builder().url(base.resolve(path)!!).build()).execute().close()
                    return server.takeRequest().let { it.path to it.getHeader(ManagerHeader) }
                }
                // The default manager: requests as before.
                AssistantManagerChoice.choose("test-hub", null, "assistant")
                assertNull(header("/api/v1/assistant"))
                // Another manager: the assistant's requests name it, the rest go by ownership as before.
                AssistantManagerChoice.choose("test-hub", "m-2", "assistant")
                assertEquals("m-2", header("/api/v1/assistant"))
                // One conversation's routes are not the page's: its manager when a page listed it, else none (the hub
                // routes it by ownership).
                assertNull(header("/api/v1/assistant/conversations/c1/feed?after=3"))
                AssistantManagerChoice.noteItem(listOf("assistant", "conversations", "c1"), "m-2")
                AssistantManagerChoice.noteItem(listOf("assistant", "conversations", "h1"), "assistant")
                assertEquals("m-2", header("/api/v1/assistant/conversations/c1/feed?after=3"))
                assertNull(header("/api/v1/assistant/conversations/h1/stop"))
                assertEquals("m-2", header("/api/v1/assistant/conversations"))
                assertEquals("m-2", header("/hub/api/v1/assistant/memory"))
                assertNull(header("/api/v1/sessions/lab/s1/send"))
                assertNull(header("/api/v1/assistants"))
                // Picking the default again sends none; another hub's choice is not this one's.
                AssistantManagerChoice.choose("test-hub", "assistant", "assistant")
                assertNull(header("/api/v1/assistant"))
                AssistantManagerChoice.choose("other-hub", "m-2", "assistant")
                assertNull(header("/api/v1/assistant"))
                // A request pinned to a manager (`?manager=`) keeps it whatever the page shows; the default's (empty)
                // goes without the query or a header.
                AssistantManagerChoice.choose("test-hub", "m-2", "assistant")
                assertEquals("/api/v1/assistant?manager=m-3" to null, sent("/api/v1/assistant?manager=m-3"))
                assertEquals("/api/v1/assistant/conversations" to null, sent("/api/v1/assistant/conversations?manager="))
                // Another manager's things in a merged list: their own requests name it, whatever the page shows.
                AssistantManagerChoice.choose("test-hub", null, "assistant")
                AssistantManagerChoice.noteItem(listOf("inbox", "mail:a1:t:7"), "m-3")
                AssistantManagerChoice.noteItem(listOf("inbox", "lark:f1:2"), null)
                AssistantManagerChoice.noteItem(listOf("extensions", "notes"), "m-3")
                assertEquals("m-3", header("/api/v1/inbox/mail:a1:t:7"))
                assertEquals("m-3", header("/api/v1/inbox/mail:a1:t:7/done"))
                assertEquals("m-3", header("/hub/api/v1/extensions/notes/-/config"))
                assertNull(header("/api/v1/inbox/lark:f1:2/reply"))
                assertNull(header("/api/v1/inbox"))
                assertNull(header("/api/v1/inbox/done"))
                assertNull(header("/api/v1/extensions/other/-/"))
                // The default's own (or stamped with the default's id) need nothing.
                AssistantManagerChoice.noteItem(listOf("inbox", "mail:a2:t:1"), "assistant")
                assertNull(header("/api/v1/inbox/mail:a2:t:1"))
                // Stamped again without a manager: the default's now.
                AssistantManagerChoice.noteItem(listOf("extensions", "notes"), null)
                assertNull(header("/api/v1/extensions/notes/-/config"))
            }
        }
    }

    /** The current assistant's own routes: the assistant page's, the knowledge base, a new to-do. */
    @Test fun theCurrentAssistantsPaths() {
        fun current(path: String, method: String = "GET") = isCurrentAssistantPath("https://example.com/hub/api/v1/$path".toHttpUrl(), method)
        assertTrue(current("assistant/schedule"))
        assertTrue(current("kb/people"))
        assertTrue(current("kb/people/p1", "PUT"))
        assertTrue(current("kb-search?q=x"))
        assertTrue(current("todos", "POST"))
        // the lists every manager answers, and one thing's own routes (named by the stamp its list gave it)
        assertFalse(current("todos"))
        assertFalse(current("todos/t1", "PATCH"))
        assertFalse(current("inbox"))
        assertFalse(current("machines"))
        assertFalse(current("assistant?space=health"))
        AssistantManagerChoice.choose("hub", "m-3", "assistant")
        val base = "https://example.com/api/v1/"
        assertEquals("m-3", managerHeaderFor("${base}kb/projects".toHttpUrl(), "hub"))
        assertEquals("m-3", managerHeaderFor("${base}todos".toHttpUrl(), "hub", "POST"))
        // the health space is the default manager's (docs/assistant/health.md): its list and new chats name none
        assertNull(managerHeaderFor("${base}assistant?space=health".toHttpUrl(), "hub"))
        assertNull(managerHeaderFor("${base}assistant/conversations?space=health".toHttpUrl(), "hub", "POST"))
        assertNull(managerHeaderFor("${base}todos".toHttpUrl(), "hub"))
        assertNull(managerHeaderFor("${base}kb/projects".toHttpUrl(), "another-hub"))
        // a to-do the list showed as another manager's names that one, whichever is current
        AssistantManagerChoice.noteItem(listOf("todos", "t1"), "m-2")
        assertEquals("m-2", managerHeaderFor("${base}todos/t1".toHttpUrl(), "hub", "PATCH"))
        AssistantManagerChoice.choose("hub", null, "assistant")
        assertNull(managerHeaderFor("${base}kb/projects".toHttpUrl(), "hub"))
        assertNull(managerHeaderFor("${base}todos".toHttpUrl(), "hub", "POST"))
    }

    /** The to-dos list is every manager's: each to-do keeps its manager, and an answer about one keeps the one it had. */
    @Test fun toDosKeepTheirManager() = kotlinx.coroutines.runBlocking {
        fun answer(body: String) = okhttp3.Response.Builder().request(Request.Builder().url("https://example.com/").build())
            .protocol(okhttp3.Protocol.HTTP_1_1).code(200).message("OK").body(body.toResponseBody()).build()
        AssistantManagerChoice.choose("hub", "m-3", "assistant")
        val api = HttpTasksApi({ path, method, _ ->
            when {
                method == "GET" -> answer("""{"todos":[{"id":"t1","title":"a","manager":"assistant"},{"id":"t2","title":"b","manager":"m-3"},{"id":"t3","title":"c"}]}""")
                method == "POST" && path.endsWith("/todos") -> answer("""{"todo":{"id":"t9","title":"new"}}""")
                else -> answer("""{"todo":{"id":"t2","title":"b2"}}""")
            }
        })
        assertEquals(listOf("assistant", "m-3", null), api.todos().map { it.manager })
        assertNull(AssistantManagerChoice.itemHeader(listOf("todos", "t1", "decisions")))
        assertEquals("m-3", AssistantManagerChoice.itemHeader(listOf("todos", "t2", "decisions")))
        // an answer about one comes back unstamped: it keeps its list's manager
        assertEquals("m-3", api.patchTodo("t2", title = "b2").manager)
        // a new one is the current assistant's
        assertEquals("m-3", api.createTodo("new").manager)
        assertEquals("m-3", AssistantManagerChoice.itemHeader(listOf("todos", "t9")))
        // one made for an agent of another assistant is that one's (the default's: none), whichever is current
        assertEquals("m-2", withContext(RequestManager("m-2")) { api.createTodo("new") }.manager)
        assertEquals("m-2", AssistantManagerChoice.itemHeader(listOf("todos", "t9")))
        assertNull(withContext(RequestManager("")) { api.createTodo("new") }.manager)
        assertNull(AssistantManagerChoice.itemHeader(listOf("todos", "t9")))
    }

    @Test fun assistantPaths() {
        assertTrue(isAssistantPath("https://example.com/api/v1/assistant".toHttpUrl()))
        assertTrue(isAssistantPath("https://example.com/persona/api/v1/assistant/tasks/1/run".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1/inbox".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1/assistant?space=health".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1/assistant/conversations/c1/feed".toHttpUrl()))
        assertTrue(isAssistantPath("https://example.com/api/v1/assistant/conversations".toHttpUrl()))
        // The tracks, their kinds and extension trust are the default manager's (#13500).
        assertFalse(isAssistantPath("https://example.com/api/v1/assistant/tracks".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1/assistant/tracks/qa-plain/space-md".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1/assistant/track-kinds".toHttpUrl()))
        assertFalse(isAssistantPath("https://example.com/api/v1/assistant/extension-trust/notes".toHttpUrl()))
    }

    /**
     * The same extension under two managers (each one's built-ins): the list notes neither, and an extension's page names
     * its own manager on its own requests, so the default's voice keeps going to the default elsewhere.
     */
    @Test fun anExtensionsPageNamesItsOwnManager() = kotlinx.coroutines.runBlocking {
        val api = HttpExtensionManagement({ _, _, _ ->
            okhttp3.Response.Builder().request(Request.Builder().url("https://example.com/").build()).protocol(okhttp3.Protocol.HTTP_1_1)
                .code(200).message("OK").body(run {
                    """{"extensions":[{"id":"voice","manager":"assistant"},{"id":"voice","manager":"m-3"},{"id":"notes","manager":"m-3"}]}""".toResponseBody()
                }).build()
        })
        AssistantManagerChoice.choose("hub", null, "assistant")
        assertEquals(listOf("assistant", "m-3", "m-3"), api.listing().extensions.map { it.manager })
        assertNull(AssistantManagerChoice.itemHeader(listOf("extensions", "voice", "-", "config")))
        suspend fun named(path: String) = "https://example.com/hub/api/v1/$path".toHttpUrl().withRequestManager().queryParameter(ManagerQuery)
        val page = ManagerPins.of(listOf(listOf("extensions", "voice"), listOf("plugins", "voice")), "m-3")!!
        withContext(page) {
            assertEquals("m-3", named("extensions/voice/-/config"))
            assertEquals("m-3", named("plugins/voice"))
            assertNull(named("extensions/notes"))
            // an inbox item's request names its own, and a URL that names one keeps it
            assertEquals("m-1", withContext(RequestManager("m-1")) { named("extensions/voice") })
            assertEquals("m-1", named("extensions/voice?manager=m-1"))
            // a page of the default's inside: the default, which the client takes off again
            assertEquals("", withContext(ManagerPins.of(listOf(listOf("extensions", "voice")), "assistant")!!) { named("extensions/voice") })
        }
        assertNull(named("extensions/voice/-/config"))
    }

    /** A page's pins are its own requests': one made elsewhere while the page is open names nothing, on the URL or the header. */
    @Test fun aPagesPinsDoNotReachOtherRequests() = kotlinx.coroutines.runBlocking {
        AssistantManagerChoice.choose("hub", null, "assistant")
        val url = "https://example.com/api/v1/extensions/voice/-/config".toHttpUrl()
        val opened = CompletableDeferred<Unit>()
        val closed = CompletableDeferred<Unit>()
        val page = launch(ManagerPins.of(listOf(listOf("extensions", "voice")), "m-3")!!) {
            assertEquals("m-3", url.withRequestManager().queryParameter(ManagerQuery))
            opened.complete(Unit)
            closed.await()
        }
        opened.await()
        assertNull(url.withRequestManager().queryParameter(ManagerQuery))
        assertNull(managerHeaderFor(url, "hub"))
        closed.complete(Unit)
        page.join()
    }

    /** What a hub's lists note before its roster is read is under that hub, and found once the roster lands. */
    @Test fun notesBeforeTheRosterAreTheHubs() {
        AssistantManagerChoice.atHub("hub-a")
        AssistantManagerChoice.noteItem(listOf("inbox", "x"), "m-1")
        val pins = ManagerPins.of(listOf(listOf("extensions", "voice")), "m-3")!!
        AssistantManagerChoice.choose("hub-a", null, "assistant")
        assertEquals("m-1", AssistantManagerChoice.itemHeader(listOf("inbox", "x", "done"), "hub-a"))
        assertEquals("m-3", pins.manager(listOf("extensions", "voice"), "hub-a"))
        assertNull(pins.manager(listOf("extensions", "voice"), "hub-b"))
    }

    /** What one hub's lists showed is that hub's: another paired hub's requests name nothing by it. */
    @Test fun stampsAreTheirHubs() {
        AssistantManagerChoice.choose("hub-a", null, "assistant")
        AssistantManagerChoice.noteItem(listOf("inbox", "x"), "m-1")
        assertEquals("m-1", AssistantManagerChoice.itemHeader(listOf("inbox", "x", "done"), "hub-a"))
        assertNull(AssistantManagerChoice.itemHeader(listOf("inbox", "x", "done"), "hub-b"))
        // the roster moved the default after the list was read: compared when the request goes out
        AssistantManagerChoice.choose("hub-a", null, "m-1")
        assertNull(AssistantManagerChoice.itemHeader(listOf("inbox", "x"), "hub-a"))
    }
}
