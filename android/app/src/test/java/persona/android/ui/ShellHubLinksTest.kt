package persona.android.ui

import androidx.navigation3.runtime.NavKey
import org.junit.Assert.*
import org.junit.Test
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import persona.core.navigation.*
import persona.feature.knowledge.KnowledgeDetailRoute
import persona.feature.knowledge.KnowledgeKindRoute
import persona.feature.knowledge.KnowledgeRoute

class ShellHubLinksTest {
    private val hub = "https://hub.example.com"
    private val opened = mutableListOf<NavKey>()
    private val tabs = mutableListOf<AppTab>()
    private val navigator = object : Navigator {
        override fun navigate(key: NavKey) { opened += key }
        override fun back() = Unit
        override fun selectTab(tab: AppTab) { tabs += tab }
    }
    private val assistant = AssistantSnapshot(SessionKey("hub-m", "main-1"), listOf(Session(machineId = "hub-m", sessionId = "side-1")))

    private fun open(url: String, namespace: String? = null, sessions: List<SessionKey> = emptyList(), snapshot: AssistantSnapshot = assistant) =
        openHubPage(url, listOf(hub), namespace, sessions, snapshot, navigator)

    @Test fun anAgentLinkOpensItsConversation() {
        assertTrue(open("$hub/s/m1/abc-1?view=chat"))
        assertEquals(listOf<NavKey>(ChatRoute("m1", "abc-1")), opened)
    }

    @Test fun aLinkToAMessageLeavesItForItsChat() {
        MessageJumps.take("m1~gcafe00a1", "abc-123")
        val sessions = listOf(SessionKey("m1~gcafe00a1", "abc-123"))
        assertTrue(open("$hub/s/m1/abc?at=item%231", namespace = "gcafe00a1", sessions = sessions))
        assertEquals(listOf<NavKey>(ChatRoute("m1~gcafe00a1", "abc-123")), opened)
        assertEquals(MessageJumps.Jump("m1~gcafe00a1", "abc-123", "item#1"), MessageJumps.pending)
        assertTrue(open("$hub/persona/side-1?at=item-2"))
        assertEquals(MessageJumps.Jump("hub-m", "side-1", "item-2"), MessageJumps.pending)
        assertEquals("item-2", MessageJumps.take("hub-m", "side-1"))
        // no message: nothing waits
        assertTrue(open("$hub/s/m1/abc-1"))
        assertNull(MessageJumps.pending)
    }

    @Test fun anotherGroupsMachinesCarryItsSuffix() {
        assertTrue(open("$hub/s/m1/abc-1", namespace = "gcafe00a1"))
        assertTrue(open("$hub/machines/m1", namespace = "gcafe00a1"))
        assertEquals(listOf(ChatRoute("m1~gcafe00a1", "abc-1"), MachineDetailRoute("m1~gcafe00a1")), opened)
    }

    @Test fun aShortSessionIdOpensTheOneSessionItNames() {
        val sessions = listOf(SessionKey("m1", "abc-123"), SessionKey("m1", "def-456"), SessionKey("m2", "abc-999"))
        assertTrue(open("$hub/s/m1/abc", sessions = sessions))
        // two sessions start so: the link's id as it is
        assertTrue(open("$hub/s/m1/abc", sessions = sessions + SessionKey("m1", "abc-777")))
        assertEquals(listOf(ChatRoute("m1", "abc-123"), ChatRoute("m1", "abc")), opened)
    }

    @Test fun assistantConversationsOpenInThePersonaHome() {
        assertTrue(open("$hub/persona/side-1"))
        assertTrue(open("$hub/persona/main-1"))
        assertTrue(open("$hub/"))
        // not listed yet: on the assistant's machine
        assertTrue(open("$hub/persona/new-2"))
        assertEquals(listOf(
            ChatRoute("hub-m", "side-1", ConversationKind.Assistant),
            ChatRoute("hub-m", "main-1", ConversationKind.Assistant),
            ChatRoute("hub-m", "main-1", ConversationKind.Assistant),
            ChatRoute("hub-m", "new-2", ConversationKind.Assistant),
        ), opened)
    }

    @Test fun beforeTheAssistantIsKnown() {
        assertTrue(open("$hub/persona", snapshot = AssistantSnapshot()))
        assertEquals(listOf(AppTab.Persona), tabs)
        // a conversation whose machine is not known yet stays a web page
        assertFalse(open("$hub/persona/side-1", snapshot = AssistantSnapshot()))
        assertTrue(opened.isEmpty())
    }

