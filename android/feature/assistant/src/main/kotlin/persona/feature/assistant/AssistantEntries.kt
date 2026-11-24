package persona.feature.assistant

import androidx.compose.runtime.Composable
import androidx.navigation3.runtime.EntryProviderScope
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AssistantRoute
import persona.core.navigation.AssistantSettingsRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.StarredMessagesRoute
import persona.core.navigation.UsageCalendarRoute
import persona.feature.assistant.settings.AssistantSettingsScreen

fun EntryProviderScope<NavKey>.assistantEntries(
    navigator: Navigator,
    content: @Composable () -> Unit = { ConnectedAssistant(navigator) },
) {
    entry<AssistantRoute> { AssistantAppHost(navigator) { content() } }
    entry<StarredMessagesRoute> { AssistantAppHost(navigator) { StarredMessagesDestination(navigator) } }
    entry<UsageCalendarRoute> { AssistantAppHost(navigator) { UsageCalendarDestination(navigator) } }
    // Settings › AI › Assistant: the same page the Persona tab opens in place, as a page of its own.
    entry<AssistantSettingsRoute> { AssistantAppHost(navigator) { AssistantSettingsPage(navigator) } }
}

@Composable
private fun AssistantSettingsPage(navigator: Navigator) {
    val host = LocalAssistantHost.current
    val management = host?.management
    if (management == null) {
        persona.core.designsystem.JListPage(title = androidx.compose.ui.res.stringResource(R.string.assistant_settings_title), onBack = navigator::back) {
            item { persona.core.designsystem.JEmptyState(androidx.compose.ui.res.stringResource(R.string.assistant_settings_unavailable),
                androidx.compose.ui.res.stringResource(R.string.assistant_settings_unavailable_detail)) }
        }
        return
    }
    AssistantSettingsScreen(management, host.machines, onBack = navigator::back, conversations = host.conversations,
        onOpenConversation = { navigator.navigate(ChatRoute(it.machineId, it.sessionId, ConversationKind.Assistant)) },
        onModelConfiguration = { purpose, owner -> navigator.navigate(persona.core.navigation.ModelPurposeRoute(purpose, owner)) },
        onStandingAuthorizations = { navigator.navigate(SettingsPageRoute(SettingsPage.StandingAuthorizations)) })
}
