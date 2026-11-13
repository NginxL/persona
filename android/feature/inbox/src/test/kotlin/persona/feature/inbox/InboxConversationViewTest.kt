package persona.feature.inbox

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.CoreText
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.InboxListResult
import persona.core.domain.inbox.InboxListSettings
import persona.core.domain.inbox.buildInboxList

class InboxConversationViewTest {
    @Test fun pairedConversationsKeepWorkingItemsInDomainOrder() {
        val records = listOf(
            record("action", tier = "action", status = "action"),
            record("working", tier = "action", status = InboxItem.WORKING),
            record("fyi", tier = "fyi", status = "fyi"),
        )
        val view = pairedView(records, InboxConversationList())

        assertEquals(listOf("action", InboxItem.WORKING, "fyi"), view.list.groups.map { it.id })
        assertEquals(listOf("working"), view.list.groups.single { it.id == InboxItem.WORKING }.rows.map { it.id })
    }

    @Test fun noPairedConversationsLeaveTheOriginalListUnchanged() {
        val records = listOf(
            record("action", tier = "action", status = "action"),
            record("working", tier = "action", status = InboxItem.WORKING),
        )
        val original = original(records)
        val view = conversationInboxView(original, null, InboxListSettings(), emptyMap(), fallbackTitle)

        assertEquals(original, view.list)
        assertTrue(view.conversations.isEmpty())
    }

    @Test fun workingStaysExpandedWhenSettingsDoNotExpandIt() {
        val records = listOf(record("working", tier = "action", status = InboxItem.WORKING))
        val settings = InboxListSettings(expanded = emptySet())
        val view = conversationInboxView(original(records, settings), InboxConversationList(), settings, emptyMap(), fallbackTitle)
        val working = view.list.groups.single { it.id == InboxItem.WORKING }

        assertFalse(working.folded)
        assertEquals(working.rows, working.visibleRows)
        assertEquals(listOf("working"), working.visibleRows.map { it.id })
    }

    @Test fun conversationRowsStillReplaceTheirItemsWithoutDuplicatingThem() {
        val records = listOf(
            record("conversation", tier = "action", status = "action", feedId = "room"),
            record("working", tier = "action", status = InboxItem.WORKING),
        )
        val conversations = buildInboxConversations("paired", InboxSnapshot(records = records))
        val view = pairedView(records, conversations)

        assertEquals(listOf("conversation"), view.conversations.map { it.itemId })
        assertEquals(listOf("conversation"), view.list.groups.single { it.id == "action" }.rows.map { it.id })
        assertEquals(listOf("working"), view.list.groups.single { it.id == InboxItem.WORKING }.rows.map { it.id })
        assertEquals(1, view.list.groups.flatMap { it.rows }.count { it.id == "conversation" })
    }

    private val fallbackTitle = CoreText.plain("Read pending")

    private fun original(records: List<InboxRecord>, settings: InboxListSettings = InboxListSettings()): InboxListResult =
        buildInboxList(records.map { it.item }, settings = settings)

    private fun pairedView(records: List<InboxRecord>, conversations: InboxConversationList): InboxConversationView =
        conversationInboxView(original(records), conversations, InboxListSettings(), emptyMap(), fallbackTitle)

    private fun record(id: String, tier: String, status: String, feedId: String? = null) = InboxRecord(
        InboxItem(id, account = "account", kind = if (feedId == null) "mail" else "group", title = id,
            tier = tier, status = status, updatedAt = "2026-11-13T00:00:00Z"),
        source = if (feedId == null) "mail" else "lark",
        feedId = feedId,
    )
}
