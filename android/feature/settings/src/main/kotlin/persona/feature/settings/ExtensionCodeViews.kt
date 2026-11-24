package persona.feature.settings

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.IntrinsicSize
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.layout.widthIn
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import persona.core.contracts.api.ExtensionDiffThread
import persona.core.contracts.api.ExtensionViewAction
import persona.core.contracts.api.ExtensionViewComment
import persona.core.contracts.api.ExtensionViewItem
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import java.time.Instant

// UI version 4's code hosting components (docs/extensions/extensions.md "UI version 4", #2951) with this app's design system: a detail
// page's header, a breadcrumb, a file tree, a file's code, a file's diff (one column: a phone has no room for two;
// threads under their lines, a comment box at any line, hidden lines to show), a discussion and a comment box with its
// buttons; the diff is ExtensionCodeReviewUi.kt. What they send goes through ExtensionViewHandle.act: the action with the component's own values. Logic in
// ExtensionCodeText.kt.

/** How many lines of a file or a diff are drawn before "Show all": a long one costs nothing until asked for. */
internal const val LinesShown = 400

@Composable
internal fun tokenColor(kind: TokenKind): Color {
    val palette = PersonaTheme.palette
    return when (kind) {
        TokenKind.Keyword -> palette.accent
        TokenKind.Text -> palette.tone(Tone.Positive).text
        TokenKind.Comment -> palette.ink3
        TokenKind.Number, TokenKind.Type -> palette.cautionText
        TokenKind.Plain -> palette.ink
    }
}

@Composable
internal fun highlighted(tokens: List<CodeToken>?, text: String): AnnotatedString {
    if (tokens == null) return AnnotatedString(text.ifEmpty { " " })
    val colors = TokenKind.entries.associateWith { tokenColor(it) }
    return buildAnnotatedString {
        tokens.forEach { t ->
            withStyle(SpanStyle(color = colors.getValue(t.kind), fontStyle = if (t.kind == TokenKind.Comment) FontStyle.Italic else null)) { append(t.text) }
        }
        if (tokens.isEmpty()) append(" ")
    }
}

// ── header, breadcrumb ──

@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun CodeHeader(item: ExtensionViewItem, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JGroupItem(GroupPosition.Single, modifier) {
        Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.l), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            item.avatar?.let { JAvatar(it.text?.takeIf(String::isNotBlank) ?: item.title.orEmpty().take(1), size = Tokens.Space.xxl) }
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Text(item.title.orEmpty(), style = type.title, color = palette.ink)
                item.subtitle?.takeIf { it.isNotBlank() }?.let { Text(it, style = type.caption, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis) }
                FlowRow(Modifier.padding(top = Tokens.Space.xs), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    item.badges.orEmpty().filter { it.text.isNotBlank() }.forEach { JTag(it.text, ExtensionViewText.tone(it.tone), Modifier.align(Alignment.CenterVertically)) }
                    item.facts.orEmpty().filter { it.value.isNotBlank() }.forEach { f ->
                        Row(Modifier.align(Alignment.CenterVertically), verticalAlignment = Alignment.CenterVertically) {
                            f.icon?.takeIf { it.isNotBlank() }?.let { JIcon(ExtensionIcons.of(it), null, size = Tokens.Space.m + Tokens.Space.xxs, tint = palette.ink3); Spacer(Modifier.width(Tokens.Space.xs)) }
                                ?: f.label?.takeIf { it.isNotBlank() }?.let { Text("$it ", style = type.caption, color = palette.ink3, maxLines = 1) }
                            Text(f.value, style = type.caption.copy(fontWeight = FontWeight.Medium), color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                        }
                    }
                }
            }
        }
    }
}

