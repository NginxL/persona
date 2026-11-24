package persona.feature.files

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.geometry.isSpecified
import androidx.compose.ui.graphics.painter.Painter
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.style.TextDecoration
import coil3.compose.AsyncImagePainter
import coil3.compose.rememberAsyncImagePainter
import com.mikepenz.markdown.compose.MarkdownElement
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.compose.elements.MarkdownBulletList
import com.mikepenz.markdown.compose.elements.MarkdownOrderedList
import com.mikepenz.markdown.compose.elements.listDepth
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.m3.markdownColor
import com.mikepenz.markdown.m3.markdownTypography
import com.mikepenz.markdown.model.ImageData
import com.mikepenz.markdown.model.ImageTransformer
import com.mikepenz.markdown.model.State
import com.mikepenz.markdown.model.markdownPadding
import com.mikepenz.markdown.model.parseMarkdown
import persona.core.designsystem.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.MarkdownTokenTypes

/**
 * A Markdown file rendered with the chat's look, a block per lazy row so a long document opens at once. [base] is the
 * file's folder: links and images that name files there open in the files pane ([onOpenPath]) or load from the
 * machine ([loadImage]); web links go to the app's link sheet. [header] rows come first (the size line).
 */
@Composable
internal fun FileMarkdown(
    text: String,
    base: String,
    contentPadding: PaddingValues,
    onOpenPath: (String) -> Unit,
    loadImage: suspend (String) -> ByteArray?,
    header: LazyListScope.() -> Unit = {},
) {
    val parsed by produceState<State?>(null, text) {
        value = withContext(Dispatchers.Default) { parseMarkdown(FileMarkdownText.prepare(MathMarkdown.rewrite(text))) }
    }
    val state = parsed
    if (state == null) {
        LazyColumn(Modifier.fillMaxSize(), contentPadding = contentPadding) { header(); item { JLoading() } }
        return
    }
    val parent = LocalUriHandler.current
    val open by rememberUpdatedState(onOpenPath)
    val links = rememberFileLinks(parent, base) { open(it) }
    val load by rememberUpdatedState(loadImage)
    val images = fileImages(base, emptyMap()) { load(it) }
    val side = pagePadding
    CompositionLocalProvider(LocalUriHandler provides links) {
        Markdown(
            state = state,
            colors = fileMarkdownColors(),
            typography = fileMarkdownTypography(),
            padding = markdownPadding(block = Tokens.Space.m),
            imageTransformer = images,
            components = fileMarkdownComponents(),
            success = { success, components, modifier ->
                // Blocks that draw something: the line breaks between them become the block spacing, and a link's
                // reference definition ("[api]: https://…") draws nothing, so it must not add a gap either.
                val blocks = remember(success) { success.node.children.filter { it.type !in invisibleBlocks } }
                LazyColumn(modifier.fillMaxSize(), contentPadding = PaddingValues(
                    top = contentPadding.calculateTopPadding(), bottom = contentPadding.calculateBottomPadding() + Tokens.Space.xl)) {
                    header()
                    itemsIndexed(blocks, contentType = { _, node -> node.type.name }) { index, node ->
                        // A heading starts a section: a little more room above it than between paragraphs.
                        val section = index > 0 && node.type in sectionHeadings
                        // A column: the element puts its block spacer above itself, a Box would stack the two.
                        Column(Modifier.padding(horizontal = side).then(if (section) Modifier.padding(top = Tokens.Space.s) else Modifier)) {
                            ReadingContent { MarkdownElement(node, components, success.content, includeSpacer = index > 0) }
                        }
                    }
                }
            },
        )
    }
}

/**
 * A piece of Markdown in the file viewer's look, laid out in place (not lazily): a notebook's Markdown cell or output.
 * [attachments] are pictures the text names as `attachment:<name>` (base64), the way notebooks paste images.
 */
@Composable
internal fun MarkdownText(
    text: String,
    base: String,
    onOpenPath: (String) -> Unit,
    loadImage: suspend (String) -> ByteArray?,
    modifier: Modifier = Modifier,
    attachments: Map<String, String> = emptyMap(),
) {
    val parsed by produceState<State?>(null, text) {
        value = withContext(Dispatchers.Default) { parseMarkdown(FileMarkdownText.prepare(MathMarkdown.rewrite(text))) }
    }
    val state = parsed ?: return
    val parent = LocalUriHandler.current
    val open by rememberUpdatedState(onOpenPath)
    val links = rememberFileLinks(parent, base) { open(it) }
    val load by rememberUpdatedState(loadImage)
    val images = fileImages(base, attachments) { load(it) }
    CompositionLocalProvider(LocalUriHandler provides links) {
        Markdown(state = state, colors = fileMarkdownColors(), typography = fileMarkdownTypography(),
            padding = markdownPadding(block = Tokens.Space.m), imageTransformer = images, components = fileMarkdownComponents(),
            modifier = modifier)
    }
}

