package persona.core.data.dev

import java.io.File
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.UUID
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import kotlinx.serialization.json.putJsonObject
import persona.core.contracts.DevScrub
import persona.core.contracts.JsonCodec

enum class DevCategory(val wire: String) { Logs("logs"), Errors("errors"), Energy("energy") }

data class DevDevice(
    val platform: String = "android",
    val os: String = "android",
    val osVersion: String = "",
    val model: String = "",
    val appVersion: String = "",
    val build: String = "",
    val commit: String = "",
    val utcOffsetMinutes: Int = 0,
)

data class DevExportSummary(val events: Int = 0, val errors: Int = 0, val logLines: Int = 0)

/** Builds the shared, scrubbed Developing Persona ZIP entirely from local records. */
object DevPackage {
    const val format: String = "persona-diagnostics"
    const val version: Int = 1
    const val maxLogLines: Int = 5000
    val energyKinds: Set<String> = setOf("minute", "foreground", "thermal", "power", "battery", "path")
    private val stampFormat: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyyMMdd-HHmm")

    fun fileName(device: DevDevice, at: Instant, zone: ZoneId = ZoneId.systemDefault()): String =
        "persona-diagnostics-${device.platform}-${stampFormat.format(at.atZone(zone))}.zip"

    fun exportZip(
        workRoot: File,
        store: DevStore,
        categories: Set<DevCategory>,
        since: Instant?,
        device: DevDevice,
        log: List<String> = emptyList(),
        now: Instant,
        zone: ZoneId = ZoneId.systemDefault(),
    ): File {
        val work = File(workRoot, "dev-export-${UUID.randomUUID()}")
        check(work.mkdirs() || work.isDirectory)
        val name = fileName(device, now, zone)
        val folder = File(work, name.removeSuffix(".zip"))
        try {
            writePackage(folder, store, categories, since, device, log, now)
            val zip = File(work, name)
            ZipOutputStream(zip.outputStream().buffered()).use { output ->
                folder.listFiles().orEmpty().filter(File::isFile).sortedWith(
                    compareBy<File> { it.name != "README.md" }.thenBy(File::getName),
                ).forEach { file ->
                    output.putNextEntry(ZipEntry("${folder.name}/${file.name}").apply { time = file.lastModified() })
                    file.inputStream().buffered().use { it.copyTo(output) }
                    output.closeEntry()
                }
            }
            return zip
        } finally {
            folder.deleteRecursively()
        }
    }

    private fun writePackage(
        folder: File,
        store: DevStore,
        categories: Set<DevCategory>,
        since: Instant?,
        device: DevDevice,
        log: List<String>,
        now: Instant,
    ): DevExportSummary {
        folder.deleteRecursively()
        check(folder.mkdirs() || folder.isDirectory)
        store.trimRecords(now)
        val files = store.eventFiles()
        val cutoff = now.minusSeconds(DevStore.eventAgeSeconds)
        val eventLines = files.flatMap { file ->
            file.readLines().mapNotNull { line ->
                val event = event(line) ?: return@mapNotNull null
                if (event.at < cutoff) return@mapNotNull null
                val category = if (event.kind in energyKinds) DevCategory.Energy else DevCategory.Logs
                if (category in categories) event.at to line else null
            }
        }.sortedBy { it.first }.map { it.second }
        File(folder, "events.jsonl").writeText(jsonLines(eventLines))

        val errorLines = if (DevCategory.Errors in categories) store.errorLines(now) else emptyList()
        if (DevCategory.Errors in categories) File(folder, "errors.jsonl").writeText(jsonLines(errorLines))

        val logLines = if (DevCategory.Logs in categories) log.takeLast(maxLogLines).map(DevScrub::text) else emptyList()
        if (DevCategory.Logs in categories) File(folder, "log.txt").writeText(
            if (logLines.isEmpty()) "" else logLines.joinToString("\n", postfix = "\n"),
        )

        val firstHour = files.firstOrNull()?.let { DevStore.hourDate(it.name) }
        val from = since?.let { maxOf(it, firstHour ?: it) } ?: firstHour
        val summary = DevExportSummary(eventLines.size, errorLines.size, logLines.size)
        val manifest = buildJsonObject {
            put("format", format)
            put("version", version)
            put("platform", device.platform)
            put("os", device.os)
            put("os_version", device.osVersion)
            put("device_model", device.model)
            put("app_version", device.appVersion)
            put("build", device.build)
            put("commit", device.commit)
            put("utc_offset_minutes", device.utcOffsetMinutes)
            put("recording_since", since?.toString())
            put("exported_at", now.toString())
            putJsonObject("window") {
                put("from", from?.toString())
                put("to", now.toString())
            }
            put("categories", JsonArray((listOf("device") + DevCategory.entries.filter { it in categories }.map { it.wire }).map(::JsonPrimitive)))
            putJsonObject("counts") {
                put("events", summary.events)
                put("errors", summary.errors)
                put("metric_payloads", 0)
                put("diagnostic_payloads", 0)
                put("log_lines", summary.logLines)
            }
        }
        File(folder, "manifest.json").writeText(JsonCodec.encodeToString(JsonObject.serializer(), manifest))
        File(folder, "README.md").writeText(readme(categories))
        return summary
    }

    private fun jsonLines(lines: List<String>): String = if (lines.isEmpty()) "" else lines.joinToString("\n", postfix = "\n")

    private data class EventHeader(val kind: String, val at: Instant)

    /** A malformed or undated local line is not exported: it cannot turn arbitrary text into a diagnostics event. */
    private fun event(line: String): EventHeader? = runCatching {
        val objectLine = JsonCodec.parseToJsonElement(line).jsonObject
        val at = Instant.parse(checkNotNull(objectLine["t"]?.jsonPrimitive?.contentOrNull))
        val kind = checkNotNull(objectLine["kind"]?.jsonPrimitive?.contentOrNull)
        require(DevScrub.word(kind) == kind)
        EventHeader(kind, at)
    }.getOrNull()

    private fun readme(categories: Set<DevCategory>): String {
        var text = """
            # Persona diagnostics

            Recorded on one Android device by Persona's "Developing Persona" switch (Settings › About) and exported by its owner. Only numbers and code-chosen words are recorded: no message text, knowledge base content, keys, tokens, passwords, cookies, pairing links, device name, hub address, machine names or accounts. Free text in error messages and log lines was scrubbed: URLs keep only their path, while secrets, e-mail and IP addresses, host names, home folders and long keys are replaced.

            Times are UTC (ISO 8601); `manifest.json` has the device's offset (`utc_offset_minutes`).

            ## Files

            - `manifest.json`: device model, system, app version, build and commit, time window, included categories and counts.
            - `events.jsonl`: one JSON object a line, oldest first. `minute` and `foreground` summarize CPU time, requests by route pattern, WebSocket frames, screens, network, thermal, power and battery. `app`, `page`, `thermal`, `power`, `battery`, `path`, `socket` and `request_error` are individual changes. Route `:id` segments replace identifiers; queries and bodies are never recorded.
            - `errors.jsonl`: the app's existing error reports, including crashes and ApplicationExitInfo ANR/native-crash records, in the hub's error-report format. This local copy does not change whether reports also go to the hub.
            - `log.txt`: up to 5000 lines from this process's own logcat, read at export and scrubbed. No other process's logs are requested.

        """.trimIndent() + "\n"
        val missing = DevCategory.entries.filter { it !in categories }
        if (missing.isNotEmpty()) text += "\nLeft out by the owner: ${missing.joinToString(", ") { it.wire }}.\n"
        return text
    }
}
