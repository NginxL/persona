package persona.module.voice

import persona.core.contracts.JsonCodec
import kotlin.math.roundToInt
import kotlin.math.sqrt
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.put

/** What the extension says on the voice socket (docs/extensions/extensions.md "Voice"), its JSON frames decoded. */
internal sealed interface VoiceEvent {
    /** The session is set up: record at [inputRate], play at [outputRate] (the extension's, never assumed). */
    data class Ready(val inputRate: Int, val outputRate: Int) : VoiceEvent

    /** Drop what has not been played yet: the user talked over it, or the answer was cancelled. */
    data object Clear : VoiceEvent

    /** A caption: the whole text so far of line [id]; a [final] line does not change any more. */
    data class Transcript(val id: String, val user: Boolean, val text: String, val final: Boolean) : VoiceEvent

    /** A tool in use ("Search knowledge"...) or, [done], finished. Several can be open at once. */
    data class Tool(val id: String, val label: String?, val done: Boolean) : VoiceEvent

    /** Something went wrong; after a [fatal] one the extension closes the socket. */
    data class Error(val message: String?, val fatal: Boolean) : VoiceEvent

    companion object {
        private val SampleRates = 8_000..96_000

        /** null for a frame this app does not know (newer extensions may add some) or cannot read. */
        fun parse(text: String): VoiceEvent? {
            val frame = runCatching { JsonCodec.parseToJsonElement(text) }.getOrNull() as? JsonObject ?: return null
            fun string(key: String) = (frame[key] as? JsonPrimitive)?.takeIf { it.isString }?.content
            fun flag(key: String) = (frame[key] as? JsonPrimitive)?.booleanOrNull ?: false
            fun rate(key: String, default: Int) = (frame[key] as? JsonPrimitive)?.intOrNull?.takeIf { it in SampleRates } ?: default
            return when (string("type")) {
                "ready" -> Ready(rate("input_rate", 16_000), rate("output_rate", 24_000))
                "clear" -> Clear
                "transcript" -> Transcript(string("id") ?: return null, string("role") == "user", string("text").orEmpty(), flag("final"))
                "tool" -> Tool(string("id") ?: return null, string("label")?.takeIf { it.isNotBlank() }, flag("done"))
                "error" -> Error(string("message")?.takeIf { it.isNotBlank() }, flag("fatal"))
                else -> null
            }
        }
    }
}

/** One caption line of the conversation: the user's or Persona's, faint until [final]. */
internal data class Line(val id: String, val user: Boolean, val text: String, val final: Boolean)

/**
 * Folds a caption into the lines: the same id restates its line in place (each event carries the whole text so far),
 * a new one is appended, and a new line with nothing in it (an answer cut off before a word) is not.
 */
internal fun List<Line>.with(t: VoiceEvent.Transcript): List<Line> {
    val i = indexOfFirst { it.id == t.id }
    val line = Line(t.id, t.user, t.text, t.final)
    return when {
        i >= 0 -> toMutableList().also { it[i] = line }
        t.text.isBlank() -> this
        else -> this + line
    }.filter { it.text.isNotBlank() || !it.final }
}

/** The first frame on the socket; model and voice only when chosen (else the extension's own). */
internal fun startFrame(voice: String?, model: String? = null): String = buildJsonObject {
    put("type", "start")
    if (!model.isNullOrEmpty()) put("model", model)
    if (!voice.isNullOrEmpty()) put("voice", voice)
}.toString()

/** Stops the answer being said, the same as talking over it. */
internal const val InterruptFrame = """{"type":"interrupt"}"""

/**
 * The tools the extension has open, in the order they started. Several can run at once (a quick lookup while something
 * handed to the main conversation runs on); the newest one still open is what the page says.
 */
internal data class OpenTools(private val open: List<Pair<String, String>> = emptyList()) {
    val current: String? get() = open.lastOrNull()?.second

    fun with(event: VoiceEvent.Tool, unnamed: String): OpenTools = when {
        event.done -> copy(open = open.filterNot { it.first == event.id })
        open.any { it.first == event.id } -> copy(open = open.map { if (it.first == event.id) it.first to (event.label ?: it.second) else it })
        else -> copy(open = open + (event.id to (event.label ?: unnamed)))
    }
}

/** Loudness 0…1 of [count] PCM16 samples: their RMS against a level a voice close to the phone reaches. */
internal fun pcmLevel(samples: ShortArray, count: Int): Float {
    if (count <= 0) return 0f
    var sum = 0.0
    for (i in 0 until count) {
        val v = samples[i].toDouble()
        sum += v * v
    }
    return (sqrt(sum / count) / 6000.0).toFloat().coerceIn(0f, 1f)
}

/** The orb follows the voice closely without jittering: 40% of the new level, kept to two decimals, silence is 0. */
internal fun smoothLevel(previous: Float, next: Float): Float {
    val level = previous * 0.6f + next * 0.4f
    return if (level < 0.01f) 0f else (level * 100).roundToInt() / 100f
}
