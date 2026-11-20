package persona.core.domain.models

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.ModelPick
import persona.core.contracts.PendingSwitch
import persona.core.contracts.Session
import persona.core.contracts.SessionCompaction
import persona.core.contracts.UndeliveredMessage

/**
 * A summary row landing on a conversation the app holds (#5055, riba2534's note on #5055; iOS `preservingDetails`):
 * what it leaves out stays, what it carries (the model, the effort, `model_pick`) is its own.
 */
class SummaryRowsTest {
    private val full = Session(machineId = "mac", sessionId = "c1", assistant = true, launcher = "team", permissionMode = "plan",
        bypassAvailable = true, bypassPending = true,
        effort = "high", model = "sonnet", compaction = SessionCompaction("done"), modelPick = ModelPick("team", "sonnet"),
        pendingSwitch = PendingSwitch("work"), runtime = "agentsdk")

    @Test fun aSummaryKeepsTheChatsDetailsAndBringsItsOwnPick() {
        val summary = Session(machineId = "mac", sessionId = "c1", assistant = true, waiting = false, model = "opus", effort = "low",
            modelPick = ModelPick("claude", "opus"))
        val merged = summary.keepingDetails(full)
        assertEquals("team", merged.launcher)
        assertEquals("plan", merged.permissionMode)
        assertEquals(true, merged.bypassAvailable)
        assertEquals(false, merged.bypassPending)
        assertEquals(SessionCompaction("done"), merged.compaction)
        assertEquals("agentsdk", merged.runtime)
        assertEquals(PendingSwitch("work"), merged.pendingSwitch)
        assertEquals("opus", merged.model)
        assertEquals("low", merged.effort)
        assertEquals(ModelPick("claude", "opus"), merged.modelPick)
    }

    @Test fun suggestionTextAndRiskAlwaysComeFromTheSameRow() {
        for (oldRisk in listOf(false, true)) {
            val old = full.copy(suggestion = "Review the old plan", suggestionRisky = oldRisk)
            val summary = Session(machineId = "mac", sessionId = "c1", assistant = true, waiting = false)
            val inherited = summary.keepingDetails(old)
            assertEquals("Review the old plan", inherited.suggestion)
            assertEquals("inherited text keeps inherited risk", oldRisk, inherited.suggestionRisky)
        }
        val old = full.copy(suggestion = "Review the old plan", suggestionRisky = true)
        for (newRisk in listOf(false, true)) {
            val summary = Session(machineId = "mac", sessionId = "c1", assistant = true, waiting = false,
                suggestion = "Review the new plan", suggestionRisky = newRisk)
            val merged = summary.keepingDetails(old)
            assertEquals("Review the new plan", merged.suggestion)
            assertEquals("new text brings its own risk", newRisk, merged.suggestionRisky)
        }
    }

    @Test fun aSummaryWithoutAPickMeansItFollowsTheDefault() {
        val summary = Session(machineId = "mac", sessionId = "c1", assistant = true, waiting = false)
        assertNull(summary.keepingDetails(full).modelPick)
        assertEquals("high", summary.keepingDetails(full).effort)
    }

    @Test fun aCompleteRowOrNoneHeldIsTakenAsItIs() {
        val complete = Session(machineId = "mac", sessionId = "c1", assistant = true)
        assertSame(complete, complete.keepingDetails(full))
        val summary = complete.copy(waiting = true)
        assertSame(summary, summary.keepingDetails(null))
        assertSame(summary, summary.keepingDetails(full.copy(sessionId = "other")))
    }

    @Test fun aSummaryKeepsUndeliveredButACompleteEmptyListClearsIt() {
        val kept = UndeliveredMessage("kept", "words", at = "2026-11-17T10:00:00Z", state = "waiting")
        val detailed = full.copy(undelivered = listOf(kept))
        val summary = Session(machineId = "mac", sessionId = "c1", assistant = true, waiting = false)
        assertEquals(listOf(kept), summary.keepingDetails(detailed).undelivered)
        val complete = Session(machineId = "mac", sessionId = "c1", assistant = true)
        assertTrue(complete.keepingDetails(detailed).undelivered.isNullOrEmpty())
    }
}
