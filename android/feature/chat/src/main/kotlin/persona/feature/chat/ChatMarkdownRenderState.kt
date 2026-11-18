package persona.feature.chat

import com.mikepenz.markdown.model.ReferenceLinkHandler
import com.mikepenz.markdown.model.ReferenceLinkHandlerImpl
import com.mikepenz.markdown.model.State as MarkdownResult
import com.mikepenz.markdown.model.StreamingMarkdownState
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow

/** A parse and the renderer inputs whose identity can be reused by the next tail parse. */
internal data class ChatMarkdownRenderState(
    val result: MarkdownResult,
    val whole: Boolean,
    val stable: StableChatMarkdownRender?,
)

/**
 * [content] is the settled source prefix. [state] retains identity while [ChatMarkdownStream] advances its tail;
 * the composable applies each new result to it on the UI thread with [update].
 */
internal data class StableChatMarkdownRender(
    val content: String,
    val boundary: Int,
    val stableCount: Int,
    val state: ParsedStreamingMarkdownState,
)

/** The cache's settled split adapted to mikepenz's stable-AST-plus-tail renderer. */
internal class ParsedStreamingMarkdownState : StreamingMarkdownState {
    private val source = StringBuilder()
    private val snapshots = MutableStateFlow(StreamingMarkdownState.Snapshot(emptyList(), emptyList()))
    private val linkValues = MutableStateFlow<Map<String, String>>(emptyMap())
    private var handler: ReferenceLinkHandler = ReferenceLinkHandlerImpl()

    override val content: CharSequence get() = source
    override val snapshot: StateFlow<StreamingMarkdownState.Snapshot> = snapshots
    override val links: StateFlow<Map<String, String>> = linkValues
    override val referenceLinkHandler: ReferenceLinkHandler get() = handler

    override suspend fun append(text: String): StreamingMarkdownState.Snapshot =
        throw UnsupportedOperationException("The Markdown cache supplies complete parse results")

    fun update(result: MarkdownResult, stableCount: Int) {
        val success = result as? MarkdownResult.Success ?: return
        source.setLength(0)
        source.append(success.content)
        handler = success.referenceLinkHandler
        snapshots.value = StreamingMarkdownState.Snapshot(
            success.node.children.take(stableCount),
            success.node.children.drop(stableCount),
        )
    }
}
