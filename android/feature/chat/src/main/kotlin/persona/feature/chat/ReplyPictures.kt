package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.combinedClickable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.ColorFilter
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.painter.BitmapPainter
import androidx.compose.ui.graphics.vector.rememberVectorPainter
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.PlaceholderVerticalAlign
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.rememberTextMeasurer
import androidx.compose.ui.unit.Density
import androidx.compose.ui.unit.dp
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import com.mikepenz.markdown.model.ImageData
import com.mikepenz.markdown.model.ImageTransformer
import com.mikepenz.markdown.model.ImageWidth
import com.mikepenz.markdown.model.PlaceholderConfig
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import persona.core.contracts.api.ReplyImageSource
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.R as DesignR

/** Only the reply's explicitly supplied author can opt into automatic loads. Other Markdown surfaces stay inert. */
@Composable
internal fun ReplyMarkdown(text: String, id: String, ownReply: Boolean, selectable: Boolean = false,
    viewKeys: ((Int) -> String)? = null, streaming: Boolean? = null) {
    ReadingContent {
        val actions = LocalChatRowActions.current
        val source = actions?.replyImages
        val localImages = actions?.localImages
        if ((source == null && localImages == null) || "![" !in text || LocalInPicture.current) {
            ChatMarkdown(text, id, selectable = selectable, viewKeys = viewKeys, streaming = streaming)
        } else {
            WithReplyPictures(text, id, source, localImages, ownReply, actions.openImages, actions.openPath) { markdown ->
                ChatMarkdown(markdown, id, selectable = selectable, viewKeys = viewKeys, streaming = streaming)
            }
        }
    }
}

/** Inbox, Lark and connector authors control this text. Each picture waits for the reader. */
@Composable
fun ExternalReplyMarkdown(text: String, source: ReplyImageSource, modifier: Modifier = Modifier) {
    var viewer by remember(source, text) { mutableStateOf<ViewerRequest?>(null) }
    Box(modifier) {
        WithReplyPictures(text, text, source, localImages = null, ownReply = false, open = { images, index -> viewer = ViewerRequest(images, index) }, openPath = null) { markdown ->
            ReadingContent { ChatMarkdown(markdown, selectable = false) }
        }
    }
    viewer?.let { persona.feature.chat.media.ChatImageViewer(it.images, it.index, onDismiss = { viewer = null }) }
}

@Composable
private fun WithReplyPictures(text: String, id: String, source: ReplyImageSource?, localImages: LocalMarkdownImages?,
    ownReply: Boolean, open: (List<ViewerImage>, Int) -> Unit, openPath: ((String, Int?) -> Unit)?,
    content: @Composable (String) -> Unit) {
    key(id, source, localImages, ownReply) {
        val allowLocal = localImages != null
        val document by produceState<ReplyImageDocument?>(null, text, allowLocal) {
            value = withContext(Dispatchers.Default) { ReplyImageDocument.parse(text, allowLocal = allowLocal) }
        }
        val doc = document?.takeIf { text.startsWith(it.original) }
        if (doc == null) content(text)
        else {
            val features by (source?.features ?: remember { kotlinx.coroutines.flow.MutableStateFlow(emptySet<String>()) }).collectAsStateWithLifecycle()
            val through = source != null && ReplyImageSource.FEATURE in features
            val allowed = remember(through, ownReply) { mutableStateMapOf<String, Boolean>() }
            val gallery = remember(doc, source, localImages, through, ownReply, open, openPath) {
                ReplyGallery(doc, source, localImages, through, ownReply, allowed, open, openPath)
            }
            CompositionLocalProvider(LocalReplyGallery provides gallery) {
                content(doc.markdown)
            }
        }
    }
}

