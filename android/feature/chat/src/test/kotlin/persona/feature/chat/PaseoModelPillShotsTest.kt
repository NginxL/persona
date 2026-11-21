package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyAncestor
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.ModelPillWidth
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import java.io.File

/**
 * Glass's model pill and panel in a Paseo provider's chat (#8143): the pill marks the agent as Grok, by its letter and
 * its name, and the panel uses the named providers where switching to another engine needs a new conversation. Light
 * and dark. PNGs in `build/paseo-model-pill-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1000dp-xhdpi")
class PaseoModelPillShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/paseo-model-pill-shots").also { it.mkdirs() }

    private val grok = Session(machineId = "macbook", sessionId = "paseo-agent-2", title = "Sort the photo albums", status = "idle", state = "idle",
        agent = "paseo", agentLabelHint = "Grok", launcher = "grok", model = "grok-5", effort = "high", sourceKind = "runtime", spaceKind = "paseo",
        capabilities = SessionCapabilities(read = true, send = true, interrupt = true, approve = true, modes = false, model = true))

    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction) = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Done
    }
    private val creation = object : AgentCreationService {
        override val scope = "test"
        override suspend fun launchers(machineId: String) =
            listOf(AgentLauncher("claude"), AgentLauncher("grok", agentType = "paseo"), AgentLauncher("traex", agentType = "paseo", displayName = "My CLI"))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path)
        override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean) = when (launcher) {
            "grok" -> LauncherModels("paseo", listOf(AgentModel("grok-5", "Grok 5", isDefault = true, efforts = listOf("low", "high")),
                AgentModel("grok-5-fast", "Grok 5 Fast")))
            "traex" -> LauncherModels("paseo", listOf(AgentModel("workspace-model", "Workspace model")))
            else -> LauncherModels("claude", listOf(AgentModel("sonnet", "Sonnet 4.5", isDefault = true)))
        }
    }

    private fun settle() {
        rule.waitForIdle()
        repeat(4) { rule.mainClock.advanceTimeByFrame(); rule.waitForIdle() }
    }

    private fun inPanel(text: String) = hasText(text) and hasAnyAncestor(isDialog())

    @Test fun thePillAndThePanelNameTheProvider() {
        var dark by mutableStateOf(false)
        var round by mutableIntStateOf(0)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                val scope = rememberCoroutineScope()
                val resources = androidx.compose.ui.platform.LocalResources.current
                val chrome = remember { ChatChromeState(resources, actions, scope, {}, {}) }
                key(round) {
                    Box(Modifier.testTag("shot").width(400.dp).height(120.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        AgentModelPill(grok, chrome, creation, online = true, width = ModelPillWidth.Wide, onSwitch = {}, onOverlay = {})
                    }
                }
            }
        }
        for (d in listOf(false, true)) {
            rule.runOnIdle { dark = d; round++ }
            rule.waitUntil(5_000) { rule.onAllNodesWithText("Grok 5", substring = true, useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
            settle()
            val look = if (d) "dark" else "light"
            // The pill's engine mark is the provider's letter, read as its name.
            assertTrue(rule.onAllNodesWithContentDescription("Grok", useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty())
            File(out(), "paseo-pill-$look.png").outputStream().use {
                assertTrue(rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
            rule.onNodeWithTag("model-pill").performClick()
            rule.waitUntil(5_000) { rule.onAllNodes(inPanel("Workspace model"), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
            settle()
            assertTrue(rule.onAllNodes(inPanel("Switching from Grok to Claude Code needs a new conversation"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty())
            assertTrue(rule.onAllNodes(inPanel("Switching from Grok to My CLI needs a new conversation"), useUnmergedTree = true)
                .fetchSemanticsNodes().isNotEmpty())
            rule.onAllNodesWithText("paseo", substring = true, ignoreCase = true, useUnmergedTree = true).assertCountEquals(0)
            rule.onAllNodesWithContentDescription("paseo", substring = true, ignoreCase = true, useUnmergedTree = true).assertCountEquals(0)
            File(out(), "paseo-panel-$look.png").outputStream().use {
                assertTrue(rule.onNode(isDialog()).captureToImage().asAndroidBitmap().compress(Bitmap.CompressFormat.PNG, 100, it))
            }
        }
    }
}
