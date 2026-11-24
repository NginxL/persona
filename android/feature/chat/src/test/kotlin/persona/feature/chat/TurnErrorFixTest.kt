package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.onRoot
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.ReadinessActionId
import persona.core.designsystem.Tokens
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.LineItem
import persona.core.domain.lines.chatLines
import java.io.File

/**
 * Persona's turn that ended on a model error the hub named (#4339 C18, iOS `TurnErrorFix`): the error line becomes the
 * Model Not Connected card, whose Fix It opens the set-up; every style, light and dark, in `build/turn-fix-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class TurnErrorFixTest {
    @get:Rule val rule = createComposeRule()

    private val reply = chatLines(listOf(LineItem(Item(id = "r#0", seq = 1, role = "assistant", kind = "text",
        text = "Not logged in · Please run /login", ts = "2026-11-12T08:01:05Z")))).lines.last()
    private val apiError = chatLines(listOf(LineItem(Item(id = "e#0", seq = 1, role = "assistant", kind = "text",
        text = "API Error: 401 {\"type\":\"error\",\"error\":{\"type\":\"authentication_error\",\"message\":\"invalid x-api-key\"}}",
        ts = "2026-11-12T08:01:05Z")))).lines.last()

    private val pickerManagement = object : AssistantManagement {
        override suspend fun config() = AssistantConfig(machineId = "box", launcher = "claude", main = "main")
        override suspend fun assistantLaunchers(machineId: String) = listOf(AgentLauncher("claude"))
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

    @Test fun theHubsCodesBecomeFixes() {
        assertEquals(ReadinessActionId.ConnectModel, TurnErrorFix.of("not_logged_in")!!.action)
        assertEquals(ReadinessActionId.EditProvider, TurnErrorFix.of("auth_failed")!!.action)
        assertNull(TurnErrorFix.of("model_route")!!.action)
        assertNull(TurnErrorFix.of("tool_search_unsupported")!!.action)
        // A refusal fallback that isn't there has its own card (ModelFallback.kt); anything else stays the error line.
        assertNull(TurnErrorFix.of("model_fallback_unavailable"))
        assertNull(TurnErrorFix.of(null))
    }

    @Test fun onlyTheToolSearchRemedyMakesANonTransientLatestErrorRetryable() {
        val lines = listOf(apiError)
        assertNull(retryLine(lines, working = false, canSend = true))
        assertNull(retryLine(lines, working = false, canSend = true, turnFix = TurnErrorFix.ModelRoute))
        assertEquals(apiError.id, retryLine(lines, working = false, canSend = true, turnFix = TurnErrorFix.ToolSearchUnsupported))
        assertNull(retryLine(lines, working = true, canSend = true, turnFix = TurnErrorFix.ToolSearchUnsupported))
    }

    @Test fun theErrorLineBecomesTheCardAndFixItOpensTheSetUp() {
        val fixed = mutableListOf<ReadinessActionId>()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                DisposableEffect(loader) { onDispose { loader.shutdown() } }
                CompositionLocalProvider(LocalTurnErrorFixAction provides { fixed += it }) {
                    Column(Modifier.testTag("turn-fix").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        for ((line, fix) in listOf(reply to TurnErrorFix.NotLoggedIn, apiError to TurnErrorFix.AuthFailed)) {
                            ChatLineContent(line, SessionKey("box", "main"), loader, null, onToggle = {}, onFold = {}, folding = emptySet(),
                                foldErrors = emptyMap(), onFullText = {}, style = ChatRenderStyle.Assistant, turnFix = fix)
                        }
                    }
                }
            }
        }
        rule.onNodeWithText("Model not connected: Claude Code CLI isn't signed in").assertExists()
        rule.onNodeWithText("Model not connected: the key is wrong").assertExists()
        rule.onNodeWithText("Not logged in · Please run /login").assertDoesNotExist()
        rule.onNodeWithText("Model not connected: the key is wrong").performClick()
        rule.onNodeWithText("Not logged in · Please run /login", substring = true).assertDoesNotExist()
        rule.onAllNodesWithTextCount("Fix it", 2)
        val out = File(System.getProperty("persona.shots") ?: "build/turn-fix-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { style = s; dark = d }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("turn-fix").captureToImage().asAndroidBitmap()
            File(out, "turn-fix-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.runOnIdle { style = AppStyle.Glass; dark = false }
        rule.onAllNodesWithTextFirstClick("Fix it")
        assertEquals(listOf(ReadinessActionId.ConnectModel), fixed)
    }

    @Test fun modelRoutePicksHereAndToolSearchRetriesWithItsOwnCard() {
        var opened = 0
        var cleared = 0
        var retried = 0
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var fontScale by mutableStateOf(1f)
        rule.setContent {
            val density = androidx.compose.ui.platform.LocalDensity.current
            CompositionLocalProvider(androidx.compose.ui.platform.LocalDensity provides androidx.compose.ui.unit.Density(density.density, fontScale)) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    CompositionLocalProvider(LocalTurnErrorModelActions provides TurnErrorModelActions(
                        openPicker = { opened++ }, clearPick = { cleared++ })) {
                        Column(Modifier.testTag("turn-remedies").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l),
                            verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                            TurnErrorFixCard(TurnErrorFix.ModelRoute, "API Error: 400 model_route")
                            TurnErrorFixCard(TurnErrorFix.ToolSearchUnsupported, "API Error: 400 tool_search_unsupported",
                                launcher = "team-relay", onRetry = { retried++ })
                        }
                    }
                }
            }
        }
        rule.onNodeWithText("Model not connected: this model can't be used").assertExists()
        rule.onNodeWithText("Model not connected: this gateway can't load tools on demand").assertExists()
        rule.onNodeWithText("team-relay", substring = true).assertDoesNotExist()
        rule.onNodeWithText("Follow default").assertExists()
        rule.onNodeWithText("Retry").assertExists()
        val out = File(System.getProperty("persona.shots") ?: "build/turn-remedy-shots").also { it.mkdirs() }
        for (large in listOf(false, true)) for (s in AppStyle.entries) for (d in listOf(false, true)) {
            rule.runOnIdle { fontScale = if (large) 2f else 1f; style = s; dark = d }
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("turn-remedies").captureToImage().asAndroidBitmap()
            File(out, "turn-remedies-${if (large) "large" else "default"}-${s.name.lowercase()}-${if (d) "dark" else "light"}.png")
                .outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        rule.onNodeWithText("Fix it").performClick()
        rule.onNodeWithText("Follow default").performClick()
        rule.onNodeWithText("Retry").performClick()
        assertEquals(1, opened)
        assertEquals(1, cleared)
        assertEquals(1, retried)
    }

    @Test fun theChatNamesAndRepairsTheSessionsLauncher() {
        val key = SessionKey("box", "main")
        val text = "API Error: 401 authentication_error"
        val row = Session(machineId = key.machineId, sessionId = key.sessionId, assistant = true, status = "idle", state = "idle",
            launcher = "team-relay", turnError = text, turnErrorCode = "auth_failed")
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = listOf(
                Item("e#0", 1, role = "assistant", kind = "text", text = text)), loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(emptyList<Machine>())
            override val sessions = MutableStateFlow(listOf(row))
            override suspend fun refresh() = Unit
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        var repaired: String? = null
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, renderStyle = ChatRenderStyle.Assistant,
                    chrome = ChatChromeBindings(onFixModel = { _, launcher, _ -> repaired = launcher }))
            }
        }
        rule.waitUntil(10_000) { rule.onAllNodesWithText("Fix it").fetchSemanticsNodes().isNotEmpty() }
        val out = File(System.getProperty("persona.shots") ?: "build/turn-error-launcher-shots").also { it.mkdirs() }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.waitForIdle()
            val bitmap = rule.onRoot().captureToImage().asAndroidBitmap()
            File(out, "turn-error-${if (night) "dark" else "light"}.png").outputStream().use {
                bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)
            }
        }
        rule.onNodeWithText("Model not connected: the key for team-relay is wrong").assertExists()
        rule.onNodeWithText("Fix it").performClick()
        assertEquals("team-relay", repaired)
    }

    @Test fun modelRouteOpensOnlyTheChatsOwnBriefPicker() {
        val fixed = mutableListOf<ReadinessActionId>()
        showTurnErrorChat(AppStyle.Brief, mainConversation = "main", onFix = fixed::add)

        rule.onNodeWithText("Fix it").performClick()

        rule.waitUntil(10_000) { rule.onAllNodesWithText("Following messages use").fetchSemanticsNodes().isNotEmpty() }
        assertEquals("only the launcher/model sheet opens", 1, rule.onAllNodes(isDialog()).fetchSemanticsNodes().size)
        assertEquals("no legacy Main-chat purpose navigation", emptyList<ReadinessActionId>(), fixed)
    }

    @Test fun modelRouteOpensANonMainChatsOwnBriefPicker() {
        val fixed = mutableListOf<ReadinessActionId>()
        showTurnErrorChat(AppStyle.Brief, mainConversation = "main", sessionId = "leader", onFix = fixed::add)

        rule.onNodeWithText("Fix it").performClick()

        rule.waitUntil(10_000) { rule.onAllNodesWithText("Following messages use").fetchSemanticsNodes().isNotEmpty() }
        assertEquals("only this chat's launcher/model sheet opens", 1, rule.onAllNodes(isDialog()).fetchSemanticsNodes().size)
        assertEquals("no Main-chat purpose navigation", emptyList<ReadinessActionId>(), fixed)
    }

    @Test fun modelRouteWithoutItsOwnPickerUsesTheModelSettingsFallback() {
        val fixed = mutableListOf<ReadinessActionId>()
        showTurnErrorChat(AppStyle.Brief, mainConversation = "main", withManagement = false, onFix = fixed::add)

        rule.onNodeWithText("Fix it").performClick()

        assertEquals(listOf(ReadinessActionId.PickModel), fixed)
        assertEquals(0, rule.onAllNodes(isDialog()).fetchSemanticsNodes().size)
    }

    @Test fun olderHubsRawToolSearchErrorGetsItsOwnCardAndRetries() {
        val sent = mutableListOf<String>()
        val edge = "API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint " +
            "(defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded"
        showTurnErrorChat(AppStyle.Brief, mainConversation = "main", code = null, text = edge, onSend = sent::add)

        rule.onNodeWithText("Model not connected: this gateway can't load tools on demand").assertIsDisplayed()
        rule.onNodeWithText("Fix it").assertDoesNotExist()
        rule.onNodeWithText("Retry").performClick()
        assertEquals(listOf("Continue"), sent)
    }

    @Test fun anExplicitHubCodeIsNeverReplacedByTheRawToolSearchFallback() {
        val sent = mutableListOf<String>()
        val edge = "API Error: 400 request is not representable on this edge: ToolSearch is not supported on this endpoint " +
            "(defer_loading:true, tool_search_*, tool_reference); resend with all tools loaded"
        showTurnErrorChat(AppStyle.Brief, mainConversation = "main", code = FALLBACK_UNAVAILABLE, text = edge, onSend = sent::add)

        rule.onNodeWithText("400").assertIsDisplayed()
        rule.onNodeWithText("Model not connected: this gateway can't load tools on demand").assertDoesNotExist()
        rule.onNodeWithText("Retry").assertDoesNotExist()
        assertTrue(sent.isEmpty())
    }

    private fun showTurnErrorChat(style: AppStyle, mainConversation: String, sessionId: String = "main",
        withManagement: Boolean = true, code: String? = "model_route", text: String = "API Error: 400 model_route",
        onFix: (ReadinessActionId) -> Unit = {}, onSend: (String) -> Unit = {}) {
        val key = SessionKey("box", sessionId)
        val row = Session(machineId = key.machineId, sessionId = key.sessionId, assistant = true, status = "idle", state = "idle",
            launcher = "claude", turnError = text, turnErrorCode = code)
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = listOf(
                Item("e#0", 1, role = "assistant", kind = "text", text = text)), loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(emptyList<Machine>())
            override val sessions = MutableStateFlow(listOf(row))
            override suspend fun refresh() = Unit
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = onSend(text)
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = style) {
                ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, renderStyle = ChatRenderStyle.Assistant,
                    chrome = ChatChromeBindings(assistantManagement = pickerManagement.takeIf { withManagement },
                        mainConversation = mainConversation, onFixModel = { action, _, _ -> onFix(action) }))
            }
        }
        rule.waitUntil(10_000) {
            rule.onAllNodesWithText("Fix it").fetchSemanticsNodes().isNotEmpty() ||
                rule.onAllNodesWithText("Retry").fetchSemanticsNodes().isNotEmpty() ||
                rule.onAllNodesWithText("400").fetchSemanticsNodes().isNotEmpty()
        }
    }

    private fun androidx.compose.ui.test.junit4.ComposeContentTestRule.onAllNodesWithTextCount(text: String, count: Int) {
        assertEquals(count, onAllNodes(androidx.compose.ui.test.hasText(text)).fetchSemanticsNodes().size)
    }

    private fun androidx.compose.ui.test.junit4.ComposeContentTestRule.onAllNodesWithTextFirstClick(text: String) =
        onAllNodes(androidx.compose.ui.test.hasText(text))[0].performClick()
}
