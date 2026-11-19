package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import java.time.Instant

/** Pairing-owned presentation only. No receipt mutates the outbox, transcript, runtime state or counts. */
internal class MessageSendReceiptState(private val now: () -> Long = System::currentTimeMillis) {
    private data class Record(
        val session: SessionKey,
        val operationId: String,
        val clientId: String,
        val mainTarget: Boolean,
        val createdAt: Long,
        var request: MessageRequest? = null,
        var receipt: MessageSendReceipt? = null,
        var latestAt: Instant? = null,
        var terminal: Boolean = false,
        var closed: Boolean = false,
        var echoedUserSeq: Int? = null,
        var delivered: Boolean = false,
    )
    private data class RuntimeEvidence(val status: String, val state: AgentState, val since: String?, val queued: List<String>)
    private val records = LinkedHashMap<Pair<SessionKey, String>, Record>()
    private val completed = LinkedHashSet<Pair<SessionKey, String>>()
    private var runtime = emptyMap<SessionKey, RuntimeEvidence>()
    private val mutable = MutableStateFlow<List<MessageSendReceipt>>(emptyList())
    val receipts = mutable.asStateFlow()

    /** Local preparation includes main-conversation resolution; it is never a hub receipt or durable delivery. */
    @Synchronized fun preparing(session: SessionKey, id: String, mainTarget: Boolean, operationId: String = id) {
        val key = session to operationId
        val previous = records[key]
        if (previous == null || previous.closed) records[key] = Record(session, operationId, id, mainTarget, now())
        records[key]?.takeIf { it.receipt == null }?.let {
            it.receipt = MessageSendReceipt(session, id, "sending", Instant.ofEpochMilli(now()), mainTarget)
        }
        publish()
    }

    @Synchronized fun stopPreparing(id: String) {
        records.values.filter { it.clientId == id && it.receipt?.state == "sending" }.forEach { it.closed = true }
        publish()
    }

    @Synchronized fun begin(request: MessageRequest, mainTarget: Boolean) {
        records.values.filter { it.clientId == request.clientMessageId && it.receipt?.state == "sending" }.forEach { it.closed = true }
        val key = request.session to request.opId
        val previous = records[key]
        if (previous == null || (previous.closed && previous.latestAt == null)) records[key] = Record(request.session, request.opId, request.clientMessageId, mainTarget,
            now(), request)
        records[key]?.request = request
        records[key]?.takeIf { !it.closed && it.receipt == null }?.let {
            it.receipt = MessageSendReceipt(request.session, request.clientMessageId, "sending", Instant.ofEpochMilli(now()), mainTarget)
        }
        publish()
    }

    /** A subscriber can receive another client's receipt, with no local outbox entry at all. */
    @Synchronized fun event(event: MessageSendStateEvent) {
        val at = parseTime(event.at) ?: return
        if (event.state !in setOf("processing", "delivered", "failed", "unknown", "kept")) return
        val session = SessionKey(event.machineId, event.sessionId)
        val key = session to event.clientMessageId
        val record = records.getOrPut(key) {
            Record(session, event.clientMessageId, event.clientMessageId, false, now())
        }
        if (key in completed || record.closed || record.terminal || record.latestAt?.let { at <= it } == true) return
        record.latestAt = at
        record.terminal = event.state != "processing"
        record.delivered = record.delivered || event.state == "delivered"
        // A transcript echo is stronger than a contradictory late refusal, but still is not proof that the model ran.
        val state = if (record.echoedUserSeq != null && event.state in setOf("failed", "unknown")) "delivered" else event.state
        record.receipt = MessageSendReceipt(session, record.clientId, state, at, record.mainTarget)
        publish()
    }

