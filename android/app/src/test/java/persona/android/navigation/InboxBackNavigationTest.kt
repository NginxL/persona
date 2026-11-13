package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.contracts.SessionKey
import persona.core.navigation.*
import persona.feature.inbox.InboxEventRoute
import org.junit.Assert.assertEquals
import org.junit.Test

/**
 * A conversation opened from an inbox item (docs/android/android.md "Inbox: events, suggestions and handling chain")
 * stays a page above that item through late session metadata, a repeated tap and a hub-group migration; Back from it
 * returns to the item's page, and an Agent still opens under Agents and Back selects Persona with the item page on top.
 *
 * The shell's decision is the production [assistantHomeTarget]; only its selection side effect (Root's openAssistant)
 * is reproduced here.
 */
class InboxBackNavigationTest {
    private val stacks = AppTab.entries.associateWith { mutableListOf<NavKey>(tabRoot(it)) }
    private var selected = AppTab.Persona
    /** What the hub's session metadata says, as it arrives after the first frames. */
    private var kinds = mapOf<SessionKey, ConversationKind>()
    private fun kind(key: SessionKey): ConversationKind? = kinds[key]

    private val navigator = AppNavigator(stacks, { selected }, { selected = it }, chatTab = { route ->
        kind(SessionKey(route.machineId, route.sessionId))?.let {
            if (it == ConversationKind.Assistant) AppTab.Persona else AppTab.Agents
        }
    }, openAssistant = { chat, select ->
        assistantHomeTarget(chat, embeddedAssistant = true, kind = ::kind)?.let {
            if (select) {
                val stack = stacks.getValue(AppTab.Persona)
                while (stack.size > 1) stack.removeAt(stack.lastIndex)
                selected = AppTab.Persona
            }
            true
        } ?: false
    })

    private val event = InboxEventRoute("item")
    private fun sourced(kind: ConversationKind? = null) =
        ChatRoute("machine", "assistant", kind, RouteSource.InboxItem("item"))

    private fun openEvent() {
        selected = AppTab.Persona
        stacks.getValue(AppTab.Persona).addAll(listOf(InboxRoute, event))
    }

    @Test fun aSourcedChatPushedBeforeItsMetadataStaysAboveTheItemWhenTheKindArrives() {
        openEvent()
        val chat = sourced()
        // The session list has not loaded: neither the route nor the hub knows the kind yet.
        navigator.navigate(chat)
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))

        kinds = mapOf(SessionKey("machine", "assistant") to ConversationKind.Assistant)
        navigator.restoreChatTabs()
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))

        navigator.back()
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
    }

    @Test fun tappingTheSameSourcedChatTwiceKeepsOnePageAboveItsItem() {
        openEvent()
        val chat = sourced(ConversationKind.Assistant)
        navigator.navigate(chat)
        navigator.navigate(chat)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        navigator.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aSourcedChatWhoseHubGroupWentAwayLeavesTheItemPageInPlace() {
        openEvent()
        val chat = sourced(ConversationKind.Assistant)
        navigator.navigate(chat)
        navigator.groupGone(chat, null)
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
        navigator.back()
        assertEquals(InboxRoute, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aPageOpenedAboveTheSourcedChatSeesTheMigratedChatBeneathIt() {
        openEvent()
        val chat = ChatRoute("machine~g1", "assistant", ConversationKind.Assistant, RouteSource.InboxItem("item"))
        val above = TerminalRoute("machine~g1", "assistant")
        navigator.navigate(chat)
        navigator.navigate(above)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat, above), stacks.getValue(AppTab.Persona))

        // The shell rebuilds the replacement from the ids alone: provenance stays with the page.
        navigator.groupGone(chat, ChatRoute("machine", "assistant", ConversationKind.Assistant))
        val moved = chat.copy(machineId = "machine")
        val stack = stacks.getValue(AppTab.Persona)
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stack.take(3))
        assertEquals(moved, stack[3])
        // The page above keeps its own ids (groupGone never moved it): Back still lands on the item, not on the home.
        navigator.back()
        navigator.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

    @Test fun aClosedSourcedChatLeavesTheItemPageBehind() {
        openEvent()
        val chat = sourced(ConversationKind.Assistant)
        navigator.navigate(chat)
        navigator.sessionClosed(SessionKey("machine", "assistant"))
        assertEquals(listOf(AssistantRoute, InboxRoute, event), stacks.getValue(AppTab.Persona))
        // The item page it was opened from is still the place Back goes.
        navigator.back()
        assertEquals(InboxRoute, stacks.getValue(AppTab.Persona).last())
    }

    /**
     * The old behaviour stays: an unsourced assistant chat with no inbox conversation under it still becomes the
     * Persona home's conversation, clearing whatever secondary pages were open (docs/android/android.md "No home page").
     */
    @Test fun anUnsourcedAssistantChatStillBecomesTheHomeWhenNoSourcedPageIsOpen() {
        openEvent()
        navigator.navigate(ChatRoute("machine", "other", ConversationKind.Assistant))
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute), stacks.getValue(AppTab.Persona))
    }

    @Test fun aSourcedChatOpensOnThePersonaTabWhicheverTabIsShowing() {
        kinds = mapOf(SessionKey("machine", "assistant") to ConversationKind.Assistant)
        openEvent()
        selected = AppTab.Agents
        navigator.navigate(sourced(ConversationKind.Assistant))
        assertEquals(AppTab.Persona, selected)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, sourced(ConversationKind.Assistant)),
            stacks.getValue(AppTab.Persona))
        assertEquals(listOf(AgentsRoute), stacks.getValue(AppTab.Agents))
    }

    /**
     * The chain's link names no kind (the hub's session kind is not in it): once metadata arrives the shell still keeps
     * the page, and tapping the same link again does not stack a second copy of it.
     */
    @Test fun aKindlessSourcedChatStaysOnePageAfterItsKindArrives() {
        openEvent()
        val chat = sourced()
        navigator.navigate(chat)
        kinds = mapOf(SessionKey("machine", "assistant") to ConversationKind.Assistant)
        navigator.restoreChatTabs()
        navigator.navigate(chat)
        assertEquals(listOf(AssistantRoute, InboxRoute, event, chat), stacks.getValue(AppTab.Persona))
        navigator.back()
        assertEquals(event, stacks.getValue(AppTab.Persona).last())
    }

}
