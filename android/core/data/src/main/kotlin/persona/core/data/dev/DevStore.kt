package persona.core.data.dev

import java.io.File
import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import persona.core.contracts.DevScrub
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionErrorReport

/**
 * Bounded local records for Developing Persona. Events use UTC hourly files so the oldest hours go first; errors are
 * scrubbed before they touch disk. The app puts [directory] under `noBackupFilesDir`, never in a shared or backed-up location.
 */
class DevStore(val directory: File, private val maxBytes: Long = defaultMaxBytes) {
    private val lock = Any()
    private val events = File(directory, "events")
    private val errors = File(directory, "errors.jsonl")

    fun appendEvents(lines: List<String>, at: Instant) {
        val cutoff = at.minusSeconds(eventAgeSeconds)
        val kept = lines.mapNotNull { line ->
            if (line.isBlank()) return@mapNotNull null
            val timestamp = eventTime(line) ?: return@mapNotNull null
            if (timestamp < cutoff) null else line to timestamp
        }
        if (kept.isEmpty()) return
        synchronized(lock) {
            // Historical exits belong to their own hour; a current write time must not make an old event look fresh.
            kept.groupBy({ hourName(it.second) }, { it.first }).forEach { (hour, records) ->
                append(records.joinToString("\n", postfix = "\n"), File(events, hour), sync = false)
            }
            trimRecordsLocked(at, trimErrors = true)
        }
    }

    fun appendError(report: ExtensionErrorReport, at: Instant) {
        val clean = scrubbed(report)
        val line = JsonCodec.encodeToString(clean)
        synchronized(lock) {
            append(line + "\n", errors, sync = true)
            trimRecordsLocked(at, trimErrors = false)
        }
    }

    fun clear() = synchronized(lock) { directory.deleteRecursively() }

    fun recordedBytes(): Long = synchronized(lock) { recordFiles(directory).sumOf(File::length) }

    fun eventFiles(): List<File> = synchronized(lock) {
        events.listFiles().orEmpty().filter { it.isFile && it.name.endsWith(".jsonl") }.sortedBy(File::getName)
    }

    /** Kept error lines younger than [errorAgeSeconds], oldest first. Invalid or undated lines never leave the device. */
    fun errorLines(now: Instant): List<String> = synchronized(lock) { validErrors(now).map(ErrorLine::line) }

    fun trimRecords(now: Instant) = synchronized(lock) { trimRecordsLocked(now, trimErrors = true) }

    private data class ErrorLine(val line: String, val at: Instant)

    private fun validErrors(now: Instant): List<ErrorLine> {
        if (!errors.isFile) return emptyList()
        val cutoff = now.minusSeconds(errorAgeSeconds)
        return errors.readLines().mapNotNull { line ->
            val at = runCatching {
                Instant.parse(JsonCodec.decodeFromString(ExtensionErrorReport.serializer(), line).time)
            }.getOrNull() ?: return@mapNotNull null
            if (at >= cutoff) ErrorLine(line, at) else null
        }
    }

    private fun trimRecordsLocked(now: Instant, trimErrors: Boolean) {
        val cutoff = now.minusSeconds(eventAgeSeconds + 3600) // an hourly file covers up to an hour after its name
        eventFiles().filter { hourDate(it.name)?.isBefore(cutoff) != false }.forEach(File::delete)
        if (trimErrors) {
            val kept = validErrors(now)
            if (kept.isEmpty()) errors.delete()
            else writeAtomically(errors, kept.joinToString("\n", postfix = "\n") { it.line })
        }

        var kept = eventFiles().toMutableList()
        var total = kept.sumOf(File::length) + errors.length()
        while (total > maxBytes && kept.size > 1) {
            val oldest = kept.removeAt(0)
            val length = oldest.length()
            oldest.delete()
            total -= length
        }
        if (total > maxBytes && kept.size == 1) {
            total -= kept.single().length()
            trimCurrentEventFile(kept.single(), (maxBytes - errors.length()).coerceAtLeast(0))
            total += kept.single().length()
        }
        if (total > maxBytes) {
            val eventBytes = kept.sumOf(File::length)
            trimErrorsToBudget((maxBytes - eventBytes).coerceAtLeast(0), now)
        }
    }

