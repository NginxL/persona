package persona.feature.assistant

import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.ModelPick
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.Navigator
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import persona.feature.chat.LocalMainChatGuidance
import java.io.File

/**
 * The main conversation while it still misses a step (docs/assistant/onboarding.md 4.4): the card over the composer, the send
 * button naming the step, the hub's example questions, and a start the hub refused. Glass light and dark plus each
 * style in light, written to `build/onboarding-shots/` (or `-Dpersona.shots=<dir>`).
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-xhdpi")
class AssistantReadinessShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/onboarding-shots").also { it.mkdirs() }

    private val opened = mutableListOf<NavKey>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    private val noComputer = AssistantReadiness(mode = "setup", ready = false, next = "computer", steps = listOf(
        ReadinessStep("computer", "todo", code = "no_machine", title = "还没接上电脑",
            detail = "Persona 要在你的一台电脑上干活，现在还没接上电脑。", action = ReadinessAction("add_computer")),
        ReadinessStep("claude", "unknown"), ReadinessStep("model", "unknown"), ReadinessStep("first_message", "unknown")),
        suggestions = listOf(ReadinessSuggestion("what_can_you_do", "你能帮我做什么？", local = true),
            ReadinessSuggestion("how_to_connect_model", "怎么接模型", local = true)))

    private val notLoggedIn = AssistantReadiness(mode = "setup", ready = false, next = "model",
        machine = ReadinessMachine("mbp", "工作电脑"), launcher = "claude", steps = listOf(
            ReadinessStep("computer", "ok", machine = "mbp"), ReadinessStep("claude", "ok", version = "2.1.308"),
            ReadinessStep("model", "problem", code = "not_logged_in", title = "Claude Code 还没登录",
                detail = "在 工作电脑 上登录一次，或者改用 API Key。", action = ReadinessAction("connect_model"),
                altActions = listOf(ReadinessAction("login_claude"))),
            ReadinessStep("first_message", "todo", code = "never_tried", action = ReadinessAction("test_assistant"))),
        suggestions = noComputer.suggestions)

    private class Hub(var readiness: AssistantReadiness, refused: Boolean) : AssistantRepository, MessageSender, MainConversationSender, AssistantManagement {
        override val assistant = MutableStateFlow(AssistantSnapshot())
        override val outbox = MutableStateFlow(if (refused) listOf(OutboxMessage("m1", PendingMainConversation, "帮我看看这周的日程",
            DeliveryState.Refused, error = "no_machine", mainTarget = true)) else emptyList())
        val sent = mutableListOf<String>()
        val readinessReads = mutableListOf<Pair<Boolean, AssistantReadiness>>()
        override suspend fun refresh() = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { sent += text }
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) { sent += text }
        override suspend fun readiness(probe: Boolean) = readiness.also { readinessReads += probe to it }
        override suspend fun warm() {}
        override suspend fun endConversation(session: SessionKey) {}
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) {}
        override suspend fun runTask(id: String) = SessionKey("m", id)
        override suspend fun freshTask(id: String) = ScheduledTask(id = id)
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) {}
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) {}
        override suspend fun deleteMemory(name: String) {}
    }

    @Test fun connectModelOpensMainChatForTheShownManager() {
        assertEquals(ModelPurposeRoute("assistant", "work"), ReadinessActionId.ConnectModel.destination(null, manager = "work"))
        assertEquals(SettingsPageRoute(SettingsPage.ModelConfig), ReadinessActionId.PickModel.destination(null, manager = "work"))
    }

    @Test fun anExistingMainChatOffersSetupOnlyWhileItsModelIsBlocked() {
        val main = SessionKey("mbp", "existing-main")
        val other = SessionKey("mbp", "other-chat")
        var requested by mutableStateOf(main)
        val session = Session(machineId = main.machineId, sessionId = main.sessionId, state = "idle")
        val snapshot = AssistantSnapshot(mainSession = main, conversations = listOf(session))
        val hub = Hub(notLoggedIn, refused = false).also { it.assistant.value = snapshot }
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf(AssistantReadinessFeature)) {
                AssistantScreen(navigator, hub, hub, startConversation = { main }, management = hub,
                    machines = emptyList(), requestedSession = requested, composerService = null,
                    assistantChatContent = { _, _ ->
                        androidx.compose.foundation.layout.Column {
                            Text("Not logged in · Please run /login")
                            LocalMainChatGuidance.current?.invoke()
                        }
                    })
            }
        }

        rule.onNodeWithText("助手还没接上模型").assertExists()
        rule.onNodeWithText("接上模型").performClick()
        rule.runOnIdle { assertEquals(ModelPurposeRoute("assistant", "assistant"), opened.last()) }
        rule.runOnIdle { requested = other }
        rule.onNodeWithText("助手还没接上模型").assertDoesNotExist()

        rule.runOnIdle {
            requested = main
            hub.assistant.value = snapshot.copy(conversations = listOf(session.copy(modelPick = ModelPick("another-launcher"))))
        }
        rule.onNodeWithText("助手还没接上模型").assertDoesNotExist()

        rule.runOnIdle {
            hub.readiness = notLoggedIn.copy(machine = ReadinessMachine("other-machine", "另一台电脑"))
            hub.assistant.value = snapshot
        }
        rule.onNodeWithText("助手还没接上模型").assertDoesNotExist()

        rule.runOnIdle {
            hub.readiness = notLoggedIn
            hub.assistant.value = snapshot.copy(conversations = listOf(session.copy(turnErrorCode = "not_logged_in")))
        }
        rule.onNodeWithText("助手还没接上模型").assertExists()

        rule.runOnIdle {
            hub.readiness = notLoggedIn.copy(machine = null)
            hub.assistant.value = snapshot
        }
        rule.onNodeWithText("接上模型").assertDoesNotExist()

        rule.runOnIdle {
            hub.readiness = notLoggedIn.copy(machine = ReadinessMachine(" ", "工作电脑"))
            hub.assistant.value = snapshot.copy(conversations = listOf(session.copy(turnErrorCode = "not_logged_in")))
        }
        rule.onNodeWithText("接上模型").assertDoesNotExist()

        rule.runOnIdle {
            hub.readiness = AssistantReadiness()
            hub.assistant.value = snapshot
        }
        rule.onNodeWithText("助手还没接上模型").assertDoesNotExist()
        rule.onNodeWithText("Not logged in · Please run /login").assertExists()
    }

    @Test fun mainReadinessRefreshesWhenItsErrorClearsWhileAnotherChatIsOpen() {
        val main = Session("mbp", "existing-main", state = "idle", turnErrorCode = "not_logged_in")
        val other = Session("mbp", "other-chat", state = "idle")
        val snapshot = AssistantSnapshot(mainSession = main.key, conversations = listOf(main, other))
        val hub = Hub(notLoggedIn, refused = false).also { it.assistant.value = snapshot }
        var requested by mutableStateOf(main.key)
        rule.setContent {
            CompositionLocalProvider(LocalHubFeatures provides setOf(AssistantReadinessFeature)) {
                AssistantScreen(navigator, hub, hub, startConversation = { main.key }, management = hub,
                    machines = emptyList(), requestedSession = requested, composerService = null,
                    assistantChatContent = { active, _ ->
                        androidx.compose.foundation.layout.Column {
                            Text(active.sessionId)
                            LocalMainChatGuidance.current?.invoke()
                        }
                    })
            }
        }

        val guidance = rule.onNodeWithText("助手还没接上模型")
        rule.onNodeWithText(main.sessionId).assertExists()
        guidance.assertExists()
        rule.runOnIdle { requested = other.key }
        val otherChat = rule.onNodeWithText(other.sessionId).assertExists()
        guidance.assertDoesNotExist()

        val ready = AssistantReadiness()
        val readsBefore = rule.runOnIdle { hub.readinessReads.size }
        rule.runOnIdle {
            hub.readiness = ready
            hub.assistant.value = snapshot.copy(conversations = listOf(main.copy(turnErrorCode = null), other))
        }
        otherChat.assertExists()
        rule.runOnIdle {
            assertTrue("clearing the main chat's error reads readiness while the other chat is open",
                (false to ready) in hub.readinessReads.drop(readsBefore))
            requested = main.key
        }
        rule.onNodeWithText(main.sessionId).assertExists()
        guidance.assertDoesNotExist()
    }

    @Test fun light() = shots("light", interact = true)

    @Config(sdk = [35], qualifiers = "zh-w400dp-h900dp-night-xhdpi")
    @Test fun dark() = shots("dark")

    /** The longest send label (English) still fits on one line beside the box. */
    @Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
    @Test fun english() = shots("en-light", styles = false, check = false)

    // The screen draws its own PersonaTheme, which follows the system's night mode: dark comes from the qualifier.
    private fun shots(mode: String, interact: Boolean = false, styles: Boolean = true, check: Boolean = true) {
        var style by mutableStateOf(AppStyle.Glass)
        var hub by mutableStateOf(Hub(noComputer, refused = false))
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalHubFeatures provides setOf(AssistantReadinessFeature)) {
                    Box(Modifier.testTag("shot").width(400.dp).height(860.dp).background(PersonaTheme.palette.background)) {
                        key(hub, style) {
                            AssistantScreen(navigator, hub, hub, startConversation = { SessionKey("m", "new") },
                                machines = emptyList(), management = hub, mainConversationSender = hub,
                                composerService = null, startConversationWith = null)
                        }
                    }
                }
            }
        }
        fun capture(name: String) {
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out(), "assistant-$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        val all = if (styles) AppStyle.entries else listOf(AppStyle.Glass)
        for (s in all) {
            style = s
            val name = s.name.lowercase()
            hub = Hub(noComputer, refused = false)
            rule.waitForIdle(); if (check) rule.onNodeWithText("先添加电脑").assertExists(); capture("no-computer-$name-$mode")
            hub = Hub(notLoggedIn, refused = false)
            rule.waitForIdle(); if (check) rule.onNodeWithText("先接上模型").assertExists(); capture("not-logged-in-$name-$mode")
            hub = Hub(noComputer, refused = true)
            rule.waitForIdle(); capture("refused-$name-$mode")
        }
        if (!interact) return
        // The send button opens the step and sends nothing; the draft stays.
        style = AppStyle.Glass
        hub = Hub(notLoggedIn, refused = false)
        rule.waitForIdle()
        rule.onNodeWithText("先接上模型").performClick()
        rule.waitForIdle()
        assertEquals(ModelPurposeRoute("assistant", "assistant"), opened.last())
        assertTrue(hub.sent.isEmpty())
        // A local example question opens help here, never sent to the model.
        rule.onNodeWithText("你能帮我做什么？").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Persona 能做什么").assertExists()
        val dialog = rule.onNode(isDialog()).captureToImage().asAndroidBitmap()
        File(out(), "assistant-help-what-glass-light.png").outputStream().use { dialog.compress(Bitmap.CompressFormat.PNG, 100, it) }
        assertTrue(hub.sent.isEmpty())
    }
}
