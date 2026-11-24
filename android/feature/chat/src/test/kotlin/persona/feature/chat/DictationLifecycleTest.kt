package persona.feature.chat

import android.Manifest
import android.app.Application
import android.content.ComponentName
import android.content.Intent
import android.content.IntentFilter
import android.os.Bundle
import android.speech.RecognitionListener
import android.speech.RecognitionService
import android.speech.RecognizerIntent
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
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
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

    private fun registerSystemRecognitionService() {
        val component = ComponentName("com.example.speech", "com.example.speech.SystemRecognitionService")
        val pm = shadowOf(app.packageManager)
        pm.addServiceIfNotPresent(component)
        pm.addIntentFilterForService(component, IntentFilter(RecognitionService.SERVICE_INTERFACE))
    }

    private class FakeSpeechEngine(
        private val onStart: (FakeSpeechEngine, Intent) -> Unit = { _, _ -> },
    ) : SpeechEngine {
        var recognitionListener: RecognitionListener? = null
            private set
        var starts = 0
        var stops = 0
        var destroyed = 0
        var lastIntent: Intent? = null

        override fun setListener(listener: RecognitionListener) {
            recognitionListener = listener
        }

        override fun startListening(intent: Intent) {
            starts++
            lastIntent = intent
            onStart(this, intent)
        }

        override fun stopListening() {
            stops++
        }

        override fun destroy() {
            destroyed++
        }
    }

    @Test
    @Config(qualifiers = "zh-rCN-w400dp-h800dp")
    fun onDeviceLanguageNotSupportedFallsBackToSystemRecognizerAndDeliversChineseResults() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        registerSystemRecognitionService()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        assertTrue(SpeechRecognizer.isOnDeviceRecognitionAvailable(app))
        assertTrue(SpeechRecognizer.isRecognitionAvailable(app))
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                control = rememberDictation(onHeard = { heard += it }, onError = { errors += it })
                control?.let { DictationMic(it, "Dictate") }
            }
        }
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotNull(onDevice)
        val onDeviceShadow = shadowOf(onDevice)
        assertEquals("zh-CN", onDeviceShadow.lastRecognizerIntent.getStringExtra(RecognizerIntent.EXTRA_LANGUAGE))

        rule.runOnIdle { onDeviceShadow.triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED) }
        rule.waitForIdle()

        assertTrue(onDeviceShadow.isDestroyed)
        val fallback = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotNull(fallback)
        assertNotSame(onDevice, fallback)
        val fallbackShadow = shadowOf(fallback)
        assertFalse(fallbackShadow.isDestroyed)
        assertEquals("zh-CN", fallbackShadow.lastRecognizerIntent.getStringExtra(RecognizerIntent.EXTRA_LANGUAGE))
        assertTrue(errors.isEmpty())
        assertTrue(control!!.active)

        rule.runOnIdle { fallbackShadow.triggerOnPartialResults(words("你好")) }
        rule.waitForIdle()
        assertEquals("你好", control!!.heard)
        rule.runOnIdle { control!!.stopListening() }
        rule.runOnIdle { fallbackShadow.triggerOnResults(words("你好世界")) }
        rule.waitForIdle()

        assertEquals(listOf("你好世界"), heard)
        assertTrue(errors.isEmpty())
        assertTrue(fallbackShadow.isDestroyed)
        assertFalse(control!!.active)
    }

    @Test fun onDeviceLanguageUnavailableFallsBackAndIgnoresStaleCallbacks() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        registerSystemRecognitionService()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                control = rememberDictation(onHeard = { heard += it }, onError = { errors += it })
                control?.let { DictationMic(it, "Dictate") }
            }
        }
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        val onDeviceShadow = shadowOf(onDevice)

        rule.runOnIdle { onDeviceShadow.triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE) }
        rule.waitForIdle()

        assertTrue(onDeviceShadow.isDestroyed)
        val fallback = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotSame(onDevice, fallback)
        val fallbackShadow = shadowOf(fallback)

        // Late callbacks from the destroyed on-device recognizer must be ignored.
        rule.runOnIdle {
            onDeviceShadow.triggerOnPartialResults(words("stale"))
            onDeviceShadow.triggerOnError(SpeechRecognizer.ERROR_NETWORK)
        }
        rule.waitForIdle()
        assertEquals("", control!!.heard)
        assertTrue(errors.isEmpty())
        assertTrue(control!!.active)

        rule.runOnIdle { fallbackShadow.triggerOnPartialResults(words("first")) }
        rule.waitForIdle()
        assertEquals("first", control!!.heard)
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        rule.runOnIdle { fallbackShadow.triggerOnResults(words("first final")) }
        rule.waitForIdle()

        assertEquals(listOf("first final"), heard)
        assertTrue(errors.isEmpty())
        assertTrue(fallbackShadow.isDestroyed)
        assertFalse(control!!.active)
    }

    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun fallbackContinuesAcrossPhrasesWithoutRetryingOnDevice() = runTest {
        val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
        val opened = mutableListOf<Boolean>()
        val engines = mutableListOf<FakeSpeechEngine>()
        val saved = mutableListOf<String>()
        val errors = mutableListOf<Boolean>()
        val session = DictationSession(
            this,
            open = { listener ->
                platformInput(
                    onDevice = true,
                    intent = intent,
                    listener = listener,
                    isDefaultAvailable = { true },
                    openEngine = { onDevice ->
                        opened += onDevice
                        val engine = FakeSpeechEngine()
                        engines += engine
                        engine
                    },
                )
            },
            hasPermission = { true },
            askPermission = {},
            keep = { saved += it },
            error = { errors += it },
        )
        session.tap()
        engines[0].recognitionListener!!.onError(SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE)
        assertEquals(listOf(true, false), opened)
        assertEquals(1, engines[0].destroyed)
        assertEquals(1, engines[1].starts)

        engines[1].recognitionListener!!.onResults(words("first phrase"))
        runCurrent()
        assertEquals("subsequent phrases reuse the fallback engine", listOf(true, false), opened)
        assertEquals(2, engines[1].starts)
        assertTrue(session.listening)

        engines[1].recognitionListener!!.onPartialResults(words("second"))
        session.stopListening()
        engines[1].recognitionListener!!.onResults(words("second phrase"))
        assertEquals(listOf("first phrase second phrase"), saved)
        assertTrue(errors.isEmpty())
        assertEquals(1, engines[1].destroyed)
        assertFalse(session.active)
    }

    @Test fun fallbackFailureReportsErrorOnceWithoutLooping() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        registerSystemRecognitionService()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val errors = mutableListOf<String>()
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                control = rememberDictation(onHeard = {}, onError = { errors += it })
                control?.let { DictationMic(it, "Dictate") }
            }
        }

        // Case 1: fallback also reports ERROR_LANGUAGE_NOT_SUPPORTED (12).
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice1 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        rule.runOnIdle { shadowOf(onDevice1).triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED) }
        rule.waitForIdle()
        val fallback1 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotSame(onDevice1, fallback1)
        assertTrue(errors.isEmpty())

        rule.runOnIdle { shadowOf(fallback1).triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED) }
        rule.waitForIdle()
        assertSame("fallback must not loop and create a third recognizer", fallback1, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        assertTrue(shadowOf(fallback1).isDestroyed)
        assertFalse(control!!.active)
        assertEquals(listOf(app.getString(R.string.chat_dictation_stopped)), errors)

        // Case 2: fallback reports a non-language error (ERROR_NETWORK).
        errors.clear()
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice2 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        rule.runOnIdle { shadowOf(onDevice2).triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE) }
        rule.waitForIdle()
        val fallback2 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotSame(onDevice2, fallback2)

        rule.runOnIdle { shadowOf(fallback2).triggerOnError(SpeechRecognizer.ERROR_NETWORK) }
        rule.waitForIdle()
        assertSame(fallback2, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        assertTrue(shadowOf(fallback2).isDestroyed)
        assertFalse(control!!.active)
        assertEquals(listOf(app.getString(R.string.chat_dictation_stopped)), errors)
    }

    @Test fun onDeviceLanguageErrorsWithoutFallbackAvailableReportErrorImmediately() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        assertFalse(SpeechRecognizer.isRecognitionAvailable(app))
        val errors = mutableListOf<String>()
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                control = rememberDictation(onHeard = {}, onError = { errors += it })
                control?.let { DictationMic(it, "Dictate") }
            }
        }

        for (code in listOf(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED, SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE)) {
            errors.clear()
            rule.onNodeWithContentDescription("Dictate").performClick()
            val onDevice = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
            rule.runOnIdle { shadowOf(onDevice).triggerOnError(code) }
            rule.waitForIdle()
            assertSame(onDevice, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
            assertTrue(shadowOf(onDevice).isDestroyed)
            assertFalse(control!!.active)
            assertEquals(listOf(app.getString(R.string.chat_dictation_stopped)), errors)
        }
    }

    @Test fun quietErrorsPreserveSilentHandlingBeforeAndAfterFallback() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        registerSystemRecognitionService()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val errors = mutableListOf<String>()
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                control = rememberDictation(onHeard = {}, onError = { errors += it })
                control?.let { DictationMic(it, "Dictate") }
            }
        }
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        val onDeviceShadow = shadowOf(onDevice)

        rule.runOnIdle { onDeviceShadow.triggerOnError(SpeechRecognizer.ERROR_NO_MATCH) }
        rule.mainClock.advanceTimeBy(450)
        rule.waitForIdle()
        assertSame(onDevice, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        assertTrue(control!!.active)
        assertTrue(errors.isEmpty())

        rule.runOnIdle { onDeviceShadow.triggerOnError(SpeechRecognizer.ERROR_SPEECH_TIMEOUT) }
        rule.mainClock.advanceTimeBy(450)
        rule.waitForIdle()
        assertSame(onDevice, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        assertTrue(control!!.active)
        assertTrue(errors.isEmpty())

        // Fall back on language error, then verify quiet errors still stay silent on the fallback recognizer.
        rule.runOnIdle { onDeviceShadow.triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED) }
        rule.waitForIdle()
        val fallback = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        assertNotSame(onDevice, fallback)
        val fallbackShadow = shadowOf(fallback)

        rule.runOnIdle { fallbackShadow.triggerOnError(SpeechRecognizer.ERROR_NO_MATCH) }
        rule.mainClock.advanceTimeBy(450)
        rule.waitForIdle()
        assertSame(fallback, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        assertTrue(control!!.active)
        assertTrue(errors.isEmpty())

        rule.runOnIdle { fallbackShadow.triggerOnError(SpeechRecognizer.ERROR_SPEECH_TIMEOUT) }
        rule.mainClock.advanceTimeBy(450)
        rule.waitForIdle()
        assertTrue(control!!.active)
        assertTrue(errors.isEmpty())

        rule.runOnIdle { control!!.cancel() }
        rule.waitForIdle()
        assertTrue(fallbackShadow.isDestroyed)
        assertFalse(control!!.active)
        assertTrue(errors.isEmpty())
    }

    @Test fun cancelAndDisposeAfterFallbackCleanUpFallbackRecognizerAndIgnoreLateCallbacks() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        registerSystemRecognitionService()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val heard = mutableListOf<String>()
        val errors = mutableListOf<String>()
        var mounted by mutableStateOf(true)
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                if (mounted) {
                    control = rememberDictation(onHeard = { heard += it }, onError = { errors += it })
                    control?.let { DictationMic(it, "Dictate") }
                }
            }
        }

        // Cancel drops words and destroys the fallback recognizer.
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice1 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        rule.runOnIdle { shadowOf(onDevice1).triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED) }
        rule.waitForIdle()
        val fallback1 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        val fallbackShadow1 = shadowOf(fallback1)
        rule.runOnIdle { fallbackShadow1.triggerOnPartialResults(words("drop me")) }
        rule.runOnIdle { control!!.cancel() }
        rule.waitForIdle()
        assertTrue(fallbackShadow1.isDestroyed)
        assertFalse(control!!.active)
        rule.runOnIdle {
            fallbackShadow1.triggerOnResults(words("late after cancel"))
            fallbackShadow1.triggerOnError(SpeechRecognizer.ERROR_NETWORK)
        }
        rule.waitForIdle()
        assertTrue(heard.isEmpty())
        assertTrue(errors.isEmpty())

        // Disposing composition keeps partial words and destroys the fallback recognizer cleanly.
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice2 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        rule.runOnIdle { shadowOf(onDevice2).triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE) }
        rule.waitForIdle()
        val fallback2 = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        val fallbackShadow2 = shadowOf(fallback2)
        rule.runOnIdle { fallbackShadow2.triggerOnPartialResults(words("kept on dispose")) }
        rule.runOnIdle { mounted = false }
        rule.waitForIdle()
        assertTrue(fallbackShadow2.isDestroyed)
        assertEquals(listOf("kept on dispose"), heard)
        rule.runOnIdle {
            fallbackShadow2.triggerOnResults(words("late after dispose"))
            fallbackShadow2.triggerOnError(SpeechRecognizer.ERROR_NETWORK)
        }
        rule.waitForIdle()
        assertEquals(listOf("kept on dispose"), heard)
        assertTrue(errors.isEmpty())
    }

    @Test fun stoppingBeforeOnDeviceLanguageErrorDoesNotReopenFallbackRecognizer() {
        ShadowSpeechRecognizer.setIsOnDeviceRecognitionAvailable(true)
        registerSystemRecognitionService()
        shadowOf(app).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val errors = mutableListOf<String>()
        var control: DictationControl? = null
        rule.setContent {
            PersonaTheme(reduceMotion = true) {
                control = rememberDictation(onHeard = {}, onError = { errors += it })
                control?.let { DictationMic(it, "Dictate") }
            }
        }
        rule.onNodeWithContentDescription("Dictate").performClick()
        val onDevice = ShadowSpeechRecognizer.getLatestSpeechRecognizer()
        rule.onNodeWithContentDescription("Stop dictation").performClick()
        rule.runOnIdle { shadowOf(onDevice).triggerOnError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED) }
        rule.waitForIdle()
        assertSame(onDevice, ShadowSpeechRecognizer.getLatestSpeechRecognizer())
        assertTrue(shadowOf(onDevice).isDestroyed)
        assertFalse(control!!.active)
        assertTrue(errors.isEmpty())
    }

    @Test fun startListeningExceptionFallsBackOnceAndStopsOnDoubleFailure() = runTest {
        val intent = Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)

        // 1) On-device startListening throws -> destroys on-device, falls back to default, delivers words.
        val opened1 = mutableListOf<Boolean>()
        val engines1 = mutableListOf<FakeSpeechEngine>()
        val saved1 = mutableListOf<String>()
        val errors1 = mutableListOf<Boolean>()
        val session1 = DictationSession(
            this,
            open = { listener ->
                platformInput(
                    onDevice = true,
                    intent = intent,
                    listener = listener,
                    isDefaultAvailable = { true },
                    openEngine = { onDevice ->
                        opened1 += onDevice
                        val engine = FakeSpeechEngine(onStart = { _, _ -> if (onDevice) throw IllegalStateException("on-device unavailable") })
                        engines1 += engine
                        engine
                    },
                )
            },
            hasPermission = { true },
            askPermission = {},
            keep = { saved1 += it },
            error = { errors1 += it },
        )
        session1.tap()
        assertEquals(listOf(true, false), opened1)
        assertEquals(1, engines1[0].destroyed)
        assertEquals(0, engines1[1].destroyed)
        assertTrue(session1.listening)
        assertTrue(errors1.isEmpty())
        engines1[1].recognitionListener!!.onPartialResults(words("你好"))
        session1.stopListening()
        engines1[1].recognitionListener!!.onResults(words("你好世界"))
        assertEquals(listOf("你好世界"), saved1)
        assertTrue(errors1.isEmpty())
        assertEquals(1, engines1[1].destroyed)

        // 2) On-device startListening throws AND fallback startListening also throws -> both destroyed, fails once, no loop.
        val opened2 = mutableListOf<Boolean>()
        val engines2 = mutableListOf<FakeSpeechEngine>()
        val errors2 = mutableListOf<Boolean>()
        val session2 = DictationSession(
            this,
            open = { listener ->
                platformInput(
                    onDevice = true,
                    intent = intent,
                    listener = listener,
                    isDefaultAvailable = { true },
                    openEngine = { onDevice ->
                        opened2 += onDevice
                        val engine = FakeSpeechEngine(onStart = { _, _ -> throw IllegalStateException("start failed") })
                        engines2 += engine
                        engine
                    },
                )
            },
            hasPermission = { true },
            askPermission = {},
            keep = {},
            error = { errors2 += it },
        )
        session2.tap()
        assertEquals(listOf(true, false), opened2)
        assertEquals(1, engines2[0].destroyed)
        assertEquals(1, engines2[1].destroyed)
        assertFalse(session2.active)
        assertEquals(listOf(false), errors2)

        // 3) On-device openEngine throws -> falls back immediately to default engine.
        val opened3 = mutableListOf<Boolean>()
        val engines3 = mutableListOf<FakeSpeechEngine>()
        val errors3 = mutableListOf<Boolean>()
        val session3 = DictationSession(
            this,
            open = { listener ->
                platformInput(
                    onDevice = true,
                    intent = intent,
                    listener = listener,
                    isDefaultAvailable = { true },
                    openEngine = { onDevice ->
                        opened3 += onDevice
                        if (onDevice) throw IllegalStateException("createOnDevice failed")
                        val engine = FakeSpeechEngine()
                        engines3 += engine
                        engine
                    },
                )
            },
            hasPermission = { true },
            askPermission = {},
            keep = {},
            error = { errors3 += it },
        )
        session3.tap()
        assertEquals(listOf(true, false), opened3)
        assertEquals(1, engines3.size)
        assertEquals(1, engines3[0].starts)
        assertTrue(session3.listening)
        assertTrue(errors3.isEmpty())
        session3.finish(false)
        assertEquals(1, engines3[0].destroyed)

        // 4) Close triggered during fallback cleans up the newly opened fallback engine without reporting error.
        val engines4 = mutableListOf<FakeSpeechEngine>()
        val errors4 = mutableListOf<Boolean>()
        lateinit var session4: DictationSession
        session4 = DictationSession(
            this,
            open = { listener ->
                platformInput(
                    onDevice = true,
                    intent = intent,
                    listener = listener,
                    isDefaultAvailable = { true },
                    openEngine = { onDevice ->
                        val engine = FakeSpeechEngine(onStart = { _, _ -> if (!onDevice) session4.finish(false) })
                        engines4 += engine
                        engine
                    },
                )
            },
            hasPermission = { true },
            askPermission = {},
            keep = {},
            error = { errors4 += it },
        )
        session4.tap()
        engines4[0].recognitionListener!!.onError(SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED)
        assertEquals(2, engines4.size)
        assertEquals(1, engines4[0].destroyed)
        assertEquals(1, engines4[1].destroyed)
        assertFalse(session4.active)
        assertTrue(errors4.isEmpty())

        // 5) On-device startListening throws with no fallback available -> does not open default, fails once, destroys on-device.
        val opened5 = mutableListOf<Boolean>()
        val engines5 = mutableListOf<FakeSpeechEngine>()
        val errors5 = mutableListOf<Boolean>()
        val session5 = DictationSession(
            this,
            open = { listener ->
                platformInput(
                    onDevice = true,
                    intent = intent,
                    listener = listener,
                    isDefaultAvailable = { false },
                    openEngine = { onDevice ->
                        opened5 += onDevice
                        val engine = FakeSpeechEngine(onStart = { _, _ -> throw IllegalStateException("on-device start failed") })
                        engines5 += engine
                        engine
                    },
                )
            },
            hasPermission = { true },
            askPermission = {},
            keep = {},
            error = { errors5 += it },
        )
        session5.tap()
        assertEquals(listOf(true), opened5)
        assertEquals(1, engines5.size)
        assertEquals(1, engines5[0].destroyed)
        assertFalse(session5.active)
        assertEquals(listOf(false), errors5)
    }
}
