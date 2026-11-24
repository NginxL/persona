package persona.core.contracts.api

/**
 * A space's SPACE.md as the editor shows it (#13500, the web's `web/src/lib/space-md.ts`): the parts the user writes,
 * which the editor lets them change, and the blocks Persona keeps up to date (`<!-- persona:managed:<name>:begin -->` …
 * `:end -->`), shown read-only. The hub refuses a write that changes a managed block or its markers (400), so the
 * editor never lets one change: it edits the human parts and puts the blocks back byte for byte.
 */
sealed interface SpaceMdPart {
    /** The part's exact text; joined in order, the parts give the file back unchanged. */
    val text: String

    data class Human(override val text: String) : SpaceMdPart

    /** [text] is the whole block, markers included; [body] what is between them. */
    data class Managed(val name: String, override val text: String, val body: String) : SpaceMdPart
}

/** The parts the editor shows as fields ([editable], indexes into [parts]). */
data class SpaceMdFields(val parts: List<SpaceMdPart>, val editable: List<Int>)

object SpaceMdText {
    private val BEGIN = Regex("<!-- persona:managed:([a-z0-9_-]+):begin -->")
    private val LEADING_TITLE = Regex("^#[ \\t]+(.+?)[ \\t]*#*[ \\t]*(?:\\n|$)")

    /**
     * Splits the file into human parts and managed blocks, in order. A block whose end marker is missing runs to the end of
     * the file, so nothing of it becomes editable.
     */
    fun split(content: String): List<SpaceMdPart> {
        val parts = mutableListOf<SpaceMdPart>()
        var at = 0
        var match = BEGIN.find(content, 0)
        while (match != null) {
            val name = match.groupValues[1]
            val endMarker = "<!-- persona:managed:$name:end -->"
            val bodyStart = match.range.last + 1
            val endAt = content.indexOf(endMarker, bodyStart)
            val stop = if (endAt < 0) content.length else endAt + endMarker.length
            if (match.range.first > at) parts += SpaceMdPart.Human(content.substring(at, match.range.first))
            val body = content.substring(bodyStart, if (endAt < 0) content.length else endAt)
            parts += SpaceMdPart.Managed(name, content.substring(match.range.first, stop), body)
            at = stop
            match = if (stop >= content.length) null else BEGIN.find(content, stop)
        }
        if (at < content.length) parts += SpaceMdPart.Human(content.substring(at))
        return parts
    }

    fun join(parts: List<SpaceMdPart>): String = parts.joinToString("") { it.text }

    /**
     * The fields: every human part with words in it; when there is none, one empty part at the start (added to the parts
     * when missing), so the user always has somewhere to write. Whitespace-only parts between blocks stay unshown.
     */
    fun editable(parts: List<SpaceMdPart>): SpaceMdFields {
        val editable = parts.indices.filter { val p = parts[it]; p is SpaceMdPart.Human && p.text.isNotBlank() }
        if (editable.isNotEmpty()) return SpaceMdFields(parts, editable)
        if (parts.firstOrNull() is SpaceMdPart.Human) return SpaceMdFields(parts, listOf(0))
        return SpaceMdFields(listOf(SpaceMdPart.Human("")) + parts, listOf(0))
    }

    /**
     * The file from [parts] with the fields' words ([edits], by part index) in place of their human parts. A field written
     * right before a block keeps a line break between them, so the block's begin marker stays on a line of its own.
     */
    fun assemble(parts: List<SpaceMdPart>, edits: Map<Int, String>): String =
        parts.mapIndexed { i, p ->
            val text = edits[i]
            if (p !is SpaceMdPart.Human || text == null) p.text
            else if (parts.getOrNull(i + 1) is SpaceMdPart.Managed && text.isNotEmpty() && !text.endsWith("\n")) "$text\n\n"
            else text
        }.joinToString("")

    /** Whether every managed block of [after] is byte for byte one of [before]'s, in the same order (what the hub checks). */
    fun sameManagedBlocks(before: String, after: String): Boolean {
        fun blocks(s: String) = split(s).filterIsInstance<SpaceMdPart.Managed>().map { it.text }
        return blocks(before) == blocks(after)
    }

    /** SPACE.md without the blocks Persona keeps: what the user wrote, as the space's home shows it. */
    fun human(content: String): String = split(content)
        .filterIsInstance<SpaceMdPart.Human>()
        .map { it.text.trim() }
        .filter { it.isNotEmpty() }
        .joinToString("\n\n")

    /** The user's part of SPACE.md without a leading `# <title>` that only repeats the space's [title] shown above it. */
    fun body(content: String, title: String): String {
        val text = human(content)
        val match = LEADING_TITLE.find(text) ?: return text
        if (match.groupValues[1].trim().lowercase() != title.trim().lowercase()) return text
        return text.substring(match.range.last + 1).trimStart()
    }
}