    /** Keep the newest complete error records that fit; never delete a newer report merely because the file is large. */
    private fun trimErrorsToBudget(budget: Long, now: Instant) {
        if (budget <= 0) { errors.delete(); return }
        val kept = ArrayDeque<ErrorLine>()
        var bytes = 0L
        for (record in validErrors(now).asReversed()) {
            val size = record.line.toByteArray(Charsets.UTF_8).size + 1L
            if (size > budget) continue
            if (bytes + size > budget) break
            kept.addFirst(record)
            bytes += size
        }
        if (kept.isEmpty()) errors.delete()
        else writeAtomically(errors, kept.joinToString("\n", postfix = "\n") { it.line })
    }

    private fun eventTime(line: String): Instant? = runCatching {
        Instant.parse(checkNotNull(JsonCodec.parseToJsonElement(line).jsonObject.getValue("t").jsonPrimitive.contentOrNull))
    }.getOrNull()

    /** Even one oversized current hour cannot bypass the remaining budget; keep its newest complete JSON lines. */
    private fun trimCurrentEventFile(file: File, budget: Long) {
        if (budget <= 0) { file.delete(); return }
        val lines = file.readLines()
        val kept = ArrayDeque<String>()
        var bytes = 0L
        for (line in lines.asReversed()) {
            val size = line.toByteArray(Charsets.UTF_8).size + 1L
            if (size > budget) continue
            if (bytes + size > budget) break
            kept.addFirst(line)
            bytes += size
        }
        if (kept.isEmpty()) file.delete() else writeAtomically(file, kept.joinToString("\n", postfix = "\n"))
    }

    private fun append(text: String, file: File, sync: Boolean) {
        file.parentFile?.mkdirs()
        java.io.FileOutputStream(file, true).channel.use { channel ->
            channel.position(channel.size())
            val bytes = text.toByteArray(Charsets.UTF_8)
            val buffer = java.nio.ByteBuffer.wrap(bytes)
            while (buffer.hasRemaining()) channel.write(buffer)
            if (sync) channel.force(true)
        }
    }

    private fun writeAtomically(file: File, text: String) {
        file.parentFile?.mkdirs()
        val temporary = File(file.path + ".tmp")
        temporary.writeText(text)
        if (!temporary.renameTo(file)) {
            temporary.delete()
            error("Cannot replace ${file.name}")
        }
    }

    private fun recordFiles(root: File): List<File> =
        if (!root.exists()) emptyList() else root.walkBottomUp().filter(File::isFile).toList()

    companion object {
        const val defaultMaxBytes: Long = 50L shl 20
        const val eventAgeSeconds: Long = 24 * 3600
        const val errorAgeSeconds: Long = 7 * 24 * 3600
        private val hourFormat: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH").withZone(ZoneOffset.UTC)

        fun hourName(at: Instant): String = hourFormat.format(at) + ".jsonl"
        fun hourDate(name: String): Instant? = runCatching {
            Instant.parse(name.removeSuffix(".jsonl") + ":00:00Z")
        }.getOrNull()

        /** Only free-text fields change; code identifiers and stack frames stay useful for symbolication. */
        fun scrubbed(report: ExtensionErrorReport): ExtensionErrorReport = report.copy(
            message = report.message?.let(DevScrub::text),
            exceptions = report.exceptions?.map { it.copy(value = it.value?.let(DevScrub::text)) },
            breadcrumbs = report.breadcrumbs?.map { it.copy(message = it.message?.let(DevScrub::text)) },
        )
    }
}
