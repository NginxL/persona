package persona.feature.chat

import persona.core.chatlist.ChatListWindow
import persona.core.contracts.*
import persona.core.contracts.api.*
import persona.core.domain.lines.*
import persona.core.designsystem.StatusLineState
import persona.core.designsystem.StatusLineKind
import persona.core.designsystem.resolveStatusLine
import persona.core.domain.lines.Fold as LineFold
import persona.core.domain.lines.TaskNote as LineTask

internal fun Item.asLineItem() = LineItem(this,
    fold?.let { LineFold(it.items, it.tools, it.thoughts, it.throughSeq, it.throughId, parseTime(it.until)) },
    task?.let { LineTask(it.kind, it.title, it.status, it.toolUseId, it.taskId, it.detail, it.exitCode, it.outputFile) })

internal val chatItemOrder = compareBy<Item> { it.seq }.thenBy { it.id.substringAfterLast('#', "").toIntOrNull() ?: 0 }

internal fun ChatLine.lastItem(): Item? = when (this) {
    is ChatLine.Row -> row.item?.item
    is ChatLine.UserImages -> images.lastOrNull()?.item
    is ChatLine.Work -> steps.mapNotNull { it.lastItem() }.maxWithOrNull(chatItemOrder)
    is ChatLine.Step -> step.lastItem()
    is ChatLine.Wakes -> turn.wakes.lastOrNull()?.item
    is ChatLine.ApiError -> item.item
    is ChatLine.Recaps -> items.lastOrNull()?.item
    else -> null
}

/** The transcript item a visible line stands for when read receipts count it: a row, or the answer an error card is. */
internal fun ChatLine.seenItem(): Item? = when (this) {
    is ChatLine.Row -> row.item?.item
    is ChatLine.ApiError -> item.item
    else -> null
}

private fun WorkItem.lastItem(): Item? = when (this) {
    is WorkItem.Tool -> (call.result ?: call.use).item
    is WorkItem.Thought -> item.item
    is WorkItem.Narration -> item.item
    is WorkItem.Folded -> item.item.copy(seq = item.fold?.throughSeq ?: item.seq)
}

/** Keep the actual item snapshot: changing turn keys cannot move a historical reading window. */
internal class ChatTimeline {
    private val window = ChatListWindow<Item>({ it.id },
        chatItemOrder,
        { it.role == "assistant" && it.kind == "text" })
    private var frozenLiveId: String? = null
    private var shownLiveId: String? = null
    private val expanded = mutableMapOf<String, Pair<Item, List<Item>>>()

    /** A frozen window keeps the turn that was running when it froze, even after a newer one starts. */
    @Synchronized fun freeze(items: List<Item>) {
        if (!window.isFrozen) { window.freeze(items); frozenLiveId = shownLiveId }
    }
    @Synchronized fun follow() { window.follow(); frozenLiveId = null }
    @Synchronized fun isFrozen(): Boolean = window.isFrozen
    @Synchronized fun newMessageCount(items: List<Item>, below: Item?, lastRead: Item?, defer: Boolean = false) =
        window.newMessageCount(items, below, lastRead, defer)
    @Synchronized fun unfold(fold: Item, items: List<Item>) { expanded[fold.id] = fold to items }

    /**
     * A frozen window keeps the fold it froze with and also takes in the work the store put in its place when the fold
     * was opened: the fold's own items then come twice, and every step of the open work line counted twice.
     */
    @Synchronized fun searchItems(items: List<Item>): List<Item> = window.items(items).flatMap { item ->
        expanded[item.id]?.takeIf { it.first.fold == item.fold }?.second ?: listOf(item)
    }.distinctBy { it.id }

    /** Keeps the lines of turns that did not change since the last [lines]: a chat changes at its end. */
    private val memo = ChatLinesMemo()

