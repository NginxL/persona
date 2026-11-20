package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextAlign
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.HeldAgentMessage
import persona.core.contracts.Item
import persona.core.contracts.Machine
import persona.core.contracts.MessageEcho
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentMessageHoldFeature
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.api.DeliveryState
import persona.core.contracts.api.HeldAgentActions
import persona.core.contracts.api.OutboxMessage
import persona.core.designsystem.JDialog
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.operationFeedback
import persona.core.domain.sort.canSend

/** The manager owns held rows. The transcript replaces them by operation ID, even on a phone that never sent them. */
internal data class HeldChatMessages(val rows: List<HeldAgentMessage>, val outbox: List<OutboxMessage>)

internal fun heldChatMessages(undelivered: List<HeldAgentMessage>?, items: List<Item>, outbox: List<OutboxMessage>): HeldChatMessages {
    val held = undelivered.orEmpty().filter { it.id.isNotBlank() && !it.computer.isNullOrBlank() }.distinctBy { it.id }
    val heldIds = held.mapTo(mutableSetOf()) { it.id }
    if (heldIds.isEmpty() && outbox.isEmpty()) return HeldChatMessages(emptyList(), outbox)
    val landed = items.mapNotNullTo(mutableSetOf()) { item ->
        MessageEcho.tag(item) ?: item.id.takeIf { MessageEcho.carries(item, it) }
    }
    val rows = held.filterNot { it.id in landed }
    // A push can beat the 202. A retried attachment can have an operation ID different from its local bubble ID.
    val local = outbox.filterNot { it.state == DeliveryState.Held || it.clientMessageId in heldIds ||
        it.opId in heldIds || it.opId in landed }
    return HeldChatMessages(rows, local)
}

@Composable
internal fun heldActionsFor(repository: ChatRepository, key: SessionKey): HeldAgentActions? {
    val actions = (repository as? ChatLocalStorage)?.heldMessages(key) ?: return null
    val features by actions.features.collectAsStateWithLifecycle()
    return actions.takeIf { AgentMessageHoldFeature in features }
}

@Composable
internal fun offlineAgentPlaceholder(session: Session?, machine: Machine?, supportsHold: Boolean): String? {
    if (machine == null || machine.online || session == null || session.assistant || !session.canSend) return null
    val computer = machine.name.ifBlank { machine.id }
    val canHold = supportsHold && !session.awaitingFirstMessage && session.pendingSwitch == null
    return stringResource(if (canHold) R.string.chat_held_composer else R.string.chat_held_computer_offline, computer)
}

/** Retry always names the manager's original operation. It checks an uncertain delivery before any fresh send. */
@Composable
internal fun HeldAgentBubble(row: HeldAgentMessage, actions: HeldAgentActions?, canSend: Boolean, onNotice: (String) -> Unit) {
    val resources = LocalResources.current
    val scope = rememberCoroutineScope()
    var busy by remember(row.id) { mutableStateOf(false) }
    var confirmRemoval by remember(row.id, row.state, row.deleteOnly) { mutableStateOf(false) }
    fun act(retry: Boolean) {
        if (actions == null || busy) return
        busy = true
        scope.launch {
            try {
                if (retry) actions.retry(row) else actions.cancel(row.id)
            } catch (e: CancellationException) {
                throw e
            } catch (e: Exception) {
                onNotice(operationFeedback(e).text.resolve(resources))
            } finally {
                busy = false
            }
        }
    }
    val failed = row.state == "failed"
    val waiting = row.state == "waiting"
    val status = row.error?.takeIf(String::isNotBlank) ?: when {
        waiting -> stringResource(R.string.chat_held_waiting, row.computer.orEmpty())
        else -> stringResource(R.string.chat_held_unconfirmed)
    }
    val images = if (row.images > 0) pluralStringResource(R.plurals.chat_held_images, row.images, row.images) else null
    Column(Modifier.fillMaxWidth().testTag("held-${row.id}"), horizontalAlignment = Alignment.End,
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        UserBubble(row.text.ifBlank { images.orEmpty() }, row.id)
        if (images != null && row.text.isNotBlank())
            Text(images, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        Text(status, Modifier.padding(start = Tokens.Space.m).semantics { liveRegion = LiveRegionMode.Polite }, textAlign = TextAlign.End,
            style = PersonaTheme.type.caption, color = if (failed) PersonaTheme.palette.danger.text else PersonaTheme.palette.needsYou.text)
        if (actions != null && (waiting || failed)) {
            val remove = stringResource(if (failed) R.string.chat_delivery_delete else R.string.chat_outbox_dont_send)
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                if (failed && !row.deleteOnly && canSend) {
                    val allowed by remember(actions, row) { actions.canRetry(row) }.collectAsStateWithLifecycle(false)
                    JTextButton(stringResource(R.string.chat_delivery_retry), { act(true) }, enabled = !busy && allowed)
                }
                JTextButton(remove, { confirmRemoval = true }, enabled = !busy)
            }
            if (confirmRemoval) JDialog(onDismiss = { confirmRemoval = false },
                title = stringResource(R.string.chat_held_remove_title), text = stringResource(R.string.chat_held_remove_detail),
                confirm = remove to { confirmRemoval = false; act(false) }, destructive = true)
        }
    }
}
