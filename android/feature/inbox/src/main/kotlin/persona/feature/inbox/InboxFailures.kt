package persona.feature.inbox

import persona.core.designsystem.UiText
import persona.core.designsystem.operationFeedback
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import java.io.IOException
import persona.core.inbox.*

/**
 * Why something did not go through, for "Couldn't draft: …", "Couldn't hand off: …": the hub's own words when it gave a reason
 * (a draft the model could not write), the usual operation feedback for the connection, the local reason otherwise;
 * in the page's language once resolved.
 */
internal fun failureReason(error: Throwable): UiText {
    if (error !is IOException) return error.message?.takeIf { it.isNotBlank() }?.let { UiText.Plain(it) } ?: operationFeedback(error).text
    val failure = HubFailure.from(error)
    // Only the hub's own refusals carry words worth showing; pairing, certificates and the network have their own.
    val hubSaid = failure.message.takeIf {
        failure.kind in setOf(HubError.Server, HubError.Protocol) && it.isNotBlank() && it != failure.kind.name && !it.startsWith("HTTP ")
    }
    return hubSaid?.let { UiText.Plain(it) } ?: operationFeedback(error).text
}
