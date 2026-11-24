package persona.core.inbox

import persona.core.contracts.api.AssistantManagerChoice
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.RequestManager
import kotlinx.coroutines.withContext
import kotlinx.serialization.json.*
import org.jsoup.Jsoup

/**
 * Requests stay bound to the pairing that supplied the message, and to [manager] when one is set: the manager of the chat
 * or inbox item a link was opened from (docs/assistant/managers.md "Clients"), named on each request where it is made,
 * so a read that goes out after the page moved on keeps it. The default's names none; null leaves the requests as they are.
 */
class LarkContentSource(val transport: InboxAssistantTransport, val manager: String? = null) {
    /** The pairing, and the manager when it is not the default: what reads through this source are cached under. */
    val scope: String get() = named()?.let { "${transport.scope}#$it" } ?: transport.scope
    suspend fun document(url: String): LarkDocument = parseLarkDocument(read { readDocument(url) })
    fun image(ref: String, document: Boolean = false, width: Int = 1, height: Int = 1): LarkPart.Image = LarkPart.Image(
        AuthenticatedLarkImage("$scope:$ref") {
            read { if (document && ref.startsWith("/api/v1/lark/raw/documents/")) documentMedia(ref) else media(ref) }
        }, width = width, height = height)
    suspend fun media(ref: String): ByteArray = read { media(ref) }
    suspend fun avatar(ref: String): ByteArray = read { avatar(ref) }

    /** This pairing's reads as [manager]'s; null keeps this source. */
    fun forManager(manager: String?): LarkContentSource =
        if (manager == null || manager == this.manager) this else LarkContentSource(transport, manager)

    suspend fun <T> read(block: suspend InboxAssistantTransport.() -> T): T =
        if (manager == null) transport.block() else withContext(RequestManager(named().orEmpty())) { transport.block() }

    private fun named(): String? = manager?.takeIf { it.isNotBlank() && it != AssistantManagerChoice.current.value.defaultId }
}

data class LarkCardContent(val title: String, val blocks: List<LarkCardBlock>, val summary: String)

sealed interface LarkCardBlock {
    data class Text(val text: String) : LarkCardBlock
    /** The app's own line where the card has none to show (a part not shown, an unsupported element). */
    data class Placeholder(@androidx.annotation.StringRes val text: Int) : LarkCardBlock
    data class Button(
        val label: String,
        val actionId: String? = null,
        val actionMethod: Int = 1,
        val url: String? = null,
    ) : LarkCardBlock
    data class Image(val url: String, val alt: String, val width: Int = 1, val height: Int = 1) : LarkCardBlock
    data class Columns(val columns: List<List<LarkCardBlock>>, val weights: List<Float>) : LarkCardBlock
    data class Group(val children: List<LarkCardBlock>) : LarkCardBlock
    data object Divider : LarkCardBlock
}

fun readableLarkText(value: String): String = Jsoup.parse(
    value.replace(Regex("(?i)<br\\s*/?>"), "\n").replace("\n", "<br>"),
).body().wholeText().replace('\u00a0', ' ')