    /** [live]: the agent is at work; [open] is the same persisted choice for live and historical lines. */
    @Synchronized fun lines(items: List<Item>, live: Boolean, open: Set<String>): persona.core.domain.lines.ChatSnapshot {
        val options = if (window.isFrozen) LineOptions(liveTurnId = frozenLiveId, openWork = open)
            else LineOptions(openWork = open, live = live)
        val foldAnchors = expanded.values.mapNotNull { (fold, page) ->
            page.filter { it.kind != "tool_result" && !(it.kind == "image" && it.toolUseId != null) }
                .minWithOrNull(chatItemOrder)?.let { it.id to "tools-${fold.id}" }
        }.toMap()
        val source = searchItems(items).filter { it.kind != "turn_duration" }.map { item ->
            item.asLineItem().copy(exchangeId = foldAnchors[item.id])
        }
        return memo.lines(source, options)
            .also { if (!window.isFrozen) shownLiveId = it.liveTurnId }
    }
}

internal fun ChatLine.recordedDuration(durations: Map<String, Long>): Long? = when (this) {
    is ChatLine.Work -> durations[id.removePrefix("w-").removePrefix("dw-")]
    else -> null
}

internal fun recordedDurationLabel(res: android.content.res.Resources, seconds: Long?): String? =
    seconds?.takeIf { it >= 0 }?.let { res.getString(R.string.chat_state_took_seconds, it) }

internal typealias StatusKind = StatusLineKind

/** How long a message of yours, the newest thing in the chat, counts as the start of a turn the hub has not reported. */
internal const val AWAIT_TURN_MILLIS = 60_000L

/**
 * The session's status says it is not at work, nor offline or waiting on you, and it did not fail to start: the hub may
 * just not have caught up.
 */
internal fun mayBeCatchingUp(status: String?, startFailed: Boolean) =
    !startFailed && (status == null || status == "idle" || status == "done" || status == "unknown")

/** Your message, when it is the newest thing in the transcript (nothing has answered it yet); one pass, no copy. */
internal fun awaitedMessage(items: List<Item>): Item? =
    items.asSequence().filter { it.kind != "turn_duration" }.maxWithOrNull(chatItemOrder)
        ?.takeIf { it.role == "user" && it.kind == "text" }

/**
 * [message] ([awaitedMessage]) is unanswered while the session's status still says it is not at work (#3909, web #3654):
 * the transcript has the message before the node's next sessions push says working, and in between the chat showed
 * neither the work line nor "Working". True for [AWAIT_TURN_MILLIS] after the message's time, which a phone clock behind
 * the hub's can stretch by up to as much again; never while the hub says working, waiting on you or offline, nor
 * after a failed start. Like the web, any message counts (typed in the terminal too): it starts a turn all the same.
 */
internal fun awaitingTurn(message: Item?, status: String?, startFailed: Boolean, now: java.time.Instant): Boolean {
    if (message == null || !mayBeCatchingUp(status, startFailed)) return false
    val at = parseTime(message.ts) ?: return false
    val age = java.time.Duration.between(at, now).toMillis()
    return age > -AWAIT_TURN_MILLIS && age < AWAIT_TURN_MILLIS
}

/**
 * Delivered but not in the transcript yet, while the session's status may still say idle: the status line says "Working"
 * for it too. No clock of its own: OutboxMessageSender drops a sent bubble after its grace (a minute, longer only
 * while the session is at work), and this goes with it.
 */
internal fun awaitingDelivered(messages: List<OutboxMessage>, status: String?, startFailed: Boolean): Boolean =
    mayBeCatchingUp(status, startFailed) && messages.any { it.state == DeliveryState.Sent }

/**
 * The session waits on you by the hub's word (#2483): its unified state says needs you, which also covers a question, plan
 * or permission a hook holds while herdr's status stays working (docs/protocol/agent-states.md), or its status says blocked
 * (an older hub without the state).
 */
internal fun Session?.needsYou(): Boolean = this?.status == "blocked" || this?.agentState == AgentState.NeedsYou

/**
 * The current turn waits on you rather than works (#2483): the session [needsYou], or one of its own requests is pending
 * here before the hub's state says so. A task agent's request (Persona's chat lists those too) does not count: this
 * session's own turn may still be at work. Whenever this holds, the header says "Waiting for you" too ([chatStatusState]).
 */
