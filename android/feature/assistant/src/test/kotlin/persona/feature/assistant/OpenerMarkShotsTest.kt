package persona.feature.assistant

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.Path
import android.graphics.RectF
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.awaitCancellation
import kotlinx.coroutines.cancel
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.api.ExtensionIconPath
import persona.core.contracts.api.HubExtension
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHomeDrawer
import persona.core.designsystem.PersonaTheme
import java.io.ByteArrayOutputStream
import java.io.File
import java.time.Instant

/**
 * #10358 in pictures: the drawer's Recent with a Lark /agent conversation a made-up third-party bridge opened (it reuses
 * lark.agent), one another extension opened and Lark /agent from the Lark extension itself, drawn on the JVM with
 * Robolectric's native graphics in every style, light and dark, at the default and the largest text size (2×). "before" is
 * the page with no installed extensions to draw from (today's marks, the same code path as `main`); "after" has both
 * openers installed with their own icons. A third run, with both installed but without icons, must draw "before"'s pixels.
 * Written as PNGs to `build/opener-shots/` (or `-Dpersona.shots=<dir>`), the rows alone as `crop-*.png`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2100dp-xhdpi")
class OpenerMarkShotsTest {
    @get:Rule val rule = createComposeRule()

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    @After fun stop() = scope.cancel()

    private val minutesAgo = { m: Long -> Instant.now().minusSeconds(m * 60).toString() }
    private val main = Session(machineId = "hub", sessionId = "main", title = "主对话", status = "idle", updatedAt = minutesAgo(1))
    private val bridged = Session(machineId = "hub", sessionId = "bridged", title = "订周五下午的会议室", status = "idle",
        origin = "lark_agent", originRef = "chat-bridge", openedBy = "chat-bridge", updatedAt = minutesAgo(3))
    private val docs = Session(machineId = "hub", sessionId = "docs", title = "整理本周的文档", status = "idle",
        origin = "extension", originRef = "example-docs", openedBy = "example-docs", updatedAt = minutesAgo(8))
    private val larkOwn = Session(machineId = "hub", sessionId = "lark", title = "总结这篇飞书文档", status = "idle",
        origin = "lark_agent", originRef = "lark-ext", openedBy = "lark-ext", updatedAt = minutesAgo(15))
    private val own = Session(machineId = "hub", sessionId = "own", title = "周末去哪儿", status = "idle", origin = "user", updatedAt = minutesAgo(30))
    private val rows = listOf(bridged, docs, larkOwn, own)

    private val withIcons = listOf(
        HubExtension(id = "chat-bridge", name = "Chat Bridge", iconUrl = "/api/v1/extensions/chat-bridge/-/icon?v=1.0.0",
            contributes = mapOf("messaging" to listOf("examplechat"))),
        HubExtension(id = "example-docs", name = "Example Docs", iconUrl = "/api/v1/extensions/example-docs/-/icon?v=2.1.0"),
        HubExtension(id = "lark-ext", name = "Lark", iconUrl = "/api/v1/extensions/lark-ext/-/icon?v=3.0.0",
            contributes = mapOf("connectors" to listOf("lark"))),
    )

    private class Hub(
        private val list: List<HubExtension>,
        private val answer: suspend (String) -> ByteArray = { path -> if ("chat-bridge" in path) bubble() else page() },
    ) : OpenerIcons.Source {
        val asked = mutableListOf<String>()
        var listReads = 0
        override val listsExtensions = true
        override suspend fun extensions(): List<HubExtension> {
            listReads++
            return list
        }
        override fun iconPath(url: String) = url.takeIf(ExtensionIconPath::matches)
        override suspend fun icon(path: String, manager: String?): ByteArray {
            asked += path
            return answer(path)
        }
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/opener-shots").also { it.mkdirs() }

    private fun save(bitmap: Bitmap, name: String) =
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }

    /** The four rows together, from the page's capture. */
    private fun rowsOf(page: Bitmap): Bitmap {
        val bounds = rows.map { rule.onNodeWithText(it.title).fetchSemanticsNode().boundsInRoot }.reduce { a, b ->
            Rect(minOf(a.left, b.left), minOf(a.top, b.top), maxOf(a.right, b.right), maxOf(a.bottom, b.bottom))
        }
        val top = bounds.top.toInt().coerceAtLeast(0)
        val bottom = bounds.bottom.toInt().coerceAtMost(page.height)
        return Bitmap.createBitmap(page, 0, top, page.width, bottom - top)
    }

    private fun marks(label: String) = rule.onAllNodesWithContentDescription(label, useUnmergedTree = true).fetchSemanticsNodes().size

    @Test fun drawsTheOpenersOwnIconAndNameOnlyOnceInstalledWithOne() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var scale by mutableFloatStateOf(1f)
        var openers by mutableStateOf<OpenerIcons?>(null)
        val groups = ConversationGroups(main = main, others = rows, recent = rows)
        rule.setContent {
            CompositionLocalProvider(LocalOpenerIcons provides openers) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    CompositionLocalProvider(LocalDensity provides Density(LocalDensity.current.density, scale),
                        LocalHomeDrawer provides (style == AppStyle.Glass)) {
                        Box(Modifier.testTag("shot").width(400.dp).height(1300.dp).background(PersonaTheme.palette.background)) {
                            AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null,
                                onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {})
                        }
                    }
                }
            }
        }
        val installed = Hub(withIcons)
        val iconless = Hub(withIcons.map { it.copy(iconUrl = null) })
        for (s in AppStyle.entries) for (d in listOf(false, true)) for (x in listOf(1f, 2f)) {
            val name = "${s.name.lowercase()}-${if (d) "dark" else "light"}-${if (x == 1f) "default" else "2x"}"
            rule.runOnIdle { style = s; dark = d; scale = x; openers = null }
            rule.waitForIdle()
            assertEquals("$name before: Lark /agent's words for both", 2, marks("来自 飞书 /agent"))
            assertEquals(1, marks("来自扩展"))
            val before = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            save(before, "before-$name")
            save(rowsOf(before), "crop-before-$name")

            rule.runOnIdle { openers = OpenerIcons(scope, iconless, io = Dispatchers.Unconfined) }
            rule.waitForIdle()
            val fallback = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            assertTrue("$name: extensions without icons keep today's marks, pixel for pixel", fallback.sameAs(before))
            assertEquals(2, marks("来自 飞书 /agent"))

            rule.runOnIdle { openers = OpenerIcons(scope, installed, io = Dispatchers.Unconfined) }
            rule.waitUntil(5_000) { marks("来自 Chat Bridge") == 1 }
            rule.waitUntil(5_000) { marks("来自 Example Docs") == 1 }
            assertEquals("$name: the Lark extension's own /agent keeps Lark's mark and words", 1, marks("来自 飞书 /agent"))
            assertEquals(0, marks("来自扩展"))
            val after = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            assertTrue("$name: the openers' icons change the page", !after.sameAs(before))
            save(after, "after-$name")
            save(rowsOf(after), "crop-after-$name")
        }
        assertTrue("the Lark extension's icon is never asked for", installed.asked.none { "lark-ext" in it })
        assertTrue("no icon asked when the list has none", iconless.asked.isEmpty())
    }

    /** The drawer's Recent with [rows], in Glass, light, marked from [openers]. */
    private fun showDrawer(openers: () -> OpenerIcons?) {
        val groups = ConversationGroups(main = main, others = rows, recent = rows)
        rule.setContent {
            CompositionLocalProvider(LocalOpenerIcons provides openers()) {
                PersonaTheme(darkTheme = false, reduceMotion = true, style = AppStyle.Glass) {
                    Box(Modifier.width(400.dp).height(1300.dp)) {
                        AssistantDrawer("", {}, groups, active = null, hasMain = true, onEnd = null,
                            onMain = {}, onOpen = {}, onInfo = {}, onNew = {}, onSettings = {})
                    }
                }
            }
        }
    }

    @Test fun anIconThatFailsOrIsStillLoadingKeepsTheOriginsMarkAndWords() {
        val failing = Hub(withIcons) { error("404") }
        val loading = Hub(withIcons) { awaitCancellation() }
        var openers by mutableStateOf<OpenerIcons?>(null)
        showDrawer { openers }
        for ((name, hub) in listOf("failed" to failing, "loading" to loading)) {
            rule.runOnIdle { openers = OpenerIcons(scope, hub, io = Dispatchers.Unconfined) }
            rule.waitForIdle()
            assertEquals("$name: both icons were asked for", 2, hub.asked.size)
            assertEquals("$name: Lark /agent's mark and words stay", 2, marks("来自 飞书 /agent"))
            assertEquals(1, marks("来自扩展"))
            assertEquals("$name: never named before its icon is drawn", 0, marks("来自 Chat Bridge"))
            assertEquals(0, marks("来自 Example Docs"))
        }
    }

    @Test fun aFailedIconIsAskedAgainWhileItsRowStaysOnceTheSameListIsReadAgain() {
        var offline = true
        val hub = Hub(withIcons) { path -> if (offline) error("offline") else if ("chat-bridge" in path) bubble() else page() }
        val openers = OpenerIcons(scope, hub, io = Dispatchers.Unconfined)
        showDrawer { openers }
        rule.waitForIdle()
        assertEquals("both icons asked once, and failed", 2, hub.asked.size)
        assertEquals(2, marks("来自 飞书 /agent"))
        assertEquals(1, marks("来自扩展"))
        rule.runOnIdle { offline = false; openers.stale() } // e.g. an install elsewhere: the same list comes back
        rule.waitUntil(5_000) { marks("来自 Chat Bridge") == 1 && marks("来自 Example Docs") == 1 }
        assertEquals("the list read again", 2, hub.listReads)
        assertEquals("each icon asked again once", 4, hub.asked.size)
        assertEquals("the Lark extension's own /agent keeps Lark's", 1, marks("来自 飞书 /agent"))
    }

    private companion object {
        private const val Side = 96

        private fun png(draw: Canvas.(Paint) -> Unit): ByteArray {
            val bitmap = Bitmap.createBitmap(Side, Side, Bitmap.Config.ARGB_8888)
            Canvas(bitmap).draw(Paint(Paint.ANTI_ALIAS_FLAG))
            return ByteArrayOutputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it); it.toByteArray() }
        }

        /** A made-up chat bridge's icon: a white speech bubble on teal. */
        fun bubble() = png { paint ->
            paint.color = android.graphics.Color.rgb(0, 150, 136)
            drawRect(0f, 0f, Side.toFloat(), Side.toFloat(), paint)
            paint.color = android.graphics.Color.WHITE
            drawRoundRect(RectF(18f, 22f, 78f, 64f), 14f, 14f, paint)
            drawPath(Path().apply { moveTo(30f, 60f); lineTo(26f, 78f); lineTo(46f, 62f); close() }, paint)
        }

        /** A made-up docs extension's icon: a white page with lines on indigo. */
        fun page() = png { paint ->
            paint.color = android.graphics.Color.rgb(63, 81, 181)
            drawRect(0f, 0f, Side.toFloat(), Side.toFloat(), paint)
            paint.color = android.graphics.Color.WHITE
            drawRoundRect(RectF(26f, 16f, 70f, 80f), 6f, 6f, paint)
            paint.color = android.graphics.Color.rgb(63, 81, 181)
            for (y in listOf(32f, 44f, 56f)) drawRect(34f, y, 62f, y + 5f, paint)
        }
    }
}
