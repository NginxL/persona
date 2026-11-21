package persona.feature.chat

import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.LinkAnnotation
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.style.TextDecoration
import persona.core.designsystem.PersonaTheme
import persona.core.domain.PathRef
import persona.core.domain.PathRefs
import java.net.URLDecoder
import java.net.URLEncoder

/**
 * Paths in messages as links (docs/android/android.md "Chat content", the "File paths" item): those there on the session's machine
 * ([PathPresence]) open in the files pane. A user's plain text gets clickable spans ([messageLinkAnnotations]); a
 * reply's Markdown gets `[path](persona-path:…)` links ([markdown]), which [handler] opens instead of the page's
 * link host. A path with a line (`src/a.kt:12`, `a.kt#L12`) opens the file at that line (iOS `PathLinkURL`).
 */
internal data class PathLinkEdit(val range: IntRange, val replacement: String)

internal object PathLinks {
    const val SCHEME = "persona-path:"

    /** The link for [path], with its [line] as `#L12` after it (the encoded path has no `#` of its own). */
    fun uri(path: String, line: Int? = null): String =
        SCHEME + URLEncoder.encode(path, "UTF-8").replace("+", "%20") + (line?.takeIf { it > 0 }?.let { "#L$it" }.orEmpty())

    fun pathOf(uri: String): String? =
        if (uri.startsWith(SCHEME)) runCatching { URLDecoder.decode(uri.removePrefix(SCHEME).substringBefore('#'), "UTF-8") }.getOrNull() else null

    /** The line a `persona-path:` link opens at, or null. */
    fun lineOf(uri: String): Int? =
        if (uri.startsWith(SCHEME)) uri.substringAfter('#', "").removePrefix("L").toIntOrNull()?.takeIf { it > 0 } else null

    /** The paths to ask the machine about for a message's text (as written: Markdown's code spans are its backticks). */
    fun asked(text: String): List<String> = PathRefs.unique(PathRefs.find(text))

    // Where a path is not to become a link: code blocks (fenced, also unclosed while streaming; indented after a blank
    // line), Markdown links and images, <autolinks> and HTML.
    private val blocked = Regex(
        "```[\\s\\S]*?(?:```|$)|~~~[\\s\\S]*?(?:~~~|$)|(?:(?<=\\n\\n)|\\A)(?:(?: {4}|\\t)[^\\n]*(?:\\n|$))+|!?\\[[^\\]\\n]*]\\([^\\n]*?\\)|<[^>\\n]*>",
    )
    private val codeSpan = Regex("`([^`\\n]+)`")
    private val special = Regex("[\\\\`*_\\[\\]<>]")

    /** A reply's Markdown with the paths in [linkable] (as asked: [asked]) written as links. */
    fun markdown(text: String, linkable: Set<String>): String = apply(text, edits(text, linkable))

    /** Source replacements made by [markdown], so incremental callers can retain settled edits. */
    fun edits(text: String, linkable: Set<String>): List<PathLinkEdit> {
        if (linkable.isEmpty()) return emptyList()
        val closed = blocked.findAll(text).map { it.range }.toMutableList()
        closed += ChatLinks.matches(text).map { it.start until it.end }
        val edits = ArrayList<PathLinkEdit>()
        for (m in codeSpan.findAll(text)) {
            if (closed.any { m.range.first in it }) continue
            val ref = PathRefs.codePath(m.groupValues[1])
            if (ref != null && ref.path in linkable) edits += PathLinkEdit(m.range, "[${m.value}](${uri(ref.path, ref.line)})")
            closed += m.range
        }
        for (ref in PathRefs.findAscii(text)) {
            val range = ref.start until ref.end
            if (ref.path !in linkable || closed.any { it.first <= range.last && range.first <= it.last }) continue
            edits += PathLinkEdit(range, "[${special.replace(text.substring(ref.start, ref.end)) { "\\" + it.value }}](${uri(ref.path, ref.line)})")
        }
        return edits.sortedBy { it.range.first }
    }

    /** Apply [edits] to the same source they were computed from. */
    fun apply(text: String, edits: List<PathLinkEdit>): String {
        if (edits.isEmpty()) return text
        return buildString {
            var at = 0
            for ((range, link) in edits) {
                if (range.first < at) continue
                append(text, at, range.first); append(link); at = range.last + 1
            }
            append(text, at, text.length)
        }
    }

    /**
     * Opens `persona-path:` links and local Markdown file links (`[label](/path/to/file.md)`, `./notes.md#L12`,
     * `C:/Users/me/doc.md`) with [open]; web, app (`persona:`), and non-local schemes go on to [outer].
     */
    fun handler(outer: UriHandler, open: (String, Int?) -> Unit): UriHandler = object : UriHandler {
        override fun openUri(uri: String) {
            val path = pathOf(uri)
            if (path != null) {
                open(path, lineOf(uri))
                return
            }
            val local = LocalMarkdownPath.parseLocalPath(uri)
            if (local != null) {
                open(local.path, local.line)
                return
            }
            outer.openUri(uri)
        }
    }

    /** The refs of [refs] to link in plain text: there on the machine, not inside a web link, none overlapping. */
    fun plainLinks(text: String, refs: List<PathRef>, linkable: (String) -> Boolean): List<PathRef> {
        val urls = ChatLinks.matches(text)
        return PathRefs.split(refs.filter { r -> urls.none { r.start < it.end && it.start < r.end } }, linkable)
    }
}

/**
 * A message's plain text with its web links and the paths there on the session's machine linked (a user's bubble).
 * Without a way to ask or open (no files pane), only web links, as [chatLinkAnnotations].
 */
@Composable
internal fun messageLinkAnnotations(text: String): AnnotatedString {
    val actions = LocalChatRowActions.current
    val presence = actions?.paths
    val open = actions?.openPath
    if (presence == null || open == null) return chatLinkAnnotations(text)
    val refs = remember(text) { PathRefs.find(text) }
    val asked = remember(refs) { PathRefs.unique(refs) }
    LaunchedEffect(presence, asked) { presence.ask(asked) }
    val linked = PathLinks.plainLinks(text, refs, presence::linkable) // reads the answers: redraws when they come
    val color = PersonaTheme.palette.accent
    val urls = remember(text) { ChatLinks.matches(text) }
    return remember(text, color, linked, open) {
        val style = TextLinkStyles(SpanStyle(color = color, textDecoration = TextDecoration.Underline))
        buildAnnotatedString {
            append(text)
            urls.forEach { addLink(LinkAnnotation.Url(it.url, style), it.start, it.end) }
            linked.forEach { ref -> addLink(LinkAnnotation.Clickable(ref.path, style) { open(ref.path, ref.line) }, ref.start, ref.end) }
        }
    }
}
