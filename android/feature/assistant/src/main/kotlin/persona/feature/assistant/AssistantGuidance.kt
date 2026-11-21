package persona.feature.assistant

import androidx.compose.foundation.layout.padding
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.ReadinessStep
import persona.core.designsystem.ReadinessStepCard
import persona.core.designsystem.Tokens

/** The model-setup guidance card already used by the main chat, with its existing action and padding. */
@Composable
internal fun AssistantModelGuidanceCard(
    step: ReadinessStep,
    machine: String?,
    onAction: (persona.core.designsystem.ReadinessActionId) -> Unit,
) {
    val guidance = if (step.code == "not_logged_in" && machine != null) step.copy(
        title = stringResource(R.string.assistant_model_setup_title),
        detail = stringResource(R.string.assistant_model_setup_detail, machine),
        altActions = emptyList(),
    ) else step
    ReadinessStepCard(guidance, onAction,
        Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.xs))
}