@Composable
internal fun CodeBreadcrumb(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val crumbs = item.crumbs.orEmpty()
    Row(modifier.fillMaxWidth().horizontalScroll(handle.nativeLocal?.scroll(item.key.orEmpty(), "breadcrumb-horizontal") ?: rememberScrollState()).padding(horizontal = rowPadding, vertical = Tokens.Space.xs),
        verticalAlignment = Alignment.CenterVertically) {
        crumbs.forEachIndexed { i, c ->
            if (i > 0) Text(" / ", style = type.callout, color = palette.ink3)
            val action = c.action?.takeIf { it.id.isNotEmpty() && i < crumbs.lastIndex }
            Text(c.title, style = if (i == crumbs.lastIndex) type.callout.copy(fontWeight = FontWeight.SemiBold) else type.callout,
                color = if (action != null) palette.accent else palette.ink, maxLines = 1,
                modifier = if (action != null) Modifier.clip(RoundedCornerShape(Tokens.Radius.badge)).clickable(enabled = !state.busy) { handle.act(action, emptyMap()) }
                    .padding(horizontal = Tokens.Space.xxs, vertical = Tokens.Space.xxs) else Modifier)
        }
    }
}

// ── file tree ──

@Composable
internal fun CodeFileTree(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier, onPicked: () -> Unit = {}) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val entries = item.entries.orEmpty()
    val queryState: androidx.compose.runtime.MutableState<String> = handle.nativeLocal?.slot(item.key.orEmpty(), "file-query") { "" } ?: rememberSaveable(item.key) { mutableStateOf("") }
    var query by queryState
    val shown = remember(entries, query) { if (query.isBlank()) entries else entries.filter { it.kind != "dir" && it.path.contains(query.trim(), ignoreCase = true) } }
    val tree = remember(shown) { ExtensionCodeText.tree(shown) }
    val closedState: androidx.compose.runtime.MutableState<Set<String>> = handle.nativeLocal?.slot(item.key.orEmpty(), "file-closed") { emptySet<String>() } ?: remember(item.key) { mutableStateOf(emptySet<String>()) }
    var closed by closedState
    val askedState: androidx.compose.runtime.MutableState<String> = handle.nativeLocal?.slot(item.key.orEmpty(), "file-asked") { "" } ?: remember(item.key) { mutableStateOf("") }
    var asked by askedState
    val lines = ExtensionCodeText.lines(tree, if (query.isBlank()) closed else emptySet())
    JGroupItem(GroupPosition.Single, modifier) {
        Column(Modifier.fillMaxWidth().padding(vertical = Tokens.Space.xs)) {
            if (ExtensionCodeText.files(entries) > 12) JSearchField(query, { query = it }, stringResource(R.string.settings_ext_code_filter_files),
                Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.xs))
            lines.forEach { (n, depth) ->
                val indent = Tokens.Space.m + Tokens.Space.l * depth
                if (n.dir) {
                    val open = !n.unloaded && n.path !in closed
                    Row(Modifier.fillMaxWidth().clickable(enabled = !state.busy || !n.unloaded) {
                        if (n.unloaded) item.expand?.takeIf { it.id.isNotEmpty() }?.let { asked = n.path; handle.act(it, mapOf("path" to n.path)) }
                        else closed = if (n.path in closed) closed - n.path else closed + n.path
                    }.padding(start = indent, end = rowPadding, top = Tokens.Space.s, bottom = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                        if (state.acting == item.expand?.id && asked == n.path) JSpinner() else JIcon(if (open || query.isNotBlank()) PhIcons.CaretDown else PhIcons.CaretRight, null, size = Tokens.Space.m, tint = palette.ink3)
                        Spacer(Modifier.width(Tokens.Space.s))
                        JIcon(PhIcons.FolderSimple, null, size = Tokens.Space.l, tint = palette.ink3)
                        Spacer(Modifier.width(Tokens.Space.s))
                        Text(n.name, Modifier.weight(1f), style = type.callout, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                } else {
                    val e = n.entry!!
                    val on = n.path == item.selected
                    val mark = ExtensionCodeText.status(e.status)
                    Row(Modifier.fillMaxWidth().then(if (on) Modifier.background(palette.statusSurface) else Modifier).clickable(enabled = !state.busy) {
                        item.action?.takeIf { it.id.isNotEmpty() }?.let { handle.act(it, mapOf("path" to n.path)) } ?: handle.reveal(n.path)
                        onPicked()
                    }.padding(start = indent + Tokens.Space.m + Tokens.Space.s, end = rowPadding, top = Tokens.Space.s, bottom = Tokens.Space.s),
                        verticalAlignment = Alignment.CenterVertically) {
                        JIcon(PhIcons.FileText, null, size = Tokens.Space.l, tint = palette.ink3)
                        Spacer(Modifier.width(Tokens.Space.s))
                        Text(n.name, Modifier.weight(1f), style = type.callout, color = if (e.status == "deleted") palette.ink3 else palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                            textDecoration = if (e.status == "deleted") TextDecoration.LineThrough else null)
                        e.comments?.takeIf { it > 0 }?.let {
                            JIcon(PhIcons.ChatCircle, null, size = Tokens.Space.m, tint = palette.ink3)
                            Text(" $it  ", style = type.micro, color = palette.ink3)
                        }
                        if ((e.additions ?: 0) > 0) Text("+${e.additions}", style = type.micro, color = palette.diffAdd, maxLines = 1)
                        if ((e.deletions ?: 0) > 0) Text(" −${e.deletions}", style = type.micro, color = palette.diffDel, maxLines = 1)
                        e.badge?.takeIf { it.text.isNotBlank() }?.let { Spacer(Modifier.width(Tokens.Space.s)); JTag(it.text, ExtensionViewText.tone(it.tone)) }
                        mark?.let { (letter, tone) ->
                            Spacer(Modifier.width(Tokens.Space.s))
                            Text(letter, style = type.micro.copy(fontWeight = FontWeight.Bold), color = PersonaTheme.palette.tone(tone).text,
                                modifier = Modifier.semantics { contentDescription = e.status.orEmpty() })
                        }
                    }
                }
            }
            if (lines.isEmpty()) Text(stringResource(if (query.isBlank()) R.string.settings_ext_code_no_files else R.string.settings_ext_code_no_match),
                Modifier.fillMaxWidth().padding(Tokens.Space.l), style = type.caption, color = palette.ink3)
        }
    }
}

// ── code ──

@Composable
internal fun FileBar(path: String, trailing: @Composable () -> Unit, lead: (@Composable () -> Unit)? = null, onClick: (() -> Unit)? = null) {
    val palette = PersonaTheme.palette
    Row(Modifier.fillMaxWidth().then(if (onClick != null) Modifier.clickable(onClick = onClick) else Modifier)
        .padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalAlignment = Alignment.CenterVertically) {
        lead?.invoke() ?: JIcon(PhIcons.FileText, null, size = Tokens.Space.l, tint = palette.ink3)
        Spacer(Modifier.width(Tokens.Space.s))
        Text(path, Modifier.weight(1f), style = PersonaTheme.type.codeSmall, color = palette.ink, maxLines = 1, overflow = TextOverflow.StartEllipsis)
        Spacer(Modifier.width(Tokens.Space.s))
        trailing()
    }
}

@Composable
internal fun CodeFile(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val text = item.text.orEmpty()
    val lines = remember(text) { text.removeSuffix("\n").split('\n') }
    val tokens = remember(text, item.language, item.path) { ExtensionCodeText.highlight(lines, ExtensionCodeText.language(item.language, item.path)) }
    var all by rememberSaveable(item.key, text.length) { mutableStateOf(false) }
    val start = item.startLine ?: 1
    val hl = item.highlight
    JGroupItem(GroupPosition.Single, modifier) {
        FileBar(item.path?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_code_file), trailing = {
            Text(pluralStringResource(R.plurals.settings_ext_code_lines, lines.size, lines.size), style = type.micro, color = palette.ink3, maxLines = 1)
        })
        JDivider()
        val shown = if (all) lines.size else minOf(lines.size, LinesShown)
        Row(Modifier.fillMaxWidth().background(palette.sunken).padding(vertical = Tokens.Space.s)) {
            Column {
                for (i in 0 until shown) {
                    val n = start + i
                    val on = hl != null && n in hl.start..hl.end
                    Text("$n", Modifier.then(if (on) Modifier.background(palette.statusSurface) else Modifier).widthIn(min = Tokens.Space.xxl + Tokens.Space.s).padding(horizontal = Tokens.Space.s),
                        style = type.codeSmall, color = palette.ink3, maxLines = 1, textAlign = androidx.compose.ui.text.style.TextAlign.End)
                }
            }
            Box(Modifier.weight(1f).horizontalScroll(handle.nativeLocal?.scroll(item.key.orEmpty(), "code-horizontal") ?: rememberScrollState())) { Column(Modifier.width(IntrinsicSize.Max)) {
                for (i in 0 until shown) {
                    val n = start + i
                    val on = hl != null && n in hl.start..hl.end
                    Text(highlighted(tokens.getOrNull(i), lines[i]), Modifier.fillMaxWidth().then(if (on) Modifier.background(palette.statusSurface) else Modifier).padding(end = Tokens.Space.l),
                        style = type.codeSmall, maxLines = 1, softWrap = false)
                }
            } }
        }
        if (shown < lines.size) LoadLine(stringResource(R.string.settings_ext_code_show_all, lines.size), busy = false, enabled = true) { all = true }
        val load = item.load?.takeIf { it.id.isNotEmpty() && item.truncated == true }
        if (load != null && shown == lines.size) LoadLine(stringResource(R.string.settings_ext_code_load_rest), busy = state.acting == load.id, enabled = !state.busy) {
            handle.act(load, mapOf("path" to item.path.orEmpty(), "from" to "${start + lines.size}"))
        }
    }
}

