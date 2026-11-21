package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.hasAnyDescendant
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.EngineLeg
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.ItemsResponse
import persona.core.contracts.Machine
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentCreationService
import persona.core.contracts.api.AgentDirectories
import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AgentModel
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.AgentStartResult
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.LauncherModels
import persona.core.designsystem.AppStyle
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.ModelPillWidth
import persona.core.designsystem.PersonaTheme
import persona.core.domain.lines.EngineChange
import persona.core.domain.models.AgentModelPanel
import java.io.File

@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h760dp-xhdpi")
class EngineSwitchFlowTest {
    @get:Rule val rule = createComposeRule()

    private val agent = Session("computer", "worker", agent = "claude", launcher = "claude",
        model = "sonnet", status = "idle", state = "idle", paneId = "pane", rev = 1)
    private val creation = object : AgentCreationService {
        override val scope = "test"
        override suspend fun launchers(machineId: String) =
            listOf(AgentLauncher("claude"), AgentLauncher("codex", agentType = "codex"))
        override suspend fun launcherModels(machineId: String, launcher: String, refresh: Boolean) =
            LauncherModels(launcher, if (launcher == "claude") listOf(AgentModel("sonnet", "Sonnet"))
            else listOf(AgentModel("gpt-5.5", "GPT-5.5")))
        override suspend fun directories(machineId: String, path: String) = AgentDirectories(path)
        override suspend fun start(request: AgentStartRequest, operationId: String) = AgentStartResult.Unknown
        override suspend fun operation(operationId: String) = AgentStartResult.Unknown
    }

