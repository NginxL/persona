package persona.feature.assistant

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.collectAsState
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import kotlinx.coroutines.launch
import persona.core.contracts.api.StarredConversation
import persona.core.contracts.api.StarredSnapshot
import persona.core.contracts.parseTime
import persona.core.designsystem.FeedbackAction
import persona.core.designsystem.FeedbackStyle
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JButton
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JListPage
import persona.core.designsystem.JLoading
import persona.core.designsystem.JSearchField
import persona.core.designsystem.JTextButton
import persona.core.designsystem.LocalAppFeedback
import persona.core.designsystem.LocalIsSidebarRoot
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.currentLocale
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.rememberCopy
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import persona.feature.chat.LocalStarredMessagesStore
import persona.feature.chat.StarredLoadState
import persona.feature.chat.StarredMessagesStore
import persona.feature.chat.StarredOriginal
import persona.feature.chat.StarredSnapshotMarkdown
import java.time.ZoneId

private const val STARRED_PREVIEW_MAX_CHARS = 420
private const val STARRED_PREVIEW_MAX_LINES = 6

internal fun hasLongStarredText(text: String): Boolean =
    text.length > STARRED_PREVIEW_MAX_CHARS || text.split('\n').size > STARRED_PREVIEW_MAX_LINES

internal fun collapsedStarredText(text: String): String =
    text.split('\n').take(STARRED_PREVIEW_MAX_LINES).joinToString("\n").take(STARRED_PREVIEW_MAX_CHARS) + "…"

internal fun StarredSnapshot.displayedSpeaker(meLabel: String, assistantLabel: String): String =
    speaker.ifBlank { if (role == "user") meLabel else assistantLabel }

internal fun filterStarredSnapshots(
    snapshots: List<StarredSnapshot>,
    query: String,
    meLabel: String,
    assistantLabel: String,
): List<StarredSnapshot> {
    val terms = query.trim().split(Regex("\\s+")).filter { it.isNotEmpty() }
    if (terms.isEmpty()) return snapshots
    return snapshots.filter { star ->
        val fields = listOf(star.text, star.title, star.displayedSpeaker(meLabel, assistantLabel))
        terms.all { term -> fields.any { field -> field.contains(term, ignoreCase = true) } }
    }
}

@Composable
internal fun StarredMessagesDestination(navigator: Navigator) {
    val store = LocalStarredMessagesStore.current
    val onBack = if (LocalIsSidebarRoot.current) null else navigator::back
    StarredMessagesScreen(
        store = store,
        onBack = onBack,
        onOpenOriginal = { star ->
            val kind = if (star.conversation == StarredConversation.Assistant) {
                ConversationKind.Assistant
            } else {
                ConversationKind.Agent
            }
            navigator.navigate(ChatRoute(star.machineId, star.sessionId, kind))
        },
    )
}

/**
 * Saved starred message snapshots for the current assistant (matching iOS `StarredMessagesPage.swift`).
 */
