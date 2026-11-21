package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import androidx.navigation3.scene.Scene
import kotlinx.serialization.Serializable
import persona.core.navigation.*

/** The existing Scheduled Tasks screen can be picked as a root from the merged sidebar. */
@Serializable
internal data object SidebarScheduleRoute : NavKey

/** Keep the logical tab anchors. The first Agent detail is the visible root unless it came from another page. */
internal fun mergedVisibleRoot(
    tab: AppTab,
    stack: List<NavKey>,
    compactPlace: (NavKey) -> Boolean = { false },
    agentDrillDown: (NavKey) -> Boolean = { false },
): NavKey = when (tab) {
    AppTab.Persona -> stack.getOrNull(1)?.takeIf { stack.firstOrNull() == AssistantRoute && compactPlace(it) }
        ?: stack.firstOrNull() ?: AssistantRoute
    AppTab.Agents -> if (stack.drop(1).any(agentDrillDown)) AgentsRoute else stack.getOrNull(1) ?: AgentsRoute
}

internal fun isMergedAgentPick(key: NavKey): Boolean = when (key) {
    is ChatRoute -> key.kind != ConversationKind.Assistant
    is SpaceRoute, is MachineSpacesRoute, is MachineDetailRoute, TerminalPanesRoute -> true
    else -> false
}

internal fun mergedAgentRootSwapped(from: Scene<NavKey>, to: Scene<NavKey>): Boolean {
    fun root(scene: Scene<NavKey>): Any? =
        (scene.previousEntries + scene.entries).distinctBy { it.contentKey }.getOrNull(1)?.contentKey
    return root(from) != root(to)
}

/** External opens use the router first, including the chat below a terminal, then reveal that target as the root. */
internal fun keepMergedAgentTarget(stack: MutableList<NavKey>, requested: NavKey) {
    val visible = stack.lastOrNull() ?: return
    when (requested) {
        is ChatRoute, is InteractionRoute -> replaceBackStack(stack, listOf(AgentsRoute, visible))
        is TerminalRoute -> replaceBackStack(stack, listOf(AgentsRoute) + stack.takeLast(2).filterNot { it == AgentsRoute })
    }
}
