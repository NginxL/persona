package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.animation.AnimatedContent
import androidx.compose.animation.ContentTransform
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxHeight
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.offset
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.LocalLayoutDirection
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.IntOffset
import androidx.compose.ui.unit.LayoutDirection
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * Glass push and pop at 87, 175 and 262 ms, light and dark. Brief and Board keep the quarter-width slide and draw no dim.
 * PNGs go to `build/glass-page-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-mdpi")
class GlassPageTransitionShotsTest {
    @get:Rule val rule = createComposeRule()

    private val behind = Color(0xFFFF383C)
    private val front = Color(0xFF0088FF)
    private val marks = intArrayOf(87, 175, 262)

    @Test fun light() = frames(dark = false)
    @Test fun dark() = frames(dark = true)

    @Test fun briefPushStaysQuarterAndUndimmed() = quarterPush(AppStyle.Brief)

    @Test fun boardPushStaysQuarterAndUndimmed() = quarterPush(AppStyle.Board)

    private fun quarterPush(style: AppStyle) {
        val bitmap = midPush(style)
        val incoming = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        val outgoing = rule.onNodeWithTag("page-0").getUnclippedBoundsInRoot().left.value
        assertTrue("$style $incoming / $outgoing", incoming in 1f..399f && outgoing in -100f..-1f)
        assertEquals("$style quarter", 400f + outgoing * 4, incoming, 4f)
        assertEquals("$style dim", behind.toArgb(), bitmap.getPixel(4, bitmap.height / 2))
    }

    @Test fun glassPushStaysInsideTheClippedSlotBesideAColumn() {
        useOneMillisecondFrames()
        var page by mutableIntStateOf(0)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                Row(Modifier.fillMaxSize().testTag("window")) {
                    Box(Modifier.width(80.dp).fillMaxHeight().background(Color.Green))
                    Box(Modifier.weight(1f).fillMaxHeight().clipToBounds()) {
                        val transitions = rememberPageTransitions()
                        AnimatedContent(page, Modifier.fillMaxSize(), transitionSpec = {
                            if (targetState > initialState) transitions.push else transitions.pop
                        }) { key ->
                            Box(
                                Modifier.fillMaxSize().background(if (key == 0) behind else front)
                                    .glassPageChrome().testTag("page-$key"),
                            )
                        }
                    }
                }
            }
        }
        rule.mainClock.advanceTimeBy(32)
        val rest = rule.onNodeWithTag("window").captureToImage().asAndroidBitmap()
        assertEquals(behind.toArgb(), rest.getPixel(100, rest.height / 2))
        val start = beginTransition { page = 1 }
        rule.mainClock.advanceTimeBy(start + 175 - rule.mainClock.currentTime)
        val bitmap = rule.onNodeWithTag("window").captureToImage().asAndroidBitmap()
        val y = bitmap.height / 2
        // The column's right side is where an unclipped slide would paint. It stays the column's green.
        assertEquals(Color.Green.toArgb(), bitmap.getPixel(50, y))
        val left = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        assertTrue("front still entering, left=$left", left > 80f)
        val onPage = (left.roundToInt() - 90).coerceAtLeast(84)
        val pixel = bitmap.getPixel(onPage, y)
        assertTrue("dimmed behind $pixel at $onPage", pixel != behind.toArgb() && pixel != front.toArgb() && pixel != Color.Green.toArgb())
    }

    private fun frames(dark: Boolean) {
        useOneMillisecondFrames()
        val mode = if (dark) "dark" else "light"
        var page by mutableIntStateOf(0)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                val transitions = rememberPageTransitions()
                AnimatedContent(page, Modifier.fillMaxSize().testTag("stage"), transitionSpec = {
                    if (targetState > initialState) transitions.push else transitions.pop
                }) { key ->
                    Box(
                        Modifier.fillMaxSize().background(if (key == 0) behind else front)
                            .glassPageChrome().testTag("page-$key"),
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(32)
        val resting = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
        assertSolid(resting, behind.toArgb(), "$mode rest")
        val notes = StringBuilder()
        runTransition(to = 1, kind = "push", mode = mode, notes = notes) { page = it }
        val settled = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
        assertSolid(settled, front.toArgb(), "$mode settled")
        runTransition(to = 0, kind = "pop", mode = mode, notes = notes) { page = it }
        val returned = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
        assertSolid(returned, behind.toArgb(), "$mode returned")
        File(out(), "measured-$mode.txt").writeText(notes.toString())
    }

    private fun runTransition(to: Int, kind: String, mode: String, notes: StringBuilder, setPage: (Int) -> Unit) {
        // advanceTimeBy rounds up to a 16 ms frame and the tween samples one frame later, so a plain
        // advanceTimeBy(87) lands near 64 ms (left 184, not 132). A 1 ms frame delay hits the mark.
        val start = beginTransition { setPage(to) }
        val width = 400f
        for (ms in marks) {
            rule.mainClock.advanceTimeBy(start + ms - rule.mainClock.currentTime)
            val elapsed = rule.mainClock.currentTime - start
            val y = PageTransitionTokens.glassEasing.transform(ms / 350f)
            val bitmap = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
            val frontLeft = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
            val expected = if (kind == "push") width * (1f - y) else width * y
            notes.append("$mode $kind $ms elapsed=$elapsed left=$frontLeft expected=$expected\n")
            assertEquals("$mode $kind elapsed", ms.toLong(), elapsed)
            assertEquals("$mode $kind@$ms left=$frontLeft", expected, frontLeft, 8f)
            val midY = bitmap.height / 2
            val edge = frontLeft.roundToInt()
            val behindX = (edge - 8).coerceIn(1, bitmap.width - 2)
            val frontX = (edge + 24).coerceIn(1, bitmap.width - 2)
            val behindPixel = bitmap.getPixel(behindX, midY)
            val frontPixel = bitmap.getPixel(frontX, midY)
            assertEquals("$mode $kind@$ms front", front.toArgb(), frontPixel)
            assertTrue("$mode $kind@$ms dim $behindPixel", behindPixel != behind.toArgb() && behindPixel != front.toArgb())
            val coverage = (1f - frontLeft / width).coerceIn(0f, 1f)
            // The shadow strip is about 2×12 dp. A sample inside it is darker than the dim alone.
            if (edge - 4 > 24 + 2) assertDimmed(bitmap.getPixel(4, midY), coverage, "$mode $kind@$ms")
            val far = bitmap.getPixel(4, midY)
            if (edge > 24) {
                val nearRed = android.graphics.Color.red(behindPixel)
                val farRed = android.graphics.Color.red(far)
                assertTrue("$mode $kind@$ms shadow near=$nearRed far=$farRed edge=$edge", nearRed < farRed)
            }
            shoot(bitmap, "android-$mode-$kind", ms)
        }
        rule.mainClock.advanceTimeBy(start + 400 - rule.mainClock.currentTime)
        if (kind == "push") rule.onNodeWithTag("page-0").assertDoesNotExist()
        else rule.onNodeWithTag("page-1").assertDoesNotExist()
    }

    private fun midPush(style: AppStyle): Bitmap {
        var page by mutableIntStateOf(0)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = style) {
                val transitions = rememberPageTransitions()
                AnimatedContent(page, Modifier.fillMaxSize().testTag("stage"), transitionSpec = {
                    if (targetState > initialState) transitions.push else transitions.pop
                }) { key ->
                    Box(Modifier.fillMaxSize().background(if (key == 0) behind else front).testTag("page-$key"))
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        rule.runOnIdle { page = 1 }
        Snapshot.sendApplyNotifications()
        repeat(2) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(80)
        return rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
    }

    @Test fun rtlPushMidFrameMirrors() {
        useOneMillisecondFrames()
        var page by mutableIntStateOf(0)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            CompositionLocalProvider(LocalLayoutDirection provides LayoutDirection.Rtl) {
                PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                    val transitions = rememberPageTransitions()
                    AnimatedContent(page, Modifier.fillMaxSize().testTag("stage"), transitionSpec = {
                        if (targetState > initialState) transitions.push else transitions.pop
                    }) { key ->
                        Box(
                            Modifier.fillMaxSize().background(if (key == 0) behind else front)
                                .glassPageChrome().testTag("page-$key"),
                        )
                    }
                }
            }
        }
        rule.mainClock.advanceTimeBy(32)
        val start = beginTransition { page = 1 }
        val ms = 175
        rule.mainClock.advanceTimeBy(start + ms - rule.mainClock.currentTime)
        val y = PageTransitionTokens.glassEasing.transform(ms / 350f)
        val left = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        assertEquals("rtl push left=$left", -400f * (1f - y), left, 8f)
        shoot(rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap(), "android-light-rtl-push", ms)
    }

    @Test fun predictivePopMidFrameIsLinear() {
        useOneMillisecondFrames()
        var page by mutableIntStateOf(1)
        rule.mainClock.autoAdvance = false
        val stack = TransitionZ()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                val transitions = rememberPageTransitions()
                AnimatedContent(page, Modifier.fillMaxSize().testTag("stage"), transitionSpec = {
                    val base = if (targetState > initialState) transitions.push else transitions.predictivePop
                    stack.wrap(base, targetState, initialState)
                }) { key ->
                    Box(
                        Modifier.fillMaxSize().background(if (key == 0) behind else front)
                            .glassPageChrome().testTag("page-$key"),
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(32)
        val start = beginTransition { page = 0 }
        val ms = 87
        rule.mainClock.advanceTimeBy(start + ms - rule.mainClock.currentTime)
        val left = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        val linear = 400f * (ms / 350f)
        assertEquals("predictive left=$left linear=$linear", linear, left, 8f)
        val bitmap = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
        val midY = bitmap.height / 2
        val edge = left.roundToInt()
        val frontX = (edge + 24).coerceIn(1, bitmap.width - 2)
        assertEquals("predictive front", front.toArgb(), bitmap.getPixel(frontX, midY))
        if (edge - 4 > 24 + 2) assertDimmed(bitmap.getPixel(4, midY), 1f - left / 400f, "predictive")
        shoot(bitmap, "android-light-predictive", ms)
    }

    @Test fun pushDuringPopKeepsThePushCurve() {
        useOneMillisecondFrames()
        var page by mutableIntStateOf(1)
        rule.mainClock.autoAdvance = false
        val stack = TransitionZ()
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                val transitions = rememberPageTransitions()
                AnimatedContent(page, Modifier.fillMaxSize().testTag("stage"), transitionSpec = {
                    val base = if (targetState > initialState) transitions.push else transitions.pop
                    stack.wrap(base, targetState, initialState)
                }) { key ->
                    Box(
                        Modifier.fillMaxSize().background(if (key == 0) behind else front)
                            .glassPageChrome().testTag("page-$key"),
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(32)
        val popStart = beginTransition { page = 0 }
        val ms = 87
        rule.mainClock.advanceTimeBy(popStart + ms - rule.mainClock.currentTime)
        val y = PageTransitionTokens.glassEasing.transform(ms / 350f)
        val popLeft = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        assertEquals("pop before the interrupt left=$popLeft", 400f * y, popLeft, 8f)
        val pushStart = beginTransition { page = 1 }
        rule.mainClock.advanceTimeBy(pushStart + ms - rule.mainClock.currentTime)
        val left = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        // The new push tween starts from the offset the pop had reached. It does not restart from the off-screen edge,
        // so this is not 400 × (1 − y). The page has to be on its way back, with the page behind still there.
        assertTrue("interrupted push came back from $popLeft to $left", left < popLeft - 8f)
        assertTrue("interrupted push still in flight left=$left", left > 8f)
        rule.onNodeWithTag("page-0").assertExists()
        val bitmap = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
        val frontX = (left.roundToInt() + 24).coerceIn(1, bitmap.width - 2)
        assertEquals("interrupted front", front.toArgb(), bitmap.getPixel(frontX, bitmap.height / 2))
        shoot(bitmap, "android-light-interrupt", ms)
    }

    /**
     * NavDisplay sets a pop's incoming scene to `initialZ - 1` and leaves the page already showing
     * at the z it entered with. AnimatedContent stamps [ContentTransform.targetContentZIndex] once.
     * A spec that always says -1 stamps the first page at -1 too, then the incoming page is placed
     * last and covers the card. This steps z the way NavDisplay does, once per target.
     */
    private class TransitionZ {
        private var z = 0f
        private var stamped: Int? = null

        fun wrap(base: ContentTransform, target: Int, initial: Int): ContentTransform {
            if (target != initial && stamped != target) {
                z = if (target < initial) z - 1f else z + 1f
                stamped = target
            }
            return ContentTransform(
                base.targetContentEnter,
                base.initialContentExit,
                if (target == initial) 0f else z,
                sizeTransform = null,
            )
        }
    }

    @Test fun nestedChromeDimsOnce() {
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize()) {
                    // The dim is drawn by the outer chrome, over the inner page. Sampling the inner node would miss it.
                    Box(Modifier.fillMaxSize().offset { IntOffset(-80, 0) }.glassPageChrome().testTag("outer")) {
                        Box(Modifier.fillMaxSize().background(behind).glassPageChrome())
                    }
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        val bitmap = rule.onNodeWithTag("outer").captureToImage().asAndroidBitmap()
        // 80 / round(0.3 × 400) = 80/120. One dim is 0.10 × that. Two dims would land near 222, not 238.
        // x=120 sits in the dimmed page and outside the 24 px shadow strip (the edge is near 213).
        assertDimmed(bitmap.getPixel(120, bitmap.height / 2), 80f / 120f, "nested")
    }

    @Test fun openDrawerLeavesNoDimInEitherDirection() {
        rule.mainClock.autoAdvance = false
        var direction by mutableStateOf(LayoutDirection.Ltr)
        var pad by mutableIntStateOf(0)
        rule.setContent {
            CompositionLocalProvider(LocalLayoutDirection provides direction) {
                PersonaTheme(darkTheme = false, reduceMotion = false, style = AppStyle.Glass) {
                    Box(Modifier.fillMaxSize().graphicsLayer { translationX = 180f }) {
                        Box(
                            Modifier.fillMaxSize().padding(top = pad.dp).background(behind)
                                .glassPageChrome().testTag("page"),
                        )
                    }
                }
            }
        }
        rule.mainClock.advanceTimeByFrame()
        for (rtl in listOf(false, true)) {
            rule.runOnIdle {
                direction = if (rtl) LayoutDirection.Rtl else LayoutDirection.Ltr
                pad = 1
            }
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            rule.runOnIdle { pad = 0 }
            Snapshot.sendApplyNotifications()
            rule.mainClock.advanceTimeByFrame()
            val bitmap = rule.onNodeWithTag("page").captureToImage().asAndroidBitmap()
            val label = if (rtl) "rtl" else "ltr"
            assertEquals("$label drawer dim", behind.toArgb(), bitmap.getPixel(bitmap.width / 2, bitmap.height / 2))
        }
    }

    /**
     * Compose UI test 1.12.1 builds its own [androidx.compose.ui.test.TestMonotonicFrameClock] at 16 ms
     * (`AndroidComposeUiTestEnvironment` passes the default delay and does not install a clock from `effectContext`).
     * [androidx.compose.ui.test.MainTestClock.advanceTimeBy] with `ignoreFrameDuration` only skips the ceil; `withFrameNanos`
     * still waits `frameDelayMillis`, so a 1 ms step does not sample the tween and an 87 ms jump lands on the 80 ms frame.
     * `frameDelayNanos` is a final val with no setter. Patching it is the only way to hit 87, 175 and 262.
     */
    private fun useOneMillisecondFrames() {
        rule.mainClock.autoAdvance = false
        val environment = declaredField(rule.javaClass, "environment").get(rule)
            ?: error("compose test environment is not ready")
        val frameClock = declaredField(environment.javaClass, "frameClock").get(environment)
            ?: error("frame clock is missing")
        putLong(frameClock, "frameDelayNanos", 1_000_000L)
        putLong(rule.mainClock, "frameDelayMillis", 1L)
    }

    /** Clock time of the frame where page-1 is still at its initial offset. The next frame is the first motion. */
    private fun beginTransition(setPage: () -> Unit): Long {
        rule.runOnIdle { setPage() }
        Snapshot.sendApplyNotifications()
        var pose: Float? = null
        var poseClock = rule.mainClock.currentTime
        var guard = 0
        while (guard++ < 40) {
            rule.mainClock.advanceTimeBy(1)
            val left = leftOf("page-1") ?: continue
            val previous = pose
            if (previous == null || abs(left - previous) <= 1f) {
                pose = left
                poseClock = rule.mainClock.currentTime
                continue
            }
            return poseClock
        }
        error("page did not move from $pose")
    }

    private fun leftOf(tag: String): Float? {
        if (rule.onAllNodesWithTag(tag).fetchSemanticsNodes().isEmpty()) return null
        return rule.onNodeWithTag(tag).getUnclippedBoundsInRoot().left.value
    }

    private fun declaredField(start: Class<*>, name: String): java.lang.reflect.Field {
        var type: Class<*>? = start
        while (type != null) {
            try {
                return type.getDeclaredField(name).apply { isAccessible = true }
            } catch (_: NoSuchFieldException) {
                type = type.superclass
            }
        }
        error("no field $name on ${start.name}")
    }

    private fun putLong(target: Any, name: String, value: Long) {
        val field = declaredField(target.javaClass, name)
        val unsafeClass = Class.forName("sun.misc.Unsafe")
        val unsafe = unsafeClass.getDeclaredField("theUnsafe").apply { isAccessible = true }.get(null)
        val offset = unsafeClass.getMethod("objectFieldOffset", java.lang.reflect.Field::class.java).invoke(unsafe, field) as Long
        unsafeClass.getMethod("putLong", Any::class.java, java.lang.Long.TYPE, java.lang.Long.TYPE)
            .invoke(unsafe, target, offset, value)
    }

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/glass-page-shots").also { it.mkdirs() }

    private fun shoot(bitmap: Bitmap, prefix: String, ms: Int) {
        val name = "$prefix-${ms.toString().padStart(3, '0')}.png"
        File(out(), name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    /** Behind rgb(255, 56, 60) darkened by `0.10 × coverage`. Five levels covers rounding and the gradient's tail. */
    private fun assertDimmed(pixel: Int, coverage: Float, label: String) {
        val scale = 1f - PageTransitionTokens.dimAlpha(coverage)
        for ((name, channel, shift) in listOf(Triple("r", 255, 16), Triple("g", 56, 8), Triple("b", 60, 0))) {
            val actual = (pixel shr shift) and 0xFF
            assertEquals("$label $name pixel=$pixel", (channel * scale).roundToInt().toFloat(), actual.toFloat(), 5f)
        }
    }

    private fun assertSolid(bitmap: Bitmap, color: Int, label: String) {
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        val bad = pixels.indexOfFirst { it != color }
        assertEquals("$label pixel $bad of ${bitmap.width}x${bitmap.height}", -1, bad)
    }
}
