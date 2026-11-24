package persona.android.navigation

import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.navigation3.runtime.NavKey
import persona.core.contracts.DevProbe
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.InteractionRoute
import persona.core.navigation.KnowledgeDetailRoute
import persona.core.navigation.KnowledgeRoute
import persona.core.navigation.PairingRoute
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.SettingsRoute

/** Fixed page names only: route arguments never enter a diagnostics record. */
internal fun devScreenName(tab: AppTab, key: NavKey?): String? = when (key) {
    AssistantRoute, AgentsRoute, null -> null
    is ChatRoute -> if (key.kind == ConversationKind.Assistant || (key.kind == null && tab == AppTab.Persona)) "assistant-chat" else "chat"
    is InteractionRoute -> devScreenName(tab, key.chat)
    SettingsRoute, is SettingsPageRoute, PairingRoute -> "settings"
    InboxRoute, is InboxItemRoute -> "inbox"
    KnowledgeRoute, is KnowledgeDetailRoute -> "knowledge"
    else -> "screen"
}

/** Tracks the visible tab and its top overlay, including the independently hosted Settings sheet. */
@Composable
internal fun TrackDevScreen(tab: AppTab, route: NavKey?, settingsOpen: Boolean, settingsRoute: NavKey?) {
    val tabName = if (tab == AppTab.Persona) "assistant" else "agents"
    val screen = if (settingsOpen) devScreenName(tab, settingsRoute) ?: "settings" else devScreenName(tab, route)
    DisposableEffect(tabName, screen) {
        DevProbe.tab(tabName)
        val overlay = screen?.let(DevProbe::enter)
        onDispose { overlay?.let(DevProbe::leave) }
    }
}
