package persona.android.ui

import android.app.Application
import android.graphics.Bitmap
import android.graphics.Rect
import android.view.View
import android.view.ViewGroup
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.unit.dp
import androidx.core.graphics.Insets
import androidx.core.view.ViewCompat
import androidx.core.view.WindowInsetsCompat
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import android.graphics.Color as AndroidColor
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import kotlin.math.abs
import org.junit.Before
import org.junit.Rule
import org.junit.rules.RuleChain
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowLooper
import persona.android.navigation.PaneSplits
import persona.android.navigation.sidebarSplit
import persona.core.contracts.Session
import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.AssistantRepository
import persona.core.contracts.api.AssistantSnapshot
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JPage
import persona.core.designsystem.JShellDrawer
import persona.core.designsystem.PersonaTheme
import persona.feature.assistant.AssistantSidebarContent
import java.io.File

/**
 * The real Assistant column at the foldable widths, Glass, Brief and Board, light and dark.
 * Colour blocks live in SidebarColumnDragShotsTest. These frames are the ones a review looks at.
 * #9803 also uses this harness for Persona-root resting shots and exclusion-rect checks.
 * The finger spring is the same in every style (`Theme.Split.settle`); the button path is not on this drag.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(application = Application::class, sdk = [35])
class ColumnDragRealShotsTest {
    val rule = createComposeRule()
    @get:Rule val host = RuleChain.outerRule(ComponentActivityHostRule()).around(rule)

    @Before fun hardwareSnapshots() = installColumnDragHardwareSnapshot(on = true)

    @After fun clearHardwareSnapshots() = installColumnDragHardwareSnapshot(on = false)

    @Config(qualifiers = "en-w673dp-h951dp-mdpi")
    @Test fun overFoldable() = frames(over = true, prefix = "over", windowDp = 673f, widthPx = 673, heightPx = 951)

    @Config(qualifiers = "en-w951dp-h673dp-mdpi")
    @Test fun besideFoldable() = frames(over = false, prefix = "beside", windowDp = 951f, widthPx = 951, heightPx = 673)

    /**
     * #9803 PR evidence: real phone shell at Persona root (drawer closed), every style, light and dark.
     * Publishes a leading systemGestureExclusionRects band; a pushed page publishes none.
     */
    @Config(qualifiers = "en-w411dp-h891dp-mdpi")
    @Test fun phonePersonaRootEdgeEvidence() {
        val evidence = evidenceDir()
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        var dragEnabled by mutableStateOf(true)
        lateinit var host: View
        rule.setContent {
            host = LocalView.current
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                val scope = rememberCoroutineScope()
                val (snapshot, repository, main) = assistantFixture()
                JShellDrawer(
                    opened = false,
                    onDismiss = {},
                    modifier = Modifier.testTag("shot").width(411.dp).height(891.dp),
                    dragEnabled = dragEnabled,
                    drawer = {
                        AssistantSidebarContent(
                            snapshot, repository, main.key, scope, demo = true,
                            onOpen = {}, onAction = {}, onSettings = {}, onInbox = {}, onKnowledge = {},
                            onTodos = null, todoCount = 0, showTodos = false, inboxCount = 0,
                            settingsCount = 0, settingsDot = false, settingsBadge = null,
                            pages = emptyList(), onPage = {}, onRetired = {},
                            fullWindowSearch = false,
                        )
                    },
                ) {
                    JPage("Assistant") { padding ->
                        Box(Modifier.fillMaxSize().padding(padding)) {
                            Text("The trip outline is ready.")
                        }
                    }
                }
            }
        }
        val insets = WindowInsetsCompat.Builder()
            .setInsets(WindowInsetsCompat.Type.systemGestures(), Insets.of(48, 0, 48, 0))
            .setInsets(WindowInsetsCompat.Type.mandatorySystemGestures(), Insets.of(48, 0, 48, 0))
            .build()
        rule.runOnIdle { ViewCompat.dispatchApplyWindowInsets(host, insets) }
        rule.waitForIdle()
        for (nextStyle in AppStyle.entries) {
        for (isDark in listOf(false, true)) {
            style = nextStyle
            dark = isDark
            dragEnabled = true
            rule.waitForIdle()
            rule.onNodeWithText("The trip outline is ready.").assertExists()
            val bitmap = saveEvidence(evidence, "real-phone-${nextStyle.key}-${if (isDark) "dark" else "light"}")
            assertEquals(411, bitmap.width)
            assertEquals(891, bitmap.height)
            assertTrue("phone root exclusion ${nextStyle.key} ${leadingExclusionRects(host)}", leadingExclusionRects(host).isNotEmpty())
        }
        }
        dragEnabled = false
        rule.waitForIdle()
        assertTrue("pushed page clears exclusion", leadingExclusionRects(host).isEmpty())
    }

    /**
     * #9803 PR evidence: real foldable shell at Persona root with the column hidden (the state that
     * publishes the exclusion), every style, light and dark. A pushed detail publishes none.
     */
    @Config(qualifiers = "en-w951dp-h673dp-mdpi")
    @Test fun foldablePersonaRootEdgeEvidence() {
        val evidence = evidenceDir()
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        var detailAtRoot by mutableStateOf(true)
        lateinit var host: View
        rule.setContent {
            host = LocalView.current
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                val scope = rememberCoroutineScope()
                val (snapshot, repository, main) = assistantFixture()
                AssistantPaneFrame(
                    regular = true, showSidebar = true, shown = false, overlay = false, drags = true,
                    detailAtRoot = detailAtRoot, paneShown = true, hinges = emptyList(),
                    modifier = Modifier.testTag("shot"),
                    sidebar = {
                        AssistantSidebarContent(
                            snapshot, repository, main.key, scope, demo = true,
                            onOpen = {}, onAction = {}, onSettings = {}, onInbox = {}, onKnowledge = {},
                            onTodos = null, todoCount = 0, showTodos = false, inboxCount = 0,
                            settingsCount = 0, settingsDot = false, settingsBadge = null,
                            pages = emptyList(), onPage = {}, onRetired = {},
                            fullWindowSearch = true,
                            onFoldColumn = { },
                        )
                    },
                ) {
                    JPage("Assistant") { padding ->
                        Box(Modifier.fillMaxSize().padding(padding)) {
                            Text("The trip outline is ready.")
                        }
                    }
                }
            }
        }
        rule.waitForIdle()
        for (nextStyle in AppStyle.entries) {
        for (isDark in listOf(false, true)) {
            style = nextStyle
            dark = isDark
            detailAtRoot = true
            rule.waitForIdle()
            rule.onNodeWithText("Weekend plans").assertDoesNotExist()
            rule.onNodeWithText("The trip outline is ready.").assertExists()
            val bitmap = saveEvidence(evidence, "real-foldable-951x673-${nextStyle.key}-${if (isDark) "dark" else "light"}")
            assertEquals(951, bitmap.width)
            assertEquals(673, bitmap.height)
            assertTrue("foldable root exclusion ${nextStyle.key} ${leadingExclusionRects(host)}", leadingExclusionRects(host).isNotEmpty())
        }
        }
        detailAtRoot = false
        rule.waitForIdle()
        assertTrue("pushed detail clears exclusion", leadingExclusionRects(host).isEmpty())
    }

    private fun assistantFixture(): Triple<AssistantSnapshot, AssistantRepository, Session> {
        val choice = AssistantManagerChoice.current.value
        val main = Session("computer", "assistant", title = "Main conversation", assistant = true)
        val recent = main.copy(sessionId = "recent", title = "Weekend plans", updatedAt = "2026-11-12T09:00:00Z")
        val snapshot = AssistantSnapshot(
            mainSession = main.key, conversations = listOf(main, recent),
            manager = choice.manager, managerId = choice.shown,
        )
        val repository = object : AssistantRepository {
            override val assistant = MutableStateFlow(snapshot)
            override suspend fun refresh() = Unit
        }
        return Triple(snapshot, repository, main)
    }

    private fun evidenceDir(): File =
        File(System.getenv("ROOT_EDGE_SHOTS") ?: "build/root-edge-shots").also { it.mkdirs() }

    private fun saveEvidence(out: File, name: String): Bitmap {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        println("wrote ${File(out, "$name.png").absolutePath} ${bitmap.width}x${bitmap.height}")
        return bitmap
    }

    private fun leadingExclusionRects(host: View): List<Rect> {
        val found = mutableListOf<Rect>()
        fun walk(view: View) {
            for (rect in view.systemGestureExclusionRects) {
                if (rect.width() > 0 && rect.left <= 80) found += Rect(rect)
            }
            if (view is ViewGroup) {
                for (i in 0 until view.childCount) walk(view.getChildAt(i))
            }
        }
        walk(host)
        walk(host.rootView)
        return found.distinctBy { "${it.left},${it.top},${it.right},${it.bottom}" }
    }

    private fun frames(over: Boolean, prefix: String, windowDp: Float, widthPx: Int, heightPx: Int) {
        val (snapshot, repository, main) = assistantFixture()
        var dark by mutableStateOf(false)
        var style by mutableStateOf(AppStyle.Glass)
        var shown by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = false) {
                val scope = rememberCoroutineScope()
                AssistantPaneFrame(
                    regular = true, showSidebar = true, shown = shown, overlay = over, drags = true,
                    detailAtRoot = true, hinges = emptyList(), onDragSettle = { shown = it },
                    modifier = Modifier.testTag("shot"),
                    sidebar = {
                        AssistantSidebarContent(
                            snapshot, repository, main.key, scope, demo = true,
                            onOpen = {}, onAction = {}, onSettings = {}, onInbox = {}, onKnowledge = {},
                            onTodos = null, todoCount = 0, showTodos = false, inboxCount = 0,
                            settingsCount = 0, settingsDot = false, settingsBadge = null,
                            pages = emptyList(), onPage = {}, onRetired = {},
                            fullWindowSearch = true,
                            onFoldColumn = if (over) null else ({ shown = false }),
                        )
                    },
                ) {
                    JPage("Assistant") { padding ->
                        Box(Modifier.fillMaxSize().padding(padding)) {
                            Text("The trip outline is ready.")
                        }
                    }
                }
            }
        }
        val out = File("build/column-drag-real").also { it.mkdirs() }
        println("column-drag-real ${out.absolutePath}")
        val columnDp = if (over) PaneSplits.DefaultLeft else sidebarSplit(windowDp, emptyList()).sidebar
        val columnPx = with(rule.density) { columnDp.dp.toPx() }
        for (nextStyle in AppStyle.entries) {
        for (isDark in listOf(false, true)) {
            val mode = if (isDark) "dark" else "light"
            val styleName = nextStyle.key
            rule.mainClock.autoAdvance = true
            rule.runOnIdle { style = nextStyle; dark = isDark; shown = false }
            rule.waitForIdle()
            rule.onNodeWithText("Weekend plans").assertDoesNotExist()
            rule.onNodeWithText("The trip outline is ready.").assertExists()
            val start = save(out, "$prefix-$styleName-$mode-start")
            assertEquals("$prefix $styleName $mode width", widthPx, start.width)
            assertEquals("$prefix $styleName $mode height", heightPx, start.height)
            val drag = columnDrag()
            assertEquals("Idle", drag.step())
            rule.runOnIdle { drag.begin(open = true, over = over, widthPx = columnPx) }
            var i = 0
            while (i < 24 && drag.step() != "Follow") {
                rule.onNodeWithTag("shot").captureToImage()
                rule.waitForIdle()
                ShadowLooper.idleMainLooper()
                i++
            }
            assertEquals("$prefix $styleName $mode stuck at ${drag.step()}", "Follow", drag.step())
            // The still of this frame, before the finger moves. The live capture above is the same layout.
            // A glass bar recorded without its blur is black here (`SplitColumnControl.swift:191`).
            val still = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            assertBarMatchesLive(start, still, "$prefix $styleName $mode")
            rule.runOnIdle { drag.move(columnPx / 2f) }
            rule.waitForIdle()
            rule.onNodeWithText("Weekend plans").assertExists()
            val mid = save(out, "$prefix-$styleName-$mode-mid")
            assertTrue("$prefix $styleName $mode mid matches start", !mid.sameAs(start))
            rule.mainClock.autoAdvance = false
            rule.runOnIdle {
                drag.end(2000f)
                Snapshot.sendApplyNotifications()
            }
            var frames = 0
            while (frames < 80 && drag.step() != "Fade") {
                rule.mainClock.advanceTimeByFrame()
                ShadowLooper.idleMainLooper()
                frames++
            }
            assertEquals("$prefix $styleName $mode did not release (${drag.step()})", "Fade", drag.step())
            save(out, "$prefix-$styleName-$mode-release")
            var fade = 0
            while (fade < 40 && drag.step() != "Idle") {
                rule.mainClock.advanceTimeByFrame()
                ShadowLooper.idleMainLooper()
                fade++
            }
            assertEquals("Idle", drag.step())
            assertTrue("$prefix $styleName $mode stayed shut", shown)
            rule.mainClock.autoAdvance = true
            rule.runOnIdle { drag.begin(open = false, over = over, widthPx = columnPx) }
            var shut = 0
            while (shut < 24 && drag.step() != "Follow") {
                rule.onNodeWithTag("shot").captureToImage()
                rule.waitForIdle()
                ShadowLooper.idleMainLooper()
                shut++
            }
            assertEquals("$prefix $styleName $mode shut stuck at ${drag.step()}", "Follow", drag.step())
            rule.runOnIdle { drag.move(-columnPx / 2f) }
            rule.waitForIdle()
            save(out, "$prefix-$styleName-$mode-shut")
            rule.mainClock.autoAdvance = false
            rule.runOnIdle {
                drag.end(-2000f)
                Snapshot.sendApplyNotifications()
            }
            var shutFrames = 0
            while (shutFrames < 80 && drag.step() != "Idle") {
                rule.mainClock.advanceTimeByFrame()
                ShadowLooper.idleMainLooper()
                shutFrames++
            }
            assertEquals("Idle", drag.step())
            assertTrue("$prefix $styleName $mode shut did not close", !shown)
        }
        }
    }

    /** Top bar of the rasterized still against the live capture of the same frame. */
    private fun assertBarMatchesLive(live: Bitmap, still: Bitmap, label: String) {
        val top = 8
        val bottom = 72.coerceAtMost(minOf(live.height, still.height))
        val left = 24
        val right = (minOf(live.width, still.width) - 24).coerceAtLeast(left + 1)
        var sum = 0.0
        var n = 0
        var black = 0
        for (y in top until bottom) {
            for (x in left until right) {
                val a = live.getPixel(x, y)
                val b = still.getPixel(x, y)
                sum += abs(AndroidColor.red(a) - AndroidColor.red(b))
                sum += abs(AndroidColor.green(a) - AndroidColor.green(b))
                sum += abs(AndroidColor.blue(a) - AndroidColor.blue(b))
                n++
                // The title glyphs are near-black in the live frame too. Count only still pixels that the live bar is not.
                val liveSum = AndroidColor.red(a) + AndroidColor.green(a) + AndroidColor.blue(a)
                val stillSum = AndroidColor.red(b) + AndroidColor.green(b) + AndroidColor.blue(b)
                if (stillSum < 24 && liveSum >= 24) black++
            }
        }
        val mean = sum / (n * 3)
        assertTrue("$label top bar mean channel delta $mean", mean < 20.0)
        if (!label.endsWith("dark")) {
            assertTrue("$label near-black pixels the live bar does not have: $black", black < n / 50)
        }
    }

    private fun save(out: File, name: String): Bitmap {
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        println("wrote ${File(out, "$name.png").absolutePath} ${bitmap.width}x${bitmap.height}")
        return bitmap
    }
}

