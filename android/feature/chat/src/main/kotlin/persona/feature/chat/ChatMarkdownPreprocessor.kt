package persona.feature.chat

import java.util.IdentityHashMap

/**
 * Applies the existing math/URL preprocessing to safe settled blocks and the growing tail. Constructs whose protected
 * spans can cross a block boundary keep the current whole-text behavior. [onScan] reports complete text and the suffix
 * start actually used for that rewrite (zero for a whole fallback).
 */
internal class ChatMarkdownPreprocessor(
    private val onScan: (text: String, from: Int) -> Unit = { _, _ -> },
) {
    private class Analysis(val safe: Boolean, val rewritten: String)

    private val stream = AssistantAncillaryStream { _, _ -> }
    private val analyses = IdentityHashMap<AssistantRenderedBlock, Analysis>()
    private var previous: String? = null

    @Synchronized fun rewrite(text: String): String {
        val append = previous?.let(text::startsWith) == true
        val snapshot = stream.update(text, linkable = emptySet())
        val blocks = snapshot.blocks
        analyses.keys.retainAll(blocks)
        val current = blocks.map { block ->
            analyses.getOrPut(block) {
                val safe = !block.text.contains("$$") && !block.text.contains("\\[") &&
                    '`' !in block.text && '[' !in block.text && '<' !in block.text
                Analysis(safe, if (safe) chatMarkdown(block.text) else "")
            }
        }
        val safe = current.all(Analysis::safe)
        previous = text
        if (!safe) {
            onScan(text, 0)
            return chatMarkdown(text)
        }
        onScan(text, if (append) blocks.lastOrNull()?.start ?: 0 else 0)
        return blocks.joinToString("\n\n") { block -> analyses.getValue(block).rewritten }
    }
}
