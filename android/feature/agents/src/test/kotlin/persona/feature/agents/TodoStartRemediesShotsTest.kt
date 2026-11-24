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
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.test.assertIsDisplayed
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.captureToImage
import androidx.compose.ui.test.getUnclippedBoundsInRoot
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithTag
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performScrollTo
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.io.File
import java.time.Instant
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
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
import persona.core.contracts.SHELL_WAITING
import persona.core.contracts.ServerEvent
import persona.core.contracts.TODO_START_ANSWER_FEATURE
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.TasksApi
import persona.core.designsystem.AppStyle
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TASK_RETRY_LAUNCHER_FEATURE
import persona.core.domain.tasks.TasksStore

/**
 * The real to-do page after an Agent failed to start. Synthetic plans exercise a taken-over task with no `plan_id`, the
 * exact launcher and terminal-answer capabilities, and Retry's shared busy key. Images are native JVM graphics in every
 * style and appearance, at default text and at a locally supplied 200% text density.
 */
@RunWith(AndroidJUnit4::class)
@GraphicsMode(GraphicsMode.Mode.NATIVE)
@Config(sdk = [35], qualifiers = "en-w400dp-h1400dp-xhdpi")
class TodoStartRemediesShotsTest {
    @get:Rule val rule = createComposeRule()

    private val now = Instant.parse("2026-11-20T12:00:00Z")
    private val features = setOf(TASK_RETRY_LAUNCHER_FEATURE, TODO_START_ANSWER_FEATURE)
    private val rawFailure = "synthetic launcher exited 127: executable was not found\n" +
        "second line remains visible: /opt/persona/synthetic-agent"
    private val codedRawFailure = "launcher_missing: claude could not start in the terminal\n" +
        "missing=/opt/persona/bin/claude\nexit=127"
    private val codedShort = "claude can't start in this computer's terminal"
    private val questionLine = "[oh-my-zsh] Would you like to update? [Y/n]"
    private val handedOff = mutableListOf<Todo>()
    private val errors = mutableListOf<String>()

    private data class RemedyCall(val todoId: String, val choice: String, val manager: String?)
    private data class PatchCall(val todoId: String, val done: Boolean?)

