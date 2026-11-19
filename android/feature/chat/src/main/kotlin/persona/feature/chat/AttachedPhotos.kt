package persona.feature.chat

import androidx.compose.foundation.horizontalScroll
import androidx.compose.ui.res.stringResource
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.rememberScrollState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.ImageBitmap
import androidx.compose.ui.graphics.asImageBitmap
import persona.core.contracts.ImagePayload
import persona.core.contracts.api.SessionFiles
import persona.core.designsystem.PathChip
import persona.core.designsystem.PhotoRow
import persona.core.designsystem.PhotoThumbnail
import persona.core.designsystem.Tokens
import persona.core.designsystem.ViewerImage
import persona.core.designsystem.decodePicture
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import java.util.Base64
import java.util.Collections
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * Photos sent with messages, read from the machine the session runs on (`GET /api/v1/machines/{id}/file`, iOS
 * `AttachedImages`): the composer uploads them there and writes their paths into `<attached_images>`. Bytes and
 * thumbnails are kept in memory for scrolling back (40 of each, process wide). [cwd] is the session's folder; the
 * paths are absolute, so `~` stands in when the session has none (the hub needs one).
 */
internal class AttachedPhotos(private val files: SessionFiles, private val machineId: String, cwd: String) {
    private val folder = cwd.ifBlank { "~" }

    /** The photo as the file API returns it; throws when the machine cannot give it or it is not a picture. */
    suspend fun payload(path: String): ImagePayload {
        val file = files.file(machineId, folder, path)
        val data = file.data
        check(!file.isDir && !file.truncated && data != null && file.mime.orEmpty().startsWith("image/")) { "not a picture" }
        return ImagePayload(mime = file.mime.orEmpty(), data = data)
    }

    /** The photo's bytes; throws when the machine cannot give them or they are not a picture. */
    suspend fun bytes(path: String): ByteArray {
        val key = "$machineId:$path"
        cachedBytes[key]?.let { return it }
        val payload = payload(path)
        val bytes = withContext(Dispatchers.Default) { Base64.getMimeDecoder().decode(payload.data) }
        check(bytes.isNotEmpty()) { "empty picture" }
        cachedBytes[key] = bytes
        return bytes
    }

    /** A thumbnail of the photo, its long side about [side] pixels. */
    suspend fun thumbnail(path: String, side: Int): ImageBitmap {
        val key = "$machineId:$path:$side"
        cachedThumbnails[key]?.let { return it }
        val bytes = bytes(path)
        val image = withContext(Dispatchers.Default) { decodePicture(bytes, side, atMost = false).asImageBitmap() }
        cachedThumbnails[key] = image
        return image
    }

    companion object {
        private fun <V> lru(): MutableMap<String, V> = Collections.synchronizedMap(object : LinkedHashMap<String, V>(32, 0.75f, true) {
            override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, V>?): Boolean = size > 40
        })
        private val cachedBytes = lru<ByteArray>()
        private val cachedThumbnails = lru<ImageBitmap>()
    }
}

/** Pixels of a thumbnail's long side: a 120 dp square at up to 3× density. */
private const val THUMBNAIL_PIXELS = 360

private sealed interface PhotoState {
    data object Loading : PhotoState
    data object Failed : PhotoState
    class Ready(val image: ImageBitmap) : PhotoState
}

/** The photos of a message, square thumbnails above its text; a tap opens them in the viewer, a failed one retries. */
@Composable
internal fun AttachedPhotoRow(paths: List<String>) {
    val actions = LocalChatRowActions.current
    val photos = actions?.photos
    val description = stringResource(R.string.chat_photo)
    val images = remember(paths, photos, description) {
        paths.map { path -> ViewerImage("photo:$path", description) { checkNotNull(photos) { "photos unavailable" }.bytes(path) } }
    }
    PhotoRow(alignEnd = true) {
        paths.forEachIndexed { index, path ->
            AttachedPhoto(path, photos, single = paths.size == 1) { actions?.openImages(images, index) }
        }
    }
}

@Composable
private fun AttachedPhoto(path: String, photos: AttachedPhotos?, single: Boolean, onOpen: () -> Unit) {
    var attempt by remember(path) { mutableIntStateOf(0) }
    val state by produceState<PhotoState>(PhotoState.Loading, path, photos, attempt) {
        value = PhotoState.Loading
        value = if (photos == null) PhotoState.Failed else try {
            PhotoState.Ready(photos.thumbnail(path, THUMBNAIL_PIXELS))
        } catch (e: CancellationException) {
            throw e
        } catch (_: Throwable) {
            PhotoState.Failed
        }
    }
    val picture = state
    val description = stringResource(R.string.chat_photo_named, path.substringAfterLast('/'))
    PhotoThumbnail(
        image = (picture as? PhotoState.Ready)?.image, failed = picture is PhotoState.Failed, single = single,
        onClick = onOpen, onRetry = { attempt++ }, description = description,
    )
}

/**
 * The file paths of a message as chips under it (iOS `FileChips`): a tap opens the file on the session's machine.
 * Only those there once the machine answers. Nothing when the page cannot open files (no files pane, e.g. Persona' own conversations).
 */
@Composable
internal fun FileChips(paths: List<String>, alignEnd: Boolean) {
    val actions = LocalChatRowActions.current
    val open = actions?.openPath ?: return
    if (paths.isEmpty()) return
    // Only the paths there on the machine (PathPresence): a path another machine's, or deleted, only failed to open.
    val presence = actions.paths
    if (presence != null) LaunchedEffect(presence, paths) { presence.ask(paths) }
    val shown = if (presence == null) paths else paths.filter(presence::linkable)
    if (shown.isEmpty()) return
    Box(Modifier.fillMaxWidth().padding(top = Tokens.Space.xs), contentAlignment = if (alignEnd) Alignment.CenterEnd else Alignment.CenterStart) {
        Row(Modifier.horizontalScroll(rememberScrollState()), horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            shown.forEach { path -> PathChip(fileLabel(path), fileIcon(path), onClick = { open(path, null) }) }
        }
    }
}

/** What a file chip says: the last part of the path. */
internal fun fileLabel(path: String): String = path.trimEnd('/').substringAfterLast('/').ifEmpty { path }

/** An icon for a path by its extension (a folder when it ends with `/`). */
internal fun fileIcon(path: String): PhIcon {
    if (path.endsWith("/")) return PhIcons.FolderSimple
    return when (path.substringAfterLast('/').substringAfterLast('.', "").lowercase()) {
        "png", "jpg", "jpeg", "gif", "webp", "heic", "svg" -> PhIcons.Image
        "kt", "kts", "swift", "go", "py", "js", "ts", "tsx", "jsx", "java", "rs", "c", "h", "cpp", "sh", "rb",
        "json", "yaml", "yml", "toml", "html", "css", "sql", "xml", "gradle" -> PhIcons.Code
        "md", "txt", "log", "csv", "pdf" -> PhIcons.FileText
        else -> PhIcons.File
    }
}
