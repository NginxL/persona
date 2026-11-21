package persona.core.domain.answer

import persona.core.contracts.CoreText.Key
import persona.core.contracts.CoreText
import persona.core.contracts.Interaction
import persona.core.contracts.Item
import persona.core.domain.toolSubject
import persona.core.domain.toolSubjectText
import persona.core.domain.toolVerb
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive

data class PermissionSummary(val text: CoreText, val description: String? = null, val monospace: Boolean = false)

/** Keep hub summaries intact and prefer useful tool fields over raw input. */
fun Interaction.permissionSummary(): PermissionSummary {
    // Android knows this card: use its own safe headline, not a compatibility summary or any raw input.
    if (isSecretHandoff) return PermissionSummary(CoreText.of(Key.HeadlineSecretHandoff))
    summary?.takeIf(String::isNotBlank)?.let { return PermissionSummary(CoreText.plain(it)) }
    val input = toolInput as? JsonObject
    fun field(key: String) = (input?.get(key) as? JsonPrimitive)?.takeIf { it.isString }
        ?.content?.takeIf(String::isNotBlank)
    val description = field("description")
    if (toolName == "Bash") {
        field("command")?.let { return PermissionSummary(CoreText.plain(it), description, monospace = true) }
        if (description != null) return PermissionSummary(CoreText.plain(description))
    }
    val item = Item(id = id, toolName = toolName, toolInput = toolInput)
    // A tool no case knows keeps the fallbacks below rather than a field picked from its input (the chat's rows pick one).
    if (toolSubject(item, pick = false).isNotBlank())
        return PermissionSummary(CoreText.of(Key.ToolWithSubject, toolVerb(toolName), toolSubjectText(item, pick = false)))
    if (description != null) return PermissionSummary(CoreText.plain(description))
    val raw = toolInput?.toString().orEmpty()
    return PermissionSummary(if (raw.isBlank()) toolVerb(toolName) else CoreText.plain(raw.take(240) + if (raw.length > 240) "…" else ""))
}