internal fun turnWaitsOnYou(session: Session?, key: SessionKey, requests: List<Interaction>): Boolean =
    session.needsYou() || requests.any { it.sessionKey == key }

internal fun chatStatusState(
    res: android.content.res.Resources,
    snapshot: persona.core.contracts.api.ChatSnapshot,
    session: Session?, machine: Machine?, messages: List<OutboxMessage>,
    requests: List<Interaction>, answering: Boolean,
    operationUnknown: Boolean = false,
    /** A turn of yours the hub has not reported yet ([awaitingTurn], [awaitingDelivered]). */
    awaiting: Boolean = false,
    submittingId: String? = null,
    receipts: List<MessageSendReceipt> = emptyList(),
): StatusLineState = StatusLineState(
    ended = snapshot.ended,
    replaced = snapshot.replaced,
    machineOffline = machine?.online == false,
    submitting = (submittingId != null && receipts.none { it.clientMessageId == submittingId && it.state != "sending" }) ||
        receipts.any { it.state == "sending" } ||
        messages.any { message -> message.state == DeliveryState.Sending && receipts.none { it.clientMessageId == message.clientMessageId && it.state != "sending" } },
    processing = receipts.any { it.processing },
    answering = answering,
    unconfirmed = operationUnknown || messages.any { it.state.resultUnknown } || receipts.any { it.state == "unknown" },
    pendingRequests = requests.size,
    blocked = session.needsYou(),
    requestSummary = requests.singleOrNull()?.headline?.shown,
    stopping = snapshot.stopping,
    working = session?.status == "working" || awaiting,
    waiting = session?.loop != null || !session?.background.isNullOrEmpty() || session?.runningWorkflows?.isNotEmpty() == true,
    waitingDetail = when {
        session?.loop != null -> res.getString(R.string.chat_state_looping)
        // a running workflow is in background too: counted once (#2935)
        (session?.backgroundCount ?: 0) > 0 -> session?.backgroundCount?.let { res.getQuantityString(R.plurals.chat_state_background, it, it) }
        else -> null
    },
)

internal fun chatStatus(
    res: android.content.res.Resources,
    snapshot: persona.core.contracts.api.ChatSnapshot,
    session: Session?, machine: Machine?, messages: List<OutboxMessage>,
    requests: List<Interaction>, answering: Boolean,
) = resolveStatusLine(chatStatusState(res, snapshot, session, machine, messages, requests, answering))

internal fun ChatLine.contentType(): String = when (this) {
    is ChatLine.Row -> row.type
    is ChatLine.Work -> "work"
    is ChatLine.Step -> when (step) { is WorkItem.Tool -> "tool"; is WorkItem.Folded -> "fold"; else -> "thought" }
    is ChatLine.End -> "work_end"
    is ChatLine.UserImages -> "images"
    is ChatLine.Wakes -> "wakes"
    is ChatLine.ApiError -> "api_error"
    is ChatLine.Recaps -> "recaps"
    is ChatLine.Viewed -> "viewed"
    is ChatLine.Extra -> "extra"
    is ChatLine.ModelChange -> "model_change"
    is ChatLine.ModeChange -> "mode_change"
}

internal fun interactionSource(res: android.content.res.Resources, interaction: Interaction, machine: Machine?): String = listOfNotNull(
    interaction.sessionTitle?.takeIf { it.isNotBlank() },
    machine?.takeIf { it.id == interaction.machineId }?.name?.takeIf { it.isNotBlank() } ?: res.getString(R.string.chat_state_unknown_machine),
).joinToString(" · ")

/** Where a request comes from, in the page's language. */
@androidx.compose.runtime.Composable
internal fun interactionSource(interaction: Interaction, machine: Machine?): String =
    interactionSource(androidx.compose.ui.platform.LocalResources.current, interaction, machine)

internal fun interactionDeadline(expires: java.time.Instant, now: java.time.Instant): String {
    return requireNotNull(persona.core.domain.answer.RequestExpiry.label(expires.toString(), now)).shown
}
