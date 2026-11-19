package persona.android.navigation

import android.net.Uri
import androidx.navigation3.runtime.NavKey
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.annotation.Config
import persona.core.navigation.*

/** A needs-you notification opens its conversation at the request it is about (#3983). */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class NotificationRequestLinkTest {
    private fun stacks() = mapOf<AppTab, MutableList<NavKey>>(
        AppTab.Persona to mutableListOf(AssistantRoute), AppTab.Agents to mutableListOf(AgentsRoute),
    )

    @Test fun linkCarriesTheRequestAndOpensItWithoutStackingASecondPage() {
        val link = NotificationRoute.conversation("machine", "session", false, "second")
        val parsed = notificationRoute(link.uri())!!
        assertEquals(link, parsed)
        val agent = ChatRoute("machine", "session", ConversationKind.Agent)
        val stacks = stacks()
        // The conversation is already open (showing its first request): the tap lands on the second, one page deep.
        stacks.getValue(AppTab.Agents).add(agent)
        assertEquals(AppTab.Agents, Router().open(parsed)!!.restore(stacks))
        assertEquals(listOf(AgentsRoute, InteractionRoute(agent, "second")), stacks.getValue(AppTab.Agents))
        // Opening the conversation from the list afterwards keeps that page.
        Router().parse(RouteInput.Internal(agent))!!.restore(stacks)
        assertEquals(listOf(AgentsRoute, InteractionRoute(agent, "second")), stacks.getValue(AppTab.Agents))
    }

    @Test fun linkWithoutRequestStillOpensTheChat() {
        val link = NotificationRoute.conversation("machine", "session", false)
        assertEquals(null, Uri.parse(link.uri().toString()).getQueryParameter("request"))
        assertEquals(ChatRoute("machine", "session", ConversationKind.Agent), Router().open(notificationRoute(link.uri())!!)!!.key)
    }

    @Test fun canonicalPageQueriesAreNotNotificationTabs() {
        val persona = "persona://open/settings?tab=persona"
        val agents = "persona://open/settings?tab=agents"
        assertEquals(null, notificationRoute(Uri.parse(persona)))
        assertEquals(null, notificationRoute(Uri.parse(agents)))
        assertEquals(SettingsPageRoute(SettingsPage.Persona), Router().parse(RouteInput.DeepLink(persona))?.key)
        assertEquals(SettingsPageRoute(SettingsPage.AgentSettings), Router().parse(RouteInput.DeepLink(agents))?.key)
        assertEquals(null, notificationRoute(Uri.parse("persona://open/todos/t-19?tab=agents")))
    }

    @Test fun untabbedLinkNamesItsRequestOnceMetadataArrives() {
        val untabbed = NotificationRoute(null, ChatRoute("machine", "session"), "second").uri().toString()
        assertEquals(InteractionRoute(ChatRoute("machine", "session", ConversationKind.Agent), "second"),
            Router { _, _ -> ConversationKind.Agent }.parse(RouteInput.DeepLink(untabbed))!!.key)
        // An empty request parameter still opens the chat.
        assertEquals(ChatRoute("machine", "session", ConversationKind.Agent),
            Router { _, _ -> ConversationKind.Agent }.parse(RouteInput.DeepLink("persona://open?machine=machine&session=session&request="))!!.key)
    }
}
