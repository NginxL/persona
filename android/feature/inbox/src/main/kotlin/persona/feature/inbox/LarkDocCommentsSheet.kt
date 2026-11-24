package persona.feature.inbox

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.inbox.*

/**
 * Every comment thread of a document: the quoted text and its replies; open threads on one tab, resolved ones on the
 * other (iOS FeishuDocsCommentsView, web DocComments). Where the hub's Feishu plugin can write comments ([writes],
 * `lark_doc_comments`) each thread has "Reply" and "Resolve" / "Reopen", and the box at the bottom comments on the whole
 * document, answers the thread picked, or comments on the passage the reader sent here; typing @ finds people.
 */
@Composable
internal fun LarkDocCommentsSheet(model: LarkDocCommentsModel, writes: Boolean, onDismiss: () -> Unit) {
    val scope = rememberCoroutineScope()
    var resolvedTab by remember { mutableStateOf(false) }
    LaunchedEffect(model) { if (model.threads == null) model.load() }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.inbox_lark_doc_comments_title), scrollable = false) {
        LarkDocCommentsContent(model, writes, resolvedTab, { resolvedTab = it }, Modifier.weight(1f, fill = false)) { scope.launch { it() } }
    }
}

/** The sheet's body, apart from the sheet (screenshots draw it on a page). [run] launches a model call. */
@Composable
internal fun LarkDocCommentsContent(model: LarkDocCommentsModel, writes: Boolean, resolvedTab: Boolean, onTab: (Boolean) -> Unit,
    modifier: Modifier = Modifier, run: (suspend () -> Unit) -> Unit,
) {
    val shown = model.threads(resolvedTab)
    Column(Modifier.fillMaxWidth()) {
        JSegmented(listOf(JSegment(stringResource(R.string.inbox_lark_doc_comments_open), model.threads(false).size),
            JSegment(stringResource(R.string.inbox_lark_doc_comments_resolved), model.threads(true).size)),
            if (resolvedTab) 1 else 0, { onTab(it == 1) }, Modifier.padding(horizontal = pagePadding))
        LazyColumn(modifier.fillMaxWidth(), contentPadding = PaddingValues(horizontal = pagePadding, vertical = Tokens.Space.m),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            when {
                model.threads == null && model.loadFailed -> item("failed") {
                    JNotice(stringResource(R.string.inbox_lark_doc_comments_load_failed), status = Status.NeedsYou,
                        actionLabel = stringResource(R.string.inbox_retry), onAction = { run { model.load() } })
                }
                model.threads == null -> item("loading") { JLoading() }
                shown.isEmpty() -> item("empty") {
                    JEmptyState(stringResource(if (resolvedTab) R.string.inbox_lark_doc_comments_none_resolved else R.string.inbox_lark_doc_comments_none_open),
                        stringResource(when {
                            resolvedTab -> R.string.inbox_lark_doc_comments_none_resolved_text
                            writes -> R.string.inbox_lark_doc_comments_none_open_write
                            else -> R.string.inbox_lark_doc_comments_none_open_read
                        }), icon = PhIcons.ChatCircle)
                }
                else -> items(shown, key = { it.id }) { thread ->
                    LarkDocThreadCard(thread, replying = (model.target as? LarkDocCommentTarget.Thread)?.id == thread.id,
                        actions = if (!writes) null else LarkDocThreadActions(resolving = thread.id in model.resolving,
                            reply = { model.reply(thread) }, setResolved = { r -> run { model.setResolved(thread, r) } }))
                }
            }
        }
        if (writes) LarkDocCommentComposer(model, run)
    }
}

internal class LarkDocThreadActions(val resolving: Boolean, val reply: () -> Unit, val setResolved: (Boolean) -> Unit)

@Composable
private fun LarkDocThreadCard(thread: LarkDocCommentThread, replying: Boolean, actions: LarkDocThreadActions?) {
    val palette = PersonaTheme.palette
    JCard(Modifier.fillMaxWidth().testTag("docComments.thread.${thread.id}"), padding = Tokens.Space.m,
        status = if (replying) Status.Working else null) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                if (thread.whole || thread.quote.isBlank()) Text(stringResource(R.string.inbox_lark_doc_comments_whole), Modifier.weight(1f),
                    style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1)
                else Text(thread.quote, Modifier.weight(1f), style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 2,
                    overflow = TextOverflow.Ellipsis)
                if (thread.resolved) JTag(stringResource(R.string.inbox_lark_doc_comments_resolved), tone = Tone.Positive)
                if (thread.partial) JTag(stringResource(R.string.inbox_lark_doc_comments_partial))
            }
            thread.replies.forEach { reply -> LarkDocReplyRow(reply) }
            if (actions != null) Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                if (!thread.resolved) JTextButton(stringResource(R.string.inbox_lark_doc_comments_reply), actions.reply,
                    Modifier.testTag("docComments.reply.${thread.id}"), icon = PhIcons.ArrowBendUpLeft)
                JTextButton(stringResource(if (thread.resolved) R.string.inbox_lark_doc_comments_reopen else R.string.inbox_lark_doc_comments_resolve),
                    { actions.setResolved(!thread.resolved) }, Modifier.testTag("docComments.resolve.${thread.id}"), enabled = !actions.resolving,
                    icon = if (thread.resolved) PhIcons.ArrowUUpLeft else PhIcons.CheckCircle)
            }
        }
    }
}

