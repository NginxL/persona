package persona.feature.inbox

import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.viewmodel.compose.viewModel
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import persona.core.contracts.api.ConnectionState
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JListPage
import persona.core.designsystem.JLoadable
import persona.core.designsystem.JLoading
import persona.core.designsystem.JNotice
import persona.core.designsystem.MessageItem
import persona.core.designsystem.MessageItemTokens
import persona.core.designsystem.MessageSource
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.isRefreshing
import persona.core.designsystem.jLoadableContent
import persona.core.designsystem.text
import persona.core.navigation.LarkMessagesRoute
import persona.core.navigation.Navigator
import persona.core.inbox.*

/** The optional [LarkMessagesRoute] page: every direct and group chat, not another chat implementation. */
@Composable
fun LarkMessagesScreen(repository: LarkMessagesRepository, connection: ConnectionState, navigator: Navigator) {
    val model: LarkMessagesViewModel = viewModel(key = "lark-messages:${repository.scope}") {
        LarkMessagesViewModel(repository)
    }
    LarkMessagesContent(
        state = model.state,
        connection = connection,
        onRefresh = model::refresh,
        onLoadMore = model::loadMore,
        avatar = model::avatar,
        onOpen = { row -> navigator.navigate(LarkRoute(row.id, fromMessages = true)) },
    )
}

@Composable
internal fun LarkMessagesContent(
    state: StateFlow<LarkMessagesState>,
    connection: ConnectionState,
    onRefresh: () -> Unit,
    onLoadMore: () -> Unit,
    avatar: (String?) -> AuthenticatedLarkImage?,
    onOpen: (LarkChatSummary) -> Unit,
) {
    val snapshot by state.collectAsStateWithLifecycle()
    val listState = rememberLazyListState()
    val resources = LocalResources.current
    val onlineSince = rememberOnlineSince(connection)
    val loadingText = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_loading)
    val failure = snapshot.failure?.let { stringResourceLarkMessagesFailure(it) }
    val loadable: JLoadable<List<LarkChatSummary>> = when {
        !snapshot.loaded && snapshot.failure != null -> JLoadable.Failed(failure.orEmpty())
        !snapshot.loaded -> JLoadable.Loading
        else -> JLoadable.Loaded(snapshot.rows, refreshing = snapshot.loading, failure = failure)
    }

    JListPage(
        title = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_title),
        modifier = Modifier.testTag("lark.messages.page"),
        state = listState,
        refreshing = loadable.isRefreshing,
        onRefresh = onRefresh,
    ) {
        jLoadableContent(
            state = loadable,
            onRetry = onRefresh,
            loadingText = loadingText,
            isEmpty = { it.isEmpty() && snapshot.nextCursor == null && snapshot.moreFailure == null },
            empty = {
                item("lark-messages-empty") {
                    JEmptyState(
                        title = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_empty_title),
                        description = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_empty_text),
                        icon = PersonaIcons.messagesApp,
                    )
                }
            },
        ) { rows ->
            itemsIndexed(rows, key = { _, row -> row.id }) { index, row ->
                val image = remember(row.avatarRef) { avatar(row.avatarRef) }
                val picture = rememberLarkAvatar(image, onlineSince, MessageItemTokens.avatarSize(false))
                val title = row.name.ifBlank { androidx.compose.ui.res.stringResource(R.string.inbox_lark_conversation) }
                val preview = larkChatPreview(row)
                val time = row.updatedAt?.let { displayTime(it, currentLocale, resources) }.orEmpty().takeIf { it.isNotBlank() }
                MessageItem(
                    sender = title,
                    title = preview,
                    onClick = { onOpen(row) },
                    modifier = Modifier.testTag("lark.messages.row.${row.id}"),
                    position = GroupPosition.of(index, rows.size),
                    source = MessageSource.Other,
                    time = time,
                    status = if (row.unread > 0 || row.markedUnread) Status.Unread else null,
                    unread = row.unread,
                    avatar = picture,
                    avatarIcon = if (row.isGroup) PersonaIcons.groupChat else null,
                    titleIcon = if (row.muted) PersonaIcons.muted else null,
                    titleIconLabel = if (row.muted) {
                        androidx.compose.ui.res.stringResource(persona.core.designsystem.R.string.designsystem_row_muted)
                    } else null,
                    titleTag = if (row.markedUnread && row.unread == 0) {
                        androidx.compose.ui.res.stringResource(R.string.inbox_lark_unread)
                    } else null,
                    stackTitleTimeAtLargeText = true,
                )
            }
            snapshot.moreFailure?.let { reason ->
                item("lark-messages-more-failure") {
                    JNotice(
                        text = stringResourceLarkMessagesFailure(reason),
                        modifier = Modifier.padding(top = Tokens.Space.m),
                        status = Status.NeedsYou,
                        actionLabel = androidx.compose.ui.res.stringResource(persona.core.designsystem.R.string.designsystem_retry),
                        onAction = onLoadMore,
                    )
                }
            }
            if (snapshot.nextCursor != null && snapshot.moreFailure == null) {
                item("lark-messages-more-loading") {
                    JLoading(text = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_loading))
                }
            }
        }
    }

    LaunchedEffect(
        listState,
        snapshot.nextCursor,
        snapshot.rows.size,
        snapshot.moreFailure,
        snapshot.loading,
        snapshot.loadingMore,
    ) {
        if (snapshot.loading || snapshot.loadingMore || snapshot.nextCursor == null || snapshot.moreFailure != null) {
            return@LaunchedEffect
        }
        snapshotFlow { listState.layoutInfo.visibleItemsInfo.lastOrNull()?.index ?: -1 }
            .distinctUntilChanged()
            .collect { last ->
                val total = listState.layoutInfo.totalItemsCount
                if (total > 0 && last >= total - 2) onLoadMore()
            }
    }
}

/** A real repository is still loading, or a stale demo route is waiting for the shell to remove it. */
@Composable
internal fun LarkMessagesUnavailable(loading: Boolean = false) {
    JListPage(title = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_title)) {
        if (loading) {
            item("lark-messages-repository-loading") {
                JLoading(text = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_loading))
            }
        } else {
            item("lark-messages-unavailable") {
                JNotice(
                    text = androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_unavailable),
                    status = Status.Offline,
                )
            }
        }
    }
}

@Composable
private fun larkChatPreview(row: LarkChatSummary): String {
    val text = row.preview.trim()
    if (text.isEmpty()) return ""
    return when {
        row.previewMine -> androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_preview_mine, text)
        row.isGroup && !row.previewSender.isNullOrBlank() ->
            androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_preview_sender, row.previewSender, text)
        else -> text
    }
}

@Composable
private fun stringResourceLarkMessagesFailure(reason: persona.core.designsystem.UiText): String =
    androidx.compose.ui.res.stringResource(R.string.inbox_lark_messages_load_failed, reason.text())
