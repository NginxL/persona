package persona.feature.chat

import persona.core.designsystem.dense
import persona.core.domain.isOwnMessage
import androidx.compose.foundation.background
import androidx.compose.animation.core.*
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.ScrollState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.DisableSelection
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.Dp
import persona.core.domain.diff.LineDiff
import androidx.compose.ui.unit.dp
import persona.core.contracts.Item
import persona.core.contracts.api.ChannelSources
import persona.core.designsystem.*
import persona.core.designsystem.WorkStep as TimelineStep
import persona.core.designsystem.icons.PhIcons


/**
 * An agent's words (a reply, or narration inside a work line): Markdown, its file paths as chips, a long press for the
 * message menu. A text the hub cut (`full_size`) shows the part the page carries and "Show full text (N KB)", which loads the
 * rest in place (R3).
 */
@Composable
internal fun MessageContent(item: Item, user: Boolean, update: ChatTextUpdate? = null, boxed: Boolean = false,
    /** The closeout card above the transcript says this reply's closeout block (#3612): draw the reply without it. */
    hideCloseout: Boolean = false) {
    var whole by remember(item.id) { mutableStateOf<String?>(null) }
    val cut = item.fullSize != null && whole == null && update?.streaming != true
    val text = (whole ?: update?.text ?: item.text.orEmpty().let { if (item.fullSize != null) cutText(it) else it })
        .let { if (hideCloseout) persona.core.domain.sort.stripCloseout(it, complete = !cut && update?.streaming != true) else it }
    val ancillaryStream = remember(item.id, update?.streaming == true) { AssistantAncillaryStream { _, _ -> } }
    val ancillary = if (update?.streaming == true && !user && item.from.isNullOrBlank()) {
        ancillaryStream.update(text, hostnames = LocalChatPorts.current?.hostnames.orEmpty(), linkable = emptySet())
    } else null
    val model = ancillary?.model ?: remember(text, item.from, user) { messageModel(text, item.from, user) }
    val uri = LocalUriHandler.current
    // Forward (#3901) takes the transcript's text and who wrote it when that is another session (a relay, its "From" card).
    val target = MessageTarget(item.id, model.plain, user, model.editText(), raw = text,
        from = item.from?.takeIf { it.isNotBlank() } ?: (model.parts.singleOrNull() as? MessagePart.Source)?.name?.takeIf { it.isNotBlank() },
        time = item.ts, cut = item.id.takeIf { cut }, starItem = item)
    PickableMessageColumn(target, Modifier.fillMaxWidth().messageMenu(target)) {
        MessageForwardContexts(model.contexts, "${item.id}/contexts", uri::openUri, alignEnd = user)
        AttachedFileCards(model.attached, alignEnd = user)
        // a reply still being written: its quotes do not fold yet (#2984)
        CompositionLocalProvider(LocalReplyStreaming provides (update?.streaming == true)) {
            // "Select" selects these words where they are
            SelectableWords(target) {
                Column(Modifier.fillMaxWidth()) {
                    MessageParts(model.parts, user, item.id, boxed = boxed, streaming = update?.streaming == true,
                        ancillaryCards = ancillary?.cards, ownReply = item.role == "assistant" && item.from.isNullOrBlank() && item.source == null)
                }
            }
        }
        StarredChatMark(item)
        if (cut) FullTextButton(item) { whole = it } else ReadingContent { ReaderEntry(text) }
        if (update?.streaming == true) StreamingCursor()
        // An answer's documents as cards with their first page (iOS #1543), the rest as chips.
        if (user) FileChips(model.files, alignEnd = true)
        else {
            FileChips(model.files.filterNot(FileThumbnails::isDocument), alignEnd = false)
            DocumentCards(model.files.filter(FileThumbnails::isDocument))
        }
        if (!user) PortBadges(text, ancillary?.ports)
    }
}

