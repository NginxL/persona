package persona.feature.inbox

import android.content.res.Resources
import persona.core.contracts.api.InboxDoneTarget
import persona.core.domain.inbox.InboxGroup
import persona.core.domain.inbox.InboxRowType
import persona.core.inbox.*

/**
 * The open hub items of what the page shows, each with the version it was listed at: Clean up all over the current
 * filter, section and search. Agents waiting for an answer are not the inbox's to file, and folded sections count (their header
 * says how many they hold).
 */
internal fun bulkTargets(groups: List<InboxGroup>, records: Map<String, InboxRecord>): List<InboxDoneTarget> =
    groups.filter { it.id != "done" }.flatMap { it.rows }
        .filter { it.type == InboxRowType.Item }
        .mapNotNull { row -> records[row.id]?.item?.takeIf { !it.isDone } }
        .distinctBy { it.id }
        .map { InboxDoneTarget(it.id, it.version) }

/** The current view in words, for saying what Clean up all covers; null for the whole inbox. */
internal fun bulkScope(res: Resources, section: String, sourceLabel: String?, kindLabel: String?, query: String): String? =
    listOfNotNull(
        when (section) {
            "action" -> res.getString(R.string.inbox_bay_needs_you); "fyi" -> res.getString(R.string.inbox_group_fyi)
            "noise" -> res.getString(R.string.inbox_section_folded); else -> null
        },
        sourceLabel?.takeIf { it.isNotBlank() },
        kindLabel?.takeIf { it.isNotBlank() },
        query.trim().takeIf { it.isNotEmpty() }?.let { res.getString(R.string.inbox_bulk_search, it) },
    ).joinToString(" · ").ifEmpty { null }

/** From this many items on, Clean up all asks first ("Will clean up N items"); fewer go at once, with Undo. */
internal const val BULK_CONFIRM_AT = 10

/** The menu entry Clean up all: the count, and whether it is the whole inbox or the filter. */
internal fun bulkLabel(res: Resources, count: Int, scope: String?): String =
    res.getQuantityString(if (scope == null) R.plurals.inbox_bulk_clean_all else R.plurals.inbox_bulk_clean_filtered, count, count)

/** The confirmation's text: how many, over what, and that what comes in later stays. */
internal fun bulkConfirmText(res: Resources, count: Int, scope: String?): String =
    res.getQuantityString(R.plurals.inbox_bulk_confirm_body, count, count, scope ?: res.getString(R.string.inbox_bulk_scope_all))

/**
 * What the notice says after a bulk Mark all as seen / Clear all (or Clean up all when [cleaned]); null when nothing moved and nothing
 * needs saying.
 */
internal fun bulkNotice(res: Resources, result: InboxBulkResult, cleaned: Boolean = false): String? {
    val newer = result.changedSince.size
    val moved = result.changed.size
    return when {
        result.changed.isEmpty() && newer > 0 -> res.getQuantityString(R.plurals.inbox_bulk_newer, newer, newer)
        result.changed.isEmpty() -> null
        cleaned && newer > 0 -> res.getQuantityString(R.plurals.inbox_bulk_cleaned_newer, moved, moved, newer)
        cleaned -> res.getQuantityString(R.plurals.inbox_bulk_cleaned, moved, moved)
        newer > 0 -> res.getQuantityString(R.plurals.inbox_bulk_marked_newer, moved, moved, newer)
        else -> res.getQuantityString(R.plurals.inbox_bulk_marked, moved, moved)
    }
}
