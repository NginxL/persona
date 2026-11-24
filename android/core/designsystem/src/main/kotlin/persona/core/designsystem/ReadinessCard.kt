package persona.core.designsystem

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import persona.core.contracts.api.ReadinessStep
import persona.core.domain.FailureAction

/**
 * The buttons a readiness step or a coded refusal can have on Android (docs/assistant/onboarding.md 5.1 `action.id`). An id the
 * app does not know is text only (docs/assistant/onboarding.md 5 "General rules"); `enable_tls` / `open_admin` have no page on the phone.
 */
enum class ReadinessActionId(val wire: String, @StringRes val label: Int) {
    AddComputer("add_computer", R.string.designsystem_readiness_action_add_computer),
    ConfirmComputer("confirm_computer", R.string.designsystem_readiness_action_confirm_computer),
    WakeComputer("wake_computer", R.string.designsystem_readiness_action_wake_computer),
    InstallClaude("install_claude", R.string.designsystem_readiness_action_install_claude),
    LoginClaude("login_claude", R.string.designsystem_readiness_action_login_claude),
    ConnectModel("connect_model", R.string.designsystem_readiness_action_connect_model),
    PickModel("pick_model", R.string.designsystem_readiness_action_pick_model),
    EditProvider("edit_provider", R.string.designsystem_readiness_action_edit_provider),
    TestAssistant("test_assistant", R.string.designsystem_readiness_action_test_assistant);

    /** The help this button opens in place, when it has no page of its own on the phone ("Add computer" has one: Machines › "Add computer"). */
    val help: ReadinessHelp? get() = when (this) {
        WakeComputer -> ReadinessHelp.WakeComputer
        InstallClaude -> ReadinessHelp.InstallClaude
        LoginClaude -> ReadinessHelp.LoginClaude
        else -> null
    }

    companion object {
        fun of(id: String?): ReadinessActionId? = entries.firstOrNull { it.wire == id }
    }
}

/** The button for a coded refusal's [FailureAction] (operationFeedback), null for the ordinary ones. */
fun FailureAction.readinessAction(): ReadinessActionId? = when (this) {
    FailureAction.AddComputer -> ReadinessActionId.AddComputer
    FailureAction.ConfirmComputer -> ReadinessActionId.ConfirmComputer
    FailureAction.WakeComputer -> ReadinessActionId.WakeComputer
    FailureAction.InstallClaude -> ReadinessActionId.InstallClaude
    FailureAction.LoginClaude -> ReadinessActionId.LoginClaude
    FailureAction.ConnectModel -> ReadinessActionId.ConnectModel
    FailureAction.EditProvider -> ReadinessActionId.EditProvider
    else -> null
}

/** What the send button says while the main conversation cannot answer (docs/assistant/onboarding.md 4.4: "Connect a model first" / "Add a computer first"). */
@StringRes
fun blockedSendLabel(step: ReadinessStep): Int = when {
    step.id != ReadinessStep.COMPUTER -> R.string.designsystem_readiness_send_connect_model
    step.code == "node_unreachable" -> R.string.designsystem_readiness_send_wake_computer
    step.code == "machine_pending" -> R.string.designsystem_readiness_send_confirm_computer
    step.code == "node_outdated" -> R.string.designsystem_readiness_send_update_computer
    else -> R.string.designsystem_readiness_send_add_computer
}

/** What a step's primary button does when pressed: its own action, or for a step without one, none. */
fun ReadinessStep.primaryAction(): ReadinessActionId? = ReadinessActionId.of(action?.id)

/**
 * The card for one step the main conversation still needs: the hub's title and sentence, its button and the other
 * ways ([ReadinessStep.altActions]). [busy] spins that button ("Try a message" while the hub tries).
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
fun ReadinessStepCard(
    step: ReadinessStep,
    onAction: (ReadinessActionId) -> Unit,
    modifier: Modifier = Modifier,
    busy: ReadinessActionId? = null,
    /** Shown under the sentence: "Try a message"'s answer or why it failed. */
    note: String? = null,
) {
    val status = when (step.state) {
        ReadinessStep.OK -> Status.Unread
        ReadinessStep.PROBLEM -> Status.NeedsYou
        ReadinessStep.TODO -> Status.NeedsYou
        ReadinessStep.CHECKING -> Status.Working
        else -> Status.Unknown
    }
    val primary = step.primaryAction()
    val others = step.altActions.mapNotNull { ReadinessActionId.of(it.id) }.filter { it != primary }
    JCard(modifier, status = status) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Text(step.title?.takeIf(String::isNotBlank) ?: stringResource(R.string.designsystem_readiness_one_more_step),
                style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
            step.detail?.takeIf(String::isNotBlank)?.let {
                Text(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
            }
            note?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
            if (primary != null || others.isNotEmpty()) FlowRow(
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            ) {
                primary?.let { id ->
                    JButton(stringResource(id.label), { onAction(id) }, size = JButtonSize.Small,
                        loading = busy == id, enabled = busy == null)
                }
                others.forEach { id ->
                    JButton(stringResource(id.label), { onAction(id) }, kind = JButtonKind.Secondary, size = JButtonSize.Small,
                        loading = busy == id, enabled = busy == null)
                }
            }
        }
    }
}

