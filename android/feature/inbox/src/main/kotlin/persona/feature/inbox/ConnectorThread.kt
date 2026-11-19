package persona.feature.inbox

import android.text.format.Formatter
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import persona.core.contracts.api.ConnectorReplyOutcome
import persona.core.contracts.api.ConnectorThreadChat
import persona.core.contracts.api.ConnectorThreadMedia
import persona.core.contracts.api.ConnectorThreadMessage
import persona.core.contracts.api.ConnectorsManagement
import persona.core.designsystem.ComposerFrame
import persona.core.designsystem.ComposerTextField
import persona.core.designsystem.IncomingBubbleFrame
import persona.core.designsystem.InlineTone
import persona.core.designsystem.JAvatar
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JInlineStatus
import persona.core.designsystem.JNotice
import persona.core.designsystem.JPage
import persona.core.designsystem.JSpinner
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.SendButton
import persona.core.designsystem.TimelineMark
import persona.core.designsystem.Tokens
import persona.core.designsystem.UserBubbleFrame
import persona.core.designsystem.failureReasonText
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.text
import persona.core.navigation.Navigator

/**
 * A hub connector's inbox item opened as its conversation (#2726): the thread page the connector reads (oldest first,
 * older pages on demand), quotes that jump to what they answer, media as placeholders, and a reply box when the page
 * names a reply action. Nothing here knows which connector it is: the hub's words and the page's shape only.
 * A thread that is not a chat (a pull request, a notice) or that cannot be read falls back to the item's own page.
 */
@Composable
internal fun ConnectorThreadScreen(repository: InboxRepository, connectors: ConnectorsManagement, route: ConnectorThreadRoute, navigator: Navigator,
    pins: persona.core.contracts.api.ManagerPins? = null) {
    val inbox by repository.snapshot.collectAsStateWithLifecycle()
    val record = inbox.records.inboxRecord(route.itemId)
    val ref = record?.connector
    if (record == null || ref == null || ref.thread.isBlank()) {
        InboxItemDetailScreen(repository, InboxItemDetailRoute(route.itemId), navigator)
        return
    }
    val model = remember(record.source, ref.thread, pins) {
        ConnectorThreadModel(connectors, record.source, ref.thread, pins ?: kotlin.coroutines.EmptyCoroutineContext)
    }
    val state by model.state.collectAsStateWithLifecycle()
    // Again when the item moves (a new message came in).
    LaunchedEffect(model, record.item.updatedAt) { model.load() }
    val page = state.page
    when {
        !state.loaded && page == null -> JPage(record.item.title, onBack = navigator::back) { padding ->
            Box(Modifier.fillMaxSize().padding(padding), contentAlignment = Alignment.Center) { JSpinner() }
        }
        page == null -> ConnectorThreadUnread(record, state.failure, navigator)
        !page.chat.isChat -> InboxItemDetailScreen(repository, InboxItemDetailRoute(route.itemId), navigator)
        else -> ConnectorChat(record, model, state, navigator, inbox.contentSource?.transport?.replyImages)
    }
}

/** The thread could not be read: why, and what the hub collected about the item. */
@Composable
private fun ConnectorThreadUnread(record: InboxRecord, failure: Throwable?, navigator: Navigator) {
    JPage(record.item.title, onBack = navigator::back) { padding ->
        LazyColumn(Modifier.fillMaxSize(), contentPadding = padding) {
            failure?.let { error ->
                item { JNotice(stringResource(R.string.inbox_connector_thread_failed, failureReasonText(error).text()), Modifier.padding(Tokens.Space.l)) }
            }
            item {
                Text(record.item.summary?.takeIf { it.isNotBlank() } ?: record.item.preview, style = PersonaTheme.type.body,
                    color = PersonaTheme.palette.ink, modifier = Modifier.padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s))
            }
        }
    }
}

