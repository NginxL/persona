package persona.core.hub.errors

import java.io.File
import java.time.Instant
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionErrorReport

/**
 * Reports kept on disk until a hub with `error_reports` takes them (docs/protocol/protocol.md "Error reports": at most
 * [MaxReports], at most [MaxAgeMillis] old). One file a report, `<time millis>-<id>.json`, so the oldest sort first and
 * a report's age is known without parsing it.
 */
class ErrorReportQueue(
    private val directory: File,
    private val clock: () -> Long = System::currentTimeMillis,
    private val localCopy: (ExtensionErrorReport) -> Unit = {},
) {
    /** A queued report and its file, for [delete] after the hub took it. */
    data class Entry(val file: File, val report: ExtensionErrorReport, val json: String)

    /**
     * Writes [report] synchronously (the crash handler calls this right before the process dies). Never throws;
     * false when it could not be written.
     */
    fun write(report: ExtensionErrorReport): Boolean {
        val written = try {
            directory.mkdirs()
            val millis = runCatching { Instant.parse(report.time).toEpochMilli() }.getOrDefault(clock())
            val name = "$millis-${report.id.filter { it.isLetterOrDigit() || it == '-' }.take(64)}"
            val temp = File(directory, "$name.tmp")
            temp.writeText(JsonCodec.encodeToString(ExtensionErrorReport.serializer(), report))
            temp.renameTo(File(directory, "$name.json"))
        } catch (_: Throwable) {
            false
        }
        // The optional local recorder never changes the established hub queue's outcome.
        if (written) runCatching { localCopy(report) }
        return written
    }

    /**
     * The kept reports, oldest first, after dropping the expired ones, the unreadable ones and all but the newest
     * [MaxReports].
     */
    fun pending(): List<Entry> {
        prune()
        return files().mapNotNull { file ->
            val json = runCatching { file.readText() }.getOrNull()
            val report = json?.let { runCatching { JsonCodec.decodeFromString(ExtensionErrorReport.serializer(), it) }.getOrNull() }
            if (report == null || report.id.isBlank()) { file.delete(); null } else Entry(file, report, json)
        }
    }

    fun delete(entries: Collection<Entry>) { entries.forEach { it.file.delete() } }

    /** The hub has no `error_reports`: nothing kept is ever sent (docs/protocol/protocol.md). */
    fun clear() { directory.listFiles()?.forEach { it.delete() } }

    fun prune() {
        val now = clock()
        directory.listFiles { file -> file.name.endsWith(".tmp") && now - file.lastModified() > StaleTempMillis }
            ?.forEach { it.delete() }
        val kept = files().filter { file ->
            val millis = millisOf(file)
            val fresh = millis != null && now - millis <= MaxAgeMillis
            if (!fresh) file.delete()
            fresh
        }
        kept.dropLast(MaxReports).forEach { it.delete() }
    }

    private fun files(): List<File> = directory.listFiles { file -> file.name.endsWith(".json") }
        ?.sortedWith(compareBy<File>({ millisOf(it) ?: Long.MIN_VALUE }, { it.name })).orEmpty()

    private fun millisOf(file: File) = file.name.substringBefore('-').toLongOrNull()

    companion object {
        const val MaxReports = 50
        const val MaxAgeMillis = 7L * 24 * 60 * 60 * 1000
        private const val StaleTempMillis = 60L * 60 * 1000
    }
}
