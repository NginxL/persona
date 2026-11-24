package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.test.runTest
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.InboxEventFields
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.InboxEntry
import persona.core.domain.inbox.InboxBar
import persona.core.domain.inbox.InboxItem
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxItemUiRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.LarkChatRoute
import persona.core.navigation.TodosRoute
import persona.feature.inbox.ConnectorThreadRoute
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxItemDetailRoute
import persona.feature.inbox.MailRoute
import persona.feature.inbox.InboxItemPages
import persona.feature.inbox.InboxLanding
import persona.core.inbox.InboxRecord
import persona.feature.inbox.InboxRepository
import persona.core.inbox.InboxSnapshot
import persona.core.inbox.LarkConversation
import persona.core.inbox.LarkPart
import persona.feature.inbox.LarkRoute
import persona.core.inbox.ReplyDraft
import persona.core.inbox.ReplyRequest
import persona.core.inbox.ReplyTarget
import java.io.IOException

/** Deep links resolve a historical item through the same lookup and states as the event page. */
class InboxHistoryLinksTest {
    private fun record(event: Boolean) = InboxRecord(
        InboxItem("history", kind = "dm", title = "Historical event", status = if (event) "action" else null),
        feedId = "room", source = "lark", event = if (event) InboxEventFields(status = "action") else InboxEventFields.None,
    )

    private class LookupInbox(var record: InboxRecord?, var failure: IOException? = null) : InboxRepository {
        override val entries = MutableStateFlow(emptyList<InboxEntry>())
        override val snapshot = MutableStateFlow(InboxSnapshot())
        override val connectionState = flowOf(ConnectionState.Online)
        val calls = mutableListOf<String>()
        override suspend fun item(itemId: String): InboxRecord? {
            calls += itemId
            failure?.let { throw it }
            return record
        }
        override suspend fun refresh() = Unit
        override fun conversation(feedId: String, startPosition: Long?) = flowOf(LarkConversation())
        override suspend fun loadOlder(feedId: String) = Unit
        override suspend fun refreshConversation(feedId: String) = Unit
        override suspend fun setDone(itemId: String, done: Boolean) = Unit
        override suspend fun readDraft(target: ReplyTarget): ReplyDraft? = null
        override suspend fun saveDraft(target: ReplyTarget, draft: ReplyDraft) = Unit
        override suspend fun reply(target: ReplyTarget, request: ReplyRequest) = Unit
        override suspend fun downloadFile(file: LarkPart.File): String = error("not used")
    }

    private suspend fun resolve(key: NavKey, inbox: LookupInbox, records: List<InboxRecord> = emptyList(), loaded: Boolean = true) =
        inboxDestinationWithLookup(key, records, loaded, InboxItemPages(), inbox)

    @Test fun aListedEventNeedsNoSingleRead() = runTest {
        val inbox = LookupInbox(null, IOException("must not be called"))
        assertEquals(InboxDestination.Open(InboxEventRoute("history")), resolve(InboxItemRoute("history"), inbox, listOf(record(true))))
        assertTrue(inbox.calls.isEmpty())
    }

    @Test fun aHistoricalEventOpensItsEventPage() = runTest {
        val inbox = LookupInbox(record(true))
        assertEquals(InboxDestination.Open(InboxEventRoute("history")), resolve(InboxItemRoute("history"), inbox))
        assertEquals(listOf("history"), inbox.calls)
    }

    @Test fun aHistoricalChatItemWithoutEventsOpensItsCollapsedPage() = runTest {
        val inbox = LookupInbox(record(false))
        assertEquals(InboxDestination.Open(InboxEventRoute("history")), resolve(InboxItemRoute("history"), inbox))
    }

    @Test fun missingAndFailureStayDistinct() = runTest {
        assertEquals(InboxDestination.Missing, resolve(InboxItemRoute("history"), LookupInbox(null)))
        assertEquals(InboxDestination.Failed, resolve(InboxItemRoute("history"), LookupInbox(null, IOException("offline"))))
    }

    /** #9053: the item a link opens moves the Inbox list to where it is, listed, read by id, or a chat's current item. */
    @Test fun theItemALinkOpensLandsTheList() = runTest {
        val done = InboxRecord(InboxItem("history", kind = "dm", title = "Historical event", doneAt = "2026-11-01T10:00:00Z"),
            feedId = "room", source = "lark")
        val collapsed = InboxRecord(InboxItem("note", kind = "dm", title = "A notice", tier = "noise"), feedId = "notices", source = "lark")
        val landing = persona.feature.inbox.InboxLanding()
        inboxDestinationWithLookup(InboxItemRoute("history"), emptyList(), true, InboxItemPages(), LookupInbox(done), found = landing::land)
        assertEquals(persona.core.domain.inbox.InboxBar.Landing("done", false), landing.take())
        inboxDestinationWithLookup(InboxItemRoute("note"), listOf(collapsed), true, InboxItemPages(), LookupInbox(null), found = landing::land)
        assertEquals(persona.core.domain.inbox.InboxBar.Landing("all", true), landing.take())
        inboxDestinationWithLookup(LarkChatRoute("notices"), listOf(collapsed), true, InboxItemPages(), LookupInbox(null), found = landing::land)
        assertEquals(persona.core.domain.inbox.InboxBar.Landing("all", true), landing.take())
        // Nothing found, nothing moves.
        inboxDestinationWithLookup(InboxItemRoute("gone"), emptyList(), true, InboxItemPages(), LookupInbox(null), found = landing::land)
        assertNull(landing.take())
    }

