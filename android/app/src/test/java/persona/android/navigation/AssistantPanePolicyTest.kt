package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertSame
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.KnowledgeKind
import persona.core.navigation.AddComputerRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.EngineRoute
import persona.core.navigation.ExtensionUiRoute
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxItemUiRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.LarkChatRoute
import persona.core.navigation.LauncherRoute
import persona.core.navigation.MachineDetailRoute
import persona.core.navigation.MachinesRoute
import persona.core.navigation.RouteInput
import persona.core.navigation.Router
import persona.core.navigation.restore
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.SettingsRoute
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.TodosRoute
import persona.core.navigation.tabRoot
import persona.feature.inbox.ConnectorThreadRoute
import persona.feature.inbox.InboxEventRoute
import persona.feature.inbox.InboxItemDetailRoute
import persona.feature.inbox.LarkMomentRoute
import persona.feature.inbox.LarkRoute
import persona.feature.inbox.MailRoute
import persona.feature.knowledge.KnowledgeDetailRoute
import persona.feature.knowledge.KnowledgeKindRoute
import persona.feature.knowledge.KnowledgeRoute

class AssistantPanePolicyTest {
    private fun policy(
        stack: List<NavKey>,
        tab: AppTab = AppTab.Persona,
        regular: Boolean = true,
        widePhone: Boolean = false,
    ) = assistantPanePolicy(tab, stack, regular, widePhone)

    private fun windowPolicy(width: Float, height: Float, stack: List<NavKey>): AssistantPanePolicy {
        val regular = isRegularWindow(width, height)
        return policy(stack, regular = regular, widePhone = !regular && listsBesideDetails(width, height))
    }

    private fun assertSingleRightStack(stack: List<NavKey>) {
        val policy = policy(stack)
        assertTrue(stack.toString(), policy.keepSharedSidebar)
        assertTrue(stack.toString(), policy.singleRightStack)
        assertFalse(stack.toString(), policy.keepWidePhoneListDetail)
    }

    @Test
    fun regularAssistantDestinationsKeepTheSidebarAndOneRightStack() {
        val destinations = listOf(
            InboxRoute,
            LarkRoute("feed", "item"),
            LarkMomentRoute("post", "item"),
            MailRoute("item"),
            InboxItemRoute("item"),
            InboxItemUiRoute("item", "extension", "page"),
            LarkChatRoute("chat"),
            InboxItemDetailRoute("item"),
            InboxEventRoute("item"),
            ConnectorThreadRoute("item"),
            MachinesRoute,
            MachineDetailRoute("machine"),
            AddComputerRoute(),
            EngineRoute("machine"),
            LauncherRoute("machine", "launcher"),
            KnowledgeRoute,
            KnowledgeKindRoute(KnowledgeKind.People),
            KnowledgeDetailRoute(KnowledgeKind.Projects, "project"),
            TodosRoute,
            TodoDetailRoute("todo"),
            ExtensionUiRoute("extension", "page", "Page"),
        )
        destinations.forEach { destination -> assertSingleRightStack(listOf(AssistantRoute, destination)) }
        assertSingleRightStack(listOf(AssistantRoute, InboxRoute, MailRoute("item")))
        assertSingleRightStack(listOf(AssistantRoute, MachinesRoute, MachineDetailRoute("machine")))
        assertSingleRightStack(listOf(AssistantRoute, KnowledgeRoute, KnowledgeDetailRoute(KnowledgeKind.Projects, "project")))
        assertSingleRightStack(listOf(AssistantRoute, KnowledgeKindRoute(KnowledgeKind.People), KnowledgeDetailRoute(KnowledgeKind.People, "person")))
        assertSingleRightStack(listOf(AssistantRoute, TodosRoute, TodoDetailRoute("todo")))
    }

