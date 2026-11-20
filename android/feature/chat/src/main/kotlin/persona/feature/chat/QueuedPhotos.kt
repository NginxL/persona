package persona.feature.chat

import android.content.Context
import android.content.res.Resources
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.core.content.FileProvider
import persona.core.contracts.ImagePayload
import java.io.File
import java.util.Base64
import java.util.UUID
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.withContext

/** Photos restored as ordinary composer attachments, and how many of them could not be restored. */
data class RestoredPhotos(val attachments: List<ComposerAttachment> = emptyList(), val lost: Int = 0)

/**
 * The photos a queued message carried, which the hub returns beside a Persona conversation's queue as base64 (iOS
 * `restoreQueued`'s `sent`): each is written to `cache/chat-captures/` and handed out through the chat's FileProvider like a
 * camera photo ([share]), so it comes back to the composer as the photo it was and goes up to the computer as any photo does.
 * A payload that is not base64 or cannot be written is left out and counted in [RestoredPhotos.lost]. Call off the main thread.
 */
internal fun restoredPhotos(context: Context, payloads: List<ImagePayload>,
    share: (File) -> Uri = { FileProvider.getUriForFile(context, "${context.packageName}.chat.files", it) }): RestoredPhotos {
    val folder = File(context.cacheDir, "chat-captures").apply { mkdirs() }
    val restored = payloads.mapNotNull { payload ->
        runCatching {
            val bytes = Base64.getMimeDecoder().decode(payload.data)
            check(bytes.isNotEmpty()) { "an empty photo" }
            val extension = MimeTypeMap.getSingleton().getExtensionFromMimeType(payload.mime) ?: "jpg"
            val file = File.createTempFile("queued-", ".$extension", folder)
            val uri = try { file.writeBytes(bytes); share(file) } catch (error: Throwable) { file.delete(); throw error }
            ComposerAttachment(UUID.randomUUID().toString(), file.name, uri.toString(), image = true, size = bytes.size.toLong())
        }.getOrNull()
    }
    return RestoredPhotos(restored, payloads.size - restored.size)
}

/**
 * Reads a forwarded message's photos on its source computer and restores each readable one as a local composer photo.
 * A failed read or write leaves only that photo out. The original filename stays visible in the target composer.
 */
internal suspend fun forwardedPhotos(context: Context, paths: List<String>, read: suspend (String) -> ImagePayload,
    share: (File) -> Uri = { FileProvider.getUriForFile(context, "${context.packageName}.chat.files", it) }): RestoredPhotos {
    val attachments = mutableListOf<ComposerAttachment>()
    try {
        return withContext(Dispatchers.IO) {
            var lost = 0
            for (path in paths) {
                val payload = try { read(path) } catch (cancelled: CancellationException) { throw cancelled }
                    catch (_: Exception) { null }
                val restored = payload?.let { restoredPhotos(context, listOf(it), share) }
                val attachment = restored?.attachments?.singleOrNull()
                if (attachment == null) lost++
                else attachments += attachment.copy(name = path.trimEnd('/').substringAfterLast('/').ifEmpty { attachment.name })
            }
            RestoredPhotos(attachments, lost)
        }
    } catch (cancelled: CancellationException) {
        // Returning to the caller can be cancelled after one or more files have already been staged.
        withContext(NonCancellable) { discardForwardedPhotos(context, RestoredPhotos(attachments)) }
        throw cancelled
    }
}

/**
 * Adds forwarded photos to the target's existing draft, up to its free media places. Returns the attachments rejected by
 * the target cap; rejected captures are deleted rather than left in the cache.
 */
suspend fun attachForwardedPhotos(context: Context, composer: ChatComposerState, handoffs: List<RestoredPhotos>): List<ComposerAttachment> {
    val photos = handoffs.flatMap(RestoredPhotos::attachments)
    val room = (MAX_COMPOSER_MEDIA - composer.attachments.count { it.media }).coerceAtLeast(0)
    val accepted = photos.take(room)
    val rejected = photos.drop(room)
    if (accepted.isNotEmpty()) {
        composer.beginEdit()
        composer.attachments = composer.attachments + accepted
    }
    withContext(Dispatchers.IO) { rejected.forEach { deleteCapture(context, it.uri) } }
    return rejected
}

/** Drops a duplicate handoff without adding its photos twice. */
internal suspend fun discardForwardedPhotos(context: Context, handoff: RestoredPhotos) {
    withContext(Dispatchers.IO) { handoff.attachments.forEach { deleteCapture(context, it.uri) } }
}

/** The one short notice for source photos that could not reach the target composer. */
fun forwardedPhotoNotice(resources: Resources, lost: Int): String = resources.getString(
    if (lost == 1) R.string.chat_forward_photos_lost_one else R.string.chat_forward_photos_lost_other, lost)
