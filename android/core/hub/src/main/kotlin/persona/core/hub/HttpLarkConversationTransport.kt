package persona.core.hub

import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.LarkConversationTransport
import persona.core.contracts.api.LarkMention
import persona.core.contracts.api.LarkThreadTransport
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.json.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody

class HttpLarkConversationTransport(
    private val delegate: InboxAssistantTransport,
    baseUrl: String,
    private val token: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean,
) : LarkConversationTransport, LarkThreadTransport, LarkThreadReplyTransport, InboxAssistantTransport by delegate {
    override suspend fun replyInThread(messageId: String, text: String, clientId: String) =
        (delegate as LarkThreadReplyTransport).replyInThread(messageId, text, clientId)
    override suspend fun replyInThread(messageId: String, text: String, clientId: String, mentions: List<LarkMention>) =
        (delegate as LarkThreadReplyTransport).replyInThread(messageId, text, clientId, mentions)
    override suspend fun thread(messageId: String, before: String?) =
        (delegate as LarkThreadTransport).thread(messageId, before)
    private val base = baseUrl.toHttpUrl()
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()

    override suspend fun markRead(feedId: String, position: Long, badgeCount: Long?, messageIds: List<String>) {
        require(feedId.isNotBlank() && position >= 0)
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().addPathSegments("api/v1/lark/conversations")
            .addPathSegment(feedId).addPathSegment("read").build().withRequestManager()
        val body = buildJsonObject {
            // The extension (and the web client) carry positions as strings; a number fails to decode there and
            // every read came back 400 "position is required", so no Feishu message was ever marked read from Android.
            put("position", position.toString())
            badgeCount?.let { put("badge_count", it) }
            putJsonArray("message_ids") { messageIds.distinct().forEach { add(it) } }
        }
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .post(body.toString().toRequestBody("application/json".toMediaType())).build()
        http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) throw HubFailure.from(it)
        }
    }

    override suspend fun react(messageId: String, key: String, remove: Boolean) {
        require(messageId.isNotBlank() && key.isNotBlank())
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().addPathSegments("api/v1/lark/messages")
            .addPathSegment(messageId).addPathSegment("reactions").build().withRequestManager()
        val body = buildJsonObject {
            put("key", key)
            put("remove", remove)
        }
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .post(body.toString().toRequestBody("application/json".toMediaType())).build()
        http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) throw HubFailure.from(it)
        }
    }

    override suspend fun cardAction(messageId: String, actionId: String) {
        require(messageId.isNotBlank() && actionId.isNotBlank())
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().addPathSegments("api/v1/lark/messages")
            .addPathSegment(messageId).addPathSegments("card-actions").addPathSegment(actionId).build().withRequestManager()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .post("{}".toRequestBody("application/json".toMediaType())).build()
        http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) throw HubFailure.from(it)
        }
    }

    override suspend fun userProfile(userId: String, feedId: String?): JsonObject {
        require(userId.isNotBlank())
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val urlBuilder = base.newBuilder().addPathSegments("api/v1/lark/users")
            .addPathSegment(userId)
        if (!feedId.isNullOrBlank()) urlBuilder.addQueryParameter("feed_id", feedId)
        val request = Request.Builder().url(urlBuilder.build().withRequestManager()).header("Authorization", "Bearer $token").get().build()
        return http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) throw HubFailure.from(it)
            val raw = it.body?.string().orEmpty()
            Json.parseToJsonElement(raw.ifBlank { "{}" }).jsonObject
        }
    }

    override suspend fun quickReactions(): List<String> {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().addPathSegments("api/v1/lark/reactions/quick").build()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token").get().build()
        return http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) return@use emptyList()
            val raw = it.body?.string().orEmpty()
            val obj = Json.parseToJsonElement(raw.ifBlank { "{}" }).jsonObject
            (obj["keys"] as? JsonArray)?.mapNotNull { el -> (el as? JsonPrimitive)?.contentOrNull } ?: emptyList()
        }
    }

    override suspend fun emojis(): JsonObject {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().addPathSegments("api/v1/lark/emojis").build()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token").get().build()
        return http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) return@use JsonObject(emptyMap())
            val raw = it.body?.string().orEmpty()
            Json.parseToJsonElement(raw.ifBlank { "{}" }).jsonObject
        }
    }

    override suspend fun messageDisplay(): JsonObject {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().addPathSegments("api/v1/lark/message-display").build()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token").get().build()
        return http.newCall(request).awaitResponse().use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!it.isSuccessful) throw HubFailure.from(it)
            Json.parseToJsonElement(it.body?.string().orEmpty().ifBlank { "{}" }).jsonObject
        }
    }
}
