package persona.core.domain.tasks

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.CoreText
import persona.core.contracts.Session
import persona.core.contracts.Todo
import persona.core.contracts.TodoAgent
import persona.core.contracts.WorkState

class ConversationInvalidTest {
    @Test fun codedPermanentErrorsHaveNoContinueEvenWhenTheTextIsUnrecognized() {
        val session = Session(machineId = "box", sessionId = "old", state = "unread", turnError = "Context unavailable",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed")
        val cut = Interruption.of(session)!!
        assertTrue(cut.invalid)
        assertFalse(cut.canContinue)
        assertEquals(CoreText.of(CoreText.Key.ApiConversationInvalid), cut.reason)
    }

    @Test fun waitingReplacementsOutrankAnOldInvalidInterruption() {
        val old = Session(machineId = "box", sessionId = "old", state = "unread", turnError = "opaque account transition 7A99",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed")
        val todo = Todo("t", "Fix the build", origin = "main", agents = listOf(
            TodoAgent(machineId = "box", sessionId = "old"),
            TodoAgent(machineId = "box", sessionId = "new"),
        ))
        for (on in listOf("agent", "external", "transient")) {
            val replacement = Session(machineId = "box", sessionId = "new", state = "idle",
                work = WorkState(phase = "blocked", on = on))
            assertTrue(on, replacement.waitsWithoutBackground())
            val sessions = listOf(old, replacement)
            val byKey = sessions.associateBy { it.key }
            assertEquals(on, TriageStatus.Waiting, todo.triageStatus({ byKey[it] }))
            val entries = assistantEntries("main", "main", listOf(todo), sessions, emptyList(), emptyList(), emptyList(), 1_000L)
            assertTrue(on, entries.needsYou.isEmpty())
            assertEquals(on, listOf("task:t"), entries.running.map { it.key })
            assertTrue(on, (entries.running.single() as RunningEntry.Task).waiting)
        }
    }

    @Test fun transientWaitingReplacementWithTurnErrorOutranksOldInterruption() {
        val old = Session(machineId = "box", sessionId = "old", state = "idle", turnError = "opaque account transition 7A99",
            turnErrorKind = "unrecoverable", turnErrorCode = "compaction_sealed")
        val replacement = Session(machineId = "box", sessionId = "new", state = "idle",
            turnError = "API Error: 503 Service Unavailable", turnErrorKind = "transient",
            work = WorkState(phase = "blocked", on = "transient"))
        val todo = Todo("t", "Fix the build", origin = "main", agents = listOf(
            TodoAgent(machineId = "box", sessionId = "old"),
            TodoAgent(machineId = "box", sessionId = "new"),
        ))
        assertTrue(replacement.waitsWithoutBackground())
        val staleInvalid = old.copy(work = WorkState(phase = "blocked", on = "transient"))
        val invalidOnly = todo.copy(agents = todo.agents.take(1))
        val staleByKey = mapOf(old.key to staleInvalid)
        assertTrue(Interruption.of(staleInvalid)?.invalid == true)
        assertEquals(TriageStatus.Interrupted, invalidOnly.triageStatus({ staleByKey[it] }))
        val byKey = listOf(old, replacement).associateBy { it.key }
        assertEquals(TriageStatus.Waiting, todo.triageStatus({ byKey[it] }))
    }
}
