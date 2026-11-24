package persona.core.inbox

import androidx.annotation.StringRes
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject

/** One thing an item can be muted by: a repository, a chat or a sender of its source (the hub's `mutes`). */
data class InboxMuteOption(val tag: String, val value: String, val name: String) {
    /** The name under the menu's words, on one line: a repository without its owner. */
    val shortName: String get() = if (tag == "repo") name.substringAfterLast('/') else name

    /** The menu's words for it. */
    @get:StringRes val title: Int get() = when (tag) {
        "repo" -> R.string.inbox_mute_repo
        "chat" -> R.string.inbox_mute_chat
        else -> R.string.inbox_mute_sender
    }
}

/** A mute the user set (`GET /api/v1/inbox/mutes`): nothing of [value] under [tag] in [source] comes into the inbox. */
data class InboxMute(
    val id: String,
    val source: String,
    val sourceName: String? = null,
    val tag: String,
    val value: String,
    val name: String,
    val createdAt: String? = null,
    /** Whose mute it is (several managers: each keeps its own, and "Muted" stamps it); null: not known (one manager). */
    val manager: String? = null,
) {
    /** What kind of thing it is, for people: "Repository" / "Chat" / "Sender". */
    @get:StringRes val tagName: Int get() = muteTagName(tag)
}

fun inboxMuteOptions(item: JsonObject): List<InboxMuteOption> =
    (item["mutes"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { m ->
        val tag = m.text("tag"); val value = m.text("value")
        if (tag.isBlank() || value.isBlank()) null else InboxMuteOption(tag, value, m.text("name").ifBlank { value })
    }

/** The hub's mutes, newest first. */
fun inboxMutes(body: JsonObject): List<InboxMute> =
    (body["mutes"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }.mapNotNull { m ->
        val id = m.text("id").takeIf { it.isNotBlank() } ?: return@mapNotNull null
        InboxMute(id, m.text("source"), m.cursor("source_name"), m.text("tag"), m.text("value"), m.text("name").ifBlank { m.text("value") }, m.cursor("created_at"))
    }.newestFirst()

/** The records an item's mute covers: open ones of its source with the same tag and value (iOS `InboxStore.mute`). */
fun mutedBy(records: List<InboxRecord>, record: InboxRecord, option: InboxMuteOption): Set<String> =
    records.filter { r -> r.source == record.source && !r.item.isDone && r.mutes.any { it.tag == option.tag && it.value == option.value } }
        .map { it.item.id }.toSet() + record.item.id

/** What kind of thing a mute or a rule names, for people: "Repository" / "Chat" / "Sender". */
@StringRes fun muteTagName(tag: String): Int = when (tag) {
    "repo" -> R.string.inbox_mute_tag_repo
    "chat" -> R.string.inbox_mute_tag_chat
    else -> R.string.inbox_mute_tag_sender
}

private fun List<InboxMute>.newestFirst() = sortedByDescending { it.createdAt.orEmpty() }
