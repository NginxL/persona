package persona.android.navigation

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import androidx.navigation3.runtime.NavKey
import androidx.compose.runtime.mutableStateListOf
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.ExtensionUiRoute
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MachinesRoute
import persona.core.navigation.RouteSource
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.TodosRoute
import persona.feature.assistant.AssistantSidebarShade
import persona.core.contracts.api.KnowledgeKind
import persona.core.navigation.KnowledgeKindRoute
import persona.core.navigation.KnowledgeRoute

/** Mirrors apple/PersonaTests/SidebarPageRootsTests.swift (#9667, iOS #8359). */
class SidebarPageRootsTest {
    private val inbox = InboxRoute
    private val recordings = KnowledgeRoute
    private val agent = TodoDetailRoute("a1")

    private fun path(vararg ids: String) = ids.map { MachineDetailRoute(it) }

    @Test
    fun fromTheConversationAPageOrAnAgentReplacesTheRootWithoutAPush() {
        for (root in listOf(recordings, agent)) {
            val roots = SidebarPageRoots()
            val step = roots.select(root, emptyList())
            assertEquals(SidebarPageRoots.Move.SwitchRoot, step.move)
            assertFalse(step.animated)
            assertTrue(step.path.isEmpty())
            assertEquals(root, step.root)
            assertEquals(root, roots.current)
        }
    }

    @Test
    fun aKnowledgeKindStaysTheKnowledgePlace() {
        val kind = KnowledgeKindRoute(KnowledgeKind.Recordings)
        val roots = SidebarPageRoots()
        val step = roots.select(kind, emptyList())
        assertEquals(SidebarPageRoots.Move.SwitchRoot, step.move)
        assertEquals(kind, roots.current)
        assertEquals(kind, placeRootFor(kind))
        assertEquals(AssistantSidebarShade.Knowledge, sidebarShadeKey(kind))
        assertTrue(retainsSidebarComposition(kind))
        assertTrue(isSidebarPlaceRoot(kind))
    }

    @Test
    fun backToTheConversationIsASwitchNotAPop() {
        val roots = SidebarPageRoots()
        roots.select(inbox, emptyList())
        val step = roots.select(AssistantRoute, emptyList())
        assertEquals(SidebarPageRoots.Move.SwitchRoot, step.move)
        assertFalse(step.animated)
        assertEquals(AssistantRoute, step.root)
    }

    @Test
    fun drillingDownPushesWithAnimation() {
        val roots = SidebarPageRoots()
        roots.select(inbox, emptyList())
        val step = roots.drill(MachineDetailRoute("x"), emptyList())
        assertEquals(SidebarPageRoots.Move.DrillDown, step.move)
        assertTrue(step.animated)
        assertFalse(step.closesOverlay)
        assertEquals(inbox, step.root)
        assertEquals(path("x"), step.path)
    }

    @Test
    fun eachRootKeepsItsDrillDown() {
        val roots = SidebarPageRoots()
        roots.select(inbox, emptyList())
        roots.select(agent, path("item"))
        assertTrue(roots.keeps(inbox))
        roots.select(AssistantRoute, path("sub"))
        assertTrue(roots.keeps(agent))
        val back = roots.select(inbox, emptyList())
        assertEquals(SidebarPageRoots.Move.SwitchRoot, back.move)
        assertFalse(back.animated)
        assertEquals(path("item"), back.path)
        assertFalse(roots.keeps(inbox))
        assertEquals(path("sub"), roots.select(agent, emptyList()).path)
    }

    @Test
    fun theRootInFrontPickedAgainGoesToItsRoot() {
        val roots = SidebarPageRoots()
        roots.select(inbox, emptyList())
        val pop = roots.select(inbox, path("item"))
        assertEquals(SidebarPageRoots.Move.PopToRoot, pop.move)
        assertTrue(pop.animated)
        assertTrue(pop.path.isEmpty())
        assertTrue(pop.closesOverlay)
        val none = roots.select(inbox, emptyList())
        assertEquals(SidebarPageRoots.Move.None, none.move)
        assertFalse(none.animated)
        assertTrue(none.closesOverlay)
    }

