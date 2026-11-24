package persona.platform.capsule

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.layout.LayoutCoordinates
import androidx.compose.ui.layout.findRootCoordinates
import androidx.compose.ui.layout.onGloballyPositioned
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.style.TextDecoration
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.m3.markdownColor
import com.mikepenz.markdown.m3.markdownTypography
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons

/** Plain: an icon or a word in the bar. Title: the conversation's name. Tinted: the bar's main action (V7「下一条」). */
internal enum class CapsuleControlStyle { Plain, Title, Tinted }

@Composable
internal fun CapsuleControl(label: String, tag: String, onClick: () -> Unit, modifier: Modifier = Modifier,
    icon: PhIcon? = null, iconOnly: Boolean = false, enabled: Boolean = true, description: String = label,
    style: CapsuleControlStyle = CapsuleControlStyle.Plain) {
    val palette = PersonaTheme.palette
    val tinted = style == CapsuleControlStyle.Tinted
    Row(modifier.heightIn(min = CapsuleTokens.control).clip(PersonaTheme.shapes.chipShape)
        .then(if (tinted) Modifier.background(palette.accentSoft) else Modifier)
        .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
        .semantics { contentDescription = description }.testTag(tag)
        .stableAlpha(if (enabled) 1f else .4f)
        .padding(horizontal = if (tinted) CapsuleTokens.gap else CapsuleTokens.smallGap), verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(CapsuleTokens.smallGap)) {
        if (icon != null && style != CapsuleControlStyle.Title) JIcon(icon, size = CapsuleTokens.icon, tint = palette.ink2)
        if (!iconOnly) Text(label, Modifier.weight(1f, fill = false), style = when (style) {
            CapsuleControlStyle.Title -> PersonaTheme.type.headline
            CapsuleControlStyle.Tinted -> PersonaTheme.type.label
            CapsuleControlStyle.Plain -> PersonaTheme.type.caption
        }, color = if (tinted) palette.accent else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
        if (icon != null && style == CapsuleControlStyle.Title) JIcon(icon, size = CapsuleTokens.icon, tint = palette.ink2)
    }
}

@Composable
internal fun CapsuleUnreadDot(tag: String) {
    val description = stringResource(R.string.capsule_unread)
    Box(Modifier.size(CapsuleTokens.badge).clip(CircleShape).background(PersonaTheme.palette.danger.dot)
        .semantics { contentDescription = description }.testTag(tag))
}

@Composable
internal fun CapsuleMessageBubble(message: CapsuleMessage, modifier: Modifier = Modifier,
    onTextPositioned: ((LayoutCoordinates) -> Unit)? = null) {
    val palette = PersonaTheme.palette
    val positioned = if (onTextPositioned != null) Modifier.onGloballyPositioned(onTextPositioned) else Modifier
    // An agent's or Persona's reply reads as a document (V7): no bubble, its Markdown drawn, a small name above.
    // People's messages (an inbox conversation names its sender) and your own stay chat bubbles.
    if (!message.fromUser && message.sender == null) {
        Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(CapsuleTokens.smallGap)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(CapsuleTokens.smallGap)) {
                CompositionLocalProvider(LocalReduceMotion provides true) { PersonaFace(PersonaState.Idle, size = CapsuleTokens.icon) }
                Text("Persona", style = PersonaTheme.type.micro, color = palette.ink2) // i18n-ignore: product name
                if (message.unread) CapsuleUnreadDot("capsule.message.${message.id}.unread")
            }
            ReadingContent { CapsuleMarkdown(message.text, positioned) }
        }
        return
    }
    Row(Modifier.fillMaxWidth(), horizontalArrangement = if (message.fromUser) Arrangement.End else Arrangement.Start) {
        Box(modifier.widthIn(max = CapsuleTokens.cardWidth * CapsuleTokens.bubbleWidthFraction)
            .clip(PersonaTheme.shapes.controlShape).background(if (message.fromUser) palette.userBubble else palette.surfaceStrong)) {
            Column(Modifier.padding(CapsuleTokens.gap).padding(end = if (message.unread && !message.fromUser) CapsuleTokens.gap else CapsuleTokens.zero)) {
                message.sender?.let { Text(it, style = PersonaTheme.type.micro, color = palette.ink2) }
                ReadingContent {
                    Text(message.text, positioned, style = PersonaTheme.type.callout,
                        color = if (message.fromUser) palette.userBubbleText ?: palette.ink else palette.ink)
                }
            }
            if (message.unread && !message.fromUser) Box(Modifier.align(Alignment.TopEnd).padding(CapsuleTokens.smallGap)) {
                CapsuleUnreadDot("capsule.message.${message.id}.unread")
            }
        }
    }
}

