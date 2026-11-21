package persona.core.contracts.api

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

/** Someone notified by a Feishu message; the text must still contain the literal `@name`. */
@Serializable
data class LarkMention(@SerialName("user_id") val userId: String, val name: String)

/** A group member offered by the conversation-members route. */
@Serializable
data class LarkMember(
    @SerialName("user_id") val userId: String,
    val name: String,
    val avatar: String? = null,
)
