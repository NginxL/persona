package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.width
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/**
 * A wide and a small Glass [JButton] at rest and held at the press plateau, light and dark.
 * The plateau is 1.2 s into the 270 ms press spring, past the overshoot. PNGs go to
 * `build/glass-press-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp-mdpi")
class GlassPressShotsTest {
    @get:Rule val rule = createComposeRule()

    @Test fun wideLight() = shot(dark = false, wide = true)

    @Test fun wideDark() = shot(dark = true, wide = true)

    @Test fun smallLight() = shot(dark = false, wide = false)

    @Test fun smallDark() = shot(dark = true, wide = false)

    private fun shot(dark: Boolean, wide: Boolean) {
        useOneMillisecondFrames()
        rule.mainClock.autoAdvance = false
        val mode = if (dark) "dark" else "light"
        val size = if (wide) "wide" else "small"
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                Box(
                    Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("stage"),
                    contentAlignment = Alignment.Center,
                ) {
                    JButton(
                        if (wide) "Continue" else "Go",
                        {},
                        Modifier.then(if (wide) Modifier.width(300.dp) else Modifier).testTag("button"),
                        size = if (wide) JButtonSize.Large else JButtonSize.Small,
                    )
                }
            }
        }
        // One composition per case. Changing size inside a running test kept the previous button through the rest frame.
        rule.mainClock.advanceTimeBy(80)
        val out = File(System.getProperty("persona.shots") ?: "build/glass-press-shots").also { it.mkdirs() }
        val rest = capture()
        save(out, rest, "$size-$mode-rest")
        rule.onNodeWithTag("button").performTouchInput { down(center) }
        rule.mainClock.advanceTimeBy(1_200)
        val held = capture()
        save(out, held, "$size-$mode-held")
        assertTrue(
            "$size $mode grew ${span(rest)} -> ${span(held)}",
            span(held) > span(rest) + 4,
        )
    }

    private fun capture(): Bitmap = rule.onNodeWithTag("stage").captureToImage().asAndroidBitmap()

    private fun save(out: File, bitmap: Bitmap, name: String) {
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    /** Width of pixels that are not the page background. */
    private fun span(bitmap: Bitmap): Int {
        val background = bitmap.getPixel(0, 0)
        var minX = bitmap.width
        var maxX = -1
        for (y in 0 until bitmap.height step 2) {
            for (x in 0 until bitmap.width) {
                if (bitmap.getPixel(x, y) != background) {
                    if (x < minX) minX = x
                    if (x > maxX) maxX = x
                }
            }
        }
        return if (maxX < minX) 0 else maxX - minX
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