    @Test fun pickerRequiresConsentAndCanCancelBeforeTheNextMessage() {
        val requests = mutableListOf<ChatAction>()
        var rendered by mutableStateOf<DrawnSwitch?>(null)
        var dark by mutableStateOf(false)
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction): ChatActionResult {
                requests += action
                return ChatActionResult.Done
            }
            override suspend fun verify(session: Session, action: ChatAction) = ChatActionResult.Unknown
            override suspend fun info(session: Session) = SessionInfo(session.sessionId)
        }
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                CompositionLocalProvider(LocalHubFeatures provides setOf(AgentModelPanel.ENGINE_SWITCH)) {
                    val resources = LocalResources.current
                    val scope = rememberCoroutineScope()
                    val state = remember(resources, scope) { ChatChromeState(resources, actions, scope, {}, {}) }
                    Box {
                        AgentModelPill(rendered.over(agent, 0L), state, creation, true, ModelPillWidth.Wide,
                            onSwitch = { rendered = it }, onOverlay = {})
                    }
                }
            }
        }

        rule.onNodeWithTag("model-pill").performClick()
        val target = hasText(AgentModelPanel.modelLabel("gpt-5.5")) and hasClickAction()
        rule.waitUntil(10_000) { rule.onAllNodes(target).fetchSemanticsNodes().isNotEmpty() }
        rule.onNode(target).performClick()
        rule.onNodeWithText("Switch Agent?").assertExists()
        rule.runOnIdle { assertTrue("picking alone does not call the hub", requests.isEmpty()) }
        captureDialog("confirm-light")
        rule.onNodeWithText("Don't ask again").performClick()
        rule.onNodeWithText("Keep current Agent").performClick()
        rule.onNodeWithText("Switch Agent?").assertDoesNotExist()
        rule.runOnIdle { assertTrue("dismissing leaves the pending state alone", requests.isEmpty()) }

        rule.runOnIdle { dark = true }
        rule.onNode(target).performClick()
        captureDialog("confirm-dark")
        rule.onNodeWithText("Switch Agent").performClick()
        rule.waitUntil(10_000) { rendered?.switch != null }
        rule.runOnIdle {
            val session = rendered.over(agent, 0L)
            assertEquals(listOf(ChatAction.Modes(launcher = "codex", from = "claude", model = "gpt-5.5")), requests)
            assertEquals(PendingSwitch("codex", "gpt-5.5", agent = "codex"), session.pendingSwitch)
            assertTrue(switchesNext(session, true))
        }

        rule.onNodeWithTag("model-pill").performClick()
        rule.onNodeWithText("Cancel pending switch").performClick()
        rule.waitUntil(10_000) { rendered?.switch == null }
        rule.runOnIdle {
            assertEquals(ChatAction.Modes(launcher = "claude", from = "claude"), requests.last())
            assertEquals(2, requests.size)
            assertNull(rendered.over(agent, 0L).pendingSwitch)
        }
    }

    @Test @Config(fontScale = 2f)
    fun confirmationKeepsBothActionsReachableAtLargestTextSize() {
        var dark by mutableStateOf(false)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                EngineSwitchConfirmation(onDismiss = {}, onConfirm = {})
            }
        }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.onNodeWithText("Switch Agent").assertExists()
            rule.onNodeWithText("Keep current Agent").assertExists()
            captureDialog("confirm-large-" + if (night) "dark" else "light")
        }
    }

    @Test fun handoffStaysInItsReaderUntilRequested() {
        var dark by mutableStateOf(false)
        val change = EngineChange("claude", "codex", "gpt-5.5", resumed = true, handoff = "Review the retry behavior.")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = AppStyle.Glass) {
                Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag("handoff-shot")) {
                    EngineChangeDivider(change)
                }
            }
        }
        for (night in listOf(false, true)) {
            rule.runOnIdle { dark = night }
            rule.onNodeWithText(change.handoff).assertDoesNotExist()
            capture(rule.onNodeWithTag("handoff-shot").captureToImage().asAndroidBitmap(), "handoff-" + if (night) "dark" else "light")
        }
        rule.onNodeWithText("View handoff context").performClick()
        rule.waitUntil(15_000) { rule.onAllNodes(hasText(change.handoff, substring = true), useUnmergedTree = true).fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun theChatLoadsTheEarlierRunInPlace() {
        val old = SessionKey(agent.machineId, "old-codex")
        val current = agent.copy(engineHistory = listOf(EngineLeg(old.sessionId, "codex", "2026-11-20T10:00:03Z")))
        val repo = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
                Item("live", 1, "2026-11-20T10:00:04Z", role = "user", kind = "text", text = "The current run."),
            )))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
            override suspend fun earlierEnginePage(session: SessionKey, beforeSeq: Int?): ItemsResponse {
                assertEquals(old, session)
                assertNull(beforeSeq)
                return ItemsResponse(listOf(Item("past", 1, "2026-11-20T10:00:01Z", role = "user", kind = "text", text = "An earlier run.")))
            }
        }
        renderChat(current, listOf(current), repo)
        rule.waitUntil(10_000) { rule.onAllNodes(hasText("Load earlier messages")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("An earlier run.").assertDoesNotExist()
        rule.onNodeWithText("Load earlier messages").performClick()
        rule.waitUntil(10_000) { rule.onAllNodes(hasText("An earlier run.")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithText("The current run.").assertExists()
    }

    @Test fun cachedExactLookupCannotKeepAnOldEngineChatOpen() {
        val successor = agent.copy(sessionId = "codex-successor", agent = "codex", engineHistory = listOf(
            EngineLeg(agent.sessionId, "claude", "2026-11-20T10:00:03Z")))
        val followed = mutableListOf<SessionKey>()
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        renderChat(agent, listOf(successor), chats, ChatChromeBindings(lookupRows = listOf(agent),
            onFollowSession = { followed += it }))
        rule.waitUntil(10_000) { followed.isNotEmpty() }
        rule.runOnIdle { assertEquals(listOf(successor.key), followed) }
    }

    private fun renderChat(current: Session, listed: List<Session>, repo: ChatRepository,
        chrome: ChatChromeBindings = ChatChromeBindings()) {
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(current.machineId, "Computer", online = true)))
            override val sessions = MutableStateFlow(listed)
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
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Glass) {
                ChatScreen(current.key, repo, sender, interactions, sessions, onBack = {}, chrome = chrome)
            }
        }
    }

    private fun captureDialog(name: String) = capture(
        rule.onNode(isDialog() and hasAnyDescendant(hasText("Switch Agent?")), useUnmergedTree = true)
            .captureToImage().asAndroidBitmap(), name)


    private fun capture(bitmap: Bitmap, name: String) {
        val output = File(System.getProperty("persona.shots") ?: "build/engine-switch-shots").apply { mkdirs() }
        File(output, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
    }
}
