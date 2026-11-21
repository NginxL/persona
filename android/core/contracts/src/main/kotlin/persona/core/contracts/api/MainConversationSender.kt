package persona.core.contracts.api

import persona.core.contracts.SessionKey
import kotlinx.coroutines.flow.StateFlow

/** Local outbox destination until a main conversation is durably bound. Never send it to the hub. */
val PendingMainConversation = SessionKey("", "main")

/** Shared entry point for sharing, asking Persona and handing context to Persona. */
interface MainConversationSender : MessageSendReceiptSource {
    val outbox: StateFlow<List<OutboxMessage>>
    /** Commits locally before returning; a failed call must leave the editor intact. */
    suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot? = null)
    /** External text keeps its provenance; legacy senders reject a mark rather than dropping it. */
    suspend fun sendToMain(text: String, clientMessageId: String, draft: DraftSnapshot?, via: String?) {
        require(via == null) { "Message provenance is not supported" }
        sendToMain(text, clientMessageId, draft)
    }
    /**
     * The hub put the main conversation [previous] away ("New main chat"): forget it as the main conversation, so the next
     * message to the main conversation starts a new one instead of going to the archived one.
     */
    suspend fun mainReplaced(previous: SessionKey) = Unit
    /**
     * The hub's main conversation may have changed elsewhere (Settings › Assistant moved the assistant to another machine, another
     * device started a new one): ask the hub, and forget the local one when the hub names another or none, so the next
     * message goes where the hub says instead of to a conversation that is an ordinary one now.
     */
    suspend fun mainChanged() = Unit
}

/** Bind to one immutable pairing, like MessageTransport. */
interface MainConversationResolver {
    suspend fun findMainConversation(): SessionKey?
    /** The caller journals this attempt before invoking a legacy, non-idempotent endpoint. */
    suspend fun createMainConversation(): SessionKey
    /** A share/intent-created main carries the same mark as the message that follows. */
    suspend fun createMainConversation(via: String?): SessionKey {
        require(via == null) { "Message provenance is not supported" }
        return createMainConversation()
    }
}
