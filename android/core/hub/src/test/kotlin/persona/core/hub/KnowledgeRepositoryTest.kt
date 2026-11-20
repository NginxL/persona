package persona.core.hub

import persona.core.contracts.HubScope
import persona.core.contracts.api.KnowledgeKind
import persona.core.contracts.api.KnowledgeUpdate
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.runBlocking
import okhttp3.OkHttpClient
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.mockwebserver.MockResponse
import persona.core.hub.ProtocolCompatibilityMockServer as MockWebServer
import org.junit.Assert.*
import org.junit.Test

class KnowledgeRepositoryTest {
    @Test fun oldDisabledReplyIsScopedAndConfigurationRefusalsDoNotRetry() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setResponseCode(503).setHeader("X-Request-Id", "request-1")
                .setBody("""{"code":"internal","detail":"the knowledge base is off","error":"Request failed"}"""))
            val legacy = runCatching { repository.list(KnowledgeKind.Projects) }.exceptionOrNull() as HubFailure
            assertEquals("kb_disabled", legacy.code)
            assertFalse(legacy.retryable)
            assertEquals("request-1", legacy.requestId)
            assertTrue(legacy.body!!.contains("the knowledge base is off"))
            server.enqueue(MockResponse().setResponseCode(403).setBody("""{"code":"kb_read_only","error":"Request failed","retryable":true}"""))
            val readOnly = runCatching { repository.update(KnowledgeKind.Projects, "example", KnowledgeUpdate(tags = emptyList())) }
                .exceptionOrNull() as HubFailure
            assertEquals("kb_read_only", readOnly.code)
            assertFalse(readOnly.retryable)
            assertEquals(HubError.Server, readOnly.kind)
            assertEquals(2, server.requestCount)
        }
    }

    @Test fun ambiguousLegacyErrorsKeepTheirOriginalCode() {
        for ((path, status, code, detail) in listOf(
            listOf("/api/v1/models", "503", "internal", "the knowledge base is off"),
            listOf("/api/v1/kb-other/projects", "503", "internal", "the knowledge base is off"),
            listOf("/api/v1/kb/projects", "502", "internal", "the knowledge base is off"),
            listOf("/api/v1/kb/projects", "503", "internal", "wrapped: the knowledge base is off"),
            listOf("/api/v1/kb/projects", "503", "future_code", "the knowledge base is off"),
        )) {
            val response = okhttp3.Response.Builder().request(okhttp3.Request.Builder().url("https://hub.invalid$path").build())
                .protocol(okhttp3.Protocol.HTTP_1_1).code(status.toInt()).message("Unavailable")
                .body("""{"code":"$code","detail":"$detail","error":"Request failed"}""".toResponseBody()).build()
            response.use { assertEquals(code, HubFailure.from(it).code) }
        }
    }

    @Test fun preservesAvailabilityCodesAndSuccessfulEmptyLists() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            for (code in listOf("kb_disabled", "kb_unavailable")) {
                server.enqueue(MockResponse().setResponseCode(503).setBody("""{"error":"unavailable","code":"$code"}"""))
                val error = runCatching { repository.list(KnowledgeKind.Projects) }.exceptionOrNull() as HubFailure
                assertEquals(code, error.code)
                assertEquals(503, error.status)
            }
            server.enqueue(MockResponse().setBody("""{"items":[],"tags":[]}"""))
            assertTrue(repository.list(KnowledgeKind.Projects).items.isEmpty())
        }
    }

    @Test fun readsExistingWireFormatEncodesParametersAndRejectsRetiredPairingsAndRedirects() = runBlocking {
        MockWebServer().use { server ->
            var current = true
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { current })
            server.enqueue(MockResponse().setBody("""{"items":[{"kind":"projects","id":"sample","name":"示例项目","relevance":80,"tags":null}],"tags":null,"future":true}"""))
            assertEquals("示例项目", (repository.list(KnowledgeKind.Projects, "A & B").items.single().name).zhAny())
            val list = server.takeRequest()
            assertEquals("A & B", (list.requestUrl!!.queryParameter("tag")).zhAny())
            assertEquals("Bearer fixture-token", (list.getHeader("Authorization")).zhAny())
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"people","id":"sample/#id","path":"data/people/sample","summary":"# 档案","log":null,"links":null},"agents":null}"""))
            assertEquals("# 档案", (repository.detail(KnowledgeKind.People, "sample/#id").summary).zhAny())
            assertEquals("/api/v1/kb/people/sample%2F%23id", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("""{"hits":[{"path":"projects/sample/summary.md","line":"进展"}]}"""))
            assertEquals(1, repository.search("进展 & 状态").size)
            val search = server.takeRequest()
            assertEquals("进展 & 状态", (search.requestUrl!!.queryParameter("q")).zhAny())
            assertEquals("/api/v1/kb-search", (search.requestUrl!!.encodedPath).zhAny())
            server.enqueue(MockResponse().setResponseCode(302).setHeader("Location", "https://example.com/redirect"))
            try { repository.list(KnowledgeKind.Chats); fail("Redirect must not receive pairing credentials") }
            catch (_: HubFailure) { }
            server.takeRequest()
            current = false
            try { repository.search("query"); fail("Retired pairing must not issue requests") }
            catch (_: CancellationException) { }
            assertEquals(4, server.requestCount)
        }
    }

    @Test fun entryKeepsLiveAgentsAndTaggingSendsOnlyWhatIsSet() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"weather","path":"data/projects/weather",
                "meta":{"dirs":["laptop:/code/weather"]}},"agents":[
                {"machine_id":"laptop","session_id":"s1","title":"小组件","cwd":"/code/weather","status":"working"},
                {"machine_id":"laptop","session_id":"s2","cwd":"/code/weather","status":"offline"},
                {"machine_id":"server","session_id":"persona","assistant":true,"status":"idle"}]}"""))
            val entry = repository.entry(KnowledgeKind.Projects, "weather")
            assertEquals(listOf("s1"), (entry.agents.orEmpty().map { it.sessionId }).zhAny())
            assertEquals("/api/v1/kb/projects/weather", (server.takeRequest().path).zhAny())
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"other","path":"x"}}"""))
            assertTrue(runCatching { repository.entry(KnowledgeKind.Projects, "weather") }.exceptionOrNull() is HubFailure)
            server.takeRequest()

            server.enqueue(MockResponse().setBody("""{"id":"weather"}"""))
            repository.update(KnowledgeKind.Projects, "weather", KnowledgeUpdate("P1", 55, emptyList(), "paused", "long"))
            val put = server.takeRequest()
            assertEquals("PUT", (put.method).zhAny())
            assertEquals("/api/v1/kb/projects/weather", (put.path).zhAny())
            assertEquals("Bearer fixture-token", (put.getHeader("Authorization")).zhAny())
            assertEquals("""{"importance":"P1","relevance":55,"tags":[],"status":"paused","project_kind":"long"}""", (put.body.readUtf8()).zhAny())
            server.enqueue(MockResponse().setBody("{}"))
            repository.update(KnowledgeKind.People, "p", KnowledgeUpdate("P0", 0, listOf("a")))
            assertEquals("""{"importance":"P0","relevance":0,"tags":["a"]}""", (server.takeRequest().body.readUtf8()).zhAny())
            server.enqueue(MockResponse().setResponseCode(404).setBody("""{"error":"not found"}"""))
            assertTrue(runCatching { repository.update(KnowledgeKind.People, "gone", KnowledgeUpdate(tags = emptyList())) }.exceptionOrNull() is HubFailure)
        }
    }

    /** An entry's page pins the assistant it was opened for: its reads and saves name it, whatever is current. */
    @Test fun aPagesPinNamesItsAssistantOnReadsAndSaves() = runBlocking {
        try {
            persona.core.contracts.api.AssistantManagerChoice.choose("hub", null, "assistant")
            MockWebServer().use { server ->
                val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                    "fixture-token", OkHttpClient(), { true })
                val pins = persona.core.contracts.api.ManagerPins.of(listOf(listOf("kb"), listOf("kb-search")), "m-work", "hub")!!
                server.enqueue(MockResponse().setBody("""{"detail":{"kind":"projects","id":"weather","path":"data/projects/weather"}}"""))
                kotlinx.coroutines.withContext(pins) { repository.entry(KnowledgeKind.Projects, "weather") }
                assertEquals("/api/v1/kb/projects/weather?manager=m-work", server.takeRequest().path)
                server.enqueue(MockResponse().setBody("{}"))
                kotlinx.coroutines.withContext(pins) { repository.update(KnowledgeKind.Projects, "weather", KnowledgeUpdate(tags = emptyList())) }
                assertEquals("/api/v1/kb/projects/weather?manager=m-work", server.takeRequest().path)
                // Outside the page nothing is pinned: the hub client names the current assistant as before.
                server.enqueue(MockResponse().setBody("""{"items":[]}"""))
                repository.list(KnowledgeKind.Projects)
                assertEquals("/api/v1/kb/projects", server.takeRequest().path)
            }
        } finally { persona.core.contracts.api.AssistantManagerChoice.reset() }
    }

    @Test fun recordingDetailKeepsItsSegments() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"items":[{"kind":"recordings","id":"r1","name":"r1","updated":"2026-11-05T14:00:00Z","extra":"phone"}],"tags":[]}"""))
            assertEquals("phone", repository.list(KnowledgeKind.Recordings).items.single().extra)
            assertEquals("/api/v1/kb/recordings", server.takeRequest().requestUrl!!.encodedPath)
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"recordings","id":"r1","path":"data/recordings/r1",
                "meta":{"started_at":"2026-11-05T14:00:00Z","duration":12,"complete":true,"bookmarks":[]},"summary":"",
                "recent":"x","segments":[{"start":0.5,"end":2,"text":"你好","speaker":"a"}],"unknown":[1]}}"""))
            val detail = repository.detail(KnowledgeKind.Recordings, "r1")
            assertEquals(listOf(0.5 to "你好"), detail.segments.orEmpty().map { it.start to it.text })
            assertEquals("/api/v1/kb/recordings/r1", server.takeRequest().path)
        }
    }

    /** A recording's minutes (#2738): read with its detail; Make minutes and Add to to-dos post to the hub's recordings routes. */
    @Test fun recordingMinutesDecodeAndWritesPostTheirBodies() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"recordings","id":"r1","path":"data/recordings/r1",
                "meta":{"kind":"meeting","minutes_state":"ready"},"segments":[],
                "minutes":{"version":3,"revision":2,"title":"Weekly sync","when":"Tue 14:00","where":"Room A","attendees":["Alex","Sam"],
                "points":[{"text":"Ship on Friday","at":62.5,"who":"Alex"}],"decisions":[{"text":"Keep the beta"}],
                "actions":[{"id":"a1","who":"Sam","what":"Write the notes","by":"Friday","due_date":"2026-11-14","at":90,"todo_id":"t9"},
                {"id":"a2","what":"Book a room"}],"open":[],"partial":true,"markdown":"# Weekly sync","future":1}}}"""))
            val minutes = repository.detail(KnowledgeKind.Recordings, "r1").minutes!!
            assertEquals(3, minutes.version)
            assertEquals("Tue 14:00", minutes.time)
            assertEquals("Room A", minutes.where)
            assertEquals(listOf("Alex", "Sam"), minutes.attendees)
            assertEquals(62.5, minutes.points.single().at!!, 0.0)
            assertNull(minutes.decisions.single().at)
            assertEquals(listOf("2026-11-14" to true, null to false), minutes.actions.map { it.dueDate to it.added })
            assertTrue(minutes.partial)
            assertFalse(minutes.isEmpty)
            server.takeRequest()

            server.enqueue(MockResponse().setResponseCode(202).setBody("""{"state":"pending"}"""))
            repository.makeMinutes("r 1/x")
            val make = server.takeRequest()
            assertEquals("POST", make.method)
            assertEquals("/api/v1/recordings/r%201%2Fx/minutes", make.path)
            assertEquals("Bearer fixture-token", make.getHeader("Authorization"))
            assertEquals("{}", make.body.readUtf8())

            server.enqueue(MockResponse().setBody("""{"todos":[]}"""))
            repository.addMinutesTodos("r1", 3, listOf("a1", "a2"))
            val add = server.takeRequest()
            assertEquals("/api/v1/recordings/r1/minutes/todos", add.path)
            assertEquals("""{"version":3,"actions":["a1","a2"]}""", add.body.readUtf8())

            server.enqueue(MockResponse().setResponseCode(409).setBody("""{"error":"The minutes changed","code":"minutes_stale"}"""))
            val stale = runCatching { repository.addMinutesTodos("r1", 2, listOf("a1")) }.exceptionOrNull() as HubFailure
            assertEquals("minutes_stale", stale.code)
            assertEquals(409, stale.status)
            server.takeRequest()
            assertTrue(runCatching { repository.makeMinutes("..") }.exceptionOrNull() is IllegalArgumentException)
            assertTrue(runCatching { repository.addMinutesTodos("r1", 3, emptyList()) }.exceptionOrNull() is IllegalArgumentException)
            assertEquals(4, server.requestCount)
        }
    }

    /** Topics (docs/assistant/knowledge.md "Topics"): the list's `extra` is the point count; the detail carries its points, unknown kinds and fields kept. */
    @Test fun topicsDecodeTheirPoints() = runBlocking {
        MockWebServer().use { server ->
            val repository = HttpKnowledgeRepository(HubScope("demo", "generation"), server.url("/").toString(),
                "fixture-token", OkHttpClient(), { true })
            server.enqueue(MockResponse().setBody("""{"items":[{"kind":"topics","id":"gardening","name":"Gardening","extra":"12"}],"tags":[]}"""))
            assertEquals("12", repository.list(KnowledgeKind.Topics).items.single().extra)
            assertEquals("/api/v1/kb/topics", server.takeRequest().path)
            server.enqueue(MockResponse().setBody("""{"detail":{"kind":"topics","id":"gardening","path":"data/topics/gardening","points":[
                {"id":"p1","kind":"pitfall","text":"Overwatering rots roots","why":"Soil stays wet","when":"2026-11-02",
                 "sources":[{"kind":"chats","id":"c1","day":"2026-11-01"}]},
                {"id":"p2","path":["Soil","Mix"],"kind":"mystery","text":"Add perlite","future":true},
                {"text":"A bare point"}],"unknown":1}}"""))
            val points = repository.detail(KnowledgeKind.Topics, "gardening").points.orEmpty()
            assertEquals(listOf("pitfall", "mystery", ""), points.map { it.kind })
            assertEquals("2026-11-02", points[0].confirmed)
            assertEquals("Soil stays wet", points[0].why)
            assertEquals(listOf("chats" to "c1"), points[0].sources.orEmpty().map { it.kind to it.id })
            assertEquals(listOf("Soil", "Mix"), points[1].path)
            assertNull(points[2].why)
            assertEquals("/api/v1/kb/topics/gardening", server.takeRequest().path)
        }
    }
}
