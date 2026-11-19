package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.LauncherModels
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** New Agent's All models list names the owning launcher on every row, including rows with no model detail. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class NewAgentModelSheetTest {
    @get:Rule val rule = createComposeRule()

    @Test fun everyModelRowNamesItsLauncherInLightAndDark() {
        val state = NewAgentState(
            machineId = "box",
            selection = AgentDefaults(launcher = "team-relay"),
            launchers = listOf(AgentLauncher("team-relay", displayName = "Team Relay")),
            models = NewAgentModels("box", "team-relay", LauncherModels("claude", listOf(AgentModel("claude-fable-5-2", "Fable")))),
        )
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                NewAgentModelSheet(state, onPick = {}, onRefresh = {}, onDismiss = {})
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/new-agent-launcher-shots").apply { mkdirs() }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.waitForIdle()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            File(out, "models-${if (night) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        rule.onNodeWithText("Team Relay · claude-fable-5-2").assertExists()
    }
}
