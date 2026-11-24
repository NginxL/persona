package persona.android.capsule

import org.junit.Assert.*
import org.junit.Test
import persona.core.domain.inbox.InboxItem
import persona.feature.inbox.*
import persona.core.inbox.*

class CapsuleInboxNoticesTest {
    private fun row(id: String = "room", version: String = "v1", message: String? = null,
        section: InboxConversationSection = InboxConversationSection.Action) = InboxConversationRow(
        InboxConversationKey("scope", "lark", "account", id), "Conversation", listOf(InboxRecord(
            InboxItem("item-$id", tier = "action", version = version), feedId = id)), section, "$version|$message",
        latestMessageId = message)
    private fun CapsuleInboxNotices.update(vararg rows: InboxConversationRow, ready: Boolean = true) =
        update(rows.toList(), ready) { it.thread }

    @Test fun initialVerifiedCatalogAndHydratingItsOldMessagesDoNotAlert() {
        val notices = CapsuleInboxNotices()
        assertTrue(notices.update(row(), ready = false).isEmpty())
        assertTrue(notices.update(row()).isEmpty())
        assertTrue(notices.update(row(message = "old-message")).isEmpty())
        assertTrue(notices.update(row(message = "old-message")).isEmpty())
    }
    @Test fun newItemAndNewMessageEdgesAlertOnceEvenWhenTheTextRepeats() {
        val notices = CapsuleInboxNotices()
        notices.update(row(message = "m1"))
        assertEquals("room", notices.update(row(message = "m2")).single().targetId)
        assertTrue(notices.update(row(message = "m2")).isEmpty())
        assertEquals("room", notices.update(row(version = "v2", message = "m2")).single().targetId)
        assertEquals("other", notices.update(row(version = "v2", message = "m2"), row("other")).single().targetId)
    }
    @Test fun successfulReadAndReverifyingTheSameMessageDoNotAlert() {
        val notices = CapsuleInboxNotices()
        notices.update(row(message = "m1"))
        assertTrue(notices.update(row(message = null)).isEmpty())
        assertTrue(notices.update(row(message = "m1")).isEmpty())
        assertEquals("other", notices.update(row(message = null), row("other", message = "n1")).single().targetId)
        assertEquals("room", notices.update(row(message = "m2"), row("other", message = "n1")).single().targetId)
        assertTrue(notices.update(row(message = "m3", section = InboxConversationSection.ReadPending), row("other", message = "n1")).isEmpty())
    }
}
