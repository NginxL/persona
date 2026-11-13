package persona.feature.inbox

import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch
import persona.core.contracts.InboxChainEntry
import persona.core.contracts.InboxSuggestion
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.inbox.InboxTier
import persona.core.navigation.Navigator

/**
 * An inbox event's page (inbox phase 1): the item's tags and "View original" (its source page as the list opened it before),
 * ✦ Summary, Recap, where the processing stands, the suggestion with its buttons right under them, then the Handling chain (the
 * newest entry, "Expand all (N)") and the items it is Related. Only items with event fields open here ([inboxRowPage]).
 */
@Composable
fun InboxEventScreen(repository: InboxRepository, route: InboxEventRoute, navigator: Navigator) {
    val inbox by repository.snapshot.collectAsStateWithLifecycle()
    val listedRecord = inbox.records.inboxRecord(route.itemId)
    var lookup by remember(route.itemId) { mutableStateOf<InboxItemLookupResult?>(null) }
    var lookupAttempt by remember(route.itemId) { mutableIntStateOf(0) }
    LaunchedEffect(repository, route.itemId, listedRecord, lookupAttempt) {
        if (listedRecord == null) {
            lookup = null
            lookup = readInboxItem(repository, route.itemId)
        } else lookup = null
    }
    val record = listedRecord ?: (lookup as? InboxItemLookupResult.Found)?.record
    val pages = LocalInboxItemPages.current
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    var refreshing by remember { mutableStateOf(false) }
    var markingDone by remember { mutableStateOf(false) }
    var error by remember { mutableStateOf<String?>(null) }
    var stale by rememberSaveable(route.itemId) { mutableStateOf(false) }
    val open = LocalLarkLinkOpener.current
    fun go(target: ChainTarget) {
        val shown = record ?: return
        when (target) {
            is ChainTarget.Page -> navigator.navigate(target.key)
            ChainTarget.Source -> navigator.navigate(pages.open(shown))
            is ChainTarget.Url -> open?.invoke(target.url, inbox.contentSource)
            is ChainTarget.Item -> navigator.navigate(inbox.records.firstOrNull { it.item.id == target.id }
                ?.let { inboxRowPage(it, pages) } ?: InboxEventRoute(target.id))
            ChainTarget.Inline, ChainTarget.None -> Unit
        }
    }
    JListPage(
        title = record?.item?.title?.takeIf { it.isNotBlank() } ?: stringResource(R.string.inbox_item),
        onBack = navigator::back, largeTitle = false, refreshing = refreshing,
        onRefresh = {
            scope.launch {
                refreshing = true
                try {
                    repository.refreshNow()
                    if (record == null) lookupAttempt++
                } catch (e: CancellationException) { throw e } catch (_: Exception) { } finally { refreshing = false }
            }
        },
        actions = {
            if (record != null) JIconButton(PhIcons.CheckCircle, stringResource(if (record.item.isDone) R.string.inbox_move_back else R.string.inbox_done),
                enabled = !markingDone, onClick = {
                    markingDone = true
                    scope.launch {
                        try { repository.setDone(record.item.id, !record.item.isDone); error = null; navigator.back() }
                        catch (e: CancellationException) { throw e }
                        catch (e: Exception) { if (feedback?.failure(e, operation = true) != true) error = res.getString(R.string.inbox_failed_retry) }
                        finally { markingDone = false }
                    }
                })
        },
    ) {
        if (record == null) {
            when (val result = lookup) {
                null -> item("event-loading") { JLoading(text = stringResource(R.string.inbox_loading)) }
                is InboxItemLookupResult.Failed -> item("event-failed") {
                    JNotice(stringResource(R.string.inbox_refresh_failed), actionLabel = stringResource(R.string.inbox_retry),
                        onAction = { lookupAttempt++ })
                }
                InboxItemLookupResult.Missing -> item("event-missing") { StatusText(stringResource(R.string.inbox_item_missing)) }
                is InboxItemLookupResult.Found -> Unit
            }
            return@JListPage
        }
        item("event-source") {
            Row(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                Row(Modifier.weight(1f).horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    eventTag(record)?.let { (text, status) -> JTag(text, status = status) }
                    inboxSourceTags(res, record).forEach { JTag(it) }
                }
                JTextButton(stringResource(R.string.inbox_event_view_source), { navigator.navigate(pages.open(record)) }, icon = PhIcons.ArrowSquareOut)
            }
        }
        record.item.summary?.takeIf { it.isNotBlank() }?.let { summary ->
            item("event-summary") { JNotice(summary, Modifier.padding(bottom = Tokens.Space.s), title = stringResource(R.string.inbox_ai_summary), icon = PhIcons.Sparkle) }
        }
        record.event.recap?.let { recap ->
            item("event-recap") {
                JCard(Modifier.padding(bottom = Tokens.Space.s)) {
                    Text(stringResource(R.string.inbox_event_recap), style = PersonaTheme.type.headline, color = PersonaTheme.palette.ink)
                    Spacer(Modifier.padding(top = Tokens.Space.xs))
                    Text(recap, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink)
                }
            }
        }
        if (!record.item.isDone) eventMark(record)?.takeIf { it !is EventMark.Suggests }?.let { mark ->
            item("event-state") { EventStateLine(mark, Modifier.padding(bottom = Tokens.Space.s)) }
        }
        // "Suggestion updated" stays over the new suggestion until the user answers again (the card is drawn anew for it).
        if (stale && !record.item.isDone) item("event-stale") {
            JNotice(stringResource(R.string.inbox_suggestion_stale), Modifier.padding(bottom = Tokens.Space.s), icon = PhIcons.ArrowsClockwise)
        }
        val suggestion = record.event.suggestion
        if (suggestion != null && !record.item.isDone) item("event-suggestion:${suggestion.version}") {
            SuggestionCard(repository, record.item.id, suggestion, Modifier.padding(bottom = Tokens.Space.s),
                needsYou = record.item.needsYou, onStale = { stale = it })
        }
        error?.let { message -> item("event-error") { StatusText(message, isError = true) } }
        eventChain(record, ::go)
        item("event-line") { InboxLineSection(repository, record, navigator) }
        if (record.event.related.isNotEmpty()) {
            item("event-related-header") { JSectionHeader(stringResource(R.string.inbox_event_related), count = record.event.related.size) }
            itemsIndexed(record.event.related, key = { _, it -> "event-related:${it.id}" }) { index, related ->
                JGroupItem(GroupPosition.of(index, record.event.related.size)) {
                    JRow(related.title.ifBlank { related.id },
                        leadingIcon = if (related.source == "mail") PhIcons.EnvelopeSimple else PhIcons.ChatCircleDots,
                        onClick = { go(ChainTarget.Item(related.id)) })
                }
            }
        }
        item("event-end") { Spacer(Modifier.padding(top = Tokens.Space.xxl)) }
    }
}

