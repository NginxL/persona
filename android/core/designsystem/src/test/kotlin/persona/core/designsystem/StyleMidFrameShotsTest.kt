package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.animation.AnimatedContent
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.graphics.toArgb
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File
import kotlin.math.abs

/**
 * One mid frame of a Brief push and a Board push, at 150 ms of the 300 ms curve, on a 1 ms frame clock.
 * The pages are flat colour, with no Glass chrome, so the same composition on the previous page motion
 * differs by no pixel. PNGs go to `build/style-mid-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-mdpi")
class StyleMidFrameShotsTest {
    @get:Rule val rule = createComposeRule()

    private val behind = Color(0xFFFF383C)
    private val front = Color(0xFF0088FF)

    @Test fun briefPushMidFrame() = mid(AppStyle.Brief, "brief-mid")

    @Test fun boardPushMidFrame() = mid(AppStyle.Board, "board-mid")

    private fun mid(style: AppStyle, name: String) {
        useOneMillisecondFrames()
        var page by mutableIntStateOf(0)
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = false, style = style) {
                val transitions = rememberPageTransitions()
                AnimatedContent(page, Modifier.fillMaxSize().testTag("stage"), transitionSpec = {
                    if (targetState > initialState) transitions.push else transitions.pop
                }) { key ->
                    Box(
                        Modifier.fillMaxSize().background(if (key == 0) behind else front).testTag("page-$key"),
                    )
                }
            }
        }
        rule.mainClock.advanceTimeBy(32)
        val start = beginTransition { page = 1 }
        rule.mainClock.advanceTimeBy(start + 150 - rule.mainClock.currentTime)
        val bitmap = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()
        val incoming = rule.onNodeWithTag("page-1").getUnclippedBoundsInRoot().left.value
        val outgoing = rule.onNodeWithTag("page-0").getUnclippedBoundsInRoot().left.value
        assertEquals("$style quarter", 400f + outgoing * 4, incoming, 4f)
        assertEquals("$style undimmed", behind.toArgb(), bitmap.getPixel(4, bitmap.height / 2))
        val out = File(System.getProperty("persona.shots") ?: "build/style-mid-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    /**
     * 1 ms frames need a patched clock. Compose UI test 1.12.1 always builds a 16 ms `TestMonotonicFrameClock`;
     * `effectContext` does not replace it, and `advanceTimeBy(1, ignoreFrameDuration = true)` does not dispatch a frame.
     * `frameDelayNanos` has no setter. See `GlassPageTransitionShotsTest`.
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

    private fun beginTransition(setPage: () -> Unit): Long {
        rule.runOnIdle { setPage() }
        Snapshot.sendApplyNotifications()
        // Brief and Board use FastOutSlowIn, whose first frames move by less than 1 px. Compare with the
        // first sample, not the previous frame, or a slow start never counts as motion.
        var origin: Float? = null
        var originClock = rule.mainClock.currentTime
        var last = origin
        var guard = 0
        while (guard++ < 120) {
            rule.mainClock.advanceTimeBy(1)
            val left = leftOf("page-1") ?: continue
            val start = origin
            last = left
            if (start == null) {
                origin = left
                originClock = rule.mainClock.currentTime
                continue
            }
            if (abs(left - start) <= 0.5f) {
                originClock = rule.mainClock.currentTime
                continue
            }
            return originClock
        }
        error("page did not move from $origin (last $last)")
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
}
