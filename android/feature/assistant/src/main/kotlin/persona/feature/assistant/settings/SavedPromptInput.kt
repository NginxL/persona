package persona.feature.assistant.settings

import persona.core.contracts.api.SavedPrompt

internal object SavedPromptInput {
    const val NAME_MAX = 32
    const val TEXT_MAX = 8000

    private val namePattern = Regex("[\\p{L}\\p{Nd}\\p{Mn}_.-]+")

    fun name(raw: String): String = raw.trim().trimStart('/')
    fun text(raw: String): String = raw.replace("\r\n", "\n").replace('\r', '\n').trim()
    fun length(text: String): Int = text.codePointCount(0, text.length)

    fun validName(name: String): Boolean = length(name) <= NAME_MAX && namePattern.matches(name)
    fun nameTaken(name: String, id: String?, prompts: List<SavedPrompt>): Boolean =
        prompts.any { it.id != id && it.name.equals(name, ignoreCase = true) }

    fun validText(text: String): Boolean = text.isNotEmpty() && length(text) <= TEXT_MAX &&
        text.none { it.isISOControl() && it != '\n' && it != '\t' }
}