/** The page's first tag: the section the event is in (In progress says who is on it), none for a done one. */
@Composable
private fun eventTag(record: InboxRecord): Pair<String, Status?>? {
    val item = record.item
    // The manager filed it (looked and found nothing for the user): it says so rather than a bare Done.
    if (item.isDone) return stringResource(if (record.event.doneBy == "manager") R.string.inbox_done_by_manager else R.string.inbox_done) to null
    return when (item.section) {
        "working" -> stringResource(R.string.inbox_section_working) to Status.Working
        InboxTier.Action.wire -> stringResource(R.string.inbox_bay_needs_you) to Status.NeedsYou
        InboxTier.Fyi.wire -> inboxTierTitle(InboxTier.Fyi) to null
        InboxTier.Noise.wire -> inboxTierTitle(InboxTier.Noise) to null
        else -> null
    }
}

/** Where the processing stands, as one line: who is on it, "AI is preparing a suggestion", or that it failed. */
@Composable
internal fun EventStateLine(mark: EventMark, modifier: Modifier = Modifier) {
    when (mark) {
        is EventMark.Working -> JInlineStatus(stringResource(mark.word), modifier, tone = InlineTone.Busy)
        EventMark.Preparing -> JInlineStatus(stringResource(R.string.inbox_handling_preparing), modifier, tone = InlineTone.Busy)
        is EventMark.Failed -> JInlineStatus(mark.reason?.let { stringResource(R.string.inbox_handling_failed_reason, it) }
            ?: stringResource(R.string.inbox_handling_failed), modifier, tone = InlineTone.Failed)
        is EventMark.Suggests -> Unit
    }
}

