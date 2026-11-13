package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.*
import persona.core.contracts.SessionKey
import persona.core.hub.errors.ErrorBreadcrumbs

internal class AppNavigator(
    private val stacks: Map<AppTab, MutableList<NavKey>>,
    private val selectedTab: () -> AppTab,
    private val onSelectTab: (AppTab) -> Unit,
    private val chatTab: (ChatRoute) -> AppTab? = { null },
    /** Returns true when the Persona home shows this conversation itself; the flag says whether to switch to it. */
    private val openAssistant: (ChatRoute, Boolean) -> Boolean = { _, _ -> false },
) : Navigator {
    /**
     * Pages [navigate] pushed onto another tab's stack, and the tab each was opened from: back from such a page returns
     * to that tab (a task card in the Persona home opens its agent's chat under Agents; back goes to the home, not to
     * the Agents list). The user picking a tab themselves drops these.
     */
    private val openedFrom = mutableMapOf<NavKey, AppTab>()

    init { stacks.values.forEach(::restoreSecondaryParents) }

    override fun selectTab(tab: AppTab) {
        openedFrom.clear()
        // Explicit Agents selection opens its list; targeted session navigation uses onSelectTab directly.
        if (tab == AppTab.Agents) {
            val stack = stacks.getValue(tab)
            while (stack.size > 1) stack.removeAt(stack.lastIndex)
        }
        onSelectTab(tab)
        ErrorBreadcrumbs.shared.navigation("tab:${tab.name}")
    }

    override fun navigate(key: NavKey) {
        val from = selectedTab()
        // The route's type only, never its arguments (machine, session, item ids).
        ErrorBreadcrumbs.shared.navigation(key::class.simpleName ?: "route")
        val shown = stacks.mapValues { it.value.lastOrNull() }
        val opened = open(key)
        val to = selectedTab()
        val page = stacks.getValue(to).lastOrNull() ?: return
        // Not recorded: the tab's root, and a page that already topped the target tab's stack but is not the one asked
        // for (this call did not open it).
        if (to != from && page != tabRoot(to) && (page != shown[to] || page == opened)) openedFrom[page] = from
    }

    /** Returns the actual key opened, which may have inherited the visible inbox conversation's source. */
    private fun open(key: NavKey): NavKey {
        val route = inheritVisibleInboxSource(key)
        if (route == ConnectionSettingsRoute) return open(SettingsPageRoute(SettingsPage.Diagnostics))
        val chat = when (route) { is ChatRoute -> route; is InteractionRoute -> route.chat; else -> null }
        if (chat != null && openAssistant(chat, true)) return route
        // A sourced assistant conversation belongs above the item that opened it, even when its kind is already known.
        // Router.restore would instead pop to an earlier copy and retain that copy's provenance. Agents keep that path.
        val fromInbox = chat != null && chat.source is RouteSource.InboxItem &&
            (chat.kind == ConversationKind.Assistant || chatTab(chat) == AppTab.Persona)
        if (chat != null && chat.kind != null && !fromInbox) {
            Router().parse(RouteInput.Internal(route))?.let { resolved ->
                val tab = resolved.restore(stacks)
                onSelectTab(tab)
                return stacks.getValue(tab).lastOrNull() ?: route
            }
        }
        val rootTab = when (route) {
            AssistantRoute -> AppTab.Persona
            AgentsRoute -> AppTab.Agents
            else -> null
        }
        if (rootTab != null) {
            onSelectTab(rootTab)
            return route
        }
        val target = if (route is ChatRoute || fromInbox) chatTab(requireNotNull(chat))
            ?: stacks.entries.firstOrNull { route in it.value }?.key
            ?: selectedTab().takeIf { it == AppTab.Persona }
            ?: AppTab.Agents else selectedTab()
        onSelectTab(target)
        if (chat != null && (route is ChatRoute || fromInbox)) stacks.filterKeys { it != target }.values.forEach { stack ->
            stack.removeAll { page -> page is ChatRoute && sameConversation(page, chat) }
        }
        val stack = stacks.getValue(target)
        if (stack.last() != route) stack.add(route)
        restoreSecondaryParents(stack)
        return route
    }

    /** A child conversation opened above an inbox conversation belongs to the same return chain. */
    private fun inheritVisibleInboxSource(key: NavKey): NavKey {
        if (selectedTab() != AppTab.Persona) return key
        val source = stacks.getValue(AppTab.Persona).lastOrNull()?.chat()?.source as? RouteSource.InboxItem ?: return key
        return when (key) {
            is ChatRoute -> if (key.source == null) key.copy(source = source) else key
            is InteractionRoute -> if (key.chat.source == null) key.copy(chat = key.chat.copy(source = source)) else key
            else -> key
        }
    }

    /** Repair restored entries once session metadata becomes available. */
    fun restoreChatTabs() {
        val visible = stacks.getValue(selectedTab()).lastOrNull()
        stacks.forEach { (tab, stack) ->
            stack.filterIsInstance<ChatRoute>().forEach chatEntry@ { chat ->
                val owner = chatTab(chat) ?: return@chatEntry
                if (owner == AppTab.Persona && openAssistant(chat, visible == chat)) {
                    stack.removeAll { it == chat }
                    return@chatEntry
                }
                if (owner != tab) {
                    stack.removeAll { it == chat }
                    val destination = stacks.getValue(owner)
                    if (chat !in destination) {
                        if (owner == selectedTab() && visible != chat) destination.add(1, chat)
                        else destination.add(chat)
                    }
                    if (visible == chat) onSelectTab(owner)
                }
            }
        }
    }

    override fun back() {
        val stack = stacks.getValue(selectedTab())
        if (stack.size <= 1) return
        val page = stack.removeAt(stack.lastIndex)
        openedFrom.remove(page)?.let(onSelectTab)
    }

    /** Remove every destination owned by the confirmed closed session, including hidden tabs. */
    fun sessionClosed(session: SessionKey) {
        stacks.values.forEach { stack ->
            stack.removeAll { key ->
                when (key) {
                    is ChatRoute -> key.machineId == session.machineId && key.sessionId == session.sessionId
                    is InteractionRoute -> key.chat.machineId == session.machineId && key.chat.sessionId == session.sessionId
                    is TerminalRoute -> key.machineId == session.machineId && key.sessionId == session.sessionId
                    else -> false
                }
            }
        }
        openedFrom.keys.retainAll { page -> stacks.values.any { page in it } }
    }

    /**
     * The chat's hub group went away: its pages become [moved] (the same session under its new id) where they stand,
     * else they go. Visited tabs stay composed, so this can come from a hidden tab: the tab on screen stays as it is.
     */
    fun groupGone(chat: ChatRoute, moved: ChatRoute?) {
        fun shows(key: NavKey) = key.chat()?.let { sameConversation(it, chat) } == true
        // Callers rebuilding the replacement may only know its new ids and kind; provenance belongs to the existing page.
        val destination = moved?.let { if (it.source == null) it.copy(source = chat.source) else it }
        stacks.values.forEach { stack ->
            val at = stack.indexOfFirst { shows(it) }.takeIf { it >= 0 } ?: return@forEach
            val from = stack.firstNotNullOfOrNull { page -> openedFrom[page]?.takeIf { shows(page) } }
            stack.removeAll { shows(it) }
            if (destination != null && stack.none { it is ChatRoute && sameConversation(it, destination) }) {
                stack.add(at, destination)
                from?.let { openedFrom[destination] = it }
            }
        }
        openedFrom.keys.retainAll { page -> stacks.values.any { page in it } }
    }
}

private fun NavKey.chat(): ChatRoute? = when (this) {
    is ChatRoute -> this
    is InteractionRoute -> chat
    else -> null
}

private fun sameConversation(left: ChatRoute, right: ChatRoute): Boolean =
    left.machineId == right.machineId && left.sessionId == right.sessionId
