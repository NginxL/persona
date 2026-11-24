package persona.feature.assistant

import android.graphics.Bitmap
import android.content.res.Configuration
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.feature.assistant.settings.AssistantSettingsManagerTest
import persona.feature.assistant.settings.AssistantSettingsScreen
import java.io.File
import java.io.IOException

/**
 * The title menu in iOS's order and a new conversation's first message (#4339 C18), in every style, light and dark: the
 * menu over a working side conversation while the hub is out of reach, the first message with "Sending…" while its
 * conversation starts, and kept as a failed bubble with Resend and Delete. PNGs in `build/assistant-details-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class AssistantDetailsShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")
    private val side = SessionKey("mac", "side")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() = AssistantManagerChoice.reset()

    private class Hub(val main: SessionKey) : AssistantRepository, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main, conversations = listOf(
            Session(machineId = "mac", sessionId = "main", title = "Main chat", assistant = true),
            Session(machineId = "mac", sessionId = "side", title = "Trip plan", assistant = true, status = "working",
                launcher = "claude", model = "anthropic/claude-opus-4-5"),
        )))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        var gate = CompletableDeferred<Unit>()
        var refuse = false
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        suspend fun start(): SessionKey {
            gate.await()
            if (refuse) throw IOException("offline")
            return SessionKey("mac", "new")
        }
    }

    private class Composer : ChatComposerService {
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        override suspend fun upload(session: SessionKey, name: String, file: File) = error("not used")
        override suspend fun interrupt(session: SessionKey) = Unit
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("not used")
    }

    @Test fun theMenuAndTheFirstMessage() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var round by mutableIntStateOf(0)
        var hub = Hub(main)
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                CompositionLocalProvider(LocalConfiguration provides configuration, LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant")) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        key(round) {
                            val h = hub
                            AssistantScreen(navigator, h, h, h::start, composerService = Composer(), mainConversationSender = h,
                                requestedSession = side,
                                connection = ConnectionState.Connecting,
                                machines = listOf(Machine("mac", name = "MacBook Pro", online = true)),
                                chatContent = { key -> Text("chat:${key.sessionId}", Modifier.padding(Tokens.Space.l)) })
                        }
                    }
                }
            }
        }
        fun capture(name: String, dialog: Boolean = false) {
            rule.waitForIdle()
            val node = if (dialog) rule.onNode(isDialog()) else rule.onNodeWithTag("shot")
            val bitmap = node.captureToImage().asAndroidBitmap()
            val out = File(System.getProperty("persona.shots") ?: "build/assistant-details-shots").also { it.mkdirs() }
            File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
        fun send(text: String) {
            rule.onNode(hasSetTextAction()).performTextReplacement(text)
            rule.onNodeWithContentDescription("Send").performClick()
            rule.waitForIdle()
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            val name = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            hub = Hub(main); style = s; dark = d; round++
            rule.waitForIdle()
            rule.onNodeWithText("chat:side").assertExists()
            rule.onNodeWithText("Persona").performClick()
            capture("$name-1-menu", dialog = true)
            // iOS's heading names the conversation and what it runs on, in every style
            rule.onNodeWithText("Trip plan · claude · Opus 4.5").assertExists()
            rule.onNodeWithText("Copy info").assertExists()
            rule.onNodeWithText("Copy directory").performClick(); rule.waitForIdle()

            // New chat's first message: its bubble and "Sending…" while the conversation starts.
            rule.onNodeWithContentDescription("New chat").performClick(); rule.waitForIdle()
            send("Plan a weekend in the mountains")
            capture("$name-2-sending")
            rule.onNodeWithText("Sending…").assertExists()
            // The start fails: the message stays as a failed bubble, with why, Resend and Delete.
            hub.refuse = true
            rule.runOnIdle { hub.gate.complete(Unit) }
            rule.waitForIdle()
            capture("$name-3-failed")
            rule.onNodeWithText("Resend").assertExists()
            rule.onNodeWithText("Delete").performClick(); rule.waitForIdle()
        }
    }

    /** #6401's Details identity and Rename action remain visible and usable with the system's largest text. */
    @Config(sdk = [35], qualifiers = "en-w400dp-h1800dp-xhdpi", fontScale = 2f)
    @Test fun profileIdentityAndRenameAtTheLargestTextSize() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val roster = ManagerRoster(listOf(
            HubManager("assistant", "Helper", online = true, isDefault = true),
            HubManager("work", "Work", online = true),
        ), "assistant")
        val hub = AssistantSettingsManagerTest.Hub(
            mutableMapOf("assistant" to AssistantConfig(manager = "assistant")),
            managers = roster.managers.toMutableList(),
        )
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                CompositionLocalProvider(LocalManagerRoster provides roster) {
                    Box(Modifier.testTag("profile-shot").width(400.dp).height(1800.dp).background(PersonaTheme.palette.background)) {
                        AssistantSettingsScreen(hub, emptyList(), onBack = {})
                    }
                }
            }
        }
        rule.waitForIdle()
        rule.onNodeWithText("Helper").assertIsDisplayed()
        rule.onNodeWithText("Rename").assertIsDisplayed().assertHasClickAction()
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            val bitmap = rule.onNodeWithTag("profile-shot").captureToImage().asAndroidBitmap()
            val out = File(System.getProperty("persona.shots") ?: "build/assistant-details-shots").also { it.mkdirs() }
            val suffix = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            File(out, "profile-rename-large-$suffix.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
