package persona.feature.assistant

import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.launch
import persona.core.contracts.api.AssistantReadiness
import persona.core.contracts.api.ReadinessStep
import persona.core.designsystem.ReadinessActionId
import persona.core.designsystem.ReadinessHelp
import persona.core.navigation.Navigator

/** The existing readiness action routing, kept outside the page's oversized Compose lambda. */
internal fun runAssistantReadinessAction(
    action: ReadinessActionId,
    launcher: String?,
    readiness: AssistantReadiness?,
    manager: String,
    scope: CoroutineScope,
    readinessController: MainReadiness,
    navigator: Navigator,
    onHelp: (ReadinessHelp) -> Unit,
) {
    action.help?.let { onHelp(it); return }
    if (action == ReadinessActionId.TestAssistant) {
        scope.launch { readinessController.refresh(probe = true) }
        return
    }
    val machine = readiness?.steps?.firstOrNull { it.id == ReadinessStep.COMPUTER }?.machine
    action.destination(launcher, machine, manager)?.let(navigator::navigate)
}
