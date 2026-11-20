package persona.core.hub

import kotlinx.coroutines.runBlocking
import okhttp3.Protocol
import okhttp3.Request
import okhttp3.Response
import okhttp3.ResponseBody.Companion.toResponseBody
import okhttp3.HttpUrl.Companion.toHttpUrl
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.api.SessionMoveConflictPolicy
import persona.core.contracts.api.SessionMovedFile

class HubSessionFilesTest {
    @Test fun fileAndGitRequestsEncodePathsAndAcceptEmptyAndPartialResponses() = runBlocking {
        val requests = mutableListOf<String>()
        var body = """{"path":"/workspace/a b#%.kt","name":"a b#%.kt","text":"val a = 1","future":true}"""
        var status = 200
        val api = HubSessionFiles({ path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build())
                .protocol(Protocol.HTTP_1_1).code(status).message("response").body(body.toResponseBody()).build()
        })
        assertEquals("val a = 1", (api.file("machine/segment", "/workspace/中文 &", "/workspace/a b#%.kt").text).zhAny())
        val url = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/machine%2Fsegment/file", (url.encodedPath).zhAny())
        assertEquals("/workspace/中文 &", (url.queryParameter("cwd")).zhAny())
        assertEquals("/workspace/a b#%.kt", (url.queryParameter("path")).zhAny())