    @Test
    fun anotherConversationStartsAtItsRootWithoutAPop() {
        val roots = SidebarPageRoots()
        val step = roots.select(AssistantRoute, path("sub"), contentChanged = true)
        assertEquals(SidebarPageRoots.Move.SwitchRoot, step.move)
        assertFalse(step.animated)
        assertTrue(step.path.isEmpty())
        assertFalse(roots.keeps(AssistantRoute))
    }

    @Test
    fun anotherAssistantForgetsEveryRoot() {
        val roots = SidebarPageRoots()
        roots.select(inbox, emptyList())
        roots.select(agent, path("a"))
        val step = roots.forget()
        assertEquals(AssistantRoute, step.root)
        assertTrue(step.path.isEmpty())
        assertFalse(step.animated)
        assertEquals(AssistantRoute, roots.current)
        assertFalse(roots.keeps(inbox))
    }

    @Test
    fun theShadedPlaceFollowsTheRoot() {
        assertEquals(AssistantSidebarShade.Inbox, sidebarShadeKey(inbox))
        assertEquals(AssistantSidebarShade.Knowledge, sidebarShadeKey(recordings))
        assertEquals(AssistantSidebarShade.None, sidebarShadeKey(agent))
        assertEquals(AssistantSidebarShade.Conversation, sidebarShadeKey(AssistantRoute))
        assertEquals(AssistantSidebarShade.Todos, sidebarShadeKey(TodosRoute))
        assertEquals(AssistantSidebarShade.Machines, sidebarShadeKey(MachinesRoute))
        assertEquals("extension:ext/page", sidebarShadeKey(ExtensionUiRoute("ext", "page", "Page")))
        assertEquals(AssistantSidebarShade.None, sidebarShadeKey(ExtensionUiRoute("ext", "page", "Page", depth = 1)))
    }

    @Test
    fun agentAndPersonaRootsUseOneDistinctShadeKey() {
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent)
        val otherAgent = ChatRoute("machine", "other-agent", ConversationKind.Agent)
        val agentKey = sidebarShadeKey(agent)

