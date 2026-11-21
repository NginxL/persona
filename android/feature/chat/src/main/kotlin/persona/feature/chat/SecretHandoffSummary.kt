package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Interaction
import persona.core.designsystem.DecisionButtons
import persona.core.designsystem.JAction
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens

/** The complete non-secret scope of a one-run hand-off, in the chat and in the Agents tab (#3962). */
@Composable
fun SecretHandoffSummary(request: Interaction) {
    val handoff = request.secretHandoff ?: return
    Column(Modifier.testTag("secret-handoff-summary"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Text(when {
            handoff.machine.isNotEmpty() && handoff.agent.isNotEmpty() ->
                stringResource(R.string.chat_secret_handoff_lead, handoff.agent, handoff.machine)
            handoff.machine.isNotEmpty() -> stringResource(R.string.chat_secret_handoff_lead_unnamed, handoff.machine)
            else -> stringResource(R.string.chat_secret_handoff_lead_bare)
        }, style = PersonaTheme.type.body)
        handoff.variables.forEach { variable ->
            Column(Modifier.fillMaxWidth().testTag("secret-handoff-variable"), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Text(variable.name, style = PersonaTheme.type.code)
                val item = listOf(variable.provider, variable.title).filter(String::isNotEmpty).joinToString(" › ")
                if (item.isNotEmpty()) Text(item, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
            }
        }
        if (handoff.cwd.isNotEmpty()) Text(handoff.cwd, style = PersonaTheme.type.code, color = PersonaTheme.palette.ink2)
        if (handoff.prompt.isNotEmpty()) Text(handoff.prompt, style = PersonaTheme.type.callout)
        Text(stringResource(R.string.chat_secret_handoff_note), style = PersonaTheme.type.caption,
            color = PersonaTheme.palette.ink2)
    }
}

/** Deny, the hub-scoped Always choice, and Allow; malformed or incomplete cards permit only Deny. */
@Composable
fun SecretHandoffActions(
    request: Interaction,
    canDeny: Boolean,
    canAllow: Boolean,
    canAlways: Boolean,
    onDeny: () -> Unit,
    onAllow: () -> Unit,
    onAlways: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val scope = request.alwaysLabel?.takeIf(String::isNotBlank)
    Column(modifier, verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        DecisionButtons(
            primary = JAction(stringResource(R.string.chat_request_allow), enabled = canAllow, onClick = onAllow),
            secondary = JAction(stringResource(R.string.chat_request_deny), enabled = canDeny, onClick = onDeny),
            more = if (scope == null) emptyList() else listOf(
                JAction(stringResource(R.string.chat_secret_handoff_always), enabled = canAlways, onClick = onAlways)),
        )
        scope?.let { Text(stringResource(R.string.chat_secret_handoff_always_scope, it),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2) }
    }
}
