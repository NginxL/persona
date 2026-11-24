package persona.feature.agents

import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.resumeCommand
import kotlinx.coroutines.test.runTest
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.async
import kotlinx.coroutines.test.runCurrent
import org.junit.Assert.*
import org.junit.Test

class AgentOperationsTest {
    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun spaceRenameUpdatesMembersAndPreferenceKeysBeforeReplyAndRestoresOnFailure() = runTest {
        val api = FakeOperations()
        api.pins = AgentOperationPreferences(spaces = listOf("machine/旧空间", "other/保留"))
        api.mutes = AgentOperationPreferences(spaces = listOf("machine/旧空间"), allowed = setOf("machine/session"))
        val operations = AgentOperationsState(api)
        operations.refresh()
        val first = Session("machine", "session", workspaceId = "workspace", spaceId = "space", spaceName = "旧空间")
        val second = first.copy(sessionId = "second")
        val untouched = first.copy(machineId = "other")
        val reply = CompletableDeferred<Unit>()
        api.onSpaceRename = { reply.await(); throw AgentOperationFailure(false, IllegalStateException("rejected")) }
        val saving = async { runCatching { operations.renameSpace(first, " 新空间\n") } }
        runCurrent()
        assertEquals(listOf("新空间", "新空间", "旧空间"), operations.state.value.project(listOf(first, second, untouched)).map { it.spaceName })
        assertEquals(listOf("machine/新空间", "other/保留"), operations.state.value.pins?.spaces)
        assertEquals(listOf("machine/新空间"), operations.state.value.mutes?.spaces)
        assertEquals(setOf("machine/session"), operations.state.value.mutes?.allowed)
        assertTrue(operations.state.value.busy)
        reply.complete(Unit)
        assertTrue(saving.await().isFailure)
        assertEquals(listOf(first, second, untouched), operations.state.value.project(listOf(first, second, untouched)))
        assertEquals(api.pins, operations.state.value.pins)
        assertEquals(api.mutes, operations.state.value.mutes)
        api.onSpaceRename = { fail("No-op must not call the API") }
        operations.renameSpace(first, " ")
        operations.renameSpace(first, " 旧空间 ")
    }

    @Test fun preferencesPreserveRemoteEntriesAndRollbackFailedSpaceRenameAndClose() = runTest {
        val api = FakeOperations()
        val operations = AgentOperationsState(api)
        val session = Session(machineId = "machine", sessionId = "session", title = "旧会话",
            workspaceId = "workspace", spaceId = "space", spaceName = "旧空间", status = "working")
        api.pins = AgentOperationPreferences(spaces = listOf("machine/旧空间"), sessions = listOf("other/session"))
        api.mutes = AgentOperationPreferences(spaces = listOf("machine/旧空间"), allowed = setOf("other/allowed"))
        operations.refresh()
        operations.preference(false, false, session.key.path, "machine/旧空间")
        assertEquals(setOf("other/allowed", session.key.path), api.mutes.allowed)
        operations.preference(true, false, session.key.path)
        assertEquals(listOf("other/session", session.key.path), api.pins.sessions)
        val before = operations.state.value
        api.fail = true
        try { operations.renameSpace(session, "新空间"); fail("Rename must fail") } catch (_: AgentOperationFailure) { }
        assertEquals(before, operations.state.value)
        assertEquals(listOf(session), operations.state.value.project(listOf(session)))
        try { operations.session(session, AgentSessionEdit(closed = true)); fail("Close must fail") } catch (_: AgentOperationFailure) { }
        assertEquals(before, operations.state.value)
        api.uncertain = true
        try { operations.session(session, AgentSessionEdit(closed = true)); fail("Close must be uncertain") } catch (_: AgentOperationFailure) { }
        assertTrue("close:${session.key.path}" in operations.state.value.uncertain)
        assertEquals(listOf(session), operations.state.value.project(listOf(session)))
        try { operations.session(session, AgentSessionEdit(closed = true)); fail("Duplicate close must be blocked") } catch (_: IllegalStateException) { }
        assertEquals(listOf(session.key), operations.reconcile(listOf(session.copy(status = "offline", state = "offline"))))
        assertTrue(operations.state.value.uncertain.isEmpty())
        api.fail = false
        operations.renameSpace(session, "新空间")
        assertEquals("workspace", api.renamedWorkspace)
        assertEquals(listOf("machine/新空间"), operations.state.value.pins?.spaces)
        assertEquals(listOf("machine/新空间"), operations.state.value.mutes?.spaces)
        assertEquals("新空间", operations.state.value.project(listOf(session)).single().spaceName)
        // The list copies the same resume command as the chat (core:domain), with the folder.
        assertEquals("codex resume 'a'\"'\"'b'", session.copy(agent = "codex", sessionId = "a'b", cwd = "").resumeCommand())
    }

    @Test fun deleteHidesTheConversationUntilTheHubDropsItAndRestoresOnFailure() = runTest {
        val api = FakeOperations()
        val operations = AgentOperationsState(api)
        val session = Session(machineId = "machine", sessionId = "old", title = "旧对话", status = "offline")
        val other = Session(machineId = "machine", sessionId = "other", status = "working")
        api.fail = true
        try { operations.session(session, AgentSessionEdit(deleted = true)); fail("Delete must fail") } catch (_: AgentOperationFailure) { }
        assertEquals(listOf(session, other), operations.state.value.project(listOf(session, other)))
        api.fail = false
        operations.session(session, AgentSessionEdit(deleted = true))
        assertEquals(listOf(session.key), api.deleted)
        // Gone from the list right away, while the hub still lists it.
        assertEquals(listOf(other), operations.state.value.project(listOf(session, other)))
        assertTrue(operations.reconcile(listOf(session, other)).isEmpty())
        // Once the hub's list has dropped it, the edit is done (the view model already told an open chat to go).
        operations.reconcile(listOf(other))
        assertTrue(operations.state.value.sessions.isEmpty())
    }

