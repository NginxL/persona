package persona.feature.inbox

import persona.core.domain.inbox.InboxBar
import persona.core.domain.inbox.InboxItem
import persona.core.domain.inbox.InboxListSettings
import persona.core.domain.inbox.buildInboxList
import org.junit.Assert.assertEquals
import org.junit.Test
import persona.core.inbox.*

/** Pending counts match "Needs you" without including work already taken by the AI twin. */
class InboxNeedsYouTest {
    private fun record(id: String, state: String? = null, tier: String = "action") = InboxRecord(
        InboxItem(id, kind = "dm", title = id, tier = tier, updatedAt = "2026-11-02T10:00:00Z"),
        agent = state?.let { InboxAgent(state = it) },
    )

    private val records = listOf(
        record("reply"), record("running", "running"), record("queued", "queued"),
        record("drafted", "drafted"), record("failed", "failed"), record("fyi-running", "running", tier = "fyi"),
    )

    @Test fun rowsTheAiIsOnAreNotTheUsers() {
        assertEquals(listOf(false, true, true, false, false, true), records.map { it.agentWorking })
        val list = buildInboxList(records.map { it.item })
        assertEquals("a reply, a draft to look at, a failed hand-off", 3, needsYouCount(list, records))
    }

    @Test fun thePendingCountsMatchThePage() {
        val list = buildInboxList(records.map { it.item })
        val byId = records.associateBy { it.item.id }
        val page = needsYouCount(list) { byId[it.id]?.agentWorking == true }
        assertEquals(page, needsYouCount(list, records))
    }

    @Test fun theSectionBarCountsTheSameNeedsYou() {
        // #9053: the bar's Needs you counts the rows the list shows there, the AI twin's In progress (DESIGN_SYSTEM 4.8).
        val list = buildInboxList(records.map { it.item })
        val byId = records.associateBy { it.item.id }
        val shown = inboxDisplayGroups(list, persona.core.contracts.CoreText.plain("In progress")) { byId[it.id]?.agentWorking == true }
        val bar = InboxBar.bar(shown, InboxBar.ACTION, InboxListSettings())
        assertEquals(needsYouCount(list, records), bar.choices.first { it.id == InboxBar.ACTION }.count)
        assertEquals(3, bar.choices.first { it.id == InboxBar.WORKING }.count)
    }

    @Test fun aHubThatSaysInProgressCountsTheSame() {
        // Inbox phase 1: the hub's `status` puts an item In progress though it was triaged action.
        val events = listOf(
            InboxRecord(InboxItem("mine", kind = "dm", tier = "action", status = "action", updatedAt = "2026-11-02T10:00:00Z")),
            InboxRecord(InboxItem("hub", kind = "dm", tier = "action", status = "working", updatedAt = "2026-11-02T09:00:00Z")),
        )
        assertEquals(listOf(false, true), events.map { it.agentWorking })
        assertEquals(1, needsYouCount(buildInboxList(events.map { it.item }), events))
    }
}
