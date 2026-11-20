package persona.core.data.dev

import java.io.Closeable
import java.time.Duration
import java.time.Instant
import java.util.concurrent.ExecutorService
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlin.math.roundToInt
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import persona.core.contracts.DevProbeSink
import persona.core.contracts.DevScrub
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionErrorReport

data class DevDeviceState(
    val thermal: String = "nominal",
    val battery: Double? = null,
    val lowPower: Boolean = false,
    val batteryState: String = "unknown",
)

data class DevNetPath(val type: String = "none", val expensive: Boolean = false, val constrained: Boolean = false)

/**
 * In-memory counters for Developing Persona. The platform owner calls [roll] once a minute in front and [lifecycle]
 * on a foreground change; there is no background timer. Writes are batched on one utility thread, crashes synchronously.
 */
class DevRecorder(
    val store: DevStore,
    private val clock: () -> Instant,
    private val cpuMillis: () -> Long,
    private val build: String,
    private val writer: ExecutorService = Executors.newSingleThreadExecutor { runnable ->
        Thread(runnable, "persona-dev-writer").apply { isDaemon = true }
    },
) : DevProbeSink, Closeable {
    private data class Route(val method: String, val pattern: String)
    private data class RouteStat(
        var count: Int = 0,
        var bytesIn: Long = 0,
        var bytesOut: Long = 0,
        val millis: MutableList<Long> = mutableListOf(),
        val status: MutableMap<String, Int> = mutableMapOf(),
    )
    private data class Tally(
        val from: Instant,
        val cpu: Long,
        val device: DevDeviceState,
        val background: Boolean,
        val routes: MutableMap<Route, RouteStat> = mutableMapOf(),
        val frames: MutableMap<String, Int> = mutableMapOf(),
        var frameBytes: Long = 0,
        var connects: Int = 0,
        var drops: Int = 0,
        val pages: MutableMap<String, Long> = mutableMapOf(),
    ) {
        val requests: Int get() = routes.values.sumOf { it.count }
        val bytesIn: Long get() = routes.values.sumOf { it.bytesIn }
        val bytesOut: Long get() = routes.values.sumOf { it.bytesOut }
        val frameCount: Int get() = frames.values.sum()
    }
    private data class State(
        var on: Boolean = false,
        var generation: Long = 0,
        var active: Boolean = false,
        var minute: Tally? = null,
        var period: Tally? = null,
        var page: String = "launch",
        var pageSince: Instant = Instant.EPOCH,
        var path: DevNetPath = DevNetPath(),
        val buffer: MutableList<String> = mutableListOf(),
        val errorsNoted: MutableSet<String> = mutableSetOf(),
    )

    private val lock = Any()
    private var state = State()

    val isOn: Boolean get() = synchronized(lock) { state.on }

    fun start(active: Boolean, page: String, device: DevDeviceState) {
        val now = clock()
        val cpu = cpuMillis()
        synchronized(lock) {
            val s = state
            if (s.on) return
            s.on = true
            s.active = active
            s.minute = Tally(now, cpu, device, !active)
            s.period = if (active) Tally(now, cpu, device, false) else null
            s.page = DevScrub.word(page)
            s.pageSince = now
            s.buffer += line(now, "app", mapOf("event" to JsonPrimitive("recording"), "active" to JsonPrimitive(active), "build" to JsonPrimitive(build)))
        }
    }

    /** Drops what was not written and invalidates every task authorized by the previous recording generation. */
    fun stop() = synchronized(lock) { state = State(generation = state.generation + 1) }

    fun lifecycle(active: Boolean, device: DevDeviceState) {
        val now = clock()
        val cpu = cpuMillis()
        val lines = synchronized(lock) {
            val s = state
            if (!s.on || s.active == active) return
            s.active = active
            settlePage(s, now)
            val out = mutableListOf<String>()
            s.minute?.takeIf { it.background == active }?.let { out += close(it, now, cpu, device, s) }
            if (active) {
                if (s.minute?.background != false) s.minute = Tally(now, cpu, device, false)
                s.period = Tally(now, cpu, device, false)
                out += line(now, "app", mapOf("event" to JsonPrimitive("foreground")))
            } else {
                s.period?.let { out += foreground(it, now, cpu, device) }
                s.period = null
                if (s.minute?.background != true) s.minute = Tally(now, cpu, device, true)
                out += line(now, "app", mapOf("event" to JsonPrimitive("background")))
            }
            s.errorsNoted.clear()
            out.toList()
        }
        append(lines, flush = true)
    }

    fun roll(device: DevDeviceState) {
        val now = clock()
        val cpu = cpuMillis()
        val lines = synchronized(lock) {
            val s = state
            if (!s.on || s.minute == null) return
            settlePage(s, now)
            val current = s.minute!!
            val out = close(current, now, cpu, device, s)
            s.minute = Tally(now, cpu, device, current.background)
            s.errorsNoted.clear()
            listOf(out)
        }
        append(lines, flush = true)
    }

    override fun page(name: String) {
        val now = clock()
        val clean = DevScrub.word(name)
        val lines = synchronized(lock) {
            val s = state
            if (!s.on || clean == s.page) return
            settlePage(s, now)
            s.page = clean
            listOf(line(now, "page", mapOf("page" to JsonPrimitive(clean))))
        }
        append(lines)
    }

    fun device(kind: String, fields: Map<String, Any?>) {
        val now = clock()
        val page = synchronized(lock) { if (state.on) state.page else null } ?: return
        val clean = fields.mapValues { jsonValue(it.value) } + ("page" to JsonPrimitive(page))
        append(listOf(line(now, DevScrub.word(kind), clean)))
    }

    fun path(path: DevNetPath) {
        val now = clock()
        val lines = synchronized(lock) {
            val s = state
            if (!s.on || path == s.path) return
            s.path = path
            listOf(line(now, "path", mapOf(
                "type" to JsonPrimitive(DevScrub.word(path.type)),
                "expensive" to JsonPrimitive(path.expensive),
                "constrained" to JsonPrimitive(path.constrained),
            )))
        }
        append(lines)
    }

    override fun request(method: String, url: String?, status: Int?, bytesIn: Long, bytesOut: Long, millis: Long) {
        val route = Route(DevScrub.word(method.uppercase()), DevScrub.route(url ?: "/"))
        val code = status?.toString() ?: "failed"
        val now = clock()
        val lines = synchronized(lock) {
            val s = state
            if (!s.on) return
            count(s.minute, route, code, bytesIn, bytesOut, millis)
            count(s.period, route, code, bytesIn, bytesOut, millis)
            if (status != null && status < 400) return
            val key = "${route.method} ${route.pattern} $code"
            if (!s.errorsNoted.add(key)) return
            listOf(line(now, "request_error", mapOf(
                "route" to JsonPrimitive("${route.method} ${route.pattern}"),
                "status" to JsonPrimitive(code), "page" to JsonPrimitive(s.page),
            )))
        }
        append(lines)
    }

    override fun frame(type: String, bytes: Long) {
        val clean = DevScrub.word(type.ifEmpty { "unknown" })
        synchronized(lock) {
            val s = state
            if (!s.on) return
            s.minute?.let { it.frames[clean] = (it.frames[clean] ?: 0) + 1; it.frameBytes += bytes.coerceAtLeast(0) }
            s.period?.let { it.frames[clean] = (it.frames[clean] ?: 0) + 1; it.frameBytes += bytes.coerceAtLeast(0) }
        }
    }

    override fun socket(event: String) {
        if (event != "connected" && event != "dropped") return
        val now = clock()
        val lines = synchronized(lock) {
            val s = state
            if (!s.on) return
            if (event == "connected") { s.minute?.connects = (s.minute?.connects ?: 0) + 1; s.period?.connects = (s.period?.connects ?: 0) + 1 }
            else { s.minute?.drops = (s.minute?.drops ?: 0) + 1; s.period?.drops = (s.period?.drops ?: 0) + 1 }
            listOf(line(now, "socket", mapOf("event" to JsonPrimitive(event), "page" to JsonPrimitive(s.page))))
        }
        append(lines)
    }

    fun exit(reason: String, at: Instant) {
        val lines = synchronized(lock) {
            val s = state
            if (!s.on) return
            listOf(line(at, "app", mapOf(
                "event" to JsonPrimitive("exit"), "reason" to JsonPrimitive(DevScrub.word(reason)),
                "page" to JsonPrimitive(s.page),
            )))
        }
        append(lines)
    }

    fun error(report: ExtensionErrorReport) {
        val generation = synchronized(lock) { if (!state.on) return; state.generation }
        val write = Runnable {
            runCatching {
                if (isCurrent(generation)) store.appendError(report, clock())
            }
        }
        if (report.kind == "crash") runCatching { writer.submit(write).get() }
        else writer.execute(write)
    }

    fun flush() = append(emptyList(), flush = true)

    /** Waits until every line queued before this call is on disk. Never call from the writer thread. */
    fun settle() {
        flush()
        writer.submit { }.get()
    }

    fun afterWrites(work: () -> Unit) = writer.execute(work)

    override fun close() {
        stop()
        writer.shutdown()
        writer.awaitTermination(5, TimeUnit.SECONDS)
    }

    private fun append(lines: List<String>, flush: Boolean = false) {
        val accepted = synchronized(lock) {
            val s = state
            if (!s.on) return
            s.buffer += lines
            if (!flush && s.buffer.size < BUFFER_LIMIT) return
            val out = s.buffer.toList()
            s.buffer.clear()
            if (out.isEmpty()) null else out to s.generation
        } ?: return
        val (batch, generation) = accepted
        val now = clock()
        writer.execute {
            runCatching {
                if (isCurrent(generation)) store.appendEvents(batch, now)
            }
        }
    }

    private fun isCurrent(generation: Long): Boolean = synchronized(lock) {
        state.on && state.generation == generation
    }

    private fun settlePage(s: State, now: Instant) {
        val seconds = Duration.between(s.pageSince, now).seconds.coerceAtLeast(0)
        if (seconds > 0 && s.pageSince != Instant.EPOCH) {
            s.minute?.pages?.merge(s.page, seconds, Long::plus)
            s.period?.pages?.merge(s.page, seconds, Long::plus)
        }
        s.pageSince = now
    }

    private fun count(tally: Tally?, route: Route, code: String, bytesIn: Long, bytesOut: Long, millis: Long) {
        if (tally == null) return
        val stat = tally.routes.getOrPut(route, ::RouteStat)
        stat.count++
        stat.bytesIn += bytesIn.coerceAtLeast(0)
        stat.bytesOut += bytesOut.coerceAtLeast(0)
        if (stat.millis.size < 2000) stat.millis += millis.coerceAtLeast(0)
        stat.status[code] = (stat.status[code] ?: 0) + 1
    }

    private fun close(tally: Tally, now: Instant, cpu: Long, device: DevDeviceState, s: State): String {
        val used = (cpu - tally.cpu).coerceAtLeast(0)
        val http = tally.routes.entries.sortedWith(compareBy({ it.key.pattern }, { it.key.method })).map { (route, stat) ->
            val sorted = stat.millis.sorted()
            jsonObject(
                "route" to JsonPrimitive("${route.method} ${route.pattern}"),
                "n" to JsonPrimitive(stat.count), "in" to JsonPrimitive(stat.bytesIn), "out" to JsonPrimitive(stat.bytesOut),
                "p50_ms" to JsonPrimitive(if (sorted.isEmpty()) 0 else sorted[sorted.size / 2]),
                "max_ms" to JsonPrimitive(sorted.lastOrNull() ?: 0),
                "status" to jsonObject(*stat.status.map { (key, value) -> key to JsonPrimitive(value) }.toTypedArray()),
            )
        }
        val fields = mutableMapOf(
            "from" to JsonPrimitive(tally.from.toString()), "to" to JsonPrimitive(now.toString()),
            "background" to JsonPrimitive(tally.background), "build" to JsonPrimitive(build), "page" to JsonPrimitive(s.page),
            "pages" to seconds(tally.pages),
            "net" to jsonObject("type" to JsonPrimitive(s.path.type), "expensive" to JsonPrimitive(s.path.expensive), "constrained" to JsonPrimitive(s.path.constrained)),
            "cpu_ms" to JsonPrimitive(used), "requests" to JsonPrimitive(tally.requests), "http" to JsonArray(http),
            "ws" to jsonObject(
                "frames" to jsonObject(*tally.frames.map { (key, value) -> key to JsonPrimitive(value) }.toTypedArray()),
                "bytes" to JsonPrimitive(tally.frameBytes), "connects" to JsonPrimitive(tally.connects), "drops" to JsonPrimitive(tally.drops),
            ),
            "thermal" to JsonPrimitive(device.thermal), "low_power" to JsonPrimitive(device.lowPower),
        )
        device.battery?.let { fields["battery"] = JsonPrimitive(it) }
        return line(now, "minute", fields)
    }

    private fun foreground(tally: Tally, now: Instant, cpu: Long, device: DevDeviceState): String {
        val used = (cpu - tally.cpu).coerceAtLeast(0)
        val fields = mutableMapOf(
            "from" to JsonPrimitive(tally.from.toString()), "to" to JsonPrimitive(now.toString()),
            "seconds" to JsonPrimitive(Duration.between(tally.from, now).seconds),
            "build" to JsonPrimitive(build), "cpu_s" to JsonPrimitive(used / 1000.0),
            "requests" to JsonPrimitive(tally.requests), "bytes_in" to JsonPrimitive(tally.bytesIn), "bytes_out" to JsonPrimitive(tally.bytesOut),
            "ws_frames" to JsonPrimitive(tally.frameCount), "ws_connects" to JsonPrimitive(tally.connects), "ws_drops" to JsonPrimitive(tally.drops),
            "pages" to seconds(tally.pages),
            "thermal" to jsonObject("start" to JsonPrimitive(tally.device.thermal), "end" to JsonPrimitive(device.thermal)),
        )
        if (tally.device.battery != null || device.battery != null) {
            fields["battery"] = jsonObject(
                "start" to (tally.device.battery?.let(::JsonPrimitive) ?: JsonNull),
                "end" to (device.battery?.let(::JsonPrimitive) ?: JsonNull),
            )
        }
        return line(now, "foreground", fields)
    }

    private fun seconds(pages: Map<String, Long>): JsonObject = jsonObject(
        *pages.mapNotNull { (page, seconds) -> if (seconds > 0) page to JsonPrimitive(seconds) else null }.toTypedArray(),
    )

    private fun line(at: Instant, kind: String, fields: Map<String, JsonElement>): String {
        val all = fields + mapOf("t" to JsonPrimitive(at.toString()), "kind" to JsonPrimitive(kind))
        return JsonCodec.encodeToString(JsonObject.serializer(), JsonObject(all.toSortedMap()))
    }

    private fun jsonObject(vararg fields: Pair<String, JsonElement>): JsonObject = JsonObject(fields.toMap().toSortedMap())

    private fun jsonValue(value: Any?): JsonElement = when (value) {
        null -> JsonNull
        is JsonElement -> value
        is String -> JsonPrimitive(value)
        is Boolean -> JsonPrimitive(value)
        is Double -> JsonPrimitive(if (value.isFinite()) value else 0.0)
        is Float -> JsonPrimitive(if (value.isFinite()) value else 0.0)
        is Number -> JsonPrimitive(value)
        is Map<*, *> -> JsonObject(value.entries.mapNotNull { (key, item) ->
            (key as? String)?.let { it to jsonValue(item) }
        }.toMap().toSortedMap())
        is Iterable<*> -> JsonArray(value.map(::jsonValue))
        else -> JsonPrimitive(DevScrub.word(value.toString()))
    }

    private companion object { const val BUFFER_LIMIT = 200 }
}
