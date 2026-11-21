package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import coil3.ImageLoader
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import persona.core.contracts.BackgroundTask
import persona.core.contracts.FeedItem
import persona.core.contracts.FeedResponse
import persona.core.contracts.Item
import persona.core.contracts.ServerEvent
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.Workflow
import persona.core.contracts.WorkflowAgent
import persona.core.contracts.WorkflowPhaseStats
import persona.core.contracts.WorkflowTool
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.JWorkflowCallCard
import persona.core.designsystem.JWorkflowCard
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.WorkflowFolds
import persona.core.domain.lines.ChatLine
import persona.core.domain.lines.ToolCall
import persona.core.domain.lines.WorkItem
import persona.core.domain.tasks.AssistantExtra
import persona.core.domain.tasks.TasksStore
import java.io.File
import java.time.Instant

/**
 * A Workflow run's timeline card (#3435, as the web's #3434): in the chat where its call was, in an agent's details, and
 * its line on a task card, in every style and both appearances; a running run, a completed one, a failed one and a long
 * one (185 agents, of which the hub lists 60). PNGs in `build/workflow-shots/`.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "zh-w400dp-h2600dp-xhdpi")
class WorkflowShotsTest {
    @get:Rule val rule = createComposeRule()

    private fun out(): File = File(System.getProperty("persona.shots") ?: "build/workflow-shots").also { it.mkdirs() }

    private val now = Instant.now()
    private fun ago(seconds: Long) = now.minusSeconds(seconds).toString()

    private fun agent(id: String, label: String, phase: String, status: String, startedSecondsAgo: Long, ranSeconds: Long? = null,
                      tool: String? = null, model: String = "claude-example") = WorkflowAgent(
        id, "lead@$id", label, phase, running = status == "running", status = status, startedAt = ago(startedSecondsAgo),
        endedAt = ranSeconds?.let { ago(startedSecondsAgo - it) }, model = model,
        latestTool = tool?.let { WorkflowTool(name = "Read", summary = it) })

    /** widget-review: Scan done, Verify running two of five back, Report not started. */
    private val running = Workflow(taskId = "wfrun", runId = "wf_run", name = "widget-review", summary = "Review the widget block by block, then check each finding",
        status = "running", running = true, phases = listOf("Scan", "Verify", "Report"), phase = "Verify", phaseIndex = 2, phaseCount = 3,
        phaseStats = listOf(WorkflowPhaseStats("Scan", 4, 4), WorkflowPhaseStats("Verify", 5, 2, running = 3), WorkflowPhaseStats("Report")),
        toolUseId = "toolu_wf_run", agentsStarted = 9, agentsDone = 6, agentsRunning = 3, calls = 96, startedAt = ago(9 * 60 + 12),
        agents = listOf("timeline", "rain", "locale", "sizes").mapIndexed { i, area ->
            agent("s$i", "scan:$area", "Scan", "done", 540L - i * 12, 110L, "Reading WeatherWidget/$area.swift")
        } + listOf("timeline", "rain", "locale", "sizes", "a11y").mapIndexed { i, area ->
            agent("v$i", "verify:$area", "Verify", if (i < 2) "done" else "running", 360L - i * 24, if (i < 2) 200L else null, "Checking the $area formatting")
        })

    /** copy-audit: completed, three phases. */
    private val completed = Workflow(taskId = "wfdone", runId = "wf_done", name = "copy-audit", summary = "Read the settings copy page by page and fix what disagrees",
        status = "completed", running = false, phases = listOf("Read", "Fix", "Check"), phase = "Check", phaseIndex = 3, phaseCount = 3,
        phaseStats = listOf(WorkflowPhaseStats("Read", 3, 3), WorkflowPhaseStats("Fix", 3, 3), WorkflowPhaseStats("Check", 2, 2)),
        toolUseId = "toolu_wf_done", agentsStarted = 8, agentsDone = 8, startedAt = ago(150 * 60), endedAt = ago(128 * 60),
        agents = listOf("read:general" to "Read", "read:privacy" to "Read", "read:about" to "Read", "fix:general" to "Fix",
            "fix:privacy" to "Fix", "fix:about" to "Fix", "check:zh" to "Check", "check:en" to "Check").mapIndexed { i, (label, phase) ->
            agent("c$i", label, phase, "done", 9000L - i * 120, 240L)
        })

    /** snapshot-sweep: failed with two agents failed, Compare never started. */
    private val failed = Workflow(taskId = "wffail", runId = "wf_fail", name = "snapshot-sweep", summary = "Compare the widget's snapshots in every size, appearance and language",
        status = "failed", running = false, phases = listOf("Snapshot", "Compare"), phase = "Snapshot", phaseIndex = 1, phaseCount = 2,
        phaseStats = listOf(WorkflowPhaseStats("Snapshot", 6, 4, failed = 2), WorkflowPhaseStats("Compare")),
        toolUseId = "toolu_wf_failed", agentsStarted = 6, agentsDone = 4, agentsFailed = 2, startedAt = ago(70 * 60), endedAt = ago(62 * 60),
        error = "Error: 2 agents failed: snapshot:dark-large, snapshot:ja-small",
        agents = listOf("light-small" to "done", "light-large" to "done", "dark-small" to "done", "dark-large" to "failed",
            "ja-small" to "failed", "en-small" to "done").mapIndexed { i, (name, status) ->
            agent("f$i", "snapshot:$name", "Snapshot", status, 4200L - i * 30, 300L)
        })

    /** parity-audit: 185 agents, the hub lists the newest 60 (Audit #126–180, Verify 1–5). */
    private val long = Workflow(taskId = "wflong", runId = "wf_long", name = "parity-audit", summary = "Audit every difference from the App, then check each finding",
        status = "running", running = true, phases = listOf("Audit", "Verify"), phase = "Verify", phaseIndex = 2, phaseCount = 2,
        phaseStats = listOf(WorkflowPhaseStats("Audit", 180, 169, running = 11), WorkflowPhaseStats("Verify", 5, 0, running = 5)),
        toolUseId = "toolu_wf_long", agentsStarted = 185, agentsDone = 169, agentsRunning = 16, startedAt = ago(52 * 60),
        agents = (126..180).map { n -> agent("p$n", "audit:area-$n", "Audit", if (n > 169) "running" else "done", 3000L - n * 12, if (n > 169) null else 90L) } +
            (1..5).map { n -> agent("q$n", "verify:batch-$n", "Verify", "running", 300L - n * 30) })

    private val lead = Session(machineId = "mac", sessionId = "lead", title = "Audit the clients", cwd = "/w/persona", status = "idle",
        state = "working", agent = "claude", lastText = "Started parity-audit; I will report when it is done.",
        background = listOf(BackgroundTask("wflong", "workflow", runId = "wf_long")), workflows = listOf(long, completed))
    private val todo = Todo("todo1", "Audit the clients", createdAt = now.minusSeconds(1500).toString(), origin = "main",
        agents = listOf(TodoAgent("mac", "lead", cwd = "/w/persona")))

    private class Api(val todos: List<Todo>) : TasksApi {
        override suspend fun todos() = todos
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList(), emptyMap())
        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
        override suspend fun retryTask(todoId: String): Todo = error("unused")
        override suspend fun removeTask(todoId: String) = Unit
        override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("unused")
        override suspend fun today(conversation: String): FeedItem = error("unused")
        override suspend fun mainConversation(): String? = "main"
        override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
    }

    private class Connection : HubConnection {
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override fun reconnect() = Unit
        override fun send(message: JsonObject): Boolean = true
    }

    private fun shot(name: String, tag: String = "shot"): Bitmap {
        val bitmap = rule.onNodeWithTag(tag).captureToImage().asAndroidBitmap()
        File(out(), "$name.png").outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        return bitmap
    }

    @Test
    fun drawsTheCardsInEveryStyleAndAppearance() {
        val store = TasksStore(Api(listOf(todo)), Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.reloadTodos()
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        // the failed run opened by the reader, to show its marks; the others as their rules leave them
        val folds = WorkflowFolds().apply { this["workflow:wf_fail"] = true }
        rule.setContent {
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Column(Modifier.width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                    Column(Modifier.testTag("shot"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        JWorkflowCallCard(running, folds = folds, onOpenAgent = {})
                        JWorkflowCallCard(completed, folds = folds, onOpenAgent = {})
                        JWorkflowCallCard(failed, folds = folds, onOpenAgent = {})
                    }
                    Column(Modifier.testTag("long")) { JWorkflowCallCard(long, folds = folds, onOpenAgent = {}) }
                    Column(Modifier.testTag("detail"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                        JWorkflowCard(completed, folds = folds, onOpenAgent = {})
                        val host = AssistantTaskHost(store, "main", listOf(lead), { "Work laptop" }, {}, {}, {})
                        AssistantExtraLine(AssistantExtra.Feed(FeedItem("f1", "main", now.toString(), "task", "todo1")), host)
                    }
                }
            }
        }
        val images = mutableMapOf<String, Bitmap>()
        for (nextStyle in AppStyle.entries) for (nextDark in listOf(false, true)) {
            rule.runOnIdle { style = nextStyle; dark = nextDark }
            rule.waitForIdle()
            // the running card is open on its lit phase, the ended ones fold to their head and what they were for
            rule.onNodeWithText("当前 Verify：verify:a11y").assertIsDisplayed()
            rule.onNodeWithText("6/9+").assertIsDisplayed()
            rule.onNodeWithText("9 分").assertIsDisplayed()
            rule.onNodeWithText("2/5+").assertIsDisplayed()
            rule.onNodeWithText("verify:sizes").assertIsDisplayed()
            rule.onAllNodesWithText("Read the settings copy page by page and fix what disagrees").assertCountEquals(2)
            rule.onNodeWithText("Error: 2 agents failed: snapshot:dark-large, snapshot:ja-small").assertIsDisplayed()
            rule.onNodeWithText("2 个失败").assertIsDisplayed()
            // the long one: its Audit rows anchored to the end, what the hub no longer lists said above them
            rule.onNodeWithText("169/185+").assertIsDisplayed()
            rule.onNodeWithText("更早的 125 个没有列出").assertIsDisplayed()
            rule.onNodeWithText("还有 43 个").assertIsDisplayed()
            rule.onNodeWithText("后台 workflow「parity-audit」 · Verify 2/2 · 169/185+ · 52 分").assertIsDisplayed()
            val suffix = "${nextStyle.name.lowercase()}-${if (nextDark) "dark" else "light"}"
            images["cards-$suffix"] = shot("workflow-cards-$suffix")
            shot("workflow-long-$suffix", "long")
            shot("workflow-detail-$suffix", "detail")
        }
        // light and dark are drawn apart (#3350: a recomposition that never happened leaves two identical shots)
        AppStyle.entries.forEach { s ->
            val name = s.name.lowercase()
            assertNotEquals(name, images.getValue("cards-$name-light").getPixel(8, 8), images.getValue("cards-$name-dark").getPixel(8, 8))
        }
    }

    @Test
    fun phasesFoldByTheirRulesAndTheReaderOpensThem() {
        var opened: WorkflowAgent? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val folds = remember { WorkflowFolds() }
                // the long one first: opened in full it still fits the window
                Column(Modifier.width(400.dp)) {
                    JWorkflowCallCard(long, folds = folds, onOpenAgent = { opened = it })
                    JWorkflowCallCard(running, folds = folds, onOpenAgent = { opened = it })
                    JWorkflowCallCard(completed, folds = folds, onOpenAgent = { opened = it })
                }
            }
        }
        // a phase behind the run with all back folds; the lit one and one with agents running are open
        rule.onAllNodesWithText("scan:timeline").assertCountEquals(0)
        rule.onNodeWithText("verify:a11y").assertIsDisplayed()
        rule.onNodeWithText("Scan").performClick()
        rule.onNodeWithText("scan:timeline").assertIsDisplayed()
        // an ended run folds to its head: no phases until it is opened
        rule.onAllNodesWithTag("workflow-phase:Fix").assertCountEquals(0)
        rule.onNodeWithText("copy-audit").performClick()
        rule.onNodeWithTag("workflow-phase:Fix").assertIsDisplayed()
        // "n more" lists the whole block, and "Show fewer" folds it again
        rule.onAllNodesWithText("audit:area-126").assertCountEquals(0)
        rule.onNodeWithText("还有 43 个").performClick()
        rule.onNodeWithText("audit:area-126").assertIsDisplayed()
        rule.onNodeWithText("收起").performClick()
        rule.onAllNodesWithText("audit:area-126").assertCountEquals(0)
        // a row opens its agent
        rule.onNodeWithText("verify:a11y").performClick()
        assertEquals("lead@v4", opened?.sessionId)
    }

    private fun use(id: String, name: String, input: JsonObject) =
        Item(id, kind = "tool_use", role = "assistant", toolName = name, toolInput = input, toolUseId = id).asLineItem()
    private fun result(id: String, text: String) = Item("$id-r", kind = "tool_result", role = "user", text = text, toolUseId = id).asLineItem()
    private fun call(id: String, name: String, input: JsonObject = buildJsonObject {}) = WorkItem.Tool(ToolCall(use(id, name, input), result(id, "ok"), emptyList()))

    @Test fun drawsTheCardInsideTheExpandedChatTrace() = captureChatTrace("")

    @Test @Config(fontScale = 2f)
    fun drawsTheCardInsideTheExpandedChatTraceAtLargestText() = captureChatTrace("-large")

    private fun captureChatTrace(scale: String) {
        var openedRun: Workflow? = null
        val read = call("r1", "Read", buildJsonObject { put("file_path", "/w/WeatherWidget/Provider.swift") })
        val launch = call("toolu_wf_run", "Workflow", buildJsonObject { put("name", "widget-review") })
        val reply = Item("r#0", kind = "text", role = "assistant", text = "widget-review is running in the background; I will go on when it reports back.")
        var dark by mutableStateOf(false)
        var readOnly by mutableStateOf(false)
        var canOpenBackground by mutableStateOf(true)
        val session = lead.copy(workflows = listOf(running))
        rule.setContent {
            PersonaTheme(darkTheme = dark, reduceMotion = true) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                val open: ((BackgroundDestination) -> Unit)? = if (canOpenBackground) ({ destination ->
                    openedRun = session.workflows.orEmpty().firstOrNull { it.backgroundId() == (destination as? BackgroundDestination.Run)?.id }
                }) else null
                val subagents = rememberChatPageSubagents(session.key, ChatChromeBindings(), readOnly, session, open)
                CompositionLocalProvider(LocalChatWorkflows provides subagents.workflowCards) {
                    Column(Modifier.testTag("shot").width(400.dp).background(PersonaTheme.palette.background).padding(Tokens.Space.l)) {
                        ChatLineContent(ChatLine.Work("w-1", listOf(read, launch), live = false, start = null, end = null, open = true),
                            SessionKey("mac", "lead"), loader, recordedDuration = 75, onToggle = {}, onFold = {}, folding = emptySet(),
                            foldErrors = emptyMap(), onFullText = {})
                        ChatLineContent(ChatLine.Step("w-1", launch, first = false),
                            SessionKey("mac", "lead"), loader, recordedDuration = null, onToggle = {}, onFold = {}, folding = emptySet(),
                            foldErrors = emptyMap(), onFullText = {})
                        MessageContent(reply, user = false)
                    }
                }
            }
        }
        for (nextDark in listOf(false, true)) {
            rule.runOnIdle { dark = nextDark }
            rule.waitForIdle()
            rule.onNodeWithText("当前 Verify：verify:a11y").assertIsDisplayed()
            shot("workflow-chat-glass-${if (nextDark) "dark" else "light"}$scale")
        }
        val open = rule.onNodeWithText("在后台任务中打开")
        open.performClick()
        assertEquals(running.runId, openedRun?.runId)
        rule.runOnIdle { readOnly = true; openedRun = null }
        for (nextDark in listOf(false, true)) {
            rule.runOnIdle { dark = nextDark }
            open.assertIsDisplayed()
            shot("workflow-chat-read-only-glass-${if (nextDark) "dark" else "light"}$scale")
        }
        open.performClick()
        assertEquals(running.runId, openedRun?.runId)
        rule.runOnIdle { canOpenBackground = false }
        open.assertDoesNotExist()
        rule.onNodeWithTag("workflow-call").assertIsDisplayed()
    }

    @Test
    fun theChatDrawsARunsCardWhereItsCallWasAndTheRowWhereTheHubListsNoRun() {
        val read = call("r1", "Read", buildJsonObject { put("file_path", "/w/WeatherWidget/Provider.swift") })
        val launch = call("toolu_wf_run", "Workflow", buildJsonObject { put("name", "widget-review") })
        var runs by mutableStateOf(mapOf("toolu_wf_run" to running))
        var line by mutableStateOf<ChatLine>(ChatLine.Work("w-1", listOf(read, launch), live = false, start = null, end = null, open = false))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val context = LocalContext.current
                val loader = remember { ImageLoader.Builder(context).build() }
                val folds = remember { WorkflowFolds() }
                CompositionLocalProvider(LocalChatWorkflows provides runs.takeIf { it.isNotEmpty() }?.let { ChatWorkflows(it, folds, null) }) {
                    Column(Modifier.width(400.dp)) {
                        ChatLineContent(line, SessionKey("mac", "lead"), loader, recordedDuration = null, onToggle = {}, onFold = {},
                            folding = emptySet(), foldErrors = emptyMap(), onFullText = {})
                    }
                }
            }
        }
        // Tool-backed cards stay inside the summary for completed and running turns alike.
        rule.onAllNodesWithTag("workflow-call").assertCountEquals(0)
        rule.onNodeWithText("widget-review").assertDoesNotExist()
        rule.runOnIdle { line = ChatLine.Work("w-1", listOf(read, launch), live = true, start = null, end = null, open = false) }
        rule.onAllNodesWithTag("workflow-call").assertCountEquals(0)
        // the line open: the card is in its own place among the steps, and not under the line too
        rule.runOnIdle { line = ChatLine.Work("w-1", listOf(read, launch), live = false, start = null, end = null, open = true) }
        rule.onAllNodesWithTag("workflow-call").assertCountEquals(0)
        rule.runOnIdle { line = ChatLine.Step("w-1", launch, first = false) }
        rule.onAllNodesWithTag("workflow-call").assertCountEquals(1)
        // a hub that lists no such run (an older one, or the run is over a day old): the row it always was
        rule.runOnIdle { runs = emptyMap() }
        rule.onAllNodesWithTag("workflow-call").assertCountEquals(0)
        rule.runOnIdle { line = ChatLine.Work("w-1", listOf(read, launch), live = false, start = null, end = null, open = false) }
        rule.onAllNodesWithTag("workflow-call").assertCountEquals(0)
    }
}
