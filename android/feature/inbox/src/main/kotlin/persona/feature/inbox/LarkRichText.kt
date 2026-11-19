package persona.feature.inbox

import android.content.res.Resources
import androidx.compose.ui.res.stringResource

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.text.*
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontStyle
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import persona.core.designsystem.*
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PhIcons

object LarkEmojiHelper {
    /** Each key's symbol and its name for accessibility. */
    private val EMOJI_MAP: Map<String, Pair<String, Int>> = mapOf(
        "thumbsup" to ("👍" to R.string.inbox_lark_emoji_thumbsup),
        "ok" to ("👌" to R.string.inbox_lark_emoji_ok),
        "done" to ("✅" to R.string.inbox_lark_emoji_done),
        "heart" to ("❤️" to R.string.inbox_lark_emoji_heart),
        "smile" to ("😄" to R.string.inbox_lark_emoji_smile),
        "laugh" to ("😂" to R.string.inbox_lark_emoji_laugh),
        "surprised" to ("😮" to R.string.inbox_lark_emoji_surprised),
        "cry" to ("😭" to R.string.inbox_lark_emoji_cry),
        "clap" to ("👏" to R.string.inbox_lark_emoji_clap),
        "fire" to ("🔥" to R.string.inbox_lark_emoji_fire),
        "party" to ("🎉" to R.string.inbox_lark_emoji_party),
        "thinking" to ("🤔" to R.string.inbox_lark_emoji_thinking),
        "muscle" to ("💪" to R.string.inbox_lark_emoji_muscle),
        "sob" to ("😭" to R.string.inbox_lark_emoji_cry),
        "wave" to ("👋" to R.string.inbox_lark_emoji_wave),
        "jiayi" to ("+1" to R.string.inbox_lark_emoji_jiayi),
        "fist" to ("👊" to R.string.inbox_lark_emoji_fist),
        "shake" to ("🤝" to R.string.inbox_lark_emoji_shake),
        "rose" to ("🌹" to R.string.inbox_lark_emoji_rose),
        "beer" to ("🍺" to R.string.inbox_lark_emoji_beer),
        "cake" to ("🎂" to R.string.inbox_lark_emoji_cake),
        "coffee" to ("☕" to R.string.inbox_lark_emoji_coffee),
    )

    fun symbol(key: String): String = EMOJI_MAP[key.lowercase()]?.first ?: "[$key]"
    fun label(res: Resources, key: String): String {
        val entry = EMOJI_MAP[key.lowercase()] ?: return key
        return res.getString(entry.second)
    }
}

/** Ancestor chain block descriptor from the Lark gateway's rich text metadata. */
data class LarkRichBlock(
    val id: String,
    val type: String,
    val listType: Int = 0,
    val start: Int = 1,
)

/** Raw rich text element leaf from message parts. */
data class LarkRawPart(
    val kind: String = "text",
    val text: String = "",
    val url: String? = null,
    val refId: String? = null,
    val metadata: JsonObject = JsonObject(emptyMap()),
) {
    val blocks: List<LarkRichBlock>
        get() {
            val list = (metadata["blocks"] as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }
            return list.mapNotNull { item ->
                val id = (item["id"] as? JsonPrimitive)?.content ?: return@mapNotNull null
                val type = (item["type"] as? JsonPrimitive)?.content ?: return@mapNotNull null
                val listType = (item["listType"] as? JsonPrimitive)?.content?.toIntOrNull() ?: 0
                val start = (item["start"] as? JsonPrimitive)?.content?.toIntOrNull() ?: 1
                LarkRichBlock(id, type, listType, start)
            }
        }
}

/** Highlighting tokens for code blocks. */
object LarkSyntax {
    enum class Kind { PLAIN, COMMENT, STRING, NUMBER, KEYWORD }
    data class Token(val text: String, val kind: Kind)

    val keywords = setOf(
        "func", "function", "def", "class", "struct", "enum", "interface", "type", "return", "if", "else", "elif",
        "for", "while", "switch", "case", "default", "break", "continue", "var", "let", "const", "import", "from", "package", "public",
        "private", "static", "async", "await", "try", "catch", "throw", "throws", "new", "nil", "null", "None", "true", "false", "True",
        "False", "self", "this", "in", "go", "defer", "select", "chan", "map", "range", "fn", "impl", "pub", "use", "mod", "match",
        "export", "extends", "implements", "guard", "where", "with", "as", "lambda", "yield", "echo", "then", "fi", "do", "done", "val", "fun"
    )

