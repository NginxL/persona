package persona.feature.assistant

import persona.core.contracts.SessionKey
import persona.core.contracts.api.PendingMainConversation
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator

/** Open a conversation using the page's existing pending-state updates and optional shell navigation. */
internal fun openAssistantConversation(
    key: SessionKey,
    hasChat: Boolean,
    navigator: Navigator,
    newChat: (NewChatStart?) -> Unit,
    viewingMainQueue: (Boolean) -> Unit,
    openedMain: (Boolean) -> Unit,
    removeEnded: (SessionKey) -> Unit,
    activeMachine: (String) -> Unit,
    activeSession: (String) -> Unit,
) {
    newChat(null)
    if (key == PendingMainConversation) {
        viewingMainQueue(true)
        return
    }
    openedMain(true)
    viewingMainQueue(false)
    removeEnded(key)
    activeMachine(key.machineId)
    activeSession(key.sessionId)
    if (!hasChat) {
        navigator.selectTab(AppTab.Persona)
        navigator.navigate(ChatRoute(key.machineId, key.sessionId))
    }
}
