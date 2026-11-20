package persona.core.hub

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import okhttp3.HttpUrl.Companion.toHttpUrlOrNull
import okhttp3.Response
import persona.core.contracts.api.ReplyImageSource
import java.io.IOException

class HubReplyImages internal constructor(
    override val features: StateFlow<Set<String>>,
    private val request: suspend (String) -> Response,
    private val direct: suspend (String) -> ByteArray,
    private val isCurrent: () -> Boolean,
) : ReplyImageSource {
    constructor(connection: OkHttpHubConnection, isCurrent: () -> Boolean) : this(
        connection.serverFeatures, { path -> connection.request(path, buffered = false) },
        PublicImageReader.shared::read, isCurrent,
    )

    override suspend fun load(url: String, size: Int, allowDirect: Boolean): ByteArray {
        checkPairing()
        require(url.toByteArray().size <= 4096 && (url.startsWith("https://", true) || url.startsWith("http://", true)))
        val target = requireNotNull(url.toHttpUrlOrNull())
        require(target.username.isEmpty() && target.password.isEmpty())
        if (ReplyImageSource.FEATURE !in features.value) {
            check(allowDirect) { "Remote images unavailable" }
            return direct(url).also { checkPairing() }
        }
        val query = HttpUrl.Builder().scheme("https").host("example.invalid")
            .addQueryParameter("url", url).addQueryParameter("size", size.coerceIn(1, ReplyImageSource.MAX_SIZE).toString()).build().encodedQuery
        return request("/api/v1/remote-image?$query").use { response ->
            if (!response.isSuccessful) throw IOException("Image request failed (${response.code})")
            withContext(Dispatchers.IO) {
                val body = requireNotNull(response.body)
                if (body.contentLength() > MAX_BYTES || body.source().request(MAX_BYTES + 1)) throw IOException("Image too large")
                body.source().buffer.readByteArray().also { checkPairing() }
            }
        }
    }

    private fun checkPairing() {
        if (!isCurrent()) throw CancellationException("Pairing changed")
    }

    private companion object { const val MAX_BYTES = 20L * 1024 * 1024 }
}
