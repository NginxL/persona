package persona.feature.chat

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.selection.toggleable
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.state.ToggleableState
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.Item
import persona.core.designsystem.*

/** The selection belongs to this chat. Dismissing the destination sheet returns to the same picks. */
@Stable
internal class ForwardPick {
    var selected by mutableStateOf<Map<String, MessageTarget>?>(null)
        private set
    var sheet by mutableStateOf<List<MessageTarget>?>(null)
        private set
    var refused by mutableStateOf(false)
        private set

    fun open(target: MessageTarget) { sheet = listOf(target) }
    fun dismiss() { sheet = null }

    fun start() {
        val first = sheet?.singleOrNull() ?: return
        selected = mapOf(first.id to first)
        dismiss()
    }

    fun toggle(target: MessageTarget) {
        val current = selected ?: return
        refused = false
        selected = when {
            target.id in current -> current - target.id
            current.size < ForwardMessage.MOST -> current + (target.id to target)
            else -> { refused = true; current }
        }
    }

    fun choose(timeline: List<Item>) {
        val current = selected ?: return
        val inOrder = timeline.mapNotNull { current[it.id] }
        if (inOrder.isNotEmpty()) sheet = inOrder
    }

    fun end() {
        dismiss()
        selected = null
        refused = false
    }
}

/** While selecting, the row owns the tap and TalkBack action, including over links and buttons inside the message. */
@Composable
internal fun PickableMessageColumn(target: MessageTarget?, modifier: Modifier, verticalArrangement: Arrangement.Vertical = Arrangement.Top,
    content: @Composable ColumnScope.() -> Unit) {
    val pick = LocalChatRowActions.current?.menu?.forwardPick
    val selected = pick?.selected
    if (pick == null || selected == null || target == null || target.pending != null || target.text.isBlank()) {
        Column(modifier, verticalArrangement = verticalArrangement, content = content)
        return
    }
    val checked = target.id in selected
    Box(Modifier.fillMaxWidth()) {
        Row(Modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android).clearAndSetSemantics {}, verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JCheckMark(if (checked) ToggleableState.On else ToggleableState.Off)
            Column(Modifier.weight(1f).then(modifier), verticalArrangement = verticalArrangement, content = content)
        }
        Box(Modifier.matchParentSize().testTag("forward-pick-" + target.id)
            .toggleable(checked, role = Role.Checkbox, onValueChange = { pick.toggle(target) })
            .semantics { contentDescription = target.text.take(200) })
    }
}

@Composable
internal fun ForwardPickFooter(pick: ForwardPick, timeline: List<Item>, modifier: Modifier = Modifier) {
    val count = pick.selected?.size ?: return
    BackHandler(enabled = pick.sheet == null, onBack = pick::end)
    Column(modifier.fillMaxWidth().padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s)) {
        if (pick.refused) Text(stringResource(R.string.chat_forward_select_limit, ForwardMessage.MOST),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
            verticalAlignment = Alignment.CenterVertically) {
            JTextButton(stringResource(R.string.chat_dialog_cancel), pick::end)
            Text(stringResource(R.string.chat_forward_selected, count), Modifier.weight(1f), style = PersonaTheme.type.callout,
                color = PersonaTheme.palette.ink, textAlign = TextAlign.Center, maxLines = 2, overflow = TextOverflow.Ellipsis)
            JButton(stringResource(R.string.chat_forward_send_count, count), { pick.choose(timeline) }, Modifier.testTag("forward-picked"),
                size = JButtonSize.Medium, enabled = count > 0)
        }
    }
}