    fun tokens(code: String): List<Token> {
        val out = ArrayList<Token>()
        val plain = StringBuilder()
        fun flush() {
            if (plain.isNotEmpty()) { out += Token(plain.toString(), Kind.PLAIN); plain.clear() }
        }
        fun emit(s: String, k: Kind) { flush(); out += Token(s, k) }
        for ((li, line) in code.split('\n').withIndex()) {
            if (li > 0) plain.append('\n')
            var i = 0
            while (i < line.length) {
                val c = line[i]
                val rest = line.substring(i)
                if (rest.startsWith("//") || (c == '#' && (i == 0 || line[i - 1] == ' ')) || rest.startsWith("--")) {
                    emit(rest, Kind.COMMENT); break
                }
                if (c == '"' || c == '\'' || c == '`') {
                    var j = i + 1
                    while (j < line.length && line[j] != c) { if (line[j] == '\\') j++; j++ }
                    val end = minOf(if (j < line.length) j + 1 else j, line.length)
                    emit(line.substring(i, end), Kind.STRING); i = end; continue
                }
                if (c.isLetter() || c == '_') {
                    var j = i
                    while (j < line.length && (line[j].isLetterOrDigit() || line[j] == '_')) j++
                    val w = line.substring(i, j)
                    if (w in keywords) emit(w, Kind.KEYWORD) else plain.append(w)
                    i = j; continue
                }
                if (c.isDigit()) {
                    var j = i
                    while (j < line.length && (line[j].isLetterOrDigit() || line[j] == '.')) j++
                    emit(line.substring(i, j), Kind.NUMBER); i = j; continue
                }
                plain.append(c); i++
            }
        }
        flush()
        return out
    }
}

/** Markdown parser for bot messages or markdown blocks. */
object LarkMarkdown {
    data class Line(
        val text: String,
        val heading: Int = 0,
        val marker: String? = null,
        val quote: Boolean = false,
        val indent: Int = 0,
    )

    private val headingRe = Regex("^(#{1,6})\\s+")
    private val ulRe = Regex("^[-*+]\\s+")
    private val olRe = Regex("^(\\d+)[.)]\\s+")

    fun lines(src: String): List<Line> {
        val out = ArrayList<Line>()
        for (raw in src.replace("\r\n", "\n").split('\n')) {
            val indent = raw.length - raw.trimStart().length
            var t = raw.trim()
            if (t.isEmpty()) {
                if (out.isNotEmpty() && out.last().text.isNotEmpty()) out += Line("")
                continue
            }
            var heading = 0
            var marker: String? = null
            var quote = false
            if (t.startsWith(">")) { quote = true; t = t.removePrefix(">").trimStart() }
            headingRe.find(t)?.let { heading = it.groupValues[1].length; t = t.substring(it.range.last + 1) }
            if (heading == 0) {
                val ul = ulRe.find(t)
                val ol = olRe.find(t)
                when {
                    ul != null -> { marker = "•"; t = t.substring(ul.range.last + 1) }
                    ol != null -> { marker = ol.groupValues[1] + "."; t = t.substring(ol.range.last + 1) }
                }
            }
            out += Line(t, heading, marker, quote, indent / 2)
        }
        while (out.isNotEmpty() && out.last().text.isEmpty()) out.removeAt(out.size - 1)
        return out
    }
}

/** Structured rich node tree for Lark gateway messages. */
sealed interface LarkRichNode {
    data class Leaf(val part: LarkRawPart) : LarkRichNode
    data class Block(val block: LarkRichBlock, val children: List<LarkRichNode>) : LarkRichNode

