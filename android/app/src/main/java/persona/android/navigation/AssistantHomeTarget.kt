package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.contracts.SessionKey
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.InteractionRoute
import persona.core.navigation.RouteSource

/**
 * The conversation the Persona home should embed, or null when the route needs an ordinary page. An assistant
 * conversation opened from an inbox item keeps that item below it on the Persona stack instead of replacing the home.
 */
internal fun assistantHomeTarget(
    key: NavKey,
    embeddedAssistant: Boolean,
    kind: (SessionKey) -> ConversationKind?,
): SessionKey? {
    if (!embeddedAssistant) return null
    val chat = when (key) {
        is ChatRoute -> key
        is InteractionRoute -> key.chat
        else -> null
    } ?: return null
    if (chat.source is RouteSource.InboxItem) return null
    val conversation = SessionKey(chat.machineId, chat.sessionId)
    return conversation.takeIf { (chat.kind ?: kind(conversation)) == ConversationKind.Assistant }
}
