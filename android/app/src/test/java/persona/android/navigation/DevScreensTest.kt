package persona.android.navigation

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.navigation.AgentsRoute
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.InboxRoute
import persona.core.navigation.InteractionRoute
import persona.core.navigation.KnowledgeRoute
import persona.core.navigation.SettingsPage
import persona.core.navigation.SettingsPageRoute
import persona.core.navigation.TerminalRoute

class DevScreensTest {
    @Test fun rootsTabsAndChatsUseOnlyFixedNames() {
        assertNull(devScreenName(AppTab.Persona, AssistantRoute))
        assertNull(devScreenName(AppTab.Agents, AgentsRoute))
        assertEquals("assistant-chat", devScreenName(AppTab.Persona, ChatRoute("private-machine", "private-session")))
        assertEquals("chat", devScreenName(AppTab.Agents, ChatRoute("private-machine", "private-session")))
        assertEquals("assistant-chat", devScreenName(AppTab.Agents,
            ChatRoute("private-machine", "private-session", ConversationKind.Assistant)))
        assertEquals("chat", devScreenName(AppTab.Agents,
            InteractionRoute(ChatRoute("private-machine", "private-session", ConversationKind.Agent), "private-request")))
    }

    @Test fun overlaysAreNamedWithoutRouteArguments() {
        assertEquals("settings", devScreenName(AppTab.Persona, SettingsPageRoute(SettingsPage.About)))
        assertEquals("inbox", devScreenName(AppTab.Persona, InboxRoute))
        assertEquals("knowledge", devScreenName(AppTab.Persona, KnowledgeRoute))
        assertEquals("screen", devScreenName(AppTab.Agents, TerminalRoute("private-machine", "private-session")))
    }
}
