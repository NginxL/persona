package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.contracts.SessionKey
import persona.core.navigation.*
import persona.feature.inbox.InboxEventRoute
import org.junit.Assert.assertEquals
import org.junit.Test

class AppNavigatorTest {
    private val stacks = AppTab.entries.associateWith { mutableListOf<NavKey>(tabRoot(it)) }
    private var selected = AppTab.Agents
    private val navigator = AppNavigator(stacks, { selected }, { selected = it })

    @Test fun secondarySettingsPagesReturnToTheOriginalTabAndPage() {
        val origin = ChatRoute("machine", "session")
        val settingsPages = listOf(SettingsRoute, SettingsPageRoute(SettingsPage.About), PairingRoute, ConnectionSettingsRoute)
        for (tab in AppTab.entries) {
            for (page in settingsPages) {
                stacks.values.forEach { it.subList(1, it.size).clear() }
                selected = tab
                stacks.getValue(tab).add(origin)
                navigator.navigate(page)
                assertEquals(tab, selected)
                val destination = if (page == ConnectionSettingsRoute) SettingsPageRoute(SettingsPage.Diagnostics) else page
                assertEquals(listOf(tabRoot(tab), origin, SettingsRoute) + if (destination == SettingsRoute) emptyList() else listOf(destination),
                    stacks.getValue(tab))
                navigator.back()
                if (page != SettingsRoute) {
                    assertEquals(SettingsRoute, stacks.getValue(tab).last())
                    navigator.back()
                }
                assertEquals(tab, selected)
                assertEquals(listOf(tabRoot(tab), origin), stacks.getValue(tab))
                assertEquals(listOf(tabRoot(AppTab.entries.first { it != tab })), stacks.getValue(AppTab.entries.first { it != tab }))
            }
        }
    }

    @Test fun contextualModelSettingsReturnDirectlyToTheAssistantTask() {
        selected = AppTab.Persona
        val origin = TodoDetailRoute("example-task")
        navigator.navigate(origin)
        navigator.navigate(ModelPurposeRoute("example-purpose", "example-manager"))
        assertEquals(listOf(AssistantRoute, origin, ModelPurposeRoute("example-purpose", "example-manager")), stacks.getValue(selected))
        navigator.back()
        assertEquals(origin, stacks.getValue(selected).last())
    }

