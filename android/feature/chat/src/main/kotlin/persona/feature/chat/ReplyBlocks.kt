package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.wrapContentHeight
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.staticCompositionLocalOf
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.withLink
import androidx.compose.ui.text.withStyle
import androidx.compose.ui.unit.isSpecified
import com.mikepenz.markdown.model.MarkdownAnnotator
import com.mikepenz.markdown.model.markdownAnnotator
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.ast.getTextInNode
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JTextButton
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons

/** The reply around it is still being written (#2984): its quotes do not fold yet; they would fold and open as the
 *  words came. */
internal val LocalReplyStreaming = staticCompositionLocalOf { false }

/** A code block's title bar, as Telegram draws it (#2984): the fence's language on the left (none when it names none),
 *  Copy on the right, which takes the whole block. On the block's own well, so bar and code read as one block. */
@Composable
internal fun CodeBar(language: String, onCopy: () -> Unit) {
    val palette = PersonaTheme.palette
    Row(Modifier.fillMaxWidth().padding(start = Tokens.Space.m),
        verticalAlignment = Alignment.CenterVertically) {
        Text(language, Modifier.weight(1f), style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 1)
        JTextButton(stringResource(R.string.chat_content_copy), onCopy, icon = PhIcons.Copy)
    }
}

/** A code span's words: the backticks off, and the one space CommonMark takes off each end. */
internal fun codeSpanWords(span: String): String {
    val inner = span.trim('`')
    return if (inner.length > 2 && inner.startsWith(" ") && inner.endsWith(" ") && inner.isNotBlank()) inner.substring(1, inner.length - 1) else inner
}

private val LINKS = setOf(MarkdownElementTypes.INLINE_LINK, MarkdownElementTypes.FULL_REFERENCE_LINK,
    MarkdownElementTypes.SHORT_REFERENCE_LINK, MarkdownElementTypes.AUTOLINK)

private fun ASTNode.inLink(): Boolean {
    var p = parent
    while (p != null) {
        if (p.type in LINKS) return true
        p = p.parent
    }
    return false
}

/** Inline code a tap copies (#2984), drawn as code (the body's color on its well), not as a link; inside a link (a path
 *  that opens) it stays the link's. With no [copy] it is only drawn, the same: the words of a message selected in place
 *  take the tap (#4210). */
internal fun copyingCode(style: SpanStyle, copy: ((String) -> Unit)?): MarkdownAnnotator = markdownAnnotator { content, child ->
    if (child.type != MarkdownElementTypes.CODE_SPAN || child.inLink()) return@markdownAnnotator false
    val words = codeSpanWords(child.getTextInNode(content).toString())
    // the style on the words themselves too: a link's styles draw no background. A thin space each side, as the
    // library pads its own inline code.
    if (copy == null) withStyle(style) { append("\u2009$words\u2009") }
    else withLink(LinkAnnotation.Clickable("copy-code", TextLinkStyles(style)) { copy(words) }) {
        withStyle(style) { append("\u2009$words\u2009") }
    }
    true
}

/** A reply's quote, as Telegram draws it (#2984): the accent bar on the left of a faint accent card, a quote mark at
 *  its top end. A long one (more than about four lines of [lineStyle]) is folded with its end faded: a tap opens it,
 *  its chevron folds it again. */
@Composable
internal fun ReplyQuote(lineStyle: TextStyle, content: @Composable () -> Unit) {
    val palette = PersonaTheme.palette
    val streaming = LocalReplyStreaming.current
    // about four lines: the style's line height, else its font size and the line spacing a paragraph has (1.25)
    val body = PersonaTheme.type.body
    val line = listOf(lineStyle.lineHeight, body.lineHeight).firstOrNull { it.isSpecified }
        ?: (listOf(lineStyle.fontSize, body.fontSize).first { it.isSpecified } * 1.25f)
    val fold = with(LocalDensity.current) { (line * 4).toDp() + Tokens.Space.m }
    val foldPx = with(LocalDensity.current) { fold.roundToPx() }
    var full by remember { mutableIntStateOf(0) }
    var open by remember { mutableStateOf(false) }
    val folds = !streaming && full > foldPx + with(LocalDensity.current) { Tokens.Space.xl.roundToPx() }
    val folded = folds && !open
    val expand = stringResource(R.string.chat_content_quote_more)
    val collapse = stringResource(R.string.chat_content_quote_less)
    // The library indents the quote's words (ChatMarkdown's `blockQuote` padding) and draws a hairline at their left
    // edge: the card's own bar is drawn over it.
    Box(Modifier.fillMaxWidth().clip(RoundedCornerShape(Tokens.Radius.badge)).background(palette.accentSoft)
        .drawWithContent {
            drawContent()
            drawRect(palette.accent, size = Size(Tokens.Space.Standard.xs.toPx() * 3 / 4, size.height))
        }) {
        Box(Modifier.fillMaxWidth()
            .padding(end = Tokens.Space.xxl, top = Tokens.Space.s, bottom = Tokens.Space.s)
            .then(if (folded) Modifier.heightIn(max = fold).clipToBounds().fade(palette.ink).clickable(onClickLabel = expand) { open = true }
                .semantics { contentDescription = expand } else Modifier)) {
            Box(Modifier.wrapContentHeight(Alignment.Top, unbounded = true).onSizeChanged { full = it.height }) { content() }
        }
        JIcon(PhIcons.Quotes, null, Modifier.align(Alignment.TopEnd).padding(Tokens.Space.s), size = Tokens.Space.l,
            tint = palette.accent, decorative = true)
        if (folds) JIconButton(if (open) PhIcons.CaretUp else PhIcons.CaretDown, if (open) collapse else expand,
            { open = !open }, Modifier.align(Alignment.BottomEnd))
    }
}

/** The end of a folded quote fades out: a mask, so any opaque [solid] does. */
private fun Modifier.fade(solid: Color): Modifier = graphicsLayer(compositingStrategy = CompositingStrategy.Offscreen).drawWithContent {
    drawContent()
    drawRect(Brush.verticalGradient(0.6f to solid, 1f to solid.copy(alpha = 0f)), blendMode = BlendMode.DstIn)
}

/** Inline code in a reply: the code face in [color] on the well. */
@Composable
internal fun inlineCodeStyle(color: Color): SpanStyle =
    PersonaTheme.type.code.toSpanStyle().copy(color = color, background = PersonaTheme.palette.sunken)
