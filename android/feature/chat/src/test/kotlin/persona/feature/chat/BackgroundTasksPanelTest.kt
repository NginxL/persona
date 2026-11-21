package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.test.hasClickAction
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.hasText
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.compose.ui.text.input.TextFieldValue
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.BackgroundTask
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.Resolution
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Workflow
import persona.core.contracts.api.ChatAction
import persona.core.contracts.api.ChatActionResult
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.ChatSnapshot
import persona.core.contracts.api.InteractionCenter
import persona.core.contracts.api.MessageSender
import persona.core.contracts.api.OutboxMessage
import persona.core.contracts.api.SessionFiles
import persona.core.contracts.api.SessionLogTail
import persona.core.contracts.api.SessionsRepository
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import java.time.Instant
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.atomic.AtomicBoolean

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h820dp-xhdpi")
class BackgroundTasksPanelTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.now()
    private val active = BackgroundTask("cmd", "monitor", description = "Watch the build", command = "tail -f build.log",
        startedAt = now.minusSeconds(90).toString())
    private val run = Workflow(taskId = "flow", runId = "run", name = "Verify build", status = "running",
        toolUseId = "call-flow", agentsStarted = 2, agentsDone = 1, startedAt = now.minusSeconds(80).toString())
    private val owner = Session("build-box", "worker", title = "Build Agent", manager = "own-manager", cwd = "/project",
        status = "idle", state = "idle", agent = "claude", background = listOf(active), workflows = listOf(run))

    private fun waitText(text: String, substring: Boolean = false) {
        rule.waitUntil(10_000) { rule.onAllNodes(hasText(text, substring = substring)).fetchSemanticsNodes().isNotEmpty() }
    }

    @Test fun lateOutputAndARetainedOutcomeUpdateTheOpenMonitorInPlace() {
        val shown = mutableStateOf(owner)
        val requests = CopyOnWriteArrayList<List<String?>>()
        val fail = AtomicBoolean()
        val endedRead = AtomicBoolean()
        val files = object : FixtureBackgroundFiles() {
            override suspend fun readLogTail(machineId: String, cwd: String, path: String, manager: String?): SessionLogTail {
                requests += listOf(machineId, cwd, path, manager)
                if (fail.get()) error("temporary read failure")
                return SessionLogTail(if (endedRead.get()) "Final result.\n" else "Checking progress.\n", false)
            }
        }
        val request = BackgroundPanelRequest(owner.key, owner.manager, BackgroundDestination.List)
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                BackgroundTasksPanel(request, shown.value, null, null, false, files, null, null, null, {})
            }
        }
        rule.onNode(hasText("Watch the build") and hasClickAction()).performClick()
        waitText("No output yet")
        assertTrue(requests.isEmpty())
        val output = active.copy(outputFile = "/project/logs/result.txt")
        rule.runOnIdle { shown.value = owner.copy(background = listOf(output)) }
        waitText("Checking progress.", substring = true)
        assertEquals(listOf(owner.machineId, owner.cwd, output.outputFile, owner.manager), requests.first())
        val finished = output.copy(status = "failed", exitCode = 5, endedAt = now.toString())
        rule.runOnIdle {
            fail.set(true)
            shown.value = owner.copy(background = listOf(output), backgroundEnded = listOf(finished))
        }
        waitText("Exit code")
        rule.onNodeWithText("5").assertExists()
        rule.onNodeWithText("Failed").assertExists()
        waitText("Could not read output.")
        rule.onNodeWithText("Checking progress.", substring = true).assertExists()
        rule.runOnIdle {
            fail.set(false)
            endedRead.set(true)
        }
        rule.onNodeWithText("Retry").performClick()
        waitText("Final result.", substring = true)
        rule.onNodeWithContentDescription("Back").performClick()
        waitText("Completed")
        rule.onNode(hasText("Watch the build") and hasClickAction()).assertExists()
    }

    @Test fun summarizesAFullRowWithoutLosingTheOpenDetailDuringAnExactReadFailure() {
        val task = active.copy(outputFile = "/project/logs/result.txt")
        val full = owner.copy(background = listOf(task))
        val summary = full.copy(waiting = false, background = null, workflows = null)
        val shown = mutableStateOf(full)
        val reads = CopyOnWriteArrayList<Session>()
        val failed = CompletableDeferred<Session>()
        val recovered = CompletableDeferred<Session>()
        val actions = object : ChatActions {
            override suspend fun isMuted(session: Session) = false
            override suspend fun execute(session: Session, action: ChatAction): ChatActionResult = error("unexpected chat action")
            override suspend fun verify(session: Session, action: ChatAction): ChatActionResult = error("unexpected chat verification")
            override suspend fun completeRow(session: Session): Session {
                reads += session
                return if (reads.size == 1) failed.await() else recovered.await()
            }
        }
        val files = object : FixtureBackgroundFiles() {
            override suspend fun readLogTail(machineId: String, cwd: String, path: String, manager: String?) =
                SessionLogTail("Checking progress.\n", false)
        }
        val request = BackgroundPanelRequest(owner.key, owner.manager, BackgroundDestination.Command(task.id, task))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                BackgroundTasksPanel(request, shown.value, actions, null, true, files, null, null, null, {})
            }
        }
        waitText("Checking progress.", substring = true)
        rule.runOnIdle { shown.value = summary }
        rule.waitUntil(10_000) { reads.size == 1 }
        assertEquals(summary, reads.single())
        rule.onNodeWithText("Checking progress.", substring = true).assertExists()
        rule.onNodeWithTag("background-list").assertDoesNotExist()

        rule.runOnIdle { failed.completeExceptionally(IllegalStateException("temporary lookup failure")) }
        rule.waitForIdle()
        rule.onNodeWithText("Checking progress.", substring = true).assertExists()
        rule.onNodeWithTag("background-list").assertDoesNotExist()
        rule.onNodeWithContentDescription("Back").performClick()
        waitText("Could not load background tasks.")
        rule.onNodeWithText("Retry").performClick()
        rule.waitUntil(10_000) { reads.size == 2 }
        rule.onNode(hasText("Watch the build") and hasClickAction()).performClick()
        rule.runOnIdle {
            recovered.complete(full.copy(background = emptyList(), backgroundEnded = listOf(
                task.copy(status = "failed", endedAt = now.toString(), exitCode = 5),
            )))
        }
        waitText("Exit code")
        rule.onNodeWithText("5").assertExists()
    }

    @Test fun aMissingOlderHubOutcomeSaysUnknownInsteadOfCompleted() {
        val shown = mutableStateOf(owner)
        val request = BackgroundPanelRequest(owner.key, owner.manager, BackgroundDestination.Command(active.id, active))
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                BackgroundTasksPanel(request, shown.value, null, null, false, null, null, null, null, {})
            }
        }
        waitText("Running")
        rule.runOnIdle { shown.value = owner.copy(background = emptyList()) }
        waitText("Not listed as running")
        rule.onNodeWithText("Runtime").assertDoesNotExist()
        rule.onNodeWithText("Exit code").assertDoesNotExist()
    }

    @Test fun typedWorkflowsOpensTheLatestRunWithoutSendingTheDraft() {
        val sent = CopyOnWriteArrayList<String>()
        val state = ChatComposerState()
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(owner.machineId, "Build computer", online = true)))
            override val sessions = MutableStateFlow(listOf(owner))
            override suspend fun refresh() = Unit
        }
        val chat = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false, items = listOf(
                Item("u", 1, role = "user", kind = "text", text = "Review the build."),
            )))
            override suspend fun loadOlder(session: SessionKey) = Unit
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int): List<Item> = emptyList()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
                sent += text
            }
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() = Unit
            override suspend fun resolve(id: String, resolution: Resolution) = Unit
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Brief) {
                Box(Modifier.fillMaxSize()) {
                    ChatScreen(owner.key, chat, sender, interactions, sessions, onBack = {}, composerState = state)
                }
            }
        }
        waitText("Review the build.")
        rule.onNode(hasSetTextAction(), useUnmergedTree = true).performTextReplacement("/workflows")
        rule.onNodeWithContentDescription("Send").performClick()
        rule.waitUntil(10_000) { rule.onAllNodes(hasText("Verify build")).fetchSemanticsNodes().isNotEmpty() }
        rule.onNodeWithTag("workflow-card").assertExists()
        assertTrue(sent.isEmpty())
        assertEquals("", state.value.text)
    }

    @Test fun workflowsSuggestionIsAvailableOnlyForAStandaloneDraft() {
        val state = ChatComposerState()
        val quoted = mutableStateOf(false)
        val attachment = ComposerAttachment("attached", "review.txt", "content://example/review.txt", image = false, path = "/project/review.txt")
        var opened = 0
        rule.setContent {
            PersonaTheme(reduceMotion = true, style = AppStyle.Brief) {
                ChatComposer(owner.key, state, working = false, sending = false, canClear = false, service = null,
                    onSend = {}, onCommand = {}, onNotice = {}, extraContent = quoted.value, onWorkflows = { opened++ })
            }
        }
        val field = rule.onNode(hasSetTextAction(), useUnmergedTree = true)
        field.performTextReplacement("/workf")
        val suggestion = rule.onNode(hasText("/workflows") and hasClickAction())
        suggestion.assertExists()

        rule.runOnIdle { state.attachments = listOf(attachment) }
        suggestion.assertDoesNotExist()
        assertEquals("/workf", state.value.text)
        assertEquals(listOf(attachment), state.attachments)

        rule.runOnIdle { state.attachments = emptyList(); quoted.value = true }
        suggestion.assertDoesNotExist()
        assertEquals("/workf", state.value.text)
        assertEquals(0, opened)

        rule.runOnIdle { quoted.value = false }
        suggestion.performClick()
        assertEquals(1, opened)
        assertEquals("", state.value.text)
    }

    @Test fun commandDispatchPreservesAdditionalContentAndSavedPromptText() {
        val state = ChatComposerState()
        var panel = 0
        var network = 0
        fun setDraft() { state.value = TextFieldValue("/workflows") }
        setDraft()
        dispatchComposerSend(state, false, null, null, onWorkflows = { panel++ }) { network++ }
        assertEquals(1, panel)
        assertEquals("", state.value.text)
        setDraft()
        dispatchComposerSend(state, true, null, null, onWorkflows = { panel++ }) { network++ }
        state.fillSavedPrompt("/workflows")
        dispatchComposerSend(state, false, null, null, onWorkflows = { panel++ }) { network++ }
        assertEquals(1, panel)
        assertEquals(2, network)
        assertEquals("/workflows", state.value.text)
    }
}

internal abstract class FixtureBackgroundFiles : SessionFiles {
    override suspend fun directory(machineId: String, cwd: String, path: String): Nothing = error("unexpected directory request")
    override suspend fun file(machineId: String, cwd: String, path: String): Nothing = error("unexpected file preview")
    override suspend fun status(machineId: String, cwd: String): Nothing = error("unexpected git status")
    override suspend fun diff(machineId: String, cwd: String, path: String, staged: Boolean): Nothing = error("unexpected git diff")
    override suspend fun log(machineId: String, cwd: String, skip: Int, limit: Int): Nothing = error("unexpected git history")
    override suspend fun show(machineId: String, cwd: String, hash: String): Nothing = error("unexpected git commit")
}
