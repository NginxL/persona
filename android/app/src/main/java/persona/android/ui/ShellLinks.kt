package persona.android.ui

import persona.android.R
import androidx.compose.ui.res.stringResource
import androidx.compose.runtime.*
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.contracts.GroupKeys
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantSnapshot
import persona.core.contracts.api.MainConversationSender
import persona.core.designsystem.AppFeedback
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.HubLinks
import persona.core.navigation.HubPage
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MessageJumps
import persona.core.navigation.Navigator
import persona.feature.chat.*
import persona.feature.assistant.personaConversationTitle
import persona.feature.inbox.InboxWebSelection
import persona.feature.inbox.LocalInboxWebSelection
import java.util.UUID

/**
 * Links for the whole shell: the link sheet over pages outside a conversation (the knowledge base, the Persona home's
 * own links), `persona://` links tapped in any conversation (they open a conversation or a tab, nothing else), links to
 * the hub's own web pages (the assistant's `/s/…`, `/persona/…`, `/todos?id=…`: the same screens in the app), and
 * text picked on a page with no conversation behind it (those pages, and the inbox's): "Hand to Persona" puts it in Persona's
 * main conversation and shows the Persona tab, as the inbox's own "Hand to Persona" does.
 */
@Composable
internal fun ShellLinkHost(session: ShellSession, navigator: Navigator, feedback: AppFeedback, content: @Composable () -> Unit) {
    // The paired sender is the connection's forwarding wrapper, not the outbox itself, so it is no
    // MainConversationSender: the main conversation comes from the pairing's chat wiring, as in a chat (ShellChat).
    // Before, "Hand to Persona" never showed on these pages (only demo mode's sender is one).
    val activeWiring by chatWiringSource().current.collectAsStateWithLifecycle()
    val main = session.sender as? MainConversationSender
        ?: activeWiring?.takeIf { it.scope.path == session.identity }?.mainSender
    val handToPersona: (suspend (String) -> Unit)? = remember(main, navigator) {
        main?.let { sender ->
            val send: suspend (String) -> Unit = { message ->
                sender.sendToMain(message, UUID.randomUUID().toString())
                navigator.selectTab(AppTab.Persona)
            }
            send
        }
    }
    val appLinks: (ChatAppLink) -> Unit = remember(navigator) {
        { link: ChatAppLink ->
            when (link) {
                is ChatAppLink.Session -> navigator.navigate(ChatRoute(link.machineId, link.sessionId))
                is ChatAppLink.Tab -> when (link.tab) {
                    "inbox" -> navigator.navigate(persona.core.navigation.InboxRoute)
                    "settings" -> navigator.navigate(persona.core.navigation.SettingsRoute)
                    "agents" -> navigator.selectTab(AppTab.Agents)
                    else -> navigator.selectTab(AppTab.Persona)
                }
            }
        }
    }
    val hubLinks: (String) -> Boolean = remember(session, navigator) {
        { link: String ->
            openHubPage(link, session.hubAddresses(), session.groups.groups.value.firstOrNull { it.selected }?.namespace,
                session.sessions.sessions.value.map { it.key }, session.assistant.assistant.value, navigator)
        }
    }
    val forwarding = rememberChatForwarding(session, navigator)
    var picked by remember { mutableStateOf<ChatLinkContext?>(null) }
    val inboxPick: (InboxWebSelection) -> Unit = { selection -> picked = ChatLinkContext("context", selection.title, selection.url, selection.text) }
    // Without a main conversation to send to, a pick can only say where it works.
    val contextNotice = stringResource(R.string.app_link_context_elsewhere)
    val editNotice = stringResource(R.string.app_link_edit_elsewhere)
    val contextElsewhere: (ChatLinkContext) -> Unit = { feedback.notice(contextNotice) }
    val editElsewhere: suspend (ChatLinkContext, String) -> Unit = { _, _ -> feedback.notice(editNotice) }
    CompositionLocalProvider(LocalChatAppLinks provides appLinks, LocalChatHubLinks provides hubLinks,
        LocalInboxWebSelection provides inboxPick.takeIf { handToPersona != null }, LocalChatForwarding provides forwarding) {
        ChatLinkHost(
            onContext = contextElsewhere.takeIf { handToPersona == null },
            onEdit = editElsewhere.takeIf { handToPersona == null },
            onHandToPersona = handToPersona,
        ) { _ ->
            val uri = androidx.compose.ui.platform.LocalUriHandler.current
            val pending = persona.android.MainActivity.pendingCapsuleUrl.value
            LaunchedEffect(pending) {
                pending?.let { url ->
                    uri.openUri(url)
                    if (persona.android.MainActivity.pendingCapsuleUrl.value == url) persona.android.MainActivity.pendingCapsuleUrl.value = null
                }
            }
            content()
        }
    }
    picked?.let { selection ->
        HandToPersonaDialog(selection, onDismiss = { picked = null }) { message ->
            handToPersona?.invoke(message)
            picked = null
        }
    }
}

