package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.Snapshot
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asAndroidBitmap
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertCountEquals
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithTag
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Duration
import java.time.Instant
import java.time.temporal.ChronoUnit
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.json.JsonObject
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
import persona.core.contracts.PlanTask
import persona.core.contracts.ServerEvent
import persona.core.contracts.TaskPlan
import persona.core.contracts.TODO_START_ANSWER_FEATURE
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.InterfaceFontScaleCap
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.currentTextDensities
import persona.core.domain.tasks.TASK_RETRY_LAUNCHER_FEATURE
import persona.core.domain.tasks.TasksStore

/** Real failed-task surfaces, fed by a real [TasksStore] and its feed-backed plan lookup. */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class AssistantTaskRemediesTest {
    @get:Rule val rule = createComposeRule()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Unconfined)
    @After fun close() = scope.cancel()

    private inner class Fixture(todo: Todo, plans: List<TaskPlan>, features: Set<String>) {
        var rows = listOf(todo)
        val hubFeatures = MutableStateFlow(features)
        val plainRetries = mutableListOf<String>()
        val launcherRetries = mutableListOf<Pair<String, String>>()
        val answers = mutableListOf<Pair<String, String>>()
        val errors = mutableListOf<Throwable>()

        private fun reopened(todoId: String): Todo {
            val updated = rows.first { it.id == todoId }.copy(doneAt = null, startError = null, reopenedAt = AT)
            rows = rows.map { if (it.id == todoId) updated else it }
            return updated
        }

        val api = object : TasksApi {
            override val features: StateFlow<Set<String>> get() = hubFeatures
            override suspend fun todos() = rows
            override suspend fun feed(conversation: String, since: String?) =
                FeedResponse(plans = plans.associateBy { it.id })
            override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("not used")
            override suspend fun dismissPlan(id: String): TaskPlan = error("not used")
            override suspend fun retryTask(todoId: String): Todo {
                plainRetries += todoId
                return reopened(todoId)
            }
            override suspend fun retryTaskOnLauncher(todoId: String, launcher: String): Todo {
                launcherRetries += todoId to launcher
                return reopened(todoId)
            }
            override suspend fun answerTaskStart(todoId: String, choice: String): Todo {
                answers += todoId to choice
                return reopened(todoId)
            }
            override suspend fun removeTask(todoId: String) = Unit
            override suspend fun route(conversation: String, todoId: String, text: String): FeedItem = error("not used")
            override suspend fun today(conversation: String): FeedItem = error("not used")
            override suspend fun mainConversation() = MAIN
            override suspend fun inboxReply(itemId: String, text: String, card: String?) = Unit
        }
        val connection = object : HubConnection {
            override val state = MutableStateFlow(ConnectionState.WaitingForNetwork)
            override val events = MutableSharedFlow<ServerEvent>()
            override fun reconnect() = Unit
            override fun send(message: JsonObject) = true
        }
        val tasks = TasksStore(api, connection, scope).also {
            it.reloadTodos()
            it.load(MAIN)
        }
        val sheet = mutableStateOf<TaskSheet?>(null)
        val host = AssistantTaskHost(tasks, MAIN, emptyList(), { "Test computer" }, {}, { errors += it }, {}, sheet = sheet)
        val tray = AssistantTrayState().apply { expanded = AssistantTray.NeedsYou }
    }

    private enum class Surface { NeedsYou, Detail }

    /** One composition per test; only local style, appearance, density and the real surface change. */
    private inner class Shown(fixture: Fixture, taskId: String, large: Boolean = false) {
        var surface by mutableStateOf(Surface.NeedsYou)
        var style by mutableStateOf(AppStyle.Glass)
        var dark by mutableStateOf(false)
        var renderedFontScale = 0f
        var chosenFontScale = 0f
        var dismissed = false

        init {
            rule.mainClock.autoAdvance = false
            rule.setContent {
                val base = LocalDensity.current
                CompositionLocalProvider(LocalDensity provides Density(base.density, if (large) 2f else 1f)) {
                    PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                        renderedFontScale = LocalDensity.current.fontScale
                        chosenFontScale = currentTextDensities().readingDensity?.fontScale ?: renderedFontScale
                        Box(Modifier.fillMaxSize().background(PersonaTheme.palette.background).testTag(SHOT_TAG)) {
                            key(surface) {
                                when (surface) {
                                    Surface.NeedsYou -> AssistantTrays(
                                        fixture.host, fixture.tray, emptyList(), emptyList(), emptyList(), { 900.dp },
                                        busy = false, enabled = true, onApprove = {}, onReview = {}, onSend = {}, onSchedule = {},
                                    )
                                    Surface.Detail -> AssistantTaskPage(TaskSheet.Task(taskId), fixture.host, onDismiss = { dismissed = true })
                                }
                            }
                        }
                    }
                }
            }
            settle()
            // The 2.0 input reaches the theme, whose interface text stops at the cap (#7321); reading text keeps 2.0.
            assertEquals(if (large) 2f else 1f, chosenFontScale, 0f)
            assertEquals(if (large) InterfaceFontScaleCap else 1f, renderedFontScale, 0f)
        }

        fun show(which: Surface, newStyle: AppStyle = style, newDark: Boolean = dark) {
            rule.runOnIdle {
                surface = which
                style = newStyle
                dark = newDark
            }
            settle()
        }
    }

    private fun settle() {
        Snapshot.sendApplyNotifications()
        repeat(4) { rule.mainClock.advanceTimeByFrame() }
        rule.mainClock.advanceTimeBy(500)
        rule.waitForIdle()
    }

    private fun show(fixture: Fixture, taskId: String = TASK, large: Boolean = false) = Shown(fixture, taskId, large)

    private fun failedFixture(
        code: String,
        args: Map<String, String>?,
        raw: String,
        retryWith: List<String>? = null,
        features: Set<String> = emptySet(),
        planId: String? = PLAN,
        earlierPlans: List<TaskPlan> = emptyList(),
    ): Fixture {
        val todo = Todo(TASK, TITLE, origin = MAIN, planId = planId, startError = raw, createdAt = AT, updatedAt = AT, doneAt = AT)
        val task = PlanTask(TITLE, todoId = TASK, error = raw, errorCode = code, errorArgs = args, retryWith = retryWith)
        return Fixture(todo, earlierPlans + TaskPlan(PLAN, MAIN, listOf(task), state = "started", createdAt = AT), features)
    }

    private fun launcherFixture(
        retryWith: List<String>? = listOf("claude", "codex"),
        features: Set<String> = emptySet(),
        planId: String? = PLAN,
        earlierPlans: List<TaskPlan> = emptyList(),
    ) = failedFixture("launcher_missing", TERMINAL_ARGS, LAUNCHER_RAW, retryWith, features, planId, earlierPlans)

    private fun shellFixture(features: Set<String> = setOf(TODO_START_ANSWER_FEATURE)) = failedFixture(
        "shell_waiting", SHELL_ARGS, SHELL_RAW, features = features,
    )

    private fun assertQuestion(count: Int) {
        rule.onAllNodesWithTag("shell-question", useUnmergedTree = true).assertCountEquals(count)
    }

    private fun assertNeedsYouShortLine(text: String) {
        rule.onNodeWithTag("assistant-tray-failed").assertIsDisplayed()
        rule.onNodeWithText(text, useUnmergedTree = true).assertIsDisplayed()
    }

    private fun assertCodedDetailRevealsRawOnlyAfterDetails(short: String, raw: String) {
        rule.onNodeWithText(short, useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText(raw, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithTag("assistant-task-start-failure-details").assertIsDisplayed().performClick()
        settle()
        rule.onNodeWithText(raw, useUnmergedTree = true).performScrollTo().assertIsDisplayed()
        rule.onNodeWithText(short, useUnmergedTree = true).assertExists()
    }

    private fun assertCodedDetailCollapsed(short: String, raw: String) {
        rule.onNodeWithText(short, useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithText(raw, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithTag("assistant-task-start-failure-details").assertIsDisplayed()
    }

    /** FlowRow keeps the suggested launcher before plain Retry whether they share a row or wrap at 2x text. */
    private fun assertLauncherBeforeRetry() {
        val launcher = rule.onNodeWithText(USE_LAUNCHER).getUnclippedBoundsInRoot()
        val retry = rule.onNodeWithText(RETRY).getUnclippedBoundsInRoot()
        assertTrue("Use launcher must precede Retry: $launcher versus $retry",
            launcher.top < retry.top || launcher.top == retry.top && launcher.left < retry.left)
    }

    private fun assertReopenedThroughTasksStore(fixture: Fixture) {
        assertTrue(fixture.errors.isEmpty())
        assertTrue(fixture.plainRetries.isEmpty())
        val todo = fixture.tasks.todos.value.single()
        assertFalse(todo.isDone)
        assertNull(todo.startError)
        assertEquals(AT, todo.reopenedAt)
        assertQuestion(0)
    }

    @Test fun terminalLauncherMissingUsesShortLinesAndDetailHidesRawFailureUntilDetails() {
        val shown = show(launcherFixture(retryWith = null))
        assertNeedsYouShortLine(LAUNCHER_SHORT)
        rule.onNodeWithText(LAUNCHER_RAW, useUnmergedTree = true).assertDoesNotExist()

        shown.show(Surface.Detail)
        assertCodedDetailRevealsRawOnlyAfterDetails(LAUNCHER_SHORT, LAUNCHER_RAW)
    }

    @Test fun worktreePathTakenUsesShortLinesAndDetailHidesRawFailureUntilDetails() {
        val shown = show(failedFixture("worktree_path_taken", mapOf("path" to "/workspace/release-check"), WORKTREE_RAW))
        assertNeedsYouShortLine(WORKTREE_SHORT)
        rule.onNodeWithText(WORKTREE_RAW, useUnmergedTree = true).assertDoesNotExist()

        shown.show(Surface.Detail)
        assertCodedDetailRevealsRawOnlyAfterDetails(WORKTREE_SHORT, WORKTREE_RAW)
    }

    @Test fun uncodedSharedDetailKeepsTheRawFailureWithoutAddingDetails() {
        val shown = show(failedFixture("", null, UNCODED_RAW))
        shown.show(Surface.Detail)
        rule.onNodeWithText(UNCODED_RAW, useUnmergedTree = true).assertIsDisplayed()
        rule.onNodeWithTag("assistant-task-start-failure-details").assertDoesNotExist()
        rule.onNodeWithText(LAUNCHER_SHORT, useUnmergedTree = true).assertDoesNotExist()
    }

    @Test fun takenOverTaskWithoutPlanIdUsesTheLivePlanAndGatesTheExactLauncherRetry() {
        val dismissed = TaskPlan("dismissed", MAIN, listOf(PlanTask(TITLE, todoId = TASK, errorCode = "worktree_path_taken",
            retryWith = listOf("codex"))), state = "dismissed", createdAt = AT)
        val fixture = launcherFixture(planId = null, earlierPlans = listOf(dismissed))
        assertNull(fixture.rows.single().planId)
        assertNull(fixture.tasks.launcherForRetry(fixture.rows.single()))
        val shown = show(fixture)

        assertNeedsYouShortLine(LAUNCHER_SHORT)
        rule.onNodeWithTag("assistant-tray-retry-launcher").assertDoesNotExist()
        rule.onNodeWithTag("assistant-tray-retry").assertIsDisplayed()
        rule.runOnIdle { fixture.hubFeatures.value = setOf(TASK_RETRY_LAUNCHER_FEATURE) }
        settle()
        assertEquals("claude", fixture.tasks.launcherForRetry(fixture.rows.single()))
        rule.onNodeWithTag("assistant-tray-retry-launcher").assertIsDisplayed()
        assertLauncherBeforeRetry()

        rule.onNodeWithTag("assistant-tray-retry-launcher").performClick()
        settle()
        assertEquals(listOf(TASK to "claude"), fixture.launcherRetries)
        assertTrue(fixture.answers.isEmpty())
        assertReopenedThroughTasksStore(fixture)
    }

    @Test fun sharedDetailGatesLauncherRetryAndSendsTheExactArgumentBeforePlainRetry() {
        val dismissed = TaskPlan("dismissed", MAIN, listOf(PlanTask(TITLE, todoId = TASK, errorCode = "worktree_path_taken",
            retryWith = listOf("codex"))), state = "dismissed", createdAt = AT)
        val fixture = launcherFixture(planId = null, earlierPlans = listOf(dismissed))
        assertNull(fixture.rows.single().planId)
        val shown = show(fixture)
        shown.show(Surface.Detail)
        rule.onNodeWithText(USE_LAUNCHER).assertDoesNotExist()
        rule.onNodeWithText(RETRY).assertIsDisplayed()

        rule.runOnIdle { fixture.hubFeatures.value = setOf(TASK_RETRY_LAUNCHER_FEATURE) }
        settle()
        rule.onNodeWithText(USE_LAUNCHER).assertIsDisplayed()
        assertLauncherBeforeRetry()
        rule.onNodeWithText(USE_LAUNCHER).performClick()
        settle()
        assertEquals(listOf(TASK to "claude"), fixture.launcherRetries)
        assertTrue(fixture.answers.isEmpty())
        assertReopenedThroughTasksStore(fixture)
    }

    @Test fun shellQuestionRequiresTodoStartAnswerOnBothRealSurfaces() {
        val fixture = shellFixture(features = emptySet())
        val shown = show(fixture)
        assertQuestion(0)
        rule.onNodeWithTag("assistant-tray-retry").assertIsDisplayed()

        rule.runOnIdle { fixture.hubFeatures.value = setOf(TODO_START_ANSWER_FEATURE) }
        settle()
        assertQuestion(1)
        rule.onNodeWithTag("shell-choice-y").assertIsDisplayed()

        shown.show(Surface.Detail)
        assertQuestion(1)
        rule.onNodeWithTag("shell-choice-y").assertIsDisplayed()
        rule.runOnIdle { fixture.hubFeatures.value = emptySet() }
        settle()
        assertQuestion(0)
        rule.onNodeWithText(RETRY).assertIsDisplayed()
    }

    @Test fun shellAnswerFromNeedsYouGoesThroughTasksStoreAndReconcilesTheReturnedTodo() {
        val fixture = shellFixture()
        show(fixture)
        rule.onNodeWithTag("shell-choice-y").performClick()
        settle()

        assertEquals(listOf(TASK to "y"), fixture.answers)
        assertTrue(fixture.launcherRetries.isEmpty())
        assertReopenedThroughTasksStore(fixture)
        rule.onNodeWithTag("assistant-tray-needs").assertDoesNotExist()
    }

    @Test fun shellAnswerFromSharedDetailGoesThroughTasksStoreAndReconcilesTheReturnedTodo() {
        val fixture = shellFixture()
        val shown = show(fixture)
        shown.show(Surface.Detail)
        rule.onNodeWithTag("shell-choice-y").performClick()
        settle()

        assertEquals(listOf(TASK to "y"), fixture.answers)
        assertTrue(fixture.launcherRetries.isEmpty())
        assertReopenedThroughTasksStore(fixture)
        rule.onNodeWithText("Starting the agent").assertIsDisplayed()
    }

    @Test fun ordinaryNonShellFailuresNeverShowAQuestionOnEitherSurface() {
        val misleadingArgs = TERMINAL_ARGS + mapOf("line" to "Would you like to update? [Y/n]", "choices" to "y,n")
        val fixture = failedFixture("launcher_missing", misleadingArgs, LAUNCHER_RAW,
            features = setOf(TODO_START_ANSWER_FEATURE))
        val shown = show(fixture)
        assertQuestion(0)
        assertNeedsYouShortLine(LAUNCHER_SHORT)

        shown.show(Surface.Detail)
        assertQuestion(0)
        assertCodedDetailRevealsRawOnlyAfterDetails(LAUNCHER_SHORT, LAUNCHER_RAW)
        assertTrue(fixture.answers.isEmpty())
    }

    @Test fun failedNeedsYouAndSharedDetailShotsAtDefaultText() = screenshotMatrix(large = false)

    @Test fun failedNeedsYouAndSharedDetailShotsAtDoubleText() = screenshotMatrix(large = true)

    private fun screenshotMatrix(large: Boolean) {
        val fixture = launcherFixture(features = setOf(TASK_RETRY_LAUNCHER_FEATURE))
        val shown = show(fixture, large = large)
        val text = if (large) "large" else "default"
        for (style in AppStyle.entries) {
            var lightNeeds: Bitmap? = null
            var lightDetail: Bitmap? = null
            for (dark in listOf(false, true)) {
                val suffix = "${style.name.lowercase()}-${if (dark) "dark" else "light"}-$text"
                shown.show(Surface.NeedsYou, style, dark)
                assertNeedsYouShortLine(LAUNCHER_SHORT)
                rule.onNodeWithTag("assistant-tray-retry-launcher").assertIsDisplayed()
                assertLauncherBeforeRetry()
                rule.onAllNodesWithText(LAUNCHER_RAW, useUnmergedTree = true).assertCountEquals(0)
                val needs = capture("assistant-task-remedy-needs-$suffix")
                if (dark) assertFalse("Needs you retained light pixels for $style at $text", lightNeeds!!.sameAs(needs))
                else lightNeeds = needs

                shown.show(Surface.Detail, style, dark)
                assertCodedDetailCollapsed(LAUNCHER_SHORT, LAUNCHER_RAW)
                rule.onNodeWithText(USE_LAUNCHER).assertIsDisplayed()
                assertLauncherBeforeRetry()
                assertQuestion(0)
                assertFalse(shown.dismissed)
                val detail = capture("assistant-task-remedy-detail-$suffix")
                if (dark) assertFalse("Task detail retained light pixels for $style at $text", lightDetail!!.sameAs(detail))
                else lightDetail = detail
            }
        }
        assertTrue(fixture.plainRetries.isEmpty())
        assertTrue(fixture.launcherRetries.isEmpty())
        assertTrue(fixture.answers.isEmpty())
        assertTrue(fixture.errors.isEmpty())
    }

    private fun capture(name: String): Bitmap {
        val bitmap = rule.onNodeWithTag(SHOT_TAG).captureToImage().asAndroidBitmap()
        assertTrue(bitmap.width > 0 && bitmap.height > 0)
        val out = File(System.getProperty("persona.shots") ?: "build/assistant-task-remedy-shots").also { it.mkdirs() }
        File(out, "$name.png").outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        return bitmap
    }

    private companion object {
        const val MAIN = "main"
        const val TASK = "task"
        const val PLAN = "plan"
        const val TITLE = "Prepare the release"
        // An hour ago on the real clock: the tray reads the triage at the current time, and a failed start shelves after
        // 48 hours (`SHELVE_AFTER_MS`), so a fixed date stops listing these rows two days after it was written.
        val AT: String = Instant.now().minus(Duration.ofHours(1)).truncatedTo(ChronoUnit.SECONDS).toString()
        const val RETRY = "Retry"
        const val USE_LAUNCHER = "Use claude instead"
        const val LAUNCHER_SHORT = "claude can't start in this computer's terminal"
        const val WORKTREE_SHORT = "A workspace from an earlier try is still in the way"
        const val SHOT_TAG = "assistant-task-remedy-shot"
        val TERMINAL_ARGS = mapOf("where" to "terminal", "launcher" to "claude", "missing" to "/opt/persona/bin/claude")
        val SHELL_ARGS = mapOf(
            "line" to "[oh-my-zsh] Would you like to update? [Y/n]",
            "choices" to "y,n,enter",
            "kind" to "update",
            "default" to "y",
            "screen" to "Last login: Fri Nov 20 09:59:00\n[oh-my-zsh] Would you like to update? [Y/n]",
            "pane" to "window-1:pane-2",
        )
        val LAUNCHER_RAW = "launcher_missing: claude could not start in the terminal\n" +
            "missing=/opt/persona/bin/claude\nexit=127"
        val UNCODED_RAW = "synthetic launcher exited 127: executable was not found\n" +
            "second line remains visible: /opt/persona/synthetic-agent"
        val WORKTREE_RAW = "worktree_path_taken: /workspace/release-check already exists\n" +
            "choose another workspace path before retrying"
        val SHELL_RAW = "shell_waiting: the terminal is waiting for an update answer\n" +
            "window-1:pane-2 remains open"
    }
}
