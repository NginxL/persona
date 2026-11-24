package persona.feature.inbox

import android.content.Context
import androidx.compose.runtime.staticCompositionLocalOf
import coil3.imageLoader
import coil3.request.ImageRequest
import coil3.request.SuccessResult
import coil3.toBitmap
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import persona.core.designsystem.ViewerImage
import java.io.ByteArrayOutputStream
import persona.core.inbox.*

/**
 * Opens a picture of a Feishu conversation full screen (iOS `LarkImageGallery`): the conversation page provides it, and
 * every [LarkPart.Image] drawn under it (in a bubble, a card, a forwarded bundle, a link's cover) calls it on a tap.
 * Null: pictures don't open (a page that has no viewer).
 */
internal val LocalLarkImageOpener = staticCompositionLocalOf<((LarkPart.Image) -> Unit)?> { null }

/** The pictures of [messages] in order, as the viewer pages through them: the messages' own and their links' covers. */
internal fun larkImages(messages: List<LarkMessage>): List<LarkPart.Image> = messages.flatMap { message ->
    if (message.recalled) emptyList() else message.parts.mapNotNull { part ->
        when (part) {
            is LarkPart.Image -> part
            is LarkPart.External -> part.cover
            else -> null
        }
    }
}.distinct()

/** Where the viewer starts for [image]: its place among [all], or [all] is just it when it is not among them (a card's). */
internal fun larkGallery(all: List<LarkPart.Image>, image: LarkPart.Image): Pair<List<LarkPart.Image>, Int> {
    val index = all.indexOf(image)
    return if (index >= 0) all to index else listOf(image) to 0
}

/** A picture for [persona.core.designsystem.ImageViewer]: the hub's authenticated bytes, else what Coil fetches. */
internal fun larkViewerImage(context: Context, image: LarkPart.Image, index: Int, description: String): ViewerImage {
    val authenticated = image.model as? AuthenticatedLarkImage
    return ViewerImage(authenticated?.cacheKey ?: "${image.model}#$index", image.description.ifBlank { description },
        image.width.takeIf { it > 1 }, image.height.takeIf { it > 1 }) {
        authenticated?.read() ?: run {
            val result = context.imageLoader.execute(ImageRequest.Builder(context).data(image.model).build())
            check(result is SuccessResult) { "image unavailable" }
            withContext(Dispatchers.Default) {
                ByteArrayOutputStream().also { result.image.toBitmap().compress(android.graphics.Bitmap.CompressFormat.PNG, 100, it) }.toByteArray()
            }
        }
    }
}