@Composable
private fun LarkDocReplyRow(reply: LarkDocCommentReply) {
    val palette = PersonaTheme.palette
    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        JAvatar(reply.author, size = Tokens.Space.xl)
        Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                Text(reply.author, Modifier.weight(1f, fill = false), style = PersonaTheme.type.headline, color = palette.ink, maxLines = 1,
                    overflow = TextOverflow.Ellipsis)
                reply.createdAt?.let { Text(larkShortDate(it, currentLocale), style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1) }
                if (reply.edited) Text(stringResource(R.string.inbox_lark_edited), style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
            }
            val text = buildAnnotatedString {
                reply.content.forEach { s ->
                    if (s.kind == "mention" || s.kind == "link") withStyle(SpanStyle(color = palette.accent)) { append(s.text) } else append(s.text)
                }
            }
            SelectionContainer { Text(text, style = PersonaTheme.type.callout, color = palette.ink) }
            if (reply.imageCount > 0) Text(pluralStringResource(R.plurals.inbox_lark_doc_comments_images, reply.imageCount, reply.imageCount),
                style = PersonaTheme.type.micro, color = palette.ink2)
        }
    }
}

/** The box: where it goes ("Reply to <name>" / "Comment on this passage", × back to the whole document), @ suggestions, what went wrong. */
@Composable
internal fun LarkDocCommentComposer(model: LarkDocCommentsModel, run: (suspend () -> Unit) -> Unit) {
    val palette = PersonaTheme.palette
    val query = larkDocMentionQuery(model.draft)
    var people by remember { mutableStateOf<List<LarkDocPerson>>(emptyList()) }
    LaunchedEffect(query) {
        people = emptyList()
        if (query == null) return@LaunchedEffect
        delay(250)
        people = try { model.people(query).take(5) } catch (e: CancellationException) { throw e } catch (_: Exception) { emptyList() }
    }
    val target = model.target
    val focus = remember { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    // Blank space around the field types too, as in the chat composer (#894, #4093).
    ComposerFrame(Modifier.imePadding(), onBlankTap = { if (!model.sending) { focus.requestFocus(); keyboard?.show() } }, accessory = {
        when (target) {
            is LarkDocCommentTarget.Thread -> LarkDocTargetBar(stringResource(R.string.inbox_lark_doc_comments_replying_to, target.name), target.text,
                stringResource(R.string.inbox_lark_doc_comments_cancel_reply)) { model.target = LarkDocCommentTarget.Document }
            is LarkDocCommentTarget.Passage -> LarkDocTargetBar(stringResource(R.string.inbox_lark_doc_comments_on_passage), target.quote,
                stringResource(R.string.inbox_lark_doc_comments_cancel_passage)) { model.target = LarkDocCommentTarget.Document }
            LarkDocCommentTarget.Document -> Unit
        }
        people.forEach { person ->
            JRow(person.name, Modifier.testTag("docComments.person.${person.userId}"), leading = { JAvatar(person.name, size = Tokens.Space.xl) },
                onClick = { model.mention(person); people = emptyList() }, chevron = false)
        }
        model.failure?.let { f ->
            val reason = if (f.unsupported) stringResource(R.string.inbox_lark_doc_comments_unsupported)
                else f.reason ?: stringResource(R.string.inbox_lark_doc_comments_unreachable)
            Text(stringResource(f.action, reason), style = PersonaTheme.type.caption, color = palette.danger.dot, maxLines = 3,
                overflow = TextOverflow.Ellipsis)
        }
    }, trailing = {
        SendButton({ run { model.send() } }, Modifier.testTag("docComments.send"), enabled = model.canSend, loading = model.sending)
    }) {
        ComposerTextField(model.draft, { model.draft = it }, stringResource(when (target) {
            LarkDocCommentTarget.Document -> R.string.inbox_lark_doc_comments_placeholder_document
            is LarkDocCommentTarget.Thread -> R.string.inbox_lark_doc_comments_placeholder_reply
            is LarkDocCommentTarget.Passage -> R.string.inbox_lark_doc_comments_placeholder_passage
        }), Modifier.testTag("docComments.box").focusRequester(focus), enabled = !model.sending)
    }
}

@Composable
private fun LarkDocTargetBar(heading: String, summary: String, removeLabel: String, onRemove: () -> Unit) {
    val palette = PersonaTheme.palette
    Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Column(Modifier.weight(1f)) {
            Text(heading, style = PersonaTheme.type.caption, color = palette.accent, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (summary.isNotBlank()) Text(summary, style = PersonaTheme.type.micro, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        JIconButton(PhIcons.X, removeLabel, onRemove)
    }
}