@Composable
fun StarredMessagesScreen(
    store: StarredMessagesStore?,
    onBack: (() -> Unit)?,
    onOpenOriginal: (StarredSnapshot) -> Unit,
    modifier: Modifier = Modifier,
) {
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    val resources = LocalResources.current
    val state = store?.state?.collectAsState()?.value
    val loadState = state?.loadState ?: StarredLoadState.Unsupported
    val snapshots = state?.snapshots.orEmpty()
    val canWrite = state?.canWrite == true
    val refreshing = state?.refreshing == true && snapshots.isNotEmpty()
    var query by rememberSaveable { mutableStateOf("") }
    val meLabel = stringResource(R.string.assistant_starred_speaker_me)
    val assistantLabel = stringResource(R.string.assistant_starred_speaker_assistant)
    val matches = remember(snapshots, query, meLabel, assistantLabel) {
        filterStarredSnapshots(snapshots, query, meLabel, assistantLabel)
    }
    // Unstar and Undo run in the screen-level scope so removing a card from the LazyColumn
    // never cancels the Undo coroutine that restores it.
    val onUnstar: (StarredSnapshot) -> Unit = { star ->
        store?.let { active ->
            scope.launch {
                active.remove(star.source, id = star.id).onSuccess { removed ->
                    val message = resources.getString(R.string.assistant_starred_unstarred)
                    if (removed != null) {
                        val undo = FeedbackAction(resources.getString(R.string.assistant_starred_undo)) {
                            scope.launch {
                                active.add(removed).onFailure {
                                    feedback?.notice(resources.getString(R.string.assistant_starred_action_failed))
                                }
                            }
                        }
                        feedback?.notice(message, undo)
                    } else {
                        feedback?.notice(message, FeedbackStyle.Success)
                    }
                }.onFailure {
                    feedback?.notice(resources.getString(R.string.assistant_starred_action_failed))
                }
            }
        }
    }

    DisposableEffect(store) {
        if (store != null) {
            scope.launch { store.startReading() }
        }
        onDispose {
            store?.stopReading()
        }
    }

    JListPage(
        title = stringResource(R.string.assistant_starred_title),
        modifier = modifier.testTag("starred-messages-page"),
        onBack = onBack,
        refreshing = refreshing,
        onRefresh = store?.takeIf { it.offered }?.let { active ->
            {
                scope.launch {
                    active.loadIndex()
                    active.loadPage()
                }
            }
        },
    ) {
        if (loadState == StarredLoadState.Ready && snapshots.isNotEmpty()) {
            item(key = "starred-search") {
                JSearchField(
                    query = query,
                    onQueryChange = { query = it },
                    placeholder = stringResource(R.string.assistant_starred_search),
                )
            }
        }
        if (matches.isNotEmpty()) {
            items(matches, key = { it.id ?: "${it.machineId}/${it.sessionId}/${it.itemId}" }) { star ->
                StarredMessageCard(
                    star = star,
                    store = store,
                    canWrite = canWrite,
                    pending = state?.isPending(star.source) == true,
                    meLabel = meLabel,
                    assistantLabel = assistantLabel,
                    onOpenOriginal = { onOpenOriginal(star) },
                    onUnstar = { onUnstar(star) },
                )
            }
        } else {
            item(key = "starred-empty") {
                StarredEmptyState(
                    loadState = loadState,
                    hasSnapshots = snapshots.isNotEmpty(),
                    onRetry = store?.let { active -> { scope.launch { active.loadPage() } } },
                )
            }
        }
    }
}

