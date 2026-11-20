package persona.feature.chat

import android.graphics.Bitmap
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
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
    val actions = LocalChatRowActions.current
    val source = actions?.replyImages
    if (source == null || "![" !in text || LocalInPicture.current) {
        ChatMarkdown(text, id, selectable = selectable, viewKeys = viewKeys, streaming = streaming)
        return
    }
    WithReplyPictures(text, id, source, ownReply, actions.openImages) { markdown ->
        ChatMarkdown(markdown, id, selectable = selectable, viewKeys = viewKeys, streaming = streaming)
    }
}

/** Inbox, Lark and connector authors control this text. Each picture waits for the reader. */
@Composable
fun ExternalReplyMarkdown(text: String, source: ReplyImageSource, modifier: Modifier = Modifier) {
    var viewer by remember(source, text) { mutableStateOf<ViewerRequest?>(null) }
    Box(modifier) {
        WithReplyPictures(text, text, source, ownReply = false, open = { images, index -> viewer = ViewerRequest(images, index) }) { markdown ->
            ChatMarkdown(markdown, selectable = false)
        }
    }
    viewer?.let { persona.feature.chat.media.ChatImageViewer(it.images, it.index, onDismiss = { viewer = null }) }
}

@Composable
private fun WithReplyPictures(text: String, id: String, source: ReplyImageSource, ownReply: Boolean,
    open: (List<ViewerImage>, Int) -> Unit, content: @Composable (String) -> Unit) {
    key(id, source, ownReply) {
        val document by produceState<ReplyImageDocument?>(null, text) {
            value = withContext(Dispatchers.Default) { ReplyImageDocument.parse(text) }
        }
        val doc = document?.takeIf { text.startsWith(it.original) }
        if (doc == null) content(text)
        else {
            val features by source.features.collectAsStateWithLifecycle()
            val through = ReplyImageSource.FEATURE in features
            val allowed = remember(through, ownReply) { mutableStateMapOf<String, Boolean>() }
            val gallery = remember(doc, source, through, ownReply, open) {
                ReplyGallery(doc, source, through, ownReply, allowed, open)
            }
            CompositionLocalProvider(LocalReplyGallery provides gallery) {
                content(doc.markdown)
            }
        }
    }
}

internal class ReplyGallery(val document: ReplyImageDocument, val source: ReplyImageSource, val throughHub: Boolean,
    private val ownReply: Boolean, private val allowed: MutableMap<String, Boolean>,
    private val open: (List<ViewerImage>, Int) -> Unit) {
    fun canLoad(picture: ReplyPicture) = (throughHub && ownReply) || allowed[picture.url] == true
    fun allow(picture: ReplyPicture) { allowed[picture.url] = true }

    fun show(index: Int) {
        val images = document.pictures.mapIndexed { i, picture ->
            ViewerImage("$i:${picture.url}", picture.alt.ifEmpty { picture.host }, askToLoad = !canLoad(picture)) {
                // A gated gallery page, including one composed ahead of a swipe, waits for its own button.
                source.load(picture.url, ReplyImageLayout.viewer, allowDirect = !throughHub)
            }
        }
        open(images, index)
    }
}

internal val LocalReplyGallery = staticCompositionLocalOf<ReplyGallery?> { null }

private sealed interface TileState {
    data object Loading : TileState
    data object Failed : TileState
    class Ready(val bitmap: Bitmap) : TileState
}