    /**
     * #9053: a stack that let go of its Inbox list (the drawer, a link home) drops the landing nobody took, so a list
     * entered later opens on Needs you; a stack still holding the list under the item keeps it for Back. (Putting a
     * choice back when a page opened beside the list closes: InboxSectionBarTest.)
     */
    @Test fun aLandingNobodyTookIsDropped() {
        val done = InboxRecord(InboxItem("history", kind = "dm", title = "Historical event", doneAt = "2026-11-01T10:00:00Z"),
            feedId = "room", source = "lark")
        val landings = persona.core.navigation.AppTab.entries.associateWith { persona.feature.inbox.InboxLanding().apply { land(done) } }
        settleLandings(mapOf(
            persona.core.navigation.AppTab.Persona to listOf(persona.core.navigation.AssistantRoute),
            persona.core.navigation.AppTab.Agents to listOf(persona.core.navigation.AgentsRoute, persona.core.navigation.InboxRoute, InboxItemRoute("history")),
        ), landings)
        assertNull(landings.getValue(persona.core.navigation.AppTab.Persona).pending)
        assertEquals(persona.core.domain.inbox.InboxBar.Landing("done", false), landings.getValue(persona.core.navigation.AppTab.Agents).pending)
    }

    @Test fun loadingWaitsAndAChatLinkSkipsTheItemLookup() = runTest {
        val inbox = LookupInbox(null, IOException("must not be called"))
        assertNull(resolve(InboxItemRoute("history"), inbox, loaded = false))
        assertEquals(InboxDestination.Open(LarkRoute("room")), resolve(LarkChatRoute("room"), inbox))
        assertTrue(inbox.calls.isEmpty())
    }

    /**
     * #9667: leaving the Inbox root for To-dos does not drop a landing the composed Inbox host still holds.
     * An empty host list still settles the visible stack, so a landing nobody took is dropped as before.
     */
    @Test fun leavingTheInboxHostKeepsTheLandingTheVisibleRootWouldDrop() {
        val done = InboxRecord(InboxItem("history", kind = "dm", title = "Historical event", doneAt = "2026-11-01T10:00:00Z"),
            feedId = "room", source = "lark")
        val landing = InboxLanding().apply { land(done) }
        val inboxHost = listOf(InboxRoute, InboxItemRoute("history"))
        val todos = listOf(TodosRoute)
        val conversation = listOf(AssistantRoute, InboxRoute)
        settleLandings(
            mapOf(AppTab.Persona to todos),
            mapOf(AppTab.Persona to landing),
            personaHosts = listOf(conversation, inboxHost, todos),
        )
        assertEquals(InboxBar.Landing("done", false), landing.pending)
        assertEquals(inboxHost, inboxHostStack(todos, listOf(conversation, inboxHost, todos)))
        assertEquals(conversation, inboxHostStack(conversation, listOf(conversation)))
        assertNull(inboxHostStack(listOf(AssistantRoute), listOf(listOf(AssistantRoute), todos)))

        landing.enter()
        settleLandings(
            mapOf(AppTab.Persona to todos),
            mapOf(AppTab.Persona to landing),
            personaHosts = listOf(listOf(AssistantRoute), listOf(InboxRoute)),
        )
        assertEquals("entering stays until the composed list takes it", InboxBar.Landing("", false), landing.pending)

        settleLandings(
            mapOf(AppTab.Persona to listOf(AssistantRoute)),
            mapOf(AppTab.Persona to landing),
            personaHosts = listOf(listOf(AssistantRoute), todos),
        )
        assertNull(landing.pending)
    }

