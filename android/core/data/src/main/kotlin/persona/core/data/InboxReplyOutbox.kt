package persona.core.data

import persona.core.contracts.SessionKey
import persona.core.contracts.api.LarkMention
import persona.feature.inbox.InboxHandOffRequest
import persona.feature.inbox.ReplyRequest
import persona.feature.inbox.ReplyTarget
import kotlinx.coroutines.Deferred
import kotlinx.serialization.Serializable
import kotlin.coroutines.AbstractCoroutineContextElement
import kotlin.coroutines.CoroutineContext

/** AI-authored drafts require confirmation; the flag is omitted on disk when false. */
@Serializable
internal data class InboxStoredDraft(
    val text: String,
    val clientId: String,
    val version: String? = null,
    val ai: Boolean = false,
    val mentions: List<LarkMention> = emptyList(),
)

@Serializable
internal data class InboxPendingReply(
    val target: ReplyTarget,
    val request: ReplyRequest,
    val feedId: String? = null,
    val state: String = "sending",
    val serverId: String? = null,
    val error: String? = null,
    /** The server's copy has been seen in a chat window: the entry stays only as the receipt for dedupe (#4687). */
    val seen: Boolean = false,
) {
    // A mail target change is a definite refusal: nothing was sent and it needs a new identifier.
    val outboxState: OutboxState get() = if (state == "waiting") OutboxState.WaitingForNetwork
        else if (state == "target_changed") OutboxState.Refused
        else OutboxState.entries.firstOrNull { it.wire == state } ?: OutboxState.Unknown
}

/** Additional durable queues join the public outbox's catch-up boundary. */
fun interface OutboxReconciler { suspend fun onReady() }

/**
 * The internal half of an Agent hand-off, committed before its message is enqueued. [scope] is the originating inbox,
 * not the target Agent's pairing. The item route is captured while its ownership is known so replay never resolves an
 * unloaded manager-aware item through the default manager. Delivered entries remain until the hub record succeeds;
 * recorded entries are idempotence tombstones for later evidence and caller retries.
 */
@Serializable
internal data class InboxAgentHandOff(
    val scope: String,
    val request: InboxHandOffRequest,
    val hubItemId: String,
    val manager: String? = null,
    val delivered: Boolean = false,
    val recorded: Boolean = false,
    /** Target outbox journals which can independently prove delivery when the origin reopens. */
    val deliveryScopes: Set<String> = emptySet(),
) {
    val session: SessionKey get() = SessionKey(checkNotNull(request.machineId), checkNotNull(request.sessionId))
}

/** Crosses the app's grouped MessageSender facade without adding hand-off metadata to its public message contract. */
internal class InboxAgentHandOffContext(
    val handOff: InboxAgentHandOff,
    /** Lazy so fakes and same-pairing stores do no redundant IO; a cross-pairing Room store awaits it before enqueue. */
    val originPersisted: Deferred<Unit>? = null,
) : AbstractCoroutineContextElement(InboxAgentHandOffContext) {
    companion object Key : CoroutineContext.Key<InboxAgentHandOffContext>
}

@Serializable
internal data class InboxReplyDocument(
    val drafts: Map<String, InboxStoredDraft> = emptyMap(),
    val outbox: List<InboxPendingReply> = emptyList(),
    val agentHandOffs: List<InboxAgentHandOff> = emptyList(),
) {
    /** Both reply entry points share one durable, ordered queue and immutable request identity. */
    fun enqueue(reply: InboxPendingReply): InboxReplyDocument {
        val previous = outbox.firstOrNull { it.request.clientId == reply.request.clientId }
        require(previous == null || previous.request == reply.request && previous.target == reply.target) {
            "Reply identifier is already in use"
        }
        if (previous?.state == "sent") return this
        return copy(outbox = if (previous == null) outbox + reply else outbox.map {
            if (it.request.clientId == reply.request.clientId) reply else it
        })
    }

    fun enqueue(handOff: InboxAgentHandOff): InboxReplyDocument {
        val previous = agentHandOffs.firstOrNull { it.request.operationId == handOff.request.operationId }
        require(previous == null || previous.scope == handOff.scope && previous.request == handOff.request &&
            previous.hubItemId == handOff.hubItemId && previous.manager == handOff.manager) {
            "Hand-off identifier is already in use"
        }
        // A caller retry keeps the original delivery and recording state.
        return if (previous == null) copy(agentHandOffs = agentHandOffs + handOff) else this
    }

    /** Persist the target's journal location before its outbox row can produce delivery evidence. */
    fun linkAgentHandOffTarget(clientId: String, targetScope: String): InboxReplyDocument = copy(
        agentHandOffs = agentHandOffs.map {
            if (it.request.operationId == clientId) it.copy(deliveryScopes = it.deliveryScopes + targetScope) else it
        },
    )

    /** Merge a moved journal entry without ever turning a delivery or completion tombstone back off. */
    fun mergeAgentHandOff(handOff: InboxAgentHandOff): InboxReplyDocument {
        val previous = agentHandOffs.firstOrNull { it.request.operationId == handOff.request.operationId }
        require(previous == null || previous.scope == handOff.scope && previous.request == handOff.request &&
            previous.hubItemId == handOff.hubItemId && previous.manager == handOff.manager) {
            "Hand-off identifier is already in use"
        }
        if (previous == null) return copy(agentHandOffs = agentHandOffs + handOff)
        val merged = previous.copy(
            delivered = previous.delivered || handOff.delivered,
            recorded = previous.recorded || handOff.recorded,
            deliveryScopes = previous.deliveryScopes + handOff.deliveryScopes,
        )
        return copy(agentHandOffs = agentHandOffs.map {
            if (it.request.operationId == handOff.request.operationId) merged else it
        })
    }

    /** A target's authoritative evidence credits the origin without replacing its committed item route. */
    fun commitAgentHandOffDelivery(handOff: InboxAgentHandOff): InboxReplyDocument {
        val delivered = handOff.copy(delivered = true)
        val previous = agentHandOffs.firstOrNull { it.request.operationId == delivered.request.operationId }
        require(previous == null || previous.scope == delivered.scope && previous.request == delivered.request &&
            previous.hubItemId == delivered.hubItemId && previous.manager == delivered.manager) {
            "Hand-off identifier is already in use"
        }
        if (previous == null) return copy(agentHandOffs = agentHandOffs + delivered)
        val merged = previous.copy(
            delivered = true,
            recorded = previous.recorded || delivered.recorded,
            deliveryScopes = previous.deliveryScopes + delivered.deliveryScopes,
        )
        return copy(agentHandOffs = agentHandOffs.map {
            if (it.request.operationId == delivered.request.operationId) merged else it
        })
    }

    /** A failed local enqueue leaves no hand-off that later, unrelated evidence could release. */
    fun removeUndeliveredAgentHandOff(clientId: String): InboxReplyDocument = copy(
        agentHandOffs = agentHandOffs.filter {
            it.request.operationId != clientId || it.delivered || it.recorded
        },
    )

    fun transition(clientId: String, state: String, error: String? = null): InboxReplyDocument = copy(outbox = outbox.map {
        if (it.request.clientId == clientId && it.state != "sent") it.copy(state = state, error = error) else it
    })

    fun confirm(clientId: String, serverId: String? = null, seen: Boolean = false): InboxReplyDocument = copy(
        drafts = drafts.filterValues { it.clientId != clientId },
        outbox = outbox.map { reply -> if (reply.request.clientId == clientId)
            reply.copy(state = "sent", serverId = serverId ?: reply.serverId, seen = seen || reply.seen) else reply },
    )
}