/** The composer's send: a filled circle in the accent once there is something to send (V7). */
@Composable
internal fun CapsuleSendButton(description: String, enabled: Boolean, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    Box(Modifier.size(CapsuleTokens.sendButton).clip(CircleShape).background(if (enabled) palette.action else palette.sunken)
        .clickable(enabled = enabled, role = Role.Button, onClick = onClick)
        .semantics { contentDescription = description }.testTag("capsule.send"), contentAlignment = Alignment.Center) {
        JIcon(PhIcons.ArrowUp, size = CapsuleTokens.icon, tint = if (enabled) palette.onAction else palette.ink3)
    }
}

/** Compact Markdown for the card: the chat page's renderer with the capsule's smaller scale. */
@Composable
private fun CapsuleMarkdown(text: String, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val body = type.callout.copy(color = palette.ink)
    Markdown(text, modifier = modifier.fillMaxWidth(),
        colors = markdownColor(text = palette.ink, codeBackground = palette.sunken, inlineCodeBackground = palette.sunken,
            dividerColor = palette.line, tableBackground = palette.surface),
        typography = markdownTypography(h1 = type.headline, h2 = type.headline, h3 = type.bodyStrong, h4 = type.bodyStrong,
            h5 = type.callout, h6 = type.caption, text = body, paragraph = body, code = type.code, inlineCode = type.code,
            quote = type.callout.copy(color = palette.ink2), ordered = body, bullet = body, list = body, table = body,
            textLink = TextLinkStyles(SpanStyle(color = palette.accent, textDecoration = TextDecoration.Underline))))
}

/** Clipped ancestor bounds prevent offscreen content from becoming a read receipt. */
internal fun capsuleReplyVisible(reply: LayoutCoordinates?, viewport: LayoutCoordinates?): Boolean {
    if (reply == null || viewport == null || !reply.isAttached || !viewport.isAttached) return false
    val root = viewport.findRootCoordinates()
    val messageBounds = root.localBoundingBoxOf(reply, clipBounds = true)
    val viewportBounds = root.localBoundingBoxOf(viewport, clipBounds = true)
    val rootBounds = Rect(0f, 0f, root.size.width.toFloat(), root.size.height.toFloat())
    val intersection = messageBounds.intersect(viewportBounds).intersect(rootBounds)
    return intersection.width > 0f && intersection.height > 0f
}

@Composable
internal fun CapsuleOverlay(title: String, onClose: () -> Unit, content: @Composable ColumnScope.() -> Unit) {
    val palette = PersonaTheme.palette
    Box(Modifier.fillMaxSize().background(palette.scrim).clickable(onClick = onClose).testTag("capsule.overlay")) {
        Column(Modifier.align(Alignment.TopEnd).padding(CapsuleTokens.gap).fillMaxWidth()
            .heightIn(max = CapsuleTokens.menuMaxHeight).clip(PersonaTheme.shapes.cardShape)
            .background(palette.surfaceOpaque).border(CapsuleTokens.border, palette.line, PersonaTheme.shapes.cardShape)
            .clickable {}.verticalScroll(rememberScrollState()).padding(CapsuleTokens.gap)) {
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Text(title, Modifier.weight(1f), style = PersonaTheme.type.label, color = palette.ink,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                CapsuleControl(stringResource(R.string.capsule_close), "capsule.menu.close", onClose,
                    icon = PhIcons.X, iconOnly = true)
            }
            content()
        }
    }
}

