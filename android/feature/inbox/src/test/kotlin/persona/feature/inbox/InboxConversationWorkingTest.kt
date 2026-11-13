package persona.feature.inbox

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.CoreText
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.InboxListResult
import persona.core.domain.inbox.InboxListSettings
import persona.core.domain.inbox.buildInboxList

/**
 * Rebuilding the list around paired conversation rows keeps the hub's In progress section: its place between Needs you
 * and FYI, always open, one row per item, and gone when the filter leaves it empty.
 */
class InboxConversationWorkingTest {
    private val readPending = CoreText.plain("Read pending")

    @Test fun workingKeepsItsPlaceAndStaysOpenWhileOtherSectionsFollowSettings() {
        val records = listOf(
            record("noise", tier = "noise", status = "noise"),
            record("working", tier = "fyi", status = InboxItem.WORKING),
            record("done", tier = "action", status = "action", doneAt = "2026-11-13T01:00:00Z"),
        )
        val settings = InboxListSettings(expanded = emptySet())
        val view = conversationInboxView(original(records, settings), InboxConversationList(), settings, emptyMap(), readPending)

        assertEquals(listOf(InboxItem.WORKING, "noise", "done"), view.list.groups.map { it.id })
        val working = view.list.groups.single { it.id == InboxItem.WORKING }
        assertFalse(working.folded)
        assertEquals(listOf("working"), working.visibleRows.map { it.id })
        // Every other section still folds as the settings say, so the working change did not leak into them.
        assertTrue(view.list.groups.single { it.id == "noise" }.folded)
        assertTrue(view.list.groups.single { it.id == "noise" }.visibleRows.isEmpty())
    }

    @Test fun aPairedConversationTakesItsWorkingItemOverExactlyOnce() {
        val records = listOf(
            record("room-1", tier = "action", status = InboxItem.WORKING, feedId = "room"),
            record("mail-working", tier = "fyi", status = InboxItem.WORKING),
        )
        val conversations = buildInboxConversations("paired", InboxSnapshot(records = records))
        val view = conversationInboxView(original(records), conversations, InboxListSettings(), emptyMap(), readPending)

        assertEquals(1, view.conversations.size)
        // The chat's row replaces its item everywhere: a leaving item must not stay behind under In progress as well.
        assertEquals(1, view.list.groups.flatMap { it.rows }.count { it.id == "room-1" })
        assertEquals(listOf("mail-working"), view.list.groups.single { it.id == InboxItem.WORKING }.rows.map { it.id })
    }

    @Test fun aFilterThatHidesTheWorkingItemLeavesNoEmptyInProgressSection() {
        val records = listOf(
            record("working", tier = "fyi", status = InboxItem.WORKING, account = "mail-1"),
            record("other", tier = "fyi", status = "fyi", account = "mail-2"),
        )
        val settings = InboxListSettings(source = "mail-2")
        val view = conversationInboxView(original(records, settings), InboxConversationList(), settings, emptyMap(), readPending)

        assertNull(view.list.groups.firstOrNull { it.id == InboxItem.WORKING })
        assertEquals(listOf("other"), view.list.groups.flatMap { it.rows }.map { it.id })
    }

    @Test fun aHubWithoutStatusOnlyKeepsWhatItListed() {
        val records = listOf(
            record("action", tier = "action", status = null),
            record("fyi", tier = "fyi", status = null),
        )
        val settings = InboxListSettings(expanded = setOf("action", "fyi"))
        val view = conversationInboxView(original(records, settings), InboxConversationList(), settings, emptyMap(), readPending)

        // An older hub sends no `status`: no In progress section appears out of nothing.
        assertEquals(listOf("action", "fyi"), view.list.groups.map { it.id })
        assertTrue(view.conversations.isEmpty())
    }

    private fun original(records: List<InboxRecord>, settings: InboxListSettings = InboxListSettings()): InboxListResult =
        buildInboxList(records.map { it.item }, settings = settings)

    private fun record(
        id: String, tier: String, status: String?, feedId: String? = null,
        account: String = "account", doneAt: String? = null,
    ) = InboxRecord(
        InboxItem(id, account = account, kind = if (feedId == null) "mail" else "group", title = id,
            tier = tier, status = status, updatedAt = "2026-11-13T00:00:00Z", doneAt = doneAt),
        source = if (feedId == null) "mail" else "lark",
        feedId = feedId,
    )
}
