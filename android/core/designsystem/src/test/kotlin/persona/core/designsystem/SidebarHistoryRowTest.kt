package persona.core.designsystem

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** The compact history row's activity mark follows iOS `RowStateMark.spins` (#6490). */
class SidebarHistoryRowTest {
    @Test fun ownWorkingConversationSpins() {
        assertTrue(sidebarHistoryRowSpins(Status.Working, working = false))
        assertTrue(sidebarHistoryRowSpins(Status.Working, working = true))
    }

    @Test fun idleLeadWithAgentsWorkingSpins() {
        assertTrue(sidebarHistoryRowSpins(Status.Idle, working = true))
        assertTrue(sidebarHistoryRowSpins(Status.Offline, working = true))
    }

    @Test fun needsYouKeepsItsDot() {
        assertFalse(sidebarHistoryRowSpins(Status.NeedsYou, working = true))
    }

    @Test fun unreadKeepsItsDot() {
        assertFalse(sidebarHistoryRowSpins(Status.Unread, working = true))
    }

    @Test fun idleRowWithoutWorkDoesNotSpin() {
        assertFalse(sidebarHistoryRowSpins(Status.Idle, working = false))
    }
}
