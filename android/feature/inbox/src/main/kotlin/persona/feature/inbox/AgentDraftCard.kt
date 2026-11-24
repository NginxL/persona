package persona.feature.inbox

import androidx.compose.ui.res.stringResource
import persona.core.designsystem.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import persona.core.navigation.Navigator
import kotlinx.coroutines.CoroutineScope
import persona.core.inbox.*

/**
 * The AI twin's work on an item, in its row and on its page: busy or failed as a status line, and an AI draft (the AI
 * twin's, or one from "AI draft") as a card with "Revise" and "Send". "Send" always asks first ("Send as you?"), where it
 * goes and the whole text (the old app's ConfirmSend). [onEdit] / [onSend] let a page use its own sheet and send path
 * (the mail page checks the newest message); by default the card opens [InboxDraftSheet] and sends to the item.
 */
@Composable
internal fun AgentDraftCard(record: InboxRecord, composers: ReplyComposers, navigator: Navigator,
    onEditingChange: (Boolean) -> Unit = {},
    repository: InboxRepository? = null,
    onEdit: (() -> Unit)? = null,
    onSend: (() -> Unit)? = null,
    /** An item's own page keeps its reply around, so a draft from "AI draft" shows as soon as it comes. */
    keepComposer: Boolean = false,
) {
    // From a hub with events (inbox phase 1) the suggestion and the processing state take the AI twin's place (the hub
    // mirrors a reply suggestion into `agent` for older apps): the event page draws them, not this card.
    val agent = record.agent?.takeIf { record.event.suggestion == null && record.event.handling == null }
    val drafted = agent != null && agent.state == "drafted" && agent.draft.isNotBlank()
    if (agent?.state == "running" || agent?.state == "queued") {
        StatusLine(StatusLineState(waiting = agent.state == "queued", working = agent.state == "running",
            waitingDetail = stringResource(R.string.inbox_reply_ai_waiting), workDetail = agent.task))
    } else if (agent?.state == "failed") {
        StatusText(stringResource(R.string.inbox_reply_ai_failed), isError = true)
    }
    // Rows without an AI draft make no reply of their own (one per row would read every draft at once).
    val target = ReplyTarget.Inbox(record.item.id)
    val composer = if (keepComposer || drafted || record.aiDraft) composers.get(target, if (drafted) agent?.draft.orEmpty() else "")
        else composers.existing(target)
    if (composer != null) DraftCardBody(record, composer, if (drafted) agent?.draft else null, navigator, repository,
        onEditingChange, onEdit, onSend)
}

@Composable
private fun DraftCardBody(
    record: InboxRecord,
    composer: ReplyComposer,
    agentDraft: String?,
    navigator: Navigator,
    repository: InboxRepository?,
    onEditingChange: (Boolean) -> Unit,
    onEdit: (() -> Unit)?,
    onSend: (() -> Unit)?,
) {
    // A page that opened before the item came in made the reply empty: the AI twin's draft fills it now.
    LaunchedEffect(composer, agentDraft) { agentDraft?.let(composer::offerAiDraft) }
    val state by composer.state.collectAsStateWithLifecycle()
    var editing by rememberSaveable(record.item.id) { mutableStateOf(false) }
    var confirming by remember { mutableStateOf(false) }
    LaunchedEffect(state.sent) { if (state.sent) editing = false }
    val editingChanged by rememberUpdatedState(onEditingChange)
    SideEffect { editingChanged(editing || confirming) }
    DisposableEffect(Unit) { onDispose { editingChanged(false) } }
    val canEdit = onEdit != null || repository != null
    if (state.sent || state.draft.ai && state.draft.text.isNotBlank()) DraftPreview(
        text = if (state.sent) stringResource(R.string.inbox_sent) else state.draft.text,
        actions = {
            if (canEdit) JButton(stringResource(R.string.inbox_reply_edit), { if (onEdit != null) onEdit() else editing = true }, kind = JButtonKind.Secondary,
                size = JButtonSize.Small, enabled = state.canEdit && !state.sent)
            JButton(state.actionLabel, {
                when {
                    state.checksResult -> composer.send()
                    onSend != null -> onSend()
                    else -> confirming = true
                }
            }, size = JButtonSize.Small, enabled = !state.loading && !state.sending && !state.sent && state.draft.text.isNotBlank())
        })
    if (state.sent || state.draft.ai) ReplyStatus(state)
    if (editing && repository != null) InboxDraftSheet(repository, record, composer, navigator, onDismiss = { editing = false })
    if (confirming) SendAsYouDialog(sendConfirmation(record, state.draft.text),
        onSend = { confirming = false; composer.send() }, onDismiss = { confirming = false }, enabled = !state.sending)
}

class ReplyComposers(private val repository: InboxRepository, private val scope: CoroutineScope) {
    private val composers = mutableMapOf<ReplyTarget, ReplyComposer>()
    @Synchronized fun get(target: ReplyTarget, initialText: String = "", replyRepository: InboxRepository = repository): ReplyComposer =
        composers.getOrPut(target) { ReplyComposer(replyRepository, target, scope, initialText) }

    /** The reply already open for [target], without opening one. */
    @Synchronized fun existing(target: ReplyTarget): ReplyComposer? = composers[target]
}

@Composable
internal fun rememberReplyComposers(repository: InboxRepository): ReplyComposers {
    val paired by repository.replyComposers.collectAsStateWithLifecycle()
    paired?.let { return it }
    val shared = (repository as? PinnedInboxRepository)?.base ?: repository
    if (shared is InboxExperienceRepository) return shared.composers
    val scope = rememberCoroutineScope()
    return remember(repository, scope) { ReplyComposers(repository, scope) }
}