    @Test fun unloadedWindowIsNotEvidenceOfAnAmbiguousDeletion() = runTest {
        val api = FakeOperations().apply { fail = true; uncertain = true }
        val operations = AgentOperationsState(api)
        val session = Session("demo", "task", status = "offline")
        try { operations.session(session, AgentSessionEdit(deleted = true)); fail("Expected uncertain result") }
        catch (_: AgentOperationFailure) { }
        assertTrue(operations.reconcile(emptyList(), complete = false).isEmpty())
        assertTrue(operations.state.value.uncertain.isNotEmpty())
        assertEquals(listOf(session.key), operations.reconcile(emptyList(), complete = true))
        assertTrue(operations.state.value.uncertain.isEmpty())
    }

    /** A Tern space is one of Tern's sessions and a Tern pane's tab a Tern tab; the dialogs say so (iOS SpaceRenameForm). */
    @Test fun renameDialogsNameTernForItsSpacesAndTabs() {
        val herdr = Session("machine", "session", paneId = "w1:p1", spaceKind = "herdr", spaceId = "w1", spaceName = "app")
        val tern = herdr.copy(paneId = "tern:201863462919", spaceKind = "tern", spaceId = "4")
        val words = listOf(renameNote(tern, space = true), renameNote(herdr, space = true), renameNote(tern, space = false),
            renameNote(herdr, space = false), tabNameLabel(tern), tabNameLabel(herdr))
        assertEquals(listOf(R.string.agents_dialog_rename_space_text_tern, R.string.agents_dialog_rename_space_text,
            R.string.agents_dialog_rename_text_tern, R.string.agents_dialog_rename_text,
            R.string.agents_dialog_tab_name_tern, R.string.agents_dialog_tab_name), words)
        assertEquals("real, distinct resource ids", words.size, words.toSet().size)
    }

    /** A runtime's session (#6294) has no tab and no Claude's /rename to speak of: its rename says nothing more. */
    @Test fun aSessionWithoutATerminalRenamesWithoutANote() {
        val paseo = Session("machine", "paseo", sourceKind = "runtime", agent = "claude", spaceKind = "paseo", slotName = "release",
            capabilities = SessionCapabilities(read = true, send = true, rename = true))
        assertNull(renameNote(paseo, space = false))
        assertEquals(R.string.agents_dialog_rename_text, renameNote(Session("machine", "s", paneId = "w1:p1"), space = false))
    }

    /** Swiping closes where the node says it can (#6294), else in a terminal pane, as before; never while busy or offline. */
    @Test fun swipingClosesWhereTheNodeSaysItCan() {
        val paseo = Session("machine", "paseo", status = "idle", sourceKind = "runtime", agent = "claude",
            capabilities = SessionCapabilities(read = true, send = true, close = true))
        fun swipe(session: Session, busy: Boolean = false) = closeSwipe(AgentSession(session), busy) {}
        assertNotNull(swipe(paseo))
        assertNull(swipe(paseo, busy = true))
        assertNull(swipe(paseo.copy(status = "offline")))
        assertNull(swipe(paseo.copy(capabilities = paseo.capabilities!!.copy(close = false))))
        assertNull("an older node says nothing", swipe(paseo.copy(capabilities = SessionCapabilities(read = true, send = true))))
        assertNotNull(swipe(Session("machine", "s", status = "idle", paneId = "w1:p1")))
        assertNull(swipe(Session("machine", "s", status = "idle", assistant = true)))
    }

    private class FakeOperations : AgentOperations {
        override val scope = "scope"
        var pins = AgentOperationPreferences()
        var mutes = AgentOperationPreferences()
        var fail = false
        var uncertain = false
        var renamedWorkspace: String? = null
        var onSpaceRename: suspend () -> Unit = {}
        private fun checkFailure() { if (fail) throw AgentOperationFailure(uncertain, IllegalStateException()) }
        override suspend fun pins() = pins
        override suspend fun mutes() = mutes
        override suspend fun savePins(value: AgentOperationPreferences) { checkFailure(); pins = value }
        override suspend fun saveMutes(value: AgentOperationPreferences) { checkFailure(); mutes = value }
        override suspend fun renameSession(key: SessionKey, title: String?, tabName: String?) { checkFailure() }
        override suspend fun renameSpace(machineId: String, workspaceId: String, name: String) { checkFailure(); renamedWorkspace = workspaceId; onSpaceRename() }
        override suspend fun closeSession(key: SessionKey) { checkFailure() }
        val deleted = mutableListOf<SessionKey>()
        val forked = mutableListOf<SessionKey>()
        override suspend fun deleteSession(key: SessionKey) { checkFailure(); deleted += key }
        override suspend fun forkSession(key: SessionKey): String { checkFailure(); forked += key; return "fork-${key.sessionId}" }
    }

    @Test fun forkReturnsNewSessionIdAndClearsBusyOnCompletionAndFailure() = runTest {
        val api = FakeOperations()
        val operations = AgentOperationsState(api)
        val session = Session("machine", "orig", status = "idle", canFork = true)
        assertEquals("fork-orig", operations.fork(session))
        assertEquals(listOf(session.key), api.forked)
        assertFalse(operations.state.value.busy)
        api.fail = true
        try { operations.fork(session); fail("Expected fork failure") } catch (_: AgentOperationFailure) {}
        assertFalse(operations.state.value.busy)
    }
}
