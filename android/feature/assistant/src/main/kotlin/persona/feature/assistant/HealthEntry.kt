package persona.feature.assistant

import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSpace

/**
 * Where the sidebar's Health entry and a health chat's New go (#4329, the same rule on every client;
 * docs/assistant/health.md): the newest health chat when there is one, else the health space's new-chat page.
 */
internal object HealthEntry {
    sealed interface Target {
        /** Open this health chat in the assistant's chat, as a past conversation in the sidebar opens. */
        data class Open(val key: SessionKey) : Target
        /** Open the health space's new-chat page: its first message starts a health chat. */
        object New : Target
    }

    /** The health chats to list, newest first (`updated_at`; ties by session id), archived ones left out. */
    fun history(conversations: List<Session>): List<Session> = conversations
        .filter { !it.archived }
        .distinctBy { it.key }
        .sortedWith(compareByDescending<Session> { it.updated }.thenBy { it.sessionId })

    /** Where the Health entry goes for the space's [conversations]. */
    fun resolve(conversations: List<Session>): Target =
        history(conversations).firstOrNull()?.let { Target.Open(it.key) } ?: Target.New

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
