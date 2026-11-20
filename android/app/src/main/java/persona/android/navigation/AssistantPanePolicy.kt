package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AddComputerRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.EngineRoute
import persona.core.navigation.ExtensionUiRoute
import persona.core.navigation.LauncherRoute
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MachinesRoute
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.TodosRoute
import persona.feature.knowledge.KnowledgeDetailRoute
import persona.feature.knowledge.KnowledgeKindRoute
import persona.feature.knowledge.KnowledgeRoute

/** How the Assistant stack shares the window, without measuring a device or owning another back stack. */
internal data class AssistantPanePolicy(
    val keepSharedSidebar: Boolean,
    val singleRightStack: Boolean,
    val keepWidePhoneListDetail: Boolean,
)

/**
 * In regular windows the shared sidebar stays beside the Assistant root and its sidebar destinations. Their lists and
 * details use one right-hand stack. A wide phone keeps its existing Inbox list-detail instead; Agents never inherit
 * either Assistant rule.
 */
internal fun assistantPanePolicy(
    tab: AppTab,
    destinations: List<NavKey>,
    regular: Boolean,
    widePhone: Boolean,
): AssistantPanePolicy {
    val secondary = destinations.drop(1)
    val assistantRoot = tab == AppTab.Persona && destinations.singleOrNull() == AssistantRoute
    val assistantSecondary = tab == AppTab.Persona && secondary.isNotEmpty() &&
        secondary.all(::isAssistantSecondaryDestination)
    // A place that replaced the conversation as the stack's root still keeps the column, in one pane (#9667).
    // A drill on it is already [assistantSecondary] via drop(1).
    val placeRoot = tab == AppTab.Persona && destinations.singleOrNull()?.let {
        it != AssistantRoute && isAssistantSecondaryDestination(it)
    } == true
    val inboxSecondary = tab == AppTab.Persona && secondary.isNotEmpty() && secondary.all(::isInboxDestination)
    return AssistantPanePolicy(
        keepSharedSidebar = regular && (assistantRoot || assistantSecondary || placeRoot),
        singleRightStack = regular && (assistantSecondary || placeRoot),
        keepWidePhoneListDetail = !regular && widePhone && inboxSecondary,
    )
}

private fun isAssistantSecondaryDestination(key: NavKey): Boolean = when (key) {
    MachinesRoute, TodosRoute, KnowledgeRoute -> true
    is MachineDetailRoute, is AddComputerRoute, is EngineRoute, is LauncherRoute,
    is TodoDetailRoute, is KnowledgeDetailRoute, is KnowledgeKindRoute, is ExtensionUiRoute -> true
    else -> isInboxDestination(key)
}
