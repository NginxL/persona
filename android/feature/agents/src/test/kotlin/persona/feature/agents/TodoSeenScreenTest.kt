package persona.feature.agents

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.Direction
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.Supervision
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppFeedback
import persona.core.designsystem.AppStyle
import persona.core.designsystem.FeedbackAction
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TasksStore

/** Android's #5707 controls: synthetic to-dos only; every style, light/dark, and 200% text. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class TodoSeenScreenTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-05T09:30:00Z")
    private val notes = Todo("notes", title = "Write the release notes", notes = "Synthetic release summary",
        origin = "conv", createdAt = "2026-11-05T07:30:00Z", updatedAt = "2026-11-05T09:00:00Z",
        agents = listOf(TodoAgent(machineId = "m", sessionId = "notes")))
    private val noteSession = Session(machineId = "m", sessionId = "notes", state = "unread", status = "idle",
        updatedAt = "2026-11-05T09:00:00Z")
    private val open = Todo("open", title = "Back up the NAS", createdAt = "2026-11-05T08:00:00Z")

    private class Api(var rows: List<Todo>, supported: Boolean = true) : TasksApi {
        override val features = MutableStateFlow(if (supported) setOf("todo_seen") else emptySet())
        val sent = mutableListOf<Pair<String, Boolean>>()
        var gate: CompletableDeferred<Unit>? = null
        var failure: Throwable? = null
        override suspend fun todos(): List<Todo> = rows
        override suspend fun setTodoSeen(id: String, seen: Boolean): Todo {
            sent += id to seen
            gate?.await()
            failure?.let { throw it }
            val updated = rows.first { it.id == id }.copy(seenAt = if (seen) "2026-11-05T09:31:00Z" else null)
            rows = rows.map { if (it.id == id) updated else it }
            return updated
        }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse()
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = null
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Notes : AppFeedback {
        val failures = mutableListOf<Throwable>()
        val notices = mutableListOf<Pair<String, FeedbackAction?>>()
        override fun failure(error: Throwable, operation: Boolean) = true.also { failures += error }
        override fun notice(message: String) = true.also { notices += message to null }
        override fun notice(message: String, action: FeedbackAction) = true.also { notices += message to action }
    }

    private val feedback = Notes()
    private lateinit var store: TasksStore
    private var shotStyle by mutableStateOf(AppStyle.Glass)
    private var shotDark by mutableStateOf(false)
    private var shotHeight by mutableStateOf(900)
    private var detailId by mutableStateOf<String?>(null)

    private fun openList(api: Api, style: AppStyle = AppStyle.Glass, dark: Boolean = false, height: Int = 900,
                         directions: DirectionTree? = null) {
        store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.reloadTodos()
        shotStyle = style
        shotDark = dark
        shotHeight = height
        detailId = null
        rule.setContent {
            PersonaTheme(darkTheme = shotDark, reduceMotion = true, style = shotStyle) {
                CompositionLocalProvider(LocalAppFeedback provides feedback) {
                    val todos by store.todos.collectAsState()
                    Box(Modifier.testTag("shot").width(400.dp).height(shotHeight.dp).background(PersonaTheme.palette.background)) {
                        detailId?.let { id ->
                            TodoDetailScreen(id, todos, emptyList(), listOf(noteSession), store, onBack = {}, onOpenSession = {},
                                onHandOff = {}, now = now)
                        } ?: TodosScreen(todos, listOf(noteSession), store, onBack = {}, onOpenTodo = {}, now = now,
                            directions = directions)
                    }
                }
            }
        }
        rule.waitForIdle()
    }

    private fun openDetail(api: Api, id: String) {
        openList(api)
        rule.runOnIdle { detailId = id }
        rule.waitForIdle()
    }

    private fun sectionAbsent(name: String) =
        rule.onAllNodesWithText(name, substring = true).fetchSemanticsNodes().isEmpty()

    @Test fun gotItMovesAtOnceAndUndoRestoresJustFinished() {
        val api = Api(listOf(notes, open)).apply { gate = CompletableDeferred() }
        openList(api)
        rule.onNodeWithText("Got it").performClick()
        rule.waitForIdle()
        assertTrue("row left Just finished before the hub answered", sectionAbsent("Delivered · awaiting review"))
        assertEquals(listOf("notes" to true), api.sent)
        val (message, undo) = feedback.notices.single()
        assertEquals("Moved to Done", message)
        assertEquals("Undo", undo?.label)
        api.gate!!.complete(Unit)
        rule.waitForIdle()
        undo!!.run()
        rule.waitForIdle()
        assertEquals(listOf("notes" to true, "notes" to false), api.sent)
        assertTrue("Undo restored Just finished", !sectionAbsent("Delivered · awaiting review"))
        assertTrue(feedback.failures.isEmpty())
    }

    @Test fun openConcernGotItMovesToOtherOpenWithMatchingFeedback() {
        val api = Api(listOf(notes.copy(kind = "concern")))
        openList(api)
        rule.onNodeWithText("Got it").performClick()
        rule.waitForIdle()
        assertEquals(listOf("notes" to true), api.sent)
        assertEquals("Moved to Other open", feedback.notices.single().first)
        assertTrue("the open concern left Just finished", sectionAbsent("Delivered · awaiting review"))
        assertTrue("and joined Other open", !sectionAbsent("Other open"))
    }

    @Test fun aRefusedWritePutsTheRowBackAndSaysWhy() {
        val api = Api(listOf(notes)).apply { failure = IllegalStateException("hub unavailable") }
        openList(api)
        rule.onNodeWithText("Got it").performClick()
        rule.waitForIdle()
        assertTrue("the refused row is back", !sectionAbsent("Delivered · awaiting review"))
        assertEquals("hub unavailable", feedback.failures.single().message)
    }

    @Test fun aHubWithoutTheCapabilityOffersNeitherAction() {
        val api = Api(listOf(notes), supported = false)
        openList(api)
        assertTrue(sectionAbsent("Got it"))
        rule.runOnIdle { detailId = notes.id }
        rule.waitForIdle()
        assertTrue(api.sent.isEmpty())
    }

    @Test fun openingJustFinishedMarksItSeenSilently() {
        val api = Api(listOf(notes))
        openDetail(api, notes.id)
        assertEquals(listOf("notes" to true), api.sent)
        assertNotNull(store.todos.value.single().seenAt)
        assertTrue(feedback.notices.isEmpty())
        assertTrue(feedback.failures.isEmpty())
    }

    @Test fun openingAnotherSectionDoesNotMarkItSeen() {
        val api = Api(listOf(open))
        openDetail(api, open.id)
        assertTrue(api.sent.isEmpty())
    }

    @Test fun supervisedDeliveryOffersNoFalseDoneAction() {
        val supervised = notes.copy(supervision = Supervision(supervisor = "main", state = "pending_review"))
        val api = Api(listOf(supervised))
        openDetail(api, notes.id)
        assertTrue("Seen is not acceptance", sectionAbsent("Got it"))
        assertEquals(listOf("notes" to true), api.sent)
        assertTrue("still awaiting review", !sectionAbsent("Delivered · awaiting review"))
    }

    @Test fun directionGroupingOffersSeenOnlyForAnOrdinaryDelivery() {
        val supervised = notes.copy(id = "supervised", title = "Supervised delivery",
            supervision = Supervision(supervisor = "main", state = "pending_review"))
        val tree = DirectionTree(listOf(Direction("main", secretary = true, tasks = listOf(
            DirectionTask(notes.id, state = DirectionTaskState.Review),
            DirectionTask(supervised.id, state = DirectionTaskState.Review),
        ))))
        openList(Api(listOf(notes, supervised)), directions = tree)
        rule.onNodeWithTag("todos-filter").performClick()
        rule.onNodeWithText("Pinned").performClick()
        rule.waitForIdle()
        rule.onAllNodesWithText("Got it").assertCountEquals(1)
    }

    @Test fun englishCopy() {
        openList(Api(listOf(notes)))
        rule.onNodeWithText("Got it").assertExists()
    }

    @Config(sdk = [35], qualifiers = "zh-w400dp-h1400dp-xhdpi")
    @Test fun chineseCopy() {
        openList(Api(listOf(notes)))
        rule.onNodeWithText("知道了").assertExists()
    }

    @Config(sdk = [35], qualifiers = "ja-w400dp-h1400dp-xhdpi")
    @Test fun japaneseCopy() {
        openList(Api(listOf(notes)))
        rule.onNodeWithText("了解").assertExists()
    }

    @Test fun shotsInEveryStyleAndTheme() {
        openList(Api(listOf(notes, open)))
        for (style in AppStyle.entries) for (dark in listOf(false, true)) {
            rule.runOnIdle { shotStyle = style; shotDark = dark }
            shot(style, dark, false)
        }
    }

    @Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi", fontScale = 2f)
    @Test fun shotsAtTheLargestTextSize() {
        openList(Api(listOf(notes, open)), height = 1400)
        for (style in AppStyle.entries) for (dark in listOf(false, true)) {
            rule.runOnIdle { shotStyle = style; shotDark = dark }
            shot(style, dark, true)
        }
    }

    private fun shot(style: AppStyle, dark: Boolean, large: Boolean) {
        rule.onNodeWithText("Got it").assertExists()
        val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
        val out = File(System.getProperty("persona.shots") ?: "build/todo-seen-shots").also { it.mkdirs() }
        val name = "todos-seen-${style.name.lowercase()}-${if (dark) "dark" else "light"}${if (large) "-large" else ""}.png"
        File(out, name).outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
    }
}
