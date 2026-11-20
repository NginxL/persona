package persona.feature.chat

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Color
import android.graphics.Paint
import android.graphics.Path
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.getOrNull
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.ByteArrayOutputStream
import java.io.File
import java.net.InetAddress
import java.util.concurrent.CopyOnWriteArrayList
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.runBlocking
import kotlinx.coroutines.withTimeout
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okhttp3.mockwebserver.Dispatcher
import okhttp3.mockwebserver.MockResponse
import okhttp3.mockwebserver.MockWebServer
import okhttp3.mockwebserver.RecordedRequest
import okio.Buffer
import org.junit.After
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Item
import persona.core.contracts.Endpoint
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ReplyImageSource
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.ViewerImage
import persona.core.hub.HubConnectionConfig
import persona.core.hub.HubNetwork
import persona.core.hub.HubReplyImages
import persona.core.hub.OkHttpHubConnection

/** The picture sources, artwork and messages are fabricated; the regular chat row and native Markdown are exercised. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ReplyImageShotsTest {
    @get:Rule val rule = createComposeRule()
    private val origin = "https://gallery.example"
    private var hub: MockWebServer? = null
    private var connection: OkHttpHubConnection? = null
    @After fun closeFixture() { connection?.close(); hub?.close() }
    private fun image(label: String, n: Int) = "![$label]($origin/$n.png)"
    private data class Read(val url: String, val size: Int, val allowDirect: Boolean)

    private class Pictures(throughHub: Boolean = true) : ReplyImageSource {
        override val features = MutableStateFlow(if (throughHub) setOf(ReplyImageSource.FEATURE) else emptySet())
        val reads = CopyOnWriteArrayList<Read>()
        val failures = mutableSetOf<String>()
        var hold: CompletableDeferred<Unit>? = null
        private val artwork = mutableMapOf<Int, ByteArray>()
        override suspend fun load(url: String, size: Int, allowDirect: Boolean): ByteArray {
            reads.add(Read(url, size, allowDirect))
            hold?.await()
            if (url in failures) error("Fixture unavailable")
            val n = url.substringAfterLast('/').substringBefore('.').toInt()
            return synchronized(artwork) { artwork.getOrPut(n) { landscape(n) } }
        }

        /** Small vector-like landscapes with a distinct horizon and sun, encoded locally into real PNGs. */
        private fun landscape(n: Int): ByteArray {
            val colors = listOf(
                listOf("#E3DBAC", "#93B89C", "#405F64", "#F4C66F"),
                listOf("#B5D8DF", "#678F9C", "#284C60", "#F6CF85"),
                listOf("#F4D7BD", "#D28F78", "#865D6F", "#FFF0B5"),
                listOf("#D8CDDB", "#918DAE", "#4D546C", "#F5D1AC"),
                listOf("#D1DDD0", "#82A29B", "#3E686C", "#F4D6A5"),
                listOf("#F2CEA3", "#CA916A", "#94674F", "#FFE89F"),
                listOf("#BDD6D9", "#92AF9E", "#477477", "#EED99D"),
            )[n % 7].map(Color::parseColor)
            val width = if (n == 0) 48 else if (n == 1) 720 else 300
            val height = if (n == 0) 48 else if (n == 1) 480 else 300
            val bitmap = Bitmap.createBitmap(width, height, Bitmap.Config.ARGB_8888)
            val paint = Paint(Paint.ANTI_ALIAS_FLAG)
            val canvas = Canvas(bitmap)
            canvas.scale(width / 100f, height / 100f)
            canvas.drawColor(colors[0])
            paint.color = colors[3]
            canvas.drawCircle(64f + n % 5 * 3, 24f, 9f, paint)
            fun ridge(color: Int, vararg points: Float) {
                paint.color = color
                val path = Path().apply {
                    moveTo(0f, 100f)
                    for (i in points.indices step 2) lineTo(points[i], points[i+1])
                    lineTo(100f, 100f)
                    close()
                }
                canvas.drawPath(path, paint)
            }
            ridge(colors[1], 0f, 62f, 19f, 55f, 40f, 34f + n % 5, 60f, 56f, 81f, 42f, 100f, 55f)
            ridge(colors[2], 0f, 77f, 25f, 70f, 49f, 74f, 79f, 59f, 100f, 66f)
            return ByteArrayOutputStream().use { out ->
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, out)
                bitmap.recycle()
                out.toByteArray()
            }
        }
    }

    private fun actions(source: ReplyImageSource, open: (List<ViewerImage>, Int) -> Unit = { _, _ -> }) =
        ChatRowActions(SessionKey("fixture", "reply"), { "" }, null, photos = null, pictures = { emptyList() },
            openImages = open, menu = null, retryLineId = null, onRetry = {}, replyImages = source)

    /** The screenshot path pairs the production reader to a loopback mock hub that serves only the synthetic artwork. */
    private fun pairToFixture(pictures: Pictures): ReplyImageSource {
        val server = MockWebServer().also { hub = it }
        server.dispatcher = object : Dispatcher() {
            override fun dispatch(request: RecordedRequest): MockResponse {
                val target = requireNotNull(request.requestUrl)
                if (target.encodedPath == "/api/v1/health")
                    return MockResponse().setBody("""{"ok":true,"hub_id":"fixture"}""")
                if (request.getHeader("Authorization") != "Bearer example-device") return MockResponse().setResponseCode(403)
                return when (target.encodedPath) {
                    "/api/v1/ws" -> MockResponse().withWebSocketUpgrade(object : WebSocketListener() {
                        override fun onOpen(webSocket: WebSocket, response: Response) {
                            webSocket.send("""{"type":"hello","hub_id":"fixture","features":["remote_images"],"endpoints":[]}""")
                        }
                    })
                    "/api/v1/remote-image" -> {
                        val url = requireNotNull(target.queryParameter("url"))
                        require(url.startsWith("$origin/"))
                        val bytes = runBlocking { pictures.load(url, requireNotNull(target.queryParameter("size")).toInt(), false) }
                        MockResponse().setHeader("Content-Type", "image/png").setBody(Buffer().write(bytes))
                    }
                    else -> MockResponse().setResponseCode(404)
                }
            }
        }
        server.start(InetAddress.getLoopbackAddress(), 0)
        val paired = OkHttpHubConnection(HubConnectionConfig("fixture", null, "example-device",
            listOf(Endpoint(server.url("/").toString(), "lan")), "fixture"), HubNetwork("loopback")).also { connection = it }
        runBlocking { withTimeout(10_000) { paired.serverFeatures.first { ReplyImageSource.FEATURE in it } } }
        return HubReplyImages(paired) { true }
    }

    private fun screenshot(name: String) {
        rule.mainClock.advanceTimeBy(600)
        rule.waitForIdle()
        val out = File(System.getenv("PERSONA_REPLY_IMAGE_SHOTS") ?: "build/reply-image-shots").apply { mkdirs() }
        val bitmap = rule.onNodeWithTag("reply-proof").captureToImage().asAndroidBitmap()
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun awaitPicture(label: String) = rule.waitUntil(15_000) {
        rule.onAllNodes(SemanticsMatcher("ready picture") {
            it.config.getOrNull(SemanticsActions.OnClick)?.label == label
        }, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty()
    }

    @Test fun galleryContainsInlineVisibleAndHiddenPicturesAndDrawsEveryStyle() {
        val source = Pictures()
        val labels = listOf("Canyon", "Coast", "Evening", "Foothills", "Mesa", "Inlet", "Island")
        val message = "### Ridgefield survey\n\nThe map ${image("Trail marker", 0)} highlights the morning route.\n\n" +
            image("Lake overlook", 1) + "\n\nSeven other viewpoints\n\n" +
            labels.mapIndexed { index, label -> image(label, index + 2) }.joinToString("\n")
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        var opened = emptyList<ViewerImage>()
        var selected = -1
        var original: String? = null
        val uri = object : UriHandler { override fun openUri(uri: String) { original = uri } }
        val actions = actions(pairToFixture(source)) { images, index -> opened = images; selected = index }
        rule.setContent {
            CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, fontScale), LocalChatRowActions provides actions, LocalUriHandler provides uri) {
                PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                    Column(Modifier.testTag("reply-proof").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        MessageContent(Item("reply", kind = "text", role = "assistant", text = message), user = false)
                    }
                }
            }
        }
        rule.waitUntil(20_000) { source.reads.map { it.url }.distinct().size >= 8 }
        (listOf("Lake overlook") + labels.take(6)).forEach(::awaitPicture)
        rule.onNodeWithText("+1").assertExists()
        assertTrue(source.reads.none(Read::allowDirect))
        assertEquals(256, source.reads.first { it.url.endsWith("/0.png") }.size)
        assertEquals(768, source.reads.first { it.url.endsWith("/1.png") }.size)
        assertTrue(source.reads.filter { it.url.substringAfterLast('/').first() in '2'..'7' }.all { it.size == 256 })
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            screenshot("reply-${s.name.lowercase()}-${if (d) "dark" else "light"}")
        }
        style = AppStyle.Glass
        fontScale = 2f
        for (d in listOf(false, true)) { dark = d; screenshot("reply-glass-large-${if (d) "dark" else "light"}") }
        rule.onNodeWithContentDescription("Inlet +1").performClick()
        assertEquals(7, selected)
        assertEquals(listOf("Trail marker", "Lake overlook") + labels, opened.map(ViewerImage::description))
        runBlocking { opened.last().load() }
        assertEquals(Read("$origin/8.png", 2048, false), source.reads.last())
        rule.onNodeWithContentDescription("Trail marker", useUnmergedTree = true).performClick()
        assertEquals(0, selected)
        rule.onNodeWithContentDescription("Lake overlook").performSemanticsAction(SemanticsActions.OnLongClick)
        rule.onNodeWithText("gallery.example").assertExists()
        rule.onNodeWithText("Open original").performClick()
        assertEquals("$origin/1.png", original)
    }

    @Test fun externalAndOlderHubPicturesWaitAndUnopenedGalleryNeighborsStayGated() {
        val source = Pictures()
        val pictures = image("First hill", 2) + "\n" + image("Second hill", 3)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                Column(Modifier.testTag("reply-proof").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    ExternalReplyMarkdown(pictures, source)
                }
            }
        }
        rule.waitUntil(15_000) { rule.onAllNodesWithText("Load image").fetchSemanticsNodes().size == 2 }
        assertTrue(source.reads.isEmpty())
        screenshot("external-before-loading")
        rule.onNodeWithContentDescription("First hill").performClick()
        rule.waitUntil(10_000) { source.reads.count { it.url.endsWith("/2.png") } == 1 }
        awaitPicture("First hill")
        rule.onNodeWithContentDescription("First hill").performClick()
        rule.onNodeWithText("1 / 2").assertExists()
        try {
            rule.waitUntil(10_000) { source.reads.any { it.size == 2048 } }
        } catch (timeout: ComposeTimeoutException) {
            throw AssertionError("Viewer reads ${source.reads}; ${rule.onAllNodes(isRoot(), useUnmergedTree = true).printToString(maxDepth = 8)}", timeout)
        }
        assertTrue(source.reads.none { it.url.endsWith("/3.png") })
        assertTrue(source.reads.none(Read::allowDirect))
    }

    @Test fun singleTileReservesSpaceAnErrorShrinksAndOneTapRetries() {
        val source = Pictures(throughHub = false)
        val url = "$origin/1.png"
        source.failures.add(url)
        val row = actions(source)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalChatRowActions provides row) {
                    Column(Modifier.testTag("reply-proof").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        MessageContent(Item("old", role = "assistant", kind = "text", text = image("Overlook", 1)), user = false)
                    }
                }
            }
        }
        rule.waitUntil(15_000) { rule.onAllNodesWithText("Load image").fetchSemanticsNodes().isNotEmpty() }
        assertTrue(source.reads.isEmpty())
        val tile = rule.onNodeWithContentDescription("Overlook")
        tile.assertHeightIsEqualTo(240.dp)
        tile.performClick()
        rule.waitUntil(10_000) { tile.fetchSemanticsNode().size.height == with(rule.density) { 80.dp.roundToPx() } }
        screenshot("retry-single")
        source.failures.clear()
        val release = CompletableDeferred<Unit>()
        source.hold = release
        tile.performClick()
        rule.waitUntil(10_000) { source.reads.size == 2 }
        tile.assertHeightIsEqualTo(240.dp)
        screenshot("loading-single")
        release.complete(Unit)
        rule.waitForIdle()
        assertTrue(source.reads.all(Read::allowDirect))
    }
}