@Composable
private fun ConnectorChat(record: InboxRecord, model: ConnectorThreadModel, state: ConnectorThreadState, navigator: Navigator,
    imageSource: persona.core.contracts.api.ReplyImageSource?) {
    val page = state.page ?: return
    val scope = rememberCoroutineScope()
    val list = rememberLazyListState()
    val reduceMotion = PersonaTheme.reduceMotion
    var replyTo by remember { mutableStateOf<ConnectorThreadMessage?>(null) }
    var text by remember { mutableStateOf("") }
    var lit by remember { mutableStateOf<String?>(null) }
    var problem by remember { mutableStateOf<String?>(null) }
    val notNear = stringResource(R.string.inbox_connector_quote_not_found)
    val olderFailed = stringResource(R.string.inbox_connector_older_failed)
    val context = LocalContext.current
    // The list has one row before the messages (older / the thread's start): a message's row is its index plus one.
    fun jump(id: String) {
        scope.launch {
            if (!model.reach(id)) { problem = notNear; return@launch }
            val index = model.state.value.page?.messages?.indexOfFirst { it.id == id }?.takeIf { it >= 0 } ?: return@launch
            problem = null
            if (reduceMotion) list.scrollToItem(index + 1) else list.animateScrollToItem(index + 1)
            lit = id
            delay(1600)
            if (lit == id) lit = null
        }
    }
    // The newest message in view when the page first comes, and after the user's own reply.
    LaunchedEffect(page.messages.lastOrNull()?.id) {
        if (lit == null && page.messages.isNotEmpty()) list.scrollToItem(page.messages.size)
    }
    JPage(
        title = page.chat.name.ifBlank { record.item.title },
        subtitle = chatHeadline(page.chat) ?: record.sourceLabel.takeIf { it.isNotBlank() },
        onBack = navigator::back,
        bottomBar = if (page.chat.reply.isNullOrBlank()) null else ({
            ConnectorComposer(text, { text = it }, replyTo, { replyTo = null }, state, record.connector?.replyTo) {
                val sending = text
                val quoting = replyTo
                scope.launch {
                    val outcome = model.reply(sending, quoting)
                    if (outcome is ConnectorReplyOutcome.Sent) { text = ""; replyTo = null }
                }
            }
        }),
    ) { padding ->
        LazyColumn(Modifier.fillMaxSize(), state = list, contentPadding = padding, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            item(key = "start") {
                Column(Modifier.fillMaxWidth().padding(horizontal = Tokens.Space.l), horizontalAlignment = Alignment.CenterHorizontally) {
                    if (!page.before.isNullOrEmpty()) {
                        if (state.older) JSpinner(Modifier.padding(Tokens.Space.s))
                        else JTextButton(stringResource(R.string.inbox_connector_older), onClick = {
                            scope.launch { model.older()?.let { problem = olderFailed.format(failureReasonText(it).resolve(context)) } }
                        })
                    } else if (page.messages.isEmpty()) {
                        Text(stringResource(R.string.inbox_connector_empty), style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
                            modifier = Modifier.padding(vertical = Tokens.Space.xl))
                    }
                    problem?.let { JNotice(it, Modifier.padding(vertical = Tokens.Space.s)) }
                    if (page.chat.reply.isNullOrBlank()) {
                        Text(stringResource(R.string.inbox_connector_read_only), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3,
                            modifier = Modifier.padding(vertical = Tokens.Space.xs))
                    }
                }
            }
            items(page.messages, key = { it.id }) { message ->
                ConnectorMessageRow(message, imageSource, lit = message.id == lit, canReply = !page.chat.reply.isNullOrBlank(),
                    onQuote = { id -> jump(id) }, onReply = { replyTo = message })
            }
        }
    }
}

/** The line under the title: "Group · 12 people", "Channel · 340 subscribers"; null for a one-to-one chat (the title says who). */
@Composable
private fun chatHeadline(chat: ConnectorThreadChat): String? {
    val n = chat.members
    return when (connectorChatKind(chat.kind)) {
        ConnectorChatKind.Group -> if (n != null && n > 0) pluralStringResource(R.plurals.inbox_connector_group_members, n, n)
            else stringResource(R.string.inbox_connector_group)
        ConnectorChatKind.Channel -> if (n != null && n > 0) pluralStringResource(R.plurals.inbox_connector_channel_members, n, n)
            else stringResource(R.string.inbox_connector_channel)
        else -> null
    }
}

