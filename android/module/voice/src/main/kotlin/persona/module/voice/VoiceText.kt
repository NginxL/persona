package persona.module.voice

import persona.core.designsystem.UiText
import persona.core.designsystem.operationFeedback
import persona.core.domain.FailureAction
import android.content.res.Resources
import androidx.annotation.StringRes
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.LocalResources

/** The voice page's words (docs/android/android.md "Voice chat"), in the language of [resources]. */
internal class VoiceText(private val resources: Resources) {
    private fun get(@StringRes id: Int, vararg args: Any): String = resources.getString(id, *args)

    val title get() = get(R.string.voice_title)
    val connecting get() = get(R.string.voice_connecting)
    val listening get() = get(R.string.voice_listening)
    val speaking get() = get(R.string.voice_speaking)
    val muted get() = get(R.string.voice_muted)
    val ended get() = get(R.string.voice_ended)
    /** A tool the extension did not name. */
    val working get() = get(R.string.voice_working)
    val restarted get() = get(R.string.voice_restarted)
    val noExtension get() = get(R.string.voice_no_extension)
    val microphone get() = get(R.string.voice_microphone)
    val modelMissing get() = get(R.string.voice_model_missing)
    val modelUnavailable get() = get(R.string.voice_model_unavailable)
    val configUnreadable get() = get(R.string.voice_config_unreadable)
    val askTitle get() = get(R.string.voice_ask_title)
    val askText get() = get(R.string.voice_ask_text)
    val deniedTitle get() = get(R.string.voice_denied_title)
    val deniedText get() = get(R.string.voice_denied_text)
    val allow get() = get(R.string.voice_allow)
    val voices get() = get(R.string.voice_voices)
    val noVoices get() = get(R.string.voice_no_voices)
    /** An error frame without words. */
    val error get() = get(R.string.voice_error)

    fun unreachable(reason: String) = get(R.string.voice_unreachable, reason)
    fun tool(label: String) = get(R.string.voice_tool, label)
    fun voicesFailed(reason: String) = get(R.string.voice_voices_failed, reason)

    internal fun string(@StringRes id: Int, vararg args: Any) = get(id, *args)
    internal fun resolve(text: UiText) = text.resolve(resources)
}

/** The page's words in the language it is shown in. */
@Composable
internal fun rememberVoiceText(): VoiceText {
    val resources = LocalResources.current
    return remember(resources) { VoiceText(resources) }
}

/** What one conversation is doing, as the page shows it. */
internal data class VoiceState(
    val phase: Phase = Phase.Connecting,
    val lines: List<Line> = emptyList(),
    /** The tool being used right now ("Search knowledge"), said in place of the status. */
    val tool: String? = null,
    /** Persona's voice is playing. */
    val speaking: Boolean = false,
    val muted: Boolean = false,
    /** What went wrong, under the status; a fatal one also ended the conversation. */
    val error: String? = null,
) {
    enum class Phase { Connecting, Live, Ended }
}

/** The status line under the orb. A tool in use is said over speaking: it is why Persona is quiet or still going. */
internal fun statusText(state: VoiceState, text: VoiceText): String = when {
    state.phase == VoiceState.Phase.Connecting -> text.connecting
    state.phase == VoiceState.Phase.Ended -> text.ended
    state.tool != null -> text.tool(state.tool)
    state.speaking -> text.speaking
    state.muted -> text.muted
    else -> text.listening
}

/** How a voice reads in the "Voice" sheet: "Female · Chinese, English · Default". */
internal fun voiceDetail(voice: VoiceOption, isDefault: Boolean, text: VoiceText): String? = listOfNotNull(
    when (voice.gender) { "female" -> text.string(R.string.voice_female); "male" -> text.string(R.string.voice_male); else -> null },
    voice.languages.mapNotNull { Languages[it] }.joinToString(text.string(R.string.voice_list_separator)) { text.string(it) }.ifEmpty { null },
    text.string(R.string.voice_default).takeIf { isDefault },
).joinToString(" · ").ifEmpty { null }

private val Languages = mapOf(
    "zh" to R.string.voice_language_zh, "en" to R.string.voice_language_en, "ja" to R.string.voice_language_ja,
    "ko" to R.string.voice_language_ko, "es" to R.string.voice_language_es, "fr" to R.string.voice_language_fr,
    "de" to R.string.voice_language_de, "yue" to R.string.voice_language_yue,
)