    @Test
    fun aSidebarPlaceRootKeepsTheColumnWithoutASecondPane() {
        for (root in listOf(
            InboxRoute,
            TodosRoute,
            KnowledgeRoute,
            KnowledgeKindRoute(KnowledgeKind.Recordings),
            MachinesRoute,
            TodoDetailRoute("todo"),
            ExtensionUiRoute("extension", "page", "Page"),
        )) {
            val alone = policy(listOf(root))
            assertTrue(root.toString(), alone.keepSharedSidebar)
            assertTrue(root.toString(), alone.singleRightStack)
            assertFalse(policy(listOf(root), regular = false).keepSharedSidebar)
        }
        val drilled = policy(listOf(InboxRoute, MailRoute("item")))
        assertTrue(drilled.keepSharedSidebar)
        assertTrue(drilled.singleRightStack)
    }

    @Test
    fun assistantRootSettingsAndAgentsKeepTheirOwnPolicies() {
        val root = policy(listOf(AssistantRoute))
        assertTrue(root.keepSharedSidebar)
        assertFalse(root.singleRightStack)
        assertFalse(root.keepWidePhoneListDetail)

        listOf(
            listOf(AssistantRoute, SettingsRoute),
            listOf(AssistantRoute, SettingsRoute, SettingsPageRoute(SettingsPage.About)),
            listOf(AssistantRoute, InboxRoute, SettingsPageRoute(SettingsPage.About)),
        ).forEach { stack ->
            val settings = policy(stack)
            assertFalse(stack.toString(), settings.keepSharedSidebar)
            assertFalse(stack.toString(), settings.singleRightStack)
        }

        val agents = policy(listOf(AssistantRoute, InboxRoute, MailRoute("item")), tab = AppTab.Agents)
        assertFalse(agents.keepSharedSidebar)
        assertFalse(agents.singleRightStack)
        assertFalse(agents.keepWidePhoneListDetail)
    }

    @Test
    fun compactAndWidePhoneKeepTheOldNavigation() {
        val inbox = listOf(AssistantRoute, InboxRoute, MailRoute("item"))
        val compact = policy(inbox, regular = false)
        assertFalse(compact.keepSharedSidebar)
        assertFalse(compact.singleRightStack)
        assertFalse(compact.keepWidePhoneListDetail)

        val wide = policy(inbox, regular = false, widePhone = true)
        assertFalse(wide.keepSharedSidebar)
        assertFalse(wide.singleRightStack)
        assertTrue(wide.keepWidePhoneListDetail)

        val wideMachines = policy(listOf(AssistantRoute, MachinesRoute), regular = false, widePhone = true)
        assertFalse(wideMachines.keepSharedSidebar)
        assertFalse(wideMachines.singleRightStack)
        assertFalse(wideMachines.keepWidePhoneListDetail)
    }

    @Test
    fun windowBoundariesUseSizeRatherThanDeviceNames() {
        val inbox = listOf(AssistantRoute, InboxRoute, MailRoute("item"))
        assertFalse(windowPolicy(599f, 800f, inbox).singleRightStack)
        assertTrue(windowPolicy(600f, 800f, inbox).singleRightStack)
        assertFalse(windowPolicy(600f, 479f, inbox).singleRightStack)
        assertTrue(windowPolicy(600f, 480f, inbox).singleRightStack)
        assertFalse(windowPolicy(759f, 400f, inbox).keepWidePhoneListDetail)
        assertTrue(windowPolicy(760f, 400f, inbox).keepWidePhoneListDetail)
        assertTrue(windowPolicy(839f, 600f, inbox).singleRightStack)
        assertTrue(windowPolicy(840f, 600f, inbox).singleRightStack)
    }

    @Test
    fun inboxDetailsBackToTheListThenTheMainConversation() {
        val stacks = mutableMapOf<AppTab, MutableList<NavKey>>(AppTab.Persona to mutableListOf(AssistantRoute))
        var selected = AppTab.Persona
        val navigator = AppNavigator(stacks, { selected }, { selected = it })
        val stack = stacks.getValue(AppTab.Persona)
        stack.add(InboxRoute)
        for (detail in listOf(
            LarkRoute("feed", "item"),
            MailRoute("item"),
            InboxEventRoute("item"),
            ConnectorThreadRoute("item"),
        )) {
            navigator.navigate(detail)
            assertEquals(detail, stack.last())
            assertSingleRightStack(stack)
            navigator.back()
            assertEquals(InboxRoute, stack.last())
            assertSingleRightStack(stack)
        }
        navigator.back()
        assertEquals(listOf(AssistantRoute), stack)
        assertTrue(policy(stack).keepSharedSidebar)
        assertFalse(policy(stack).singleRightStack)
    }