@Composable
private fun StarredEmptyState(
    loadState: StarredLoadState,
    hasSnapshots: Boolean,
    onRetry: (() -> Unit)?,
) {
    when (loadState) {
        StarredLoadState.Idle, StarredLoadState.Loading -> {
            Box(
                Modifier
                    .fillMaxWidth()
                    .padding(vertical = Tokens.Space.xxl),
                contentAlignment = Alignment.Center,
            ) {
                JLoading()
            }
        }
        StarredLoadState.Unsupported -> {
            JEmptyState(
                title = stringResource(R.string.assistant_starred_unavailable_title),
                description = stringResource(R.string.assistant_starred_unavailable_detail),
                icon = PersonaIcons.pluginStar,
            )
        }
        StarredLoadState.Failed -> {
            JEmptyState(
                title = stringResource(R.string.assistant_starred_failed_title),
                description = stringResource(R.string.assistant_starred_failed_detail),
                icon = PersonaIcons.error,
                action = onRetry?.let { retry ->
                    { JButton(stringResource(R.string.assistant_starred_retry), onClick = retry) }
                },
            )
        }
        StarredLoadState.Ready -> {
            if (!hasSnapshots) {
                JEmptyState(
                    title = stringResource(R.string.assistant_starred_empty_title),
                    description = stringResource(R.string.assistant_starred_empty_detail),
                    icon = PersonaIcons.pluginStar,
                )
            } else {
                JEmptyState(
                    title = stringResource(R.string.assistant_starred_no_matches_title),
                    description = stringResource(R.string.assistant_starred_no_matches_detail),
                    icon = PersonaIcons.pluginStar,
                )
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun StarredMessageCard(
    star: StarredSnapshot,
    store: StarredMessagesStore?,
    canWrite: Boolean,
    pending: Boolean,
    meLabel: String,
    assistantLabel: String,
    onOpenOriginal: () -> Unit,
    onUnstar: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val scope = rememberCoroutineScope()
    val copy = rememberCopy()
    val locale = currentLocale
    var expanded by rememberSaveable(star.id) { mutableStateOf(false) }
    var checking by remember(star.id) { mutableStateOf(false) }
    var original by remember(star.id) { mutableStateOf<StarredOriginal?>(null) }

    val longText = remember(star.text) { hasLongStarredText(star.text) }
    val displayText = remember(star.text, longText, expanded) {
        if (longText && !expanded) collapsedStarredText(star.text) else star.text
    }
    val formattedTime = remember(star.writtenAt, star.starredAt, locale) {
        val instant = parseTime(star.writtenAt) ?: parseTime(star.starredAt)
        instant?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, locale, ZoneId.systemDefault()) }
    }
    val titleText = star.title.ifBlank { stringResource(R.string.assistant_starred_conversation_fallback) }
    val speakerText = star.displayedSpeaker(meLabel, assistantLabel)
    val copiedNotice = stringResource(R.string.assistant_starred_copied)

    JGroupItem(
        position = GroupPosition.Single,
        modifier = Modifier.testTag("starred-snapshot-${star.id ?: star.itemId}"),
    ) {
        Column(
            modifier = Modifier
                .fillMaxWidth()
                .padding(Tokens.Space.l),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            Text(
                text = titleText,
                style = PersonaTheme.type.headline,
                color = palette.ink,
                modifier = Modifier.semantics { heading() },
            )
            Row(
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    text = speakerText,
                    style = PersonaTheme.type.caption,
                    color = palette.ink2,
                )
                formattedTime?.let { timestamp ->
                    Text(
                        text = timestamp,
                        style = PersonaTheme.type.caption,
                        color = palette.ink2,
                    )
                }
            }
            StarredSnapshotMarkdown(
                text = displayText,
                id = "starred-${star.id ?: star.itemId}",
                modifier = Modifier.fillMaxWidth().testTag("starred-snapshot-text"),
            )
            if (longText) {
                JTextButton(
                    text = stringResource(
                        if (expanded) R.string.assistant_starred_show_less else R.string.assistant_starred_show_more,
                    ),
                    onClick = { expanded = !expanded },
                )
            }
            val omitted = star.omittedChars ?: 0
            if (omitted > 0) {
                Text(
                    text = stringResource(R.string.assistant_starred_omitted, omitted),
                    style = PersonaTheme.type.caption,
                    color = palette.ink2,
                )
            }
            if (original == StarredOriginal.Unavailable || !star.source.isVerified) {
                Text(
                    text = stringResource(R.string.assistant_starred_original_unavailable),
                    style = PersonaTheme.type.caption,
                    color = palette.ink2,
                )
            } else if (original == StarredOriginal.Failed) {
                Text(
                    text = stringResource(R.string.assistant_starred_original_failed),
                    style = PersonaTheme.type.caption,
                    color = palette.ink2,
                )
            }
            FlowRow(
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m),
                verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
            ) {
                if (original != StarredOriginal.Unavailable && star.source.isVerified && store != null) {
                    JTextButton(
                        text = stringResource(
                            if (checking) R.string.assistant_starred_checking else R.string.assistant_starred_open_original,
                        ),
                        onClick = {
                            if (checking) return@JTextButton
                            checking = true
                            scope.launch {
                                try {
                                    val result = store.verifyOriginal(star.source)
                                    if (result != StarredOriginal.Retired) {
                                        original = result
                                        if (result == StarredOriginal.Verified) onOpenOriginal()
                                    }
                                } finally {
                                    checking = false
                                }
                            }
                        },
                        modifier = Modifier.testTag("starred-open-original"),
                        enabled = !checking,
                    )
                }
                JTextButton(
                    text = stringResource(R.string.assistant_starred_copy),
                    onClick = { copy(star.text, copiedNotice) },
                    modifier = Modifier.testTag("starred-copy"),
                )
                if (canWrite && store != null) {
                    JTextButton(
                        text = stringResource(R.string.assistant_starred_unstar),
                        onClick = onUnstar,
                        modifier = Modifier.testTag("starred-unstar"),
                        enabled = !pending,
                    )
                }
            }
        }
    }
}
