package persona.core.hub.dev

import java.util.concurrent.atomic.AtomicBoolean
import okhttp3.Interceptor
import okhttp3.MediaType
import okhttp3.RequestBody
import okhttp3.Response
import okhttp3.ResponseBody
import okio.Buffer
import okio.BufferedSink
import okio.BufferedSource
import okio.ForwardingSink
import okio.ForwardingSource
import okio.Sink
import okio.Source
import okio.buffer
import persona.core.contracts.DevProbe

/** Measures route, status, elapsed time and actual streamed body bytes. Headers, queries and bodies are never retained. */
class DevHttpInterceptor : Interceptor {
    override fun intercept(chain: Interceptor.Chain): Response {
        val request = chain.request()
        // The socket listener owns its upgrade and frames; counting it here as well would double every socket connection.
        if (request.header("Upgrade").equals("websocket", ignoreCase = true)) return chain.proceed(request)
        val started = System.nanoTime()
        val requestBody = request.body?.let(::CountingRequestBody)
        val outgoing = if (requestBody == null) request else request.newBuilder().method(request.method, requestBody).build()
        var reported = false
        fun report(status: Int?, bytesIn: Long) {
            if (reported) return
            reported = true
            DevProbe.request(
                request.method, request.url.toString(), status, bytesIn,
                requestBody?.bytesWritten ?: 0, (System.nanoTime() - started) / 1_000_000,
            )
        }
        return try {
            val response = chain.proceed(outgoing)
            val body = requireNotNull(response.body)
            val counted = CountingResponseBody(body) { bytesIn -> report(response.code, bytesIn) }
            response.newBuilder().body(counted).build()
        } catch (error: Exception) {
            report(null, 0)
            throw error
        }
    }

    private class CountingRequestBody(private val delegate: RequestBody) : RequestBody() {
        var bytesWritten: Long = 0
            private set
        override fun contentType(): MediaType? = delegate.contentType()
        override fun contentLength(): Long = delegate.contentLength()
        override fun isOneShot(): Boolean = delegate.isOneShot()
        override fun isDuplex(): Boolean = delegate.isDuplex()
        override fun writeTo(sink: BufferedSink) {
            val counting = object : ForwardingSink(sink) {
                override fun write(source: Buffer, byteCount: Long) {
                    bytesWritten += byteCount
                    super.write(source, byteCount)
                }
            }.buffer()
            delegate.writeTo(counting)
            counting.flush()
        }
    }

    private class CountingResponseBody(
        private val delegate: ResponseBody,
        private val finished: (Long) -> Unit,
    ) : ResponseBody() {
        private var bytes = 0L
        private val done = AtomicBoolean()
        private val counting = object : ForwardingSource(delegate.source()) {
            override fun read(sink: Buffer, byteCount: Long): Long {
                val read = super.read(sink, byteCount)
                if (read > 0) bytes += read else if (read == -1L) finish()
                return read
            }
        }
        private val buffered = counting.buffer()
        override fun contentType(): MediaType? = delegate.contentType()
        override fun contentLength(): Long = delegate.contentLength()
        override fun source(): BufferedSource = buffered
        override fun close() {
            try { delegate.close() } finally { finish() }
        }
        private fun finish() {
            if (done.compareAndSet(false, true)) finished(bytes)
        }
    }
}
