package persona.feature.chat

import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.domain.ports.PortLinks
import persona.core.domain.views.AssistantFences

/**
 * AC2: streaming ancillary work is incremental, without changing any whole-message result.
 * The expected seam reports one scanned suffix as (complete text, zero-based start) per update; `finished = true` settles whole.
 * Its blocks are the top-level rendered units, so stable units can be checked by identity rather than by timing.
 */
class AssistantAncillaryStreamTest {
    @Test fun everyPrefixEqualsWholeMessageAncillaries() {
        val scans = mutableListOf<Pair<Int, Int>>()
        val stream = AssistantAncillaryStream { text, from -> scans += text.length to from }
        for (end in 0..reply.length) {
            val prefix = reply.substring(0, end)
            assertWhole(prefix, stream.update(prefix))
        }
        assertEquals(reply.length + 1, scans.size)
    }

    @Test fun ordinaryAppendsReuseSettledBlocksAndScanOnlyTheGrowingTail() {
        val scans = mutableListOf<Pair<Int, Int>>()
        val stream = AssistantAncillaryStream { text, from -> scans += text.length to from }
        val done = "First settled paragraph.\n\n" +
            "```kotlin\nval settled = 1\n```\n\n" +
            "```persona-card\n{\"fallback\":\"Settled card\"}\n```\n\n" +
            "Fourth /tmp/settled.kt:12 and localhost:5173/.\n\n"
        val prefixes = listOf(done + "Tail", done + "Tail grows", done + "Tail grows again", done + "Tail grows again!")

        val first = stream.update(prefixes[0])
        assertWhole(prefixes[0], first)
        assertEquals(5, first.blocks.size)
        val settled = first.blocks.take(4)
        var previous = first
        for (index in 1 until prefixes.size) {
            val next = stream.update(prefixes[index])
            assertWhole(prefixes[index], next)
            assertEquals(5, next.blocks.size)
            for (block in 0 until 4) assertSame("settled block $block at prefix $index", settled[block], next.blocks[block])
            assertSame(first.cards.single(), next.cards.single())
            assertSame(first.ports.single(), next.ports.single())
            assertTrue("the unfinished tail must be rebuilt", previous.blocks[4] !== next.blocks[4])
            previous = next
        }

        assertEquals(listOf(0, done.length, done.length, done.length), scans.map { it.second })
        assertEquals(prefixes.first().length, scans.first().first)
        assertEquals(prefixes.drop(1).map { it.length - done.length }, scans.drop(1).map { it.first - it.second })
    }

    @Test fun structuredContentAndFinalSettlementUseWholeTextFallbacks() {
        val scans = mutableListOf<Pair<Int, Int>>()
        val stream = AssistantAncillaryStream { text, from -> scans += text.length to from }
        val opening = "<cross-session-message from-name=\"Other\">quoted /tmp/quoted.kt:4"
        stream.update(opening)
        val structured = "$opening</cross-session-message>\n\nTail"
        assertWhole(structured, stream.update(structured))
        val growing = "$structured grows"
        assertWhole(growing, stream.update(growing))
        assertEquals(listOf(0, 0, 0), scans.map { it.second })

        val finished = stream.update(growing, finished = true)
        assertWhole(growing, finished)
        assertEquals(4, scans.size)
        assertEquals(0, scans.last().second)
        assertEquals(growing.length, scans.last().first - scans.last().second)
    }

    @Test fun aNonAppendEditResetsIdentityAndThenStartsReusingAgain() {
        val scans = mutableListOf<Pair<Int, Int>>()
        val stream = AssistantAncillaryStream { text, from -> scans += text.length to from }
        val before = stream.update("Old settled.\n\nTail")
        val edited = "New settled.\n\nTail"
        val after = stream.update(edited)
        assertWhole(edited, after)
        assertEquals(0, scans.last().second)
        assertTrue(before.blocks.first() !== after.blocks.first())

        val appended = stream.update("$edited grows")
        assertWhole("$edited grows", appended)
        assertSame(after.blocks.first(), appended.blocks.first())
        assertEquals("New settled.\n\n".length, scans.last().second)
    }

    private fun assertWhole(text: String, actual: AssistantAncillarySnapshot) {
        val model = messageModel(text)
        val paths = PathLinks.asked(text)
        assertEquals("messageModel for ${text.length} characters", model, actual.model)
        assertEquals("path refs for ${text.length} characters", paths, actual.paths)
        assertEquals("path links for ${text.length} characters", PathLinks.markdown(text, paths.toSet()), actual.linkedMarkdown)
        assertEquals("cards for ${text.length} characters", AssistantFences.blocks(text), actual.cards)
        assertEquals("ports for ${text.length} characters", PortLinks.find(text), actual.ports)
    }

    companion object {
        private val reply = """
            Intro with **Markdown** and /tmp/assistant.kt:12.

            ```kotlin
            val path = "/tmp/not-linked.kt"
            ```

            ```persona-card
            {"fallback":"Card fallback","html":"<b>ignored</b>"}
            ```

            Serving http://localhost:5173/docs.
            Another settled paragraph.

            Tail with `src/Tail.kt:8` and **bold**
        """.trimIndent()
    }
}
