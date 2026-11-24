package persona.module.voice

import persona.core.contracts.JsonCodec
import persona.core.hub.HubError
import persona.core.hub.HubFailure
import persona.core.hub.awaitResponse
import persona.core.contracts.api.HubExtension
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineDispatcher
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import kotlinx.serialization.SerializationException
import kotlinx.serialization.builtins.ListSerializer
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.contentOrNull
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

/**
 * The hub's voice extensions (docs/extensions/extensions.md "Voice") on one route of one pairing: the extension list, an extension's voices and
 * models, the model configuration of `plugin.<id>` (docs/assistant/model-config.md; the purpose kept its name) and the extension's
 * socket. A hub before #1844 ([legacy]) has these routes only under `/api/v1/plugins`. Calls after the
 * pairing changed ([isCurrent] false) fail with a cancellation, like [HttpMachineMetricsApi].
 */
class HttpVoiceExtensions(
    baseUrl: String,
    private val token: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean = { true },
    private val dispatcher: CoroutineDispatcher = Dispatchers.Default,
    /** The hub answers only under `/api/v1/plugins`, with the key `plugins` (its hello has no `extensions` feature). */
    private val legacy: () -> Boolean = { false },
) : VoiceExtensions {
    private val base = baseUrl.toHttpUrl()
    private val root get() = if (legacy()) "api/v1/plugins" else "api/v1/extensions"
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()

    // A conversation can be quiet for minutes, and the extension speaks whenever the model does: no read timeout, and
    // pings keep the hub's proxy and NATs from dropping the socket in between.
    private val sockets by lazy {
        client.newBuilder().readTimeout(0, TimeUnit.MILLISECONDS).pingInterval(15, TimeUnit.SECONDS).build()
    }

    private fun checkScope() { if (!isCurrent()) throw CancellationException("Pairing changed") }

    private fun extension(id: String, vararg rest: String): HttpUrl {
        // Extension ids are a-z0-9_- (docs/extensions/extensions.md); anything else must not become a path ("..").
        require(id.matches(ExtensionId)) { "Invalid extension id" }
        return base.newBuilder().addPathSegments(root).addPathSegment(id)
            .apply { rest.forEach(::addPathSegment) }.build()
    }

    /** The body as JSON; null for a 404 when [missing] allows it (an older hub or extension). */
    private suspend fun read(url: HttpUrl, missing: Boolean): JsonElement? = withContext(dispatcher) {
        checkScope()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token").build()
        val body = http.newCall(request).awaitResponse().use { response ->
            checkScope()
            if (missing && response.code == 404) return@withContext null
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string() ?: throw HubFailure(HubError.Protocol)
        }
        val json = try { JsonCodec.parseToJsonElement(body) }
        catch (error: SerializationException) { throw HubFailure(HubError.Protocol, cause = error) }
        checkScope()
        json
    }

    /** Decoding errors (SerializationException is one) are the hub's or extension's protocol, not the network's. */
    private inline fun <T> decode(block: () -> T): T = try { block() }
    catch (error: IllegalArgumentException) { throw HubFailure(HubError.Protocol, cause = error) }

    override suspend fun voiceExtension(): HubExtension? {
        val json = read(base.newBuilder().addPathSegments(root).build(), missing = true) ?: return null
        val list = (json as? JsonObject)?.let { it["extensions"] ?: it["plugins"] } as? JsonArray ?: return null
        return decode { JsonCodec.decodeFromJsonElement(ListSerializer(HubExtension.serializer()), list) }
            .firstOrNull { it.isRunningVoice }
    }

    override suspend fun voices(extensionId: String, model: String?): VoiceOptions {
        val url = extension(extensionId, "voices").newBuilder()
            .apply { model?.takeIf { it.isNotBlank() }?.let { addQueryParameter("model", it) } }.build()
        val json = read(url, missing = false)!!
        return decode { JsonCodec.decodeFromJsonElement(VoiceOptions.serializer(), json) }
    }

    override suspend fun models(extensionId: String): VoiceModelOptions {
        val json = read(extension(extensionId, "models"), missing = true) ?: return VoiceModelOptions()
        return decode { JsonCodec.decodeFromJsonElement(VoiceModelOptions.serializer(), json) }
    }

    override suspend fun modelChoice(extensionId: String): VoiceModelChoice? {
        require(extensionId.matches(ExtensionId)) { "Invalid extension id" }
        val url = base.newBuilder().addPathSegments("api/v1/model-config/purposes").addPathSegment("plugin.$extensionId").build()
        val json = read(url, missing = true) ?: return null
        // A newer hub always answers with the purpose; anything else is not "no configuration".
        val purpose = (json as? JsonObject)?.get("purpose") as? JsonObject ?: throw HubFailure(HubError.Protocol)
        val model = ((purpose["effective"] as? JsonObject)?.get("model") as? JsonPrimitive)?.contentOrNull?.trim()
        val status = (purpose["status"] as? JsonPrimitive)?.contentOrNull?.trim()
        return VoiceModelChoice(
            model = model?.takeIf { it.isNotEmpty() },
            // "Unverified" means nobody paid to try it, not that it is broken (docs/assistant/model-config.md).
            status = status?.takeIf { it.isNotEmpty() && it != Unverified },
        )
    }

    override fun connect(extensionId: String, listener: VoiceSocketListener): VoiceSocket {
        checkScope()
        val request = Request.Builder().url(extension(extensionId, "ws")).header("Authorization", "Bearer $token").build()
        // One ending reaches the listener: the extension's close, a failure, or nothing after the page closed it.
        val ended = AtomicBoolean(false)
        val opened = AtomicBoolean(false)
        val socket = sockets.newWebSocket(request, object : WebSocketListener() {
            override fun onOpen(webSocket: WebSocket, response: Response) {
                opened.set(true)
                if (!ended.get()) listener.onOpen()
            }

            override fun onMessage(webSocket: WebSocket, text: String) {
                if (!ended.get()) listener.onText(text)
            }

            override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                if (!ended.get()) listener.onBinary(bytes.toByteArray())
            }

            override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                // Finish the closing handshake; the extension has said all it will say.
                val _ = runCatching { webSocket.close(1000, null) }
                if (ended.compareAndSet(false, true)) listener.onClosed(code, reason)
            }

            override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                if (ended.compareAndSet(false, true)) listener.onClosed(code, reason)
            }

            override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                val status = response?.code
                val error = response?.use { HubFailure.from(it) } ?: HubFailure.from(t)
                // OkHttp does not retry an upgrade on a pooled connection that died behind a forwarder (adb reverse,
                // ssh -L): every later attempt failed the same way until the app restarted (docs/android/android.md).
                if (response == null && !opened.get()) http.connectionPool.evictAll()
                if (ended.compareAndSet(false, true)) listener.onFailure(error, status)
            }
        })
        return object : VoiceSocket {
            override fun send(text: String): Boolean = socket.send(text)
            override fun send(bytes: ByteArray, offset: Int, length: Int): Boolean = socket.send(bytes.toByteString(offset, length))
            override fun close(code: Int, reason: String?) {
                ended.set(true)
                if (!socket.close(code, reason)) socket.cancel()
            }
        }
    }

    private companion object {
        val ExtensionId = Regex("[A-Za-z0-9_-]{1,64}")
        const val Unverified = "未验证" // i18n-ignore: the hub's status token
    }
}
