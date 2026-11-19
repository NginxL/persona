package persona.feature.chat

import com.mikepenz.markdown.model.State as MarkdownResult
import com.mikepenz.markdown.model.parseMarkdown
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.CoroutineStart
import kotlinx.coroutines.Deferred
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.async
import persona.core.designsystem.MathMarkdown

/** What a reply's text parses as: its formulas as images (MathMarkdown) and its bare links as autolinks (ChatLinks). */
internal fun chatMarkdown(text: String) = ChatLinks.markdown(MathMarkdown.rewrite(text))

/**
 * The page's parsed Markdown (docs/android/android.md "Reply Markdown parsing"): one entry per id, the text it
 * was parsed from and its parse.
 *
 * What it keeps: every id the page's prewarm last asked to [keep], and the most recently used [OTHERS] ids besides
 * (thinking, plans, recaps, replies older than the prewarm's). So it holds at most the prewarm's set (its 80 replies'
 * text parts) plus [OTHERS]: about 112 entries, which at the ~19 bytes of parse per byte of reply measured on the JVM
 * (ChatMarkdownBench.retainedHeapPerEntry: 38 KB for a 2 KB reply, 294 KB for 16 KB, 1.15 MB for 64 KB) is about 4 MB
 * of 2 KB replies and 33 MB of 16 KB ones. A plain LRU as large as the prewarm's set lost all of it to the first other
 * text: the prewarm, walking its set in order, then evicted each entry just before it came back to it.
 *
 * One parse per id and text: a caller that asks while a parse of the same id and text is under way (the page's prewarm
 * and the row it warms, say) waits for that one. The parse runs in the cache's own [scope], so a caller that leaves (a
 * row scrolled away, a prewarm restarted) does not cancel it for the others or lose its work. Nor does the last caller
 * to leave: the cache keeps no count of who waits, so a parse runs to the end and is kept even when nobody waits for it
 * any more. That costs little, since a streaming reply parses only its tail.
 *
 * A streaming reply parses from its last settled block on ([ChatMarkdownStream]); once it ends, its final text is parsed
 * whole and kept under the same id.
 */
internal class ChatMarkdownCache(
    /** Parses Markdown whole: mikepenz's parseMarkdown, or one that counts in tests. */
    private val parseWhole: (String) -> MarkdownResult = { parseMarkdown(it) },
    private val scope: CoroutineScope = CoroutineScope(SupervisorJob() + Dispatchers.Default),
) {
    /** [result] parsed from [text]; [whole]: by [parseWhole], not from a stream's tail. */
    private class Entry(val text: String, val render: ChatMarkdownRenderState) {
        val result: MarkdownResult get() = render.result
        val whole: Boolean get() = render.whole
    }
    private data class Parse(val id: String, val text: String, val whole: Boolean)

    /** Least recently used first. */
    private val entries = LinkedHashMap<String, Entry>(128, 0.75f, true)
    private var kept: Set<String> = emptySet()
    private val running = HashMap<Parse, Deferred<ChatMarkdownRenderState>>()
    private val streams = ChatMarkdownStream(parseWhole)
    private val preprocessors = object : LinkedHashMap<String, ChatMarkdownPreprocessor>(8, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, ChatMarkdownPreprocessor>) = size > 8
    }

    @Synchronized fun cached(id: String, text: String) = entries[id]?.takeIf { it.text == text }?.result
    @Synchronized fun cachedRenderState(id: String, text: String) = entries[id]?.takeIf { it.text == text }?.render

    /** The ids the page's prewarm parses, kept however many other ids are parsed between its runs. */
    @Synchronized fun keep(ids: Collection<String>) {
        kept = ids.toHashSet()
        trim()
    }

    private fun trim() {
        var others = entries.keys.count { it !in kept }
        val ids = entries.keys.iterator()
        while (others > OTHERS && ids.hasNext()) {
            if (ids.next() !in kept) {
                ids.remove()
                others--
            }
        }
    }

    /**
     * [text]'s Markdown under [id]. [streaming] is what the row knows of the reply (MessageContent): true while it
     * streams, false once it is whole; null from a caller that does not know (the page's prewarm, a plan or a recap),
     * which goes with what the row last said.
     */
    suspend fun parse(id: String, text: String, streaming: Boolean? = null): MarkdownResult =
        parseRenderState(id, text, streaming).result

    /** [text]'s parse and the renderer identity a streaming row can retain for its settled prefix. */
    suspend fun parseRenderState(id: String, text: String, streaming: Boolean? = null): ChatMarkdownRenderState {
        val parse = synchronized(this) {
            when (streaming) {
                true -> streams.start(id)
                false -> {
                    streams.end(id)
                    preprocessors.remove(id)
                }
                null -> {}
            }
            val whole = !streams.streaming(id)
            entries[id]?.takeIf { it.text == text && (it.whole || !whole) }?.let { return it.render }
            val key = Parse(id, text, whole)
            running[key] ?: scope.async(start = CoroutineStart.LAZY) { compute(key) }.also { job ->
                running[key] = job
                job.invokeOnCompletion { synchronized(this) { running.remove(key, job) } }
                job.start()
            }
        }
        return parse.await()
    }

    private fun compute(parse: Parse): ChatMarkdownRenderState {
        val markdown = if (parse.whole) chatMarkdown(parse.text)
            else preprocessors.getOrPut(parse.id) { ChatMarkdownPreprocessor() }.rewrite(parse.text)
        val render = if (parse.whole) {
            ChatMarkdownRenderState(parseWhole(markdown), whole = true, stable = null)
        } else {
            val parsed = streams.parseMetadata(parse.id, markdown)
            val previous = synchronized(this) { entries[parse.id]?.render?.stable }
            val stable = parsed.stable?.let { split ->
                val state = if (parsed.whole || previous == null) ParsedStreamingMarkdownState() else previous.state
                val content = if (!parsed.whole && previous != null && previous.boundary == split.boundary) previous.content
                    else (parsed.result as MarkdownResult.Success).content.substring(0, split.boundary)
                StableChatMarkdownRender(content, split.boundary, split.count, state)
            }
            ChatMarkdownRenderState(parsed.result, parsed.whole, stable)
        }
        synchronized(this) {
            // A delta's tail parse that ends after the stream's whole parse of the same text keeps the whole one, which
            // the next caller would otherwise parse again.
            val entry = entries[parse.id]
            if (render.whole || entry == null || !entry.whole || entry.text != parse.text)
                entries[parse.id] = Entry(parse.text, render)
            trim()
        }
        return render
    }

    companion object {
        const val OTHERS = 32
    }
}