    @Test fun tasksAndPages() {
        assertTrue(open("$hub/todos/todo_1"))
        assertTrue(open("$hub/todos?id=todo_1"))
        assertTrue(open("persona://open/todos/todo_1"))
        assertTrue(open("persona://task/todo_1"))
        assertTrue(open("$hub/inbox/item-1"))
        assertTrue(open("$hub/agents"))
        assertEquals(listOf(
            TodoDetailRoute("todo_1"), TodoDetailRoute("todo_1"), TodoDetailRoute("todo_1"), TodoDetailRoute("todo_1"),
            InboxItemRoute("item-1"), AgentsRoute,
        ), opened)
    }

    @Test fun knowledgeEntriesOpenTheExistingReaderForEveryEquivalentLink() {
        val cases = listOf(
            KnowledgeDetailRoute(KnowledgeKind.Docs, "notes/plan.md") to listOf(
                "$hub/kb/docs/notes%2Fplan.md", "persona://open/kb/docs/notes%2Fplan.md", "persona://kb/docs/notes/plan.md",
            ),
            KnowledgeDetailRoute(KnowledgeKind.Meetings, "m-31") to listOf(
                "$hub/kb/meetings/m-31", "persona://open/kb/meetings/m-31",
            ),
            KnowledgeDetailRoute(KnowledgeKind.Recordings, "rec 5") to listOf(
                "$hub/kb/recordings/rec%205", "persona://open/kb/recordings/rec%205",
            ),
        )
        for ((route, links) in cases) for (link in links) assertOpens(link, route)
        assertOpens("$hub/kb", KnowledgeRoute)
        assertOpens("persona://open/kb", KnowledgeRoute)
    }

    @Test fun knowledgeKindOpensTheFilteredListAndColdKnowledgeUsesTheShellResolver() {
        for (link in listOf("$hub/kb/people", "persona://open/kb/people")) {
            assertOpens(link, KnowledgeKindRoute(KnowledgeKind.People))
        }
        val router = Router(hubLink = { link -> resolveHubRoute(link, listOf(hub), null, emptyList(), assistant) })
        val cold = router.parse(RouteInput.DeepLink("persona://open/kb/docs/notes%2Fplan.md"))
        assertEquals(AppTab.Persona, cold?.tab)
        assertEquals(KnowledgeDetailRoute(KnowledgeKind.Docs, "notes/plan.md"), cold?.key)
    }

    @Test fun explicitHubSelectsItsPairedGroup() {
        val groups = listOf(
            HubGroupInfo("group-a", "A", null, true, ConnectionState.Online,
                listOf(HubMemberInfo("hub-a", "A", paired = true))),
            HubGroupInfo("group-b", "B", "gb000001", false, ConnectionState.Online,
                listOf(HubMemberInfo("hub-b", "B", paired = true))),
        )
        val route = resolveHubRoute("persona://open/s/m1/abc-1?hub=hub-b", listOf(hub), null,
            emptyList(), assistant, groups = groups)
        assertEquals("group-b", route?.hubGroupId)
        assertEquals(ChatRoute("m1~gb000001", "abc-1"), route?.key)
    }

    @Test fun extensionPagesAndSettingsOpenTheExistingRoutesForEveryEquivalentLink() {
        val page = ExtensionUiRoute("weather", "today", "", state = "{\"d\":1}")
        for (link in listOf(
            "$hub/extension/weather/today?state=%7B%22d%22%3A1%7D",
            "$hub/plugin/weather/today?state=%7B%22d%22%3A1%7D",
            "persona://open/extension/weather/today?state=%7B%22d%22%3A1%7D",
            "persona://extension/weather/today?state=%7B%22d%22%3A1%7D",
        )) assertOpens(link, page)
        for (link in listOf(
            "$hub/settings?tab=extensions&extension=github",
            "persona://open/settings?tab=extensions&extension=github&add=1",
            "persona://settings/extensions/github",
        )) assertOpens(link, ExtensionRoute("github"))
    }

    @Test fun canonicalAndOlderAgentLinksKeepTheirMessageAnchor() {
        MessageJumps.take("m1", "abc-1")
        for (link in listOf(
            "persona://open/s/m1/abc-1?at=item%231&token=abc",
            "persona://session/m1/abc-1?at=item%231",
        )) {
            assertOpens(link, ChatRoute("m1", "abc-1"))
            assertEquals(link, MessageJumps.Jump("m1", "abc-1", "item#1"), MessageJumps.pending)
            assertEquals(link, "item#1", MessageJumps.take("m1", "abc-1"))
        }
    }

    private fun assertOpens(link: String, route: NavKey) {
        opened.clear()
        assertTrue(link, open(link))
        assertEquals(link, listOf(route), opened)
    }

    @Test fun otherLinksAreLeftToTheSheet() {
        assertFalse(open("https://other.example.com/s/m1/abc-1"))
        assertFalse(open("$hub/kb/spells/x"))
        assertFalse(open("https://hub.example.com:8443/s/m1/abc-1"))
        assertTrue(opened.isEmpty() && tabs.isEmpty())
    }
}
