package persona.feature.assistant

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.activity.ComponentActivity
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createAndroidComposeRule
import androidx.compose.ui.unit.dp
import androidx.navigation3.runtime.NavKey
import androidx.test.ext.junit.runners.AndroidJUnit4
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
import persona.core.designsystem.icons.PhIcons
import persona.core.navigation.AppTab
import persona.core.navigation.Navigator
import persona.feature.chat.ChatComposer
import persona.feature.chat.rememberChatComposerState
import java.io.File

/**
 * Where Back from a conversation lands and New chat's page (#4339 C11), in every style, light and dark, and the Assistant
 * tab's menu on Glass, inside the shell's bar: the same steps on `main` and on the branch draw the before and after. The conversation itself is a stand-in (the
 * shared chat page needs the app's services); the page around it is the real one. Written as PNGs to
 * `build/no-home-shots/` (or `-Dpersona.shots=<dir>`). The real Archive / End comparison writes to
 * `build/archive-end-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp-xhdpi")
class NoHomePageShotsTest {
    @get:Rule val rule = createAndroidComposeRule<ComponentActivity>()
    private val main = SessionKey("mac", "main")
    private val side = SessionKey("mac", "side")
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) = Unit
        override fun back() = Unit
        override fun selectTab(tab: AppTab) = Unit
    }
    private val words = mapOf(
        "main" to Triple("Main chat", "What's on my calendar today?", "Two meetings: design review at 10:00 and a 1:1 at 15:30."),
        "side" to Triple("Trip plan", "Plan a weekend in the mountains", "Here is a two-day plan with a cabin near the trailhead."),
    )

    @Before fun setup() = AssistantManagerChoice.choose("hub", null, "assistant")
    @After fun cleanup() = AssistantManagerChoice.reset()

    private class Hub(main: SessionKey) : AssistantRepository, MessageSender, MainConversationSender, AssistantManagement {
        private val all = listOf(
            Session(machineId = "mac", sessionId = "main", title = "Main chat", assistant = true),
            Session(machineId = "mac", sessionId = "side", title = "Trip plan", assistant = true),
        )
        override val assistant = MutableStateFlow(AssistantSnapshot(mainSession = main, conversations = all, managerId = "assistant"))
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        val archived = mutableListOf<SessionKey>()
        val ended = mutableListOf<SessionKey>()
        override suspend fun refresh() {
            assistant.value = assistant.value.copy(conversations = all.filter { it.key !in archived && it.key !in ended })
        }
        override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        override suspend fun archiveConversation(session: SessionKey) { archived += session }
        override suspend fun archivedConversations(): List<Session> = all.filter { it.key in archived }.map { it.copy(archived = true) }
        override suspend fun endConversation(session: SessionKey) { ended += session }
        override suspend fun warm() = Unit
        override suspend fun tasks(): List<ScheduledTask> = emptyList()
        override suspend fun saveTask(task: ScheduledTask): ScheduledTask = error("Unexpected save")
        override suspend fun deleteTask(id: String) = error("Unexpected delete")
        override suspend fun runTask(id: String): SessionKey = error("Unexpected run")
        override suspend fun freshTask(id: String): ScheduledTask = error("Unexpected fresh")
        override suspend fun runs(): List<Run> = emptyList()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = error("Unexpected notes")
        override suspend fun memory(): List<MemoryFile> = emptyList()
        override suspend fun saveMemory(name: String, content: String) = error("Unexpected memory")
        override suspend fun deleteMemory(name: String) = error("Unexpected delete")
        suspend fun start() = SessionKey("mac", "new")
    }

    @Test fun backNewChatAndTheMenu() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var round by mutableIntStateOf(0)
        val hub = Hub(main)
        val sidebar = AssistantSidebarHost()
        val needs = NeedsYou(listOf(
            NeedsYouLine.Agent(SessionKey("mac", "agent"), "r1", "plan", null, who = "weather-app", what = "Plan ready"),
            NeedsYouLine.Inbox("i1", lark = false, itemKind = "mail", who = "Alex", what = "Re: Q4 budget"),
        ), more = 1)
        // Glass draws them over the composer (#4339 C17), all of them, as many as fit.
        val shellNeeds = needs.lines + NeedsYouLine.Inbox("i2", lark = false, itemKind = "mail", who = "Sam", what = "Lunch on Friday?")
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                // The shell's bar and its one sidebar, as the app shows the page (Root.kt).
                val chrome = HomeChrome(listOf(NavItem("Persona", "Assistant", PhIcons.Sparkle), NavItem("Agents", "Agents", PhIcons.Terminal)),
                    "Persona", {}, { JPersonaButton(PersonaState.Idle, "Chat history", {}) })
                CompositionLocalProvider(LocalConfiguration provides configuration, LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant"), LocalHomeChrome provides chrome, LocalAssistantSidebarHost provides sidebar) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        key(round) {
                            AssistantScreen(navigator, hub, hub, hub::start, composerService = null, mainConversationSender = hub,
                                requestedSession = side, needsYou = needs, shellNeedsYou = shellNeeds, onOpenNeed = {}, onOpenInbox = {},
                                machines = listOf(Machine("mbp", name = "MacBook Pro", online = true)), phoneToolsAvailable = true,
                                chatContent = { key -> StandInChat(key) })
                        }
                    }
                }
            }
        }
        fun capture(name: String, dialog: Boolean = false) {
            rule.waitForIdle()
            val node = if (dialog) rule.onNode(isDialog()) else rule.onNodeWithTag("shot")
            saveCapture(name, node.captureToImage().asAndroidBitmap())
        }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d; round++
            rule.waitForIdle()
            val name = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            rule.onNodeWithText("Plan a weekend in the mountains").assertExists()
            capture("$name-1-conversation")
            back()
            capture("$name-2-back")
            rule.onNodeWithContentDescription("New chat").performClick()
            capture("$name-3-new-chat")
            // The menu opens from the selected tab on Glass (Brief and Board's switcher has none).
            if (s != AppStyle.Glass) continue
            round++
            rule.waitForIdle()
            rule.onNodeWithText("Assistant").performClick()
            capture("$name-4-menu", dialog = true)
            back()
        }
    }

    /**
     * Archive and End act through the real row menus and state holders. Archive leaves Trip plan on screen; End replaces it
     * with Main chat. Every style, light and dark, with the sheet and page composited into the same PNG.
     */
    @Test
    @Config(qualifiers = "en-w400dp-h1200dp-xhdpi")
    fun archiveAndEndOnTheOpenConversation() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var round by mutableIntStateOf(0)
        var hub by mutableStateOf(Hub(main))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                CompositionLocalProvider(LocalConfiguration provides configuration, LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant")) {
                    Box(Modifier.testTag("shot").width(400.dp).height(1200.dp).background(PersonaTheme.palette.background)) {
                        key(round) {
                            AssistantScreen(navigator, hub, hub, hub::start, composerService = null, mainConversationSender = hub,
                                management = hub, requestedSession = side, chatContent = { key -> StandInChat(key) })
                        }
                    }
                }
            }
        }
        fun reset() {
            hub = Hub(main)
            round++
            rule.waitForIdle()
            rule.onNodeWithText("Plan a weekend in the mountains").assertExists()
        }
        fun openList() {
            rule.onNodeWithContentDescription("Chats").performClick()
            rule.waitForIdle()
            rule.onAllNodesWithContentDescription("Chat actions").assertCountEquals(2)
        }
        fun capture(name: String) = captureAllWindows(name, "archive-end-shots")
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            reset()
            openList()
            rule.onAllNodesWithContentDescription("Chat actions")[1].performClick()
            rule.onNodeWithText("Archive").performClick()
            rule.waitUntil(5_000) { hub.archived == listOf(side) }
            rule.waitForIdle()
            rule.onAllNodesWithContentDescription("Chat actions").assertCountEquals(1)
            rule.onNodeWithText("Archived (1)").assertExists()
            rule.onNodeWithText("Archived").assertExists()
            rule.onAllNodesWithText("Trip plan").assertCountEquals(1)
            rule.onNodeWithText("Plan a weekend in the mountains").assertExists()
            rule.onNodeWithText("Here is a two-day plan with a cabin near the trailhead.").assertExists()
            val name = "${s.name.lowercase()}-${if (d) "dark" else "light"}"
            capture("archive-end-$name-1-archive-list")
            back()
            rule.onAllNodesWithText("Trip plan").assertCountEquals(1)
            rule.onNodeWithText("Plan a weekend in the mountains").assertExists()
            capture("archive-end-$name-2-archive-chat")

            reset()
            openList()
            rule.onAllNodesWithContentDescription("Chat actions")[1].performClick()
            rule.onNodeWithText("End…").performClick()
            rule.onNodeWithText("End chat").performClick()
            rule.waitUntil(5_000) { hub.ended == listOf(side) }
            rule.waitForIdle()
            rule.onAllNodesWithContentDescription("Chat actions").assertCountEquals(1)
            rule.onAllNodesWithText("Trip plan").assertCountEquals(0)
            rule.onAllNodesWithText("Main chat").assertCountEquals(2)
            rule.onNodeWithText("What's on my calendar today?").assertExists()
            rule.onNodeWithText("Two meetings: design review at 10:00 and a 1:1 at 15:30.").assertExists()
            rule.onAllNodesWithText("Archived (1)").assertCountEquals(0)
            capture("archive-end-$name-3-end-list")
            back()
            rule.onAllNodesWithText("Main chat").assertCountEquals(1)
            rule.onNodeWithText("What's on my calendar today?").assertExists()
            capture("archive-end-$name-4-end-main")
        }
    }

    /**
     * The page that starts the main chat, with no main conversation yet (iOS `ShellHome`): its lead says "This is your main
     * chat", where New chat's says "Start a new side chat" (#4339 C18). Every style, light and dark.
     */
    @Test fun theBlankPageThatStartsTheMainChat() {
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        val hub = object : AssistantRepository, MessageSender, MainConversationSender {
            override val assistant = MutableStateFlow(AssistantSnapshot())
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun refresh() = Unit
            override suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?) = Unit
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
        }
        val sidebar = AssistantSidebarHost()
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                val configuration = Configuration(LocalConfiguration.current).apply {
                    uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                        if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
                }
                val chrome = HomeChrome(listOf(NavItem("Persona", "Assistant", PhIcons.Sparkle), NavItem("Agents", "Agents", PhIcons.Terminal)),
                    "Persona", {}, { JPersonaButton(PersonaState.Idle, "Chat history", {}) })
                CompositionLocalProvider(LocalConfiguration provides configuration, LocalManagerRoster provides ManagerRoster(listOf(
                    HubManager("assistant", "Helper", online = true, isDefault = true),
                ), "assistant"), LocalHomeChrome provides chrome, LocalAssistantSidebarHost provides sidebar) {
                    Box(Modifier.testTag("shot").width(400.dp).height(900.dp).background(PersonaTheme.palette.background)) {
                        AssistantScreen(navigator, hub, hub, { SessionKey("mac", "new") }, composerService = null, mainConversationSender = hub,
                            machines = listOf(Machine("mbp", name = "MacBook Pro", online = true)), phoneToolsAvailable = true,
                            chatContent = { key -> StandInChat(key) })
                    }
                }
            }
        }
        val out = File(System.getProperty("persona.shots") ?: "build/no-home-shots").also { it.mkdirs() }
        for (s in AppStyle.entries) for (d in listOf(false, true)) {
            style = s; dark = d
            rule.waitForIdle()
            rule.onNodeWithText("This is your main chat", substring = true).assertExists()
            rule.onAllNodesWithText("Start a new side chat", substring = true).assertCountEquals(0)
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            File(out, "no-main-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }

    private fun saveCapture(name: String, bitmap: Bitmap, directory: String = "no-home-shots") {
        val out = File(System.getProperty("persona.shots") ?: "build/$directory").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }

    private fun back() {
        rule.runOnIdle { rule.activity.onBackPressedDispatcher.onBackPressed() }
        rule.waitForIdle()
    }

    private fun captureAllWindows(name: String, directory: String) {
        rule.waitForIdle()
        saveCapture(name, rule.runOnIdle { captureWindows() }, directory)
    }

    /** Draw the real page and sheet windows together; capturing only the page node omits the sheet. */
    @Suppress("UNCHECKED_CAST")
    private fun captureWindows(): Bitmap {
        val global = Class.forName("android.view.WindowManagerGlobal")
        val instance = global.getMethod("getInstance").invoke(null)
        val views = global.getDeclaredField("mViews").apply { isAccessible = true }.get(instance) as List<android.view.View>
        val bitmap = Bitmap.createBitmap(views.first().width, views.first().height, Bitmap.Config.ARGB_8888)
        val canvas = android.graphics.Canvas(bitmap)
        views.forEach { view ->
            val position = IntArray(2).also { view.getLocationOnScreen(it) }
            canvas.save(); canvas.translate(position[0].toFloat(), position[1].toFloat()); view.draw(canvas); canvas.restore()
        }
        return bitmap
    }

    /** The conversation as the shared chat page draws it, roughly: a question, an answer, the composer under them. */
    @Composable
    private fun StandInChat(key: SessionKey) {
        val (title, asked, answer) = words.getValue(key.sessionId)
        val insets = LocalChromeInsets.current
        Column(Modifier.fillMaxSize().padding(top = insets.top, bottom = insets.bottom)) {
            Column(Modifier.weight(1f).fillMaxWidth().padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.l),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                Text(title, Modifier.align(Alignment.CenterHorizontally), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
                Spacer(Modifier.weight(1f))
                UserBubble(asked)
                Text(answer, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
            }
            ChatComposer(key, rememberChatComposerState(), working = false, sending = false, canClear = false, service = null,
                onSend = {}, onCommand = {}, onNotice = {}, placeholder = "Ask Persona…")
        }
    }
}
