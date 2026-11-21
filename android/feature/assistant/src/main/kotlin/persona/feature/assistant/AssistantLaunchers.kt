package persona.feature.assistant

import persona.core.contracts.api.AgentLauncher
import persona.core.contracts.api.AssistantManagement

/** Read the starter's default and available launchers for the page's current machine. */
internal suspend fun loadAssistantStarterLaunchers(
    management: AssistantManagement?,
    enabled: Boolean,
    machine: String,
    onDefault: (String?) -> Unit,
    loaded: Boolean,
    onLoading: () -> Unit,
    onSuccess: (String, List<AgentLauncher>) -> Unit,
    onFailure: (String) -> Unit,
) {
    management ?: return
    if (!enabled || machine.isBlank()) return
    assistantAttempt { management.config().launcher }.onSuccess { onDefault(it.takeIf(String::isNotBlank)) }
    if (loaded) return
    onLoading()
    assistantAttempt { management.assistantLaunchers(machine) }
        .onSuccess { onSuccess(machine, it) }
        .onFailure { onFailure(machine) }
}