@Composable
private fun StreamingCursor() {
    val transition = rememberInfiniteTransition(label = "streaming-cursor")
    val alpha by transition.animateFloat(1f, 0f,
        infiniteRepeatable(tween(500), RepeatMode.Reverse), label = "cursor-alpha")
    DisableSelection { Text("▍", Modifier.alpha(alpha), color = PersonaTheme.palette.accent, style = PersonaTheme.type.body) }
}

@Composable
private fun MessageParts(parts: List<MessagePart>, user: Boolean, id: String, onLastLine: ((Int) -> Unit)? = null, boxed: Boolean = false,
    streaming: Boolean? = null, ancillaryCards: List<persona.core.domain.views.AssistantFence>? = null, ownReply: Boolean = false) {
    // Bubble padding below the last text line, so the reported extent reaches the bottom of the row.
    val bottomPadding = with(LocalDensity.current) { Tokens.Space.m.roundToPx() }
    // A reply's ```persona-view blocks are numbered across the whole text item: `<item id>/<n>`, the hub's key.
    val firstCard = remember(parts, ancillaryCards) {
        if (ancillaryCards != null && parts.size == 1) listOf(0) else {
            var seen = 0
            parts.map { part ->
                val first = seen
                if (part is MessagePart.Text && persona.core.domain.views.AssistantFences.mentions(part.text))
                    seen += persona.core.domain.views.AssistantFences.blocks(part.text).count { it.isView }
                first
            }
        }
    }
    parts.forEachIndexed { index, part ->
        when (part) {
            is MessagePart.Text -> if (user) ReadingContent {
                val annotated = messageLinkAnnotations(part.text)
                val layoutState = remember { mutableStateOf<androidx.compose.ui.text.TextLayoutResult?>(null) }
                Text(annotated, Modifier.chatLinkLongPress(annotated) { layoutState.value }, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink,
                    onTextLayout = { layout ->
                        layoutState.value = layout
                        if (index == parts.lastIndex && layout.lineCount > 0)
                            onLastLine?.invoke((layout.size.height - layout.getLineTop(layout.lineCount - 1)).toInt() + bottomPadding)
                    })
            } else AssistantAnswerFrame(boxed) {
                    ReplyMarkdown(part.text, "$id/$index", ownReply,
                        viewKeys = { n -> persona.core.domain.views.AssistantFences.key(id, firstCard[index] + n) }, streaming = streaming)
                }
            is MessagePart.Source -> JGroupItem(GroupPosition.Single) {
                Column(Modifier.fillMaxWidth().padding(10.dp.dense)) {
                    Text(stringResource(R.string.chat_content_from, part.name.ifBlank { stringResource(R.string.chat_content_other_session) }), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    // Markdown like an answer: agents write Markdown, and the hub turns a teammate's JSON notes into it.
                    if (part.text.isNotBlank()) ReplyMarkdown(keepLineBreaks(part.text), "$id/$index", ownReply = false)
                }
            }
        }
    }
}

/** What each of a bubble's [items] reads now: its live or full text from [updates], else its own. */
internal fun bubbleTexts(items: List<Item>, updates: Map<String, ChatTextUpdate>): List<String> =
    items.map { updates[it.id]?.text ?: it.text.orEmpty() }

/** The time under your message, in the app's language: 14:30 · 2:30 PM; null when [ts] is not a time. */
internal fun userBubbleTime(ts: String, locale: java.util.Locale, zone: java.time.ZoneId = java.time.ZoneId.systemDefault()): String? =
    persona.core.contracts.parseTime(ts)?.let { LocalizedFormats.format(it, LocalizedFormats.TIME, locale, zone) }

/**
 * Your message (several queued ones read as one): the photos it carries above its text (`<attached_images>`, read from
 * the session's machine, R3), its file paths as chips under the bubble, a long press for the message menu. Its
 * `<persona-context>` blocks fold into one row above the bubble; a message of nothing but blocks has no bubble (#876).
 */
@Composable
internal fun MergedUserBubble(items: List<Item>, updates: Map<String, ChatTextUpdate> = emptyMap(),
    onLastLine: ((Int) -> Unit)? = null, images: (@Composable () -> Unit)? = null, menu: Boolean = true,
    /** A message only in the outbox, in this state: its menu offers what fits one not sent ([MessageTarget.pending]). */
    pending: persona.core.contracts.api.DeliveryState? = null,
    /** An unsent message's look on Glass ([OutboxBubble]): its bubble faded, its state's mark before it. */
    faded: Boolean = false, leading: (@Composable () -> Unit)? = null, openingSource: (@Composable () -> Unit)? = null) {
    // Keyed on this bubble's own texts: [updates] is the whole chat's, and changes with every streamed chunk of a reply.
    val texts = bubbleTexts(items, updates)
    val models = remember(items, texts) { items.mapIndexed { index, item -> messageModel(texts[index], item.from, user = true) } }
    val parts = remember(models) { models.flatMap { it.parts } }
    // A forward (#3901) is a card at the top of the bubble; the other blocks fold into one row above it, as before.
    val forwards = remember(models) { models.flatMap { bubbleForwardGroups(it.contexts) } }
    val contexts = remember(models) { models.flatMap { it.contexts }.filter { it.kind != ForwardMessage.KIND } }
    val photos = remember(models) { models.flatMap { it.images }.distinct() }
    val files = remember(models) { models.flatMap { it.files }.distinct().take(6) }
    val attached = remember(models) { models.flatMap { it.attached }.distinctBy { it.path } }
    val target = remember(models, items, texts, pending) {
        MessageTarget(items.firstOrNull()?.id.orEmpty(), models.joinToString("\n\n") { it.plain }.trim(), true,
            models.joinToString("\n\n") { it.editText() }.trim(), pending, items.lastOrNull()?.ts,
            raw = texts.joinToString("\n\n"),
            from = items.firstNotNullOfOrNull { it.from?.takeIf(String::isNotBlank) },
            cut = items.singleOrNull()?.takeIf { it.fullSize != null && updates[it.id] == null }?.id,
            starItem = items.singleOrNull())
    }
    val timestamp = items.lastOrNull()?.ts
    val locale = currentLocale
    val time = remember(timestamp, locale) { timestamp?.let { userBubbleTime(it, locale) } }
    // A message that came in from a channel (WeChat, the Feishu bot…) says so under it; the app's own say nothing.
    val channels = LocalChannels.current
    val source = remember(items, channels) { items.firstNotNullOfOrNull { ChannelSources.of(it.clientMsgId, channels) } }
    val sourceLine: (@Composable () -> Unit)? = if (source != null) ({ JChannelSource(source) }) else openingSource
    val starredUserItem = items.singleOrNull()
    val hasStarMark = starredUserItem != null &&
        LocalStarredChatContext.current?.source(starredUserItem)?.let { starSource ->
            LocalStarredMessagesStore.current?.state?.collectAsState()?.value?.mark(starSource) != null
        } == true
    val footer: (@Composable () -> Unit)? = if (time == null && files.isEmpty() && sourceLine == null && !hasStarMark) null else ({
        Column(horizontalAlignment = Alignment.End) {
            FileChips(files, alignEnd = true)
            if (sourceLine == null && !hasStarMark) time?.let { Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3) }
            else Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                sourceLine?.invoke()
                if (hasStarMark) StarredChatMark(starredUserItem)
                time?.let { Text(it, style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3) }
            }
        }
    })
    val uri = LocalUriHandler.current
    val streaming = items.any { updates[it.id]?.streaming == true }
    // Typed by the user: the pale blue; another session's or conversation's keeps the style's tint (#2643).
    val own = remember(items) { items.all(::isOwnMessage) }
    // Only blocks, no words, photos or cursor: the folded row alone, no empty bubble.
    val bubble = (contexts.isEmpty() && attached.isEmpty()) || parts.isNotEmpty() || photos.isNotEmpty() || images != null || streaming ||
        forwards.isNotEmpty()
    val fromCard = models.any { it.fromCard }
    PickableMessageColumn(target.takeIf { menu }, Modifier.fillMaxWidth()) {
        MessageContextGroup(contexts, "${items.firstOrNull()?.id.orEmpty()}/contexts", uri::openUri)
        AttachedFileCards(attached)
        if (fromCard) Text(stringResource(R.string.chat_from_card), Modifier.align(Alignment.End).padding(bottom = Tokens.Space.xs),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1)
        if (bubble) UserBubbleFrame(Modifier.messageMenu(target.takeIf { menu }), footer = footer, own = own, faded = faded, leading = leading) {
            forwards.forEachIndexed { index, forward ->
                ForwardBubbleGroup(forward, "${items.firstOrNull()?.id.orEmpty()}/forward/$index")
                if (index < forwards.lastIndex || parts.isNotEmpty() || photos.isNotEmpty() || images != null || streaming)
                    Spacer(Modifier.height(Tokens.Space.s))
            }
            images?.invoke()
            if (photos.isNotEmpty()) {
                AttachedPhotoRow(photos)
                if (parts.isNotEmpty()) Spacer(Modifier.height(Tokens.Space.s))
            }
            val foldText = remember(parts, streaming) {
                if (streaming || parts.isEmpty() || parts.any { it !is MessagePart.Text }) ""
                else parts.filterIsInstance<MessagePart.Text>().joinToString("\n\n") { it.text }
            }
            val foldKey = items.firstOrNull()?.id?.takeIf { it.isNotBlank() && foldText.isNotEmpty() }
            SelectableWords(target.takeIf { menu }) {
                BubbleFold(foldText, foldKey, own = own) { shownText ->
                    val displayParts = remember(parts, foldText, shownText) {
                        if (shownText === foldText) parts else listOf(MessagePart.Text(shownText))
                    }
                    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                        MessageParts(displayParts, true, items.firstOrNull()?.id.orEmpty(), onLastLine)
                        if (streaming) StreamingCursor()
                    }
                }
            }
        } else if (openingSource != null) Box(Modifier.align(Alignment.End)) { footer?.invoke() }
    }
}

