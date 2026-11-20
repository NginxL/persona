package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.material3.LocalRippleConfiguration
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.AgentHandoff
import persona.core.domain.tasks.HandoffWake
import persona.core.domain.tasks.TasksStore

/**
 * #8312: the hand-off card says the failure and the cut-off, Needs you and In progress carry the actions, and what just
 * finished is under Finished (#10922), where opening it keeps it. Native captures in build/quiet-task-shots.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h2400dp-xhdpi")
class QuietTasksUiTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    @After fun close() = scope.cancel()

    private val at = Instant.now().minusSeconds(3600).toString()
    private val cut = Session("desk", "cut", title = "Rewrite the intro", state = "idle",
        turnError = "The model service stopped responding.")
    private val resetAt = Instant.now().plusSeconds(3 * 3600).toString()
    private val limited = Session("desk", "limit", title = "Wait out the limit", state = "idle",
        turnError = "Weekly limit reached", turnErrorKind = "usage_limit", turnErrorResetsAt = resetAt)
    private val failed = Todo("failed", "Publish the notes", origin = "main", startError = "rpc: connection closed",
        doneAt = at, createdAt = at, updatedAt = at)
    private val interrupted = Todo("cut", "Rewrite the intro", origin = "main", createdAt = at, updatedAt = at,
        agents = listOf(TodoAgent("desk", "cut", linkedAt = at)))
    private val waiting = Todo("limit", "Wait out the limit", origin = "main", createdAt = at, updatedAt = at,
        agents = listOf(TodoAgent("desk", "limit", linkedAt = at)))
    private val finished = Todo("done", "Rename the button", origin = "main", doneAt = at, createdAt = at, updatedAt = at)
    private val receipt = FeedItem("receipt", "main", at, "receipt", "done", buildJsonObject { put("summary", "Renamed the button") })
    private val handoffs = listOf(
        AgentHandoff("cut", "Rewrite the intro", HandoffWake.Created, machineId = "desk", sessionId = "cut", todoId = "cut"),
        AgentHandoff("failed", "Publish the notes", HandoffWake.Created, todoId = "failed", failure = "rpc: connection closed"),
        AgentHandoff("limit", "Wait out the limit", HandoffWake.Created, machineId = "desk", sessionId = "limit", todoId = "limit"),
    )

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private class Api(var rows: List<Todo>, var items: List<FeedItem>) : TasksApi {
        val retried = mutableListOf<String>()
        val removed = mutableListOf<String>()
        val seen = mutableListOf<String>()
        override val features = MutableStateFlow(setOf("todo_seen"))
        override suspend fun todos() = rows
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(items)
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo { retried += todoId; return rows.first { it.id == todoId } }
        override suspend fun removeTask(todoId: String) { removed += todoId }
        override suspend fun setTodoSeen(id: String, seen: Boolean): Todo {
            this.seen += id
            return rows.first { it.id == id }.copy(seenAt = Instant.now().toString())
        }
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private fun ready(): Pair<Api, AssistantTaskHost> {
        val api = Api(listOf(failed, interrupted, waiting, finished), listOf(receipt))
        val store = TasksStore(api, Connection(), scope)
        store.reloadTodos()
        store.load("main")
        val host = AssistantTaskHost(store, "main", listOf(cut, limited), { it }, {}, { throw it }, {})
        return api to host
    }

    @Test fun theRowsActOnTheirOwnAndFinishedKeepsWhatWasOpened() {
        val (api, host) = ready()
        val tray = AssistantTrayState().also { it.expanded = AssistantTray.NeedsYou }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                AssistantTrays(host, tray, emptyList(), emptyList(), emptyList(), { 900.dp }, busy = false, enabled = true,
                    onApprove = {}, onReview = {}, onSend = {}, onSchedule = {})
            }
        }
        rule.onNodeWithText("Didn't start: The connection to this computer dropped", substring = true).assertDoesNotExist()
        rule.onNodeWithText("The connection to this computer dropped").assertIsDisplayed()
        rule.onNodeWithText("Retry").performClick()
        rule.runOnIdle { assertEquals(listOf("failed"), api.retried); assertNull(host.sheet.value) }
        rule.onNodeWithText("Remove").performClick()
        rule.runOnIdle { assertEquals(listOf("failed"), api.removed); assertNull(host.sheet.value) }
        rule.onNodeWithText("Continue").performClick()
        rule.runOnIdle { assertEquals(listOf("failed", "cut"), api.retried); assertNull(host.sheet.value) }
        rule.onNodeWithText("Publish the notes").performClick()
        rule.runOnIdle { assertEquals(TaskSheet.Task("failed"), host.sheet.value) }
        rule.runOnIdle { tray.expanded = AssistantTray.Running }
        rule.waitForIdle()
        // what finished is no longer under In progress: it has its own entry (#10922)
        rule.onNodeWithText("Just finished").assertDoesNotExist()
        rule.onNodeWithText("Renamed the button").assertDoesNotExist()
        rule.onNodeWithText("Usage Limit", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Weekly limit reached", substring = true).assertIsDisplayed()
        rule.onNodeWithText("Resets", substring = true).assertIsDisplayed()
        rule.onNodeWithTag("assistant-entry-running").assertIsDisplayed()
        rule.onNodeWithText("Continue").performClick()
        rule.runOnIdle { assertEquals(listOf("failed", "cut", "limit"), api.retried) }
        rule.onNodeWithTag("assistant-entry-finished").assertIsDisplayed().performClick()
        rule.waitForIdle()
        rule.onNodeWithTag("assistant-tray-finished").assertIsDisplayed()
        rule.onNodeWithTag("assistant-finished-row-new").assert(hasText("Just finished")).assert(hasText("Renamed the button"))
        rule.onNodeWithTag("assistant-finished-row-new").performClick()
        rule.runOnIdle {
            assertEquals(TaskSheet.Task("done"), host.sheet.value)
            assertEquals(listOf("done"), api.seen)
        }
        rule.waitForIdle()
        // seen, it stays: Done instead of Just finished, and the entry still lists it
        rule.onNodeWithTag("assistant-finished-row-new").assertDoesNotExist()
        rule.onNodeWithTag("assistant-finished-row").assert(hasText("Done")).assert(hasText("Renamed the button"))
        rule.onNodeWithTag("assistant-entry-finished").assertIsDisplayed()
    }

    private var style by mutableStateOf(AppStyle.Glass)
    private var dark by mutableStateOf(false)

    @Test fun shotsInEveryStyleLightAndDark() {
        val (_, host) = ready()
        val needs = AssistantTrayState().also { it.expanded = AssistantTray.NeedsYou }
        val running = AssistantTrayState().also { it.expanded = AssistantTray.Running }
        val finishedTray = AssistantTrayState().also { it.expanded = AssistantTray.Finished }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                CompositionLocalProvider(LocalRippleConfiguration provides null) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.m),
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        AgentHandoffCards(handoffs, host)
                        AssistantTrays(host, needs, emptyList(), emptyList(), emptyList(), { 700.dp }, busy = false, enabled = true,
                            onApprove = {}, onReview = {}, onSend = {}, onSchedule = {})
                        AssistantTrays(host, running, emptyList(), emptyList(), emptyList(), { 700.dp }, busy = false, enabled = true,
                            onApprove = {}, onReview = {}, onSend = {}, onSchedule = {})
                        AssistantTrays(host, finishedTray, emptyList(), emptyList(), emptyList(), { 700.dp }, busy = false, enabled = true,
                            onApprove = {}, onReview = {}, onSend = {}, onSchedule = {})
                    }
                }
            }
        }
        rule.onNodeWithText("Interrupted · The model service stopped responding.").assertIsDisplayed()
        rule.onNodeWithText("Didn't start: The connection to this computer dropped").assertIsDisplayed()
        assertTrue(rule.onAllNodesWithText("Usage Limit", substring = true).fetchSemanticsNodes().size >= 2)
        rule.onNodeWithText("Resets", substring = true).assertIsDisplayed()
        rule.onNodeWithText("resets", substring = true).assertIsDisplayed()
        val output = File("build/quiet-task-shots").also { it.mkdirs() }
        val light = mutableMapOf<AppStyle, Bitmap>()
        for (s in listOf(AppStyle.Glass, AppStyle.Brief, AppStyle.Board)) for (d in listOf(false, true)) {
            style = s; dark = d
            Snapshot.sendApplyNotifications()
            rule.waitForIdle()
            rule.onNodeWithTag("assistant-finished-row-new").assert(hasText("Just finished"))
            rule.onNodeWithText("Retry").assertIsDisplayed()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            if (!d) light[s] = bitmap.copy(Bitmap.Config.ARGB_8888, false)
            else assertFalse("Light and dark must differ for $s", light.getValue(s).sameAs(bitmap))
            File(output, "quiet-${s.name.lowercase()}-${if (d) "dark" else "light"}.png").outputStream()
                .use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        }
    }
}
