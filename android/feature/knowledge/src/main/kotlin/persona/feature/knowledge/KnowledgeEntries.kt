package persona.feature.knowledge

import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import persona.core.designsystem.LocalHubFeatures
import androidx.compose.ui.platform.LocalContext
import androidx.lifecycle.viewmodel.compose.viewModel
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.api.*
import persona.core.navigation.Navigator
import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import dagger.hilt.EntryPoint
import dagger.hilt.InstallIn
import dagger.hilt.android.EntryPointAccessors
import dagger.hilt.components.SingletonComponent

@Serializable @SerialName("persona.feature.knowledge.KnowledgeRoute") data object KnowledgeRoute : NavKey
/** The knowledge list opened at one kind by a deep link; the user can still change the filter in the page. */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeKindRoute")
data class KnowledgeKindRoute(val kind: KnowledgeKind) : NavKey
/**
 * The knowledge map: topics and their points, or projects and their parts, as an outline (iOS `KBTopicsMap`, the web's Knowledge map).
 * Of [manager]'s knowledge base, the assistant current when it was opened (null from a page saved before it was kept: the one
 * current when it opens again); switching assistants while it is open leaves it that one's.
 */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeMapRoute")
data class KnowledgeMapRoute(val manager: String? = null) : NavKey
/**
 * An entry of [manager]'s knowledge base (the assistant current when it was opened; null from a page saved before it was
 * kept: the one current when it opens again). Its page stays that assistant's (docs/assistant/managers.md "Clients").
 */
@Serializable @SerialName("persona.feature.knowledge.KnowledgeDetailRoute")
data class KnowledgeDetailRoute(val kind: KnowledgeKind, val id: String, val manager: String? = null) : NavKey

@EntryPoint
@InstallIn(SingletonComponent::class)
interface KnowledgeSendingEntryPoint {
    fun mainConversationSender(): MainConversationSender
}

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
        val context = LocalContext.current.applicationContext
        val mainSender = remember(sender, context) {
            sender as? MainConversationSender ?: EntryPointAccessors.fromApplication(
                context, KnowledgeSendingEntryPoint::class.java,
            ).mainConversationSender()
        }
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