/** A row's event line under its preview: the suggestion's kind and first line, or where the processing stands. */
@Composable
internal fun EventRowMark(mark: EventMark) {
    val palette = PersonaTheme.palette
    when (mark) {
        is EventMark.Suggests -> Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(PhIcons.Sparkle, null, size = Tokens.Space.m, tint = palette.accent, decorative = true)
            Text(stringResource(R.string.inbox_suggestion_row, stringResource(mark.kind), mark.line), style = PersonaTheme.type.caption,
                color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        is EventMark.Working -> JStateLabel(stringResource(mark.word), Status.Working)
        EventMark.Preparing -> JStateLabel(stringResource(R.string.inbox_handling_preparing), Status.Working)
        is EventMark.Failed -> JStateLabel(stringResource(R.string.inbox_handling_failed), Status.NeedsYou, problem = true)
    }
}

/**
 * The suggestion and its buttons. Edit opens [SuggestionEditor]; a stale suggestion (the hub has a newer one) says
 * "Suggestion updated" and the page redraws from the item the hub answers with. A retry of the same answer keeps its client id.
 */
@Composable
internal fun SuggestionCard(repository: InboxRepository, itemId: String, suggestion: InboxSuggestion, modifier: Modifier = Modifier,
    /** In Needs you the card is marked as the user's to decide; a conclusion in FYI is just read. */
    needsYou: Boolean = true,
    /** True when the answer found a newer suggestion (the page says "Suggestion updated"), false when the user answers again. */
    onStale: (Boolean) -> Unit = {},
) {
    val palette = PersonaTheme.palette
    val feedback = LocalAppFeedback.current
    val res = LocalResources.current
    val scope = rememberCoroutineScope()
    val buttons = suggestionButtons(suggestion)
    var busy by remember(suggestion.version) { mutableStateOf<SuggestionAction?>(null) }
    var editing by rememberSaveable(suggestion.version) { mutableStateOf(false) }
    var notice by remember(suggestion.version) { mutableStateOf<String?>(null) }
    var last by remember(suggestion.version) { mutableStateOf<SuggestionRequest?>(null) }
    fun act(action: SuggestionAction, text: String?) {
        if (busy != null) return
        val request = last?.takeIf { it.action == action && it.text == text } ?: SuggestionRequest(itemId, suggestion.version, action, text)
        last = request
        busy = action
        onStale(false)
        scope.launch {
            try {
                val outcome = repository.actOnSuggestion(request)
                editing = false
                notice = null
                onStale(outcome == SuggestionOutcome.Stale)
            } catch (e: CancellationException) { throw e }
            catch (e: Exception) { if (feedback?.failure(e, operation = true) != true) notice = res.getString(R.string.inbox_failed_retry) }
            finally { busy = null }
        }
    }
    JCard(modifier, status = if (needsYou) Status.NeedsYou else null) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JIcon(PhIcons.Sparkle, null, size = Tokens.Space.l, tint = palette.accent, decorative = true)
            Text(stringResource(suggestionKindLabel(suggestion)), style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1)
        }
        Spacer(Modifier.padding(top = Tokens.Space.xs))
        Text(suggestion.text, style = PersonaTheme.type.body, color = palette.ink)
        suggestion.note?.takeIf { it.isNotBlank() }?.let {
            Spacer(Modifier.padding(top = Tokens.Space.xs))
            Text(stringResource(R.string.inbox_suggestion_note, it), style = PersonaTheme.type.caption, color = palette.ink3)
        }
        notice?.let { JNotice(it, Modifier.padding(top = Tokens.Space.s), status = Status.NeedsYou) }
        Spacer(Modifier.padding(top = Tokens.Space.s))
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            JButton(stringResource(buttons.confirm), { act(SuggestionAction.Confirm, null) }, Modifier.weight(1f), size = JButtonSize.Small,
                enabled = busy == null, loading = busy == SuggestionAction.Confirm)
            if (buttons.editable) JButton(stringResource(R.string.inbox_suggestion_edit), { editing = true }, Modifier.weight(1f),
                kind = JButtonKind.Secondary, size = JButtonSize.Small, enabled = busy == null)
            JButton(stringResource(R.string.inbox_suggestion_reject), { act(SuggestionAction.Reject, null) }, Modifier.weight(1f),
                kind = JButtonKind.Quiet, size = JButtonSize.Small, enabled = busy == null, loading = busy == SuggestionAction.Reject)
        }
    }
    if (editing) SuggestionEditor(suggestion, buttons, busy,
        onConfirm = { text -> act(SuggestionAction.Confirm, confirmText(suggestion, text)) },
        onInstruct = { words -> act(SuggestionAction.Instruct, words.trim()) },
        onDismiss = { if (busy == null) editing = false })
}

