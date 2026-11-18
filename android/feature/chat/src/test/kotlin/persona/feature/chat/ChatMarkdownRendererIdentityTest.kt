package persona.feature.chat

import com.mikepenz.markdown.model.parseMarkdown
import kotlinx.coroutines.runBlocking
import org.intellij.markdown.MarkdownElementTypes
import org.intellij.markdown.ast.ASTNode
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.concurrent.atomic.AtomicInteger
import com.mikepenz.markdown.model.State as MarkdownResult

/** AC2: streaming work retains the renderer identity of settled blocks, not merely an equivalent parse tree. */
class ChatMarkdownRendererIdentityTest {
    @Test fun settledBlocksKeepRendererIdentityUntilWholeSettlement() = runBlocking {
        val wholeParses = AtomicInteger()
        val cache = ChatMarkdownCache({ source ->
            wholeParses.incrementAndGet()
            parseMarkdown(source)
        })
        val id = "reply/identity"
        val settledSource = "A **settled** paragraph.\n\n"
        val initialSource = settledSource + "The tail"
        val appendedSource = initialSource + " keeps growing"

        val initial = cache.parseRenderState(id, initialSource, streaming = true)
        assertTrue(initial.whole)
        assertNotNull(initial.stable)
        val initialStable = initial.stable!!
        assertEquals(settledSource, initialStable.content)
        val settled = initial.result.paragraphStartingAt(0)
        val initialTail = initial.result.paragraphStartingAt(settledSource.length)
        assertEquals(1, wholeParses.get())

        val appended = cache.parseRenderState(id, appendedSource, streaming = true)
        assertFalse(appended.whole)
        assertEquals(1, wholeParses.get())
        assertEquals(appendedSource, appended.result.successContent())
        assertSame(settled, appended.result.paragraphStartingAt(0))
        assertNotSame(initialTail, appended.result.paragraphStartingAt(settledSource.length))

        // Markdown's streaming renderer can skip this prefix only when both inputs retain identity.
        assertNotNull(appended.stable)
        val appendedStable = appended.stable!!
        assertSame(initialStable.content, appendedStable.content)
        assertSame(initialStable.state, appendedStable.state)

        // Ending the reply retains the existing whole-parse behavior and cannot leak the streaming split.
        val final = cache.parseRenderState(id, appendedSource, streaming = false)
        assertTrue(final.whole)
        assertEquals(2, wholeParses.get())
        assertEquals(appendedSource, final.result.successContent())
        assertNotSame(appended.result, final.result)
        assertSame(final.result, cache.cached(id, appendedSource))
        assertNull(final.stable)

        // An edit before the boundary is a whole-parse fallback, not another reusable stable split.
        val fallbackId = "reply/fallback"
        cache.parseRenderState(fallbackId, initialSource, streaming = true)
        val beforeEdit = cache.parseRenderState(fallbackId, appendedSource, streaming = true)
        assertNotNull(beforeEdit.stable)
        val beforeEditStable = beforeEdit.stable!!
        val beforeEditSettled = beforeEdit.result.paragraphStartingAt(0)
        val parsesBeforeEdit = wholeParses.get()
        val editedSource = "An **edited** paragraph.\n\nThe tail keeps growing"

        val fallback = cache.parseRenderState(fallbackId, editedSource, streaming = true)
        assertTrue(fallback.whole)
        assertEquals(parsesBeforeEdit + 1, wholeParses.get())
        assertEquals(editedSource, fallback.result.successContent())
        assertNotSame(beforeEditSettled, fallback.result.paragraphStartingAt(0))
        assertNotNull(fallback.stable)
        assertNotSame(beforeEditStable.content, fallback.stable!!.content)
        assertNotSame(beforeEditStable.state, fallback.stable!!.state)
    }

    private fun MarkdownResult.successContent(): String {
        assertTrue(this is MarkdownResult.Success)
        return (this as MarkdownResult.Success).content
    }

    private fun MarkdownResult.paragraphStartingAt(offset: Int): ASTNode {
        assertTrue(this is MarkdownResult.Success)
        return (this as MarkdownResult.Success).node.children.single {
            it.type == MarkdownElementTypes.PARAGRAPH && it.startOffset == offset
        }
    }
}
