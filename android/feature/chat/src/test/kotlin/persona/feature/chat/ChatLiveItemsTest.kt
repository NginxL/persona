package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import java.time.Instant
import java.util.concurrent.Callable
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.runBlocking
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Item

/**
 * AC1/AC4 contract for the page's live-items view. The production API must keep the structural
 * base stable for text-only revisions while returning complete, current items to on-demand readers.
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatLiveItemsTest {
    private val res = ApplicationProvider.getApplicationContext<Context>().resources

    @Test
    fun textRevisionsAreRowLocalAndOnDemandReadersSeeTheCompleteLatestText() = runBlocking {
        val question = Item("question", seq = 1, role = "user", kind = "text", text = "Question")
        val reply = Item(
            "reply", seq = 2, ts = "2026-10-30T09:59:00Z", role = "assistant", kind = "text",
            text = "OldNeedle https://example.com/old",
        )
        val base = listOf(question, reply)
        val live = ChatLiveItems(base, streamingItemIds = setOf("reply"))
        val initialGeneration = live.generation
        val initialReply = live.item("reply")
        val frozenSearch = live.currentItems()

        val latest = "LatestNeedle https://example.com/latest"
        live.updateText("reply", ChatTextUpdate(latest, streaming = true))

        // A text revision redraws only its row: neither the sibling nor the projection base changes.
        assertEquals(initialGeneration, live.generation)
        assertSame(base, live.structuralItems)
        assertSame(reply, live.structuralItems.item("reply"))
        assertSame(question, live.item("question"))
        assertNotSame(initialReply, live.item("reply"))
        assertEquals(latest, live.item("reply")?.text)

        val current = live.currentItems()
        assertEquals(listOf("question", "reply"), current.map(Item::id))
        assertSame(question, current.item("question"))
        assertEquals(latest, current.item("reply").text)

        // A search already in progress owns its frozen list; a new search reads current text.
        assertEquals(listOf("reply"), searchChat(res, frozenSearch, "OldNeedle").hits.map { it.itemId })
        assertTrue(searchChat(res, frozenSearch, "LatestNeedle").hits.isEmpty())
        assertTrue(searchChat(res, current, "OldNeedle").hits.isEmpty())
        assertEquals(listOf("reply"), searchChat(res, current, "LatestNeedle").hits.map { it.itemId })
        assertEquals(
            listOf("https://example.com/latest"),
            ChatLinks.toPreload(current, Instant.parse("2026-10-30T10:00:00Z")),
        )

        // Producers may be conflated between observers; the latest whole value loses no earlier chunk.
        live.updateText("reply", ChatTextUpdate("first", streaming = true))
        live.updateText("reply", ChatTextUpdate("first second", streaming = true))
        live.updateText("reply", ChatTextUpdate("first second third", streaming = true))
        assertEquals("first second third", live.currentItems().item("reply").text)
        assertSame(base, live.structuralItems)
        assertSame(question, live.currentItems().item("question"))

        // An explicit host update wins over the synthetic streaming holder without replacing it globally.
        val explicit = ChatTextUpdate("explicit wins", streaming = false)
        val explicitUpdates = mapOf("reply" to explicit)
        assertSame(explicit, live.rowTextUpdates(explicitUpdates).getValue("reply"))
        assertEquals("explicit wins", live.currentItems(explicitUpdates).item("reply").text)
        assertEquals("first second third", live.currentItems().item("reply").text)

        // A newer structural generation is visible and settling the turn removes only its stream holder.
        val settledBase = listOf(
            question,
            reply.copy(text = "first second third"),
            Item("settled", seq = 3, role = "assistant", kind = "text", text = "Turn settled"),
        )
        live.reset(settledBase, streamingItemIds = emptySet())
        assertTrue(live.generation > initialGeneration)
        assertNotSame(base, live.structuralItems)
        assertSame(settledBase, live.structuralItems)
        assertSame(question, live.item("question"))
        assertFalse(live.rowTextUpdates().containsKey("reply"))
        assertEquals(
            listOf("question", "reply", "settled"),
            live.currentItems().map(Item::id),
        )
        assertEquals("first second third", live.currentItems().item("reply").text)
    }

    @Test
    fun concurrentFirstReadersSeeOneCompleteOverlay() {
        val live = ChatLiveItems(listOf(Item("reply", seq = 1, role = "assistant", kind = "text", text = "Old")))
        live.updateText("reply", ChatTextUpdate("Latest", streaming = true))
        val revision = live.revisions.value
        val executor = Executors.newFixedThreadPool(8)
        try {
            val readers = (1..32).map {
                Callable {
                    val items = revision.items
                    assertEquals("Latest", items.single().text)
                    items
                }
            }
            // Each thread materializes the same revision for the first time on a real JVM thread.
            val results = executor.invokeAll(readers).map { it.get(10, TimeUnit.SECONDS) }
            assertEquals(1, results.toSet().size)
        } finally {
            executor.shutdownNow()
            assertTrue(executor.awaitTermination(10, TimeUnit.SECONDS))
        }
    }

    private fun List<Item>.item(id: String) = single { it.id == id }
}
