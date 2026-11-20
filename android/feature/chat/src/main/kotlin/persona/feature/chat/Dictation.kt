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

private fun platformInput(context: Context, onDevice: Boolean, intent: Intent, listener: DictationSession.Listener): DictationSession.Input {
    val speech = if (onDevice && Build.VERSION.SDK_INT >= 31) SpeechRecognizer.createOnDeviceSpeechRecognizer(context)
        else SpeechRecognizer.createSpeechRecognizer(context)
    fun words(results: Bundle?) = results?.getStringArrayList(SpeechRecognizer.RESULTS_RECOGNITION)?.firstOrNull()?.trim().orEmpty()
    speech.setRecognitionListener(object : RecognitionListener {
        override fun onPartialResults(partialResults: Bundle?) = listener.partial(words(partialResults))
        override fun onResults(results: Bundle?) = listener.result(words(results))
        override fun onError(error: Int) = listener.failed(error == SpeechRecognizer.ERROR_NO_MATCH || error == SpeechRecognizer.ERROR_SPEECH_TIMEOUT)
        override fun onReadyForSpeech(params: Bundle?) {}
        override fun onBeginningOfSpeech() {}
        override fun onRmsChanged(rmsdB: Float) {}
        override fun onBufferReceived(buffer: ByteArray?) {}
        override fun onEndOfSpeech() {}
        override fun onEvent(eventType: Int, params: Bundle?) {}
    })
    return object : DictationSession.Input {
        override fun start() = speech.startListening(intent)
        override fun stop() = speech.stopListening()
        override fun close() { speech.destroy() }
    }
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
