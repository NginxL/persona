package persona.core.contracts

import java.time.Instant

/** The hub continues a session's failed start after its terminal question is answered. */
const val START_CONTINUE_FEATURE = "start_continue"
/** The hub answers a planned task's startup question and restarts it in the kept tab. */
const val TODO_START_ANSWER_FEATURE = "todo_start_answer"
/** A start stopped on its shell's question; only this code carries a startup question. */
const val SHELL_WAITING = "shell_waiting"

/** The question a start stopped on, as the computer listed it. */
data class ShellQuestion(
    val line: String,
    val choices: List<String>,
    val kind: String = "",
    val screen: String? = null,
    val pane: String? = null,
    val lineMode: Boolean = false,
    val secret: Boolean = false,
    val default: String? = null,
    val keptUntil: String? = null,
) {
    /** Missing or malformed expiry stays open; only a parseable expiry at or before [now] closes the card. */
    fun isOpenAt(now: Instant): Boolean = keptUntil?.let { raw ->
        runCatching { Instant.parse(raw) }.getOrNull()?.let { it.isAfter(now) } ?: true
    } ?: true
}

/** A shell_waiting question in a session's or plan task's error arguments; null without a line and at least one choice. */
fun shellQuestion(code: String?, args: Map<String, String>?): ShellQuestion? {
    if (code != SHELL_WAITING) return null
    val line = args?.get("line").orEmpty()
    val choices = args?.get("choices").orEmpty().split(',').map(String::trim).filter(String::isNotEmpty)
    if (line.isEmpty() || choices.isEmpty()) return null
    fun text(key: String) = args?.get(key).orEmpty()
    fun optional(key: String) = text(key).takeIf(String::isNotEmpty)
    return ShellQuestion(
        line = line,
        choices = choices,
        kind = text("kind"),
        screen = optional("screen"),
        pane = optional("pane"),
        lineMode = text("line_mode") == "true",
        secret = text("secret") == "true",
        default = optional("default"),
        keptUntil = optional("kept_until"),
    )
}
