package persona.feature.chat

import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.CustomAccessibilityAction
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.customActions
import androidx.compose.ui.semantics.semantics
import persona.core.contracts.UndeliveredMessage
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JAction
import persona.core.designsystem.JIcon
import persona.core.designsystem.JMenu
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

/**
 * An owner message the assistant keeps after it could not go in (Session.undelivered): the failed-outbox look, with
 * Delete always and Retry after it gave up. Its actions belong to the hub, not the phone's local outbox.
 */
@Composable
internal fun UndeliveredBubble(
    message: UndeliveredMessage,
    glass: Boolean,
    busy: Boolean,
    canSend: Boolean,
    onAction: ((DeliveryAction) -> Unit)?,
) {
    val failed = message.gaveUp
    val reason = message.error?.takeIf(String::isNotBlank) ?: stringResource(R.string.chat_undelivered_default_reason)
    val status = stringResource(if (failed) R.string.chat_undelivered_failed else R.string.chat_undelivered_waiting, reason)
    val spoken = when {
        !failed -> stringResource(R.string.chat_undelivered_waiting_spoken)
        canSend && onAction != null -> stringResource(R.string.chat_undelivered_failed_spoken)
        else -> status
    }
    Column(Modifier.fillMaxWidth(), horizontalAlignment = Alignment.End, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        UserBubble(message.text, "undelivered-${message.id}", faded = glass,
            leading = if (glass) ({ UndeliveredMarkButton(failed, spoken, busy, canSend, onAction) }) else null)
        Text(status, style = PersonaTheme.type.caption,
            color = if (failed) PersonaTheme.palette.danger.text else PersonaTheme.palette.cautionText)
        message.images.takeIf { it > 0 }?.let { count ->
            Text(stringResource(R.string.chat_undelivered_images, count), style = PersonaTheme.type.caption,
                color = PersonaTheme.palette.ink2, maxLines = 1)
        }
        if (!glass && onAction != null) {
            Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                if (failed && canSend) JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small,
                    text = stringResource(R.string.chat_delivery_retry), onClick = { onAction(DeliveryAction.Retry) }, enabled = !busy)
                JButton(kind = JButtonKind.Secondary, size = JButtonSize.Small,
                    text = stringResource(R.string.chat_delivery_delete), onClick = { onAction(DeliveryAction.Delete) }, enabled = !busy)
            }
        }
    }
}

@Composable
private fun UndeliveredMarkButton(
    failed: Boolean,
    spoken: String,
    busy: Boolean,
    canSend: Boolean,
    onAction: ((DeliveryAction) -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val retryLabel = stringResource(R.string.chat_delivery_retry)
    val deleteLabel = stringResource(R.string.chat_delivery_delete)
    var open by remember { mutableStateOf(false) }
    val actions = if (onAction == null || busy) emptyList() else buildList {
        if (failed && canSend) add(DeliveryAction.Retry)
        add(DeliveryAction.Delete)
    }
    Box(
        Modifier.size(Tokens.MinTouch.android).clip(CircleShape)
            .then(if (actions.isEmpty()) Modifier else Modifier.clickable(role = Role.Button) { open = true })
            .semantics {
                contentDescription = spoken
                if (actions.isNotEmpty()) customActions = actions.map { action ->
                    CustomAccessibilityAction(if (action == DeliveryAction.Retry) retryLabel else deleteLabel) {
                        onAction?.invoke(action); true
                    }
                }
            },
        contentAlignment = Alignment.Center,
    ) {
        JIcon(if (failed) PersonaIcons.errorFilled else PersonaIcons.time, null,
            size = if (failed) Tokens.Size.iconXS else Tokens.Size.markS,
            tint = if (failed) palette.danger.dot else palette.cautionText, decorative = true)
        JMenu(open, { open = false }, actions.map { action ->
            val label = stringResource(if (action == DeliveryAction.Retry) R.string.chat_delivery_retry else R.string.chat_delivery_delete)
            JAction(label, if (action == DeliveryAction.Delete) PersonaIcons.delete else PersonaIcons.retry,
                destructive = action == DeliveryAction.Delete) {
                open = false
                onAction?.invoke(action)
            }
        })
    }
}
