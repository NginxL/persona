package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.launcherModel
import persona.core.designsystem.*
import persona.feature.chat.ChatComposer
import persona.feature.chat.rememberChatComposerState
import java.io.File

/** The home composer's "Launcher" chip (#2976, iOS StarterComposer): what a new conversation starts with, and the pick. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class StarterLauncherShotsTest {
    @get:Rule val rule = createComposeRule()

    private val launchers = listOf(AgentLauncher("ccc", model = "claude-opus-5-5"), AgentLauncher("sol", model = "gateway/gpt-6.1-sol"),
        AgentLauncher("cx", agentType = "codex"))

    @Test fun launcherModelsAreShortAndOldDefaultPlaceholdersAreAbsent() {
        assertEquals("gpt-6.1-sol", launcherModel(launchers[1]))
        assertEquals(null, launcherModel(launchers[2]))
        assertEquals(null, launcherModel(AgentLauncher("old", model = "默认")))
    }

    @Test fun theChipInEveryStyleAndAppearance() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var picked by mutableStateOf<String?>(null)
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Column(Modifier.testTag("shot").width(400.dp).height(360.dp).background(PersonaTheme.palette.background)) {
                        Spacer(Modifier.weight(1f))
                        ChatComposer(SessionKey("box", "pending"), rememberChatComposerState(), working = false, sending = false,
                            canClear = false, service = null, onSend = {}, onCommand = {}, onNotice = {}, placeholder = "问 Persona…",
                            accessory = { StarterLauncherChip(picked, "ccc", launchers, failed = false, enabled = true) { picked = it } })
                    }
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/starter-launcher-shots").apply { mkdirs() }
        for (p in listOf(null, "sol")) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            picked = p; style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText(if (p == null) "默认：ccc" else "sol").assertExists()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "launcher-${p ?: "default"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        // The list: the default, then the launchers with their models; picking one marks the chip for this time only.
        picked = null; style = AppStyle.Glass; dark = false
        rule.onNodeWithText("默认：ccc").performClick()
        rule.onNodeWithText("新对话使用").assertExists()
        rule.onNodeWithText("sol · gpt-6.1-sol").performClick()
        rule.waitForIdle()
        assertEquals("sol", picked)
    }
}