@Composable
internal fun ThoughtContent(text: String, id: String, collapsed: Boolean, last: Boolean = true) {
    var open by rememberSaveable(id) { mutableStateOf(!collapsed) }
    // A thought in a work step fades its text (ThinkingRow chip, MessageViews.swift:778, ChatView.swift:7388).
    // The BeautifulUI trace (MessageViews.swift:791) does not pass unfold, and this row is not that trace.
    val edge = rememberOpenEdge(id)
    val revealing = edge.revealing(open)
    Column {
        val thought = stringResource(R.string.chat_work_thought)
        TimelineStep(PhIcons.Lightbulb, thought, verb = thought, last = last, onClick = { open = !open })
        if (open) Column(Modifier.unfolding(revealing)) {
            Column(Modifier.workDetail(Tokens.Space.m)) {
                ChatMarkdown(text, id, PersonaTheme.type.callout, PersonaTheme.palette.ink2)
            }
            Box(Modifier.workDetail()) { ReaderEntry(text, thought) }
        }
    }
}

internal fun toolCategory(name: String) = when (val base = persona.core.domain.toolBaseName(name)) {
    "Read", "LS", "NotebookRead" -> PhIcons.Eye
    "Edit", "MultiEdit", "NotebookEdit" -> PhIcons.PencilSimple
    "Write" -> PhIcons.File
    "Bash", "PowerShell" -> PhIcons.TerminalWindow
    "Grep", "Glob", "ToolSearch" -> PhIcons.MagnifyingGlass
    "WebFetch", "WebSearch" -> PhIcons.GlobeSimple
    "Task", "Agent" -> PhIcons.Robot
    "TodoWrite", "TaskCreate", "TaskUpdate", "TaskList", "TaskGet" -> PhIcons.ListChecks
    "EnterPlanMode", "ExitPlanMode" -> PhIcons.Lightbulb
    // Persona's own tools (iOS ToolPresentation.symbol); PersonaTools knows the rest of the mcp__ ones.
    "conversation_rename" -> PhIcons.PencilSimple
    "conversation_archive", "conversation_unarchive" -> PhIcons.Archive
    "conversations_list", "conversation_read", "conversation_new", "conversation_send" -> PhIcons.ChatCircle
    "computer_state", "computer_run" -> PhIcons.Desktop
    "browser_run", "browser_look" -> PhIcons.GlobeSimple
    else -> PersonaTools.icon(name) ?: when {
        base.startsWith("phone_") -> PhIcons.DeviceMobile
        base.startsWith("mail_") -> PhIcons.EnvelopeSimple
        else -> PhIcons.Wrench
    }
}