        body = """{"is_repo":true,"files":null,"commits":null}"""
        assertTrue(api.status("machine", "/workspace").files.isEmpty())
        body = """{"path":"/workspace","entries":null,"truncated":true}"""
        assertTrue(api.directory("machine", "/workspace", "/workspace").truncated)
        body = """{"path":"a.kt","diff":"+new","truncated":true}"""
        assertTrue(api.diff("machine", "/workspace", "a.kt", true).truncated)
        assertEquals("1", (("https://example.com" + requests.last()).toHttpUrl().queryParameter("staged")).zhAny())
        body = """{"commits":null}"""
        assertTrue(api.log("machine", "/workspace", 30).commits.isEmpty())
        assertEquals("30", (("https://example.com" + requests.last()).toHttpUrl().queryParameter("skip")).zhAny())
        body = """{"commit":{"hash":"abc"},"files":null,"diff":""}"""
        assertEquals("abc", (api.show("machine", "/workspace", "abc").commit.hash).zhAny())
        body = """{"stats":[{"path":"src/a.kt","kind":"file","size":12},{"path":"~/x y/","kind":"dir"},{"path":"/gone/a.md"}]}"""
        val stats = api.stat("machine", "/workspace", listOf("src/a.kt", "~/x y/", "/gone/a.md")).stats
        assertEquals(listOf("file", "dir", "missing"), (stats.map { it.kind }).zhAny())
        assertEquals(12L, stats.first().size)
        val statUrl = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/machine/stat", (statUrl.encodedPath).zhAny())
        assertEquals("src/a.kt\n~/x y/\n/gone/a.md", (statUrl.queryParameter("paths")).zhAny())
        assertEquals("/workspace", (statUrl.queryParameter("cwd")).zhAny())
        status = 404
        body = """{"error":"not found","code":"not_found"}"""
        try { api.file("machine", "/workspace", "missing"); fail("Expected HTTP failure") }
        catch (failure: HubFailure) { assertEquals(404, failure.status) }
    }

    @Test fun aVersionOfAFileComesFromGitBlobWhenTheHubHasIt() = runBlocking {
        val requests = mutableListOf<String>()
        var body = """{"path":"/cache/blobs/ab12.png","size":10}"""
        var features = emptySet<String>()
        val respond: suspend (String) -> Response = { path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build())
                .protocol(Protocol.HTTP_1_1).code(200).message("response").body(body.toResponseBody()).build()
        }
        val api = HubSessionFiles(respond, features = { features })
        assertFalse(api.comparesVersions("machine"))
        features = setOf("git_blob")
        assertTrue(api.comparesVersions("machine"))
        assertEquals("/cache/blobs/ab12.png", api.blob("machine", "/workspace", "art/a b.png", "abc^").path)
        val url = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/machine/git/blob", url.encodedPath)
        assertEquals("art/a b.png", url.queryParameter("path"))
        assertEquals("abc^", url.queryParameter("rev"))
        body = """{"missing":true}"""
        assertTrue(api.blob("machine", "/workspace", "a.png", "HEAD").missing)
    }

    @Test fun thumbnailsReturnThePictureAndNullWhenTheMachineCannotDrawIt(): Unit = runBlocking {
        val requests = mutableListOf<Pair<String, Long>>()
        var status = 200
        var type = "image/jpeg"
        var errorCode = "no_thumbnail"
        val respond: suspend (String, Long) -> Response = { path, seconds ->
            requests += path to seconds
            val response = Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(status).message("response").header("Content-Type", type)
                .body(if (status == 200) byteArrayOf(1, 2, 3).toResponseBody() else """{"error":"x","code":"$errorCode"}""".toResponseBody()).build()
            // As OkHttpHubConnection.request: any status but 2xx arrives as a HubFailure, not as a response.
            if (status !in 200..299) response.use { throw HubFailure.from(it) }
            response
        }
        val api = HubSessionFiles({ error("thumbnails wait longer than the shared read timeout") }, slow = respond)
        assertArrayEquals(byteArrayOf(1, 2, 3), api.thumbnail("m", "/w", "docs/a b.pdf", 2000))
        val url = ("https://example.com" + requests.last().first).toHttpUrl()
        assertEquals("/api/v1/machines/m/file/thumb", url.encodedPath)
        assertEquals("docs/a b.pdf", url.queryParameter("path"))
        assertEquals("1024", url.queryParameter("size"))
        assertEquals(HubSessionFiles.THUMB_TIMEOUT_SECONDS, requests.last().second)
        status = 404
        assertNull(api.thumbnail("m", "/w", "a.docx", 128))
        status = 200; type = "text/html"
        try { api.thumbnail("m", "/w", "a.docx", 128); fail("Expected a protocol failure") }
        catch (failure: HubFailure) { assertEquals(HubError.Protocol, failure.kind) }
        status = 409; type = "application/json"; errorCode = "node_outdated"
        try { api.thumbnail("m", "/w", "a.docx", 128); fail("Expected HTTP failure") }
        catch (failure: HubFailure) { assertEquals(409, failure.status) }
        // A timeout is not the machine saying it has no picture: it stays an error.
        val timedOut = HubSessionFiles({ error("unused") }, slow = { _, _ -> throw HubFailure(HubError.Timeout) })
        try { timedOut.thumbnail("m", "/w", "a.docx", 128); fail("Expected a timeout") }
        catch (failure: HubFailure) { assertEquals(HubError.Timeout, failure.kind) }
    }

    @Test fun convertAsksForAPdfAndWaitsForLibreOffice(): Unit = runBlocking {
        val requests = mutableListOf<Pair<String, Long>>()
        val api = HubSessionFiles({ error("conversions wait longer than the shared read timeout") }, slow = { path, seconds ->
            requests += path to seconds
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(200).message("response").body("""{"path":"/cache/a.pdf","size":42,"mime":"application/pdf"}""".toResponseBody()).build()
        })
        val got = api.convertToPdf("m", "/w", "a.docx")
        assertEquals("/cache/a.pdf", got.path)
        assertEquals(42L, got.size)
        val url = ("https://example.com" + requests.last().first).toHttpUrl()
        assertEquals("/api/v1/machines/m/file/convert", url.encodedPath)
        assertEquals("pdf", url.queryParameter("to"))
        assertEquals("a.docx", url.queryParameter("path"))
        assertEquals(HubSessionFiles.CONVERT_TIMEOUT_SECONDS, requests.last().second)
        assertTrue(HubSessionFiles.CONVERT_TIMEOUT_SECONDS > 120)
    }

    @Test fun previewAsksForAStreamAndReadsAnOlderHubsBase64(): Unit = runBlocking {
        val requests = mutableListOf<String>()
        var body = """{"path":"/w/clip.mp4","name":"clip.mp4","size":9000000,"mime":"video/mp4","stream":true,"revision":"r1"}"""
        val api = HubSessionFiles({ path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(200).message("response").body(body.toResponseBody()).build()
        })
        val streamed = api.filePreview("m", "/w", "clip.mp4")
        assertTrue(streamed.stream)
        assertNull(streamed.data)
        assertEquals("1", ("https://example.com" + requests.last()).toHttpUrl().queryParameter("stream"))
        // An older hub ignores the flag: the base64 comes as before.
        body = """{"path":"/w/a.png","name":"a.png","size":3,"mime":"image/png","data":"AQID"}"""
        val old = api.filePreview("m", "/w", "a.png")
        assertFalse(old.stream)
        assertEquals("AQID", old.data)
        // Plain file() (pictures in Markdown, the chat's photos) still asks for the bytes.
        api.file("m", "/w", "a.png")
        assertNull(("https://example.com" + requests.last()).toHttpUrl().queryParameter("stream"))
    }

    @Test fun pullPostsTheFolderAndDecodesTheNewStatus(): Unit = runBlocking {
        val posts = mutableListOf<Pair<String, String>>()
        var status = 200
        var answer = """{"status":{"is_repo":true,"branch":"main","upstream":"origin/main","behind":0,"commits":[{"hash":"fff","subject":"更新示例代码"}],"files":null},"updated":true,"future":1}"""
        val api = HubSessionFiles({ error("GET used for a pull") }, post = { path, body ->
            posts += path to body
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(status).message("response").body(answer.toResponseBody()).build()
        })
        val pulled = api.pull("machine/segment", "/workspace/中文 &")
        assertTrue(pulled.updated)
        assertEquals("main", pulled.status.branch)
        assertEquals(listOf("fff"), pulled.status.commits.map { it.hash })
        assertEquals("/api/v1/machines/machine%2Fsegment/git/pull", posts.last().first)
        assertEquals("""{"cwd":"/workspace/中文 &"}""", posts.last().second)

        answer = """{"status":{"is_repo":true}}"""
        assertFalse(api.pull("machine", "/workspace").updated)

        // A refusal as the hub answers it (#1888): a 409 with the reason in the app's language, the node's words as the detail.
        status = 409
        answer = """{"code":"uncommitted_changes","delivered":"none","error":"工作区有未提交的改动，请先在电脑上提交或 stash，再拉取。","detail":"the working tree has uncommitted changes: commit or stash them first"}"""
        try { api.pull("machine", "/workspace"); fail("Expected HTTP failure") }
        catch (failure: HubFailure) {
            assertEquals(409, failure.status)
            assertEquals("工作区有未提交的改动，请先在电脑上提交或 stash，再拉取。", failure.message)
        }
    }

    @Test fun downloadStreamsTheWholeFileIntoPlace(): Unit = runBlocking {
        val requests = mutableListOf<String>()
        var status = 200
        val bytes = ByteArray(300_000) { (it % 251).toByte() }
        val respond: suspend (String) -> Response = { path ->
            requests += path
            Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                .code(status).message("response").body(if (status == 200) bytes.toResponseBody() else """{"code":"not_found"}""".toResponseBody()).build()
        }
        val api = HubSessionFiles({ error("buffered request used for a download") }, stream = respond)
        val dir = kotlin.io.path.createTempDirectory("dl").toFile()
        val into = java.io.File(dir, "a/song.mp3")
        assertEquals(300_000L, api.download("m", "/w", "music/song a.mp3", into))
        assertArrayEquals(bytes, into.readBytes())
        val url = ("https://example.com" + requests.last()).toHttpUrl()
        assertEquals("/api/v1/machines/m/file/download", (url.encodedPath).zhAny())
        assertEquals("music/song a.mp3", (url.queryParameter("path")).zhAny())
        assertEquals("/w", (url.queryParameter("cwd")).zhAny())

        status = 404
        val missing = java.io.File(dir, "missing.mp3")
        try { api.download("m", "/w", "missing.mp3", missing); fail("Expected HTTP failure") }
        catch (failure: HubFailure) { assertEquals(404, failure.status) }
        assertFalse(missing.exists())
        assertEquals(listOf("song.mp3"), (java.io.File(dir, "a").list()!!.toList()).zhAny())
        dir.deleteRecursively()
    }

    @Test fun fileEditsPostTheirBodiesAndDecodeTheAnswers(): Unit = runBlocking {
        val posts = mutableListOf<Pair<String, String>>()
        var status = 200
        var answer = """{"path":"~/proj/b c.txt"}"""
        val api = HubSessionFiles({ error("GET used for a change") }, post = { _, _ -> error("pull's POST used for a change") },
            editPost = { path, body ->
                posts += path to body
                val response = Response.Builder().request(Request.Builder().url("https://example.com$path").build()).protocol(Protocol.HTTP_1_1)
                    .code(status).message("response").body(answer.toResponseBody()).build()
                // As OkHttpHubConnection.request: any status but 2xx arrives as a HubFailure.
                if (status !in 200..299) response.use { throw HubFailure.from(it) }
                response
            })
        assertEquals("~/proj/b c.txt", api.rename("machine/segment", "/w/中文 &", "~/proj/a.txt", "b c.txt"))
        assertEquals("/api/v1/machines/machine%2Fsegment/files/rename", posts.last().first)
        assertEquals("""{"path":"~/proj/a.txt","cwd":"/w/中文 &","name":"b c.txt"}""", posts.last().second)

        answer = """{"trashed":[{"path":"~/d/b.txt","trash_id":"t1"},{"path":"~/d/c","trash_id":"t2"}],"future":1}"""
        assertEquals(listOf("t1", "t2"), api.trash("m", "/w", listOf("~/d/b.txt", "~/d/c")).map { it.trashId })
        assertEquals("/api/v1/machines/m/files/trash", posts.last().first)
        assertEquals("""{"paths":["~/d/b.txt","~/d/c"],"cwd":"/w"}""", posts.last().second)
        answer = """{"trashed":null}"""
        assertTrue(api.trash("m", "/w", listOf("a")).isEmpty())

        answer = """{"restored":[{"path":"~/d/b.txt"}]}"""
        assertEquals(listOf("~/d/b.txt"), api.restore("m", listOf("t1")))
        assertEquals("/api/v1/machines/m/files/restore", posts.last().first)
        assertEquals("""{"trash_ids":["t1"]}""", posts.last().second)

        // The machine's refusals keep their code: outside the home, a taken name, an old node, an old hub without the routes.
        for ((code, body) in listOf(409 to """{"error":"only the home","code":"read_only"}""", 409 to """{"error":"taken","code":"exists","name":"b"}""",
                409 to """{"error":"personad on box is too old","code":"node_outdated"}""", 404 to "404 page not found")) {
            status = code; answer = body
            try { api.rename("m", "/w", "/etc/hosts", "b"); fail("Expected a refusal") }
            catch (failure: HubFailure) {
                assertEquals(code, failure.status)
                assertEquals(Regex("\"code\":\"(\\w+)\"").find(body)?.groupValues?.get(1), failure.code)
            }
        }
        // A dropped connection stays the connection's failure.
        val offline = HubSessionFiles({ error("unused") }, editPost = { _, _ -> throw HubFailure(HubError.Timeout) })
        try { offline.trash("m", "/w", listOf("a")); fail("Expected a timeout") }
        catch (failure: HubFailure) { assertEquals(HubError.Timeout, failure.kind) }
        assertTrue(HubSessionFiles.EDIT_TIMEOUT_SECONDS > 120)
    }

    @Test fun editsAreOfferedByTheHubsFileEditFeature() {
        var features = emptySet<String>()
        val api = HubSessionFiles({ error("unused") }, features = { features })
        // No list yet (an older hub's hello): offered, and the answer tells (iOS FolderEdits.available).
        assertTrue(api.editsFiles("m"))
        features = setOf("git_blob", "upload_dir")
        assertFalse(api.editsFiles("m"))
        features = setOf("git_blob", "file_edit")
        assertTrue(api.editsFiles("m"))
    }

    @Test fun movePostsOnePathAndOnlyAnExplicitConflictPolicy(): Unit = runBlocking {
        val posts = mutableListOf<Pair<String, String>>()
        val api = HubSessionFiles({ error("GET used for a change") }, editPost = { path, body ->
            posts += path to body
            val initial = posts.size == 1
            val answer = if (initial) """{"error":"taken","code":"exists","name":"a.txt"}"""
                else """{"moved":[{"from":"~/a.txt","to":"~/b c/a 2.txt"}]}"""
            Response.Builder().request(Request.Builder().url("https://example.com$path").build())
                .protocol(Protocol.HTTP_1_1).code(if (initial) 409 else 200).message("response").body(answer.toResponseBody()).build()
        })
        try { api.move("machine/segment", "/w", "~/a.txt", "~/b c"); fail("Expected conflict") }
        catch (failure: HubFailure) {
            assertEquals(409, failure.status)
            assertEquals("exists", failure.code)
        }
        assertEquals(1, posts.size)
        for (policy in listOf(SessionMoveConflictPolicy.KeepBoth, SessionMoveConflictPolicy.Replace)) {
            assertEquals(SessionMovedFile("~/a.txt", "~/b c/a 2.txt"), api.move("machine/segment", "/w", "~/a.txt", "~/b c", policy))
        }
        assertEquals(List(3) { "/api/v1/machines/machine%2Fsegment/files/move" }, posts.map { it.first })
        assertEquals(listOf(
            """{"paths":["~/a.txt"],"cwd":"/w","dir":"~/b c"}""",
            """{"paths":["~/a.txt"],"cwd":"/w","dir":"~/b c","conflict":"keep"}""",
            """{"paths":["~/a.txt"],"cwd":"/w","dir":"~/b c","conflict":"replace"}""",
        ), posts.map { it.second })
    }
}
