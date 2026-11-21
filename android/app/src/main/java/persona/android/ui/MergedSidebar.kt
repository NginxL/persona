package persona.android.ui

import androidx.compose.runtime.*
import androidx.lifecycle.ViewModelStoreOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.navigation3.runtime.NavKey
import kotlinx.coroutines.flow.Flow
import persona.core.contracts.GroupKeys
import persona.core.contracts.Session
import persona.core.contracts.api.CurrentAssistant
import persona.core.domain.sort.NeedsYouRow
import persona.core.domain.sort.isBotmux
import persona.core.navigation.LocalDetailKey
import persona.core.navigation.Navigator
import persona.feature.agents.AgentsDependencies
import persona.feature.agents.AgentsListHost
import persona.feature.agents.AgentsPreferencesSnapshot
import persona.feature.agents.AgentsSidebarLayout
import persona.feature.assistant.MergedSidebarContent

@Composable
internal fun mergedSidebar(
    session: ShellSession,
    sessions: List<Session>,
    assistant: CurrentAssistant,
    preferences: AgentsPreferencesSnapshot,
    waiting: List<NeedsYouRow>,
    agentRoot: NavKey?,
    agentsInFront: Boolean,
    agents: AgentsDependencies,
    navigator: Navigator,
    machine: String,
    onMachineSelected: (String) -> Unit,
    terminalsAvailable: Flow<Boolean>,
    listOwner: ViewModelStoreOwner,
    usage: @Composable () -> Unit,
): MergedSidebarContent {
    var searchPick by remember { mutableStateOf<Session?>(null) }
    val knownAgents = remember(sessions, assistant, preferences.hiddenBotmuxHubs) {
        sessions.filter {
            !it.assistant && assistant.ownsOn(it.machineId, it.manager) &&
                (!it.isBotmux || GroupKeys.namespaceOf(it.machineId) !in preferences.hiddenBotmuxHubs)
        }
    }
    val machines by session.sessions.machines.collectAsStateWithLifecycle()
    val machineNames = remember(machines) { machines.associate { it.id to it.displayName } }
    return MergedSidebarContent(
        list = { listState, prefix, footer, search ->
            CompositionLocalProvider(LocalDetailKey provides agentRoot) {
                AgentsListHost(
                    navigator, agents, machine, onMachineSelected, terminalsAvailable, listOwner,
                    sidebar = AgentsSidebarLayout(prefix, footer, waiting, search, searchPick) { searchPick = null },
                    providedListState = listState,
                )
            }
        },
        usage = usage,
        agents = knownAgents,
        agentMachines = machineNames,
        pinnedAgents = remember(preferences.pins.sessions) { preferences.pins.sessions.toSet() },
        onSearchAgent = { key -> searchPick = knownAgents.firstOrNull { it.key == key } },
        agentsInFront = agentsInFront,
        showKnowledge = session.knowledge != null,
    )
}
