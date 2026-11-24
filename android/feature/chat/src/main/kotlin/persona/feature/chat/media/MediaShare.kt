package persona.feature.chat.media

import android.app.Activity
import persona.feature.chat.R
import android.content.ClipData
import android.content.ClipboardManager
import android.content.ContentValues
import android.content.Context
import android.content.ContextWrapper
import android.content.Intent
import android.graphics.Bitmap
import android.net.Uri
import android.os.Environment
import android.provider.MediaStore
import androidx.core.content.FileProvider
import persona.core.designsystem.ImageMath
import persona.core.designsystem.jpegBytes
import persona.feature.chat.ComposerAttachment
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.util.UUID

/**
 * Saving, sharing and copying a chat picture (the image viewer's "Save" / "Share" / "Copy"), and writing a marked-up one
 * for the composer. Shared and copied pictures are files in `cache/shared/`, handed out through the chat's
 * FileProvider (`<package>.chat.files`, res/xml/chat_file_paths.xml); edited ones go to `cache/chat-captures/` like
 * camera photos. Each call reports whether it worked; the caller words the notice.
 */
object MediaShare {
    /** Long side and JPEG quality of a marked-up picture, as a photo picked for a message (iOS sends the same). */
    const val EditedMaxSide = 1600
    const val EditedQuality = 75
    private const val SharedMaxAgeMs = 24 * 3600 * 1000L

    private fun authority(context: Context) = "${context.packageName}.chat.files"

    /** Into the gallery's `Pictures/Persona` through MediaStore: no permission needed from Android 10 on. */
    suspend fun saveImage(context: Context, bytes: ByteArray): Boolean = withContext(Dispatchers.IO) {
        val type = ImageMath.sniffMime(bytes)
        val resolver = context.contentResolver
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, "persona-${System.currentTimeMillis()}.${ImageMath.extension(type)}")
            put(MediaStore.MediaColumns.MIME_TYPE, type)
            put(MediaStore.MediaColumns.RELATIVE_PATH, Environment.DIRECTORY_PICTURES + "/Persona")
            put(MediaStore.MediaColumns.IS_PENDING, 1)
        }
        val uri = runCatching { resolver.insert(MediaStore.Images.Media.EXTERNAL_CONTENT_URI, values) }.getOrNull()
            ?: return@withContext false
        val written = runCatching {
            checkNotNull(resolver.openOutputStream(uri)) { "No output stream" }.use { it.write(bytes) }
            resolver.update(uri, ContentValues().apply { put(MediaStore.MediaColumns.IS_PENDING, 0) }, null, null)
        }.isSuccess
        if (!written) { val _ = runCatching { resolver.delete(uri, null, null) } }
        written
    }

    /** The system share sheet with the picture; false when it could not be written or no sheet opened. */
    suspend fun shareImage(context: Context, bytes: ByteArray): Boolean {
        val type = ImageMath.sniffMime(bytes)
        val uri = sharedUri(context, bytes, ImageMath.extension(type)) ?: return false
        val send = Intent(Intent.ACTION_SEND).apply {
            this.type = type
            putExtra(Intent.EXTRA_STREAM, uri)
            clipData = ClipData.newRawUri("", uri)
            addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
        }
        val chooser = Intent.createChooser(send, context.getString(R.string.chat_viewer_share_title))
        if (context.activity() == null) chooser.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        return try {
            context.startActivity(chooser)
            true
        } catch (_: Exception) {
            false
        }
    }

    /** The picture on the clipboard as a content URI (apps that paste pictures read it from there). */
    suspend fun copyImage(context: Context, bytes: ByteArray): Boolean {
        val uri = sharedUri(context, bytes, ImageMath.extension(ImageMath.sniffMime(bytes))) ?: return false
        return try {
            val clipboard = context.getSystemService(ClipboardManager::class.java) ?: return false
            clipboard.setPrimaryClip(ClipData.newUri(context.contentResolver, context.getString(R.string.chat_viewer_clip_label), uri))
            true
        } catch (_: Exception) {
            false
        }
    }

    /**
     * A marked-up picture as a photo for the composer: long side [EditedMaxSide], JPEG [EditedQuality], written to
     * `cache/chat-captures/` and handed out through the chat's FileProvider, which the composer reads without a grant.
     */
    suspend fun writeEditedPhoto(context: Context, picture: Bitmap): ComposerAttachment = withContext(Dispatchers.IO) {
        val bytes = picture.jpegBytes(EditedMaxSide, EditedQuality)
        val folder = File(context.cacheDir, "chat-captures").apply { mkdirs() }
        val file = File.createTempFile("edited-", ".jpg", folder)
        try {
            file.writeBytes(bytes)
        } catch (e: Throwable) {
            file.delete()
            throw e
        }
        val uri = FileProvider.getUriForFile(context, authority(context), file)
        ComposerAttachment(id = UUID.randomUUID().toString(), name = file.name, uri = uri.toString(), image = true, size = file.length())
    }

    /** [bytes] as a file in `cache/shared/` and its content URI; shared files older than a day are dropped on the way. */
    private suspend fun sharedUri(context: Context, bytes: ByteArray, extension: String): Uri? = withContext(Dispatchers.IO) {
        try {
            val folder = File(context.cacheDir, "shared").apply { mkdirs() }
            val now = System.currentTimeMillis()
            folder.listFiles()?.forEach { if (now - it.lastModified() > SharedMaxAgeMs) it.delete() }
            val file = File(folder, "image-$now.$extension")
            file.writeBytes(bytes)
            FileProvider.getUriForFile(context, authority(context), file)
        } catch (e: CancellationException) {
            throw e
        } catch (_: Exception) {
            null
        }
    }

    private tailrec fun Context.activity(): Activity? = when (this) {
        is Activity -> this
        is ContextWrapper -> baseContext.activity()
        else -> null
    }
}
