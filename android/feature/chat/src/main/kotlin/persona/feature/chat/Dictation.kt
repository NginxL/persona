package persona.feature.chat

import android.Manifest
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import android.os.Bundle
import android.speech.RecognitionListener
import android.speech.RecognizerIntent
import android.speech.SpeechRecognizer
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.*
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.text.TextRange
import androidx.compose.ui.text.input.TextFieldValue
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.LifecycleEventObserver
import androidx.lifecycle.compose.LocalLifecycleOwner
import persona.core.contracts.SessionKey

/** Both writing fields use the phone's speech service. Phones with only a voice-input activity retain their system UI. */
class DictationControl internal constructor(internal val session: DictationSession?, private val fallback: () -> Unit) {
    val active get() = session?.active == true
    val heard get() = session?.heard.orEmpty()
    fun cancel() { session?.finish(false) }
    internal fun stopListening() { session?.stopListening() }
    internal fun tap() { if (session == null) fallback() else session.tap() }
}

@Composable
fun rememberDictation(onHeard: (String) -> Unit, onError: (String) -> Unit, prompt: String? = null): DictationControl? {
    val context = LocalContext.current
    val resources = LocalResources.current
    val locale = resources.configuration.locales[0]
    val intent = remember(locale, prompt) {
        Intent(RecognizerIntent.ACTION_RECOGNIZE_SPEECH)
            .putExtra(RecognizerIntent.EXTRA_LANGUAGE_MODEL, RecognizerIntent.LANGUAGE_MODEL_FREE_FORM)
            .putExtra(RecognizerIntent.EXTRA_LANGUAGE, if (locale.language == "zh") "zh-CN" else locale.toLanguageTag())
            .putExtra(RecognizerIntent.EXTRA_PROMPT, prompt ?: resources.getString(R.string.chat_dictation_prompt))
            .putExtra(RecognizerIntent.EXTRA_PARTIAL_RESULTS, true)
    }
    val onDevice = remember(context) { Build.VERSION.SDK_INT >= 31 && SpeechRecognizer.isOnDeviceRecognitionAvailable(context) }
    val inApp = remember(context, onDevice) { onDevice || SpeechRecognizer.isRecognitionAvailable(context) }
    val heard by rememberUpdatedState(onHeard)
    val failed by rememberUpdatedState(onError)
    if (!inApp) {
        if (!remember(context, intent) { intent.resolveActivity(context.packageManager) != null }) return null
        val launcher = rememberLauncherForActivityResult(ActivityResultContracts.StartActivityForResult()) { result ->
            if (result.resultCode == Activity.RESULT_OK) result.data?.getStringArrayListExtra(RecognizerIntent.EXTRA_RESULTS)
                ?.firstOrNull()?.trim()?.takeIf(String::isNotEmpty)?.let(heard)
        }
        return remember(launcher, intent) { DictationControl(null) {
            runCatching { launcher.launch(intent) }.onFailure { failed(resources.getString(R.string.chat_dictation_unavailable)) }
        } }
    }
    val scope = rememberCoroutineScope()
    var answer by remember { mutableStateOf<((Boolean) -> Unit)?>(null) }
    val permission = rememberLauncherForActivityResult(ActivityResultContracts.RequestPermission()) { granted ->
        val callback = answer
        answer = null
        callback?.invoke(granted)
    }
    val session = remember(context, intent, onDevice) {
        DictationSession(scope, open = { platformInput(context, onDevice, intent, it) },
            hasPermission = { context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED },
            askPermission = { reply -> answer = reply; permission.launch(Manifest.permission.RECORD_AUDIO) },
            keep = { heard(it) }, error = { denied -> failed(resources.getString(if (denied) R.string.chat_dictation_permission else R.string.chat_dictation_stopped)) })
    }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    DisposableEffect(session, lifecycle) {
        fun leave() {
            answer = null
            session.finish(true)
        }
        val observer = LifecycleEventObserver { _, event -> if (event == Lifecycle.Event.ON_STOP) leave() }
        lifecycle.addObserver(observer)
        onDispose { lifecycle.removeObserver(observer); leave() }
    }
    return remember(session) { DictationControl(session) {} }
}

@Composable
internal fun rememberComposerDictation(session: SessionKey, state: ChatComposerState, available: Boolean, onNotice: (String) -> Unit) =
    key(session, state) { if (available) rememberDictation(onHeard = state::appendDictation, onError = onNotice) else null }

internal interface SpeechEngine {
    fun setListener(listener: RecognitionListener)
    fun startListening(intent: Intent)
    fun stopListening()
    fun destroy()
}

private class PlatformSpeechEngine(private val speech: SpeechRecognizer) : SpeechEngine {
    override fun setListener(listener: RecognitionListener) = speech.setRecognitionListener(listener)
    override fun startListening(intent: Intent) = speech.startListening(intent)
    override fun stopListening() = speech.stopListening()
    override fun destroy() = speech.destroy()
}

private fun isLanguageFallbackError(error: Int): Boolean =
    Build.VERSION.SDK_INT >= 31 &&
        (error == SpeechRecognizer.ERROR_LANGUAGE_NOT_SUPPORTED || error == SpeechRecognizer.ERROR_LANGUAGE_UNAVAILABLE)

private fun destroyQuietly(engine: SpeechEngine?) {
    if (engine == null) return
    try {
        engine.destroy()
    } catch (_: RuntimeException) {
    }
}

