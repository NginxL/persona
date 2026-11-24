package persona.feature.inbox

import androidx.compose.ui.res.stringResource
import androidx.compose.ui.res.pluralStringResource

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.designsystem.*
import kotlinx.coroutines.launch
import persona.core.inbox.*

@Composable
internal fun LarkThreadSheet(root: LarkMessage, repository: InboxRepository, mentionFeed: String? = null, onDismiss: () -> Unit) {
    val loader = remember(root.id, root.thread) { root.thread?.let { LarkThreadLoader(root.id, it) } }
    val scope = rememberCoroutineScope()
    val replies = remember(root.id, root.thread, repository) {
        root.thread?.takeIf { it.canReply }?.let { LarkThreadReplyRepository(repository, root.id, it) }
    }
    val composers = rememberReplyComposers(repository)
    val connection by repository.connectionState.collectAsStateWithLifecycle(persona.core.contracts.api.ConnectionState.Connecting)
    val onlineSince = rememberOnlineSince(connection)
    val composer = replies?.let { composers.get(it.target, replyRepository = it) }
    val scroll = rememberLazyListState()
    var mediaError by remember(root.id) { mutableStateOf<String?>(null) }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.inbox_lark_thread)) {
        Column(Modifier.fillMaxWidth().fillMaxHeight(.92f)) {

            if (loader == null) {
                JEmptyState(stringResource(R.string.inbox_lark_thread_unavailable_title), stringResource(R.string.inbox_lark_thread_unavailable_text))
            } else {
                val state by loader.state.collectAsStateWithLifecycle()
                LaunchedEffect(loader) { loader.load() }
                LaunchedEffect(state.revision) { if (state.revision > 0) scroll.scrollToItem(0) }
                if (state.loading) JChip(stringResource(R.string.inbox_lark_thread_loading), status = Status.Working)
                state.error?.let { error ->
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.weight(1f)) { StatusText(stringResource(error), isError = true) }
                        JButton(stringResource(R.string.inbox_retry), enabled = !state.loading, onClick = {
                            scope.launch { loader.load(state.failedOlder) }
                        })
                    }
                }
                mediaError?.let { StatusText(it, isError = true) }
                LazyColumn(Modifier.weight(1f).fillMaxWidth(), state = scroll, reverseLayout = true,
                    contentPadding = PaddingValues(Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                    if (state.loaded && state.messages.isEmpty()) item("empty") {
                        JEmptyState(stringResource(R.string.inbox_lark_thread_empty_title), stringResource(R.string.inbox_lark_thread_empty_text))
                    }
                    items(state.messages.asReversed(), key = { "reply:${it.id}" }) { message ->
                        LarkBubble(message, true, repository, onlineSince, retrying = false, onRetry = {}, onError = { mediaError = it })
                    }
                    if (state.hasOlder) item("older") {
                        JButton(stringResource(if (state.loading) R.string.inbox_lark_loading else R.string.inbox_lark_thread_older),
                            modifier = Modifier.fillMaxWidth(), enabled = !state.loading,
                            onClick = { scope.launch { loader.load(older = true) } })
                    }
                    item("root") {
                        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                            LarkBubble(root, true, repository, onlineSince, retrying = false, onRetry = {}, onError = { mediaError = it })
                            JDivider()
                            Text(pluralStringResource(R.plurals.inbox_lark_replies, root.replyCount.toQuantity(), root.replyCount), style = PersonaTheme.type.callout,
                                color = PersonaTheme.palette.ink3)
                        }
                    }
                }
                Box(Modifier.align(Alignment.End)) {
                    if (!state.loading) JumpToLatest(0, onClick = { scope.launch { loader.load() } })
                }
            }
            Column(Modifier.fillMaxWidth().imePadding(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                if (composer == null) {
                    StatusText(stringResource(R.string.inbox_lark_thread_no_reply))
                } else {
                    val reply by composer.state.collectAsStateWithLifecycle()
                    LaunchedEffect(reply.sent) { if (reply.sent) loader?.load() }
                    if (reply.sent) StatusText(stringResource(R.string.inbox_sent))
                    LarkReplyComposerField(repository, composer, reply, mentionFeed, composer::send,
                        stringResource(R.string.inbox_lark_thread_reply))
                }
            }
        }
    }
}
