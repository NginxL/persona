package persona.core.inbox

import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonObject

/** An item's `doc` block; [reply] is where a reply goes (`reply.label`: "Comment on this row"…), [error] why the hub could not read it. */
@Serializable
data class InboxDocRef(
    val url: String,
    val type: String = "",
    val notice: String = "",
    val rows: List<InboxDocRow> = emptyList(),
    val reply: String? = null,
    val error: String? = null,
)

/** A row about the user: its title, its other fields, and its status columns (as name / value). */
@Serializable
data class InboxDocRow(
    val title: String,
    val table: String? = null,
    val fields: List<InboxDocField> = emptyList(),
    val status: List<InboxDocField> = emptyList(),
)

@Serializable
data class InboxDocField(val name: String, val value: String)

/** The hub's `doc` block; null without one or without a link to open. Unknown fields are ignored. */
fun inboxDocRef(doc: JsonObject?): InboxDocRef? {
    doc ?: return null
    val url = doc.text("url").ifBlank { doc.text("link") }.ifBlank { return null }
    fun JsonObject.list(key: String) = (get(key) as? kotlinx.serialization.json.JsonArray).orEmpty().mapNotNull { it as? JsonObject }
    fun JsonObject.pairs(key: String) = list(key).map { InboxDocField(it.text("name"), it.text("value")) }.filter { it.name.isNotBlank() }
    return InboxDocRef(
        url = url,
        type = doc.text("type"),
        notice = doc.text("notice"),
        rows = doc.list("rows").map { InboxDocRow(it.text("title"), it.cursor("table"), it.pairs("fields"), it.pairs("status")) },
        reply = (doc["reply"] as? JsonObject)?.cursor("label"),
        error = doc.cursor("error"),
    )
}