@Composable
internal fun LoadLine(title: String, busy: Boolean, enabled: Boolean, onClick: () -> Unit) {
    JDivider()
    Row(Modifier.fillMaxWidth().clickable(enabled = enabled, onClick = onClick).padding(vertical = Tokens.Space.m), horizontalArrangement = Arrangement.Center,
        verticalAlignment = Alignment.CenterVertically) {
        if (busy) { JSpinner(); Spacer(Modifier.width(Tokens.Space.s)) }
        Text(title, style = PersonaTheme.type.callout, color = PersonaTheme.palette.accent, maxLines = 1)
    }
}

// ── comments ──

@Composable
internal fun CommentLine(c: ExtensionViewComment) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val res = LocalResources.current
    val ago = c.at?.let { runCatching { agoText(res, Instant.parse(it)) }.getOrNull() }
    if (c.event) {
        Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
            Text("${c.author} ${c.body.orEmpty()}", Modifier.weight(1f), style = type.caption, color = palette.ink3)
            ago?.let { Text(it, style = type.micro, color = palette.ink3, maxLines = 1) }
        }
        return
    }
    Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
        JAvatar(c.avatar?.text?.takeIf(String::isNotBlank) ?: c.author.take(1), size = Tokens.Space.xl + Tokens.Space.xs)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(c.author, style = type.callout.copy(fontWeight = FontWeight.SemiBold), color = palette.ink, maxLines = 1, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f, fill = false))
                ago?.let { Spacer(Modifier.width(Tokens.Space.s)); Text(it, style = type.micro, color = palette.ink3, maxLines = 1) }
                c.badge?.takeIf { it.text.isNotBlank() }?.let { Spacer(Modifier.width(Tokens.Space.s)); JTag(it.text, ExtensionViewText.tone(it.tone)) }
            }
            c.body?.takeIf { it.isNotBlank() }?.let { ReadingContent { ExtensionMarkdown(it) } }
        }
    }
}

