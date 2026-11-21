package persona.core.contracts

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/**
 * A task's brief (docs/assistant/unified-entry.md "Briefs"), sent on a plan's task before Start.
 * The hub owns it: PlanTaskSerializer decodes it but never writes it back with an edited Start.
 */
@Serializable
data class TaskBrief(
    val request: Request? = null,
    @SerialName("started_by") val startedBy: String? = null,
    val reason: String? = null,
    @SerialName("trigger_title") val triggerTitle: String? = null,
    @WireTime @SerialName("created_at") val createdAt: String? = null,
    val goal: String = "",
    @SerialName("done_when") val doneWhen: List<String> = emptyList(),
    @SerialName("expect_minutes") val expectMinutes: Int? = null,
    @SerialName("out_of_bounds") val outOfBounds: List<String> = emptyList(),
    val reconstructed: Boolean? = null,
    val handover: String? = null,
    /** The criteria in effect now; null means the hub sent only the original done_when. */
    val criteria: List<String>? = null,
    @SerialName("expect_minutes_now") val expectNow: Int? = null,
    val id: String = "",
    val trigger: String? = null,
    val conversation: String? = null,
) {
    /** The owner's verbatim message and where the hub read it. */
    @Serializable
    data class Request(
        val text: String,
        val truncated: Boolean = false,
        @WireTime val at: String? = null,
        val conversation: String? = null,
        @SerialName("message_id") val messageId: String? = null,
        val part: Boolean? = null,
        val via: List<String> = emptyList(),
    )

    val criteriaNow: List<String> get() = criteria ?: doneWhen
    val expect: Int get() = if ((expectNow ?: 0) > 0) expectNow ?: 0 else expectMinutes ?: 0
    val bounds: List<String> get() = outOfBounds
    val goalText: String get() = goal.trim()
}
