package persona.core.domain.lines

import java.time.Instant
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Item
import persona.core.contracts.ModeChange

/** Permission mode records join an Agent's transcript at their time, never inside the exchange they happened in. */
class ModeChangesTest {
    private val t0: Instant = Instant.parse("2026-11-18T09:00:00Z")

    private fun sent(seq: Int, at: Instant, role: String = "user") = LineItem(
        Item("$seq#0", seq, ts = at.toString(), role = role, kind = "text", text = if (role == "user") "go $seq" else "done $seq"),
    )

    private fun work(seq: Int, at: Instant): List<LineItem> {
        val call = LineItem(Item("$seq#0", seq, ts = at.toString(), role = "assistant", kind = "tool_use", toolUseId = "tool", toolName = "Read"))
        return listOf(call, LineItem(Item("${seq + 1}#0", seq + 1, ts = at.plusSeconds(10).toString(), role = "user", kind = "tool_result", toolUseId = "tool", text = "ok")))
    }

    private fun change(at: Instant, kind: String = "set", to: String = "acceptEdits") = ModeChange(
        kind = kind, from = "default", to = to, applied = "now", actorKind = "device", deviceName = "Phone", at = at.toString(),
    )

    private fun kinds(lines: List<ChatLine>) = lines.map { line ->
        when {
            line is ChatLine.ModeChange -> "mode"
            line is ChatLine.Row && line.row.type == "user" -> "user"
            line is ChatLine.Row && line.row.type == "reply" -> "answer"
            line is ChatLine.Work -> "work"
            else -> "other"
        }
    }

    @Test fun changesFollowTheExchangeTheyHappenedInAndTheNewestFollowsTheLast() {
        val items = buildList {
            add(sent(1, t0))
            addAll(work(2, t0.plusSeconds(20)))
            add(sent(4, t0.plusSeconds(60), role = "assistant"))
            add(sent(5, t0.plusSeconds(600)))
            add(sent(6, t0.plusSeconds(660), role = "assistant"))
        }
        val lines = chatLines(items).lines

        val merged = ModeChanges.merge(lines, listOf(
            change(t0.plusSeconds(900), to = "plan"),
            change(t0.plusSeconds(30)),
            change(t0.minusSeconds(60), to = "auto"),
        ))

        assertEquals(listOf("mode", "user", "work", "answer", "mode", "user", "answer", "mode"), kinds(merged))
        assertEquals(3, merged.count { it is ChatLine.ModeChange })
        assertEquals(lines.last().id, merged.filterNot { it is ChatLine.ModeChange }.last().id)
    }

    @Test fun changesOlderThanLoadedHistoryWaitForTheirPageAndEmptyChatsStayEmpty() {
        val lines = chatLines(listOf(
            sent(1, t0), sent(2, t0.plusSeconds(60), role = "assistant"),
            sent(3, t0.plusSeconds(600)), sent(4, t0.plusSeconds(660), role = "assistant"),
        )).lines
        val old = change(t0.minusSeconds(60), to = "auto")
        val during = change(t0.plusSeconds(30))
        val newest = change(t0.plusSeconds(900), to = "plan")

        val held = ModeChanges.merge(lines, listOf(old, during, newest), earliest = t0)
        assertEquals(listOf("user", "answer", "mode", "user", "answer", "mode"), kinds(held))

        val earlierPage = ModeChanges.merge(lines, listOf(old, during, newest), earliest = t0.minusSeconds(120))
        assertEquals(listOf("mode", "user", "answer", "mode", "user", "answer", "mode"), kinds(earlierPage))
        assertTrue(ModeChanges.merge(emptyList(), listOf(during)).isEmpty())
        assertSame(lines, ModeChanges.merge(lines, emptyList()))
    }

    @Test fun idsSurviveTheListGrowingOrLosingItsOldestRecord() {
        val lines = chatLines(listOf(sent(1, t0), sent(2, t0.plusSeconds(60), role = "assistant"))).lines
        val twice = listOf(change(t0.plusSeconds(5)), change(t0.plusSeconds(5)), change(t0.plusSeconds(9), to = "plan"))

        val before = ModeChanges.merge(lines, twice).filterIsInstance<ChatLine.ModeChange>()
        val after = ModeChanges.merge(lines, twice.drop(1)).filterIsInstance<ChatLine.ModeChange>()

        assertEquals(3, before.size)
        assertEquals(before.size, before.map { it.id }.distinct().size)
        assertEquals(before.last().id, after.last().id)
        assertEquals(before.last().contentKey, after.last().contentKey)
    }

    @Test fun changesNewerThanAFrozenWindowWaitForLiveFollowing() {
        val lines = chatLines(listOf(sent(1, t0), sent(2, t0.plusSeconds(60), role = "assistant"))).lines
        val during = change(t0.plusSeconds(30))
        val atCutoff = change(t0.plusSeconds(60), to = "plan")
        val newer = change(t0.plusSeconds(120), to = "auto")

        val frozen = ModeChanges.merge(lines, listOf(during, atCutoff, newer), until = t0.plusSeconds(60))

        assertEquals(listOf(during, atCutoff), frozen.filterIsInstance<ChatLine.ModeChange>().map { it.change })
        assertTrue(ModeChanges.merge(lines, listOf(during), until = t0.minusSeconds(1)).none { it is ChatLine.ModeChange })
        assertEquals(3, ModeChanges.merge(lines, listOf(during, atCutoff, newer)).count { it is ChatLine.ModeChange })
    }

    @Test fun validRecordsAppearOnceAndMalformedRecordsAddNoLine() {
        val lines = chatLines(listOf(sent(1, t0), sent(2, t0.plusSeconds(60), role = "assistant"))).lines
        val valid = change(t0.plusSeconds(30))
        val merged = ModeChanges.merge(lines, listOf(
            valid,
            valid.copy(kind = "made_up", at = t0.plusSeconds(31).toString()),
            valid.copy(at = "never"),
        ))

        assertEquals(listOf(valid), merged.filterIsInstance<ChatLine.ModeChange>().map { it.change })
        assertEquals(1, merged.count { it is ChatLine.ModeChange })
    }
}