    /** Accepted delivery keeps Processing until runtime evidence; kept is terminal and asks the chat to reread its row. */
    @Synchronized fun result(request: MessageRequest, result: MessageTransportResult) {
        val record = records[request.session to request.opId] ?: return
        if (result == MessageTransportResult.Kept || result == MessageTransportResult.Held) {
            record.receipt = MessageSendReceipt(request.session, record.clientId, "kept", Instant.ofEpochMilli(now()), record.mainTarget)
            record.latestAt = Instant.ofEpochMilli(now())
            record.terminal = true
            record.closed = false
        } else {
            val accepted = result == MessageTransportResult.Accepted || (result as? MessageTransportResult.HubError)?.delivered == "yes"
            record.delivered = record.delivered || accepted
            if (!accepted && record.echoedUserSeq == null) record.closed = true
            // A slow socket consumer may deliver Processing after HTTP success. Remove local Sending, not its correlation.
            if (accepted && record.receipt?.state == "sending") record.receipt = null
        }
        publish()
    }

    @Synchronized fun discard(clientId: String) {
        records.values.filter { it.clientId == clientId }.forEach { it.closed = true }
        publish()
    }

    @Synchronized fun transcript(session: SessionKey, items: List<Item>) {
        val active = records.values.filter { it.session == session && !it.closed }
        val lastReply = items.filter { it.role == "assistant" && it.kind != "tool_result" }.maxOfOrNull { it.seq }
        if (lastReply != null) items.filter { MessageEcho.candidate(it) && it.seq < lastReply }.forEach { item ->
            // Historical replies fence replayed ids; a first segment cannot complete a still-active request.
            if (active.none { matches(it, item) } || items.any { it.kind == "turn_duration" && it.seq > item.seq }) {
                completed += session to item.id
                MessageEcho.tag(item)?.let { completed += session to it }
            }
        }
        while (completed.size > Limit) completed.remove(completed.first())
        active.forEach { record ->
            val user = items.firstOrNull { matches(record, it) }
            user?.let { record.echoedUserSeq = it.seq }
            val after = record.echoedUserSeq
            // A first text/thinking/tool segment is not completion and may beat the runtime-state snapshot.
            if (after != null && items.any { it.kind == "turn_duration" && it.seq > after }) record.closed = true
        }
        publish()
    }

    private fun matches(record: Record, item: Item): Boolean = MessageEcho.carries(item, record.operationId) ||
        MessageEcho.carries(item, record.clientId) || record.request?.let {
            MessageEcho.candidate(item) && MessageEcho.arrived(it.text, record.createdAt, item)
        } == true

    /** Only a changed live runtime snapshot can supersede a receipt; a delayed unchanged Idle baseline cannot. */
    @Synchronized fun sessions(sessions: List<Session>) {
        val next = sessions.associate { it.key to RuntimeEvidence(it.status, it.agentState, it.stateSince, it.queued.orEmpty()) }
        for (session in sessions) {
            val evidence = next.getValue(session.key)
            val previous = runtime[session.key]
            val takeover = session.agentState in setOf(AgentState.Working, AgentState.NeedsYou, AgentState.Offline) ||
                session.queued.orEmpty().isNotEmpty() || !session.startError.isNullOrBlank()
            val finished = previous?.state == AgentState.Working && session.status == "done"
            if (previous != null && evidence != previous && (takeover || finished)) {
                val since = parseTime(session.stateSince)
                records.values.filter {
                    val received = it.latestAt
                    it.session == session.key &&
                        (takeover || it.echoedUserSeq != null || it.delivered) &&
                        (evidence.queued != previous.queued || since == null || received == null || since >= received)
                }.forEach { it.closed = true }
            }
        }
        runtime = next
        publish()
    }

    /** An older running turn may stop while this message is still in hub preflight. */
    @Synchronized fun finishTurn(session: SessionKey) {
        records.values.filter { it.session == session &&
            (it.echoedUserSeq != null || it.delivered) }.forEach { it.closed = true }
        publish()
    }

    /** Reconnects do not replay these subscriber-only receipts; old ones never cross a new connection baseline. */
    @Synchronized fun reconnect() {
        records.values.forEach { it.closed = true }
        publish()
    }

    @Synchronized fun expire() { publish() }

    private fun publish() {
        records.values.filter { now() - it.createdAt >= LifetimeMillis }.forEach { it.closed = true }
        while (records.size > Limit) records.remove(records.keys.first())
        mutable.value = records.values.filterNot { it.closed }.mapNotNull { it.receipt }
    }

    private companion object {
        const val Limit = 256
        const val LifetimeMillis = 15 * 60_000L
    }
}
