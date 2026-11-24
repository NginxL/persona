package persona.feature.files

import android.content.ClipData
import android.content.Context
import android.content.Intent
import androidx.core.content.FileProvider
import persona.core.contracts.api.SessionFileContent
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.util.Base64

/** Shares files of the files pane from this module's cache folder (`files-share/`), read-only. */
class FilesShareProvider : FileProvider()

internal object FileShare {
    /** Text this long goes as a file: a chooser carries its extras through a binder transaction that caps near 1 MB. */
    private const val INLINE_TEXT = 100_000

    /** Whether ⋯ offers "Share": text, bytes that are whole (a cut image or PDF would not open), or a streamed file. */
    fun canShare(file: SessionFileContent) = file.text != null || file.stream || (file.data != null && !file.truncated)

    /**
     * Shares [file] as text, or its bytes as a file through [FilesShareProvider] (a streamed file's from [download]);
     * false when it cannot be shared.
     */
    suspend fun share(context: Context, file: SessionFileContent,
        download: suspend (path: String, into: File) -> Long = { _, _ -> throw UnsupportedOperationException("download") }): Boolean {
        val text = file.text
        val send = when {
            text != null && text.length <= INLINE_TEXT -> Intent(Intent.ACTION_SEND)
                .setType("text/plain").putExtra(Intent.EXTRA_TEXT, text).putExtra(Intent.EXTRA_SUBJECT, file.name)
            else -> {
                val copy = if (text == null && file.stream) {
                    val into = withContext(Dispatchers.IO) { shareFile(context, file.name.ifEmpty { "file" }) }
                    val _ = download(file.path, into)
                    into
                } else {
                    val bytes = withContext(Dispatchers.Default) {
                        text?.toByteArray() ?: runCatching { Base64.getMimeDecoder().decode(file.data.orEmpty()) }.getOrNull()
                    } ?: return false
                    withContext(Dispatchers.IO) { shareFile(context, file.name.ifEmpty { "file" }).apply { writeBytes(bytes) } }
                }
                val uri = FileProvider.getUriForFile(context, "${context.packageName}.files.share", copy)
                Intent(Intent.ACTION_SEND).setType(shareType(file)).putExtra(Intent.EXTRA_STREAM, uri)
                    .putExtra(Intent.EXTRA_SUBJECT, file.name)
                    .also { it.clipData = ClipData.newRawUri(file.name, uri) }
                    .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
        }
        context.startActivity(Intent.createChooser(send, context.getString(R.string.files_share)).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        return true
    }

    internal fun shareType(file: SessionFileContent): String {
        val mime = file.mime?.substringBefore(';')?.trim().orEmpty()
        return when {
            file.text != null -> "text/plain"
            mime.isEmpty() -> "application/octet-stream"
            else -> mime
        }
    }

    /** Where the one file being shared goes (the previous one is removed), under its own name so the receiving app shows it. */
    private fun shareFile(context: Context, name: String): File {
        val folder = File(context.cacheDir, "files-share")
        folder.deleteRecursively()
        folder.mkdirs()
        val safe = name.replace('/', '_').replace('\\', '_').ifEmpty { "file" }
        return File(folder, safe)
    }
}