/** The Lark gateway's projected CardNode tree, with the raw card element shape as a fallback. */
fun parseLarkCard(meta: JsonObject, fallback: String): LarkCardContent {
    var remaining = 512
    fun plain(value: JsonElement?, depth: Int = 0): String {
        if (depth > 24) return ""
        return when (value) {
            is JsonPrimitive -> value.contentOrNull.orEmpty()
            is JsonArray -> value.joinToString("") { plain(it, depth + 1) }
            is JsonObject -> plain(value["text"] ?: value["content"] ?: value["children"], depth + 1)
            else -> ""
        }
    }
    fun objects(value: JsonElement?) = (value as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }
    fun node(n: JsonObject, depth: Int = 0): List<LarkCardBlock> {
        if (depth > 24 || --remaining < 0) return listOf(LarkCardBlock.Placeholder(R.string.inbox_lark_card_partial))
        val kids = objects(n["children"] ?: n["elements"] ?: n["actions"] ?: n["columns"])
        fun children() = kids.flatMap { node(it, depth + 1) }
        fun text() = readableLarkText(plain(n)).trim()
        return when (n.text("tag")) {
            "button" -> {
                val actionId = n.text("actionId").ifBlank { n.text("action_id") }.ifBlank { null }
                val actionMethod = n.number("actionMethod")?.toInt() ?: n.number("action_method")?.toInt() ?: 1
                val url = n.text("url").ifBlank { null }
                listOf(LarkCardBlock.Button(text(), actionId, actionMethod, url))
            }
            "img", "image" -> {
                val image = n["image"] as? JsonObject
                val url = image?.text("url").orEmpty().ifBlank { n.text("url") }
                if (url.isBlank()) listOf(text().takeIf { it.isNotBlank() }?.let { LarkCardBlock.Text(it) }
                    ?: LarkCardBlock.Placeholder(R.string.inbox_lark_image_unavailable))
                else listOf(LarkCardBlock.Image(url, plain(n["alt"]),
                    image?.number("width")?.toInt()?.coerceAtLeast(1) ?: 1,
                    image?.number("height")?.toInt()?.coerceAtLeast(1) ?: 1))
            }
            "hr" -> listOf(LarkCardBlock.Divider)
            "column_set" -> {
                val cols = kids.filter { it.text("tag") == "column" }
                val weights = cols.map {
                    val style = it["style"] as? JsonObject
                    (style?.text("weight")?.toFloatOrNull() ?: it.text("weight").toFloatOrNull() ?: 1f)
                        .takeIf { w -> w.isFinite() && w > 0f }?.coerceIn(.1f, 10f) ?: 1f
                }
                listOf(LarkCardBlock.Columns(cols.map { node(it, depth + 1) }, weights))
            }
            "div" -> {
                val out = children().toMutableList()
                if (kids.isEmpty()) text().takeIf { it.isNotBlank() }?.let { out += LarkCardBlock.Text(it) }
                var shorts = mutableListOf<List<LarkCardBlock>>()
                fun flush() { if (shorts.isNotEmpty()) { out += LarkCardBlock.Columns(shorts.toList(), List(shorts.size) { 1f }); shorts = mutableListOf() } }
                for (field in objects(n["fields"])) {
                    val content = objects(field["children"]).flatMap { node(it, depth + 1) }
                        .ifEmpty { listOf(LarkCardBlock.Text(readableLarkText(plain(field)))) }
                    if (field.flag("short") || field.flag("is_short")) {
                        shorts += content
                        if (shorts.size == 2) flush()
                    } else { flush(); out += LarkCardBlock.Group(content) }
                }
                flush()
                out
            }
            "body", "column", "action", "note", "form", "interactive_container" -> children()
            "plain_text", "markdown", "lark_md", "link", "at", "text_tag", "code_span" ->
                text().takeIf { it.isNotBlank() }?.let { listOf(LarkCardBlock.Text(it)) }.orEmpty()
            else -> text().takeIf { it.isNotBlank() }?.let { listOf(LarkCardBlock.Text(it)) }
                ?: listOf(LarkCardBlock.Placeholder(R.string.inbox_lark_content_unsupported))
        }
    }
    val title = readableLarkText(plain(meta["header"])).ifBlank { meta.text("title") }
    val body = meta["body"] as? JsonObject
    val blocks = if (body != null) node(body) else objects(meta["elements"]).flatMap { node(it) }
    val summary = (meta["fallback"] as? JsonArray).orEmpty().joinToString("\n") { plain(it) }
        .ifBlank { fallback }
    return LarkCardContent(title, blocks, readableLarkText(summary))
}

/** Shared by the current data repository and the window-based inbox adapter. */
fun larkInteractivePart(metadata: JsonObject, fallback: String, transport: InboxAssistantTransport, messageId: String? = null,
    manager: String? = null): LarkPart.Card =
    LarkPart.Card(parseLarkCard(metadata, fallback), LarkContentSource(transport, manager), messageId)
