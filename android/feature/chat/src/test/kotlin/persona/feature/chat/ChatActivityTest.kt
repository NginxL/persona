package persona.feature.chat

import android.content.res.Resources
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.assert
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasContentDescription
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.isDialog
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.BackgroundTask
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.LoopInfo
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Workflow
import persona.core.contracts.WorkflowAgent
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.time.Instant
import java.time.ZoneId

/**
 * A loop and background work in an Agent's chat (#4339 C8, iOS `ActivityChips` and `ActivitySheet`): the words, then
 * the page. Glass shows a pill in the header whose sheet stops the loop and opens a task's output; Brief keeps its chips.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class ChatActivityTest {
    @get:Rule val rule = createComposeRule()

    private val res: Resources get() = ApplicationProvider.getApplicationContext<android.content.Context>().resources
    private val utc = ZoneId.of("UTC")
    private val now = Instant.parse("2026-11-12T14:00:00Z")

    @Test fun onlyGlassShowsThemInTheHeader() {
        assertTrue(ChatActivity.inHeader(AppStyle.Glass, topBar = true, ChatRenderStyle.Agent))
        assertFalse(ChatActivity.inHeader(AppStyle.Brief, topBar = true, ChatRenderStyle.Agent))
        assertFalse(ChatActivity.inHeader(AppStyle.Board, topBar = true, ChatRenderStyle.Agent))
        assertFalse(ChatActivity.inHeader(AppStyle.Glass, topBar = true, ChatRenderStyle.Assistant))
        assertFalse(ChatActivity.shown(Session("m", "s")))
        assertTrue(ChatActivity.shown(Session("m", "s", loop = LoopInfo(kind = "wakeup"))))
        assertTrue(ChatActivity.shown(Session("m", "s", background = listOf(BackgroundTask("b1", "shell")))))
    }

    @Test fun thePillSaysTheNextTimeOrRunning() {
        val next = LoopInfo(kind = "wakeup", nextAt = "2026-11-12T14:05:00Z", intervalSeconds = 1800)
        assertEquals("2:05 PM", ChatActivity.shortLabel(res, next, now, utc))
        assertEquals("Next 2:05 PM", ChatActivity.label(res, next, now, utc))
        val due = next.copy(nextAt = "2026-11-12T13:55:00Z")
        assertEquals("Running", ChatActivity.shortLabel(res, due, now, utc))
        assertEquals("Loop running", ChatActivity.label(res, due, now, utc))
        val unknown = LoopInfo(kind = "wakeup", intervalSeconds = 1800)
        assertEquals("Loop", ChatActivity.shortLabel(res, unknown, now, utc))
        assertEquals("Every 30 minutes", ChatActivity.label(res, unknown, now, utc))
        assertEquals("Looping, Next 2:05 PM, 2 background tasks, tap to view", ChatActivity.spoken(res, next, 2, now, utc))
        assertEquals("1 background task, tap to view", ChatActivity.spoken(res, null, 1, now, utc))
    }

    @Test fun theLoopsCadenceTimeLeftAndTask() {
        assertEquals("Every 90 seconds", ChatActivity.cadence(res, LoopInfo(kind = "wakeup", intervalSeconds = 90)))
        assertEquals("Every 60 minutes", ChatActivity.cadence(res, LoopInfo(kind = "wakeup", intervalSeconds = 3600))) // iOS: minutes up to 2 h
        assertEquals("Every 2 hours", ChatActivity.cadence(res, LoopInfo(kind = "wakeup", intervalSeconds = 7200)))
        assertEquals("Cron */5 * * * *", ChatActivity.cadence(res, LoopInfo(kind = "cron", cron = "*/5 * * * *")))
        assertEquals(null, ChatActivity.cadence(res, LoopInfo(kind = "wakeup")))
        assertEquals("4 minutes left", ChatActivity.until(res, now.plusSeconds(4 * 60 - 30), now))
        assertEquals("1 h 5 min left", ChatActivity.until(res, now.plusSeconds(65 * 60), now))
        assertEquals("2 hours left", ChatActivity.until(res, now.plusSeconds(120 * 60), now))
        assertEquals("check the CI", ChatActivity.task(LoopInfo(kind = "wakeup", prompt = "/loop check the CI")))
    }

    private val key = SessionKey("macbook", "weather")
    private val items = listOf(Item("u1", 1, role = "user", kind = "text", text = "Watch the CI and fix what fails.",
        ts = "2026-11-12T13:00:00Z"))
    private val session = Session(machineId = key.machineId, sessionId = key.sessionId, title = "Watch CI", status = "idle",
        state = "idle", agent = "claude", paneId = "p1", cwd = "/Users/me/weather",
        loop = LoopInfo(kind = "wakeup", prompt = "/loop check the CI", intervalSeconds = 1800,
            nextAt = Instant.now().plusSeconds(600).toString()),
        background = listOf(BackgroundTask("b1", "shell", description = "Run the test suite", command = "swift test",
            outputFile = "/tmp/tasks/b1.output", startedAt = Instant.now().minusSeconds(90).toString())))
    private val sessions = object : SessionsRepository {
        override val machines = MutableStateFlow(listOf(Machine("macbook", "MacBook", online = true)))
        override val sessions = MutableStateFlow(listOf(session))
        override suspend fun refresh() = Unit
    }
    private val chats = object : ChatRepository {
        override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(items = items, loading = false))
        override suspend fun loadOlder(session: SessionKey) = Unit
        override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
    }
    private val sent = mutableListOf<String>()
    private val sender = object : MessageSender {
        override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
        override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) { sent += text }
    }
    private val interactions = object : InteractionCenter {
        override val pending = MutableStateFlow(emptyList<Interaction>())
        override suspend fun refresh() = Unit
        override suspend fun resolve(id: String, resolution: Resolution) = Unit
    }
    private val opened = mutableListOf<String>()

    private fun page(style: AppStyle, historyReadOnly: () -> Boolean = { false }, onOpenSession: ((SessionKey) -> Unit)? = null) {
        rule.setContent {
            PersonaTheme(darkTheme = false, reduceMotion = true, style = style) {
                ChatScreen(key, chats, sender, interactions, sessions, onBack = {}, historyReadOnly = historyReadOnly(),
                    chrome = ChatChromeBindings(onOpenPath = { path, _ -> opened += path }, onOpenSession = onOpenSession))
            }
        }
        rule.waitUntil(20_000) { rule.onAllNodesWithText("Watch the CI", substring = true).fetchSemanticsNodes().isNotEmpty() }
    }

    private val pill = hasContentDescription("Looping, ", substring = true) and hasClickAction()

    @Test fun glassShowsThePillAndNotTheChips() {
        page(AppStyle.Glass)
        rule.onNode(pill).assertExists()
        rule.onNode(hasContentDescription("1 background task, tap to view", substring = true)).assertExists()
        // Above the composer only what the header doesn't say: no Loop or Background chip.
        assertTrue(rule.onAllNodes(hasText("Background 1", substring = true), useUnmergedTree = true).fetchSemanticsNodes().isEmpty())
    }

    @Test fun briefKeepsItsChipsAndNoPill() {
        page(AppStyle.Brief)
        assertTrue(rule.onAllNodes(pill).fetchSemanticsNodes().isEmpty())
        rule.onAllNodes(hasText("Background 1", substring = true), useUnmergedTree = true).fetchSemanticsNodes().let { assertFalse(it.isEmpty()) }
    }

    @Test fun stopLoopAsksThenSendsTheAgentIosWords() {
        page(AppStyle.Glass)
        rule.onNode(pill).performClick()
        rule.waitForIdle()
        rule.onNode(isDialog() and androidx.compose.ui.test.hasAnyDescendant(hasText("Background tasks"))).assertExists()
        rule.onNode(hasText("Stop Loop") and hasClickAction()).performClick()
        rule.waitForIdle()
        assertTrue("nothing goes before the confirmation", sent.isEmpty())
        rule.onNodeWithText("Stop this loop?").assertExists()
        rule.onNode(hasText("Stop Loop") and hasClickAction() and
            androidx.compose.ui.test.hasAnyAncestor(androidx.compose.ui.test.hasAnyDescendant(hasText("Stop this loop?")))).performClick()
        rule.waitUntil(5_000) { sent.isNotEmpty() }
        assertEquals(listOf(ChatActivity.STOP_LOOP), sent)
    }

    @Test fun aReadOnlyChatCanViewTheWorkflowWithoutOpeningItsAgents() {
        val readOnly = mutableStateOf(true)
        val agent = WorkflowAgent("verify", "weather@verify", label = "Verify forecast", phase = "Check", running = true)
        val workflow = Workflow(taskId = "workflow", runId = "verify-run", name = "Verify the release", running = true,
            status = "running", phase = "Check", phases = listOf("Check"), agents = listOf(agent))
        sessions.sessions.value = listOf(session.copy(workflows = listOf(workflow)))
        val destinations = mutableListOf<SessionKey>()
        page(AppStyle.Glass, historyReadOnly = { readOnly.value }, onOpenSession = { destinations += it })
        rule.onNode(pill).performClick()
        rule.onNode(hasText("Verify the release") and hasClickAction()).performClick()
        rule.onNodeWithTag("workflow-card").assertExists()
        val row = rule.onNodeWithTag("workflow-agent:verify")
        row.assertExists()
        row.assert(!hasClickAction())
        assertTrue(destinations.isEmpty())

        rule.runOnIdle { readOnly.value = false }
        row.performClick()
        assertEquals(listOf(SessionKey(key.machineId, agent.sessionId)), destinations)
    }

    @Test fun aTasksOutputOpensInTheFiles() {
        page(AppStyle.Glass)
        rule.onNode(pill).performClick()
        rule.waitForIdle()
        rule.onNode(hasText("Run the test suite", substring = true) and hasClickAction(), useUnmergedTree = false).performClick()
        rule.onNodeWithText("Open as file").performClick()
        rule.waitForIdle()
        assertEquals(listOf("/tmp/tasks/b1.output"), opened)
    }
}
