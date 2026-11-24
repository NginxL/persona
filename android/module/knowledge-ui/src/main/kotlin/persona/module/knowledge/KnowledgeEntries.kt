package persona.module.knowledge

import persona.core.navigation.KnowledgeDetailRoute
import persona.core.navigation.KnowledgeKindRoute
import persona.core.navigation.KnowledgeMapRoute
import persona.core.navigation.KnowledgeRoute
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import persona.core.designsystem.LocalHubFeatures
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.*
import persona.core.navigation.Navigator

/**
 * The host must route links to its in-app reader, never to an external ACTION_VIEW intent. [onNewAgent] opens
 * "New session" with a project's directory and context filled in ("Start agent"); without it the page copies the context and
 * switches to Agents, where the user starts the session.
 */
fun EntryProviderScope<NavKey>.knowledgeEntries(
    navigator: Navigator,
    connection: KnowledgeConnection,
    assistant: AssistantConversations,
    sender: MessageSender,
    /** The main conversation's sender ("Ask Persona"); asked for only when an entry opens with a [sender] that is not one. */
    mainConversationSender: () -> MainConversationSender,
    sessions: SessionsRepository? = null,
    namespace: String? = null,
    openLinkInApp: (String) -> Unit,
    onNewAgent: ((KnowledgeAgentStart) -> Unit)? = null,
) {
    entry<KnowledgeRoute> {
        // The current assistant's knowledge base (docs/assistant/managers.md "Clients": the hub client names it on `kb…`):
        // a model per assistant, so switching reads the other one's and nothing of the last one's stays.
        val assistant = persona.core.designsystem.currentAssistant().id
        val model = viewModel(key = "knowledge:$assistant") { KnowledgeViewModel(connection) }
        KnowledgeScreen(model, navigator)
    }
    entry<KnowledgeKindRoute> { route ->
        val assistant = persona.core.designsystem.currentAssistant().id
        val model = viewModel(key = "knowledge:$assistant:${route.kind.wire}") {
            KnowledgeViewModel(connection, initialKind = route.kind)
        }
        KnowledgeScreen(model, navigator)
    }
    entry<KnowledgeMapRoute> { route ->
        val current = persona.core.designsystem.currentAssistant().id
        val owner = route.manager ?: androidx.compose.runtime.saveable.rememberSaveable { current }
        val model = viewModel(key = "knowledge-map:$owner") { KnowledgeMapViewModel(connection, owner = owner) }
        KnowledgeMapScreen(model, navigator, owner)
    }
    entry<KnowledgeDetailRoute> { route ->
        val mainSender = remember(sender) { sender as? MainConversationSender ?: mainConversationSender() }
        // The assistant it was opened for, kept for good: switching while it is open leaves it that one's.
        val current = persona.core.designsystem.currentAssistant().id
        val owner = route.manager ?: androidx.compose.runtime.saveable.rememberSaveable { current }
        val hierarchy = rememberUpdatedState("sessions_hierarchy" in LocalHubFeatures.current)
        val model = viewModel(key = "knowledge/$owner/${route.kind.wire}/${route.id}") {
            KnowledgeDetailViewModel(route.kind, route.id, connection, mainSender, owner, assistant, sender,
                sessions = sessions, namespace = namespace) { hierarchy.value }
        }
        KnowledgeDetailScreen(model, navigator, openLinkInApp, onNewAgent)
    }
}
