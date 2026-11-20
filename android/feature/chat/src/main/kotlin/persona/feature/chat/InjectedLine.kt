package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.flow.MutableStateFlow
import persona.core.contracts.Injected
import persona.core.contracts.Todo
import persona.core.designsystem.ChatHint
import persona.core.designsystem.JSheet
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.taskOfInjected
import persona.core.contracts.Item
import persona.core.contracts.MessageSource
import persona.core.designsystem.ContentTokens
import persona.core.designsystem.JTextButton
import persona.core.designsystem.Tokens
import persona.core.domain.injectedOf
import persona.core.domain.sameMessageSource
import persona.core.domain.sourceOf
import persona.core.domain.lines.ChatLine
import androidx.compose.ui.text.style.TextOverflow

/** Labels in reading order. Any intervening row ends the run, including an assistant reply. */
internal fun sourceLabelIds(lines: List<ChatLine>): Set<String> = buildSet {
    var previous: MessageSource? = null
    lines.forEach { line ->
        val source = (line as? ChatLine.Row)?.row?.item?.item?.let(::sourceOf)
        if (source != null && !sameMessageSource(previous, source)) add(line.id)
        previous = source
    }
}

@Composable
internal fun SourceMessage(item: Item, source: MessageSource, showLabel: Boolean = true,
    update: ChatTextUpdate? = null, images: (@Composable () -> Unit)? = null, hidePrompt: Boolean = false) {
    val n = item.injected ?: injectedOf(item)?.takeIf { source.kind in setOf("patrol", "followup", "supervision") }
    var whole by rememberSaveable(item.id) { mutableStateOf<String?>(null) }
    // #4314: while the main conversation folds background rounds, a scheduled task's or follow-up's prompt is never
    // shown, open or closed: only the hub's one line says which task ran (nothing more without one).
    val prompt = hidePrompt && source.kind in setOf("patrol", "followup")
    val text = if (prompt) n?.line.orEmpty() else whole ?: update?.text ?: n?.detail?.takeIf(String::isNotBlank) ?: n?.line?.takeIf(String::isNotBlank)
        ?: item.text.orEmpty().let { if (item.fullSize != null) cutText(it) else it }
    val model = remember(text, item.from) { messageModel(text, item.from) }
    val body = model.plain
    val uri = androidx.compose.ui.platform.LocalUriHandler.current
    val contexts = remember(model.contexts) { model.contexts.filterNot { it.kind == "message-source" || persona.core.domain.isRelaySourceContext(it) } }
    var expanded by rememberSaveable(item.id) { mutableStateOf(false) }
    val searchItem = LocalChatSearchItem.current
    LaunchedEffect(item.id, searchItem) {
        if (searchItem == item.id) expanded = true
    }
    var clipped by remember(item.id, body) { mutableStateOf(false) }
    val long = body.length > 320 || body.lineSequence().count() > ContentTokens.sourcePreviewLines
    val title = sourceTitle(source)
    val picking = LocalChatRowActions.current?.menu?.forwardPick?.selected != null
    val pickTarget = if (picking && !prompt) MessageTarget(item.id, body, user = false, raw = text,
        from = title, time = item.ts, cut = item.id.takeIf { item.fullSize != null && whole == null && update == null }) else null
    PickableMessageColumn(pickTarget, Modifier.fillMaxWidth().testTag("source-${item.id}"), Arrangement.spacedBy(Tokens.Space.xs)) {
        if (showLabel) Text(if (source.kind == "scheduled_task") title else stringResource(R.string.chat_source_label, title), Modifier.testTag("source-label-${item.id}"),
            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        MessageForwardContexts(contexts, "source/${item.id}/contexts", uri::openUri, alignEnd = false)
        images?.invoke()
        if (model.images.isNotEmpty()) AttachedPhotoRow(model.images)
        AttachedFileCards(model.attached, alignEnd = false)
        // A task's run without its one line: the label alone says which task ran.
        if (!prompt || body.isNotBlank()) {
            if (expanded) ReplyMarkdown(keepLineBreaks(body), "source/${item.id}", ownReply = false, selectable = true)
            else Text(messageLinkAnnotations(body), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2,
                maxLines = ContentTokens.sourcePreviewLines, overflow = TextOverflow.Ellipsis,
                onTextLayout = { clipped = it.hasVisualOverflow })
        }
        if (long || clipped || expanded) JTextButton(stringResource(if (expanded) R.string.chat_source_collapse else R.string.chat_source_expand),
            { expanded = !expanded }, modifier = Modifier.testTag("source-toggle-${item.id}"))
        if (expanded && !prompt && item.fullSize != null && whole == null) FullTextButton(item) { whole = it }
        FileChips(model.files, alignEnd = false)
    }
}

/** The name a source mention shows: its title, else the words for its kind. */
@Composable
internal fun sourceTitle(source: MessageSource): String = source.title?.takeIf(String::isNotBlank) ?: stringResource(when (source.kind) {
    "conversation" -> R.string.chat_source_conversation
    "patrol" -> R.string.chat_source_patrol
    "followup" -> R.string.chat_source_followup
    "scheduled_task" -> R.string.chat_source_scheduled_task
    "supervision" -> R.string.chat_source_supervision
    "peer" -> R.string.chat_content_other_session
    else -> R.string.chat_source_unknown
})

private val noTodos = MutableStateFlow(emptyList<Todo>())

/**
 * A message Persona typed in for the model (a task's notice, a scheduled run…; docs/protocol/protocol.md "Messages injected by Persona"):
 * one centered line, never its words. A task's notice opens the task (in the assistant's conversations); a scheduled
 * run's line shows what the task asks.
 */
@Composable
internal fun InjectedLine(id: String, n: Injected, showDetail: Boolean = true) {
    val host = LocalAssistantTaskHost.current
    val todos by (host?.tasks?.todos ?: noTodos).collectAsStateWithLifecycle()
    var open by rememberSaveable(id) { mutableStateOf(false) }
    val todo = host?.let { h -> taskOfInjected(n, todos, h.conversation)?.let { tid -> todos.firstOrNull { it.id == tid } } }
    val (icon, tint) = injectedIcon(n)
    val onClick: (() -> Unit)? = when {
        todo != null && host != null -> { { openTask(host, todo) } }
        showDetail && !n.detail.isNullOrBlank() -> { { open = true } }
        else -> null
    }
    ChatHint(n.line, icon, tint, onClick, Modifier.testTag("injected-${n.kind}"),
        onClickLabel = stringResource(if (todo != null) R.string.chat_injected_open_task else R.string.chat_injected_view_request))
    if (open) JSheet({ open = false }, title = n.title ?: n.line, scrollable = true) {
        ChatMarkdown(n.detail.orEmpty(), "injected/$id")
    }
}

@Composable
private fun injectedIcon(n: Injected): Pair<PhIcon, Color> {
    val palette = PersonaTheme.palette
    return when (n.kind) {
        "task_notice" -> when (n.state) {
            "needs" -> PhIcons.Hand to palette.needsYou.text
            "interrupted", "failed" -> PhIcons.WarningCircle to palette.danger.dot
            else -> PhIcons.CheckCircle to palette.positive
        }
        "followup" -> (if (n.state == "wake") PhIcons.BellRinging else PhIcons.Clock) to palette.accent
        else -> PhIcons.Info to palette.ink3
    }
}
