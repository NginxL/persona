package persona.feature.chat

import persona.core.contracts.SessionKey
import persona.core.contracts.api.MessageSender

/** Historical readers cannot enqueue or retry messages, even through an indirect action. */
internal class ReadOnlyMessageSender(source: MessageSender, private val refusal: String) : MessageSender {
    override val outbox = source.outbox
    override val writtenAt = source.writtenAt
    override suspend fun send(session: SessionKey, text: String, clientMessageId: String, clearDraft: String?) {
        throw IllegalStateException(refusal)
    }
    override suspend fun send(
        session: SessionKey, text: String, clientMessageId: String, clearDraft: String?, launcher: String?, model: String?,
    ) {
        throw IllegalStateException(refusal)
    }
    override suspend fun send(
        session: SessionKey, text: String, clientMessageId: String, clearDraft: String?, launcher: String?, model: String?, via: String?,
    ) {
        throw IllegalStateException(refusal)
    }
}
