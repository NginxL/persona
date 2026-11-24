package persona.core.inbox

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import persona.core.contracts.JsonCodec

/** How triage stands; null from an older hub, which sends none. */
@Serializable
data class InboxTriage(
    /** ok | unconfigured | paused | error | timeout | behind. */
    val state: String = "",
    /** The state for people, the hub's own words; may be empty. */
    val reason: String? = null,
    val waiting: Int = 0,
    val sources: List<InboxTriageSource> = emptyList(),
) {
    companion object {
        /** The model use "Set up" opens: "Inbox triage", the one that sorts the inbox (Settings › Models). */
        const val Purpose = "inbox.triage"

        /** The response's `triage`; anything else (missing, not an object, unreadable) is none. */
        fun decode(json: JsonElement?): InboxTriage? = (json as? JsonObject)?.let {
            runCatching { JsonCodec.decodeFromJsonElement(serializer(), it) }.getOrNull()
        }
    }
}

/** One source's share of the waiting items. */
@Serializable
data class InboxTriageSource(
    val source: String = "",
    val name: String? = null,
    val state: String = "",
    val reason: String? = null,
    val waiting: Int = 0,
    /** timeout | rate_limited | failed: the hub could not read it this round, so its items are the last round's. */
    @SerialName("read_error") val readError: String? = null,
    /** That, for people, in the hub's words. */
    @SerialName("read_reason") val readReason: String? = null,
)
