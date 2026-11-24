package persona.feature.assistant

import android.content.ClipboardManager
import androidx.activity.ComponentActivity
import androidx.compose.material3.Text
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.navigation3.runtime.NavKey
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Before
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Machine
import persona.core.contracts.ModelPick
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.feature.assistant.AssistantMenuItem.*
import java.io.File

/**
 * The title menu's items in iOS's order (#4339 C18, iOS `AssistantTopBar.menuItems`): Connection details while the hub is
 * out of reach, Knowledge, Details, Back to main chat, the copies, the main conversation's actions, Session info and Interrupt;
 * and what Copy directory and Copy info put on the clipboard (iOS `AssistantCopyText`).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class AssistantMenuTest {
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

    // A module's action (Modules.kt), as the voice module offers "Voice chat" while the hub runs a voice extension.
    private val moduleAction = persona.core.moduleapi.ModuleMenuAction("example.chat", "Voice chat", PersonaIcons.voiceChannel) {}

    @Test fun theItemsComeInIosOrderAndSections() {
        assertEquals(listOf(listOf(ConnectionDetails, Knowledge, Details, ModuleItem(moduleAction), BackToMain),
            listOf(CopyDirectory, CopyInfo, CopySessionId, CopyDiagnostics), listOf(SessionInfo, Interrupt)),
            assistantMenu(offline = true, modules = listOf(moduleAction), main = main, active = side, compact = false, newMain = false, working = true))
        // The main conversation: no way back to it, its own actions, nothing to interrupt while it rests.
        assertEquals(listOf(listOf(Knowledge, Details), listOf(CopyDirectory, CopyInfo, CopySessionId, CopyDiagnostics),
            listOf(Compact, NewMain), listOf(SessionInfo)),
            assistantMenu(offline = false, modules = emptyList(), main = main, active = main, compact = true, newMain = true, working = false))
        // Clear context (#5100) between them, as the web's menu has it; alone when the hub offers only it.
        assertEquals(listOf(Compact, ClearContext, NewMain),
            assistantMenu(offline = false, modules = emptyList(), main = main, active = main, compact = true, newMain = true, working = false, clear = true)[2])
        assertEquals(listOf(ClearContext),
            assistantMenu(offline = false, modules = emptyList(), main = main, active = main, compact = false, newMain = false, working = false, clear = true)[2])
        // A blank page copies what a new conversation would use, and has no session to show or stop.
        assertEquals(listOf(listOf(Knowledge, Details, BackToMain), listOf(CopyDirectory, CopyInfo)),
            assistantMenu(offline = false, modules = emptyList(), main = main, active = null, compact = false, newMain = false, working = true))
    }

    @Test fun copyInfoSaysWhichConversationWhereAndHowToGoOn() {
        val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
        assertEquals("""
            Persona assistant chat Main chat “Trip plan”
            Session ID: s1
            Machine: MacBook (mac)
            Working directory: ~/work
            Launcher: claude · Model: opus
            To continue on that machine: cd ~/work && claude --resume s1
        """.trimIndent(), assistantCopyInfo(res, "Trip plan", main = true, sessionId = "s1", machine = "MacBook", machineId = "mac",
            cwd = "~/work", launcher = "claude", model = "opus"))
        assertEquals("""
            Persona assistant (Claude on the hub)
            Machine: mac
            Working directory: ~/.persona/assistant
            Launcher: claude
        """.trimIndent(), assistantCopyInfo(res, null, main = false, sessionId = null, machine = "mac", machineId = "mac",
            cwd = AssistantDefaultCwd, launcher = "claude", model = null))
        // A side conversation keeps its own launcher, the main one follows the settings; a provider path shows its model.
        val config = AssistantConfig(machineId = "mac", launcher = "glm", cwd = "~/assistant")
        val session = Session(machineId = "mac", sessionId = "side", title = "Claude Code", cwd = "", launcher = "claude", model = "anthropic/opus")
        val copy = AssistantCopyText(session, side, main = false, config = config, assistantMachine = "", machines = emptyList())
        assertEquals("~/assistant", copy.cwd)
        assertEquals("claude", copy.launcher)
        assertEquals("glm", AssistantCopyText(session, main, main = true, config = config, assistantMachine = "", machines = emptyList()).launcher)
        assertEquals("""
            Persona assistant chat
            Session ID: side
            Machine: mac
            Working directory: ~/assistant
            Launcher: claude · Model: opus
            To continue on that machine: cd ~/assistant && claude --resume side
        """.trimIndent(), copy.info(res))
    }

    @Test fun theHeadingNamesTheConversationAndWhatItRunsOn() {
        val res = ApplicationProvider.getApplicationContext<android.content.Context>().resources
        val config = AssistantConfig(machineId = "mac", launcher = "glm", cwd = "~/assistant")
        fun title(session: Session?, key: SessionKey?, main: Boolean, group: String? = null) =
            AssistantCopyText(session, key, main, config, assistantMachine = "", machines = emptyList()).menuTitle(res, "Persona", group)
        // The main conversation follows the settings' launcher and shows the model it runs by its short name (the pill's and the
        // Agents rows'), never the id: iOS's `Fmt.model` shows "claude-opus-4-5", Android says "Opus 4.5".
        val mainRow = Session(machineId = "mac", sessionId = "main", title = "Whatever", launcher = "claude", model = "anthropic/claude-opus-4-5")
        assertEquals("Main chat · glm · Opus 4.5", title(mainRow, main, main = true))
        // A stored pick (#5055) names the launcher and the model its next message runs on, before either.
        val picked = mainRow.copy(modelPick = ModelPick("work", "work/deepseek-v4-flash", "high"))
        assertEquals("Main chat · work · Deepseek V4 Flash", title(picked, main, main = true))
        // A pick of the launcher's default model keeps the model it runs now.
        assertEquals("Main chat · work · Opus 4.5", title(mainRow.copy(modelPick = ModelPick("work")), main, main = true))
        // The short names as the rows say them: a dated Claude id, a GPT id, an id with nothing to shorten.
        assertEquals("Main chat · glm · Haiku 4.5", title(mainRow.copy(model = "claude-haiku-4-5-20251001"), main, main = true))
        assertEquals("Main chat · glm · GPT-5.4 Mini", title(mainRow.copy(model = "gpt-5.4-mini"), main, main = true))
        // No model to name (not picked, not running yet): the heading ends with the launcher.
        assertEquals("Main chat · glm", title(mainRow.copy(model = null), main, main = true))
        assertEquals("Main chat · glm", title(mainRow.copy(model = "  "), main, main = true))
        // Another conversation is named by its title and keeps its own launcher; with no title of its own it takes the persona's name.
        val side = Session(machineId = "mac", sessionId = "side", title = "Trip plan", launcher = "claude")
        assertEquals("Trip plan · claude", title(side, this.side, main = false))
        assertEquals("Trip plan · claude · Opus 4.5", title(side.copy(model = "claude-opus-4-5"), this.side, main = false))
        assertEquals("Persona · claude", title(side.copy(title = "Claude Code"), this.side, main = false))
        assertEquals("Persona · claude", title(side.copy(title = ""), this.side, main = false))
        // The blank page: a conversation that would start on the settings' launcher.
        assertEquals("New chat · glm", title(null, null, main = false))
        // With several hub groups the selected one's name ends it.
        assertEquals("Main chat · glm · Opus 4.5 · Office", title(mainRow, main, main = true, group = "Office"))
        assertEquals("New chat · glm · Office", title(null, null, main = false, group = "Office"))
        // Copy info is text for a terminal or another agent: it keeps the model's id (its provider path cut, as iOS does).
        val info = AssistantCopyText(mainRow, main, main = true, config = config, assistantMachine = "", machines = emptyList()).info(res)
        assertEquals(true, info.lines().contains("Launcher: glm · Model: claude-opus-4-5"))
    }

    @Test fun theHeadingIsTranslated() {
        val base = ApplicationProvider.getApplicationContext<android.content.Context>()
        for ((tag, words) in mapOf("zh" to ("主对话" to "新对话"), "ja" to ("メインチャット" to "新しいチャット"))) {
            val localized = android.content.res.Configuration(base.resources.configuration).apply { setLocale(java.util.Locale.forLanguageTag(tag)) }
            val res = base.createConfigurationContext(localized).resources
            val session = Session(machineId = "mac", sessionId = "main", launcher = "claude", model = "claude-opus-4-5")
            assertEquals("${words.first} · claude · Opus 4.5", AssistantCopyText(session, main, main = true, config = null, assistantMachine = "", machines = emptyList()).menuTitle(res, "Persona"))
            assertEquals("${words.second} · claude", AssistantCopyText(null, null, main = false, config = null, assistantMachine = "", machines = emptyList()).menuTitle(res, "Persona"))
        }
    }

    @Test fun everyItemHasIosIconForItsMeaning() {
        // iOS `AssistantTopBar.menuItems`: Theme.Icon.offline / knowledge / person / home / folder / copy / id / diagnostics /
        // compactContext / newConversation / info / interrupt. A module's action brings its own icon.
        val expected = mapOf(
            ConnectionDetails to PersonaIcons.offline, Knowledge to PersonaIcons.knowledge, Details to PersonaIcons.person,
            BackToMain to PersonaIcons.home, CopyDirectory to PersonaIcons.folder,
            CopyInfo to PersonaIcons.copy, CopySessionId to PersonaIcons.id, CopyDiagnostics to PersonaIcons.diagnostics,
            Compact to PersonaIcons.compactContext, NewMain to PersonaIcons.newConversation, SessionInfo to PersonaIcons.info,
            // Clear context: the web's mark for it; the icon set has no meaning of iOS's eraser (only its pane /clear has one).
            ClearContext to PersonaIcons.retry,
            Interrupt to PersonaIcons.interrupt,
        )
        assertEquals(AssistantMenuItem.entries.toSet(), expected.keys)
        for ((item, icon) in expected) assertSame(item.toString(), icon, item.icon)
        // One meaning, one glyph: no two items borrow each other's.
        assertEquals(expected.size, expected.values.map { it.name }.toSet().size)
    }

    private inner class Hub : AssistantRepository, MessageSender, MainConversationSender {
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main, conversations = listOf(
            Session(machineId = "mac", sessionId = "main", title = "Main", assistant = true),
            Session(machineId = "mac", sessionId = "side", title = "Trip plan", assistant = true, status = "working", cwd = "~/trips"),
        )))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun refresh() = Unit
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        suspend fun start() = SessionKey("mac", "new")
    }

    private class Composer : ChatComposerService {
        val interrupted = mutableListOf<SessionKey>()
        override val deliveries = MutableStateFlow(emptyMap<String, ComposerDelivery>())
        override suspend fun upload(session: SessionKey, name: String, file: File) = error("not used")
        override suspend fun interrupt(session: SessionKey) { interrupted += session }
        override suspend fun retryRefused(clientMessageId: String, clearDraft: Boolean) = error("not used")
    }

    @Test fun theMenuCopiesInterruptsAndSaysTheLinkIsDown() {
        val hub = Hub()
        val composer = Composer()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                CompositionLocalProvider(LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant")) {
                    AssistantScreen(navigator, hub, hub, hub::start, composerService = composer, mainConversationSender = hub,
                        requestedSession = side, connection = ConnectionState.WaitingForNetwork,
                        machines = listOf(Machine("mac", name = "MacBook", online = true)),
                        chatContent = { Text("chat:${it.sessionId}") })
                }
            }
        }
        rule.waitForIdle()
        fun menu() { rule.onNodeWithText("Persona").performClick(); rule.waitForIdle() }
        menu()
        // iOS's heading: the conversation's title and the launcher it runs on
        rule.onNodeWithText("Trip plan · claude").assertExists()
        for (item in listOf("Connection details", "Knowledge", "Details", "Back to main chat", "Copy directory", "Copy info",
            "Copy session ID", "Copy diagnostics", "Session info", "Interrupt")) rule.onNodeWithText(item).assertExists()
        rule.onAllNodesWithText("Assistant settings").assertCountEquals(0)
        rule.onNodeWithText("Copy directory").performClick()
        rule.waitForIdle()
        val clipboard = rule.activity.getSystemService(ClipboardManager::class.java)
        assertEquals("~/trips", clipboard.primaryClip?.getItemAt(0)?.text?.toString())
        menu()
        rule.onNodeWithText("Copy info").performClick()
        rule.waitForIdle()
        val info = clipboard.primaryClip?.getItemAt(0)?.text?.toString().orEmpty()
        assertEquals("Persona assistant chat “Trip plan”", info.lines().first())
        assertEquals("To continue on that machine: cd ~/trips && claude --resume side", info.lines().last())
        menu()
        rule.onNodeWithText("Interrupt").performClick()
        rule.waitUntil(5_000) { composer.interrupted.isNotEmpty() }
        assertEquals(listOf(side), composer.interrupted)
        menu()
        rule.onNodeWithText("Connection details").performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("connection-details").assertExists()
    }
}