@Composable
private fun ConnectorMessageRow(
    message: ConnectorThreadMessage,
    imageSource: persona.core.contracts.api.ReplyImageSource?,
    lit: Boolean,
    canReply: Boolean,
    onQuote: (String) -> Unit,
    onReply: () -> Unit,
) {
    val palette = PersonaTheme.palette
    if (message.event) {
        TimelineMark(message.text.orEmpty(), Modifier.padding(horizontal = Tokens.Space.l))
        return
    }
    val time = messageTime(message.at)
    val footer: @Composable () -> Unit = {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (message.mine && time.isNotEmpty()) Text(time, style = PersonaTheme.type.micro, color = palette.ink3)
            if (message.edited) Text(stringResource(R.string.inbox_connector_edited), style = PersonaTheme.type.micro, color = palette.ink3)
            if (canReply) JTextButton(stringResource(R.string.inbox_connector_reply), onClick = onReply, icon = PhIcons.ArrowBendUpLeft)
        }
    }
    val body: @Composable () -> Unit = {
        message.replyTo?.takeIf { it.id.isNotBlank() || !it.text.isNullOrBlank() }?.let { quote ->
            QuoteBlock(quote.from, quote.text, onClick = quote.id.takeIf { it.isNotBlank() }?.let { { onQuote(it) } })
            Spacer(Modifier.height(Tokens.Space.s))
        }
        message.media.forEach { MediaPlaceholder(it) }
        message.text?.takeIf { it.isNotBlank() }?.let { text ->
            InboxPictureText(text, imageSource)
        }
    }
    val highlight = if (lit) Modifier.background(palette.statusSurface) else Modifier
    Box(highlight.padding(horizontal = Tokens.Space.l)) {
        if (message.mine) UserBubbleFrame(footer = footer) { body() }
        else IncomingBubbleFrame(
            author = message.sender.name.ifBlank { null }, time = time.ifEmpty { null },
            avatar = { JAvatar(message.sender.name.ifBlank { "?" }, size = Tokens.Space.xxl) }, footer = footer,
        ) { body() }
    }
}