@Composable
private fun thumbnail(gallery: ReplyGallery, picture: ReplyPicture, size: Int, allowed: Boolean, attempt: Int): TileState =
    key(gallery.source, picture.url, size, allowed, attempt) {
        val state by produceState<TileState>(TileState.Loading) {
            if (allowed) value = try {
                val bytes = gallery.source.load(picture.url, size, allowDirect = !gallery.throughHub)
                TileState.Ready(withContext(Dispatchers.Default) { decodePicture(bytes, size) })
            } catch (e: CancellationException) {
                throw e
            } catch (_: Exception) {
                TileState.Failed
            }
        }
        state
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
    val allowed = gallery.canLoad(picture)
    var attempt by remember(picture.url) { mutableIntStateOf(0) }
    val longSide = square ?: maxOf(ReplyImageLayout.height, column)
    val state = thumbnail(gallery, picture, ReplyImageLayout.request(longSide, LocalDensity.current.density), allowed, attempt)
    val bitmap = (state as? TileState.Ready)?.bitmap
    val (w, h) = when {
        square != null -> square to square
        state == TileState.Failed -> Tokens.Size.box80.value to Tokens.Size.box80.value
        else -> ReplyImageLayout.single(bitmap?.width, bitmap?.height, column)
    }
    val load = stringResource(DesignR.string.designsystem_load_image)
    val label = picture.alt.ifEmpty { picture.host }
    val action = when {
        !allowed -> load
        state == TileState.Failed -> stringResource(R.string.chat_delivery_retry)
        bitmap != null -> label
        else -> null
    }
    val uri = LocalUriHandler.current
    val original = stringResource(R.string.chat_reply_image_original)
    var menu by remember(picture.url) { mutableStateOf(false) }
    val palette = PersonaTheme.palette
    val shape = PersonaTheme.shapes.controlShape
    Box(Modifier.size(w.dp, h.dp).clip(shape).background(palette.sunken)
        .border(Tokens.Size.hairline, palette.line, shape)
        .combinedClickable(role = Role.Button, onClickLabel = action, onLongClickLabel = original,
            onLongClick = { menu = true }, onClick = {
                when { !allowed -> gallery.allow(picture); state == TileState.Failed -> attempt++; bitmap != null -> gallery.show(index) }
            }).semantics { contentDescription = if (more > 0) "$label +$more" else label }, contentAlignment = Alignment.Center) {
        val opacity by animateFloatAsState(if (bitmap != null) 1f else 0f,
            animationSpec = if (PersonaTheme.reduceMotion) tween(Tokens.Motion.reduced) else SharedMotion.reveal(), label = "reply picture")
        if (bitmap != null) Image(bitmap.asImageBitmap(), null, Modifier.fillMaxSize().alpha(opacity), contentScale = ContentScale.Crop)
        else if (!allowed) Text(load, style = PersonaTheme.type.callout, color = palette.ink2)
        else if (state == TileState.Failed) JIcon(PersonaIcons.photo, tint = palette.ink3)
        if (more > 0) Box(Modifier.fillMaxSize().background(palette.scrim), contentAlignment = Alignment.Center) {
            Text("+$more", style = PersonaTheme.type.title, color = palette.onMediaBackdrop)
        }
        if (menu) JMenu(true, { menu = false }, listOf(JAction(original, PersonaIcons.openExternal, section = picture.host) {
            menu = false
            uri.openUri(picture.url)
        }))
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
        if (menu) JMenu(true, { menu = false }, listOf(JAction(original, PersonaIcons.openExternal, section = picture.host) {
            menu = false
            uri.openUri(picture.url)
        }))
        return ImageData(painter, Modifier.combinedClickable(role = Role.Button,
            onClickLabel = if (!allowed) load else if (state == TileState.Failed) retry else description,
            onLongClickLabel = original, onLongClick = { menu = true }, onClick = {
                when { !allowed -> gallery.allow(picture); state == TileState.Failed -> attempt++; bitmap != null -> gallery.show(index) }
            }), contentDescription = description, colorFilter = if (bitmap == null) ColorFilter.tint(PersonaTheme.palette.ink3) else null)
    }

    override fun placeholderConfig(link: String, density: Density, containerSize: Size, imageWidth: ImageWidth, imageSize: Size,
        imageSizeChanged: ((String, Size) -> Unit)?): PlaceholderConfig {
        return PlaceholderConfig(Size(height, height), PlaceholderVerticalAlign.TextCenter)
    }
}