/** Why the socket could not be used. [live]: it dropped in the middle of a conversation. */
internal fun connectFailure(error: Throwable, status: Int?, live: Boolean, text: VoiceText): String = when {
    status == 404 -> text.noExtension
    // Mid-conversation it is nearly always the extension or the hub restarting, or the network; say that, not the error.
    live -> text.restarted
    else -> text.unreachable(reason(error, status, text))
}

/** The extension or the hub closed the socket: 1001 is an extension restarting (a deploy); 1000 is the end of it. */
internal fun closeMessage(code: Int, reason: String, live: Boolean, text: VoiceText): String? = when {
    code == 1001 -> text.restarted
    code == 1000 -> null
    reason.isNotBlank() -> reason.trim()
    live -> text.restarted
    else -> null
}

/** Why the model configuration or the extension's models could not be read; the page offers to try again. */
internal fun loadFailure(error: Throwable, text: VoiceText): String {
    val feedback = operationFeedback(error)
    return when {
        feedback.offline -> text.string(R.string.voice_load_offline)
        feedback.action == FailureAction.PairAgain || feedback.action == FailureAction.UpdateHub -> text.resolve(feedback.text)
        else -> hubMessage(error)?.let { text.string(R.string.voice_load_no_result_reason, it) } ?: text.string(R.string.voice_load_no_result)
    }
}

/** Why the voices could not be read, to follow "Couldn't load voices: ". */
internal fun voicesFailure(error: Throwable, text: VoiceText): String {
    val feedback = operationFeedback(error)
    return when {
        feedback.offline -> text.string(R.string.voice_hub_unreachable)
        feedback.action == FailureAction.PairAgain || feedback.action == FailureAction.UpdateHub -> text.resolve(feedback.text)
        else -> hubMessage(error) ?: text.string(R.string.voice_no_result)
    }
}

private fun reason(error: Throwable, status: Int?, text: VoiceText): String {
    val feedback = operationFeedback(error)
    return when {
        // The hub refuses extensions to a device that may only read and answer (docs/extensions/extensions.md); not an expired pairing.
        status == 403 -> text.string(R.string.voice_device_not_allowed)
        status == 401 || feedback.action == FailureAction.PairAgain -> text.resolve(feedback.text)
        status != null && status >= 500 -> text.string(R.string.voice_service_no_response)
        status != null -> "HTTP $status"
        feedback.action == FailureAction.UpdateHub -> text.resolve(feedback.text)
        feedback.offline -> text.string(R.string.voice_hub_unreachable)
        else -> hubMessage(error) ?: text.string(R.string.voice_connect_failed)
    }
}

// A transport failure without the hub's words carries only its kind ("Protocol"), which says nothing to a person.
private val KindName = Regex("[A-Z][A-Za-z]+")

private fun hubMessage(error: Throwable): String? = error.message?.trim()?.takeIf { it.isNotEmpty() && !it.matches(KindName) }

/** Where setting up a conversation stands. */
internal sealed interface VoiceSetup {
    data object Loading : VoiceSetup

    /** The model configuration says the voice model cannot be used as it stands: [reason] says why. */
    data class Unavailable(val reason: String) : VoiceSetup

    /** The configuration could not be read: not the same as having none (docs/assistant/model-config.md "When changes take effect"). */
    data class Failed(val message: String) : VoiceSetup

    /** Ready to talk with [model]; null: the extension's own default. */
    data class Ready(val model: String?) : VoiceSetup

    /** Stopped before it started (the app went to the background while it was being set up). */
    data object Stopped : VoiceSetup
}

/**
 * The model of the next conversation, from the model configuration of `plugin.<id>` ([choice]) and the extension's
 * models: an older hub without model configuration (null) gets the extension's default; a purpose whose status says it
 * cannot be used, or a model the extension no longer offers, is not quietly swapped for another.
 */
internal fun resolveVoiceModel(choice: VoiceModelChoice?, models: VoiceModelOptions, text: VoiceText): VoiceSetup {
    val default = models.default?.takeIf { it.isNotBlank() }
    if (choice == null) return VoiceSetup.Ready(default)
    choice.status?.trim()?.takeIf { it.isNotEmpty() && it != "未验证" }?.let { return VoiceSetup.Unavailable(it) } // i18n-ignore: the hub's status token for a usable, unverified model
    val model = choice.model?.takeIf { it.isNotBlank() } ?: return VoiceSetup.Ready(default)
    if (models.models.none { it.id == model }) return VoiceSetup.Unavailable(text.modelMissing)
    return VoiceSetup.Ready(model)
}
