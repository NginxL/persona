package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.longClick
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performTouchInput
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/**
 * Light/dark screenshots of the Markdown link long-press dropdown menu at default (1x) and 200% (2x)
 * accessibility text scale. Writes PNGs to `build/chat-link-menu-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "w400dp-h640dp-xxhdpi")
class ChatLinkMenuShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/chat-link-menu-shots").also { it.mkdirs() }

    private fun capture(name: String, dark: Boolean, fontScale: Float) {
        RuntimeEnvironment.setFontScale(fontScale)
        val markdown = "Check the [Persona release notes](https://example.com/releases/v1.0) or the [getting started guide](https://example.com/docs/start) for details."
        var openKey by mutableStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                ChatLinkHost(Modifier.size(400.dp, 640.dp).background(PersonaTheme.palette.background).testTag("host-$openKey")) {
                    Box(Modifier.fillMaxSize().padding(Tokens.Space.l)) {
                        Column {
                            ChatMarkdown(markdown, id = "shot-$name", selectable = false)
                        }
                    }
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodesWithText("https://", substring = true).fetchSemanticsNodes().isEmpty() }
        rule.waitForIdle()
        rule.onNodeWithText("Persona release notes", substring = true).performTouchInput {
            longClick(Offset(centerX, 24f))
        }
        rule.waitForIdle()
        rule.onNodeWithTag(CHAT_LINK_MENU_TAG).assertIsDisplayed()
        // Capture the root window so the Popup/DropdownMenu overlay is included in the screenshot.
        val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    @Test fun lightDefault() = capture("link-menu-light-default", dark = false, fontScale = 1.0f)

    @Test fun darkDefault() = capture("link-menu-dark-default", dark = true, fontScale = 1.0f)

    @Test fun lightLarge() = capture("link-menu-light-large", dark = false, fontScale = 2.0f)

    @Test fun darkLarge() = capture("link-menu-dark-large", dark = true, fontScale = 2.0f)
}
