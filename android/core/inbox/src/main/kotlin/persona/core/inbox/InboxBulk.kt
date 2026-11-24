package persona.core.inbox

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonArray

/**
 * What a bulk Clean up all / Mark all as seen / Clear all / Move back (`POST /api/v1/inbox/bulk`) did: [changed] moved (Undo sends these back), [unchanged] were already
 * so, [changedSince] took new messages after the list was drawn and stay open, [missing] are gone from the hub, [failed]
 * the hub could not write and stay as they were.
 */
data class InboxBulkResult(
    val changed: List<String> = emptyList(),
    val unchanged: List<String> = emptyList(),
    val changedSince: List<String> = emptyList(),
    val missing: List<String> = emptyList(),
    val failed: List<String> = emptyList(),
) {
    companion object {
        fun from(data: JsonObject): InboxBulkResult {
            fun ids(key: String) = data[key]?.jsonArray.orEmpty().mapNotNull { element ->
                (element as? JsonPrimitive)?.contentOrNull ?: (element as? JsonObject)?.get("id")?.let { (it as? JsonPrimitive)?.contentOrNull }
            }
            val skipped = data["skipped"]?.jsonArray.orEmpty().mapNotNull { it as? JsonObject }
            fun skippedFor(reason: String) = skipped.filter { (it["reason"] as? JsonPrimitive)?.contentOrNull == reason }
                .mapNotNull { (it["id"] as? JsonPrimitive)?.contentOrNull }
            return InboxBulkResult(ids("changed"), ids("unchanged"), skippedFor("changed"), skippedFor("missing"), ids("failed"))
        }
    }
}