    /** #9481 / #9667: inboxPicked (AssistantView.swift:633–640) and the drawer (AssistantView.swift:567–569). */
    @Test fun aSidebarReturnToTheInboxRootEntersAndAKeptItemLands() {
        val fyi = InboxRecord(InboxItem("note", kind = "dm", title = "Weekly note", tier = "fyi"), feedId = "room", source = "lark")
        val landing = InboxLanding()
        val entered = InboxBar.Landing("", false)
        inboxSidebarPicked(SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.None), landing, null)
        assertEquals(entered, landing.pending)
        inboxSidebarPicked(SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.PopToRoot), landing, fyi)
        assertEquals(entered, landing.pending)
        inboxSidebarPicked(SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.SwitchRoot), landing, fyi)
        assertEquals(entered, landing.pending)
        val kept = SidebarPageRoots.Step(InboxRoute, listOf(InboxEventRoute("note")), SidebarPageRoots.Move.SwitchRoot)
        inboxSidebarPicked(kept, landing, inboxRecordOnPath(kept.path, listOf(fyi), landing))
        assertEquals(InboxBar.Landing("fyi", false), landing.pending)
        inboxSidebarPicked(SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.DrillDown), landing, fyi)
        assertEquals(InboxBar.Landing("fyi", false), landing.pending)
        inboxSidebarPicked(SidebarPageRoots.Step(TodosRoute, emptyList(), SidebarPageRoots.Move.SwitchRoot), landing, fyi)
        assertEquals(InboxBar.Landing("fyi", false), landing.pending)
        inboxSidebarPicked(
            SidebarPageRoots.Step(InboxRoute, listOf(InboxItemRoute("missing")), SidebarPageRoots.Move.SwitchRoot),
            landing,
            null,
        )
        assertEquals("a kept path with no item enters", entered, landing.pending)

        val item = InboxItemRoute("note")
        assertEquals(
            SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.None),
            compactInboxPick(listOf(InboxRoute), inFront = true),
        )
        assertEquals(
            SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.PopToRoot),
            compactInboxPick(listOf(InboxRoute, item), inFront = true),
        )
        assertEquals(
            SidebarPageRoots.Step(InboxRoute, emptyList(), SidebarPageRoots.Move.SwitchRoot),
            compactInboxPick(null, inFront = false),
        )
        assertEquals(
            SidebarPageRoots.Step(InboxRoute, listOf(item), SidebarPageRoots.Move.SwitchRoot),
            compactInboxPick(listOf(AssistantRoute, InboxRoute, item), inFront = false),
        )
    }

    /** Item pages beyond the two route types, a page above the item, and a history row the snapshot lacks. */
    @Test fun aKeptItemPageIsFoundUnderADeeperRouteAndAHistoryRowIsRemembered() {
        val fyi = InboxRecord(InboxItem("note", kind = "dm", title = "Weekly note", tier = "fyi"), feedId = "room", source = "lark")
        val history = InboxRecord(InboxItem("old", kind = "dm", title = "Old note", tier = "fyi", doneAt = "2026-11-01T10:00:00Z"),
            feedId = "room", source = "lark")
        val landing = InboxLanding().apply { land(history) }
        val entered = InboxBar.Landing("", false)
        assertEquals(fyi, inboxRecordOnPath(listOf(InboxItemUiRoute("note", "ext", "page")), listOf(fyi), landing))
        assertEquals(fyi, inboxRecordOnPath(listOf(InboxItemDetailRoute("note")), listOf(fyi), landing))
        assertEquals(fyi, inboxRecordOnPath(listOf(ConnectorThreadRoute("note")), listOf(fyi), landing))
        assertEquals(fyi, inboxRecordOnPath(listOf(InboxEventRoute("note"), MailRoute("note")), listOf(fyi), landing))
        assertEquals(fyi, inboxRecordOnPath(
            listOf(InboxEventRoute("note"), ChatRoute("machine", "agent")),
            listOf(fyi),
            landing,
        ))
        assertEquals("the link's read, not the snapshot", history,
            inboxRecordOnPath(listOf(InboxItemDetailRoute("old")), emptyList(), landing))

        val historyStep = SidebarPageRoots.Step(InboxRoute, listOf(InboxItemDetailRoute("old")), SidebarPageRoots.Move.SwitchRoot)
        inboxSidebarPicked(historyStep, landing, inboxRecordOnPath(historyStep.path, emptyList(), landing))
        assertEquals(InboxBar.Landing("done", false), landing.pending)

        val chat = SidebarPageRoots.Step(
            InboxRoute,
            listOf(ChatRoute("machine", "waiting")),
            SidebarPageRoots.Move.SwitchRoot,
        )
        inboxSidebarPicked(chat, landing, inboxRecordOnPath(chat.path, listOf(fyi), landing))
        assertEquals("a waiting-agent chat is not an item", entered, landing.pending)
    }

    /** A compact Inbox host is [Assistant, Inbox, …]. It wins over a conversation list that also holds one. */
    @Test fun aCompactInboxHostBeatsTheConversationStack() {
        val place = listOf(AssistantRoute, InboxRoute, InboxEventRoute("note"))
        val conversation = listOf(AssistantRoute, InboxRoute, MailRoute("other"))
        val todos = listOf(TodosRoute)
        assertEquals(place, inboxHostStack(todos, listOf(conversation, place)))
        val regular = listOf(InboxRoute, InboxItemRoute("note"))
        assertEquals(regular, inboxHostStack(todos, listOf(conversation, place, regular)))
        val landing = InboxLanding().apply {
            land(InboxRecord(InboxItem("note", kind = "dm", title = "Weekly note", tier = "fyi"), feedId = "room", source = "lark"))
        }
        settleLandings(
            mapOf(AppTab.Persona to todos),
            mapOf(AppTab.Persona to landing),
            personaHosts = listOf(conversation, place),
        )
        assertEquals(InboxBar.Landing("fyi", false), landing.pending)
    }
}
