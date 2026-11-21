package persona.android.navigation

import androidx.compose.runtime.mutableStateListOf
import androidx.navigation3.runtime.NavKey
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotSame
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.LarkMessagesRoute
import persona.core.navigation.TodoDetailRoute
import persona.feature.assistant.AssistantSidebarShade
import persona.feature.inbox.InboxLanding
import persona.feature.inbox.LarkRoute
import persona.android.ui.leaveDisabledLarkMessages

class LarkMessagesNavigationTest {
    @Test fun messagesIsARetainedSecondaryPlaceNotAnInboxOrPrimaryTab() {
        assertEquals(LarkMessagesRoute, placeRootFor(LarkMessagesRoute))
        assertTrue(isSidebarPlaceRoot(LarkMessagesRoute))
        assertTrue(retainsSidebarComposition(LarkMessagesRoute))
        assertEquals("lark-messages", sidebarHostId(LarkMessagesRoute))
        assertEquals(AssistantSidebarShade.Messages, sidebarShadeKey(LarkMessagesRoute))
        assertEquals(2, AppTab.entries.size)

        val policy = assistantPanePolicy(AppTab.Persona, listOf(LarkMessagesRoute, LarkRoute("chat", fromMessages = true)), regular = true, widePhone = false)
        assertTrue(policy.keepSharedSidebar)
        assertTrue(policy.singleRightStack)
        assertFalse(policy.keepWidePhoneListDetail)
    }

    @Test fun aChatOpenedFromRegularMessagesStaysOnThatHost() {
        val fixture = Fixture(regular = true)
        assertTrue(fixture.open(LarkMessagesRoute))
        assertEquals(listOf(LarkMessagesRoute), fixture.hosts.current.toList())

        val chat = LarkRoute("chat", fromMessages = true)
        assertFalse(fixture.open(chat))
        // AppNavigator performs this push after openPlace declines to re-parent the route.
        fixture.hosts.current.add(chat)
        assertEquals(listOf(LarkMessagesRoute, chat), fixture.hosts.current.toList())
        assertFalse(InboxRoute in fixture.hosts.current)

        fixture.hosts.current.removeAt(fixture.hosts.current.lastIndex)
        assertTrue(fixture.window.leavePlaceRoot(true, fixture.hosts.current, fixture.roots, fixture::show))
        assertEquals(listOf(AssistantRoute), fixture.hosts.current.toList())
    }

    @Test fun aChatAndBackKeepTheCompactMessagesParent() {
        val fixture = Fixture(regular = false)
        applySidebarDestination(
            LarkMessagesRoute, false, fixture.roots, fixture.hosts, fixture.conversation, fixture::show,
            assignPersona = {}, landing = InboxLanding(), records = emptyList(),
        )
        assertEquals(listOf(AssistantRoute, LarkMessagesRoute), fixture.conversation.toList())

        val chat = LarkRoute("chat", fromMessages = true)
        assertFalse(fixture.open(chat))
        fixture.hosts.current.add(chat)
        assertEquals(listOf(AssistantRoute, LarkMessagesRoute, chat), fixture.conversation.toList())

        fixture.conversation.removeAt(fixture.conversation.lastIndex)
        assertEquals(listOf(AssistantRoute, LarkMessagesRoute), fixture.conversation.toList())
        fixture.conversation.removeAt(fixture.conversation.lastIndex)
        assertEquals(listOf(AssistantRoute), fixture.conversation.toList())
    }

    @Test fun disablingRegularMessagesRestoresThePreviousAssistantPath() {
        val fixture = Fixture(regular = true)
        val detail = TodoDetailRoute("trip")
        fixture.hosts.activate(AssistantRoute, listOf(AssistantRoute, detail))
        assertTrue(fixture.open(LarkMessagesRoute))
        assertEquals(listOf(LarkMessagesRoute), fixture.hosts.current.toList())

        leaveDisabledLarkMessages(
            regular = true,
            pageRoots = fixture.roots,
            hosts = fixture.hosts,
            showHost = fixture::show,
            showCompactHome = { error("A regular place must not reset the conversation") },
        )

        assertEquals(AssistantRoute, fixture.roots.current)
        assertEquals(listOf(AssistantRoute, detail), fixture.hosts.current.toList())
    }

    @Test fun disablingMessagesForgetsOnlyThatPlaceAndItsDrillDowns() {
        val fixture = Fixture(regular = true)
        fixture.hosts.activate(InboxRoute, listOf(InboxRoute))
        fixture.roots.select(InboxRoute, emptyList())
        fixture.hosts.activate(LarkMessagesRoute, listOf(LarkMessagesRoute, LarkRoute("chat", fromMessages = true)))
        fixture.roots.select(LarkMessagesRoute, emptyList())
        fixture.roots.markOpenedAsRoot(LarkMessagesRoute)

        fixture.roots.forgetPlace(LarkMessagesRoute)
        fixture.hosts.forgetPlace(LarkMessagesRoute)

        assertEquals(AssistantRoute, fixture.roots.current)
        assertFalse(fixture.roots.openedAsRoot(LarkMessagesRoute))
        assertNull(fixture.hosts.stack("lark-messages"))
        assertNotSame(fixture.hosts.current, fixture.hosts.stack("inbox"))
        assertEquals(listOf(InboxRoute), fixture.hosts.stack("inbox")?.toList())
        assertEquals(listOf(AssistantRoute), fixture.hosts.current.toList())
    }

    private class Fixture(regular: Boolean) {
        val conversation = mutableStateListOf<NavKey>(AssistantRoute)
        val hosts = SidebarRootHosts(conversation)
        val roots = SidebarPageRoots()
        val window = SidebarPlaceWindow().apply { noteSize(if (regular) 1000f else 400f, 800f) }

        fun show(root: NavKey, next: List<NavKey>) = hosts.activate(root, next)

        fun open(key: NavKey): Boolean = openSidebarPlace(
            key, window, roots, hosts, ::show, assignPersona = {}, selectPersona = {},
        )
    }
}
