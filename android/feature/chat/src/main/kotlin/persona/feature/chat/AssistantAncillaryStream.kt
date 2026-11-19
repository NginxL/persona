package persona.feature.chat

import persona.core.domain.FilePathRef
import persona.core.domain.FilePaths
import persona.core.domain.PathRef
import persona.core.domain.PathRefs
import persona.core.domain.ports.PortLink
import persona.core.domain.ports.PortLinkMatch
import persona.core.domain.ports.PortLinks
import persona.core.domain.views.AssistantFence
import persona.core.domain.views.AssistantFences

/** One settled or unfinished source block and the positioned results computed for it. */
internal data class AssistantRenderedBlock(
    val text: String,
    val start: Int,
    internal val files: List<FilePathRef> = emptyList(),
    internal val refs: List<PathRef> = emptyList(),
    internal val edits: List<PathLinkEdit> = emptyList(),
    internal val cards: List<AssistantFence> = emptyList(),
    internal val ports: List<PortLinkMatch> = emptyList(),
)

internal data class AssistantAncillarySnapshot(
    val model: MessageModel,
    val blocks: List<AssistantRenderedBlock>,
    val paths: List<String>,
    val linkedMarkdown: String,
    val cards: List<AssistantFence>,
    val ports: List<PortLink>,
)

/**
 * Ancillary reply work over settled blocks and an unfinished tail. A first/reset update scans whole text once and
 * partitions its positioned results; an append scans only the old tail plus the new suffix. Structured messages whose
 * tags can change earlier meaning keep the existing whole-message behavior.
 */