internal class ReplyGallery(
    val document: ReplyImageDocument,
    val source: ReplyImageSource?,
    val localImages: LocalMarkdownImages?,
    val throughHub: Boolean,
    private val ownReply: Boolean,
    private val allowed: MutableMap<String, Boolean>,
    private val open: (List<ViewerImage>, Int) -> Unit,
    val openPath: ((String, Int?) -> Unit)? = null,
) {
    fun canLoad(picture: ReplyPicture): Boolean =
        if (picture.isLocal) localImages != null
        else source != null && ((throughHub && ownReply) || allowed[picture.url] == true)

    fun allow(picture: ReplyPicture) {
        if (!picture.isLocal) allowed[picture.url] = true
    }

    fun show(index: Int) {
        val images = document.pictures.mapIndexed { i, picture ->
            val localPath = picture.localPath
            if (localPath != null && localImages != null) {
                val label = picture.alt.ifEmpty { LocalMarkdownPath.fileName(localPath) }
                ViewerImage("local:${localImages.cacheKey(localPath)}", label, askToLoad = false) {
                    localImages.bytes(localPath)
                }
            } else {
                ViewerImage("$i:${picture.url}", picture.alt.ifEmpty { picture.host }, askToLoad = !canLoad(picture)) {
                    // A gated gallery page, including one composed ahead of a swipe, waits for its own button.
                    checkNotNull(source) { "reply image source unavailable" }
                        .load(picture.url, ReplyImageLayout.viewer, allowDirect = !throughHub)
                }
            }
        }
        open(images, index)
    }
}

internal val LocalReplyGallery = staticCompositionLocalOf<ReplyGallery?> { null }

private sealed interface TileState {
    data object Loading : TileState
    data class Failed(val reason: String? = null) : TileState
    class Ready(val bitmap: Bitmap) : TileState
}

@Composable
private fun thumbnail(gallery: ReplyGallery, picture: ReplyPicture, size: Int, allowed: Boolean, attempt: Int): TileState {
    val localPath = picture.localPath
    val localImages = gallery.localImages
    if (localPath != null && localImages != null) {
        val resources = LocalResources.current
        val recordedFailure = localImages.failureOf(localPath)
        if (recordedFailure != null) return TileState.Failed(recordedFailure.reason)
        val cached = remember(localImages, localPath, size, attempt) {
            if (attempt == 0) localImages.cachedThumbnail(localPath, size) else null
        }
        if (cached != null) return TileState.Ready(cached)
        return key(localImages, localPath, size, attempt) {
            val state by produceState<TileState>(TileState.Loading) {
                value = try {
                    val bitmap = localImages.thumbnail(localPath, size, attempt)
                    localImages.clearFailure(localPath)
                    TileState.Ready(bitmap)
                } catch (e: CancellationException) {
                    throw e
                } catch (e: Throwable) {
                    val reason = LocalMarkdownImages.failureReason(resources, e)
                    localImages.recordFailure(localPath, attempt, reason)
                    TileState.Failed(reason)
                }
            }
            state
        }
    }
    return key(gallery.source, picture.url, size, allowed, attempt) {
        val state by produceState<TileState>(TileState.Loading) {
            val source = gallery.source
            if (allowed && source != null) value = try {
                val bytes = source.load(picture.url, size, allowDirect = !gallery.throughHub)
                TileState.Ready(withContext(Dispatchers.Default) { decodePicture(bytes, size) })
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                TileState.Failed()
            }
        }
        state
    }
}

@Composable
internal fun ReplyPictureRun(gallery: ReplyGallery, range: IntRange) {
    BoxWithConstraints(Modifier.fillMaxWidth()) {
        val column = minOf(maxWidth.value, ReplyImageLayout.maximumWidth)
        if (range.first == range.last) {
            ReplyTile(gallery, range.first, column, square = null)
        } else {
            val count = range.last - range.first + 1
            val square = ReplyImageLayout.square(count, column)
            val shown = range.take(ReplyImageLayout.mostShown)
            Column(verticalArrangement = Arrangement.spacedBy(ReplyImageLayout.gap.dp)) {
                for (row in shown.chunked(ReplyImageLayout.columns(count))) {
                    Row(horizontalArrangement = Arrangement.spacedBy(ReplyImageLayout.gap.dp)) {
                        for (i in row) ReplyTile(gallery, i, column, square,
                            more = if (i == shown.last()) count - shown.size else 0)
                    }
                }
            }
        }
    }
}