private fun platformInput(context: Context, onDevice: Boolean, intent: Intent, listener: DictationSession.Listener): DictationSession.Input =
    platformInput(
        onDevice = onDevice && Build.VERSION.SDK_INT >= 31,
        intent = intent,
        listener = listener,
        isDefaultAvailable = { SpeechRecognizer.isRecognitionAvailable(context) },
        openEngine = { preferOnDevice ->
            PlatformSpeechEngine(
                if (preferOnDevice && Build.VERSION.SDK_INT >= 31) SpeechRecognizer.createOnDeviceSpeechRecognizer(context)
                else SpeechRecognizer.createSpeechRecognizer(context),
            )
        },
    )

internal fun platformInput(
    onDevice: Boolean,
    intent: Intent,
    listener: DictationSession.Listener,
    isDefaultAvailable: () -> Boolean,
    openEngine: (onDevice: Boolean) -> SpeechEngine,
): DictationSession.Input = PlatformDictationInput(onDevice, intent, listener, isDefaultAvailable, openEngine)

private class PlatformDictationInput(
    onDevice: Boolean,
    private val intent: Intent,
    private val listener: DictationSession.Listener,
    private val isDefaultAvailable: () -> Boolean,
    private val openEngine: (onDevice: Boolean) -> SpeechEngine,
) : DictationSession.Input {
    private var usingOnDevice = onDevice
    private var fellBack = false
    private var stopping = false
    private var closed = false
    private var current: SpeechEngine? = null

    init {
        current = if (usingOnDevice) {
            try {
                openAndAttach(true)
            } catch (e: RuntimeException) {
                if (!isDefaultAvailable()) throw e
                usingOnDevice = false
                fellBack = true
                openAndAttach(false)
            }
        } else {
            openAndAttach(false)
        }
    }

    override fun start() {
        if (closed) return
        stopping = false
        val engine = current ?: return
        try {
            engine.startListening(intent)
        } catch (e: RuntimeException) {
            if (closed || current !== engine) return
            if (!fallbackToDefault() && !closed) throw e
        }
    }

    override fun stop() {
        if (closed) return
        stopping = true
        current?.stopListening()
    }

    override fun close() {
        closed = true
        stopping = false
        val previous = current
        current = null
        destroyQuietly(previous)
    }

    private fun openAndAttach(preferOnDevice: Boolean): SpeechEngine {
        val engine = openEngine(preferOnDevice)
        try {
            attach(engine)
        } catch (e: RuntimeException) {
            destroyQuietly(engine)
            throw e
        }
        return engine
    }

    private fun attach(engine: SpeechEngine) {
        engine.setListener(object : RecognitionListener {
            override fun onPartialResults(partialResults: Bundle?) {
                if (!closed && current === engine) listener.partial(words(partialResults))
            }
            override fun onResults(results: Bundle?) {
                if (!closed && current === engine) listener.result(words(results))
            }
            override fun onError(error: Int) {
                if (closed || current !== engine) return
                if (isLanguageFallbackError(error) && (fallbackToDefault() || closed)) return
                listener.failed(error == SpeechRecognizer.ERROR_NO_MATCH || error == SpeechRecognizer.ERROR_SPEECH_TIMEOUT)
            }
            override fun onReadyForSpeech(params: Bundle?) {}
            override fun onBeginningOfSpeech() {}
            override fun onRmsChanged(rmsdB: Float) {}
            override fun onBufferReceived(buffer: ByteArray?) {}
            override fun onEndOfSpeech() {}
            override fun onEvent(eventType: Int, params: Bundle?) {}
        })
    }

    private fun fallbackToDefault(): Boolean {
        if (closed || stopping || !usingOnDevice || fellBack || !isDefaultAvailable()) return false
        usingOnDevice = false
        fellBack = true
        val previous = current
        current = null
        destroyQuietly(previous)
        if (closed) return false
        val next = try {
            openAndAttach(false)
        } catch (_: RuntimeException) {
            return false
        }
        if (closed) {
            destroyQuietly(next)
            return false
        }
        current = next
        return try {
            next.startListening(intent)
            if (closed) {
                if (current === next) {
                    current = null
                    destroyQuietly(next)
                }
                false
            } else {
                true
            }
        } catch (_: RuntimeException) {
            if (current === next) {
                current = null
                destroyQuietly(next)
            }
            false
        }
    }

    private fun words(results: Bundle?) =
        results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()?.trim().orEmpty()
}

/**
 * What dictation heard, at the end of the draft: right after it, with one space only between Latin text and a Latin
 * start ("run the tests" + "and commit"); Chinese needs none, and spaces the draft ended with are dropped.
 */
fun withDictation(draft: String, heard: String): String {
    if (draft.isBlank()) return heard
    if (heard.isEmpty()) return draft
    val text = draft.trimEnd()
    val last = text.last()
    val separator = if (last.code < 128 && last.isLetterOrDigit() && heard.first().code < 128) " " else ""
    return text + separator + heard
}

/** Puts what dictation heard at the end of the draft, the cursor after it; never at the selection (no padding spaces). */
internal fun ChatComposerState.appendDictation(heard: String) {
    beginEdit()
    val text = withDictation(value.text, heard)
    value = TextFieldValue(text, TextRange(text.length))
}