internal class AssistantAncillaryStream(
    private val onScan: (text: String, from: Int) -> Unit,
) {
    private var text: String? = null
    private var snapshot: AssistantAncillarySnapshot? = null
    private var settled = emptyList<AssistantRenderedBlock>()
    private var tailStart = 0
    private var structured = false
    private var hostnames = emptyList<String>()
    private var linkable: Set<String>? = null

    @Synchronized
    fun update(
        text: String,
        finished: Boolean = false,
        hostnames: List<String> = emptyList(),
        linkable: Set<String>? = null,
    ): AssistantAncillarySnapshot {
        val previous = this.text
        val options = this.hostnames == hostnames && this.linkable == linkable
        snapshot?.takeIf { text == previous && !finished && options }?.let { return it }
        val append = !finished && previous != null && options && !structured && text.startsWith(previous)
        val from = if (append) tailStart else 0
        val suffix = text.substring(from)
        val nextStructured = (append && structured) || structuredContent(suffix)
        val result = if (nextStructured) scanStructured(text, hostnames, linkable)
        else scanPlain(text, suffix, from, append, hostnames, linkable)
        this.text = text
        this.hostnames = hostnames.toList()
        this.linkable = linkable?.toSet()
        snapshot = result
        structured = nextStructured
        return result
    }

    private fun scanStructured(text: String, hostnames: List<String>, linkable: Set<String>?): AssistantAncillarySnapshot {
        onScan(text, 0)
        settled = emptyList()
        tailStart = 0
        val paths = PathLinks.asked(text)
        return AssistantAncillarySnapshot(
            model = messageModel(text),
            blocks = listOf(AssistantRenderedBlock(text, 0)),
            paths = paths,
            linkedMarkdown = PathLinks.markdown(text, linkable ?: paths.toSet()),
            cards = AssistantFences.blocks(text),
            ports = PortLinks.find(text, hostnames),
        )
    }

    private fun scanPlain(
        wholeText: String,
        suffix: String,
        from: Int,
        append: Boolean,
        hostnames: List<String>,
        linkable: Set<String>?,
    ): AssistantAncillarySnapshot {
        onScan(wholeText, from)
        val files = FilePaths.findDetailed(suffix)
        val refs = PathRefs.find(suffix, Int.MAX_VALUE)
        val paths = if (linkable == null) PathRefs.unique(refs).toSet() else linkable
        val edits = PathLinks.edits(suffix, paths)
        val cards = AssistantFences.blocks(suffix)
        val ports = PortLinks.findMatches(suffix, hostnames)
        val split = splitBlocks(suffix)
        val firstView = if (append) settled.sumOf { block -> block.cards.count(AssistantFence::isView) } else 0

        fun block(segment: Segment): AssistantRenderedBlock {
            val start = from + segment.start
            val end = start + segment.text.length
            return AssistantRenderedBlock(
                text = segment.text,
                start = start,
                files = files.filter { it.start >= segment.start && it.start < segment.start + segment.text.length }
                    .map { it.copy(start = it.start + from, end = it.end + from) },
                refs = refs.filter { it.start >= segment.start && it.start < segment.start + segment.text.length }
                    .map { it.copy(start = it.start + from, end = it.end + from) },
                edits = edits.filter { it.range.first >= segment.start && it.range.first < segment.start + segment.text.length }
                    .map { PathLinkEdit(it.range.shift(from), it.replacement) },
                cards = cards.filter { it.start >= segment.start && it.start < segment.start + segment.text.length }
                    .map { card -> card.copy(index = if (card.isView) firstView + card.index else card.index,
                        start = card.start + from, end = card.end + from) },
                ports = ports.filter { it.start >= segment.start && it.start < segment.start + segment.text.length }
                    .map { it.copy(start = it.start + from, end = it.end + from) },
            ).also { check(end == it.start + it.text.length) }
        }

        val done = split.done.map(::block)
        val allSettled = if (append) settled + done else done
        val tail = block(split.tail)
        settled = allSettled
        tailStart = from + split.tail.start
        return snapshot(wholeText, allSettled + tail)
    }

    private fun snapshot(text: String, blocks: List<AssistantRenderedBlock>): AssistantAncillarySnapshot {
        val shown = text.trim()
        val files = blocks.flatMap(AssistantRenderedBlock::files).sortedBy(FilePathRef::start)
            .distinctBy(FilePathRef::path).take(6).map(FilePathRef::path)
        val refs = blocks.flatMap(AssistantRenderedBlock::refs)
            .sortedWith(compareBy<PathRef> { it.start }.thenByDescending { it.end }).take(40)
        val edits = blocks.flatMap(AssistantRenderedBlock::edits).sortedBy { it.range.first }
        val cards = blocks.flatMap(AssistantRenderedBlock::cards).sortedBy(AssistantFence::start)
        val ports = blocks.flatMap(AssistantRenderedBlock::ports)
            .sortedWith(compareByDescending<PortLinkMatch> { it.web }.thenBy { it.start })
            .distinctBy(PortLinkMatch::link).map(PortLinkMatch::link)
        return AssistantAncillarySnapshot(
            model = MessageModel(
                parts = shown.takeIf(String::isNotBlank)?.let { listOf(MessagePart.Text(it)) } ?: emptyList(),
                images = emptyList(), files = files,
            ),
            blocks = blocks,
            paths = PathRefs.unique(refs),
            linkedMarkdown = PathLinks.apply(text, edits),
            cards = cards,
            ports = ports,
        )
    }

    private data class Segment(val text: String, val start: Int)
    private data class Split(val done: List<Segment>, val tail: Segment)
    private data class Fence(val character: Char, val count: Int)

    private fun splitBlocks(text: String): Split {
        val lines = text.split('\n')
        val starts = IntArray(lines.size)
        for (index in 1 until lines.size) starts[index] = starts[index - 1] + lines[index - 1].length + 1
        val done = mutableListOf<Segment>()
        var start = 0
        var fence: Fence? = null
        for (index in lines.indices) {
            val line = lines[index]
            val trimmed = line.trim()
            fence?.let { open ->
                if (closes(open, trimmed)) fence = null
                continue
            }
            opening(trimmed)?.let { open ->
                fence = open
                continue
            }
            if (trimmed.isNotEmpty() || index <= start || index + 1 >= lines.size) continue
            val next = lines[index + 1]
            if (next.isEmpty() || next.startsWith(' ') || next.startsWith('\t')) continue
            done += Segment(lines.subList(start, index).joinToString("\n"), starts[start])
            start = index + 1
        }
        return Split(done, Segment(lines.subList(start, lines.size).joinToString("\n"), starts[start]))
    }

    private fun opening(line: String): Fence? {
        val character = line.firstOrNull() ?: return null
        if (character != '`' && character != '~') return null
        val count = line.takeWhile { it == character }.length
        if (count < 3) return null
        val suffix = line.substring(count)
        if (character == '`' && '`' in suffix) return null
        return Fence(character, count)
    }

    private fun closes(open: Fence, line: String): Boolean {
        val count = line.takeWhile { it == open.character }.length
        return count >= open.count && line.substring(count).all { it == ' ' || it == '\t' }
    }

    private fun structuredContent(text: String): Boolean = '<' in text || "[Image #" in text ||
        "相关图片：" in text || "（我从手机发来了图片，请用 Read 查看：" in text // i18n-ignore: existing hub note markers

    private fun IntRange.shift(by: Int): IntRange = first + by..last + by
}
