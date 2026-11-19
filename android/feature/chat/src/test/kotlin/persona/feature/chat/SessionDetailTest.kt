package persona.feature.chat

import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.*

class SessionDetailTest {
    @Test fun aCompleteCodexRowHydratesTheWaitingFlagOmittedByASummary() {
        val summary = Session("computer", "waiting", agent = "codex", waiting = false)
        val complete = summary.copy(waiting = null, awaitingFirstMessage = true)
        val merged = sessionWithDetail(summary, null, null, complete)
        assertTrue(merged.awaitingFirstMessage)
        assertTrue(sessionWithDetail(summary, merged, null).awaitingFirstMessage)
        assertFalse(sessionWithDetail(summary.copy(sessionId = "other"), merged, null, complete).awaitingFirstMessage)
    }
    @Test fun archivedMetadataIsAvailableOnlyForItsReadOnlyConversation() {
        for (agent in listOf("claude", "codex")) {
            val archived = Session("computer", "history", agent = agent, title = "Archived $agent",
                cwd = "/projects/app", archived = true)
            val shown = readOnlySessionMetadata(archived.key, historyReadOnly = true, archived)
            assertEquals(archived, shown)
            assertEquals("Archived $agent", shown?.displayTitle)
            assertNull(readOnlySessionMetadata(archived.key, historyReadOnly = false, archived))
            assertNull(readOnlySessionMetadata(SessionKey("other", "history"), historyReadOnly = true, archived))
            assertNull(readOnlySessionMetadata(SessionKey("computer", "other"), historyReadOnly = true, archived))
            assertNull(readOnlySessionMetadata(archived.key, historyReadOnly = true, null))
        }
    }

    @Test fun summaryDoesNotEraseOpenChatDetail() {
        val held = Session("demo", "task", launcher = "example", permissionMode = "plan", effort = "high",
            model = "example-model", queued = listOf("next"), efforts = listOf("low", "high"))
        val summary = Session("demo", "task", title = "Updated", state = "working", waiting = false)
        val merged = sessionWithDetail(summary, held, null)
        assertEquals("Updated", merged.title)
        assertEquals("working", merged.state)
        assertEquals(held.launcher, merged.launcher)
        assertEquals(held.permissionMode, merged.permissionMode)
        assertEquals(held.queued, merged.queued)
        assertEquals(held.efforts, merged.efforts)
    }
    @Test fun infoHydratesOneSummaryAndDoesNotCrossSessionKeys() {
        val summary = Session("demo", "task", waiting = false)
        val old = Session("demo", "different", queued = listOf("unrelated"))
        val merged = sessionWithDetail(summary, old, SessionInfo(launcher = "example", permissionMode = "plan", model = "new-model"))
        assertEquals("example", merged.launcher)
        assertEquals("plan", merged.permissionMode)
        assertEquals("new-model", merged.model)
        assertNull(merged.queued)
    }
    @Test fun summaryCarriesBothQueuedRestartEdges() {
        val summary = Session("demo", "task", waiting = false)
        val queued = sessionWithDetail(summary.copy(bypassPending = true), summary, null)
        assertTrue(queued.bypassPending)
        // A later authoritative false clears the edge too, so the outcome is read without another session lookup.
        assertFalse(sessionWithDetail(summary, queued, null).bypassPending)
    }

    @Test fun legacyCompleteRowCanClearOldDetail() {
        val old = Session("demo", "task", queued = listOf("old"))
        val complete = Session("demo", "task", queued = emptyList())
        assertEquals(complete, sessionWithDetail(complete, old, null))
    }
}