    companion object {
        private class Box(val block: LarkRichBlock) {
            val children = ArrayList<Any>()
        }

        fun tree(parts: List<LarkRawPart>): List<LarkRichNode> {
            val root = ArrayList<Any>()
            val open = ArrayList<Box>()
            fun add(x: Any) {
                if (open.isEmpty()) root += x else open.last().children += x
            }
            for (part in parts) {
                val path = part.blocks
                var shared = 0
                while (shared < open.size && shared < path.size && open[shared].block.id == path[shared].id) shared++
                while (open.size > shared) open.removeAt(open.size - 1)
                for (b in path.drop(shared)) {
                    val box = Box(b)
                    add(box)
                    open += box
                }
                add(part)
            }
            fun convert(items: List<Any>): List<LarkRichNode> = items.map { item ->
                if (item is Box) Block(item.block, convert(item.children)) else Leaf(item as LarkRawPart)
            }
            return adoptNestedLists(convert(root))
        }

        fun adoptNestedLists(nodes: List<LarkRichNode>): List<LarkRichNode> = nodes.map { node ->
            if (node !is Block) return@map node
            val inner = adoptNestedLists(node.children)
            if (!node.block.type.endsWith("-list")) return@map Block(node.block, inner)
            val out = ArrayList<LarkRichNode>()
            for (child in inner) {
                val last = out.lastOrNull()
                if (child is Block && child.block.type.endsWith("-list") && last is Block && last.block.type == "list-item") {
                    out[out.size - 1] = Block(last.block, last.children + child)
                } else {
                    out += child
                }
            }
            Block(node.block, out)
        }

        fun marker(b: LarkRichBlock, index: Int): String {
            if (b.type != "ordered-list") return listOf("•", "◦", "▪", "")[b.listType.coerceIn(0, 3)]
            val n = b.start + index
            return when (b.listType) {
                1 -> (if (n in 1..26) ('a' + n - 1).toString() else "$n") + "."
                2 -> (if (n in 1..26) ('A' + n - 1).toString() else "$n") + "."
                else -> "$n."
            }
        }
    }
}

/** Renders a syntax-highlighted code block. */
@Composable
fun LarkCodeBlock(code: String, language: String, modifier: Modifier = Modifier) {
    val copy = rememberCopy()
    val tokens = remember(code) { LarkSyntax.tokens(code) }
    val palette = PersonaTheme.palette
    val shape = RoundedCornerShape(Tokens.Radius.control)
    val codeSize = PersonaTheme.type.code.fontSize

    Column(
        modifier
            .fillMaxWidth()
            .clip(shape)
            .background(palette.ink.copy(alpha = 0.05f))
            .border(RichTextTokens.codeBorder, palette.ink.copy(alpha = 0.1f), shape)
            .padding(Tokens.Space.m)
    ) {
        Row(
            Modifier.fillMaxWidth().padding(bottom = Tokens.Space.xs),
            horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically
        ) {
            Text(
                language.ifBlank { "code" },
                style = PersonaTheme.type.micro,
                color = palette.ink3,
                maxLines = 1
            )
            Row(
                Modifier.clickable { copy(code, null) },
                verticalAlignment = Alignment.CenterVertically,
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)
            ) {
                JIcon(PhIcons.Copy, modifier = Modifier.size(RichTextTokens.codeCopyIcon), tint = palette.accent)
                Text(
                    stringResource(R.string.inbox_lark_code_copy),
                    style = PersonaTheme.type.micro,
                    color = palette.accent,
                    maxLines = 1,
                    softWrap = false,
                    overflow = TextOverflow.Ellipsis
                )
            }
        }
        SelectionContainer {
            val annotated = buildAnnotatedString {
                for (t in tokens) {
                    val color = when (t.kind) {
                        LarkSyntax.Kind.KEYWORD -> palette.accent
                        LarkSyntax.Kind.STRING -> palette.codeString
                        LarkSyntax.Kind.NUMBER -> palette.codeNumber
                        LarkSyntax.Kind.COMMENT -> palette.ink3
                        LarkSyntax.Kind.PLAIN -> palette.ink
                    }
                    withStyle(SpanStyle(color = color, fontFamily = FontFamily.Monospace, fontSize = codeSize)) {
                        append(t.text)
                    }
                }
            }
            Text(annotated, style = PersonaTheme.type.body)
        }
    }
}

