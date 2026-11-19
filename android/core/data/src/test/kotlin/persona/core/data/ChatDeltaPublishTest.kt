package persona.core.data

import persona.core.contracts.*
import persona.core.contracts.api.*
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.*
import kotlinx.coroutines.test.*
import org.junit.Assert.*
import org.junit.Test

/** Streamed text: how often a reply's chunks publish, and what each publish shows. */
@OptIn(ExperimentalCoroutinesApi::class)
class ChatDeltaPublishTest {
    private val key = SessionKey("machine", "session")
    private fun item(seq: Int) = Item("$seq#0", seq, role = if (seq % 2 == 0) "assistant" else "user", kind = "text",
        text = "history $seq ".repeat(20))

    private class Harness(val connection: FakeConnection, val repository: HubChatRepository, val seen: MutableList<ChatSnapshot>,
        /** While true, the items fetch (a turn end's requestSync) never returns. */
        val fetchHangs: java.util.concurrent.atomic.AtomicBoolean)

    /** A chat with 200 history items, observed by a collector that sees every published state. */
    private fun TestScope.harness(window: Long): Harness {
        val connection = FakeConnection()
        val history = (1..200).map { item(it) }
        val hangs = java.util.concurrent.atomic.AtomicBoolean(false)
        val transport = FakeChatTransport().apply {
            fetch = { if (hangs.get()) awaitCancellation(); ItemsResponse(history, history = "h", toSeq = 200) }
        }
        val repository = HubChatRepository(transport, connection,
            FakeItemsCache(CachedChat(history, "h", 200, false)), FakeSessions(), backgroundScope,
            deltaPublishWindowMillis = window)
        val seen = mutableListOf<ChatSnapshot>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) { repository.observe(key).collect { seen += it } }
        runCurrent()
        seen.clear()
        return Harness(connection, repository, seen, hangs)
    }

    private suspend fun TestScope.delta(h: Harness, text: String) {
        h.connection.events.emit(DeltaEvent(key.machineId, key.sessionId, text))
        runCurrent()
    }

    @Test fun theFirstChunkOfAReplyShowsAtOnce() = runTest {
        val h = harness(window = 50)
        delta(h, "示例")
        assertEquals(1, h.seen.size)
        assertEquals("示例", h.seen.last().items.last().text)
    }

    @Test fun chunksInsideOneWindowPublishOnceAtItsEndWithTheLatestText() = runTest {
        val h = harness(window = 50)
        delta(h, "甲")
        delta(h, "甲乙")
        delta(h, "甲乙丙")
        assertEquals(1, h.seen.size)
        advanceTimeBy(50); runCurrent()
        assertEquals(2, h.seen.size)
        assertEquals("甲乙丙", h.seen.last().items.last().text)
        // The window closed on a quiet period: the next chunk shows at once again.
        advanceTimeBy(50); runCurrent()
        delta(h, "甲乙丙丁")
        assertEquals(3, h.seen.size)
        assertEquals("甲乙丙丁", h.seen.last().items.last().text)
    }

    @Test fun aWindowThatKeepsReceivingChunksPublishesOncePerWindow() = runTest {
        val h = harness(window = 50)
        for (n in 1..10) {
            delta(h, "字".repeat(n))
            advanceTimeBy(10); runCurrent()
        }
        // 100 ms of chunks at 10 ms: the first at once, then one publish per 50 ms window.
        advanceTimeBy(100); runCurrent()
        assertEquals("字".repeat(10), h.seen.last().items.last().text)
        // Virtual time is exact: the chunk at 0 ms at once, the window ending at 50 ms, and the one ending at 100 ms.
        assertEquals("publishes=${h.seen.size}", 3, h.seen.size)
    }

    @Test fun theTurnsEndPublishesAtOnceAndNoLaterWindowRepeatsIt() = runTest {
        val h = harness(window = 50)
        delta(h, "甲")
        delta(h, "甲乙")
        assertEquals(1, h.seen.size)
        delta(h, "")            // turn finished inside the open window
        assertEquals(2, h.seen.size)
        assertEquals("甲乙", h.seen.last().items.last().text)
        assertTrue(h.seen.last().streamingItemIds.isEmpty())
        advanceTimeBy(200); runCurrent()
        assertEquals(2, h.seen.size)
    }

    @Test fun theTurnsEndPublishesWithoutWaitingForTheCatchUpFetch() = runTest {
        val h = harness(window = 50)
        h.fetchHangs.set(true)
        h.connection.state.value = ConnectionState.Connecting
        runCurrent()
        h.seen.clear()
        delta(h, "甲")
        delta(h, "甲乙")        // inside the open window: not published yet
        assertEquals(1, h.seen.size)
        delta(h, "")            // turn finished: no time passes and the fetch never answers
        assertEquals(2, h.seen.size)
        assertEquals("甲乙", h.seen.last().items.last().text)
        assertTrue(h.seen.last().streamingItemIds.isEmpty())
    }

    @Test fun aNewTurnsFirstChunkShowsAtOnceEvenWhileTheLastTurnsWindowIsOpen() = runTest {
        val h = harness(window = 50)
        delta(h, "甲")
        delta(h, "甲乙")
        delta(h, "")           // turn ends while the window is open
        val afterEnd = h.seen.size
        delta(h, "新")          // next turn's first chunk, same virtual time
        assertEquals(afterEnd + 1, h.seen.size)
        assertEquals("新", h.seen.last().items.last().text)
    }

    @Test fun chunksDuringAWindowKeepTheTextChangingBetweenPublishes() = runTest {
        val h = harness(window = 50)
        val texts = mutableListOf<String>()
        for (n in 1..10) {
            delta(h, "字".repeat(n))
            advanceTimeBy(10); runCurrent()
            texts += h.seen.last().items.last().text.orEmpty()
        }
        // Every 50 ms a publish shows the text at that moment, never an older one.
        assertTrue("distinct=${texts.distinct().size}", texts.distinct().size >= 3)
        assertEquals(texts.sortedBy { it.length }, texts)
        assertEquals("字".repeat(10), h.seen.last().items.last().text)
    }

    @Test fun chunksAppliedInArrivalOrderShowTheLastOneApplied() = runTest {
        val h = harness(window = 50)
        delta(h, "较长的草稿")
        delta(h, "较短")        // a shorter draft arriving after a longer one in the same window wins: arrival order
        advanceTimeBy(50); runCurrent()
        assertEquals("较短", h.seen.last().items.last().text)
    }

    @Test fun appendOnlyDeltasHaveOneStructuralEmissionAndCurrentLiveText() = runTest {
        val h = harness(window = 50)
        val structural = mutableListOf<ChatSnapshot>()
        backgroundScope.launch(UnconfinedTestDispatcher(testScheduler)) {
            h.repository.observeStructure(key).collect { structural += it }
        }
        runCurrent()
        structural.clear() // History is already loaded; count this reply's structural rows.
        var expectedText = ""
        for (n in 1..200) {
            expectedText = "示例回复 ".repeat(1600).take(n * 40)
            delta(h, expectedText)
            assertEquals("latest live text at chunk $n", expectedText, h.repository.liveText(key))
            assertEquals("structural emissions at chunk $n", 1, structural.size)
            assertEquals(201, structural.single().items.size)
            advanceTimeBy(80); runCurrent()
        }
        assertEquals(201, structural.sumOf { it.items.size })
        assertEquals(200, h.seen.size)
        assertEquals(40_200, h.seen.sumOf { it.items.size })
        assertEquals(8_000, expectedText.length)
        assertEquals(expectedText, h.seen.last().items.last().text)
        val completePublishes = h.seen.size
        delta(h, "")
        assertEquals(completePublishes + 1, h.seen.size)
        assertEquals(expectedText, h.seen.last().items.last().text)
        assertTrue(h.seen.last().streamingItemIds.isEmpty())
        assertEquals("turn end is the only later structural event", 2, structural.size)
        assertEquals(402, structural.sumOf { it.items.size })
        advanceTimeBy(100); runCurrent()
        assertEquals(completePublishes + 1, h.seen.size)
    }

    @Test fun measurementOfAReplyOf200Chunks() = runTest {
        for (window in listOf(50L, 100L)) for (gap in listOf(0L, 80L)) {
            val h = harness(window)
            for (n in 1..200) {
                delta(h, "示例回复 ".repeat(1600).take(n * 40))
                if (gap > 0) { advanceTimeBy(gap); runCurrent() }
            }
            advanceTimeBy(window * 2); runCurrent()   // flush the trailing publish
            val publishes = h.seen.size
            val itemsBuilt = h.seen.sumOf { it.items.size }
            val charsCopied = h.seen.sumOf { it.items.lastOrNull()?.text?.length ?: 0 }
            println("MEASURE window=${window}ms chunkGap=${gap}ms publishes=$publishes itemsBuilt=$itemsBuilt charsCopied=$charsCopied finalChars=${h.seen.last().items.last().text?.length}")
            assertEquals(8000, h.seen.last().items.last().text?.length)
            // The PR table's counts (virtual time, so exact; +-2 for a future off-by-one in the window): no gap is the first
            // chunk plus one window's end; a gap of 80 ms outlasts a 50 ms window (every chunk at once) and not a 100 ms one.
            val expected = when { gap == 0L -> 1..3; window == 50L -> 198..202; else -> 159..163 }
            assertTrue("window=$window gap=$gap publishes=$publishes", publishes in expected)
        }
    }
}
