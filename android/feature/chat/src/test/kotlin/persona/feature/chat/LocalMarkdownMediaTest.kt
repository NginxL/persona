package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.ui.platform.UriHandler
import kotlinx.coroutines.test.runTest
import org.junit.After
import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Before
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.contracts.api.SessionDirectory
import persona.core.contracts.api.SessionFileContent
import persona.core.contracts.api.SessionFiles
import persona.core.contracts.api.SessionGitDiff
import persona.core.contracts.api.SessionGitLog
import persona.core.contracts.api.SessionGitShow
import persona.core.contracts.api.SessionGitStatus
import java.io.File
import java.nio.file.Files
import java.util.Base64

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class LocalMarkdownMediaTest {
    private lateinit var tempDir: File

    @Before
    fun setUp() {
        LocalMarkdownImages.clearCaches()
        tempDir = Files.createTempDirectory("persona-local-md-test").toFile()
    }

    @After
    fun tearDown() {
        LocalMarkdownImages.clearCaches()
        tempDir.deleteRecursively()
    }

    @Test
    fun `parses and resolves absolute, home, Windows drive, and cwd-relative paths`() {
        assertEquals(LocalPathTarget("/tmp/report.png", null), LocalMarkdownPath.parseLocalPath("/tmp/report.png"))
        assertEquals(LocalPathTarget("~/charts/aa.png", null), LocalMarkdownPath.parseLocalPath("~/charts/aa.png"))
        assertEquals(LocalPathTarget("C:/Users/me/aa.png", null), LocalMarkdownPath.parseLocalPath("C:/Users/me/aa.png"))
        assertEquals(LocalPathTarget("C:\\Users\\me\\aa.png", null), LocalMarkdownPath.parseLocalPath("C:\\Users\\me\\aa.png"))
        assertEquals(LocalPathTarget("charts/my chart.png", null), LocalMarkdownPath.parseLocalPath("charts/my%20chart.png?v=1"))
        assertEquals(LocalPathTarget("./docs/summary.md", 42), LocalMarkdownPath.parseLocalPath("./docs/summary.md#L42"))

        assertEquals("/tmp/report.png", LocalMarkdownPath.resolve("/tmp/report.png", "/work/repo"))
        assertEquals("~/charts/aa.png", LocalMarkdownPath.resolve("~/charts/aa.png", "/work/repo"))
        assertEquals("/work/repo/charts/aa.png", LocalMarkdownPath.resolve("charts/aa.png", "/work/repo"))
        assertEquals("/work/out/fig.png", LocalMarkdownPath.resolve("../out/fig.png", "/work/repo"))
        assertEquals("C:\\Users\\me\\aa.png", LocalMarkdownPath.resolve("C:/Users/me/aa.png", "/work/repo"))
        assertEquals("C:\\work\\proj\\charts\\aa.png", LocalMarkdownPath.resolve("charts/aa.png", "C:\\work\\proj"))
    }

    @Test
    fun `strictly excludes web, protocol-relative, non-local schemes, and hub routes`() {
        val rejected = listOf(
            "",
            "#anchor",
            "//cdn.example.com/a.png",
            "http://example.com/a.png",
            "https://example.com/a.png",
            "file:///etc/passwd",
            "data:image/png;base64,AAAA",
            "blob:https://example.com/123",
            "mailto:user@example.com",
            "tel:+123456789",
            "javascript:alert(1)",
            "persona://session/m1/s1",
            "attachment:chart.png",
            "content://com.example/1",
            "/api/v1/machines/m1/sessions/s1/items/i1/image",
            "/api/v1/remote-image?url=https%3A%2F%2Fexample.com%2Fa.png",
        )
        for (value in rejected) {
            assertNull("Expected rejection for $value", LocalMarkdownPath.parseLocalPath(value))
        }
    }

    @Test
    fun `ReplyImageDocument parses local images, lifts inline local images, and excludes non-local schemes`() {
        val md = """
            Before ![AA 榜单](/tmp/report.png) after.

            ![Rel](charts/aa.png)

            ![Web](https://img.example.com/a.png)

            ![BlockedFile](file:///etc/passwd)
            ![BlockedData](data:image/png;base64,AAAA)
            ![BlockedHub](/api/v1/machines/m1/sessions/s1/items/i1/image)
        """.trimIndent()

        val doc = ReplyImageDocument.parse(md, allowLocal = true)
        assertNotNull(doc)
        val parsed = doc!!
        assertEquals(3, parsed.pictures.size)
        assertTrue(parsed.pictures[0].isLocal)
        assertEquals("/tmp/report.png", parsed.pictures[0].localPath)
        assertTrue(parsed.pictures[1].isLocal)
        assertEquals("charts/aa.png", parsed.pictures[1].localPath)
        assertFalse(parsed.pictures[2].isLocal)
        assertEquals("https://img.example.com/a.png", parsed.pictures[2].url)

        // Inline local image is lifted into its own fenced group while keeping surrounding words
        assertTrue(parsed.markdown.contains("Before"))
        assertTrue(parsed.markdown.contains("after."))
        assertFalse(parsed.markdown.contains("![AA 榜单](/tmp/report.png)"))
    }

    @Test
    fun `LocalMarkdownImages loads via download, caches by full hub-machine-cwd-path key, and isolates failure until manual retry`() = runTest {
        val png = byteArrayOf(0x89.toByte(), 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A)
        var downloadCalls = 0
        var shouldFail = false
        val files = FakeSessionFiles(
            onDownload = { _, _, _, into ->
                downloadCalls++
                if (shouldFail) throw IllegalStateException("network down")
                into.writeBytes(png)
                png.size.toLong()
            },
        )
        val dummyBitmap = Bitmap.createBitmap(4, 4, Bitmap.Config.ARGB_8888)
        val loaderA = LocalMarkdownImages(files, hubScope = "hub-a", machineId = "mac-1", cwd = "/repo/a", cacheDir = tempDir) { _, _ -> dummyBitmap }
        val loaderB = LocalMarkdownImages(files, hubScope = "hub-b", machineId = "mac-1", cwd = "/repo/a", cacheDir = tempDir) { _, _ -> dummyBitmap }

        // First load downloads; second load hits memory/disk cache
        val bmp1 = loaderA.thumbnail("charts/aa.png", 256, attempt = 0)
        val bmp2 = loaderA.thumbnail("charts/aa.png", 256, attempt = 0)
        assertEquals(bmp1, bmp2)
        assertEquals(1, downloadCalls)
        assertArrayEquals(png, loaderA.bytes("charts/aa.png"))
        assertEquals(1, downloadCalls)

        // Different hub scope has a distinct cache key and fetches independently
        loaderB.thumbnail("charts/aa.png", 256, attempt = 0)
        assertEquals(2, downloadCalls)

        // Failure is recorded per attempt and preserved until manual retry()
        shouldFail = true
        loaderA.recordFailure("/tmp/missing.png", attempt = 0, reason = "File not found")
        assertEquals(LocalImageFailure(0, "File not found"), loaderA.failureOf("/tmp/missing.png"))
        assertEquals(0, loaderA.attemptOf("/tmp/missing.png"))

        // Manual retry increments attempt and clears stored failure
        val nextAttempt = loaderA.retry("/tmp/missing.png")
        assertEquals(1, nextAttempt)
        assertEquals(1, loaderA.attemptOf("/tmp/missing.png"))
        assertNull(loaderA.failureOf("/tmp/missing.png"))
    }

    @Test
    fun `LocalMarkdownImages falls back to SessionFiles file base64 when download is unsupported`() = runTest {
        val raw = byteArrayOf(1, 2, 3, 4)
        val b64 = Base64.getEncoder().encodeToString(raw)
        val files = FakeSessionFiles(
            onDownload = { _, _, _, _ -> throw UnsupportedOperationException("no stream") },
            onFile = { _, _, path ->
                SessionFileContent(path = path, name = "a.png", size = raw.size.toLong(), mime = "image/png", data = b64)
            },
        )
        val loader = LocalMarkdownImages(files, hubScope = "hub-a", machineId = "mac-1", cwd = "/repo", cacheDir = tempDir) { _, _ ->
            Bitmap.createBitmap(2, 2, Bitmap.Config.ARGB_8888)
        }
        assertArrayEquals(raw, loader.bytes("a.png"))
    }

    @Test
    fun `PathLinks handler opens local Markdown file links in file viewer and isolates web and non-local links`() {
        val openedLocal = mutableListOf<Pair<String, Int?>>()
        val openedOuter = mutableListOf<String>()
        val outer = object : UriHandler {
            override fun openUri(uri: String) {
                openedOuter += uri
            }
        }
        val handler = PathLinks.handler(outer) { path, line -> openedLocal += path to line }

        handler.openUri("/tmp/report.md")
        handler.openUri("./docs/summary.md#L12")
        handler.openUri("C:/Users/me/notes.md")
        handler.openUri(PathLinks.uri("src/Main.kt", 7))
        handler.openUri("https://example.com/page")
        handler.openUri("http://example.org")
        handler.openUri("file:///etc/passwd")
        handler.openUri("data:text/plain,hi")
        handler.openUri("persona://session/m1/s1")
        handler.openUri("/api/v1/machines/m1/file/download?path=a")

        assertEquals(
            listOf(
                "/tmp/report.md" to null,
                "./docs/summary.md" to 12,
                "C:/Users/me/notes.md" to null,
                "src/Main.kt" to 7,
            ),
            openedLocal,
        )
        assertEquals(
            listOf(
                "https://example.com/page",
                "http://example.org",
                "file:///etc/passwd",
                "data:text/plain,hi",
                "persona://session/m1/s1",
                "/api/v1/machines/m1/file/download?path=a",
            ),
            openedOuter,
        )
    }

    private class FakeSessionFiles(
        private val onDownload: suspend (String, String, String, File) -> Long = { _, _, _, _ -> throw UnsupportedOperationException() },
        private val onFile: suspend (String, String, String) -> SessionFileContent = { _, _, _ -> throw UnsupportedOperationException() },
    ) : SessionFiles {
        override suspend fun directory(machineId: String, cwd: String, path: String): SessionDirectory = throw UnsupportedOperationException()
        override suspend fun file(machineId: String, cwd: String, path: String): SessionFileContent = onFile(machineId, cwd, path)
        override suspend fun download(machineId: String, cwd: String, path: String, into: File): Long = onDownload(machineId, cwd, path, into)
        override suspend fun status(machineId: String, cwd: String): SessionGitStatus = throw UnsupportedOperationException()
        override suspend fun diff(machineId: String, cwd: String, path: String, staged: Boolean): SessionGitDiff = throw UnsupportedOperationException()
        override suspend fun log(machineId: String, cwd: String, skip: Int, limit: Int): SessionGitLog = throw UnsupportedOperationException()
        override suspend fun show(machineId: String, cwd: String, hash: String): SessionGitShow = throw UnsupportedOperationException()
    }
}
