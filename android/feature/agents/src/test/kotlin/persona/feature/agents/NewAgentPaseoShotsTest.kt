package persona.feature.agents

import androidx.activity.ComponentActivity
import androidx.compose.runtime.*
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.lifecycle.viewModelScope
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.*
import java.io.File

/**
 * New Agent on a computer whose engine is Paseo (#8143): Paseo's providers other than Claude Code and Codex are one agent
 * type, "Other agents", each listed by its own name with no "paseo" under it, and a provider's model list says whose
 * models they are without the terminal's /model. Every style, light and dark. PNGs in `build/new-agent-paseo-shots/`
 * (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class NewAgentPaseoShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val computers = listOf(Machine("computer", "Studio", online = true, engine = "paseo"))
    private val launchers = listOf(
        AgentLauncher("claude"), AgentLauncher("codex", agentType = "codex", promptOptional = true),
        AgentLauncher("grok", model = "Grok 5", agentType = PaseoAgent), AgentLauncher("kimi", model = "Kimi K3", agentType = PaseoAgent),
        AgentLauncher("trae-cli", agentType = "trae-cli"))
    private val service = object : AgentCreationService {
        override val scope = "hub/pairing"
        override suspend fun launchers(machineId: String) = launchers
        override suspend fun directories(machineId: String, path: String) = AgentDirectories("/code/project")
        override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
    }
    private val store = object : AgentCreationStore {
        override suspend fun lastMachine(scope: String) = "computer"
        override suspend fun defaults(scope: String, machine: String) = AgentDefaults(cwd = "/code/project", launcher = "grok")
        override suspend fun saveDefaults(scope: String, machine: String, defaults: AgentDefaults) = Unit
        override suspend fun pending(scope: String): AgentStartOperation? = null
        override suspend fun pending(scope: String, value: AgentStartOperation?) = Unit
    }

    @Test fun paseosOtherProvidersAreOneTypeEachByItsOwnName() {
        val model = NewAgentViewModel(service, store, MutableStateFlow(computers))
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                NewAgentPage(model, computers, online = true, onBack = {})
            }
        }
        try {
            rule.waitUntil(10_000) { model.state.value.launchers.isNotEmpty() && !model.state.value.loading }
            rule.runOnIdle { model.selection(model.state.value.selection.copy(launcher = "grok")) }
            assertEquals(PaseoAgent, model.state.value.agentType)
            val other = rule.activity.getString(R.string.agents_new_type_other)
            val out = File(System.getProperty("persona.shots") ?: "build/new-agent-paseo-shots").also { it.mkdirs() }
            for (s in AppStyle.entries) for (d in listOf(false, true)) {
                rule.runOnIdle { style = s; dark = d }
                rule.waitForIdle()
                rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText(other))
                rule.onNodeWithText(other).assertIsDisplayed()
                rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("TRAE CLI"))
                rule.onNodeWithText("TRAE CLI").assertIsDisplayed()
                rule.onNode(hasScrollToNodeAction()).performScrollToNode(hasText("kimi"))
                rule.onNodeWithText("grok").assertIsDisplayed()
                // The engine's id is nowhere: not the type, not under a provider.
                rule.onAllNodesWithText("paseo", substring = true, ignoreCase = true, useUnmergedTree = true).assertCountEquals(0)
                rule.onRoot().captureToImage().asAndroidBitmap().let { bitmap ->
                    out.resolve("new-agent-paseo-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                        bitmap.compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it)
                    }
                }
            }
        } finally { model.viewModelScope.cancel() }
    }

    @Test fun aProvidersModelListSaysWhoseModelsTheyAre() {
        val models = LauncherModels(PaseoAgent, listOf(AgentModel("grok-5", "Grok 5", isDefault = true), AgentModel("grok-5-fast", "Grok 5 Fast")))
        val state = NewAgentState(initialized = true, machineId = "computer", selection = AgentDefaults(cwd = "/code/project", launcher = "grok"),
            launchers = launchers, models = NewAgentModels("computer", "grok", models))
        rule.setContent {
            PersonaTheme(reduceMotion = true) { NewAgentModelSheet(state, onPick = {}, onRefresh = {}, onDismiss = {}) }
        }
        rule.onNodeWithText("The models “grok” offers. Only for this start.", useUnmergedTree = true).assertExists()
        rule.onAllNodesWithText("/model", substring = true, useUnmergedTree = true).assertCountEquals(0)
        // A launcher running Claude Code keeps its note.
        assertNull(launcherAgentLine(launchers[2]))
        assertEquals("Claude Code", launcherAgentLine(launchers[0]))
        assertEquals("Codex", launcherAgentLine(launchers[1]))
        assertEquals("TRAE CLI", launcherAgentLine(launchers.last()))
    }
}