/** What a message answers: who and a line of it, set off by a rule; a tap goes to it when it has an id. */
@Composable
private fun QuoteBlock(from: String?, text: String?, onClick: (() -> Unit)?) {
    val palette = PersonaTheme.palette
    val line = listOfNotNull(from?.takeIf { it.isNotBlank() }, text?.takeIf { it.isNotBlank() }).joinToString(": ")
    val label = stringResource(R.string.inbox_connector_quote_jump, line)
    Row(
        Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.badge)).background(palette.sunken)
            .then(if (onClick != null) Modifier.clickable(role = Role.Button, onClickLabel = label, onClick = onClick).semantics { contentDescription = label } else Modifier)
            .padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Box(Modifier.width(Tokens.Space.xxs).height(Tokens.Space.xl).background(palette.line))
        Spacer(Modifier.width(Tokens.Space.s))
        JIcon(PhIcons.Quotes, null, size = Tokens.Space.l, tint = palette.ink3)
        Spacer(Modifier.width(Tokens.Space.s))
        Text(line, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

/** A media part, described rather than downloaded: its kind, name, size and length. */
@Composable
private fun MediaPlaceholder(media: ConnectorThreadMedia) {
    val palette = PersonaTheme.palette
    val context = LocalContext.current
    val (icon, kind) = mediaKind(media.kind)
    val parts = listOfNotNull(
        media.emoji?.takeIf { it.isNotBlank() },
        stringResource(kind),
        media.name?.takeIf { it.isNotBlank() },
        media.size?.takeIf { it > 0 }?.let { Formatter.formatShortFileSize(context, it) },
        media.duration?.takeIf { it > 0 }?.let { pluralStringResource(R.plurals.inbox_connector_seconds, it, it) },
    )
    Row(
        Modifier.padding(vertical = Tokens.Space.xxs).clip(RoundedCornerShape(Tokens.Radius.badge)).background(palette.sunken).padding(horizontal = Tokens.Space.s, vertical = Tokens.Space.s),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        JIcon(icon, null, size = Tokens.Space.l, tint = palette.ink2)
        Spacer(Modifier.width(Tokens.Space.s))
        Text(parts.joinToString(" · "), style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
    }
}

private fun mediaKind(kind: String): Pair<PhIcon, Int> = when (kind) {
    "image", "photo" -> PhIcons.Image to R.string.inbox_connector_media_image
    "sticker" -> PhIcons.Image to R.string.inbox_connector_media_sticker
    "video" -> PhIcons.Play to R.string.inbox_connector_media_video
    "voice", "audio" -> PhIcons.Microphone to R.string.inbox_connector_media_voice
    "file" -> PhIcons.File to R.string.inbox_connector_media_file
    else -> PhIcons.Paperclip to R.string.inbox_connector_media_other
}

@Composable
internal fun ConnectorComposer(
    text: String,
    onText: (String) -> Unit,
    replyTo: ConnectorThreadMessage?,
    onClearReply: () -> Unit,
    state: ConnectorThreadState,
    goesTo: String?,
    onSend: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val focus = remember { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    ComposerFrame(
        // Blank space around the field types too, as in the chat composer (#894, #4093).
        onBlankTap = { if (!state.sending) { focus.requestFocus(); keyboard?.show() } },
        accessory = {
            replyStatus(state)?.let { (line, tone) -> JInlineStatus(line, Modifier.padding(bottom = Tokens.Space.s), tone = tone) }
            replyTo?.let { quoted ->
                Row(verticalAlignment = Alignment.CenterVertically) {
                    JIcon(PhIcons.ArrowBendUpLeft, null, size = Tokens.Space.l, tint = palette.ink3)
                    Spacer(Modifier.width(Tokens.Space.s))
                    Text(stringResource(R.string.inbox_connector_replying_to, quoted.sender.name.ifBlank { quoted.text.orEmpty() }),
                        style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis,
                        modifier = Modifier.weight(1f))
                    JIconButton(PhIcons.X, stringResource(R.string.inbox_connector_cancel_reply), onClick = onClearReply)
                }
            }
            goesTo?.takeIf { it.isNotBlank() && replyTo == null && state.outcome == null && !state.sending }?.let {
                Text(stringResource(R.string.inbox_connector_reply_goes, it), style = PersonaTheme.type.micro, color = palette.ink3,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
        },
        trailing = {
            SendButton(onClick = onSend, enabled = text.isNotBlank() && !state.sending, loading = state.sending)
        },
    ) {
        ComposerTextField(text, onText, stringResource(R.string.inbox_connector_placeholder), Modifier.focusRequester(focus), enabled = !state.sending)
    }
}

/**
 * How the reply is going, as the hub said it: sending (the hub's gate may wait for the user to approve it on the phone),
 * sent with the connector's words, not sent because it was turned down, or failed and why.
 */
@Composable
private fun replyStatus(state: ConnectorThreadState): Pair<String, InlineTone>? = when (val outcome = state.outcome) {
    null -> if (state.sending) stringResource(R.string.inbox_connector_sending) to InlineTone.Busy else null
    is ConnectorReplyOutcome.Sent -> (outcome.note?.let { stringResource(R.string.inbox_connector_sent_note, it) }
        ?: stringResource(R.string.inbox_connector_sent)) to InlineTone.Done
    is ConnectorReplyOutcome.Declined -> stringResource(R.string.inbox_connector_declined, outcome.reason) to InlineTone.Caution
    is ConnectorReplyOutcome.Failed -> stringResource(R.string.inbox_connector_failed,
        state.replyError?.let { failureReasonText(it).text() } ?: outcome.reason) to InlineTone.Failed
}

@Composable
private fun messageTime(at: String): String {
    val context = LocalContext.current
    return remember(at) {
        runCatching { LocalizedFormats.format(java.time.Instant.parse(at), LocalizedFormats.MONTH_DAY_TIME, LocalizedFormats.locale(context)) }
            .getOrDefault("")
    }
}

