package persona.module.voice

import persona.core.contracts.JsonCodec
import java.io.File
import java.io.IOException
import java.nio.file.Files
import java.nio.file.StandardCopyOption
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull

/**
 * The voice picked on this phone for each voice extension, kept with the pairing: one small file in the pairing's
 * own directory (`hub-data/<pairing>/voice.json`), erased with it when the phone is unpaired or paired again. The
 * model is not here: it belongs to the hub's model configuration (docs/assistant/model-config.md).
 */
internal class VoicePrefs(private val file: File) {
    @Synchronized
    fun voice(extension: String): String? = read()[extension]

    /** Writes only while the pairing's directory exists: never recreates a pairing that was just erased. */
    @Synchronized
    fun setVoice(extension: String, voice: String) {
        val directory = file.parentFile ?: return
        if (!directory.isDirectory) return
        val voices = read() + (extension to voice)
        val text = JsonObject(voices.mapValues { JsonPrimitive(it.value) }).toString()
        val temporary = File(directory, "${file.name}.tmp")
        try {
            temporary.writeText(text)
            Files.move(temporary.toPath(), file.toPath(), StandardCopyOption.REPLACE_EXISTING, StandardCopyOption.ATOMIC_MOVE)
        } catch (_: IOException) {
            temporary.delete()
        }
    }

    private fun read(): Map<String, String> = runCatching {
        if (!file.isFile) return emptyMap()
        val json = JsonCodec.parseToJsonElement(file.readText()) as? JsonObject ?: return emptyMap()
        json.mapNotNull { (key, value) -> (value as? JsonPrimitive)?.contentOrNull?.takeIf { it.isNotBlank() }?.let { key to it } }.toMap()
    }.getOrDefault(emptyMap())

    companion object {
        /** The file of the pairing [identity] (its scope path, `<hub>/<generation>`) under the app's [filesDir]. */
        fun of(filesDir: File, identity: String) = VoicePrefs(File(filesDir, "hub-data/$identity/voice.json"))
    }
}