        assertFalse("an Agent root must not leave every conversation shaded", agentKey == AssistantSidebarShade.None)
        assertFalse("an Agent root is not the Persona conversation", agentKey == AssistantSidebarShade.Conversation)
        assertEquals(agentKey, sidebarShadeKey(agent.copy()))
        val roots = listOf(agent, AssistantRoute, InboxRoute, otherAgent)
        val keys = roots.map(::sidebarShadeKey)
        assertEquals("each picked root moves the one shade key: $keys", keys.size, keys.toSet().size)
    }

    @Test
    fun agentDrillDownFromMainKeepsTheConversationShadeRoot() {
        val roots = SidebarPageRoots()
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent)
        val step = roots.drill(agent, emptyList())

        assertEquals(SidebarPageRoots.Move.DrillDown, step.move)
        assertEquals(AssistantRoute, step.root)
        assertEquals(AssistantSidebarShade.Conversation, sidebarShadeKey(step.root))
        assertFalse(
            "selecting that Agent as a root must move the shade off Main chat",
            sidebarShadeKey(agent) == AssistantSidebarShade.Conversation ||
                sidebarShadeKey(agent) == AssistantSidebarShade.None,
        )
    }

    @Test
    fun visibleAgentRootAndCrossTabDrilldownChooseDifferentShadeRoots() {
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent)
        val agentShade = sidebarShadeKey(agent)

        assertEquals(agentShade, visibleSidebarShade(true, AppTab.Agents, AssistantRoute, agent, null))
        assertEquals(agentShade, visibleSidebarShade(false, AppTab.Agents, AssistantRoute, agent, null))
        assertEquals(AssistantSidebarShade.None, visibleSidebarShade(true, AppTab.Agents, AssistantRoute, AgentsRoute, null))
        assertEquals(AssistantSidebarShade.Conversation,
            visibleSidebarShade(true, AppTab.Agents, AssistantRoute, agent, AppTab.Persona))
        assertEquals(AssistantSidebarShade.Conversation,
            visibleSidebarShade(false, AppTab.Agents, AssistantRoute, agent, AppTab.Persona))
        assertEquals(AssistantSidebarShade.Inbox,
            visibleSidebarShade(true, AppTab.Agents, InboxRoute, agent, AppTab.Persona))
        assertEquals(AssistantSidebarShade.Conversation,
            visibleSidebarShade(true, AppTab.Persona, AssistantRoute, AssistantRoute, null))
        assertNull(visibleSidebarShade(false, AppTab.Persona, AssistantRoute, AssistantRoute, null))
    }

    @Test
    fun widthRewritesOnlyTheCompactPrefix() {
        val drilled = listOf(InboxRoute, MachineDetailRoute("x"))
        assertEquals(listOf(AssistantRoute) + drilled, compactPrefix(drilled))
        assertNull(compactPrefix(listOf(AssistantRoute, InboxRoute)))
        assertNull(compactPrefix(listOf(AssistantRoute)))
        assertEquals(drilled, regularPromotion(listOf(AssistantRoute) + drilled))
        assertNull(regularPromotion(listOf(AssistantRoute, MachineDetailRoute("x"))))
        assertNull(regularPromotion(listOf(InboxRoute)))
    }

    @Test
    fun thePhoneDrawerStillPushesOverTheConversation() {
        val stack = mutableListOf<NavKey>(AssistantRoute, MachineDetailRoute("kept"))
        pushCompactSidebarPlace(stack, InboxRoute)
        assertEquals(listOf(AssistantRoute, InboxRoute), stack)
        pushCompactSidebarPlace(stack, TodosRoute)
        assertEquals(listOf(AssistantRoute, TodosRoute), stack)
        pushCompactSidebarPlace(stack, TodosRoute)
        assertEquals(listOf(AssistantRoute, TodosRoute), stack)
    }

    @Test
    fun replacingTheStackKeepsTheSharedPrefix() {
        val stack = mutableListOf<NavKey>(InboxRoute, MachineDetailRoute("a"), MachineDetailRoute("b"))
        replaceBackStack(stack, listOf(InboxRoute))
        assertEquals(listOf(InboxRoute), stack)
        replaceBackStack(stack, listOf(TodosRoute, TodoDetailRoute("t")))
        assertEquals(listOf(TodosRoute, TodoDetailRoute("t")), stack)
        replaceBackStack(stack, listOf(TodosRoute, TodoDetailRoute("t")))
        assertEquals(listOf(TodosRoute, TodoDetailRoute("t")), stack)
    }

    @Test
    fun aRootSwapIsOnlyARegularPersonaStack() {
        assertTrue(personaRootSwapped(regular = true, persona = true, from = InboxRoute, to = TodosRoute))
        assertFalse(personaRootSwapped(regular = false, persona = true, from = InboxRoute, to = TodosRoute))
        assertFalse(personaRootSwapped(regular = true, persona = false, from = InboxRoute, to = TodosRoute))
        assertFalse(personaRootSwapped(regular = true, persona = true, from = InboxRoute, to = InboxRoute))
        assertFalse(personaRootSwapped(regular = true, persona = true, from = null, to = InboxRoute))
    }

    @Test
    fun aPlaceRootShowsTheSidebarButton() {
        assertTrue(showsPlaceRootLeading(regular = true, persona = true, primary = false, entryIsStackRoot = true))
        assertFalse(showsPlaceRootLeading(regular = false, persona = true, primary = false, entryIsStackRoot = true))
        assertFalse(showsPlaceRootLeading(regular = true, persona = true, primary = true, entryIsStackRoot = true))
        assertFalse(showsPlaceRootLeading(regular = true, persona = true, primary = false, entryIsStackRoot = false))
        assertFalse(showsPlaceRootLeading(regular = true, persona = false, primary = false, entryIsStackRoot = true))
    }

    @Test
    fun unfoldingPromotesOnlyAPlaceOpenedAsARoot() {
        assertNull(regularPromotion(listOf(AssistantRoute, TodoDetailRoute("from-chat"))) { false })
        assertEquals(
            listOf(InboxRoute, InboxItemRoute("i")),
            regularPromotion(listOf(AssistantRoute, InboxRoute, InboxItemRoute("i"))) { it == InboxRoute },
        )
        val roots = SidebarPageRoots()
        assertFalse(roots.openedAsRoot(TodoDetailRoute("from-chat")))
        roots.markOpenedAsRoot(InboxRoute)
        assertTrue(roots.openedAsRoot(InboxRoute))
        roots.forget()
        assertFalse(roots.openedAsRoot(InboxRoute))
    }

    @Test
    fun placeRoutesSwitchAndConversationDrillsDoNot() {
        assertEquals(InboxRoute, placeRootFor(InboxItemRoute("i")))
        assertEquals(InboxRoute, placeRootFor(InboxRoute))
        assertEquals(TodosRoute, placeRootFor(TodosRoute))
        assertEquals(KnowledgeRoute, placeRootFor(KnowledgeRoute))
        assertEquals(MachinesRoute, placeRootFor(MachinesRoute))
        assertNull(placeRootFor(TodoDetailRoute("from-chat")))
        assertNull(placeRootFor(MachineDetailRoute("m")))
        assertNull(placeRootFor(ExtensionUiRoute("ext", "page", "Page")))
        assertEquals(SidebarRootHosts.CONVERSATION, sidebarHostId(AssistantRoute))
        assertEquals("inbox", sidebarHostId(InboxRoute))
        assertEquals("todo:abc", sidebarHostId(TodoDetailRoute("abc")))
        assertEquals("extension:ext/page", sidebarHostId(ExtensionUiRoute("ext", "page", "Page")))
    }

    @Test
    fun anotherRootKeepsTheConversationList() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("i")))
        assertTrue(hosts.stack(SidebarRootHosts.CONVERSATION) === conversation)
        assertEquals(listOf(AssistantRoute), conversation.toList())
        hosts.activate(AssistantRoute, listOf(AssistantRoute))
        assertEquals(listOf(InboxRoute, InboxItemRoute("i")), hosts.stack("inbox")!!.toList())
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("i")))
        hosts.foldOntoConversation()
        // A fixed place stays on its own list: the conversation is inserted under it (InboxView stays composed).
        assertEquals(listOf(AssistantRoute), conversation.toList())
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("i")), hosts.stack("inbox")!!.toList())
        assertTrue(hosts.current === hosts.stack("inbox"))
        assertTrue(hosts.visited.contains(SidebarRootHosts.CONVERSATION))
        assertTrue(hosts.visited.contains("inbox"))
        assertTrue(hosts.unfoldPrefixedPlace())
        assertEquals(listOf(InboxRoute, InboxItemRoute("i")), hosts.current.toList())
        assertEquals(listOf(AssistantRoute), conversation.toList())
    }

    @Test
    fun aTodoRootDropsFromCompositionWhenLeftAndKeepsItsPath() {
        val todo = TodoDetailRoute("x")
        val drill = MachineDetailRoute("m")
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        roots.select(todo, emptyList())
        hosts.activate(todo, listOf(todo, drill))
        assertTrue(hosts.visited.contains("todo:x"))
        roots.select(AssistantRoute, listOf(drill))
        hosts.activate(AssistantRoute, listOf(AssistantRoute))
        assertNull(hosts.stack("todo:x"))
        assertFalse(hosts.visited.contains("todo:x"))
        assertEquals(listOf(SidebarRootHosts.CONVERSATION), hosts.visited.toList())
        assertEquals(listOf(drill), roots.select(todo, emptyList()).path)
    }

    @Test
    fun oneRouteIsNotComposedOnTheConversationAndOnItsOwnHost() {
        val todo = TodoDetailRoute("x")
        val conversation = mutableStateListOf<NavKey>(AssistantRoute, todo)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(todo, listOf(todo))
        assertEquals(listOf(AssistantRoute), conversation.toList())
        assertEquals(listOf(todo), hosts.current.toList())
    }

    @Test
    fun aTodoRootTrimsTheSameDetailOffTheTodosHost() {
        val todo = TodoDetailRoute("x")
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(TodosRoute, listOf(TodosRoute, todo))
        hosts.activate(todo, listOf(todo))
        assertEquals(listOf(TodosRoute), hosts.stack("todos")!!.toList())
        assertTrue(hosts.visited.contains("todos"))
        assertEquals(listOf(todo), hosts.current.toList())
        assertFalse(hosts.current.any { it == TodosRoute })
    }

    @Test
    fun inboxStaysComposedWhenTodosIsInFront() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("i")))
        hosts.activate(TodosRoute, listOf(TodosRoute))
        assertEquals(listOf(InboxRoute, InboxItemRoute("i")), hosts.stack("inbox")!!.toList())
        assertTrue(hosts.visited.contains("inbox"))
        assertTrue(hosts.visited.contains("todos"))
        assertEquals(listOf(AssistantRoute), conversation.toList())
    }

    @Test
    fun foldingATodoRootCopiesItOntoTheConversationAndDropsTheHost() {
        val todo = TodoDetailRoute("x")
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(todo, listOf(todo))
        hosts.foldOntoConversation()
        assertEquals(listOf(AssistantRoute, todo), conversation.toList())
        assertTrue(hosts.current === conversation)
        assertNull(hosts.stack("todo:x"))
        assertEquals(listOf(SidebarRootHosts.CONVERSATION), hosts.visited.toList())
    }

    @Test
    fun foldingATodoRootKeepsTheInboxHost() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("i")))
        val todo = TodoDetailRoute("x")
        hosts.activate(todo, listOf(todo))
        hosts.foldOntoConversation()
        assertEquals(listOf(AssistantRoute, todo), conversation.toList())
        assertTrue(hosts.current === conversation)
        assertNull(hosts.stack("todo:x"))
        assertEquals(listOf(InboxRoute, InboxItemRoute("i")), hosts.stack("inbox")!!.toList())
        assertTrue(hosts.visited.contains("inbox"))
        assertFalse(hosts.unfoldPrefixedPlace())
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("i")))
        assertTrue(hosts.current === hosts.stack("inbox"))
        assertEquals(listOf(InboxRoute, InboxItemRoute("i")), hosts.current.toList())
    }

    @Test
    fun unfoldingStripsAPrefixLeftOnAHostThatIsNotInFront() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("i")))
        hosts.foldOntoConversation()
        hosts.activate(TodosRoute, listOf(TodosRoute))
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("i")), hosts.stack("inbox")!!.toList())
        assertFalse(hosts.unfoldPrefixedPlace())
        assertEquals(listOf(InboxRoute, InboxItemRoute("i")), hosts.stack("inbox")!!.toList())
        assertEquals(listOf(TodosRoute), hosts.current.toList())
    }

    @Test
    fun aCompactRouteToARetainedInboxHostDoesNotPushASecondCopy() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow()
        window.noteSize(400f, 800f)
        hosts.activate(InboxRoute, listOf(InboxRoute, InboxItemRoute("old")))
        val opened = window.openPlace(InboxItemRoute("new"), roots, hosts, { root, next ->
            hosts.activate(root, next)
        }) {}
        assertTrue(opened)
        assertNull(window.pending)
        assertEquals(listOf(AssistantRoute), conversation.toList())
        assertEquals(listOf(AssistantRoute, InboxRoute, InboxItemRoute("new")), hosts.current.toList())
    }

    @Test
    fun aMeasuredCompactOpenLeavesNothingPending() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow()
        window.noteSize(400f, 800f)
        val opened = window.openPlace(InboxItemRoute("stale"), roots, hosts, { _, _ -> }) {}
        assertFalse(opened)
        assertNull(window.pending)
        window.noteSize(951f, 673f)
        window.onWidth(true, hosts, conversation, roots, { root, next -> hosts.activate(root, next) }) {}
        assertEquals(listOf(AssistantRoute), hosts.current.toList())
        assertNull(hosts.stack("inbox"))
    }

    @Test
    fun anUnmeasuredOpenIsAppliedOnTheFirstRegularFrame() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow()
        val opened = window.openPlace(InboxItemRoute("fresh"), roots, hosts, { _, _ -> }) {}
        assertFalse(opened)
        assertEquals(InboxItemRoute("fresh"), window.pending)
        window.noteSize(951f, 673f)
        assertEquals(InboxItemRoute("fresh"), window.pending)
        window.onWidth(true, hosts, conversation, roots, { root, next -> hosts.activate(root, next) }) {}
        assertNull(window.pending)
        assertEquals(listOf(AssistantRoute), conversation.toList())
        assertEquals(listOf(InboxRoute, InboxItemRoute("fresh")), hosts.current.toList())
    }

    @Test
    fun applyingAPendingInboxItemKeepsTheRestoredConversation() {
        val chat = ChatRoute("machine", "chat-x", ConversationKind.Agent)
        val conversation = mutableStateListOf<NavKey>(AssistantRoute, chat)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow()
        val opened = window.openPlace(InboxItemRoute("fresh"), roots, hosts, { _, _ -> }) {}
        assertFalse(opened)
        conversation.add(InboxRoute)
        conversation.add(InboxItemRoute("fresh"))
        window.noteSize(951f, 673f)
        window.onWidth(true, hosts, conversation, roots, { root, next -> hosts.activate(root, next) }) {}
        assertEquals(listOf(AssistantRoute, chat), conversation.toList())
        assertEquals(listOf(InboxRoute, InboxItemRoute("fresh")), hosts.current.toList())
    }

    @Test
    fun backFromAgentsAtRegularWidthUsesTheInboxHost() {
        val opened = regularInboxNavigator()
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item-1"))
        opened.agents.add(chat)
        opened.navigator.back()
        assertEquals(AppTab.Persona, opened.selected)
        assertEquals(listOf(AgentsRoute), opened.agents.toList())
        assertEquals(listOf(AssistantRoute), opened.conversation.toList())
        assertEquals(listOf(InboxRoute, InboxItemRoute("item-1")), opened.hosts.current.toList())
        assertTrue(opened.stacks.getValue(AppTab.Persona) === opened.hosts.current)
    }

    @Test
    fun restoringAnInboxAgentAtRegularWidthDoesNotPushUnderTheConversation() {
        val opened = regularInboxNavigator(chatTab = { AppTab.Persona })
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item-1"))
        opened.agents.add(chat)
        opened.navigator.restoreChatTabs()
        assertEquals(listOf(AgentsRoute), opened.agents.toList())
        assertEquals(listOf(AssistantRoute), opened.conversation.toList())
        assertEquals(listOf(InboxRoute, InboxItemRoute("item-1"), chat), opened.hosts.current.toList())
    }

    @Test
    fun restoringAnInboxAgentAtCompactWidthLandsOnTheRetainedHost() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow()
        window.noteSize(400f, 800f)
        hosts.activate(InboxRoute, listOf(InboxRoute))
        val agents = mutableStateListOf<NavKey>(AgentsRoute)
        val stacks = mutableMapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to conversation,
            AppTab.Agents to agents,
        )
        var selected = AppTab.Agents
        val navigator = AppNavigator(
            stacks, { selected }, { selected = it },
            chatTab = { AppTab.Persona },
            openPlace = { key ->
                openSidebarPlace(key, window, roots, hosts, { root, next ->
                    hosts.activate(root, next)
                    stacks[AppTab.Persona] = hosts.current
                }, { stacks[AppTab.Persona] = it }) { selected = AppTab.Persona }
            },
        )
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item-1"))
        agents.add(chat)
        navigator.restoreChatTabs()
        assertEquals(listOf(AgentsRoute), agents.toList())
        assertEquals(listOf(AssistantRoute), conversation.toList())
        assertTrue(stacks.getValue(AppTab.Persona) === hosts.current)
        assertEquals(
            listOf(AssistantRoute, InboxRoute, InboxItemRoute("item-1"), chat),
            hosts.current.toList(),
        )
    }

    private class RegularInbox(val conversation: androidx.compose.runtime.snapshots.SnapshotStateList<NavKey>,
        val hosts: SidebarRootHosts, val agents: androidx.compose.runtime.snapshots.SnapshotStateList<NavKey>,
        val stacks: MutableMap<AppTab, MutableList<NavKey>>) {
        var selected = AppTab.Agents
        lateinit var navigator: AppNavigator
    }

    private fun regularInboxNavigator(chatTab: (ChatRoute) -> AppTab? = { null }): RegularInbox {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow()
        window.noteSize(951f, 673f)
        val agents = mutableStateListOf<NavKey>(AgentsRoute)
        val stacks = mutableMapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to hosts.current,
            AppTab.Agents to agents,
        )
        val opened = RegularInbox(conversation, hosts, agents, stacks)
        fun show(root: NavKey, next: List<NavKey>) {
            hosts.activate(root, next)
            stacks[AppTab.Persona] = hosts.current
        }
        opened.navigator = AppNavigator(
            stacks, { opened.selected }, { opened.selected = it },
            chatTab = chatTab,
            openPlace = { key ->
                openSidebarPlace(key, window, roots, hosts, ::show, { stacks[AppTab.Persona] = it }) {
                    opened.selected = AppTab.Persona
                }
            },
            leavePlaceRoot = {
                window.leavePlaceRoot(opened.selected == AppTab.Persona, stacks.getValue(AppTab.Persona), roots, ::show)
            },
        )
        return opened
    }

    @Test
    fun aPlaceRouteAtRegularWidthDoesNotPushUnderAnotherRoot() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        hosts.activate(TodosRoute, listOf(TodosRoute))
        roots.adoptVisible(TodosRoute)
        roots.markOpenedAsRoot(TodosRoute)
        val todos = hosts.stack("todos")!!
        val stacks = mutableMapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to hosts.current,
            AppTab.Agents to mutableStateListOf(AgentsRoute),
        )
        val navigator = AppNavigator(stacks, { AppTab.Persona }, {}, openPlace = { key ->
            val place = placeRootFor(key) ?: return@AppNavigator false
            if (roots.current == place && key != place) return@AppNavigator false
            roots.markOpenedAsRoot(place)
            val step = roots.select(place, hosts.current.drop(1))
            val next = if (key == place) step.stack else listOf(place, key)
            hosts.activate(place, next)
            stacks[AppTab.Persona] = hosts.current
            true
        }, leavePlaceRoot = {
            if (hosts.current.size != 1 || hosts.current.first() == AssistantRoute) return@AppNavigator false
            val step = roots.select(AssistantRoute, emptyList())
            hosts.activate(step.root, step.stack)
            stacks[AppTab.Persona] = hosts.current
            true
        })
        navigator.navigate(InboxItemRoute("item-1"))
        assertEquals(listOf(TodosRoute), todos.toList())
        assertEquals(listOf(InboxRoute, InboxItemRoute("item-1")), hosts.current.toList())
        assertEquals(listOf(AssistantRoute), conversation.toList())
        navigator.back()
        assertEquals(listOf(InboxRoute), hosts.current.toList())
        navigator.back()
        assertEquals(listOf(AssistantRoute), hosts.current.toList())
        assertEquals(listOf(InboxRoute), hosts.stack("inbox")!!.toList())
    }

    @Test
    fun aDrillOnThePlaceInFrontStillPushes() {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        hosts.activate(InboxRoute, listOf(InboxRoute))
        roots.adoptVisible(InboxRoute)
        val stacks = mutableMapOf<AppTab, MutableList<NavKey>>(
            AppTab.Persona to hosts.current,
            AppTab.Agents to mutableStateListOf(AgentsRoute),
        )
        val navigator = AppNavigator(stacks, { AppTab.Persona }, {}, openPlace = { key ->
            val place = placeRootFor(key) ?: return@AppNavigator false
            if (roots.current == place && key != place) return@AppNavigator false
            true
        })
        navigator.navigate(InboxItemRoute("item-1"))
        assertEquals(listOf(InboxRoute, InboxItemRoute("item-1")), hosts.current.toList())
    }

    private fun personaRootSwapped(regular: Boolean, persona: Boolean, from: NavKey?, to: NavKey?): Boolean =
        regular && persona && from != null && to != null && from != to
}
