package persona.feature.chat

import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.ast.ASTNode
import org.intellij.markdown.ast.findChildOfType
import org.intellij.markdown.flavours.gfm.GFMFlavourDescriptor
import org.intellij.markdown.parser.MarkdownParser
import org.intellij.markdown.parser.CancellationToken
import persona.core.contracts.api.ReplyImageSource
import java.net.URI

internal data class ReplyPicture(
    val url: String,
    val alt: String,
    val host: String,
    val localPath: String? = null,
) {
    val isLocal: Boolean get() = localPath != null
}

/** Replaces only parsed web and (when enabled) local-path images. Fences, code spans, linked pictures and quoted text retain their Markdown. */
internal class ReplyImageDocument private constructor(
    val original: String,
    val markdown: String,
    val pictures: List<ReplyPicture>,
    private val marker: String,
    private val groups: List<IntRange>,
) {
    fun group(fence: String): IntRange? {
        val lines = fence.lineSequence().map { it.trim() }.iterator()
        if (!lines.hasNext() || lines.next() != "~~~$marker" || !lines.hasNext()) return null
        return lines.next().toIntOrNull()?.let(groups::getOrNull)
    }

    fun inline(link: String): Int? = link.removePrefix("$marker:").takeIf { it != link }
        ?.toIntOrNull()?.takeIf { it in pictures.indices }

    private data class Match(val start: Int, val end: Int, val picture: ReplyPicture)

    companion object {
        fun parse(text: String, allowLocal: Boolean = false): ReplyImageDocument? {
            if (!text.contains("![")) return null
            val root = MarkdownParser(GFMFlavourDescriptor(), cancellationToken = CancellationToken.NonCancellable)
                .buildMarkdownTreeFromString(text, 0)
            val matches = mutableListOf<Match>()
            fun collect(node: ASTNode) {
                when (node.type) {
                    MarkdownElementTypes.IMAGE -> {
                        val link = node.findChildOfType(MarkdownElementTypes.INLINE_LINK) ?: return
                        val destination = link.findChildOfType(MarkdownElementTypes.LINK_DESTINATION) ?: return
                        val rawDest = text.substring(destination.startOffset, destination.endOffset).removeSurrounding("<", ">")
                        val alt = link.findChildOfType(MarkdownElementTypes.LINK_TEXT)?.let {
                            text.substring(it.startOffset, it.endOffset).removeSurrounding("[", "]")
                        }.orEmpty()
                        val web = ChatLinks.webUrl(rawDest)
                        if (web != null) {
                            matches.add(Match(node.startOffset, node.endOffset, ReplyPicture(web, alt, URI(web).host)))
                        } else if (allowLocal) {
                            val local = LocalMarkdownPath.parseLocalPath(rawDest) ?: return
                            matches.add(Match(node.startOffset, node.endOffset, ReplyPicture(local.path, alt, "", localPath = local.path)))
                        }
                    }
                    MarkdownElementTypes.INLINE_LINK, MarkdownElementTypes.BLOCK_QUOTE, MarkdownElementTypes.CODE_FENCE,
                    MarkdownElementTypes.CODE_BLOCK, MarkdownElementTypes.CODE_SPAN -> Unit
                    else -> node.children.forEach(::collect)
                }
            }
            collect(root)
            if (matches.isEmpty()) return null
            // Generated markers never collide with examples or links the author supplied.
            var marker = "persona-reply-images"
            while (text.contains(marker)) marker += "x"
            val groups = mutableListOf<IntRange>()
            val out = StringBuilder(text.length)
            var cursor = 0
            var index = 0
            while (index < matches.size) {
                val found = matches[index]
                val lineStart = text.lastIndexOf('\n', found.start - 1) + 1
                val lineEnd = text.indexOf('\n', found.end).takeIf { it >= 0 } ?: text.length
                // All pictures of the same kind on an otherwise empty line form one block (including a line within a paragraph).
                var last = index
                while (last + 1 < matches.size && matches[last + 1].start < lineEnd &&
                    matches[last + 1].picture.isLocal == found.picture.isLocal &&
                    text.substring(matches[last].end, matches[last + 1].start).isBlank()) last++
                val alone = text.substring(lineStart, found.start).isBlank() && text.substring(matches[last].end, lineEnd).isBlank()
                if (!alone) {
                    if (found.picture.isLocal) {
                        // Lift local inline images into their own block while preserving surrounding text,
                        // so failures surface the complete reason and manual Retry (aligned with iOS PR #12102).
                        val before = text.substring(cursor, found.start).trimEnd(' ', '\t')
                        out.append(before)
                        if (out.isNotEmpty() && out.last() != '\n') out.append('\n')
                        out.append("\n~~~").append(marker).append('\n')
                            .append(groups.size).append("\n~~~\n\n")
                        groups.add(index..index)
                        var nextCursor = found.end
                        while (nextCursor < text.length && (text[nextCursor] == ' ' || text[nextCursor] == '\t')) nextCursor++
                        cursor = nextCursor
                    } else {
                        out.append(text, cursor, found.start)
                        out.append("![").append(found.picture.alt).append("](").append(marker).append(':').append(index).append(')')
                        cursor = found.end
                    }
                    index++
                    continue
                }
                var end = lineEnd
                while (last + 1 < matches.size) {
                    val next = matches[last + 1]
                    if (next.picture.isLocal != found.picture.isLocal) break
                    if (!text.substring(end, next.start).isBlank()) break
                    val nextEnd = text.indexOf('\n', next.end).takeIf { it >= 0 } ?: text.length
                    var nextLast = last + 1
                    while (nextLast + 1 < matches.size && matches[nextLast + 1].start < nextEnd &&
                        matches[nextLast + 1].picture.isLocal == found.picture.isLocal &&
                        text.substring(matches[nextLast].end, matches[nextLast + 1].start).isBlank()) nextLast++
                    if (!text.substring(matches[nextLast].end, nextEnd).isBlank()) break
                    last = nextLast
                    end = nextEnd
                }
                out.append(text, cursor, lineStart).append("~~~").append(marker).append('\n')
                    .append(groups.size).append("\n~~~")
                groups.add(index..last)
                cursor = end
                index = last + 1
            }
            out.append(text, cursor, text.length)
            return ReplyImageDocument(text, out.toString(), matches.map(Match::picture), marker, groups)
        }
    }
}

internal object ReplyImageLayout {
    const val viewer = ReplyImageSource.MAX_SIZE
    const val height = 240f
    const val maximumWidth = 480f
    const val gap = 4f
    const val mostShown = 6

    fun request(points: Float, density: Float) = ReplyImageSource.sizeFor(points * density.coerceAtLeast(1f))

    fun single(width: Int?, pixelsHigh: Int?, column: Float): Pair<Float, Float> {
        if (width == null || pixelsHigh == null) return minOf(column, height * 4 / 3) to height
        if (width <= column && pixelsHigh <= height) return width.toFloat() to pixelsHigh.toFloat()
        return minOf(column, maxOf(height * 3 / 4, height * width / pixelsHigh)) to height
    }

    fun columns(count: Int) = if (count == 2 || count == 4) 2 else 3
    fun square(count: Int, width: Float): Float {
        val across = if (count == 2) 2 else 3
        return ((minOf(width, maximumWidth) - gap * (across - 1)) / across).coerceAtLeast(1f)
    }
}
