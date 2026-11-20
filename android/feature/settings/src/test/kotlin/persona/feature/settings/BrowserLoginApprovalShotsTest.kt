package persona.feature.settings

import android.graphics.Bitmap
import android.graphics.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.assertIsEnabled
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowDialog
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme

/** Host-side native Robolectric captures of every required request/number appearance (#10319). */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
class BrowserLoginApprovalShotsTest {
    @get:Rule val rule = createComposeRule()

    private enum class Scene { Lan, Internet, Number }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)
    private var revision by mutableIntStateOf(0)
    private var hub = ScriptedBrowserLoginHub()
    private val request = BrowserLoginRequest("a".repeat(40) + "b-_", "hub-1")

    private fun show() {
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background)) {
                    key(revision) { BrowserLoginApproval(hub, request, rememberCoroutineScope()) {} }
                }
            }
        }
    }

    private fun present(scene: Scene) {
        var allowed = false
        hub = ScriptedBrowserLoginHub { _, allow ->
            when (allow) {
                true -> {
                    allowed = true
                    browserLoginDetails(state = "confirm", number = "47")
                }
                false -> browserLoginDetails(state = "denied")
                null -> browserLoginDetails(
                    state = if (allowed) "confirm" else "scanned",
                    network = if (scene == Scene.Internet) "internet" else "lan",
                    number = if (allowed) "47" else null,
                )
            }
        }
        rule.runOnIdle { revision++ }
        rule.waitUntil(5_000) { rule.onAllNodesWithText("Chrome · Mac", substring = true).fetchSemanticsNodes().isNotEmpty() }
        when (scene) {
            Scene.Lan -> {
                rule.onNodeWithText("Your local network", substring = true).assertExists()
                rule.onNodeWithText("The name the browser gave itself", substring = true).assertExists()
            }
            Scene.Internet -> {
                rule.onNodeWithText("This request came from the internet. Only allow a browser you are signing in to yourself, right now.", substring = true).assertExists()
                rule.onNode(hasText("Allow sign-in") and hasClickAction()).assertIsEnabled()
            }
            Scene.Number -> {
                rule.onNode(hasText("Allow sign-in") and hasClickAction()).assertIsEnabled().performClick()
                rule.waitUntil(5_000) { rule.onAllNodesWithText("47").fetchSemanticsNodes().isNotEmpty() }
                rule.onNodeWithText("Type this number in the browser.", substring = true).assertExists()
                rule.onNodeWithText("Don't tell anyone this number.", substring = true).assertExists()
                rule.onNode(hasText("Cancel") and hasClickAction()).assertIsEnabled()
            }
        }
        rule.onNode(isDialog()).assertExists()
    }

    private fun scenes(vararg scenes: Scene, large: Boolean) {
        show()
        for (scene in scenes) {
            present(scene)
            for (nextStyle in AppStyle.entries) {
                var light: IntArray? = null
                for (nextDark in listOf(false, true)) {
                    rule.runOnIdle {
                        style = nextStyle
                        dark = nextDark
                    }
                    rule.waitForIdle()
                    val name = "browser-login-${scene.name.lowercase()}-${nextStyle.key}-${if (nextDark) "dark" else "light"}${if (large) "-large" else ""}"
                    light = captureDialog(name, light)
                }
            }
        }
    }

    @Test fun requestsAndNumberInEveryStyleAndAppearance() {
        scenes(Scene.Lan, Scene.Internet, Scene.Number, large = false)
    }

    @Config(qualifiers = "en-w400dp-h3000dp-xhdpi", fontScale = 2f)
    @Test fun requestsAndNumberAtTheLargestAccessibilityTextSize() {
        scenes(Scene.Lan, Scene.Internet, Scene.Number, large = true)
    }

    /** The same real dialog-window capture used by ExtensionMarketRemovalShotsTest. */
    private fun captureDialog(name: String, light: IntArray?): IntArray {
        lateinit var bitmap: Bitmap
        rule.runOnIdle {
            val decor = checkNotNull(ShadowDialog.getLatestDialog().window).decorView
            bitmap = Bitmap.createBitmap(decor.width, decor.height, Bitmap.Config.ARGB_8888)
            decor.draw(Canvas(bitmap))
        }
        assertTrue("capture has dimensions", bitmap.width > 0 && bitmap.height > 0)
        val pixels = IntArray(bitmap.width * bitmap.height)
        bitmap.getPixels(pixels, 0, bitmap.width, 0, 0, bitmap.width, bitmap.height)
        assertTrue("content rendered", pixels.indices.step(97).map { pixels[it] }.toSet().size > 20)
        if (light != null) assertFalse("light and dark differ", pixels.contentEquals(light))
        val output = File(System.getProperty("persona.shots") ?: "build/browser-login-shots").also { it.mkdirs() }
        File(output, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        return pixels
    }
}
