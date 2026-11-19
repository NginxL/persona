package persona.feature.chat

import persona.core.contracts.SessionKey
import persona.core.contracts.api.MainConversationSender
import persona.core.contracts.api.PendingMainConversation
import kotlinx.coroutines.flow.filterNotNull
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.map
import kotlinx.coroutines.withTimeoutOrNull

/** How long "Hand to Persona" waits for the main conversation to be known before it stops following the message. */
private const val BIND_TIMEOUT_MILLIS = 60_000L

/**
 * "Hand to Persona" from a page with no conversation behind it (the inbox's Set Up with the assistant): queues [text] for
 * the main Persona conversation through the outbox ([operationId] keeps a retry from sending twice), calls [onQueued] (the
 * Persona tab), then [onBound] with the conversation once the outbox knows which it is, so the page can open it.
 * Returns that conversation, or null when it was not known in time.
 *
 * A message's own "Hand to Persona" is forward-to-Persona now (#3901, iOS #3807): the message menu offers Forward.
 */
suspend fun handToPersona(
    sender: MainConversationSender, text: String, operationId: String,
    onQueued: () -> Unit, onBound: (SessionKey) -> Unit,
): SessionKey? {
    if (sender.outbox.value.none { it.clientMessageId == operationId }) sender.sendToMain(text, operationId)
    onQueued()
    val target = withTimeoutOrNull(BIND_TIMEOUT_MILLIS) {
        sender.outbox.map { messages ->
            messages.firstOrNull { it.clientMessageId == operationId }?.session?.takeIf {
                it != PendingMainConversation && it.machineId.isNotBlank() && it.sessionId.isNotBlank()
            }
        }.filterNotNull().first()
    }
    target?.let(onBound)
    return target
}
