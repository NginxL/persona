package persona.core.domain.sort

import java.time.Instant
import kotlinx.serialization.json.jsonPrimitive
import persona.core.domain.FixtureReader
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Closeout
import persona.core.contracts.CloseoutChecked
import persona.core.contracts.Session
import persona.core.contracts.WorkState
import persona.core.domain.zhAny

class CloseoutTest {
    @Test fun reportsAboveTheSameCard() {
        for ((_, sample) in FixtureReader.read("work-reports")) {
            if (sample.getValue("name").jsonPrimitive.content == "done") {
                val text = sample.getValue("text").jsonPrimitive.content
                assertEquals(text, stripCloseout(text, complete = false))
            }
            assertEquals(sample.getValue("name").jsonPrimitive.content, sample.getValue("display").jsonPrimitive.content,
                stripCloseout(sample.getValue("text").jsonPrimitive.content))
        }
    }

    private val now = Instant.parse("2026-11-10T10:00:00Z")
    private fun session(work: WorkState?, state: String = "unread", status: String = "done") =
        Session(machineId = "m", sessionId = "s", status = status, state = state, work = work)
    private fun line(work: WorkState?, state: String = "unread", status: String = "done") = session(work, state, status).workLine(now).zhAny()

    @Test fun aClaimStandsAgainstWhatTheHubFound() {
        val said = Closeout("done", merged = "yes", deployed = "no")
        assertEquals(CloseoutClaim(true, null, ClaimState.Unchecked), said.claim(CloseoutSubject.Merged))
        assertEquals(CloseoutClaim(false, null, ClaimState.Unchecked), said.claim(CloseoutSubject.Deployed))
        val checked = said.copy(checked = CloseoutChecked(merged = "yes", deployed = "yes"))
        assertEquals(ClaimState.Confirmed, checked.claim(CloseoutSubject.Merged)!!.state)
        assertEquals(ClaimState.Contradicted, checked.claim(CloseoutSubject.Deployed)!!.state)
        assertTrue(checked.contradicted)
        assertFalse(said.contradicted)
        // the agent did not say: the hub's finding stands alone; neither said nor found: nothing to show
        val found = Closeout("done", checked = CloseoutChecked(merged = "no"))
        assertEquals(CloseoutClaim(null, false, ClaimState.Found), found.claim(CloseoutSubject.Merged))
        assertNull(found.claim(CloseoutSubject.Deployed))
        assertFalse(found.contradicted)
        // "n/a" and any other word are not said
        assertNull(Closeout("done", merged = "n/a").claim(CloseoutSubject.Merged))
    }

    private fun facts(work: WorkState?) = session(work).closeoutFacts.map { it.text.zhAny() to it.contradicted }

    @Test fun theRowKeepsItsLineAndSaysWhereTheCloseoutStandsBesideIt() {
        val said = WorkState("done", line = "修好了重试", closeout = Closeout("done", merged = "yes", deployed = "no"))
        assertEquals("✓ 修好了重试", line(said))
        assertEquals(listOf("已合并" to false, "未部署" to false), facts(said))
        // the hub's finding replaces the agent's word once it looked
        assertEquals(listOf("已合并 ✓" to false, "未部署" to false), facts(said.copy(closeout = said.closeout!!.copy(
            checked = CloseoutChecked(merged = "yes", deployed = "no")))))
        // only one of the two said
        assertEquals(listOf("已部署" to false), facts(WorkState("done", line = "修好了重试", closeout = Closeout("done", deployed = "yes"))))
        // says nothing on either, or no closeout at all: today's row
        assertTrue(facts(WorkState("done", line = "修好了重试", closeout = Closeout("done"))).isEmpty())
        assertTrue(facts(WorkState("done", line = "修好了重试")).isEmpty())
        assertEquals("✓ 修好了重试", line(WorkState("done", line = "修好了重试")))
    }

    @Test fun aContradictedClaimIsMarked() {
        val work = WorkState("", line = "说已合并，但 PR 没有合并", closeout = Closeout("done", merged = "yes", deployed = "yes",
            checked = CloseoutChecked(merged = "no", deployed = "no")))
        assertEquals("说已合并，但 PR 没有合并", line(work))
        assertEquals(listOf("✗ 说已合并，实际没有" to true, "✗ 说已部署，实际没有" to true), facts(work))
        assertEquals(CloseoutMark.Contradicted, session(work).closeoutMark)
        // a "no" the hub found to be a yes is not an overclaim: the finding is said, unmarked
        assertEquals(listOf("已合并 ✓" to false), facts(WorkState("done", line = "好了", closeout = Closeout("done", merged = "no",
            checked = CloseoutChecked(merged = "yes")))))
    }

