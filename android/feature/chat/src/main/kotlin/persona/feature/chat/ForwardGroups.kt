package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.domain.MessageContext

/** Adjacent forwards from one original session share a card. Everything else stays in its own place. */
internal fun <T> forwardGroups(blocks: List<T>, origin: (T) -> ForwardMessage.Origin?): List<List<T>> {
    val groups = mutableListOf<MutableList<T>>()
    var previous: ForwardMessage.Origin? = null
    for (block in blocks) {
        val source = origin(block)
        if (source?.sameChat(previous) == true) groups.last().add(block)
        else groups.add(mutableListOf(block))
        previous = source
    }
    return groups
}

internal fun bubbleForwardGroups(blocks: List<MessageContext>): List<List<MessageContext>> {
    if (blocks.none { it.kind == ForwardMessage.KIND }) return emptyList()
    return forwardGroups(blocks) { if (it.kind == ForwardMessage.KIND) ForwardMessage.origin(it.url, it.attributes["msg"]) else null }
        .filter { it.first().kind == ForwardMessage.KIND }
}

/** Replies and source cards keep ordinary contexts together and show a forwarding run at its original position. */
@Composable
internal fun MessageForwardContexts(blocks: List<MessageContext>, id: String, open: (String) -> Unit, alignEnd: Boolean) {
    if (blocks.none { it.kind == ForwardMessage.KIND }) {
        MessageContextGroup(blocks, id, open, alignEnd = alignEnd)
        return
    }
    val runs = remember(blocks) { forwardGroups(blocks) { if (it.kind == ForwardMessage.KIND) ForwardMessage.origin(it.url, it.attributes["msg"]) else null } }
    runs.forEachIndexed { index, group ->
        if (group.first().kind == ForwardMessage.KIND) ForwardBubbleGroup(group, "$id/$index")
        else MessageContextGroup(group, "$id/$index", open, alignEnd = alignEnd)
    }
}

private const val PREVIEW_LINES = 3

@Composable
private fun GroupTitle(count: Int, chat: String?) {
    val title = if (chat.isNullOrBlank()) stringResource(R.string.chat_forward_group, count)
        else stringResource(R.string.chat_forward_group_chat, count, chat)
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalAlignment = Alignment.CenterVertically) {
        JIcon(PersonaIcons.forwardMessage, null, size = Tokens.Space.l, tint = PersonaTheme.palette.accent, decorative = true)
        Text(title, style = PersonaTheme.type.callout, color = PersonaTheme.palette.accent, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

/** The same grouped accessory in the picker, existing chats, Persona's new conversation, and an Agent's first message. */
@Composable
fun ForwardComposerCards(contexts: List<ChatLinkContext>, compact: Boolean = false, singleLines: Int = 2,
    onRemove: ((List<ChatLinkContext>) -> Unit)? = null) {
    val groups = remember(contexts) { forwardGroups(contexts) { if (it.isForward) ForwardMessage.origin(it) else null } }
    for (group in groups) {
        val remove = onRemove?.let { callback -> { callback(group) } }
        val first = group.first()
        if (group.size == 1) {
            ChatContextCard(first, compact, if (contexts.size == 1) singleLines else 1, remove)
        } else {
            JCard(Modifier.fillMaxWidth().testTag("forward-group-bar"), padding = if (compact) Tokens.Space.xs else Tokens.Space.s) {
                Row(verticalAlignment = Alignment.Top) {
                    Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                        GroupTitle(group.size, first.extra["chat"])
                        if (!compact) {
                            val resources = LocalResources.current
                            group.take(PREVIEW_LINES).forEach { ref ->
                                val author = ForwardMessage.shownAuthor(ref.extra, resources)
                                val text = ForwardMessage.summary(ref.text, resources).replace('\n', ' ')
                                Text(stringResource(R.string.chat_forward_group_line, author, text), style = PersonaTheme.type.caption,
                                    color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            }
                            if (group.size > PREVIEW_LINES) Text(stringResource(R.string.chat_forward_group_more, group.size - PREVIEW_LINES),
                                style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                            first.route?.let { Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2, maxLines = 2) }
                        }
                    }
                    if (remove != null) JIconButton(PersonaIcons.close, stringResource(R.string.chat_forward_remove), remove, raised = false)
                }
            }
        }
    }
}

/** A sent or pending group: each message opens its original; on a device without that chat it expands in place. */
@Composable
internal fun ForwardBubbleGroup(group: List<MessageContext>, id: String) {
    if (group.size == 1) { ForwardBubbleCard(group.first(), id); return }
    val palette = PersonaTheme.palette
    val resources = LocalResources.current
    val locale = currentLocale
    val chat = LocalChatRowActions.current?.session
    val forwarding = LocalChatForwarding.current
    val rule = Tokens.Space.xs * 3 / 4
    var all by rememberSaveable(id) { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().testTag("forward-group").clip(PersonaTheme.shapes.controlShape).background(palette.sunken)
        .drawBehind { drawRect(palette.accent, size = Size(rule.toPx(), size.height)) }
        .padding(start = Tokens.Space.m, end = Tokens.Space.s, top = Tokens.Space.s, bottom = Tokens.Space.s),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        GroupTitle(group.size, group.first().attributes["chat"])
        for (message in if (all) group else group.take(PREVIEW_LINES)) {
            var expanded by rememberSaveable(id, message.url, message.attributes["msg"]) { mutableStateOf(false) }
            val author = ForwardMessage.shownAuthor(message.attributes, resources)
            val time = ForwardMessage.meta(message.attributes, resources, locale)
            val summary = remember(message) { ForwardMessage.summary(message.body, resources) }
            Column(Modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)
                .clickable(role = Role.Button, onClickLabel = stringResource(R.string.chat_forward_open_original)) {
                    if (!openForwarded(message, chat, forwarding)) expanded = !expanded
                }, verticalArrangement = Arrangement.Center) {
                Text(if (time == null) author else stringResource(R.string.chat_forward_meta, author, time),
                    style = PersonaTheme.type.caption, color = palette.ink2)
                val bodyModifier = if (expanded) Modifier.heightIn(max = ChatContextBodyMaxHeight).verticalScroll(rememberScrollState()) else Modifier
                if (expanded) ReadingContent {
                    Text(message.body.ifBlank { summary }, bodyModifier, style = PersonaTheme.type.callout, color = palette.ink)
                } else Text(summary, bodyModifier, style = PersonaTheme.type.callout, color = palette.ink, maxLines = 2,
                    overflow = TextOverflow.Ellipsis)
            }
        }
        if (!all && group.size > PREVIEW_LINES) JTextButton(stringResource(R.string.chat_forward_group_more, group.size - PREVIEW_LINES), { all = true })
    }
}