@Composable
private fun ReplyTile(gallery: ReplyGallery, index: Int, column: Float, square: Float?, more: Int = 0) {
    val picture = gallery.document.pictures[index]
    val localPath = picture.localPath
    val localImages = gallery.localImages
    val allowed = gallery.canLoad(picture)
    var webAttempt by remember(picture.url) { mutableIntStateOf(0) }
    val attempt = if (localPath != null && localImages != null) localImages.attemptOf(localPath) else webAttempt
    val longSide = square ?: maxOf(ReplyImageLayout.height, column)
    val state = thumbnail(gallery, picture, ReplyImageLayout.request(longSide, LocalDensity.current.density), allowed, attempt)
    val bitmap = (state as? TileState.Ready)?.bitmap
    val failedState = state as? TileState.Failed
    val (w, h) = when {
        square != null -> square to square
        failedState != null -> Tokens.Size.box80.value to Tokens.Size.box80.value
        else -> ReplyImageLayout.single(bitmap?.width, bitmap?.height, column)
    }
    val load = stringResource(DesignR.string.designsystem_load_image)
    val retryLabel = stringResource(R.string.chat_delivery_retry)
    val label = picture.alt.ifEmpty { if (localPath != null) LocalMarkdownPath.fileName(localPath) else picture.host }
    val action = when {
        !allowed -> load
        failedState != null -> retryLabel
        bitmap != null -> label
        else -> null
    }
    val uri = LocalUriHandler.current
    val original = stringResource(R.string.chat_reply_image_original)
    val openFileLabel = stringResource(R.string.chat_local_image_open_file)
    var menu by remember(picture.url) { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    val shape = PersonaTheme.shapes.controlShape
    val triggerRetry: () -> Unit = {
        if (localPath != null && localImages != null) localImages.retry(localPath)
        else webAttempt++
    }
    val canLongPress = localPath == null || gallery.openPath != null
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        Box(Modifier.size(w.dp, h.dp).clip(shape).background(palette.sunken)
            .border(Tokens.Size.hairline, palette.line, shape)
            .combinedClickable(role = Role.Button, onClickLabel = action,
                onLongClickLabel = if (localPath != null) openFileLabel else original,
                onLongClick = if (canLongPress) ({ menu = true }) else null,
                onClick = {
                    when {
                        !allowed -> gallery.allow(picture)
                        failedState != null -> triggerRetry()
                        bitmap != null -> gallery.show(index)
                    }
                }).semantics { contentDescription = if (more > 0) "$label +$more" else label }, contentAlignment = Alignment.Center) {
            val opacity by animateFloatAsState(if (bitmap != null) 1f else 0f,
                animationSpec = if (PersonaTheme.reduceMotion) tween(Tokens.Motion.reduced) else SharedMotion.reveal(), label = "reply picture")
            if (bitmap != null) Image(bitmap.asImageBitmap(), null, Modifier.fillMaxSize().alpha(opacity), contentScale = ContentScale.Crop)
            else if (!allowed) InterfaceContent { Text(load, style = PersonaTheme.type.callout, color = palette.ink2) }
            else if (failedState != null) JIcon(PersonaIcons.photo, tint = palette.ink3)
            if (more > 0) Box(Modifier.fillMaxSize().background(palette.scrim), contentAlignment = Alignment.Center) {
                InterfaceContent { Text("+$more", style = PersonaTheme.type.title, color = palette.onMediaBackdrop) }
            }
            if (menu) InterfaceContent {
                val menuActions = if (localPath != null) {
                    listOfNotNull(gallery.openPath?.let { open ->
                        JAction(openFileLabel, PersonaIcons.openExternal, section = LocalMarkdownPath.fileName(localPath)) {
                            menu = false
                            open(localPath, null)
                        }
                    })
                } else {
                    listOf(JAction(original, PersonaIcons.openExternal, section = picture.host) {
                        menu = false
                        uri.openUri(picture.url)
                    })
                }
                if (menuActions.isNotEmpty()) JMenu(true, { menu = false }, menuActions)
            }
        }
        if (failedState != null && localPath != null) {
            val reason = failedState.reason ?: stringResource(R.string.chat_local_image_load_failed)
            InterfaceContent {
                Row(
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
                    verticalAlignment = Alignment.CenterVertically,
                ) {
                    Text(reason, style = PersonaTheme.type.caption, color = palette.ink2)
                    Text(
                        retryLabel,
                        style = PersonaTheme.type.caption,
                        color = palette.accent,
                        modifier = Modifier.clickable(role = Role.Button, onClick = triggerRetry),
                    )
                }
            }
        }
    }
}