/** Edit: the suggestion's text to change and confirm, or "Say something" to tell the processing conversation instead. */
@Composable
private fun SuggestionEditor(
    suggestion: InboxSuggestion,
    buttons: SuggestionButtons,
    busy: SuggestionAction?,
    onConfirm: (String) -> Unit,
    onInstruct: (String) -> Unit,
    onDismiss: () -> Unit,
) {
    var text by rememberSaveable(suggestion.version) { mutableStateOf(suggestion.text) }
    var words by rememberSaveable(suggestion.version) { mutableStateOf("") }
    JSheet(onDismiss = onDismiss, title = stringResource(R.string.inbox_suggestion_editor_title), dismissible = busy == null, scrollable = true) {
        Column(Modifier.fillMaxWidth().imePadding(), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            JTextField(text, { text = it }, Modifier.fillMaxWidth(), label = stringResource(suggestionKindLabel(suggestion)), minLines = 3, maxLines = 10)
            JButton(stringResource(buttons.confirm), { onConfirm(text) }, Modifier.fillMaxWidth(), size = JButtonSize.Medium,
                enabled = busy == null && text.isNotBlank(), loading = busy == SuggestionAction.Confirm)
            JDivider()
            JTextField(words, { words = it }, Modifier.fillMaxWidth(), label = stringResource(R.string.inbox_suggestion_say),
                placeholder = stringResource(R.string.inbox_suggestion_say_hint), minLines = 2)
            JButton(stringResource(R.string.inbox_suggestion_say_send), { onInstruct(words) }, Modifier.fillMaxWidth(), kind = JButtonKind.Secondary,
                size = JButtonSize.Medium, enabled = busy == null && words.isNotBlank(), loading = busy == SuggestionAction.Instruct)
        }
    }
}

/** The Handling chain: a header, the newest entry (all of them once expanded), each tapping where its link goes or opening in place. */
private fun LazyListScope.eventChain(record: InboxRecord, go: (ChainTarget) -> Unit) {
    val chain = record.event.chain
    if (chain.isEmpty()) return
    item("event-chain-header") { JSectionHeader(stringResource(R.string.inbox_event_chain), count = chain.size) }
    item("event-chain") {
        var expanded by rememberSaveable(record.item.id) { mutableStateOf(false) }
        val shown = shownChain(chain, expanded)
        Column {
            shown.forEachIndexed { index, entry ->
                JGroupItem(GroupPosition.of(index, shown.size)) { ChainRow(entry, record, go) }
            }
            if (chain.size > 1) JTextButton(
                if (expanded) stringResource(R.string.inbox_collapse) else stringResource(R.string.inbox_event_chain_all, chain.size),
                { expanded = !expanded }, Modifier.padding(top = Tokens.Space.xs))
        }
    }
}

@Composable
private fun ChainRow(entry: InboxChainEntry, record: InboxRecord, go: (ChainTarget) -> Unit) {
    val palette = PersonaTheme.palette
    val target = chainTarget(entry, record)
    var open by rememberSaveable(record.item.id, entry.id) { mutableStateOf(false) }
    val time = chainTime(entry.at, currentLocale)
    JRow(onClick = when (target) {
        ChainTarget.None -> null
        ChainTarget.Inline -> ({ open = !open })
        else -> ({ go(target) })
    }, chevron = target !is ChainTarget.None && target !is ChainTarget.Inline) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (time.isNotEmpty()) Text(time, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1)
            JIcon(chainIcon(entry.kind, record), null, size = Tokens.Space.l, tint = palette.ink2, decorative = true)
            Text(entry.text.ifBlank { entry.kind }, Modifier.weight(1f, fill = false), style = PersonaTheme.type.callout, color = palette.ink,
                maxLines = if (open) Int.MAX_VALUE else 2, overflow = TextOverflow.Ellipsis)
            if (target == ChainTarget.Inline) JIcon(if (open) PhIcons.CaretUp else PhIcons.CaretDown, null, size = Tokens.Space.l, tint = palette.ink3, decorative = true)
        }
    }
    if (open && target == ChainTarget.Inline) Column(Modifier.fillMaxWidth().padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.m),
        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        entry.detail?.takeIf { it.isNotBlank() }?.let { Text(it, style = PersonaTheme.type.callout, color = palette.ink2) }
        entry.messages.forEach { message ->
            Text(stringResource(R.string.inbox_suggestion_row, message.sender, message.text), style = PersonaTheme.type.callout, color = palette.ink2)
        }
    }
}
