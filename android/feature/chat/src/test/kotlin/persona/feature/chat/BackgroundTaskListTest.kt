package persona.feature.chat

import kotlinx.coroutines.ExperimentalCoroutinesApi
import kotlinx.coroutines.cancelAndJoin
import kotlinx.coroutines.launch
import kotlinx.coroutines.test.advanceTimeBy
import kotlinx.coroutines.test.runCurrent
import kotlinx.coroutines.test.runTest
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.BackgroundTask
import persona.core.contracts.Session
import persona.core.contracts.Workflow
import persona.core.contracts.api.SessionLogTail
import java.time.Instant

class BackgroundTaskListTest {
    private val now = Instant.parse("2026-11-20T10:00:00Z")
    private fun before(s: Long) = now.minusSeconds(s).toString()
    private val shell = BackgroundTask("command", "shell", description = "Run checks", startedAt = before(100))
    private val monitor = BackgroundTask("monitor", "monitor", description = "Watch results", startedAt = before(40))
    private val run = Workflow(taskId = "work", runId = "run", status = "running", name = "Verify", startedAt = before(90))

    @Test fun exactRowsDeduplicateWorkflowsAndPreferARetainedOutcomeOverAStaleRunningRecord() {
        val stale = run.copy(taskId = "stale", runId = "old", status = "completed", endedAt = before(100_000))
        val ended = shell.copy(status = "failed", endedAt = before(10), exitCode = 7)
        val session = Session("m", "s", workflows = listOf(run, stale), background = listOf(shell, monitor,
            BackgroundTask("work", "workflow")), backgroundEnded = listOf(ended))
        val sections = backgroundSections(session, now)
        assertEquals(2, sections.runningCount)
        assertEquals(listOf(run), sections.runningRuns)
        assertEquals(listOf(monitor), sections.runningCommands.map(BackgroundCommand::task))
        assertEquals(emptyList<Workflow>(), sections.completedRuns)
        assertEquals(BackgroundOutcome.Failed, sections.command("command", shell).outcome)
        assertEquals(90L, sections.command("command", shell).elapsed(now))
        assertEquals(run, sections.latestWorkflow)
        assertEquals(run, sections.workflow("run"))
        assertTrue(sections.command("monitor", monitor).opens)
    }

    @Test fun disappearanceRetainsTheLastMetadataWithoutInferringAResultOrElapsedTime() {
        val command = backgroundSections(Session("m", "s"), now).command("command", shell.copy(outputFile = "/work/tail.txt"))
        assertEquals(BackgroundOutcome.Unknown, command.outcome)
        assertEquals("/work/tail.txt", command.task.outputFile)
        assertFalse(command.running)
        assertNull(command.elapsed(now))
        assertFalse(BackgroundCommand(BackgroundTask("child", "agent"), BackgroundOutcome.Running).opens)
    }

    @Test fun aSummaryDoesNotDoubleCountARunningWorkflowLink() {
        val summarized = Session("m", "s", waiting = false, backgroundCountHint = 2, runningWorkflowsCount = 1, backgroundTasksCount = 1)
        assertEquals(2, backgroundRunningCount(summarized, now))
        val legacy = Session("m", "s", background = listOf(shell, BackgroundTask("w", "workflow")))
        assertEquals(2, backgroundRunningCount(legacy, now))
    }

    @OptIn(ExperimentalCoroutinesApi::class)
    @Test fun aRunningLogPollCanBeStoppedAndAFinishedCommandReadsExactlyOnce() = runTest {
        var attempts = 0
        val updates = mutableListOf<Result<SessionLogTail>>()
        val running = launch {
            readBackgroundOutput(true, read = {
                if (++attempts == 2) error("temporary read failure")
                SessionLogTail("line $attempts", false)
            }, onResult = { updates += it })
        }
        runCurrent()
        assertEquals("line 1", updates.single().getOrThrow().text)
        advanceTimeBy(2_000); runCurrent()
        assertTrue(updates.last().isFailure)
        advanceTimeBy(2_000); runCurrent()
        assertEquals("line 3", updates.last().getOrThrow().text)
        running.cancelAndJoin()
        advanceTimeBy(10_000)
        assertEquals(3, attempts)
        readBackgroundOutput(false, read = { attempts++; SessionLogTail("finished", false) }, onResult = { updates += it })
        advanceTimeBy(10_000)
        assertEquals(4, attempts)
        assertEquals("finished", updates.last().getOrThrow().text)
        assertEquals(2, backgroundOutputLines("one\r\ntwo\r\n"))
        assertEquals(0, backgroundOutputLines(""))
    }
}
