package persona.feature.assistant

import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSpace

/**
 * The Health home's history and new-chat helpers (#13273, docs/assistant/health.md).
 * Both the sidebar's Health entry and a health chat's New open the home; its first message starts a health chat.
 */
internal object HealthEntry {
    /** The health chats to list, newest first (`updated_at`; ties by session id), archived ones left out. */
    fun history(conversations: List<Session>): List<Session> = conversations
        .filter { !it.archived }
        .distinctBy { it.key }
        .sortedWith(compareByDescending<Session> { it.updated }.thenBy { it.sessionId })

    /**
     * New chat's blank page, opened on [from]: the health space's when New was tapped on a health chat or on the health
     * space's own new-chat page ([health]), else the assistant's.
     */
    fun newChat(from: SessionKey?, health: Boolean): NewChatStart =
        NewChatStart(from, space = AssistantSpace.HEALTH.takeIf { health })

    /** Start a blank health or assistant chat, keeping the page it was opened from. */
    fun start(from: SessionKey?, health: Boolean, clear: () -> Unit, show: (NewChatStart) -> Unit) {
        clear()
        show(newChat(from, health))
    }
}
