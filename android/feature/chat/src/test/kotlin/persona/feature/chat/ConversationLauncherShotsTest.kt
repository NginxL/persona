package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.io.File

/** The real existing-chat chip and sheet: assistant-only choices, model pick, scope wording and clearing it. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class ConversationLauncherShotsTest {
    @get:Rule val rule = createComposeRule()

    private val sharedCase = JsonCodec.parseToJsonElement(
        File(checkNotNull(System.getProperty("persona.repo.root")), "spec/fixtures/assistant-launchers/cases.json").readText(),
    ).jsonObject.getValue("cases").jsonArray.single().jsonObject
    private val sharedCandidates = sharedCase.getValue("candidates").jsonArray.map {
        JsonCodec.decodeFromJsonElement<AgentLauncher>(it)
    }
    private val sharedAssistantNames = sharedCase.getValue("assistant").jsonArray.map { it.jsonPrimitive.content }

    private val management = object : AssistantManagement {
        override suspend fun config() = AssistantConfig(machineId = "box", launcher = "claude", main = "main")
        override suspend fun assistantLaunchers(machineId: String) = sharedCandidates.filter { it.name in sharedAssistantNames }
        override suspend fun launcherModels(machineId: String, launcher: String) = LauncherModels(agent = "claude",
            models = listOf(AgentModel("default", name = "Default", isDefault = true), AgentModel("opus", name = "Opus")))
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String) = SessionKey("box", "main")
        override suspend fun freshTask(id: String) = ScheduledTask(id)
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    @Test fun choicesAndScopeInEveryStyleAndAppearance() = runScene(false)

    @Test @Config(fontScale = 2f)
    fun largeTextInEveryStyleAndAppearance() = runScene(true)

    @Test fun restoredModelUsesItsDisplayName() {
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ConversationLauncherChip(management, "box", "main", "old-launcher", "runtime", "opus") { _, _ -> }
            }
        }
        rule.onNodeWithText("runtime · opus").assertDoesNotExist()
        rule.waitUntil(5_000) {
            rule.onAllNodes(androidx.compose.ui.test.hasText("runtime · Opus")).fetchSemanticsNodes().isNotEmpty()
        }
    }

    @Test fun aSavedAliasSelectsAndNamesTheFoldedRow() {
        val folded = AgentModel("claude-opus-5-5", "Opus 5.5", aliases = listOf("opus", "fable"))
        val canonical = AgentModel("opus", "Canonical Opus")
        assertEquals(folded, listedAgentModel(listOf(folded), "OPUS"))
        assertEquals(canonical, listedAgentModel(listOf(folded, canonical), "opus"))
        val foldedManagement = object : AssistantManagement by management {
            override suspend fun launcherModels(machineId: String, launcher: String) = LauncherModels(agent = "claude",
                models = listOf(AgentModel("default", name = "Default", isDefault = true), folded))
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Board) {
                ConversationLauncherChip(foldedManagement, "box", "main", "old-launcher", "runtime", "opus") { _, _ -> }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodes(androidx.compose.ui.test.hasText("runtime · Opus 5.5")).fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun theModelRouteRemedyOpensTheSameBriefAndBoardPicker() {
        var open by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Brief) {
                ConversationLauncherChip(management, "box", "main", "old-launcher", "runtime", "opus",
                    openRequested = open, onOpenHandled = { open = false }) { _, _ -> }
            }
        }
        rule.runOnIdle { open = true }
        rule.waitUntil(5_000) { rule.onAllNodes(hasDialogText("Opus")).fetchSemanticsNodes().isNotEmpty() }
        assertEquals(false, open)
    }

    private fun hasDialogText(text: String) = androidx.compose.ui.test.hasText(text) and
        androidx.compose.ui.test.hasAnyAncestor(isDialog())

    private fun runScene(largeText: Boolean) {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var launcher by mutableStateOf<String?>(null)
        var model by mutableStateOf<String?>(null)
        rule.setContent {
            val configuration = Configuration(LocalConfiguration.current).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(LocalConfiguration provides configuration) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("shot")) {
                        Box(Modifier.align(Alignment.BottomCenter)) {
                            ConversationLauncherChip(management, "box", "main", "old-launcher", launcher, model) { l, m ->
                                launcher = l; model = m
                            }
                        }
                    }
                }
            }
        }
        rule.waitUntil(5_000) { rule.onAllNodes(androidx.compose.ui.test.hasText("Default: claude")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Default: claude").performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(androidx.compose.ui.test.hasText("Opus")).fetchSemanticsNodes().isNotEmpty() }
        assertEquals(listOf("runtime", "codex-gw", "team-api"), sharedAssistantNames)
        // A main chat already on Claude keeps the same-engine Team API, not the other-engine Codex provider.
        rule.onNodeWithText("runtime").assertExists()
        rule.onNodeWithText("team-api", substring = true).assertExists()
        rule.onNodeWithText("codex-gw").assertDoesNotExist()
        rule.onNodeWithText("默认").assertDoesNotExist()
        rule.onNodeWithText("runtime · 默认").assertDoesNotExist()
        rule.onNodeWithText("The pick applies from your next message until you change it. Assistant settings stay unchanged.").assertExists()
        val out = File(System.getProperty("persona.shots") ?: "build/conversation-launcher-shots").apply { mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
            val font = if (largeText) "large" else "default"
            File(out, "sheet-$font-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        rule.onNodeWithText("runtime").performClick()
        assertEquals("runtime", launcher)
        assertEquals(null, model)
        rule.onNodeWithText("runtime").performClick()
        rule.waitUntil(5_000) { rule.onAllNodes(androidx.compose.ui.test.hasText("Opus")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("Opus").performClick()
        assertEquals("runtime", launcher)
        assertEquals("opus", model)
        rule.onNodeWithText("runtime · opus").assertDoesNotExist()
        rule.onNodeWithText("runtime · Opus").performClick()
        rule.onNodeWithText("Use the default").performClick()
        assertEquals(null, launcher)
        assertEquals(null, model)
    }
}
