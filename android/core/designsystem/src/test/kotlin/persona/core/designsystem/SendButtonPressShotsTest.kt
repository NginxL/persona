package persona.core.designsystem

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.padding
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

/**
 * The composer's send pressed on Glass (#4210 T3, iOS `ComposerRoundButton(pressScales:)`): at rest, then 100 and 400 ms into
 * the press, then 100 ms after the finger lifts. On `main` it stays at rest through all four. PNGs in `build/send-press-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class SendButtonPressShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun frames(dark: Boolean) {
        rule.mainClock.autoAdvance = false
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = false, style = AppStyle.Glass) {
                Box(Modifier.testTag("shot").background(PersonaTheme.palette.background).padding(16.dp)) {
                    ComposerActionSlot(ComposerAction(SendKind.Send), {}, {}, Modifier.testTag("send"))
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/send-press-shots").also { it.mkdirs() }
        val mode = if (dark) "dark" else "light"
        fun capture(name: String) {
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "press-$mode-$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.mainClock.advanceTimeBy(500)
        capture("1-rest")
        rule.onNodeWithTag("send").performTouchInput { down(center) }
        rule.mainClock.advanceTimeBy(100)
        capture("2-pressed-100ms")
        rule.mainClock.advanceTimeBy(300)
        capture("3-pressed-400ms")
        rule.onNodeWithTag("send").performTouchInput { up() }
        rule.mainClock.advanceTimeBy(100)
        capture("4-released-100ms")
    }

    @Test fun light() = frames(dark = false)
    @Test fun dark() = frames(dark = true)
}
