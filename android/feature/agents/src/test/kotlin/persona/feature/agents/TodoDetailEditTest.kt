package persona.feature.agents

import android.Manifest
import android.app.Application
import android.os.Bundle
import android.speech.SpeechRecognizer
import androidx.test.core.app.ApplicationProvider
import org.robolectric.Shadows.shadowOf
import org.robolectric.shadows.ShadowSpeechRecognizer
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.ui.test.assertIsNotEnabled
import androidx.compose.ui.test.hasSetTextAction
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onAllNodesWithText
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.onNodeWithText
import androidx.compose.ui.test.performClick
import androidx.compose.ui.test.performTextReplacement
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.serialization.json.JsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme
import persona.core.domain.tasks.TasksStore

/** Editing a to-do (#3736): a blank title can't be saved, and the editor keeps what was typed until the hub accepts it. */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h900dp")
class TodoDetailEditTest {
    @get:Rule val rule = createComposeRule()

    private class Api(var todo: Todo) : TasksApi {
        var gate: CompletableDeferred<Unit>? = null
        var fail = false
        val patches = mutableListOf<Pair<String?, String?>>()
        override suspend fun todos() = listOf(todo)
        override suspend fun patchTodo(id: String, title: String?, notes: String?, done: Boolean?): Todo {
            patches += title to notes
            gate?.await()
            if (fail) error("offline")
            return todo.copy(title = title ?: todo.title, notes = notes ?: todo.notes).also { todo = it }
        }
        override suspend fun feed(conversation: String, since: String?) = FeedResponse(emptyList(), emptyMap())
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
        override val state = MutableStateFlow(ConnectionState.Online)
        override val events = MutableSharedFlow<ServerEvent>()
        override val lastHello: Hello get() = Hello(serverVersion = "1.0.0", protocol = 1)
        override fun reconnect() = Unit
        override fun send(message: JsonObject) = true
    }

    private val errors = mutableListOf<String>()

    private fun open(api: Api) {
        val store = TasksStore(api, Connection(), CoroutineScope(Dispatchers.Unconfined))
        store.reloadTodos()
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                val todos by store.todos.collectAsState()
                TodoDetailScreen("t1", todos, emptyList(), emptyList(), store, onBack = {}, onOpenSession = {}, onHandOff = {},
                    onError = { errors += it })
            }
        }
        rule.waitForIdle()
        rule.onNodeWithContentDescription("Edit").performClick()
        rule.waitForIdle()
    }

    private fun fields() = rule.onAllNodes(hasSetTextAction())

    @Test fun stoppingDictationKeepsNotesLocalUntilSave() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val api = Api(Todo("t1", title = "Review", notes = "Original notes"))
        open(api)
        rule.onNodeWithContentDescription("Dictate notes").performClick()
        val speech = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        val words = Bundle().apply { putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf("and more")) }
        rule.runOnIdle { speech.triggerOnPartialResults(words) }
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        rule.runOnIdle { speech.triggerOnResults(words) }
        rule.onNodeWithText("Original notes and more").assertExists()
        assertTrue(api.patches.isEmpty())
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertEquals(listOf("Review" to "Original notes and more"), api.patches)
    }

    @Test fun aCorrectedFinalReachesSaveAfterAnImmediateAttempt() = saveAfterStop("some seeds", "sunflower seeds")

    @Test fun finalOnlyRecognitionReachesSaveAfterAnImmediateAttempt() = saveAfterStop(null, "sunflower seeds")

    @Test fun aMissingFinalAllowsSavingTheLastPartialAfterTheTimeout() = saveAfterStop("some seeds", null)

    private fun saveAfterStop(partial: String?, final: String?) {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val api = Api(Todo("t1", title = "Review", notes = "Original"))
        open(api)
        rule.onNodeWithContentDescription("Dictate notes").performClick()
        val recognition = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        fun words(text: String) = Bundle().apply { putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf(text)) }
        if (partial != null) rule.runOnIdle { recognition.triggerOnPartialResults(words(partial)) }
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        val save = rule.onNodeWithText("Save")
        save.performClick()
        assertTrue("an immediate Save must not write an unfinished transcript", api.patches.isEmpty())
        save.assertIsNotEnabled()
        if (final != null) rule.runOnIdle { recognition.triggerOnResults(words(final)) }
        else rule.mainClock.advanceTimeBy(1600)
        rule.waitForIdle()
        save.performClick()
        rule.waitForIdle()
        assertEquals(listOf("Review" to "Original ${final ?: partial}"), api.patches)
    }

    @Test fun cancellingNotesWhileTheFinalIsPendingCannotPatchOrReopenTheDraft() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(ApplicationProvider.getApplicationContext<Application>()).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val api = Api(Todo("t1", title = "Review", notes = "Original"))
        open(api)
        rule.onNodeWithContentDescription("Dictate notes").performClick()
        val recognition = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        rule.onNodeWithText("Cancel").performClick()
        val words = Bundle().apply { putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf("late final")) }
        rule.runOnIdle { recognition.triggerOnResults(words) }
        assertTrue(api.patches.isEmpty())
        rule.onNodeWithContentDescription("Edit").performClick()
        rule.onNodeWithText("Original").assertExists()
    }

    @Test fun blankTitleCannotBeSaved() {
        val api = Api(Todo("t1", title = "Ship the fix", notes = "Run tests first"))
        open(api)
        fields()[0].performTextReplacement("   ")
        rule.waitForIdle()
        rule.onNodeWithText("Save").assertIsNotEnabled()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertTrue("no request for a blank title", api.patches.isEmpty())
    }

    @Test fun failedSaveKeepsTheEditorAndText() {
        val api = Api(Todo("t1", title = "Ship the fix", notes = "Run tests first")).apply { fail = true }
        open(api)
        fields()[1].performTextReplacement("Run tests, then open the PR")
        rule.waitForIdle()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertEquals(1, errors.size)
        assertTrue("the edit is still there", rule.onAllNodesWithText("Run tests, then open the PR").fetchSemanticsNodes().isNotEmpty())
        rule.onNodeWithText("Save").assertExists()
    }

    @Test fun saveInFlightCannotBeSentTwice() {
        val api = Api(Todo("t1", title = "Ship the fix", notes = "")).apply { gate = CompletableDeferred() }
        open(api)
        fields()[0].performTextReplacement("Ship the login fix")
        rule.waitForIdle()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        rule.onNodeWithText("Save").assertIsNotEnabled()
        rule.onNodeWithText("Save").performClick()
        rule.waitForIdle()
        assertEquals(1, api.patches.size)
        api.gate?.complete(Unit)
        rule.waitForIdle()
        assertTrue("closes once saved", rule.onAllNodesWithText("Save").fetchSemanticsNodes().isEmpty())
        assertTrue(rule.onAllNodesWithText("Ship the login fix").fetchSemanticsNodes().isNotEmpty())
    }
}
