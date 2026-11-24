package persona.feature.chat

import android.content.res.Configuration
import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithContentDescription
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.text.input.TextFieldValue
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.height
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.serialization.json.JsonObject
import org.junit.After
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.QueueEdit
import persona.core.contracts.Resolution
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.api.AssistantConfig
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantNotes
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MemoryFile
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.contracts.api.SessionsRepository
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JChipTokens
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.TasksStore
import java.io.File
import java.time.Instant

/**
 * Verifies the compacted main-chat bottom chrome stack (status tray capsules, queued message strip, launcher/mode
 * chips, input field, and expanded quick-action toolbar) across Glass, Brief, and Board, in light/dark themes, at
 * 1.0x and 2.0x font scale, and in a short IME-constrained viewport.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h760dp-xhdpi")
class ChatBottomChromeCompactTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)

    @After
    fun tearDown() {
        scope.cancel()
    }

    private val main = SessionKey("computer-a", "main")
    private val nowIso = Instant.now().minusSeconds(1800).toString()
    private val running = Session(
        "computer-a", "work-1", title = "Check API migration", cwd = "/work/app",
        state = "working", status = "working", activity = "Running tests", updatedAt = nowIso,
    )
    private val blocked = Session(
        "computer-a", "need-1", title = "Confirm rollout", cwd = "/work/app",
        state = "needs_you", status = "blocked", attentionReason = "Waiting for confirmation", updatedAt = nowIso,
    )
    private val todos = listOf(
        Todo("t-need", blocked.title, origin = "main", createdAt = nowIso, updatedAt = nowIso,
            agents = listOf(TodoAgent(blocked.machineId, blocked.sessionId, cwd = blocked.cwd))),
        Todo("t-run", running.title, origin = "main", createdAt = nowIso, updatedAt = nowIso,
            agents = listOf(TodoAgent(running.machineId, running.sessionId, cwd = running.cwd))),
        Todo("t-done", "Summarize notes", origin = "main", createdAt = nowIso, updatedAt = nowIso, doneAt = nowIso),
    )
    private val personaSession = JsonCodec.decodeFromString<Session>(
        """{"machine_id":"computer-a","session_id":"main","title":"Persona","assistant":true,"agent":"claude",
            "launcher":"agy","permission_mode":"bypassPermissions","effort":"medium","status":"working","state":"working",
            "queued":["Check the release checklist and regression results"]}""",
    )

    private val api = object : TasksApi {
        override suspend fun todos() = todos
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
        override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
        override suspend fun retryTask(todoId: String): Todo = error("not used")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
        override suspend fun today(conversation: String): FeedItem = error("not used")
        override suspend fun mainConversation() = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }
    private val connection = object : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }
    private val sessions = object : SessionsRepository {
        override val sessions = MutableStateFlow(listOf(personaSession, running, blocked))
        override val machines = MutableStateFlow(listOf(Machine("computer-a", "Computer A", online = true)))
        override suspend fun refresh() = Unit
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = flowOf(
            ChatSnapshot(
                loading = false,
                items = listOf(
                    Item("u1", 1, role = "user", kind = "text", text = "Please check this change."),
                    Item("a1", 2, role = "assistant", kind = "text", text = "Reviewing the latest commit and test results now."),
                ),
            ),
        )
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
    }
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = Unit
    }
    private val actions = object : ChatActions {
        override suspend fun isMuted(session: Session) = false
        override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = ChatActionResult.Done
        override suspend fun editQueue(session: Session, keep: List<Int>): QueueEdit = QueueEdit(session.queued.orEmpty())
        override suspend fun sendQueuedNow(session: Session) = Unit
    }
    private val management = object : AssistantManagement {
        override suspend fun config() = AssistantConfig(main = "main", launcher = "agy")
        override suspend fun tasks() = emptyList<ScheduledTask>()
        override suspend fun warm() = Unit
        override suspend fun endConversation(session: SessionKey) = Unit
        override suspend fun saveTask(task: ScheduledTask) = task
        override suspend fun deleteTask(id: String) = Unit
        override suspend fun runTask(id: String) = main
        override suspend fun freshTask(id: String) = ScheduledTask(id)
        override suspend fun runs() = emptyList<Run>()
        override suspend fun notes() = AssistantNotes()
        override suspend fun setNotes(text: String) = Unit
        override suspend fun memory() = emptyList<MemoryFile>()
        override suspend fun saveMemory(name: String, content: String) = Unit
        override suspend fun deleteMemory(name: String) = Unit
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(5) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(300)
        rule.waitForIdle()
    }

    private fun saveShot(name: String) {
        val output = File(System.getProperty("persona.shots") ?: "build/bottom-chrome-compact-shots").also { it.mkdirs() }
        val bitmap = rule.onNodeWithTag("bottom-chrome-shot").captureToImage().asAndroidBitmap()
        File(output, "$name.png").outputStream().use {
            assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it))
        }
    }

    @Test
    fun stackedBottomChromeStaysCompactUnclippedAndAccessibleAcrossStylesLargeTextAndIme() {
        val tasks = TasksStore(api, connection, scope).also { it.reloadTodos() }
        var style by mutableStateOf(AppStyle.Brief)
        var dark by mutableStateOf(true)
        var fontScale by mutableStateOf(1f)
        var viewportHeight by mutableStateOf(760)
        val composer = ChatComposerState().apply {
            focused = true
            value = TextFieldValue("")
        }
        rule.mainClock.autoAdvance = false
        rule.setContent {
            val baseDensity = LocalDensity.current
            val configuration = Configuration(LocalConfiguration.current).apply {
                this.fontScale = fontScale
                screenHeightDp = viewportHeight
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or
                    if (dark) Configuration.UI_MODE_NIGHT_YES else Configuration.UI_MODE_NIGHT_NO
            }
            CompositionLocalProvider(
                LocalConfiguration provides configuration,
                LocalDensity provides Density(baseDensity.density, fontScale),
            ) {
                PersonaTheme(darkTheme = dark, reduceMotion = true, style = style) {
                    Box(
                        Modifier.width(400.dp).height(viewportHeight.dp)
                            .background(PersonaTheme.palette.background)
                            .testTag("bottom-chrome-shot"),
                    ) {
                        key(style, dark, fontScale, viewportHeight) {
                            ChatScreen(
                                main, chats, sender, interactions, sessions, {},
                                chrome = ChatChromeBindings(
                                    actions = actions,
                                    connection = connection,
                                    tasks = tasks,
                                    assistantManagement = management,
                                    onOpenSession = {},
                                ),
                                renderStyle = ChatRenderStyle.Assistant,
                                embedded = true,
                                showTopBar = false,
                                composerState = composer,
                            )
                        }
                    }
                }
            }
        }
        settle()
        rule.waitUntil(10_000) { rule.onAllNodesWithTag("assistant-tray-bar").fetchSemanticsNodes().isNotEmpty() }

        // 1. Standard 1.0x across all 3 styles, light & dark (with expanded composer toolbar and queued message).
        for (s in AppStyle.entries) for (isDark in listOf(false, true)) {
            style = s; dark = isDark; fontScale = 1f; viewportHeight = 760
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            // Expand Glass queue so we inspect both folded and expanded states.
            if (s == AppStyle.Glass) {
                saveShot("stacked-folded-${s.name.lowercase()}-${if (isDark) "dark" else "light"}")
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick()
                settle()
            }
            val trayBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val queueItem = rule.onNodeWithContentDescription("Item 1: Check the release checklist and regression results").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val add = rule.onNodeWithContentDescription("Add attachments and quick messages").getUnclippedBoundsInRoot()
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()

            assertTrue("$s dark=$isDark: tray bar above queue row", trayBar.bottom <= queueItem.top)
            assertTrue("$s dark=$isDark: queue row above input field", queueItem.bottom <= field.top)
            assertTrue("$s dark=$isDark: input field above bottom toolbar", field.bottom <= add.top)
            assertTrue("$s dark=$isDark: add button touch height >= 48dp", add.height >= Tokens.MinTouch.android)
            val minQueueRow = if (s == AppStyle.Glass) JChipTokens.height else Tokens.Size.slimBar
            assertTrue("$s dark=$isDark: queue row height >= $minQueueRow", queueItem.height >= minQueueRow)
            assertTrue("$s dark=$isDark: toolbar stays inside viewport", add.bottom <= root.bottom)

            // Total bottom chrome height stays well below 45% of 760dp (342dp), even with expanded toolbar + chips + queue.
            val totalChrome = root.bottom - trayBar.top
            val maxAllowed = if (s == AppStyle.Glass) 295.dp else 335.dp
            assertTrue("$s dark=$isDark: total bottom chrome ($totalChrome) <= $maxAllowed", totalChrome <= maxAllowed)

            // Transcript remains clearly visible above the bottom stack.
            val reply = rule.onNodeWithText("Reviewing the latest commit and test results now.").getUnclippedBoundsInRoot()
            assertTrue("$s dark=$isDark: transcript sits above tray bar", reply.bottom <= trayBar.top)

            saveShot("stacked-open-${s.name.lowercase()}-${if (isDark) "dark" else "light"}")
        }

        // 2. 2.0x system font scale across all 3 styles (no overlap, no clipping).
        for (s in AppStyle.entries) {
            style = s; dark = true; fontScale = 2f; viewportHeight = 760
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            if (s == AppStyle.Glass) {
                rule.onAllNodesWithContentDescription("queued · Persona", substring = true)[0].performClick()
                settle()
            }
            val trayBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val queueItem = rule.onNodeWithContentDescription("Item 1: Check the release checklist and regression results").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val add = rule.onNodeWithContentDescription("Add attachments and quick messages").getUnclippedBoundsInRoot()
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()
            assertTrue("$s 2.0x: tray bar above queue row", trayBar.bottom <= queueItem.top)
            assertTrue("$s 2.0x: queue row above input field", queueItem.bottom <= field.top)
            assertTrue("$s 2.0x: input field above bottom toolbar", field.bottom <= add.top)
            assertTrue("$s 2.0x: add button stays inside viewport", add.bottom <= root.bottom)
            rule.onNodeWithContentDescription("Add attachments and quick messages").assertIsDisplayed()
            saveShot("stacked-large-2x-${s.name.lowercase()}-dark")
        }

        // 3. Short IME-constrained viewport (420dp remaining height) across all 3 styles.
        for (s in AppStyle.entries) {
            style = s; dark = true; fontScale = 1f; viewportHeight = 420
            settle()
            rule.onNode(hasSetTextAction()).performClick()
            settle()
            val trayBar = rule.onNodeWithTag("assistant-tray-bar").getUnclippedBoundsInRoot()
            val field = rule.onNode(hasSetTextAction()).getUnclippedBoundsInRoot()
            val add = rule.onNodeWithContentDescription("Add attachments and quick messages").getUnclippedBoundsInRoot()
            val root = rule.onNodeWithTag("bottom-chrome-shot").getUnclippedBoundsInRoot()
            assertTrue("$s IME: tray bar stays inside viewport top", trayBar.top >= root.top)
            assertTrue("$s IME: tray bar above input field", trayBar.bottom <= field.top)
            assertTrue("$s IME: input field above bottom toolbar", field.bottom <= add.top)
            assertTrue("$s IME: bottom toolbar stays inside viewport", add.bottom <= root.bottom)
            rule.onNodeWithContentDescription("Add attachments and quick messages").assertIsDisplayed()
            saveShot("stacked-ime-${s.name.lowercase()}-dark")
        }
    }
}
