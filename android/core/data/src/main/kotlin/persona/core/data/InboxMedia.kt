package persona.core.data

import android.content.Context
import android.net.Uri
import android.os.Handler
import android.os.Looper
import android.os.ParcelFileDescriptor
import androidx.core.content.FileProvider
import persona.core.contracts.api.InboxAssistantTransport
import persona.core.contracts.api.MailTransport
import persona.core.inbox.LarkContentSource
import persona.core.inbox.LarkPart
import persona.core.inbox.MailAttachmentTransport
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.io.FileNotFoundException

class InboxFileProvider : FileProvider() {
    override fun openFile(uri: Uri, mode: String): ParcelFileDescriptor {
        if (mode != "r") throw FileNotFoundException("read-only cache")
        val context = requireNotNull(context)
        val segments = uri.pathSegments
        val directory = when (segments.firstOrNull()) {
            "inbox_files" -> File(context.cacheDir, "inbox-downloads")
            "scoped_inbox_files" -> File(context.cacheDir, "hub-data")
            else -> throw FileNotFoundException("unknown media directory")
        }.canonicalFile
        val file = File(directory, segments.drop(1).joinToString("/")).canonicalFile
        if (!file.toPath().startsWith(directory.toPath()) || file == directory) throw FileNotFoundException("invalid media path")
        return synchronized(CacheFiles.lock) {
            val lease = CacheFiles.retain(file)
            try {
                file.setLastModified(System.currentTimeMillis())
                ParcelFileDescriptor.open(file, ParcelFileDescriptor.MODE_READ_ONLY, Handler(Looper.getMainLooper())) {
                    lease.close()
                }
            } catch (failure: Exception) { lease.close(); throw failure }
        }
    }
}

/** Used by inbox decorators that must share the pairing's file-erasure lock. */
suspend fun <T> ScopedHubData.withInboxFiles(write: () -> T): T = writeFiles(write)

class InboxReadingStorage(private val data: ScopedHubData) : persona.core.inbox.InboxStateStorage {
    private val file = android.util.AtomicFile(File(data.noBackupDirectory, "lark-reading/positions.json"))
    override suspend fun read(): String? = withContext(Dispatchers.IO) { data.writeFiles {
        if (file.baseFile.exists()) file.readFully().decodeToString() else null
    } }
    override suspend fun write(value: String) = withContext(Dispatchers.IO) { data.writeFiles {
        check(file.baseFile.parentFile!!.isDirectory || file.baseFile.parentFile!!.mkdirs())
        val stream = file.startWrite()
        try { stream.write(value.toByteArray()); file.finishWrite(stream) }
        catch (error: Exception) { file.failWrite(stream); throw error }
    } }
}

class InboxMedia(
    private val context: Context,
    private val data: ScopedHubData? = null,
    private val budget: Long = data?.cacheBudgets?.inboxMedia ?: CacheBudgets().inboxMedia,
    /** The content URI a saved download is opened by (InboxFileProvider). */
    private val uri: (File) -> String = { FileProvider.getUriForFile(context, "${context.packageName}.inbox.files", it).toString() },
) {
    fun mailTransport(transport: MailTransport): MailAttachmentTransport {
        val data = requireNotNull(data) { "Mail attachments require a pairing data store" }
        require(transport.scope == data.scope.path) { "Media belongs to another pairing" }
        return InboxMailAttachments(transport, BudgetFileCache(data.inboxMediaDirectory, budget)) { write ->
            data.writeFiles(write)
        }
    }

    /** [file] fetched as the manager its chat was read as ([LarkPart.File.source]), each manager's under its own name. */
    suspend fun download(transport: InboxAssistantTransport, file: LarkPart.File): String = withContext(Dispatchers.IO) {
        require(data == null || transport.scope == data.scope.path) { "Media belongs to another pairing" }
        val source = LarkContentSource(transport, file.source?.manager)
        val safeName = file.name.substringAfterLast('/').substringAfterLast('\\')
            .replace(Regex("[^\\p{L}\\p{N}._-]"), "_").take(100).ifBlank { "file" }
        // The default's files keep the names they were saved under.
        val key = if (source.scope == transport.scope) file.ref else "${source.scope}:${file.ref}"
        val name = "${cacheDigest(key)}-$safeName"
        val directory = data?.inboxMediaDirectory ?: File(context.cacheDir, "inbox-downloads/${transport.scope}")
        val cache = BudgetFileCache(directory, budget)
        suspend fun <T> access(block: () -> T): T = if (data == null) block() else data.writeFiles(block)
        access { cache.read(name) }?.let { return@withContext uri(it) }
        val bytes = source.media(file.ref)
        access { uri(cache.put(name, bytes)) }
    }
}
