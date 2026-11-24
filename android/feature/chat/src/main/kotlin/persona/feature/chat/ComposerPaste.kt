package persona.feature.chat

import android.content.ClipData
import android.content.ClipDescription
import android.content.ClipboardManager
import android.content.Context
import android.net.Uri
import android.webkit.MimeTypeMap
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.Stable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.core.content.FileProvider
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JIconButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.icons.PersonaIcons
import java.io.File

/**
 * Whether the clipboard holds a picture the composer has not offered yet (iOS `PasteboardImages`). It reads only the
 * clip's description, which shows no "pasted from your clipboard" notice; Paste reads the picture itself. Android answers
 * only while the app has the focus, so [refresh] runs again when the window gets it back, and on every change of clip.
 * A copy taken or put away ([dismiss]) is not offered again.
 */
@Stable
internal class ClipboardImages(internal val clipboard: ClipboardManager?) {
    var offered by mutableStateOf(false)
        private set
    /** Changes heard while the app is in front; with the clip's time it tells one copy from the next. */
    private var changes = 0
    private var handled: String? = null

    private fun current(): String? = runCatching { clipboard?.primaryClipDescription }.getOrNull()
        ?.takeIf(::holdsImage)?.let { "${it.timestamp}/$changes" }

    fun refresh() {
        val now = current()
        offered = now != null && now != handled
    }

    fun changed() {
        changes++
        refresh()
    }

    /** Taken or put away: the same copy is not offered again. */
    fun dismiss() {
        handled = current()
        refresh()
    }
}

/** Whether a clip says it carries a picture. */
internal fun holdsImage(description: ClipDescription): Boolean = description.hasMimeType("image/*")

/** The composer's [ClipboardImages], asked again as the window gets the focus back and on every change of clip. */
@Composable
internal fun rememberClipboardImages(focused: Boolean): ClipboardImages {
    val context = LocalContext.current
    val images = remember(context) { ClipboardImages(context.getSystemService(ClipboardManager::class.java)) }
    val windowFocused = LocalWindowInfo.current.isWindowFocused
    LaunchedEffect(images, windowFocused, focused) { if (windowFocused) images.refresh() }
    DisposableEffect(images) {
        val listener = ClipboardManager.OnPrimaryClipChangedListener { images.changed() }
        val _ = runCatching { images.clipboard?.addPrimaryClipChangedListener(listener) }
        onDispose { val _ = runCatching { images.clipboard?.removePrimaryClipChangedListener(listener) } }
    }
    return images
}

/**
 * The pictures [clip] carries, copied into `cache/chat-captures/` (the clipboard's grant ends with the clip) and handed
 * out through the chat's FileProvider like a camera photo ([share]), at most [limit]. One that cannot be read is left
 * out. Call off the main thread.
 */
internal fun pastedImages(context: Context, clip: ClipData, limit: Int,
    share: (File) -> Uri = { FileProvider.getUriForFile(context, "${context.packageName}.chat.files", it) }): List<Uri> {
    val resolver = context.contentResolver
    val folder = File(context.cacheDir, "chat-captures").apply { mkdirs() }
    val anyImage = holdsImage(clip.description)
    return (0 until clip.itemCount).asSequence().mapNotNull { clip.getItemAt(it).uri }
        .map { it to runCatching { resolver.getType(it) }.getOrNull() }
        .filter { (_, type) -> type?.startsWith("image/") ?: anyImage }
        .take(limit)
        .mapNotNull { (source, type) ->
            runCatching {
                val extension = type?.let { MimeTypeMap.getSingleton().getExtensionFromMimeType(it) } ?: "png"
                val file = File.createTempFile("pasted-", ".$extension", folder)
                try {
                    checkNotNull(resolver.openInputStream(source)).use { input -> file.outputStream().use { input.copyTo(it) } }
                } catch (error: Throwable) {
                    file.delete()
                    throw error
                }
                runCatching { share(file) }.onFailure { file.delete() }.getOrThrow()
            }.getOrNull()
        }.toList()
}

/**
 * Paste over the focused composer while the clipboard holds a picture (iOS `PasteImageChip`, as Signal and Telegram offer):
 * one tap attaches it as a photo, ✕ ("Don't paste") puts it away. The text field itself only pastes text.
 */
@Composable
internal fun PasteImageChip(onPaste: () -> Unit, onDismiss: () -> Unit) {
    Row(Modifier.fillMaxWidth().testTag("composer-paste-image"), verticalAlignment = Alignment.CenterVertically) {
        JButton(stringResource(R.string.chat_paste_image), onPaste, kind = JButtonKind.Secondary, size = JButtonSize.Small,
            icon = PersonaIcons.photo)
        Spacer(Modifier.weight(1f))
        JIconButton(PersonaIcons.close, stringResource(R.string.chat_paste_dont), onDismiss, raised = false, tint = PersonaTheme.palette.ink2)
    }
}