/** A file's images from the machine, and its inline formulas drawn in the text's colour. */
@Composable
private fun fileImages(base: String, attachments: Map<String, String>, load: suspend (String) -> ByteArray?): ImageTransformer {
    val machine = remember(base, attachments) { MachineImages(base, attachments, load) }
    return rememberMathImages(images = machine)
}

/** Links in a file's Markdown: in-page anchors do nothing, files open in the files pane, web links in the link sheet. */
@Composable
private fun rememberFileLinks(parent: UriHandler, base: String, open: (String) -> Unit): UriHandler = remember(parent, base) {
    object : UriHandler {
        override fun openUri(uri: String) {
            when {
                uri.trim().startsWith("#") -> Unit
                else -> FileMarkdownText.target(uri, base)?.let { open(it) } ?: parent.openUri(uri)
            }
        }
    }
}

@Composable
private fun fileMarkdownColors() = PersonaTheme.palette.let { palette ->
    markdownColor(text = palette.ink, codeBackground = palette.sunken, inlineCodeBackground = palette.sunken,
        dividerColor = palette.line, tableBackground = palette.surface, darkTheme = palette.dark,
        highlightText = palette.ink, highlightBackground = palette.marker)
}

@Composable
private fun fileMarkdownTypography() = PersonaTheme.type.let { type ->
    val body = type.body
    markdownTypography(h1 = type.largeTitle, h2 = type.title, h3 = type.headline,
        h4 = type.bodyStrong, h5 = type.callout, h6 = type.caption,
        text = body, paragraph = body, code = type.code, inlineCode = type.code,
        quote = type.callout.copy(color = PersonaTheme.palette.ink2), ordered = body,
        bullet = body, list = body, table = body, alertTitle = type.bodyStrong,
        textLink = TextLinkStyles(SpanStyle(color = PersonaTheme.palette.accent, textDecoration = TextDecoration.Underline)))
}

@Composable
private fun fileMarkdownComponents() = markdownComponents(
    orderedList = { model -> MarkdownOrderedList(model.content, model.node, depth = model.listDepth,
        markerModifier = { firstBaselineAligned().padding(end = Tokens.Space.s) },
        listModifier = { firstBaselineAligned(Modifier.weight(1f)) }) },
    unorderedList = { model -> MarkdownBulletList(model.content, model.node, depth = model.listDepth,
        markerModifier = { firstBaselineAligned().padding(end = Tokens.Space.s) },
        listModifier = { firstBaselineAligned(Modifier.weight(1f)) }) },
    table = { model -> ReadableMarkdownTable(model.content, model.node, model.typography.table) },
    codeFence = { model ->
        MarkdownFence(model.content.substring(model.node.startOffset, model.node.endOffset)) { JCodeBlock(it.code, small = true) }
    },
    codeBlock = { model -> JCodeBlock(model.content.substring(model.node.startOffset, model.node.endOffset).trimIndent(), small = true) },
)

private val invisibleBlocks = setOf(MarkdownTokenTypes.EOL, MarkdownTokenTypes.WHITE_SPACE, MarkdownElementTypes.LINK_DEFINITION)

private val sectionHeadings = setOf(MarkdownElementTypes.ATX_1, MarkdownElementTypes.ATX_2, MarkdownElementTypes.ATX_3,
    MarkdownElementTypes.SETEXT_1, MarkdownElementTypes.SETEXT_2)

/**
 * Images in a Markdown file: a path (relative to the file's folder [base], absolute or "~") is read from the machine
 * through the hub; web images are not fetched (the app has no image network stack), they show nothing.
 */
private class MachineImages(
    private val base: String,
    private val attachments: Map<String, String>,
    private val load: suspend (String) -> ByteArray?,
) : ImageTransformer {
    @Composable
    override fun transform(link: String): ImageData? {
        if (link.startsWith("attachment:")) {
            val name = link.removePrefix("attachment:")
            // Decoded off the main thread: a pasted screenshot can be megabytes of base64.
            val decoded by produceState<ByteArray?>(null, name, attachments) {
                value = withContext(Dispatchers.Default) {
                    attachments[name]?.let { runCatching { java.util.Base64.getMimeDecoder().decode(it) }.getOrNull() }
                }
            }
            val bytes = decoded ?: return null
            return ImageData(rememberAsyncImagePainter(bytes), contentDescription = name)
        }
        val path = remember(link, base) { FileMarkdownText.target(link, base) } ?: return null
        val bytes by produceState<ByteArray?>(null, path) { value = load(path) }
        val data = bytes ?: return null
        return ImageData(rememberAsyncImagePainter(data), contentDescription = path.substringAfterLast('/'))
    }

    @Composable
    override fun intrinsicSize(painter: Painter): Size {
        val density = LocalDensity.current.density
        var size by remember(painter) { mutableStateOf(painter.intrinsicSize) }
        if (painter is AsyncImagePainter) {
            val state by painter.state.collectAsState()
            state.painter?.intrinsicSize?.let { size = it }
        }
        // An image's own pixels count as dp, the way a browser shows a README's images in CSS pixels.
        return if (size.isSpecified) Size(size.width * density, size.height * density) else size
    }
}
