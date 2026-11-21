package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import persona.core.contracts.Session
import persona.core.contracts.SessionCapabilities
import persona.core.contracts.api.ConnectionState
import org.junit.Assert.*
import org.junit.Test
import persona.core.domain.sort.SessionRenamer
import persona.core.domain.sort.SessionRename
import persona.core.domain.sort.resumeCommand
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.test.*

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class ChatChromeTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    @OptIn(kotlinx.coroutines.ExperimentalCoroutinesApi::class)
    @Test fun renameSendsOnlyChangesGuardsDuplicateSavesAndReportsSuccessOrFailure() = runTest {
        val session = Session("machine", "session", title = "旧会话", paneId = "pane", slotName = "旧标签")
        val names = MutableStateFlow(listOf(session))
        val calls = mutableListOf<SessionRename>()
        val reply = CompletableDeferred<Unit>()
        var fail = false
        val renamer = object : SessionRenamer {
            override fun project(source: Flow<List<Session>>) = names
            override suspend fun rename(session: Session, title: String?, tabName: String?): Boolean {
                val edit = SessionRename(title, tabName)
                calls += edit
                names.value = listOf(edit.apply(session))
                try {
                    if (fail) error("Rejected")
                    reply.await()
                } catch (error: Exception) {
                    names.value = listOf(session)
                    throw error
                }
                return true
            }
        }
        val state = ChatChromeState(res, null, backgroundScope, {}, {}, renamer)
        state.rename(session, " 旧会话 ", " 旧标签 ")
        runCurrent()
        assertTrue(calls.isEmpty())
        state.rename(session, " 新会话 ", "")
        state.rename(session, "不能重复", "")
        runCurrent()
        assertTrue(state.busy)
        assertEquals(listOf(SessionRename(title = "新会话")), calls)
        assertEquals("新会话", names.value.single().displayTitle)
        reply.complete(Unit)
        runCurrent()
        assertFalse(state.busy)
        assertEquals("已重命名", state.notice)
        fail = true
        state.rename(session, "", " 新标签 ")
        runCurrent()
        assertEquals(SessionRename(tabName = "新标签"), calls.last())
        assertEquals(session, names.value.single())
        assertFalse(state.busy)
        assertNotNull(state.notice)
        assertNotEquals("已重命名", state.notice)
    }

    // #6294: the rename dialog's note speaks of the terminal's tab and Claude's /rename; a runtime's session has neither.
    @Test fun onlyASessionWithATerminalGetsTheRenameNote() {
        assertEquals(R.string.chat_chrome_rename_text, chatRenameNote(Session("m", "s", paneId = "w1:p1")))
        assertEquals(R.string.chat_chrome_rename_text_tern, chatRenameNote(Session("m", "s", paneId = "tern:1")))
        assertEquals(R.string.chat_chrome_rename_text, chatRenameNote(null))
        assertNull(chatRenameNote(Session("m", "s", agent = "claude", sourceKind = "runtime",
            capabilities = SessionCapabilities(read = true, send = true, rename = true))))
    }

    @Test fun companionsListATabsAgentsTogetherWithTheLauncherFirst() {
        // tab 1: this session (pane 1) split off from the launcher (pane 0), plus a third pane; tab 2: one agent
        fun pane(id: String, slot: Int, index: Int?, state: String = "idle") = Session(machineId = "machine",
            sessionId = id, spaceId = "space", slotId = "t$slot", slotIndex = slot, paneId = "w1:p$id", paneIndex = index, state = state)
        val current = pane("b", 1, 1)
        val launcher = pane("a", 1, 0)
        val third = pane("c", 1, 2, "needs_you")
        val other = pane("d", 2, 0, "working")
        val peers = spaceCompanions(current, listOf(other, third, current, launcher))
        // by tab, then by place: urgency no longer pulls a pane out of its tab's group
        assertEquals(listOf("a", "c", "d"), peers.map { it.sessionId })
        val places = companionPlaces(current, peers)
        assertEquals(CompanionPlace.Launcher, places[launcher.key])
        assertEquals(CompanionPlace.Branch, places[third.key])
        assertEquals(CompanionPlace.Alone, places[other.key])
        // the top-left pane a shell: the agents are peers, none marked
        val peersOnly = listOf(current.copy(paneIndex = 1), third)
        assertEquals(setOf(CompanionPlace.Alone), companionPlaces(current.copy(paneIndex = 1), peersOnly.drop(1)).values.toSet())
    }

    @Test fun companionsStayInTheSameMachineAndSpaceAndCommandsQuoteShellInput() {
        val current = Session(machineId = "machine", sessionId = "current", spaceId = "space", cwd = "/workspace/project")
        val peer = current.copy(sessionId = "peer")
        assertEquals(listOf(peer), spaceCompanions(current, listOf(current, peer,
            peer.copy(machineId = "other"), peer.copy(spaceId = "other"), peer.copy(assistant = true))))
        assertTrue(spaceCompanions(current.copy(spaceId = null), listOf(peer.copy(spaceId = null))).isEmpty())
        assertEquals("cd -- '/workspace/project' && codex resume 'session'", current.copy(agent = "codex", sessionId = "session").resumeCommand())
        assertEquals("cd -- '/workspace/it'\"'\"'s' && claude --resume 'session'\"'\"'id'",
            current.copy(cwd = "/workspace/it's", sessionId = "session'id").resumeCommand())
        assertTrue(spaceCompanions(
            current.copy(spaceId = null, workspaceId = "bytedance"),
            listOf(peer.copy(spaceId = null, workspaceId = "bytedance")),
        ).isEmpty())
        assertTrue(spaceCompanions(
            current.copy(spaceId = null, spaceName = "bytedance"),
            listOf(peer.copy(spaceId = null, spaceName = "bytedance")),
        ).isEmpty())
        val worker = peer.copy(sessionId = "worker", foldedUnder = "current")
        assertEquals(listOf(peer), spaceCompanions(current, listOf(current, peer, worker)))
        assertEquals("配对已失效", connectionTitle(res, ConnectionState.Revoked))
        assertNull(connectionTitle(res, ConnectionState.Online))
    }
}
