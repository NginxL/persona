package persona.feature.chat

import android.content.Context
import android.net.Uri
import android.provider.DocumentsContract
import android.provider.OpenableColumns
import androidx.core.content.FileProvider
import persona.core.contracts.api.DraftUploadState
import java.io.File
import java.io.IOException
import java.util.Locale
import java.util.UUID

internal data class ComposerDropImportResult(
    val attachment: ComposerAttachment? = null,
    val notice: String? = null,
)

/**
 * Copy one dropped URI to an app-owned file while its transient grant is held. The returned attachment then follows the
 * exact picker path: [selectAttachment], the existing JPEG / video handling and ChatComposer's upload LaunchedEffect.
 * Call on an IO dispatcher.
 */
internal fun importDroppedAttachment(context: Context, source: Uri, mediaCount: Int): ComposerDropImportResult {
    if (source.scheme != "content" && source.scheme != "file") return rejected(context)
    val mime = runCatching { context.contentResolver.getType(source) }.getOrNull()?.lowercase(Locale.ROOT)
    val directory = mime == DocumentsContract.Document.MIME_TYPE_DIR ||
        (source.scheme == "file" && source.path?.let(::File)?.isDirectory == true)
    if (directory) return rejected(context)
    val image = mime?.startsWith("image/") == true
    val video = mime?.startsWith("video/") == true
    if ((image || video) && mediaCount >= MAX_COMPOSER_MEDIA) {
        return ComposerDropImportResult(notice = context.getString(R.string.chat_composer_photo_limit))
    }
    val name = runCatching { attachmentName(context, source) }
        .getOrDefault(context.getString(R.string.chat_attach_default_name))
    val declaredSize = droppedSize(context, source)
    oversizeReason(name, declaredSize, context.resources)?.let { reason ->
        return invalidDrop(name, declaredSize, reason, image, video)
    }

    var owned: File? = null
    try {
        val folder = File(context.cacheDir, "chat-drops").apply { mkdirs() }
        val file = File.createTempFile("drop-", ".bin", folder).also { owned = it }
        val size = copyDroppedFile(context, source, file)
        val uri = FileProvider.getUriForFile(context, "${context.packageName}.chat.files", file)
        val selected = selectAttachment(context, uri, image = image, video = video)
        val attachment = selected.copy(name = name, size = size)
        owned = null // the draft owns it now; leftDrops/deleteDrop clean it after removal or send
        return ComposerDropImportResult(attachment, attachment.error)
    } catch (error: DropTooLarge) {
        val reason = oversizeReason(name, error.bytes, context.resources)!!
        return invalidDrop(name, error.bytes, reason, image, video)
    } catch (_: Exception) {
        return rejected(context)
    } finally {
        owned?.delete()
    }
}

private fun rejected(context: Context) = ComposerDropImportResult(
    notice = context.getString(R.string.chat_composer_drop_failed),
)

private fun invalidDrop(name: String, size: Long?, reason: String, image: Boolean, video: Boolean) = ComposerDropImportResult(
    ComposerAttachment(UUID.randomUUID().toString(), name, "", image, size = size,
        uploadState = DraftUploadState.Invalid, error = reason, video = video),
    reason,
)

private fun droppedSize(context: Context, uri: Uri): Long? = runCatching {
    context.contentResolver.query(uri, arrayOf(OpenableColumns.SIZE), null, null, null)?.use {
        if (it.moveToFirst() && !it.isNull(0)) it.getLong(0).takeIf { bytes -> bytes >= 0 } else null
    }
}.getOrNull()

/** Stream a provider into [destination], never into memory. [limit] is injectable for small boundary tests. */
internal fun copyDroppedFile(context: Context, source: Uri, destination: File, limit: Long = MAX_COMPOSER_FILE_BYTES): Long {
    var total = 0L
    try {
        context.contentResolver.openInputStream(source)?.use { input ->
            destination.outputStream().use { output ->
                val buffer = ByteArray(64 * 1024)
                while (true) {
                    val read = input.read(buffer)
                    if (read < 0) break
                    total += read
                    if (total > limit) throw DropTooLarge(total)
                    output.write(buffer, 0, read)
                }
            }
        } ?: throw IOException("No stream for dropped URI")
        return total
    } catch (error: Throwable) {
        destination.delete()
        throw error
    }
}

private class DropTooLarge(val bytes: Long) : IOException()

/** App-owned drops use their own FileProvider path so cleanup cannot touch picker, camera or other-session files. */
internal fun isDropUri(uri: String, packageName: String): Boolean =
    uri.startsWith("content://$packageName.chat.files/chat-drops/")

/** Drop originals that left a draft; edited-photo sources stay while their edit still refers to them. */
internal fun leftDrops(
    before: List<ComposerAttachment>,
    now: List<ComposerAttachment>,
    packageName: String,
    keptBefore: Collection<String> = emptyList(),
    keptNow: Collection<String> = emptyList(),
): List<String> = (before.filter { old -> now.none { it.id == old.id } }.map { it.uri } + keptBefore)
    .filter { uri -> isDropUri(uri, packageName) && uri !in keptNow && now.none { it.uri == uri } }.distinct()

/** Delete only one of this app's own drop copies, off the main thread; never sweep cache/chat-drops. */
internal fun deleteDrop(context: Context, uri: String) {
    if (!isDropUri(uri, context.packageName)) return
    val _ = runCatching { context.contentResolver.delete(Uri.parse(uri), null, null) }
}