/** The design-system controller is internal to its module. Tests drive the same begin / move / end the gesture uses. */
private class ColumnDragHandle(private val controller: Any) {
    private val type = controller.javaClass
    fun step(): String = type.getMethod("getStep").invoke(controller).toString()
    fun begin(open: Boolean, over: Boolean, widthPx: Float) {
        val zoneType = Class.forName("persona.core.designsystem.ColumnDragZone")
        val zone = zoneType.enumConstants.first { it.toString() == if (open) "Open" else "Close" }
        type.getMethod("begin", zoneType, java.lang.Boolean.TYPE, java.lang.Float.TYPE)
            .invoke(controller, zone, over, widthPx)
    }
    fun move(dxPx: Float) {
        type.getMethod("move", java.lang.Float.TYPE).invoke(controller, dxPx)
    }
    fun end(velocityPxPerSec: Float) {
        type.getMethod("end", java.lang.Float.TYPE).invoke(controller, velocityPxPerSec)
    }
}

private fun columnDrag(): ColumnDragHandle {
    val holder = Class.forName("persona.core.designsystem.SidebarColumnDragKt")
    val getter = holder.methods.first { it.name.startsWith("getColumnDragControllerForTest") && it.parameterCount == 0 }
    val controller = getter.invoke(null) ?: error("missing column drag controller")
    return ColumnDragHandle(controller)
}
