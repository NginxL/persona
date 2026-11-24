package persona.feature.settings

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.AssistantReadiness
import persona.core.contracts.api.ReadinessStep
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCard
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.ReadinessActionId
import persona.core.designsystem.Status
import persona.core.designsystem.StatusLabel
import persona.core.designsystem.Tokens
import persona.core.designsystem.primaryAction
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AddComputerRoute
import persona.core.navigation.ModelProviderRoute
import persona.core.navigation.ModelPurposeRoute
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute

/** The four steps the checklist shows for the main conversation (docs/assistant/onboarding.md 4.5), in the hub's order. */
internal val MainStepIds = listOf(ReadinessStep.COMPUTER, ReadinessStep.CLAUDE, ReadinessStep.MODEL, ReadinessStep.FIRST_MESSAGE)

@StringRes
internal fun mainStepName(id: String): Int = when (id) {
    ReadinessStep.COMPUTER -> R.string.settings_ready_step_computer
    ReadinessStep.CLAUDE -> R.string.settings_ready_step_claude
    ReadinessStep.MODEL -> R.string.settings_ready_step_model
    else -> R.string.settings_ready_step_first_message
}

internal fun mainStepStatus(state: String): Status = when (state) {
    ReadinessStep.OK -> Status.Unread
    ReadinessStep.TODO, ReadinessStep.PROBLEM -> Status.NeedsYou
    ReadinessStep.CHECKING -> Status.Working
    ReadinessStep.SKIPPED -> Status.Idle
    else -> Status.Unknown
}

@StringRes
internal fun mainStepStateLabel(state: String): Int = when (state) {
    ReadinessStep.OK -> R.string.settings_ready_satisfied
    ReadinessStep.TODO, ReadinessStep.PROBLEM -> R.string.settings_ready_needs_setup
    ReadinessStep.CHECKING -> R.string.settings_ready_state_checking
    ReadinessStep.SKIPPED -> R.string.settings_ready_state_skipped
    else -> R.string.settings_ready_state_unknown
}

/** Where a step's button goes from Settings; null for help shown in place or "Try it", which runs here. */
internal fun ReadinessActionId.settingsDestination(launcher: String?, machine: String? = null): NavKey? = when (this) {
    ReadinessActionId.AddComputer -> AddComputerRoute()
    ReadinessActionId.ConfirmComputer -> AddComputerRoute(confirm = machine.orEmpty())
    ReadinessActionId.ConnectModel -> ModelPurposeRoute("assistant")
    ReadinessActionId.PickModel -> SettingsPageRoute(SettingsPage.ModelConfig)
    ReadinessActionId.EditProvider -> launcher?.takeIf(String::isNotBlank)?.let(::ModelProviderRoute) ?: SettingsPageRoute(SettingsPage.Providers)
    else -> null
}

/**
 * The main conversation's steps: each with its state, the hub's words when something is missing and its buttons;
 * [allowed] drops the buttons this page cannot follow (no navigation during the first checklist).
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun MainConversationSteps(
    readiness: AssistantReadiness,
    probing: Boolean,
    onAction: (ReadinessActionId, ReadinessStep) -> Unit,
    allowed: (ReadinessActionId) -> Boolean = { true },
) {
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        SettingsCaption(stringResource(R.string.settings_ready_main_about))
        MainStepIds.forEach { id ->
            val step = readiness.step(id) ?: return@forEach
            // "Try it" not tried yet never blocks anything (5.1): a neutral "not tried", not "Needs setup".
            val untried = id == ReadinessStep.FIRST_MESSAGE && step.state == ReadinessStep.TODO
            val status = if (untried) Status.Unknown else mainStepStatus(step.state)
            val missing = step.state == ReadinessStep.TODO || step.state == ReadinessStep.PROBLEM
            val primary = step.primaryAction()?.takeIf(allowed)
            val others = step.altActions.mapNotNull { ReadinessActionId.of(it.id) }.filter { it != primary && allowed(it) }
            JCard(status = status) {
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                    val name = stringResource(mainStepName(id))
                    Text(name, style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
                    StatusLabel(status, text = stringResource(if (untried) R.string.settings_ready_state_not_tried else mainStepStateLabel(step.state)))
                    // What is missing, in the hub's words; what is there, in a few words of the app's.
                    if (missing || step.state == ReadinessStep.UNKNOWN) {
                        step.title?.takeIf { it.isNotBlank() && it != name && !untried }?.let {
                            Text(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                        }
                        step.detail?.takeIf(String::isNotBlank)?.let { SettingsCaption(it) }
                    } else okDetail(readiness, step)?.let { SettingsCaption(it) }
                    if (id == ReadinessStep.FIRST_MESSAGE) readiness.probe?.let { probe ->
                        SettingsCaption(if (probe.ok) stringResource(persona.core.designsystem.R.string.designsystem_readiness_probe_ok, probe.answer.orEmpty())
                            else stringResource(persona.core.designsystem.R.string.designsystem_readiness_probe_failed, probe.error.orEmpty()))
                    }
                    if (primary != null || others.isNotEmpty()) FlowRow(Modifier.padding(top = Tokens.Space.xs),
                        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        primary?.let { action ->
                            JButton(stringResource(action.label), { onAction(action, step) }, size = JButtonSize.Small,
                                loading = probing && action == ReadinessActionId.TestAssistant, enabled = !probing)
                        }
                        others.forEach { action ->
                            JButton(stringResource(action.label), { onAction(action, step) }, kind = JButtonKind.Secondary, size = JButtonSize.Small,
                                loading = probing && action == ReadinessActionId.TestAssistant, enabled = !probing)
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun okDetail(readiness: AssistantReadiness, step: ReadinessStep): String? = when (step.id) {
    ReadinessStep.COMPUTER -> readiness.machine?.name?.takeIf(String::isNotBlank)?.let { stringResource(R.string.settings_ready_step_computer_ok, it) }
    ReadinessStep.CLAUDE -> step.version?.takeIf(String::isNotBlank)?.let { stringResource(R.string.settings_ready_step_claude_ok, it) }
    ReadinessStep.MODEL -> if (step.state == ReadinessStep.OK) stringResource(R.string.settings_ready_step_model_ok) else null
    ReadinessStep.FIRST_MESSAGE -> if (step.state == ReadinessStep.OK) stringResource(R.string.settings_ready_step_first_message_ok) else null
    else -> null
}