@Composable
internal fun ToolCategoryIcon(name: String) {
    JIcon(toolCategory(name), null, size = 18.dp, tint = PersonaTheme.palette.ink2)
}

@Composable
internal fun DiffStats(diff: ToolDiff) {
    val palette = PersonaTheme.palette
    Row(horizontalArrangement = Arrangement.spacedBy(6.dp.dense)) {
        Text("+${diff.added}", color = palette.diffAdd, style = PersonaTheme.type.caption, maxLines = 1)
        Text("−${diff.removed}", color = palette.diffDel, style = PersonaTheme.type.caption, maxLines = 1)
    }
}

/** A line's code with the words that changed on [word] (tabs widened per piece, so the ranges still fit). */
internal fun diffText(line: DiffLine, word: Color): AnnotatedString = buildAnnotatedString {
    for (piece in LineDiff.pieces(line.text, line.changed)) {
        val text = piece.text.replace("\t", "    ")
        if (piece.changed) withStyle(SpanStyle(background = word)) { append(text) } else append(text)
    }
}

/** How many rows a long diff shows until Show All; unchanged runs fold into one row each before the cap. */
private const val DiffPreviewRows = 40

/** The old and new line numbers' column. */
private val DiffNumberColumn = 64.dp

@Composable
internal fun DiffView(diff: ToolDiff, id: String) {
    var expanded by rememberSaveable(id) { mutableStateOf(false) }
    val list = rememberLazyListState()
    val preview = remember(diff) { diff.folded().take(DiffPreviewRows) }
    // Something is left out: rows past the cap, or a folded run (a fold always takes away a row).
    val clipped = preview.size < diff.lines.size
    val rows = remember(diff, expanded, preview) {
        if (expanded) diff.lines.mapIndexed { index, line -> DiffRow.Line(index, line) } else preview
    }
    val palette = PersonaTheme.palette
    val horizontal = rememberScrollState()
    val measurer = rememberTextMeasurer()
    val style = PersonaTheme.type.code
    val density = LocalDensity.current
    val longest = remember(rows) {
        rows.filterIsInstance<DiffRow.Line>().maxByOrNull { it.line.text.length }?.line?.text.orEmpty().replace("\t", "    ")
    }
    val measured = remember(longest, style, density) { measurer.measure(longest, style, softWrap = false).size.width }
    Column(Modifier.clip(PersonaTheme.shapes.controlShape).background(palette.sunken)) {
        Column {
            DiffNavigation(diff, list, expanded) { expanded = true }
            BoxWithConstraints(Modifier.fillMaxWidth()) {
                // The sign column as on iOS: dotXL wide, growing with the font, then sm before the code.
                val signColumn = Tokens.Size.dotXL * density.fontScale + Tokens.Space.sm
                val codeWidth = maxOf(maxWidth - DiffNumberColumn - signColumn, with(density) { measured.toDp() } + Tokens.Space.s)
                // Keyed by the line's place in the whole diff, so rows stay put when the diff grows or Show All opens.
                LazyColumn(Modifier.fillMaxWidth().heightIn(max = 360.dp), state = list) {
                    items(rows, key = { row -> when (row) { is DiffRow.Line -> "l${row.index}"; is DiffRow.Fold -> "f${row.from}" } }) { row ->
                        when (row) {
                            is DiffRow.Fold -> Text(pluralStringResource(R.plurals.chat_diff_unchanged_lines, row.count, row.count),
                                Modifier.fillMaxWidth().background(palette.sunken).padding(start = DiffNumberColumn + signColumn),
                                style = style, color = palette.ink3, maxLines = 1)
                            is DiffRow.Line -> DiffCodeLine(row.line, style, codeWidth, signColumn, horizontal, tinted = diff.foldsContext)
                        }
                    }
                }
            }
            if (clipped) JTextButton(if (expanded) stringResource(R.string.chat_content_collapse)
                else pluralStringResource(R.plurals.chat_content_show_all_lines, diff.lines.size, diff.lines.size), { expanded = !expanded })
        }
    }
}

