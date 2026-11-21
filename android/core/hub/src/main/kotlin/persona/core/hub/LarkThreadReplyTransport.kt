package persona.core.hub

import kotlinx.serialization.json.JsonObject
import persona.core.contracts.api.LarkMention

/** Repeating an identical request reconciles the durable hub receipt by client ID. */
interface LarkThreadReplyTransport {
    suspend fun replyInThread(messageId: String, text: String, clientId: String): JsonObject
    suspend fun replyInThread(messageId: String, text: String, clientId: String, mentions: List<LarkMention>): JsonObject {
        require(mentions.isEmpty()) { "Lark mentions unavailable" }
        return replyInThread(messageId, text, clientId)
    }
}