    @Test fun inboxIsPushedOntoTheCurrentStackAndBackReturnsToTheOrigin() {
        val origin = SettingsPageRoute(SettingsPage.Hubs)
        navigator.navigate(origin)
        navigator.navigate(InboxRoute)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, SettingsRoute, origin, InboxRoute), stacks.getValue(selected))
        navigator.back()
        assertEquals(origin, stacks.getValue(selected).last())
    }

    @Test fun legacyLedgerNavigationAndRestoredStacksReturnToConnectors() {
        navigator.navigate(SettingsPageRoute(SettingsPage.Sent))
        assertEquals(listOf(AgentsRoute, SettingsRoute, SettingsPageRoute(SettingsPage.Connectors), SettingsPageRoute(SettingsPage.Sent)),
            stacks.getValue(AppTab.Agents))
        navigator.back()
        assertEquals(SettingsPageRoute(SettingsPage.Connectors), stacks.getValue(AppTab.Agents).last())
        navigator.back()
        assertEquals(listOf(AgentsRoute, SettingsRoute), stacks.getValue(AppTab.Agents))

        stacks.getValue(AppTab.Agents).add(SettingsPageRoute(SettingsPage.Sent))
        val restored = AppNavigator(stacks, { selected }, { selected = it })
        restored.back()
        assertEquals(SettingsPageRoute(SettingsPage.Connectors), stacks.getValue(AppTab.Agents).last())
    }

    @Test fun confirmedCloseRemovesAllMatchingDestinationsAcrossTabs() {
        val target = persona.core.contracts.SessionKey("machine", "closed")
        val chat = ChatRoute(target.machineId, target.sessionId)
        val other = ChatRoute("other-machine", target.sessionId)
        stacks.getValue(AppTab.Agents).addAll(listOf(chat, TerminalRoute(target.machineId, target.sessionId)))
        stacks.getValue(AppTab.Persona).addAll(listOf(chat.copy(kind = ConversationKind.Assistant), InteractionRoute(chat, "request"), other))
        navigator.sessionClosed(target)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, other), stacks.getValue(AppTab.Persona))
        navigator.sessionClosed(target)
        navigator.back()
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    @Test fun aChatWhoseGroupWentAwayLeavesItsOwnTabNotTheOneOnScreen() {
        val chat = ChatRoute("machine~g0a1b2c3d", "s")
        val hubs = SettingsPageRoute(SettingsPage.Hubs)
        stacks.getValue(AppTab.Agents).add(chat)
        selected = AppTab.Persona
        navigator.navigate(hubs)
        navigator.groupGone(chat, null)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, SettingsRoute, hubs), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        val moved = ChatRoute("machine", "s")
        stacks.getValue(AppTab.Agents).add(InteractionRoute(chat, "request"))
        navigator.groupGone(chat, moved)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, SettingsRoute, hubs), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute, moved), stacks.getValue(AppTab.Agents))

        val router = agentsOwnAgentChats()
        stacks.getValue(AppTab.Agents).remove(moved)
        router.navigate(chat)
        router.groupGone(chat, moved)
        assertEquals(listOf(AgentsRoute, moved), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(hubs, stacks.getValue(selected).last())
    }

    @Test fun switchingTabsReturnsToTheAgentsListAndBackNeverRemovesTheRoot() {
        val chat = ChatRoute("machine", "session")
        navigator.navigate(chat)
        navigator.selectTab(AppTab.Persona)
        navigator.back()
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
        navigator.selectTab(AppTab.Agents)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        navigator.back()
        navigator.back()
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    @Test fun knownAgentChatFromSettingsReturnsToThatSettingsPage() {
        selected = AppTab.Persona
        val settings = SettingsPageRoute(SettingsPage.Notifications)
        navigator.navigate(settings)
        val chat = ChatRoute("machine", "session", ConversationKind.Agent)
        navigator.navigate(chat)
        navigator.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        navigator.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(settings, stacks.getValue(selected).last())
    }

    @Test fun assistantChatsStayInPersonaAndLateMetadataRepairsRestoredChats() {
        var assistantKnown = false
        val assistantChat = ChatRoute("machine", "assistant")
        val router = AppNavigator(stacks, { selected }, { selected = it }, chatTab = {
            if (assistantKnown && it == assistantChat) AppTab.Persona else null
        })
        router.selectTab(AppTab.Persona)
        router.navigate(assistantChat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.back()

        stacks.getValue(AppTab.Agents).add(assistantChat)
        selected = AppTab.Agents // Restored position, not an explicit request to open the Agents list.
        assistantKnown = true
        router.restoreChatTabs()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, assistantChat), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.selectTab(AppTab.Agents)
        router.navigate(assistantChat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, assistantChat), stacks.getValue(AppTab.Persona))
    }

    @Test fun restoredStackKeepsChatOwnership() {
        val chat = ChatRoute("machine", "assistant")
        stacks.getValue(AppTab.Persona).add(chat)
        navigator.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, chat), stacks.getValue(AppTab.Persona))
    }

    @Test fun embeddedAssistantConversationsStayInThePersonaHome() {
        val assistantChat = ChatRoute("machine", "assistant")
        val opened = mutableListOf<Pair<ChatRoute, Boolean>>()
        val router = AppNavigator(stacks, { selected }, { selected = it }, chatTab = {
            if (it.sessionId == "assistant") AppTab.Persona else AppTab.Agents
        }, openAssistant = { chat, select ->
            (chat.sessionId == "assistant").also { if (it) { opened += chat to select; if (select) selected = AppTab.Persona } }
        })
        router.navigate(assistantChat)
        assertEquals(listOf(assistantChat to true), opened)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))

        stacks.getValue(AppTab.Agents).add(assistantChat)
        router.navigate(InboxRoute)
        router.restoreChatTabs()
        assertEquals(assistantChat to false, opened.last())
        assertEquals(AppTab.Persona, selected)
        assertEquals(InboxRoute, stacks.getValue(selected).last())
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        val agentChat = ChatRoute("machine", "agent")
        router.navigate(agentChat)
        assertEquals(listOf(AgentsRoute, agentChat), stacks.getValue(AppTab.Agents))
    }

    @Test fun anInboxAssistantChatStaysAboveItsEventAfterMetadataRepair() {
        val event = InboxEventRoute("item")
        val chat = ChatRoute("machine", "assistant", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))
        // Cross-tab identity is the session ids, not the route's kind or provenance.
        stacks.getValue(AppTab.Agents).add(chat.copy(kind = null, source = null))

        router.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        router.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
    }

    @Test fun anInboxAgentChatReturnsToTheEventWithoutMovingItsPersonaStack() {
        val event = InboxEventRoute("item")
        val chat = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Agent }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))

        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
    }

    @Test fun anInboxAssistantChatOpenedFromTheOriginalPageReturnsThere() {
        val original = LarkChatRoute("chat")
        val chat = ChatRoute("machine", "assistant", source = RouteSource.InboxItem("item"))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, original))

        router.navigate(chat)
        assertEquals(listOf(AssistantRoute, InboxRoute, original, chat), stacks.getValue(AppTab.Persona))
        router.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, original, chat), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(original, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun anUnsourcedAssistantChatStillReplacesThePersonaHomeContents() {
        val chat = ChatRoute("machine", "assistant", ConversationKind.Assistant)
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, InboxEventRoute("item")))

        router.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun movingAnInboxChatToAnotherHubGroupKeepsItsSourceAndPlace() {
        val event = InboxEventRoute("item")
        val chat = ChatRoute("machine~group", "assistant", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val moved = ChatRoute("machine", "assistant", ConversationKind.Assistant)
        val expected = moved.copy(source = chat.source)
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event, chat))

        router.groupGone(chat, moved)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, expected), stacks.getValue(AppTab.Persona))
        router.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, event, expected), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aChildAssistantChatInheritsTheSourceAndBackUnwindsToTheEvent() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val child = ChatRoute("machine", "child", ConversationKind.Assistant)
        val sourcedChild = child.copy(source = handling.source)
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(handling)
        router.navigate(child)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling, sourcedChild), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun anAgentAboveAnInboxAssistantStaysOnAgentsAndReturnsToTheHandlingChat() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val agent = ChatRoute("machine", "agent", ConversationKind.Agent)
        val sourcedAgent = agent.copy(source = handling.source)
        val router = embeddedAssistantNavigator {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(handling)
        router.navigate(agent)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, sourcedAgent), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aRequestInAChildAssistantChatKeepsTheSameReturnChain() {
        val event = InboxEventRoute("item")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val request = InteractionRoute(ChatRoute("machine", "child", ConversationKind.Assistant), "request")
        val sourcedRequest = request.copy(chat = request.chat.copy(source = handling.source))
        val router = embeddedAssistantNavigator { ConversationKind.Assistant }
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))

        router.navigate(handling)
        router.navigate(request)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, handling, sourcedRequest), stacks.getValue(AppTab.Persona))
        router.back()
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
        router.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun reopeningAnAgentFromAnotherItemReturnsToTheLatestHandlingChat() {
        val first = ChatRoute("machine", "agent", ConversationKind.Agent, RouteSource.InboxItem("first"))
        val event = InboxEventRoute("second")
        val handling = ChatRoute("machine", "handling", ConversationKind.Assistant, RouteSource.InboxItem("second"))
        val router = embeddedAssistantNavigator {
            if (it.sessionId == "agent") ConversationKind.Agent else ConversationKind.Assistant
        }
        stacks.getValue(AppTab.Agents).add(first)
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event, handling))

        router.navigate(first.copy(source = null))
        assertEquals(AppTab.Agents, selected)
        // Router.restore retains the existing Agent page and its UI state; the new origin is still recorded.
        assertEquals(listOf(AgentsRoute, first), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(handling, stacks.getValue(AppTab.Persona).last())
    }

    /** Uses Root's production decision; only its selection side effect is reproduced here. */
    private fun embeddedAssistantNavigator(kind: (SessionKey) -> ConversationKind?) = AppNavigator(
        stacks, { selected }, { selected = it },
        chatTab = { route ->
            kind(SessionKey(route.machineId, route.sessionId))?.let {
                if (it == ConversationKind.Assistant) AppTab.Persona else AppTab.Agents
            }
        },
        openAssistant = { chat, select ->
            assistantHomeTarget(chat, embeddedAssistant = true, kind = kind)?.let {
                if (select) {
                    val stack = stacks.getValue(AppTab.Persona)
                    while (stack.size > 1) stack.removeAt(stack.lastIndex)
                    selected = AppTab.Persona
                }
                true
            } ?: false
        },
    )

    private fun agentsOwnAgentChats() = AppNavigator(stacks, { selected }, { selected = it }, chatTab = { AppTab.Agents })

    @Test fun backFromAnAgentChatOpenedInThePersonaHomeReturnsToTheHome() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        val chat = ChatRoute("machine", "agent")
        router.navigate(chat)
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))

        stacks.getValue(AppTab.Agents).add(chat)
        router.navigate(chat)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)

        val known = ChatRoute("machine", "known", ConversationKind.Agent)
        router.navigate(known)
        assertEquals(AppTab.Agents, selected)
        router.back()
        assertEquals(AppTab.Persona, selected)
    }

    @Test fun pagesPushedAboveACrossTabChatGoBackToItFirst() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        router.navigate(InboxRoute)
        val chat = ChatRoute("machine", "agent")
        val terminal = TerminalRoute("machine", "agent")
        router.navigate(chat)
        router.navigate(terminal)
        assertEquals(listOf(AgentsRoute, chat, terminal), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute, chat), stacks.getValue(AppTab.Agents))
        router.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(InboxRoute, stacks.getValue(selected).last())
    }

    @Test fun pickingATabForgetsWhereACrossTabPageCameFrom() {
        val router = agentsOwnAgentChats()
        selected = AppTab.Persona
        val chat = ChatRoute("machine", "agent")
        router.navigate(chat)
        router.selectTab(AppTab.Persona)
        router.selectTab(AppTab.Agents)
        router.back()
        assertEquals(AppTab.Agents, selected)
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
        router.navigate(chat)
        router.back()
        assertEquals(AppTab.Agents, selected)
    }
}