/** Renders inline text with rich links, @ mentions, inline code, and emoji formatting. */
@Composable
fun LarkRichInlineText(
    text: String,
    modifier: Modifier = Modifier,
    source: LarkContentSource? = null,
    onOpenProfile: ((String) -> Unit)? = null,
) {
    val pictures = LocalInboxPictureMarkdown.current
    val imageSource = source?.transport?.replyImages
    if ("![" in text && pictures != null && imageSource != null) {
        pictures(text, imageSource, modifier)
        return
    }
    val open = LocalLarkLinkOpener.current
    val uri = LocalUriHandler.current
    val palette = PersonaTheme.palette
    val codeSize = PersonaTheme.type.code.fontSize

    val annotated = remember(text, palette, codeSize) {
        buildAnnotatedString {
            // Regex to find: links, @mentions (@someone), inline code `code`, emojis [key]
            val pattern = Regex("""(https?://[^\s<>"]+)|(@[^\s@]+)|(`[^`]+`)|(\[[^\]]+\])""", RegexOption.IGNORE_CASE)
            var cursor = 0
            pattern.findAll(text).forEach { match ->
                if (match.range.first > cursor) {
                    append(text.substring(cursor, match.range.first))
                }
                val raw = match.value
                when {
                    match.groups[1] != null -> {
                        // URL
                        val url = raw.trimEnd('.', ',', ';', ':', '!', '?')
                        pushLink(LinkAnnotation.Url(url, TextLinkStyles(SpanStyle(color = palette.accent))))
                        append(url)
                        pop()
                    }
                    match.groups[2] != null -> {
                        // @ Mention
                        pushStringAnnotation("mention", raw.removePrefix("@"))
                        withStyle(SpanStyle(color = palette.accent, fontWeight = FontWeight.SemiBold)) {
                            append(raw)
                        }
                        pop()
                    }
                    match.groups[3] != null -> {
                        // Inline Code `code`
                        val code = raw.removeSurrounding("`")
                        withStyle(SpanStyle(
                            fontFamily = FontFamily.Monospace,
                            background = palette.ink.copy(alpha = 0.08f),
                            color = palette.accent,
                            fontSize = codeSize
                        )) {
                            append(" $code ")
                        }
                    }
                    match.groups[4] != null -> {
                        // Emoji [key]
                        val key = raw.removePrefix("[").removeSuffix("]")
                        val symbol = LarkEmojiHelper.symbol(key)
                        append(if (symbol.startsWith("[")) raw else symbol)
                    }
                }
                cursor = match.range.last + 1
            }
            if (cursor < text.length) {
                append(text.substring(cursor))
            }
        }
    }

    Text(
        annotated,
        modifier = modifier,
        style = PersonaTheme.type.body,
        color = palette.ink
    )
}

/** Renders a tree of the Lark gateway's rich blocks: headings, lists, quotes, code, paragraphs. */
@Composable
fun LarkRichBlockView(
    nodes: List<LarkRichNode>,
    source: LarkContentSource? = null,
    onOpenProfile: ((String) -> Unit)? = null,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    Column(modifier, verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        for (node in nodes) {
            when (node) {
                is LarkRichNode.Leaf -> {
                    val p = node.part
                    if (p.kind == "code") {
                        LarkCodeBlock(p.text, p.metadata.text("languageName").ifBlank { p.metadata.text("language") })
                    } else {
                        LarkRichInlineText(p.text, source = source, onOpenProfile = onOpenProfile)
                    }
                }
                is LarkRichNode.Block -> {
                    val b = node.block
                    when {
                        b.type.startsWith("heading-") -> {
                            val level = b.type.removePrefix("heading-").toIntOrNull() ?: 1
                            val style = when (level) {
                                1 -> PersonaTheme.type.headline
                                2 -> PersonaTheme.type.title
                                else -> PersonaTheme.type.callout
                            }
                            Column(Modifier.padding(vertical = Tokens.Space.xxs)) {
                                LarkRichBlockView(node.children, source, onOpenProfile)
                            }
                        }
                        b.type == "ordered-list" || b.type == "unordered-list" -> {
                            Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                                node.children.forEachIndexed { i, child ->
                                    Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                                        Text(
                                            LarkRichNode.marker(b, i),
                                            style = PersonaTheme.type.caption,
                                            color = palette.ink3,
                                            modifier = Modifier.widthIn(min = RichTextTokens.listMarkerMinWidth),
                                            textAlign = TextAlign.End
                                        )
                                        Box(Modifier.weight(1f)) {
                                            LarkRichBlockView(listOf(child), source, onOpenProfile)
                                        }
                                    }
                                }
                            }
                        }
                        b.type == "quote" -> {
                            Row(
                                Modifier.height(IntrinsicSize.Min),
                                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)
                            ) {
                                Box(
                                    Modifier
                                        .width(RichTextTokens.quoteBar)
                                        .fillMaxHeight()
                                        .clip(RoundedCornerShape(Tokens.Radius.badge))
                                        .background(palette.accent.copy(alpha = 0.5f))
                                )
                                Box(Modifier.weight(1f)) {
                                    LarkRichBlockView(node.children, source, onOpenProfile)
                                }
                            }
                        }
                        else -> LarkRichBlockView(node.children, source, onOpenProfile)
                    }
                }
            }
        }
    }
}
