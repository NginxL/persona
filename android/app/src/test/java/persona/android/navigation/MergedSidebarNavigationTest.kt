package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import org.junit.Assert.assertEquals
import org.junit.Assert.assertSame
import org.junit.Test
import persona.core.navigation.*
import persona.feature.assistant.AssistantSidebarShade
import persona.feature.inbox.InboxLanding

class MergedSidebarNavigationTest {
    private val assistant = mutableListOf<NavKey>(AssistantRoute)
    private val firstAgent = ChatRoute("computer", "first", ConversationKind.Agent)
    private val secondAgent = ChatRoute("computer", "second", ConversationKind.Agent)
    private val agents = mutableListOf<NavKey>(AgentsRoute, firstAgent)
    private val stacks = mutableMapOf(AppTab.Persona to assistant, AppTab.Agents to agents)
    private var tab = AppTab.Agents
    private val navigator = AppNavigator(stacks, { tab }, { tab = it }, chatTab = { AppTab.Agents })

    @Test fun agentPickReplacesRootAndDrillDownBackKeepsTheSameRoute() {
        navigator.pickSidebarAgent(ChatRoute(firstAgent.machineId, firstAgent.sessionId))
        assertSame(firstAgent, agents.last())
        navigator.pickSidebarAgent(secondAgent)
        assertEquals(listOf(AgentsRoute, secondAgent), agents)
        navigator.navigate(TerminalRoute(secondAgent.machineId, secondAgent.sessionId))
        assertEquals(secondAgent, mergedVisibleRoot(tab, agents))
        navigator.back()
        assertSame(secondAgent, agents.last())
        navigator.back()
        assertEquals(listOf(AgentsRoute), agents)
    }

    @Test fun notificationAndTerminalUseTheExistingKeysAndReturnPaths() {
        val request = InteractionRoute(secondAgent, "pending")
        ResolvedRoute(AppTab.Agents, request).open(stacks)
        keepMergedAgentTarget(agents, request)
        assertEquals(listOf(AgentsRoute, request), agents)
        assertSame(request, mergedVisibleRoot(tab, agents))
        val terminal = TerminalRoute(secondAgent.machineId, secondAgent.sessionId)
        ResolvedRoute(AppTab.Agents, terminal).open(stacks)
        keepMergedAgentTarget(agents, terminal)
        assertEquals(listOf(AgentsRoute, request, terminal), agents)
    }

    @Test fun anExternalTerminalFromTheAgentsListKeepsOneAnchor() {
        val terminal = TerminalRoute("computer", "pane:example")
        agents[1] = terminal
        keepMergedAgentTarget(agents, terminal)
        assertEquals(listOf(AgentsRoute, terminal), agents)
        navigator.back()
        assertEquals(listOf(AgentsRoute), agents)
    }

    @Test fun compactMergedPlaceReplacesTheRootAndSeparateRetainsItsPush() {
        val hosts = SidebarRootHosts(assistant)
        val roots = SidebarPageRoots()
        val landing = InboxLanding()
        fun pick(useRootHost: Boolean) {
            applySidebarDestination(TodosRoute, useRootHost, roots, hosts, assistant, hosts::activate,
                { stacks[AppTab.Persona] = it }, landing, emptyList())
        }
        pick(useRootHost = false)
        assertEquals(listOf(AssistantRoute, TodosRoute), assistant)
        pick(useRootHost = true)
        assertEquals(listOf(TodosRoute), hosts.current)
        hosts.current.add(TodoDetailRoute("trip"))
        assertEquals(AssistantSidebarShade.Todos, sidebarShadeKey(mergedVisibleRoot(AppTab.Persona, hosts.current)))
        tab = AppTab.Persona
        navigator.back()
        assertEquals(listOf(TodosRoute), hosts.current)
    }

    @Test fun anAgentOpenedInsideAnotherPageKeepsTheSourceForBack() {
        tab = AppTab.Persona
        navigator.navigate(secondAgent)
        assertEquals(AppTab.Persona, navigator.sourceTab(secondAgent))
        assertSame(AgentsRoute, mergedVisibleRoot(tab, agents, agentDrillDown = { navigator.sourceTab(it) == AppTab.Persona }))
        navigator.back()
        assertEquals(AppTab.Persona, tab)
        assertSame(AssistantRoute, assistant.last())
    }
}
