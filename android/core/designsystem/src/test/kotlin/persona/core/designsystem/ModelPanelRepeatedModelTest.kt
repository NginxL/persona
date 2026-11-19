package persona.core.designsystem

import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.domain.models.PanelConfig
import persona.core.domain.models.PanelModel
import persona.core.domain.models.PanelPrefs
import persona.core.domain.models.PanelProvider
import persona.core.domain.models.PanelState

/**
 * A launcher whose list names a model twice opens the panel (#9998): Claude Code lists `default` twice when its
 * settings say `"model": "default"`, and two rows with one key made the list throw as it opened, closing the app.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ModelPanelRepeatedModelTest {
    @get:Rule val rule = createComposeRule()

    private val words = ModelPanelWords(reason = { "" }, effort = { it }, engine = { it }, note = { "" }, mark = { null })

    @Test fun aModelListedTwiceOpensWithOneRow() {
        val default = PanelModel("default", "Default (recommended)", resolved = "claude-opus-5-5", providerDefault = true)
        val claude = PanelProvider("claude", "claude", "claude",
            listOf(default, PanelModel("opus", "Opus"), PanelModel("sonnet", "Sonnet"), default.copy(name = "Default")))
        val state = PanelState(current = PanelConfig("claude", "default"), recommended = listOf("claude"))
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true) {
                JModelPanel(listOf(claude, claude), state, PanelPrefs(), {}, words, onPick = {}, onDismiss = {})
            }
        }
        rule.onNodeWithTag("model-panel-list").assertExists()
        assertEquals(1, rule.onAllNodesWithText("Default (recommended)").fetchSemanticsNodes().size)
    }
}