/**
 * Opens the app's screen for [link] when it is a page of the selected group's hubs ([HubLinks]; [hubs] their
 * addresses, [namespace] the group's machine id suffix); false for other sites, the hub's pages the app has no
 * screen for, and an assistant conversation before the assistant is known. A link to one message of a conversation
 * (`?at=`) leaves it in [MessageJumps] for that chat's page, which lands on it.
 */
internal fun openHubPage(link: String, hubs: List<String>, namespace: String?, sessions: List<SessionKey>,
    assistant: AssistantSnapshot, navigator: Navigator): Boolean {
    val page = HubLinks.page(link, hubs) ?: return false
    // The hub speaks its own machine ids; the merged lists carry the group's suffix (GroupKeys).
    fun machine(id: String) = GroupKeys.machine(id, namespace)
    when (page) {
        is HubPage.Agent -> {
            val machineId = machine(page.machineId)
            // The web page also takes the first characters of a session id (#2464): the one session they name.
            val sessionId = sessions.filter { it.machineId == machineId && it.sessionId.startsWith(page.sessionId) }
                .let { keys -> keys.firstOrNull { it.sessionId == page.sessionId } ?: keys.singleOrNull() }?.sessionId ?: page.sessionId
            page.itemId?.let { MessageJumps.request(machineId, sessionId, it) }
            navigator.navigate(ChatRoute(machineId, sessionId))
        }
        is HubPage.Conversation -> {
            val main = assistant.mainSession
            val key = when (val id = page.sessionId) {
                null -> main
                // The assistant's conversations all run on its machine: one not listed yet is there too.
                else -> assistant.conversations.map { it.key }.firstOrNull { it.sessionId == id }
                    ?: main?.let { SessionKey(it.machineId, id) } ?: return false
            }
            if (key == null) navigator.selectTab(AppTab.Persona)
            else {
                page.itemId?.let { MessageJumps.request(key.machineId, key.sessionId, it) }
                navigator.navigate(ChatRoute(key.machineId, key.sessionId, ConversationKind.Assistant))
            }
        }
        is HubPage.Screen -> navigator.navigate(page.key.let { if (it is MachineDetailRoute) it.copy(machineId = machine(it.machineId)) else it })
    }
    return true
}

/**
 * What chats need for Forward (#3901): the sessions and Persona's conversations the app already holds, each group's Recent,
 * and how a target opens: an Agent's chat in Agents, a Persona conversation in Persona's page, Persona's new conversation
 * with its composer focused, or the new-agent page. Forwards stay on their hub group (Persona's page shows the selected one).
 */
@Composable
private fun rememberChatForwarding(session: ShellSession, navigator: Navigator): ChatForwarding {
    val resources = androidx.compose.ui.platform.LocalResources.current
    val wiring = chatWiringSource()
    return remember(session, navigator, resources, wiring) {
        ChatForwarding(
            agents = session.sessions.sessions, machines = session.sessions.machines, assistant = session.assistant.assistant,
            personaGroupOf = { key ->
                GroupKeys.namespaceOf(key.machineId) == session.groups.groups.value.firstOrNull { it.selected }?.namespace
            },
            conversationTitle = { it.personaConversationTitle(resources) },
            memory = { key -> wiring.bindingsFor(key).value?.forwardMemory },
            openChat = { key, assistant ->
                if (assistant) {
                    navigator.selectTab(AppTab.Persona)
                    navigator.navigate(ChatRoute(key.machineId, key.sessionId, ConversationKind.Assistant))
                } else navigator.navigate(ChatRoute(key.machineId, key.sessionId, ConversationKind.Agent))
            },
            openPersonaHome = {
                persona.feature.assistant.AssistantComposerFocus.request()
                navigator.selectTab(AppTab.Persona)
            },
            openNewAgent = { context -> navigator.navigate(persona.core.navigation.NewAgentRoute(context = context)) },
        )
    }
}