/** The help a readiness button or a local example question opens in place, never sent to the model. */
enum class ReadinessHelp { AddComputer, WakeComputer, InstallClaude, LoginClaude, WhatCanYouDo, HowToConnectModel;
    companion object {
        /** A local suggestion's help: its own when the app has it, else what Persona does. */
        fun ofSuggestion(id: String): ReadinessHelp = when (id) {
            "how_to_connect_model" -> HowToConnectModel
            else -> WhatCanYouDo
        }
    }
}

/**
 * The help dialog. [machine] is the computer's name when the hub gave one; [onAction] runs a follow-up the dialog
 * offers ("Use an API key instead" → "Connect a model"), after it closes. Without [canConnectModel] (no model providers page to open, as in the
 * first checklist after pairing) those follow-ups are not offered: they would only reopen this help.
 */
@Composable
fun ReadinessHelpDialog(
    help: ReadinessHelp, machine: String?, onDismiss: () -> Unit, onAction: (ReadinessActionId) -> Unit,
    canConnectModel: Boolean = true,
) {
    val where = machine?.takeIf(String::isNotBlank) ?: stringResource(R.string.designsystem_readiness_that_computer)
    val gotIt = stringResource(R.string.designsystem_readiness_got_it) to onDismiss
    val useKey = (stringResource(R.string.designsystem_readiness_use_api_key) to { onDismiss(); onAction(ReadinessActionId.ConnectModel) })
        .takeIf { canConnectModel }
    @Composable fun Lines(vararg lines: String, command: String? = null, after: String? = null) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            lines.forEach { Text(it, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2) }
            command?.let { JCodeBlock(it, small = true) }
            after?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3) }
        }
    }
    when (help) {
        ReadinessHelp.AddComputer -> JDialog(onDismiss, stringResource(R.string.designsystem_readiness_help_add_title), confirm = gotIt, dismiss = null) {
            Lines(stringResource(R.string.designsystem_readiness_help_add_why), stringResource(R.string.designsystem_readiness_help_add_how),
                after = stringResource(R.string.designsystem_readiness_help_add_note))
        }
        ReadinessHelp.WakeComputer -> JDialog(onDismiss, stringResource(R.string.designsystem_readiness_help_wake_title, where), confirm = gotIt, dismiss = null) {
            Lines(stringResource(R.string.designsystem_readiness_help_wake_body, where), after = stringResource(R.string.designsystem_readiness_help_wake_after))
        }
        ReadinessHelp.InstallClaude -> JDialog(onDismiss, stringResource(R.string.designsystem_readiness_help_install_title), confirm = gotIt, dismiss = useKey) {
            Lines(stringResource(R.string.designsystem_readiness_help_install_body, where), command = InstallCommand,
                after = stringResource(R.string.designsystem_readiness_help_install_after))
        }
        ReadinessHelp.LoginClaude -> JDialog(onDismiss, stringResource(R.string.designsystem_readiness_help_login_title), confirm = gotIt, dismiss = useKey) {
            Lines(stringResource(R.string.designsystem_readiness_help_login_body, where), command = LoginCommand,
                after = stringResource(R.string.designsystem_readiness_help_login_after))
        }
        ReadinessHelp.WhatCanYouDo -> JDialog(onDismiss, stringResource(R.string.designsystem_readiness_help_what_title), confirm = gotIt, dismiss = null) {
            Lines(stringResource(R.string.designsystem_readiness_help_what_body), stringResource(R.string.designsystem_readiness_help_what_computer),
                stringResource(R.string.designsystem_readiness_help_what_data), stringResource(R.string.designsystem_readiness_help_what_memory))
        }
        ReadinessHelp.HowToConnectModel -> JDialog(onDismiss, stringResource(R.string.designsystem_readiness_help_model_title),
            confirm = if (canConnectModel) stringResource(R.string.designsystem_readiness_action_connect_model) to { onDismiss(); onAction(ReadinessActionId.ConnectModel) } else gotIt,
            dismiss = gotIt.takeIf { canConnectModel }) {
            Lines(stringResource(R.string.designsystem_readiness_help_model_body), stringResource(R.string.designsystem_readiness_help_model_key),
                stringResource(R.string.designsystem_readiness_help_model_claude))
        }
    }
}

/** What to type on the computer (the same lines the web's help shows). */
private const val InstallCommand = "curl -fsSL https://claude.ai/install.sh | bash"
private const val LoginCommand = "claude auth login"