/** A text box and one button (a reply, a comment at a line), with "Cancel" when it can close. */
@Composable
internal fun MiniComposer(placeholder: String, busy: Boolean, enabled: Boolean, onCancel: (() -> Unit)?,
                          local: NativeExtensionLocal? = null, nodeKey: String = "", slot: String = "reply",
                          send: (String) -> Unit) {
    val bodyState: androidx.compose.runtime.MutableState<String> = local?.slot(nodeKey, slot) { "" } ?: rememberSaveable { mutableStateOf("") }
    var body by bodyState
    Column(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JTextField(body, { body = it }, Modifier.fillMaxWidth().nativeInputFocus(local, nodeKey, slot), placeholder = placeholder, singleLine = false, enabled = enabled)
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.End)) {
            onCancel?.let { JButton(stringResource(R.string.settings_ext_code_cancel), it, kind = JButtonKind.Secondary, size = JButtonSize.Small) }
            JButton(stringResource(R.string.settings_ext_code_comment), { send(body.trim()); if (local == null) body = "" }, kind = JButtonKind.Primary, size = JButtonSize.Small,
                loading = busy, enabled = enabled && body.isNotBlank())
        }
    }
}

@Composable
internal fun ReplyBox(action: ExtensionViewAction, values: Map<String, String>, state: ExtensionViewState, handle: ExtensionViewHandle) {
    val slot = "reply-${values["thread"].orEmpty()}"
    // Without the extension's own state, the box (open or not, its draft) is the thread's, not its place's: a thread
    // added above doesn't move an open box down one (#4179). No thread id: one key for all, as before.
    key(slot) {
        val openState: androidx.compose.runtime.MutableState<Boolean> = handle.nativeLocal?.slot(handle.nodeKey, "$slot-open") { false } ?: rememberSaveable { mutableStateOf(false) }
        var open by openState
        val palette = PersonaTheme.palette
        if (open) MiniComposer(stringResource(R.string.settings_ext_code_reply), busy = state.acting == action.id, enabled = !state.busy, onCancel = { open = false },
            local = handle.nativeLocal, nodeKey = handle.nodeKey, slot = slot) { body ->
            handle.act(action, values + ("body" to body)); open = false
        } else Text(stringResource(R.string.settings_ext_code_reply), Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.s)
            .clip(RoundedCornerShape(Tokens.Radius.control)).background(palette.sunken).clickable { open = true }.padding(Tokens.Space.m),
            style = PersonaTheme.type.callout, color = palette.ink3, maxLines = 1)
    }
}

