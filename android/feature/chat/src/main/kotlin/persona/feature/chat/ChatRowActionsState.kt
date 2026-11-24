package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatRepository

/** The row actions, with the same remember key and callback ownership as before. */
@Composable
internal fun rememberChatRowActions(
    key: SessionKey, repository: ChatRepository, replyImages: persona.core.contracts.api.ReplyImageSource?,
    localImages: LocalMarkdownImages?, photos: AttachedPhotos?, retryId: String?, exhausted: ExhaustedRecovery?,
    onOpenPath: ((String, Int?) -> Unit)?, pathPresence: PathPresence?, files: persona.core.contracts.api.SessionFiles?,
    cwd: String?, onOpenPathState: ((String, Int?) -> Unit)?, onOpenImages: (List<persona.core.designsystem.ViewerImage>, Int) -> Unit,
    currentItems: () -> List<persona.core.contracts.Item>,
    menu: MessageMenuState, onRetry: () -> Unit, imageDescription: String,
): ChatRowActions {
    val currentOpenPath by rememberUpdatedState(onOpenPathState)
    return remember(key, repository, replyImages, localImages, photos, retryId, exhausted, onOpenPath != null, pathPresence, files, cwd) {
        ChatRowActions(key, fullText = { item -> repository.fullText(key, item.id) },
            openPath = if (onOpenPath != null) ({ path, line -> currentOpenPath?.invoke(path, line) }) else null,
            paths = pathPresence,
            photos = photos, pictures = { chatViewerImages(key, currentItems(), repository, imageDescription) },
            openImages = onOpenImages,
            menu = menu, retryLineId = retryId, onRetry = onRetry, imageDescription = imageDescription,
            thumbnails = files?.let { sessionFiles ->
                val directory = cwd.orEmpty().ifBlank { "~" }
                ({ path -> "${key.machineId}\n$directory\n$path" to { pixels: Int -> sessionFiles.thumbnail(key.machineId, directory, path, pixels) } })
            }, viewerImage = { item, description -> chatViewerImage(key, item, repository, description) }, exhausted = exhausted,
            replyImages = replyImages, localImages = localImages)
    }
}