    @Test
    fun sidebarListsAndTheirInternalDetailsShareOneBackStack() {
        val cases = listOf<Pair<NavKey, NavKey>>(
            MachinesRoute to MachineDetailRoute("machine"),
            KnowledgeRoute to KnowledgeDetailRoute(KnowledgeKind.Projects, "project"),
            TodosRoute to TodoDetailRoute("todo"),
        )
        for ((list, detail) in cases) {
            val stack = mutableListOf<NavKey>(AssistantRoute, list)
            var selected = AppTab.Persona
            val navigator = AppNavigator(mapOf(AppTab.Persona to stack), { selected }, { selected = it })
            navigator.navigate(detail)
            assertEquals(listOf(AssistantRoute, list, detail), stack)
            assertSingleRightStack(stack)
            navigator.back()
            assertEquals(listOf(AssistantRoute, list), stack)
            navigator.back()
            assertEquals(listOf(AssistantRoute), stack)
        }

        val extension = ExtensionUiRoute("extension", "page", "Page")
        val extensionStacks = mapOf<AppTab, MutableList<NavKey>>(AppTab.Persona to mutableListOf(AssistantRoute, extension))
        var selected = AppTab.Persona
        val navigator = AppNavigator(extensionStacks, { selected }, { selected = it })
        val extensionStack = extensionStacks.getValue(AppTab.Persona)
        val pushed = extension.copy(depth = 1)
        navigator.navigate(pushed)
        assertEquals(listOf(AssistantRoute, extension, pushed), extensionStack)
        assertSingleRightStack(extensionStack)
        navigator.back()
        assertEquals(listOf(AssistantRoute, extension), extensionStack)

        val deepLinkStacks = AppTab.entries.associateWith { mutableListOf<NavKey>(tabRoot(it)) }
        val deepLink = Router().parse(RouteInput.DeepLink("persona://open?target=extension&extension=extension&ui=page"))!!
        deepLink.restore(deepLinkStacks)
        assertEquals(listOf(AssistantRoute, deepLink.key), deepLinkStacks.getValue(AppTab.Persona))
        assertSingleRightStack(deepLinkStacks.getValue(AppTab.Persona))
    }

    @Test
    fun inboxDeepLinksRestoreTheListInsideTheSameAssistantStack() {
        for (link in listOf(
            "persona://open?target=inbox&item=example",
            "persona://open?target=lark&chat=example",
        )) {
            val stacks = mapOf<AppTab, MutableList<NavKey>>(AppTab.Persona to mutableListOf(AssistantRoute))
            val route = Router().parse(RouteInput.DeepLink(link))!!
            assertEquals(AppTab.Persona, route.restore(stacks))
            val stack = stacks.getValue(AppTab.Persona)
            assertEquals(3, stack.size)
            assertEquals(InboxRoute, stack[1])
            assertSingleRightStack(stack)
            stack.removeAt(stack.lastIndex)
            assertEquals(InboxRoute, stack.last())
            stack.removeAt(stack.lastIndex)
            assertEquals(AssistantRoute, stack.single())
        }
    }

    @Test
    fun windowChangesOnlyRecalculatePolicyAroundTheSameState() {
        val stack = mutableListOf<NavKey>(AssistantRoute, InboxRoute, MailRoute("item"))
        val viewModelStore = mutableMapOf<String, String>("inbox" to "store")
        val selected = stack.last()
        val draft = StringBuilder("reply draft")
        val filter = mutableMapOf("source" to "mail")
        val anchor = mutableListOf("item" to 17)

        val originalStack = stack
        val originalStore = viewModelStore
        for ((regular, widePhone) in listOf(true to false, false to false, false to true, true to false)) {
            policy(stack, regular = regular, widePhone = widePhone)
            assertSame(originalStack, stack)
            assertSame(originalStore, viewModelStore)
            assertSame(selected, stack.last())
            assertEquals("reply draft", draft.toString())
            assertEquals(mapOf("source" to "mail"), filter)
            assertEquals(listOf("item" to 17), anchor)
        }
    }
}