    @Test fun anEmptyPhaseWithALineIsNotFinished() {
        val work = WorkState("", line = "没做完：还差迁移脚本", closeout = Closeout("not_done", missing = "迁移脚本", merged = "no"))
        // the hub words the line itself
        assertEquals("没做完：还差迁移脚本", line(work))
        assertEquals(listOf("未合并" to false), facts(work))
        assertEquals(CloseoutMark.NotFinished, session(work).closeoutMark)
        // a finished or waiting turn has no mark, nor a turn without a closeout
        assertNull(session(WorkState("done", line = "好了", closeout = Closeout("done", merged = "yes"))).closeoutMark)
        assertNull(session(WorkState("blocked", on = "external", line = "等 CI", closeout = Closeout("blocked", waiting = "CI"))).closeoutMark)
        assertNull(session(WorkState("", line = "一行")).closeoutMark)
    }

    @Test fun aTurnWithoutALineSaysTheCloseoutsOwnWords() {
        assertEquals("卡在：CI", line(WorkState("", closeout = Closeout("blocked", waiting = "CI"))))
        assertEquals("卡住了", line(WorkState("", closeout = Closeout("blocked"))))
        assertEquals("没做完：评审", line(WorkState("", closeout = Closeout("not_done", missing = "评审", merged = "no"))))
        assertEquals("没做完", line(WorkState("", closeout = Closeout("not_done"))))
        assertEquals("自述已完成", line(WorkState("", closeout = Closeout("done", merged = "yes"))))
    }

    @Test fun nothingOfTheCloseoutShowsWhileItWorksOrForAStatusNobodyKnows() {
        val closeout = Closeout("done", merged = "yes")
        assertNull(line(WorkState("working", closeout = closeout), state = "working", status = "working"))
        assertNull(session(WorkState("done", line = "好了", closeout = closeout), state = "working", status = "working").shownCloseout)
        assertNull(session(WorkState("done", line = "好了", closeout = closeout), state = "working", status = "working").closeoutMark)
        assertTrue(session(WorkState("done", line = "好了", closeout = closeout), state = "working", status = "working").closeoutFacts.isEmpty())
        assertTrue(facts(WorkState("done", line = "好了", closeout = Closeout("parked", merged = "yes"))).isEmpty())
        assertEquals("✓ 好了", line(WorkState("done", line = "好了", closeout = Closeout("parked", merged = "yes"))))
        assertNull(session(null).shownCloseout)
    }

    @Test fun onlyTheLastBlockIsTakenOutOfTheReply() {
        val block = "<closeout status=\"done\" merged=\"yes\" deployed=\"no\">\ndid: Fixed the retry\nverified: unit tests pass\n</closeout>"
        assertEquals("All done.", stripCloseout("All done.\n\n$block"))
        assertEquals("All done.\n\nSee the PR.", stripCloseout("All done.\n\n$block\n\nSee the PR."))
        assertEquals("", stripCloseout(block))
        assertEquals("", stripCloseout("\n$block\n"))
        // text without a block comes back as it is, spacing included
        assertEquals("  No block here.\n", stripCloseout("  No block here.\n"))
        assertEquals("An unclosed <closeout status=\"done\"> stays", stripCloseout("An unclosed <closeout status=\"done\"> stays"))
        // an earlier block is part of what the agent wrote: only the one the hub read goes
        val earlier = "<closeout status=\"not_done\">\ndid: half\n</closeout>"
        assertEquals("First:\n\n$earlier\n\nThen:", stripCloseout("First:\n\n$earlier\n\nThen:\n\n$block"))
        // other tags are not touched
        assertEquals("<closeouts>x</closeouts>", stripCloseout("<closeouts>x</closeouts>"))
    }

    @Test fun aFenceThatHeldOnlyTheBlockGoesWithIt() {
        val block = "<closeout status=\"done\">\ndid: x\n</closeout>"
        assertEquals("Done.", stripCloseout("Done.\n\n```\n$block\n```"))
        assertEquals("Done.\n\nMore.", stripCloseout("Done.\n\n```xml\n$block\n```\n\nMore."))
        assertEquals("Done.", stripCloseout("Done.\n\n~~~\n$block\n~~~\n"))
        // a fence that holds more keeps its other lines
        assertEquals("```\nnote\n\n```", stripCloseout("```\nnote\n$block\n```"))
    }
}
