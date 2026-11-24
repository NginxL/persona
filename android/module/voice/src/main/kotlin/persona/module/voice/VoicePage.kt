package persona.module.voice

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/** What the voice page knows besides the conversation itself. */
internal data class VoicePageState(
    val setup: VoiceSetup = VoiceSetup.Loading,
    val models: VoiceModelOptions = VoiceModelOptions(),
    val voices: VoiceOptions? = null,
    val voicesError: String? = null,
    /** The voice picked on this phone for this extension, if any. */
    val voice: String? = null,
) {
    /** The voice a conversation uses: the picked one while the extension still offers it; null is the extension's default. */
    val chosenVoice: String? get() = voice?.takeIf { picked -> voices == null || voices.voices.any { it.id == picked } }

    /** The voice the "Voice" sheet checks. */
    val checkedVoice: String? get() = chosenVoice ?: voices?.default

    /** The bar's subtitle: model · voice, each when known. */
    val subtitle: String? get() {
        val model = (setup as? VoiceSetup.Ready)?.model?.let { id -> models.models.firstOrNull { it.id == id }?.label ?: id }
        val voice = voices?.voices?.firstOrNull { it.id == checkedVoice }?.name?.takeIf { it.isNotBlank() }
        return listOfNotNull(model, voice).joinToString(" · ").ifEmpty { null }
    }
}

/**
 * The voice page without the screen: reads the model from the hub's model configuration and the voices, keeps the
 * picked voice, and runs one [VoiceSession] at a time. Picking another voice starts a new conversation; starting again
 * reads the model configuration again (it applies from the next conversation, docs/assistant/model-config.md).
 */
internal class VoicePage(
    private val extensions: VoiceExtensions,
    private val extensionId: String,
    private val prefs: VoicePrefs,
    private val scope: CoroutineScope,
    private val text: VoiceText,
    private val audio: () -> VoiceAudio,
) {
    private val mutable = MutableStateFlow(VoicePageState(voice = prefs.voice(extensionId)))
    val state: StateFlow<VoicePageState> = mutable.asStateFlow()
    private val current = MutableStateFlow<VoiceSession?>(null)

    /** The conversation running now or the last one; null while setting up. */
    val session: StateFlow<VoiceSession?> = current.asStateFlow()
    private var loading: Job? = null

    /** Reads the model configuration and the voices, then starts a conversation. Also "Start again" and "Retry". */
    fun begin() {
        loading?.cancel()
        current.value?.stop()
        current.value = null
        mutable.update { it.copy(setup = VoiceSetup.Loading) }
        loading = scope.launch {
            val setup = try {
                prepare()
            } catch (cancelled: CancellationException) {
                throw cancelled
            } catch (error: Exception) {
                VoiceSetup.Failed(loadFailure(error, text))
            }
            mutable.update { it.copy(setup = setup) }
            if (setup !is VoiceSetup.Ready) return@launch
            loadVoices(setup.model)
            talk(setup.model)
        }
    }

    private suspend fun prepare(): VoiceSetup = coroutineScope {
        val models = async { extensions.models(extensionId) }
        val choice = async { extensions.modelChoice(extensionId) }
        val catalog = models.await()
        mutable.update { it.copy(models = catalog) }
        resolveVoiceModel(choice.await(), catalog, text)
    }

    /** A voice list that cannot be read does not stop the conversation: the picked voice (or the default) is used. */
    private suspend fun loadVoices(model: String?) {
        try {
            val voices = extensions.voices(extensionId, model)
            mutable.update { it.copy(voices = voices, voicesError = null) }
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (error: Exception) {
            mutable.update { it.copy(voices = null, voicesError = voicesFailure(error, text)) }
        }
    }

    private fun talk(model: String?) {
        val session = VoiceSession(extensions, extensionId, state.value.chosenVoice, model, audio(), text)
        current.value = session
        session.start()
    }

    /** Picks [voice] for this extension on this phone; a conversation with another voice starts again with it. */
    fun pickVoice(voice: String) {
        val before = state.value
        if (voice != before.voice) {
            prefs.setVoice(extensionId, voice)
            mutable.update { it.copy(voice = voice) }
        }
        if (voice == before.checkedVoice) return // the voice it already speaks with
        val setup = before.setup as? VoiceSetup.Ready ?: return
        if (current.value == null) return // still setting up: it starts with the new voice
        current.value?.stop()
        talk(setup.model)
    }

    /** The app left the foreground: the conversation ends (nothing is recorded in the background). */
    fun pause() {
        val wasLoading = loading?.isActive == true
        loading?.cancel()
        current.value?.stop()
        if (wasLoading) mutable.update { it.copy(setup = VoiceSetup.Stopped) }
    }

    /** The page is gone. */
    fun close() {
        loading?.cancel()
        current.value?.stop()
    }
}