@Composable
internal fun CodeCommentThread(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val comments = item.comments.orEmpty()
    JGroupItem(GroupPosition.Single, modifier) {
        item.title?.takeIf { it.isNotBlank() }?.let {
            Row(Modifier.fillMaxWidth().padding(horizontal = rowPadding, vertical = Tokens.Space.m), verticalAlignment = Alignment.CenterVertically) {
                Text(it, Modifier.weight(1f), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (item.resolved == true) JTag(stringResource(R.string.settings_ext_code_resolved), Tone.Positive)
            }
            JDivider()
        }
        comments.forEachIndexed { i, c -> if (i > 0) JDivider(); CommentLine(c) }
        if (comments.isEmpty()) Text(stringResource(R.string.settings_ext_code_no_comments), Modifier.fillMaxWidth().padding(Tokens.Space.l),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3)
        // its reply box is this thread's (the item's key) where the items are drawn by place (#4179)
        item.reply?.takeIf { it.id.isNotEmpty() }?.let { reply -> JDivider(); key(item.key) { ReplyBox(reply, emptyMap(), state, handle) } }
    }
}

@Composable
internal fun CodeComposer(item: ExtensionViewItem, state: ExtensionViewState, handle: ExtensionViewHandle, modifier: Modifier) {
    val name = item.name.orEmpty()
    val value = state.values[name].orEmpty()
    Column(modifier.fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JTextField(value, { handle.edit(name, it) }, Modifier.fillMaxWidth().nativeInputFocus(handle.nativeLocal, handle.nodeKey), placeholder = item.placeholder?.takeIf { it.isNotBlank() } ?: stringResource(R.string.settings_ext_code_leave_comment),
            singleLine = false, enabled = !state.busy)
        Row(Modifier.fillMaxWidth().horizontalScroll(handle.nativeLocal?.scroll(item.key.orEmpty(), "composer-buttons-horizontal") ?: rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s, Alignment.End)) {
            item.buttons.orEmpty().filter { it.action.id.isNotEmpty() }.forEachIndexed { i, b ->
                JButton(b.title, { handle.act(b.action, mapOf(name to value)) }, size = JButtonSize.Small,
                    kind = if (i == 0) JButtonKind.Primary else JButtonKind.Secondary,
                    loading = state.acting == b.action.id, enabled = !state.busy)
            }
        }
    }
}

/** The window's width less the page's sides: what a thread under a line of code takes, however wide the code is. */
@Composable
internal fun screenWidth() = with(androidx.compose.ui.platform.LocalDensity.current) {
    androidx.compose.ui.platform.LocalWindowInfo.current.containerSize.width.toDp()
} - Tokens.Space.l * 2 - Tokens.Space.s
