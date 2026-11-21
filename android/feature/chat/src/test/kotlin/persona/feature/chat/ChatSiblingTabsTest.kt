package persona.feature.chat

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.Session

/** The chat's strip of its space's tabs (iOS `AppModel.siblings`, `Session.slotLabel`). */
class ChatSiblingTabsTest {
    private fun s(id: String, space: String? = "ws1", slot: Int? = null, name: String? = null, pane: String? = "p-$id",
                  status: String = "idle", at: String = "2026-11-04T08:00:00Z", machine: String = "mac", assistant: Boolean = false) =
        Session(machineId = machine, sessionId = id, spaceId = space, slotIndex = slot, slotName = name, paneId = pane,
            status = status, updatedAt = at, assistant = assistant)

    @Test fun theSameSpacesLiveTabsInTabOrder() {
        val a = s("a", slot = 1)
        val all = listOf(
            s("b", slot = 2), a, s("c", slot = 3, status = "offline"), // offline: not a tab
            s("d", space = "ws2", slot = 1), s("e", machine = "box2", slot = 1), // another space, another machine
            s("j", slot = 0, assistant = true), // the assistant is never a tab
        )
        assertEquals(listOf("a", "b"), spaceTabs(a, all).map { it.sessionId })
        // The session itself counts even before the list has it.
        assertEquals(listOf("a", "b"), spaceTabs(a, all - a).map { it.sessionId })
        assertTrue(spaceTabs(s("x", space = null), listOf(s("y", space = null))).isEmpty())
        assertTrue(spaceTabs(null, all).isEmpty())
    }

    @Test fun onePerPaneTheOpenOneThenTheLatest() {
        // /clear left the pane listed under its old id for a moment.
        val old = s("old", slot = 1, pane = "p1", at = "2026-11-04T08:00:00Z")
        val new = s("new", slot = 1, pane = "p1", at = "2026-11-04T09:00:00Z")
        val other = s("other", slot = 2, pane = "p2")
        assertEquals(listOf("new", "other"), spaceTabs(other, listOf(old, new, other)).map { it.sessionId })
        assertEquals(listOf("old", "other"), spaceTabs(old, listOf(old, new, other)).map { it.sessionId })
    }

    @Test fun tabNames() {
        assertEquals("api", tabLabel(s("a", slot = 1, name = "api")))
        assertEquals("#2", tabLabel(s("a", slot = 2, name = "2"))) // herdr's default number
        assertEquals("#3", tabLabel(s("a", slot = 3)))
        assertEquals("a", tabLabel(Session(sessionId = "a", title = "a")))
    }

    @Test fun aTabsAgentsSitTogetherInPaneOrderTheLauncherMarked() {
        fun pane(id: String, slot: Int, place: Int?, agent: String = "claude") =
            Session(machineId = "mac", sessionId = id, spaceId = "ws1", slotId = "t$slot", slotIndex = slot, paneId = "w1:$id",
                paneIndex = place, agent = agent, status = "working")
        val lead = pane("lead", 1, 0)
        val all = listOf(pane("solo", 2, 0), pane("review", 1, 1, agent = "codex"), lead, pane("tests", 1, 2))
        val tabs = spaceTabs(lead, all)
        assertEquals(listOf("lead", "review", "tests", "solo"), tabs.map { it.sessionId })
        val labels = siblingTabs(tabs)
        // two of one kind told apart by their place
        assertEquals(listOf("#1 Claude 1", "#1 Codex", "#1 Claude 2", "#2"), labels.map { it.name })
        assertEquals(listOf(true, false, false, false), labels.map { it.launcher })
        // the top left pane is a shell: peers, none marked
        val peers = siblingTabs(spaceTabs(pane("a", 3, 1), listOf(pane("a", 3, 1), pane("b", 3, 2))))
        assertEquals(listOf(false, false), peers.map { it.launcher })
        // a shared tab named after a long task: its chips say the tab's number, not the name, so the row is not one chip
        val named = listOf(lead.copy(slotName = "一个很长很长的任务标题，被 herdr 拿来当标签页的名字"), pane("review", 1, 1, agent = "codex"))
        assertEquals(listOf("#1 Claude", "#1 Codex"), siblingTabs(spaceTabs(named[0], named)).map { it.name })
    }

    @Test fun workspaceIdOnlyAndFoldedSideOrWorkerRowsAreNotSiblingTabs() {
        val parent = Session(machineId = "mac", sessionId = "parent", workspaceId = "bytedance", status = "idle")
        val side = Session(machineId = "mac", sessionId = "side-1", workspaceId = "bytedance",
            foldedUnder = "parent", foldedAs = persona.core.contracts.FoldedAs.SIDE, status = "working")
        val unrelated = Session(machineId = "mac", sessionId = "other-chat", workspaceId = "bytedance", status = "working")
        assertTrue(spaceTabs(parent, listOf(parent, side, unrelated)).isEmpty())

        val lead = s("lead", space = "ws1", slot = 1, pane = "w1:p0")
        val peer = s("peer", space = "ws1", slot = 2, pane = "w1:p1")
        val worker = s("worker", space = "ws1", slot = 1, pane = "w1:p2").copy(
            foldedUnder = "lead", foldedAs = persona.core.contracts.FoldedAs.WORKER)
        assertEquals(listOf("lead", "peer"), spaceTabs(lead, listOf(lead, peer, worker)).map { it.sessionId })
    }
}