/**
 * One line of a diff on iOS's line model: its numbers and sign, then the code; with [tinted] (an edit's own comparison, not a
 * Codex patch) an added or removed line on its own tint, the words that changed deeper still. Only the code is selectable, and a screen reader hears "added" or "removed" for the sign.
 */
@Composable
private fun DiffCodeLine(line: DiffLine, style: TextStyle, codeWidth: Dp, signColumn: Dp, horizontal: ScrollState, tinted: Boolean) {
    val palette = PersonaTheme.palette
    val density = LocalDensity.current
    // The palette's diff tints (iOS Theme.Diff); a Codex patch keeps its plain rows, as before.
    val fill = if (!tinted) null else when (line.sign) { '+' -> palette.diffAddFill; '-' -> palette.diffDelFill; else -> null }
    val word = if (!tinted) Color.Unspecified else when (line.sign) { '+' -> palette.diffAddEmphasis; '-' -> palette.diffDelEmphasis; else -> Color.Unspecified }
    val text = remember(line, word) { diffText(line, word) }
    val state = when (line.sign) {
        '+' -> stringResource(R.string.chat_diff_line_added)
        '-' -> stringResource(R.string.chat_diff_line_removed)
        else -> null
    }
    Row(Modifier.fillMaxWidth().background(palette.sunken).let { if (fill != null) it.background(fill) else it }, verticalAlignment = Alignment.CenterVertically) {
        DisableSelection {
            Text("${line.old ?: ""}  ${line.new ?: ""}", Modifier.width(DiffNumberColumn), style = style, color = palette.ink2, maxLines = 1)
            // A hunk header or a note of a unified diff ('@') reads quietly, with no sign.
            Text(if (line.sign == '@') " " else line.sign.toString(),
                Modifier.padding(end = Tokens.Space.sm).width(Tokens.Size.dotXL * density.fontScale)
                    .semantics { state?.let { contentDescription = it } }, style = style,
                color = when (line.sign) { '+' -> palette.diffAdd; '-' -> palette.diffDel; else -> palette.ink2 })
        }
        Box(Modifier.weight(1f).horizontalScroll(horizontal)) {
            Text(text, Modifier.width(codeWidth), style = style, softWrap = false,
                color = if (line.sign == '@') palette.ink3 else palette.ink)
        }
    }
}

@Composable
internal fun TodoList(todos: List<TodoEntry>, failed: Boolean) {
    Column(verticalArrangement = Arrangement.spacedBy(6.dp.dense)) {
        val progress = pluralStringResource(R.plurals.chat_todos_progress, todos.size, todos.count { it.status == "completed" }, todos.size)
        Text(if (failed) stringResource(R.string.chat_todos_progress_failed, progress) else progress,
            style = PersonaTheme.type.caption, color = if (failed) PersonaTheme.palette.danger.text else PersonaTheme.palette.ink2)
        todos.forEach { todo ->
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                val label = when (todo.status) {
                    "completed" -> stringResource(R.string.chat_todos_done)
                    "in_progress" -> stringResource(R.string.chat_todos_in_progress)
                    "pending" -> stringResource(R.string.chat_todos_pending)
                    else -> stringResource(R.string.chat_todos_unknown)
                }
                JCheckbox(todo.status == "completed", onCheckedChange = null)
                Column(Modifier.weight(1f)) {
                    Text(todo.content, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink)
                    Text(label, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2)
                    if (todo.status == "in_progress" && !todo.activeForm.isNullOrBlank() && todo.activeForm != todo.content)
                        Text(todo.activeForm, style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2)
                }
            }
        }
    }
}
