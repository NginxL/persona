package persona.feature.chat

import android.Manifest
import android.app.Application
import android.os.Bundle
import android.speech.SpeechRecognizer
import androidx.activity.compose.LocalActivityResultRegistryOwner
import androidx.activity.result.ActivityResultRegistry
import androidx.activity.result.ActivityResultRegistryOwner
import androidx.activity.result.contract.ActivityResultContract
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.test.onNodeWithContentDescription
import androidx.compose.ui.test.performClick
import androidx.core.app.ActivityOptionsCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.LifecycleRegistry
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import kotlinx.coroutines.flow.MutableStateFlow
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowSpeechRecognizer
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.designsystem.PersonaTheme

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en-w400dp-h800dp")
class DictationLifecycleTest {
    @get:Rule val rule = createComposeRule()
    private val app get() = ApplicationProvider.getApplicationContext<Application>()

    private val composer = ChatComposerState()
    private var covered by mutableStateOf(false)

    private fun words(text: String) = Bundle().apply { putStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION, arrayListOf(text)) }

    private fun showAgentChat() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val key = SessionKey("computer", "agent")
        val chats = object : ChatRepository {
            override fun observe(session: SessionKey) = MutableStateFlow(ChatSnapshot(loading = false))
            override suspend fun loadOlder(session: SessionKey) {}
            override suspend fun expandFold(session: SessionKey, fromSeq: Int, toSeq: Int) = emptyList<Item>()
        }
        val sender = object : MessageSender {
            override val outbox = MutableStateFlow(emptyList<OutboxMessage>())
            override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) = error("Dictation sent")
        }
        val interactions = object : InteractionCenter {
            override val pending = MutableStateFlow(emptyList<Interaction>())
            override suspend fun refresh() {}
            override suspend fun resolve(id: String, resolution: Resolution) {}
        }
        val sessions = object : SessionsRepository {
            override val machines = MutableStateFlow(listOf(Machine(key.machineId, "Computer", online = true)))
            override val sessions = MutableStateFlow(listOf(Session(key.machineId, key.sessionId, agent = "claude", status = "idle")))
            override suspend fun refresh() {}
        }
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                ChatScreen(key, chats, sender, interactions, sessions, onBack = {},
                    chrome = ChatChromeBindings(covered = covered), composerState = composer)
            }
        }
    }

    @Test fun anAgentChatCoverKeepsHeardWordsAndStopsListeningUntilTheNextTap() {
        showAgentChat()
        rule.onNodeWithContentDescription("Dictate").performClick()
        val speech = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        rule.runOnIdle { speech.triggerOnPartialResults(words("before opening files")); covered = true }
        rule.waitForIdle()
        assertTrue(speech.isDestroyed)
        rule.runOnIdle { speech.triggerOnResults(words("from the covering page")); covered = false }
        rule.onNodeWithContentDescription("Dictate").assertExists()
        assertEquals("before opening files", composer.value.text)
        rule.onNodeWithContentDescription("Stop dictation").assertDoesNotExist()
    }

    @Test fun aStoppedRecordingDeliversItsFinalOnlyResultToTheSameCoveredChat() {
        showAgentChat()
        rule.onNodeWithContentDescription("Dictate").performClick()
        val speech = shadowOf(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        rule.runOnIdle { covered = true }
        rule.waitForIdle()
        assertFalse(speech.isDestroyed)
        rule.runOnIdle { speech.triggerOnResults(words("before opening files")) }
        rule.waitForIdle()
        assertTrue(speech.isDestroyed)
        rule.runOnIdle { covered = false }
        rule.onNodeWithContentDescription("Dictate").assertExists()
        assertEquals("before opening files", composer.value.text)
        rule.onNodeWithContentDescription("Stop dictation").assertDoesNotExist()
    }

    @Test fun aTabThatStoppedCannotStartFromALateGrantButAPausedPermissionDialogCan() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(app).denyPermissions(Manifest.permission.RECORD_AUDIO)
        val lifecycle = object : LifecycleOwner {
            val events = LifecycleRegistry.createUnsafe(this).apply { currentState = Lifecycle.State.RESUMED }
            override val lifecycle: Lifecycle get() = events
        }
        var request = 0
        val registry = object : ActivityResultRegistry() {
            override fun <I, O> onLaunch(requestCode: Int, contract: ActivityResultContract<I, O>, input: I, options: ActivityOptionsCompat?) {
                request = requestCode
            }
        }
        val owner = object : ActivityResultRegistryOwner { override val activityResultRegistry = registry }
        rule.setContent {
            CompositionLocalProvider(LocalLifecycleOwner provides lifecycle, LocalActivityResultRegistryOwner provides owner) {
                PersonaTheme(reduceMotion = true) { rememberDictation({}, {})?.let { DictationMic(it, "Dictate") } }
            }
        }
        rule.onNodeWithContentDescription("Dictate").performClick()
        assertNotEquals(0, request)
        rule.runOnIdle { lifecycle.events.currentState = Lifecycle.State.CREATED; registry.dispatchResult(request, true) }
        rule.waitForIdle()
        assertNull(ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        rule.runOnIdle { lifecycle.events.currentState = Lifecycle.State.RESUMED }
        rule.onNodeWithContentDescription("Dictate").performClick()
        rule.runOnIdle { lifecycle.events.currentState = Lifecycle.State.STARTED; registry.dispatchResult(request, true) }
        rule.onNodeWithContentDescription("Stop dictation").assertExists()
    }
}
