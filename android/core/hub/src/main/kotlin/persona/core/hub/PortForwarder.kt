package persona.core.hub

import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.net.InetAddress
import java.net.InetSocketAddress
import java.net.ServerSocket
import java.net.Socket
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.atomic.AtomicBoolean
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import okhttp3.HttpUrl
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import okio.ByteString.Companion.toByteString

/**
 * A machine's port on the phone's loopback (docs/operations/ports.md "Forwarding", iOS `PortForwarder`): the app listens on 127.0.0.1
 * and ::1 at the same port number when it is free (another one when not), and each connection the in-app browser makes
 * there becomes one WebSocket to the hub (`…/ports/{port}/connect`, the device token) carrying its bytes to the port on
 * the machine. The page loads from http://localhost:<port>/ exactly as on the machine: its absolute paths, its own
 * WebSocket (HMR), redirects and cookies need nothing rewritten. Only loopback is bound: nothing on the LAN reaches it.
 *
 * [connect] gives the hub's base URL and the token for each new connection (the live route, so a network change is
 * followed); listeners and their connections last until [close], and one found closed is opened again when next asked for.
 */
class PortForwarder(
    private val client: OkHttpClient,
    private val connect: () -> Pair<HttpUrl, String>,
) {
    data class Key(val machineId: String, val port: Int)

    private val forwards = ConcurrentHashMap<Key, Forward>()
    private val pumps = ConcurrentHashMap.newKeySet<Pump>()
    private val sockets = client.newBuilder().readTimeout(0, java.util.concurrent.TimeUnit.MILLISECONDS).build()

    /** The phone's port for a machine's port, listening from now on. */
    suspend fun localPort(key: Key): Int = withContext(Dispatchers.IO) {
        synchronized(forwards) {
            forwards[key]?.takeIf { it.open }?.let { return@withContext it.localPort }
            forwards.remove(key)?.close()
            val taken = forwards.values.map { it.localPort }.toSet()
            val forward = Forward(key, if (key.port in taken) 0 else key.port)
            forwards[key] = forward
            forward.localPort
        }
    }

    /** Which machine port a page on the phone's loopback is (the sheet names it). */
    fun keyOf(localPort: Int): Key? = forwards.values.firstOrNull { it.localPort == localPort && it.open }?.key

    /** Frees every port (the next pairing binds the same numbers) and ends the connections through them; never throws. */
    fun close() {
        synchronized(forwards) {
            forwards.values.forEach { it.close() }
            forwards.clear()
        }
        pumps.toList().forEach { it.cancel() }
    }

    /** The hub's WebSocket for one connection to [key]. */
    internal fun connectRequest(key: Key): Request {
        val (base, token) = connect()
        // After the hub URL's own path, as every route (a hub behind a path prefix keeps it).
        val url = base.newBuilder().addPathSegments("api/v1/machines").addPathSegment(key.machineId)
            .addPathSegment("ports").addPathSegment(key.port.toString()).addPathSegment("connect").build()
        return Request.Builder().url(url).header("Authorization", "Bearer $token").build()
    }

    /** One forwarded port: its two listeners and the connections through them. */
    private inner class Forward(val key: Key, preferred: Int) {
        private val listeners = mutableListOf<ServerSocket>()
        private val acceptors = mutableListOf<Thread>()
        val localPort: Int
        val open: Boolean get() = listeners.isNotEmpty() && listeners.none { it.isClosed }

        init {
            val v4 = try { bind(LOOPBACK_V4, preferred) } catch (e: IOException) {
                if (preferred == 0) throw e else bind(LOOPBACK_V4, 0) // taken on the phone: another number
            }
            localPort = v4.localPort
            listeners += v4
            // localhost resolves to ::1 first; without it the WebView falls back to 127.0.0.1.
            runCatching { bind(LOOPBACK_V6, localPort) }.getOrNull()?.let(listeners::add)
            listeners.forEach { server ->
                acceptors += Thread({ accept(server) }, "port-${key.port}-accept").apply { isDaemon = true; start() }
            }
        }

        private fun bind(host: InetAddress, port: Int) = ServerSocket().apply {
            reuseAddress = true
            bind(InetSocketAddress(host, port), 50)
        }

        private fun accept(server: ServerSocket) {
            while (!server.isClosed) {
                val conn = try { server.accept() } catch (_: IOException) { break }
                runCatching { Pump(conn, connectRequest(key)).start() }.onFailure { val _ = runCatching { conn.close() } }
            }
        }

        /**
         * Closes the listeners and waits for their accept threads to end. Closing a socket a thread is blocked accepting
         * on only wakes that thread; the port is released once it leaves accept, so returning earlier let the next
         * pairing's forwarder find the number still taken and bind another one (#3483) whenever the CPU was busy.
         */
        fun close() {
            listeners.forEach { val _ = runCatching { it.close() } }
            listeners.clear()
            val deadline = System.nanoTime() + CLOSE_WAIT_NANOS
            acceptors.filter { it !== Thread.currentThread() }.forEach { thread ->
                val left = (deadline - System.nanoTime()) / 1_000_000
                if (left > 0) { val _ = runCatching { thread.join(left) } }
            }
            acceptors.clear()
        }
    }

    /**
     * One browser connection and its WebSocket to the hub. Bytes from the machine are written to the browser on
     * OkHttp's reader thread, so a slow browser holds the reads; bytes from the browser are read only while the
     * socket's send queue is short, so a slow hub holds the browser. Neither side piles up in memory.
     */
    private inner class Pump(private val conn: Socket, private val request: Request) : WebSocketListener() {
        private val done = AtomicBoolean(false)
        private val output: OutputStream = conn.getOutputStream()
        @Volatile private var socket: WebSocket? = null

        fun start() {
            conn.tcpNoDelay = true
            pumps += this
            socket = sockets.newWebSocket(request, this)
        }

        fun cancel() { val _ = runCatching { socket?.cancel() }; finish() }

        override fun onOpen(webSocket: WebSocket, response: Response) {
            Thread({ readLocal(webSocket, conn.getInputStream()) }, "port-pump").apply { isDaemon = true }.start()
        }

        private fun readLocal(ws: WebSocket, input: InputStream) {
            val buffer = ByteArray(CHUNK)
            try {
                while (!done.get()) {
                    while (ws.queueSize() > MAX_QUEUED && !done.get()) Thread.sleep(5)
                    val n = input.read(buffer)
                    if (n < 0) break
                    if (n > 0 && !ws.send(buffer.toByteString(0, n))) break
                }
            } catch (_: IOException) {
            } catch (_: InterruptedException) {
            }
            // The browser is done sending: the machine's side gets the end and finishes its answer, then closes.
            ws.close(1000, null)
        }

        override fun onMessage(webSocket: WebSocket, bytes: ByteString) = write(webSocket) { bytes.write(output) }
        override fun onMessage(webSocket: WebSocket, text: String) = write(webSocket) { output.write(text.toByteArray()) }

        private inline fun write(webSocket: WebSocket, block: () -> Unit) {
            try { block(); output.flush() } catch (_: IOException) { webSocket.cancel(); finish() }
        }

        // The machine's side ended: what it sent is already written, then the browser sees the end.
        override fun onClosing(webSocket: WebSocket, code: Int, reason: String) { webSocket.close(1000, null); finish() }
        override fun onClosed(webSocket: WebSocket, code: Int, reason: String) = finish()
        override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) = finish()

        private fun finish() {
            if (done.compareAndSet(false, true)) { pumps -= this; val _ = runCatching { conn.close() } }
        }
    }

    private companion object {
        val LOOPBACK_V4: InetAddress = InetAddress.getByAddress("localhost", byteArrayOf(127, 0, 0, 1))
        val LOOPBACK_V6: InetAddress = InetAddress.getByAddress("localhost", ByteArray(16).also { it[15] = 1 })
        const val CHUNK = 64 * 1024
        /** Unsent bytes a connection may queue before it stops reading from the browser. */
        const val MAX_QUEUED = 512L * 1024
        /** How long close waits for the accept threads to leave, so their ports are free again. */
        const val CLOSE_WAIT_NANOS = 1_000_000_000L
    }
}
