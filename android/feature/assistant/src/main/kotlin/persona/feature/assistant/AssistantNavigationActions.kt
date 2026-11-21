package persona.feature.assistant

import androidx.compose.runtime.MutableState
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.SessionKey
import persona.core.contracts.api.PendingMainConversation
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.Navigator

/** The page's conversation navigation, mutating the same state objects as the original screen. */
internal class AssistantNavigationActions(
    private val newChat: MutableState<NewChatStart?>,
    private val openedMain: MutableState<Boolean>,
    private val viewingMainQueue: MutableState<Boolean>,
    private val activeMachine: MutableState<String?>,
    private val activeSession: MutableState<String?>,
    private val ended: MutableState<Set<SessionKey>>,
    private val drawerOpen: MutableState<Boolean>,
    private val scope: CoroutineScope,
) {
    fun open(key: SessionKey, hasChat: Boolean, navigator: Navigator) {
        newChat.value = null
        if (key == PendingMainConversation) {
            viewingMainQueue.value = true
            return
        }
        openedMain.value = true
        viewingMainQueue.value = false
        ended.value = ended.value - key
        activeMachine.value = key.machineId
        activeSession.value = key.sessionId
        if (!hasChat) {
            navigator.selectTab(AppTab.Persona)
            navigator.navigate(ChatRoute(key.machineId, key.sessionId))
        }
    }

    fun clear() {
        openedMain.value = true
        viewingMainQueue.value = false
        activeMachine.value = null
        activeSession.value = null
    }

    fun newConversation(sending: Boolean) {
        if (sending) return
        newChat.value = null
        clear()
        scope.launch { drawerOpen.value = false }
    }
}