/** The Markdown renderer keeps the image in the text's line using this fixed placeholder before and after loading. */
@Composable
internal fun rememberInlineReplyPictures(gallery: ReplyGallery?, body: TextStyle): ImageTransformer? {
    if (gallery == null) return null
    val measurer = rememberTextMeasurer()
    val density = LocalDensity.current
    // Some styles use the native font's metrics with an unspecified lineHeight.
    val height = remember(body, density, measurer) { with(density) {
        measurer.measure("M", style = body, maxLines = 1).size.height.toDp().value
    } }
    return remember(gallery, height) { InlineReplyPictures(gallery, height) }
}

private class InlineReplyPictures(private val gallery: ReplyGallery, private val height: Float) : ImageTransformer {
    @Composable
    override fun transform(link: String): ImageData? {
        val index = gallery.document.inline(link) ?: return null
        val picture = gallery.document.pictures[index]
        val allowed = gallery.canLoad(picture)
        var attempt by remember(picture.url) { mutableIntStateOf(0) }
        val density = LocalDensity.current
        val state = thumbnail(gallery, picture, ReplyImageLayout.request(height, density.density), allowed, attempt)
        val bitmap = (state as? TileState.Ready)?.bitmap
        val painter = if (bitmap == null) rememberVectorPainter(PersonaIcons.photo.regular)
            else remember(bitmap) { BitmapPainter(bitmap.asImageBitmap()) }
        val description = picture.alt.ifEmpty { picture.host }
        val load = stringResource(DesignR.string.designsystem_load_image)
        val retry = stringResource(R.string.chat_delivery_retry)
        val uri = LocalUriHandler.current
        val original = stringResource(R.string.chat_reply_image_original)
        var menu by remember(picture.url) { mutableStateOf(false) }
        if (menu) InterfaceContent {
            JMenu(true, { menu = false }, listOf(JAction(original, PersonaIcons.openExternal, section = picture.host) {
                menu = false
                uri.openUri(picture.url)
            }))
        }
        return ImageData(painter, Modifier.combinedClickable(role = Role.Button,
            onClickLabel = if (!allowed) load else if (state is TileState.Failed) retry else description,
            onLongClickLabel = original, onLongClick = { menu = true }, onClick = {
                when { !allowed -> gallery.allow(picture); state is TileState.Failed -> attempt++; bitmap != null -> gallery.show(index) }
            }), contentDescription = description, colorFilter = if (bitmap == null) ColorFilter.tint(PersonaTheme.palette.ink3) else null)
    }

    override fun placeholderConfig(link: String, density: Density, containerSize: Size, imageWidth: ImageWidth, imageSize: Size,
        imageSizeChanged: ((String, Size) -> Unit)?): PlaceholderConfig {
        return PlaceholderConfig(Size(height, height), PlaceholderVerticalAlign.TextCenter)
    }
}
