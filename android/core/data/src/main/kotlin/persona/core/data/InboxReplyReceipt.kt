package persona.core.data

import persona.core.inbox.ReplyTarget
import kotlinx.serialization.json.*
import java.security.MessageDigest
import java.util.Base64
import java.util.UUID
import java.nio.ByteBuffer

data class InboxReplyReceipt(val state: OutboxState, val serverId: String? = null, val error: String? = null)

fun inboxReplyReceiptId(target: ReplyTarget, clientId: String): String {
    val kind = when (target) {
        is ReplyTarget.Inbox -> "inbox_reply"
        is ReplyTarget.Lark -> if (target.feedId.startsWith("thread:")) "lark_thread_reply" else "lark_send"
    }
    val bytes = MessageDigest.getInstance("SHA-256").digest(persona.core.contracts.ProtocolCompatibility.replyReceiptSeed(kind, clientId).toByteArray())
    bytes[6] = ((bytes[6].toInt() and 15) or 128).toByte()
    bytes[8] = ((bytes[8].toInt() and 63) or 128).toByte()
    val buffer = ByteBuffer.wrap(bytes)
    return UUID(buffer.long, buffer.long).toString()
}

/** Missing receipts never prove that an upstream send did not happen. */
fun inboxReplyReceipt(document: JsonObject): InboxReplyReceipt {
    fun JsonObject.text(key: String) = (this[key] as? JsonPrimitive)?.contentOrNull
    val result = document["result"] as? JsonObject
    val status = result?.text("status")?.toIntOrNull()
    val body = result?.text("body")?.let {
        runCatching { Json.parseToJsonElement(Base64.getDecoder().decode(it).decodeToString()).jsonObject }.getOrNull()
    }
    val state = when {
        document.text("state") == "done" && status in 200..299 && body?.text("state") !in setOf("pending", "unknown") -> OutboxState.Sent
        document.text("state") == "failed" -> OutboxState.Refused
        else -> OutboxState.Unknown
    }
    return InboxReplyReceipt(state, (body?.get("message") as? JsonObject)?.text("id") ?: body?.text("message_id") ?: body?.text("id"),
        body?.text("error"))
}
