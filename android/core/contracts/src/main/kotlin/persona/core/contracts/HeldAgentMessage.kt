package persona.core.contracts

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** An owner's message the manager kept. A computer identifies an Agent queue; error is its localized delivery status. */
@Serializable
data class HeldAgentMessage(
    val id: String,
    val text: String = "",
    val images: Int = 0,
    @WireTime val at: String = "",
    val state: String = "waiting",
    /** Assistant-kept delivery metadata; absent for an offline Agent queue. */
    val attempts: Int? = null,
    @WireTime @SerialName("next_at") val nextAt: String? = null,
    val error: String? = null,
    @SerialName("delete_only") val deleteOnly: Boolean = false,
    val computer: String? = null,
) {
    val gaveUp: Boolean get() = state == "failed"
}

/** The same row as seen by an assistant conversation (#10214). */
typealias UndeliveredMessage = HeldAgentMessage
