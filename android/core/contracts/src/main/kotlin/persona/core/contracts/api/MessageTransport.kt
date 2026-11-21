package persona.core.contracts.api

import persona.core.contracts.ProtocolCompatibility

import persona.core.contracts.HubScope
import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.StateFlow
import kotlinx.serialization.json.JsonObject

data class MessageRequest(
    val scope: HubScope,
    val session: SessionKey,
    val text: String,
    val clientMessageId: String,
    val clearDraft: String? = null,
    val todoId: String? = null,
    /**
     * The operation id this attempt goes under (X-Persona-Op-Id, and the body's client_msg_id): the message's own id
     * until the hub answered an attempt as not delivered, then a new one (the hub replays the first answer for an id).
     */
    val opId: String = clientMessageId,
    /** Persona conversation overrides carried by this message; absent fields keep the hub's default. */
    val launcher: String? = null,
    val model: String? = null,
    /** The user's message that carries out the session's waiting launcher switch first (`apply_switch`, #5035). */
    val applySwitch: Boolean = false,
    /** External provenance ([MessageVia]); null for the user's typed words. Restored and retried per message. */
    val via: String? = null,
) {
    val headers: Map<String, String> get() = ProtocolCompatibility.operationHeaders(opId) + ("X-Persona-Features" to AgentMessageHoldFeature)
}

sealed interface MessageTransportResult {
    data object Accepted : MessageTransportResult
    /** The assistant kept the message to send later; its session row, not the local outbox, draws it now. */
    data object Kept : MessageTransportResult
    /** An offline Agent's manager has custody. Only a later transcript echo proves arrival. */
    data object Held : MessageTransportResult
    /** Only use this when no request bytes could have reached the hub. */
    data object NotSent : MessageTransportResult
    data object NoReceipt : MessageTransportResult
    /** Mirrors the unified hub error body; absent fields remain unknown. */
    data class HubError(
        val code: String? = null,
        val delivered: String? = null,
        val error: String? = null,
        val retryable: Boolean = false,
    ) : MessageTransportResult
}

/** Implemented by core:hub and bound to the supplied scope, never to mutable global credentials. */
interface MessageTransport {
    /** True only after the connection's initial catch-up has completed. */
    val readyToSend: StateFlow<Boolean>
    val supportsClientOps: Boolean
    /** Forward request.headers, including X-Persona-Op-Id, on every attempt. */
    suspend fun send(request: MessageRequest): MessageTransportResult
    /** Shared by transports/decorators after confirmed success, including a first message's returned session. */
    suspend fun onAccepted(session: SessionKey, reply: JsonObject?) = Unit
    /** Read authoritative evidence even before readyToSend is released. Null means unresolved. */
    suspend fun reconcile(request: MessageRequest, createdAt: Long, allowTextMatch: Boolean = true): MessageEvidence? = null
}

sealed interface MessageEvidence {
    data object Shown : MessageEvidence
    data class Receipt(val result: MessageTransportResult) : MessageEvidence
}