    private class Api(
        var rows: List<Todo>,
        featureSet: Set<String>,
        private val feedResult: FeedResponse = FeedResponse(),
    ) : TasksApi {
        override val features = MutableStateFlow(featureSet)
        val retries = mutableListOf<String>()
        val launcherRetries = mutableListOf<RemedyCall>()
        val answers = mutableListOf<RemedyCall>()
        val patches = mutableListOf<PatchCall>()
        var retryGate: CompletableDeferred<Unit>? = null

        override suspend fun todos(): List<Todo> = rows
        override suspend fun todoDecisions(todoId: String) = emptyList<persona.core.contracts.TodoDecision>()

        override suspend fun feed(conversation: String, since: String?): FeedResponse = feedResult

        override suspend fun retryTask(todoId: String): Todo {
            retries += todoId
            retryGate?.await()
            return reopened(todoId)
        }

        override suspend fun retryTaskOnLauncher(todoId: String, launcher: String): Todo {
            launcherRetries += RemedyCall(todoId, launcher, currentCoroutineContext()[RequestManager]?.id)
            return reopened(todoId)
        }

        override suspend fun answerTaskStart(todoId: String, choice: String): Todo {
            answers += RemedyCall(todoId, choice, currentCoroutineContext()[RequestManager]?.id)
            return reopened(todoId)
        }

        override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo {
            patches += PatchCall(id, done)
            val old = rows.single { it.id == id }
            val updated = old.copy(
                title = title ?: old.title,
                notes = notes ?: old.notes,
                doneAt = when (done) {
                    true -> old.doneAt ?: "2026-11-20T12:01:00Z"
                    false -> null
                    null -> old.doneAt
                },
                startError = if (done == true) null else old.startError,
            )
            rows = rows.map { if (it.id == id) updated else it }
            return updated
        }

        private fun reopened(todoId: String): Todo {
            val updated = rows.single { it.id == todoId }.copy(doneAt = null, startError = null)
            rows = rows.map { if (it.id == todoId) updated else it }
            return updated
        }

        override suspend fun startPlan(id: String, remember: Boolean): TaskPlan = error("unused")
        override suspend fun dismissPlan(id: String): TaskPlan = error("unused")
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

    private fun failedTodo() = Todo(
        id = "task-without-plan",
        title = "Prepare the synthetic release",
        notes = "A failed start whose hub close timestamp must not make it look reopened.",
        origin = "origin-conversation",
        planId = null,
        manager = "manager-work",
        createdAt = "2026-11-20T10:00:00Z",
        updatedAt = "2026-11-20T11:55:00Z",
        doneAt = "2026-11-20T11:55:00Z",
        startError = rawFailure,
        spaces = emptyList(),
    )

    private fun remedyPlan() = TaskPlan(
        id = "takeover-plan",
        conversation = "origin-conversation",
        state = "started",
        tasks = listOf(
            PlanTask(
                title = "Prepare the synthetic release",
                todoId = "task-without-plan",
                retryWith = listOf("claude", "codex"),
                errorCode = SHELL_WAITING,
                errorArgs = mapOf(
                    "line" to questionLine,
                    "choices" to "y,n,enter",
                    "kind" to "update",
                    "default" to "y",
                    "screen" to "Last login: Fri Nov 20 11:54:00\n$questionLine",
                ),
            ),
        ),
    )

    private fun codedRemedyPlan() = remedyPlan().let { plan ->
        plan.copy(tasks = plan.tasks.map {
            it.copy(errorCode = "launcher_missing", errorArgs = mapOf(
                "where" to "terminal", "launcher" to "claude", "missing" to "/opt/persona/bin/claude",
            ))
        })
    }

    private fun remedyApi(
        featureSet: Set<String> = features,
        todo: Todo = failedTodo(),
        plan: TaskPlan = remedyPlan(),
    ) = Api(listOf(todo), featureSet, FeedResponse(plans = mapOf(plan.id to plan)))

    private fun codedRemedyApi() = remedyApi(
        todo = failedTodo().copy(startError = codedRawFailure),
        plan = codedRemedyPlan(),
    )

    private fun open(
        api: Api,
        largeText: Boolean = false,
        appearance: () -> Pair<AppStyle, Boolean> = { AppStyle.Glass to false },
    ): TasksStore {
        handedOff.clear()
        errors.clear()
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.reloadTodos()
        // As on the web, the page reads the plan already loaded from its conversation feed.
        store.load("origin-conversation", whole = true)
        rule.setContent {
            val (style, dark) = appearance()
            PersonaTheme(style = style, darkTheme = dark, reduceMotion = true) {
                Box(Modifier.testTag("shot").width(400.dp).height(1400.dp).background(PersonaTheme.palette.background)) {
                    val density = LocalDensity.current
                    CompositionLocalProvider(
                        LocalDensity provides Density(density.density, if (largeText) 2f else 1f),
                    ) {
                        val todos by store.todos.collectAsState()
                        TodoDetailScreen(
                            "task-without-plan", todos, emptyList(), emptyList(), store,
                            onBack = {}, onOpenSession = {}, onHandOff = { handedOff += it },
                            onError = { errors += it }, now = now,
                        )
                    }
                }
            }
        }
        rule.waitForIdle()
        return store
    }

    private fun assertFailedStartRemedies() {
        rule.onNodeWithText("Done").assertDoesNotExist()
        rule.onNodeWithText("Didn’t start").assertIsDisplayed()
        rule.onNodeWithText("Reopen it to hand it to an agent.").assertDoesNotExist()
        rule.onNodeWithText(rawFailure, substring = true).assertIsDisplayed()
        rule.onNodeWithText("Complete").assertIsDisplayed()
        rule.onNodeWithText("Reopen").assertDoesNotExist()
        rule.onNodeWithTag("todo-retry-launcher").assertIsDisplayed()
        rule.onNodeWithTag("todo-retry").assertIsDisplayed()
        rule.onNodeWithTag("shell-question").assertExists()
        rule.onNodeWithText(questionLine, substring = true).assertExists()
    }

    private fun assertCodedStartFailureCollapsed() {
        rule.onNodeWithText("Couldn't start: $codedShort").assertIsDisplayed()
        rule.onNodeWithText(codedRawFailure, useUnmergedTree = true).assertDoesNotExist()
        rule.onNodeWithTag("todo-start-failure-details").assertIsDisplayed()
        rule.onNodeWithTag("shell-question").assertDoesNotExist()
    }

    @Test fun failedStartWithDoneAtChecksOpenAndCompletes() {
        val failed = failedTodo()
        val api = Api(listOf(failed), emptySet())
        open(api)

        assertTrue(failed.isDone)
        rule.onNodeWithText("Done").assertDoesNotExist()
        rule.onNodeWithText("Didn’t start").assertIsDisplayed()
        rule.onNodeWithText("Reopen it to hand it to an agent.").assertDoesNotExist()
        rule.onNodeWithText("Tap “Hand to agent” below to assign it.").assertIsDisplayed()
        rule.onNodeWithText("Complete").assertIsDisplayed()
        rule.onNodeWithText("Reopen").assertDoesNotExist()
        rule.onNodeWithText("Complete").performClick()
        rule.waitForIdle()

        assertEquals(listOf(PatchCall(failed.id, true)), api.patches)
        assertTrue(errors.isEmpty())
    }

    @Test fun plainRetryWorksWithoutPlanIdAndKeepsTheWholeRawFailure() {
        val failed = failedTodo()
        val api = Api(listOf(failed), emptySet())
        open(api)

        assertNull(failed.planId)
        rule.onNodeWithText("Couldn't start: $rawFailure").assertIsDisplayed()
        rule.onNodeWithTag("todo-start-failure-details").assertDoesNotExist()
        rule.onNodeWithText("Couldn't start: $codedShort").assertDoesNotExist()
        rule.onNodeWithTag("todo-retry-launcher").assertDoesNotExist()
        rule.onNodeWithTag("todo-retry").performClick()
        rule.waitForIdle()

        assertEquals(listOf(failed.id), api.retries)
        assertTrue(api.launcherRetries.isEmpty())
        assertTrue(errors.isEmpty())
    }

    @Test fun codedStartFailureUsesShortLineAndHidesRawUntilDetails() {
        open(codedRemedyApi())

        assertCodedStartFailureCollapsed()
        rule.onNodeWithTag("todo-start-failure-details").performClick()
        rule.onNodeWithText(codedRawFailure, useUnmergedTree = true).performScrollTo().assertIsDisplayed()
        rule.onNodeWithText("Couldn't start: $codedShort").assertExists()
        rule.onNodeWithText("Hide details").assertExists()
    }

    @Test fun firstSuggestedLauncherPrecedesRetryAndSendsItsExactName() {
        val api = remedyApi()
        open(api)

        val launcher = rule.onNodeWithTag("todo-retry-launcher")
        val retry = rule.onNodeWithTag("todo-retry")
        val first = launcher.getUnclippedBoundsInRoot()
        val second = retry.getUnclippedBoundsInRoot()
        assertTrue(
            "Use launcher must be laid out before Retry: $first versus $second",
            first.top < second.top || first.top == second.top && first.left < second.left,
        )
        launcher.assertIsDisplayed().performClick()
        rule.waitForIdle()

        assertEquals(listOf(RemedyCall("task-without-plan", "claude", "manager-work")), api.launcherRetries)
        assertTrue(api.retries.isEmpty())
        assertTrue(errors.isEmpty())
    }

    @Test fun launcherRetryIsHiddenWithoutItsExactHubFeature() {
        val api = remedyApi(setOf(TODO_START_ANSWER_FEATURE, "task_retry_launcher_extra"))
        open(api)

        rule.onNodeWithTag("todo-retry-launcher").assertDoesNotExist()
        rule.onNodeWithText("Use claude instead").assertDoesNotExist()
        rule.onNodeWithTag("todo-retry").assertIsDisplayed()
        assertTrue(api.launcherRetries.isEmpty())
    }

    @Test fun terminalQuestionIsHiddenWithoutItsExactHubFeature() {
        val api = remedyApi(setOf(TASK_RETRY_LAUNCHER_FEATURE, "todo_start_answer_extra"))
        open(api)

        rule.onNodeWithTag("shell-question").assertDoesNotExist()
        rule.onNodeWithText(questionLine, substring = true).assertDoesNotExist()
        assertTrue(api.answers.isEmpty())
    }

    @Test fun terminalAnswerSendsTheExactChoiceForTheTodosManager() {
        val api = remedyApi()
        open(api)

        rule.onNodeWithTag("shell-choice-y").assertExists()
        rule.onNodeWithTag("shell-choice-n").performClick()
        rule.waitForIdle()

        assertEquals(listOf(RemedyCall("task-without-plan", "n", "manager-work")), api.answers)
        assertTrue(api.retries.isEmpty())
        assertTrue(api.launcherRetries.isEmpty())
        assertTrue(errors.isEmpty())
    }

    @Test fun busyRetryDisablesLauncherAndDeduplicatesCompetingTaps() {
        val gate = CompletableDeferred<Unit>()
        val api = remedyApi().apply { retryGate = gate }
        val store = open(api)

        val retry = rule.onNodeWithTag("todo-retry")
        val launcher = rule.onNodeWithTag("todo-retry-launcher")
        retry.performClick()
        rule.waitForIdle()
        retry.assertIsNotEnabled()
        launcher.assertIsNotEnabled()
        retry.performClick()
        launcher.performClick()
        rule.waitForIdle()

        assertEquals(setOf(TasksStore.retryKey("task-without-plan")), store.busy.value)
        assertEquals(listOf("task-without-plan"), api.retries)
        assertTrue(api.launcherRetries.isEmpty())
        assertTrue(api.answers.isEmpty())
        rule.runOnIdle { gate.complete(Unit) }
        rule.waitForIdle()
        assertTrue(store.busy.value.isEmpty())
    }

    @Test fun ordinaryOpenTodoRetainsCompleteAndHandToAgentOnly() {
        val ordinary = failedTodo().copy(doneAt = null, startError = null, origin = null, manager = null)
        val api = Api(listOf(ordinary), features)
        open(api)

        rule.onNodeWithText("Complete").assertIsDisplayed()
        rule.onNodeWithText("Hand to agent").assertIsDisplayed().performClick()
        rule.onNodeWithText("Reopen").assertDoesNotExist()
        rule.onNodeWithTag("todo-retry").assertDoesNotExist()
        rule.onNodeWithTag("todo-retry-launcher").assertDoesNotExist()
        assertEquals(listOf(ordinary), handedOff)
    }

    @Test fun failedStartScreenshotsDefaultText() = screenshotMatrix(largeText = false)

    @Test fun failedStartScreenshotsLargeText() = screenshotMatrix(largeText = true)

    @Test fun codedStartFailureScreenshotsDefaultText() = screenshotMatrix(largeText = false, coded = true)

    @Test fun codedStartFailureScreenshotsLargeText() = screenshotMatrix(largeText = true, coded = true)

    private fun screenshotMatrix(largeText: Boolean, coded: Boolean = false) {
        var appearance by mutableStateOf(AppStyle.Glass to false)
        open(if (coded) codedRemedyApi() else remedyApi(), largeText = largeText, appearance = { appearance })
        val out = File(System.getProperty("persona.shots") ?: "build/todo-start-remedies-shots").also { it.mkdirs() }
        for (style in AppStyle.entries) for (dark in listOf(false, true)) {
            rule.runOnIdle { appearance = style to dark }
            rule.waitForIdle()
            if (coded) assertCodedStartFailureCollapsed() else assertFailedStartRemedies()
            val bitmap = rule.onNodeWithTag("shot").captureToImage().asAndroidBitmap()
            assertTrue(bitmap.width > 0 && bitmap.height > 0)
            val size = if (largeText) "large" else "default"
            val theme = if (dark) "dark" else "light"
            val prefix = if (coded) "todo-start-remedies-coded" else "todo-start-remedies"
            val image = File(out, "$prefix-$size-${style.name.lowercase()}-$theme.png")
            image.outputStream().use { assertTrue(bitmap.compress(Bitmap.CompressFormat.PNG, 100, it)) }
        }
    }
}
