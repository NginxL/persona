package persona.feature.chat

import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.MutableState
import androidx.compose.ui.Modifier
import persona.core.contracts.Item
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository
import persona.core.contracts.Machine
import persona.core.contracts.api.OutboxMessage
import persona.core.chatlist.ChatListState
import persona.core.designsystem.StatusLineState
import persona.core.domain.lines.ChatLine
import persona.feature.chat.media.ChatImageViewer

/** The overlays above an already composed chat page. Kept as one focused UI region so ChatPage itself stays
 * below the ART method limit; these are the same state objects and callbacks, in the same order as before. */
@Composable
internal fun ChatPageOverlays(
    pushedDetail: TaskSheet?,
    taskHost: AssistantTaskHost?,
    search: ChatSearchState,
    searchableItems: List<Item>,
    turnCount: Int,
    hasOlder: Boolean,
    repository: ChatRepository,
    key: SessionKey,
    history: String?,
    terminalHelp: MutableState<Boolean>,
    activitySheet: MutableState<Boolean>,
    session: Session?,
    readOnly: Boolean,
    canSend: Boolean,
    send: (String?) -> Unit,
    onOpenPath: ((String, Int?) -> Unit)?,
    statusSheet: MutableState<Boolean>,
    status: StatusLineState,
    messages: List<OutboxMessage>,
    deliveryActions: ChatDeliveryActions,
    onDeliveryAction: ((OutboxMessage, DeliveryAction) -> Unit)?,
    replacement: Session?,
    onOpenSession: ((SessionKey) -> Unit)?,
    onConnectionDetails: (() -> Unit)?,
    terminal: (() -> Unit)?,
    turnDurationSeconds: Long?,
    notice: String?,
    recovery: ChatStatusRecovery,
    machine: Machine?,
    draftSaveError: String?,
    fullText: MutableState<String?>,
    sheetPlan: MutableState<LinePlan?>,
    planSheet: MutableState<Boolean>,
    renderStyle: ChatRenderStyle,
    composer: ChatComposerState,
    messageMenu: MessageMenuState,
    quoteTarget: (MessageTarget) -> Unit,
    onContext: (String) -> Unit,
    onNotice: (String) -> Unit,
    onFork: ((String) -> Unit)?,
    rewindMenu: RewindMenu?,
    onDelete: (MessageTarget) -> Unit,
    viewer: MutableState<ViewerRequest?>,
    chatListState: ChatListState,
) {
    pushedDetail?.let { open ->
        val host = taskHost ?: return@let
        Box(Modifier.fillMaxSize()) { AssistantTaskPage(open, host, onDismiss = { host.sheet.value = null }) }
    }
    if (search.visible) ChatSearchScreen(search, searchableItems, turnCount, hasOlder,
        repository, key, history, onClose = { search.close(chatListState) })
    if (terminalHelp.value) TerminalHelpDialog { terminalHelp.value = false }
    if (activitySheet.value) session?.takeIf(ChatActivity::shown)?.let { s ->
        ChatActivitySheet(s, onStop = if (readOnly || !canSend) null else ({ send(ChatActivity.STOP_LOOP) }),
            onOpenOutput = onOpenPath?.let { open -> { path -> activitySheet.value = false; open(path, null) } },
            onDismiss = { activitySheet.value = false })
    }
    // The loop stopped and the background work ended: nothing left for the sheet.
    val activityShown = ChatActivity.shown(session)
    LaunchedEffect(activityShown) { if (!activityShown) activitySheet.value = false }
    if (statusSheet.value) ChatStatusSheet(status, session, messages, deliveryActions.busyIds,
        onAction = onDeliveryAction, onDismiss = { statusSheet.value = false },
        replacement = replacement,
        onOpenReplacement = if (replacement != null && onOpenSession != null) {
            { statusSheet.value = false; onOpenSession(replacement.key) }
        } else null,
        onConnectionDetails = onConnectionDetails, terminal = terminal,
        recordedDuration = turnDurationSeconds, notice = notice,
        recovery = recovery, machine = machine, draftSaveError = draftSaveError, readOnly = readOnly,
        onOpenSession = onOpenSession?.let { open -> { target -> statusSheet.value = false; open(target) } })
    fullText.value?.let { FullTextReader(it, onDismiss = { fullText.value = null }) }
    sheetPlan.value?.takeIf { planSheet.value }?.let { PlanSheet(it, onDismiss = { planSheet.value = false }) }
    ChatMessageActions(key, session, machine, renderStyle, repository, readOnly, composer, messageMenu,
        quoteTarget = quoteTarget, onContext = onContext, onNotice = onNotice, onFork = onFork, rewindMenu = rewindMenu,
        onDelete = onDelete)
    viewer.value?.let { request ->
        ChatImageViewer(request.images, request.index, onDismiss = { viewer.value = null },
            onEdited = if (readOnly) null else ({ attachment -> composer.beginEdit(); composer.attachments += attachment }),
            photoLimitReached = composer.attachments.count { it.media } >= MAX_COMPOSER_MEDIA)
    }
}