@Composable
internal fun CapsuleInboxList(content: CapsuleContent, pending: Boolean, onSelect: (CapsuleEntry) -> Unit,
    onReadFyi: () -> Unit, onReadGroup: (String) -> Unit = {}) {
    var fyiExpanded by remember { mutableStateOf(true) }
    val named = content.entries.any { it.groupTitle != null }
    val grouped = !named && content.entries.any { it.group != null }
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(CapsuleTokens.gap)
        .testTag("capsule.list"), verticalArrangement = Arrangement.spacedBy(CapsuleTokens.smallGap)) {
        if (content.entries.isEmpty()) Text(stringResource(if (content.busy) R.string.capsule_loading else R.string.capsule_empty),
            Modifier.padding(CapsuleTokens.padding), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
        // 未读: one group per machine and folder, Feishu in one, each with its own 全部已读.
        // Only titled groups: the conversation that was open (already read) is not listed again on its own.
        if (named) content.entries.filter { it.groupTitle != null }.groupBy { it.group }.forEach { (group, entries) ->
            Row(Modifier.fillMaxWidth().padding(top = CapsuleTokens.smallGap), verticalAlignment = Alignment.CenterVertically) {
                Text("${entries.first().groupTitle ?: ""} · ${entries.size}", Modifier.weight(1f).padding(CapsuleTokens.smallGap),
                    style = PersonaTheme.type.label, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (group != null) CapsuleControl(stringResource(if (pending) R.string.capsule_syncing else R.string.capsule_read_all),
                    "capsule.group.read.$group", { onReadGroup(group) }, enabled = !pending)
            }
            entries.forEach { entry ->
                JRow(entry.title, Modifier.fillMaxWidth().testTag("capsule.entry.${entry.id}"), subtitle = entry.summary,
                    subtitleLines = 2, leading = { if (entry.unread) CapsuleUnreadDot("capsule.entry.${entry.id}.unread") },
                    trailing = {
                        entry.unreadCount?.takeIf { it > 0 }?.let { count -> Text(pluralStringResource(R.plurals.capsule_message_count, count, count),
                            style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2) }
                    }, onClick = { onSelect(entry) })
            }
        }
        if (named) { content.error?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text) }; return@Column }
        val groups = if (grouped) listOf("action", "fyi", "read-pending", "completed", null) else listOf(null)
        groups.forEach { group ->
            val entries = content.entries.filter { it.group == group }
            if (entries.isNotEmpty()) {
                if (group != null) Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    val label = stringResource(when (group) {
                        "action" -> R.string.capsule_group_action
                        "fyi" -> R.string.capsule_group_fyi
                        "read-pending" -> R.string.capsule_group_pending
                        else -> R.string.capsule_group_completed
                    })
                    if (group == "fyi") CapsuleControl(label, "capsule.fyi.toggle", { fyiExpanded = !fyiExpanded },
                        Modifier.weight(1f), icon = PhIcons.CaretDown)
                    else Text(label, Modifier.weight(1f).padding(CapsuleTokens.smallGap), style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.ink2)
                    if (group == "fyi") CapsuleControl(stringResource(if (pending) R.string.capsule_syncing else R.string.capsule_read_all),
                        "capsule.fyi.read", onReadFyi, enabled = !pending)
                }
                if (group != "fyi" || fyiExpanded) entries.forEach { entry ->
                    JRow(entry.title, Modifier.fillMaxWidth().testTag("capsule.entry.${entry.id}"), subtitle = entry.summary,
                        subtitleLines = 2, leading = {
                            if (entry.unread) CapsuleUnreadDot("capsule.entry.${entry.id}.unread")
                        }, trailing = {
                            entry.unreadCount?.takeIf { it > 0 }?.let { count -> Text(pluralStringResource(R.plurals.capsule_message_count, count, count),
                                style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink2) }
                        }, onClick = { onSelect(entry) })
                }
            }
        }
        content.error?.let { Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.danger.text) }
    }
}
