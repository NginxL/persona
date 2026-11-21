package persona.android.navigation

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AssistantRoute
import persona.core.navigation.LarkMessagesRoute
import persona.feature.inbox.LarkRoute

/**
 * When a place route may become the root (#9667).
 *
 * A route that arrives before the window is measured is remembered and applied on the first regular
 * frame (`AssistantView.openFromOutside`, AssistantView.swift:597–605). Once the window is known and
 * compact, nothing is left pending: the caller pushes, and unfolding uses [regularPromotion] only
 * for a place that is still on the stack and was opened as a root. A stale pending place must not
 * skip that and resurrect an item the user already left.
 */
internal class SidebarPlaceWindow {
    var measured by mutableStateOf(false)
        private set

    /** Last known regular width. False until [noteSize] sees a non-zero window, and while compact. */
    var regular by mutableStateOf(false)
        private set

    var pending by mutableStateOf<NavKey?>(null)
        private set

    /**
     * [widthDp] or [heightDp] of zero is not a compact window: the first frame can report an
     * unmeasured container. Only a positive size is "known".
     */
    fun noteSize(widthDp: Float, heightDp: Float) {
        if (widthDp <= 0f || heightDp <= 0f) return
        val now = isRegularWindow(widthDp, heightDp)
        measured = true
        regular = now
        if (!now) pending = null
    }

    fun clearPending() {
        pending = null
    }

    /**
     * Returns true when the caller must not also push. A child of the place already in front stays
     * a drill (returns false). Before the window is measured, remembers [key] and returns false.
     * Once the window is known and compact, nothing is left pending. A fixed place that already has
     * a host is shown on that host (returns true); otherwise the caller pushes and this returns false.
     */
    fun openPlace(
        key: NavKey,
        pageRoots: SidebarPageRoots,
        hosts: SidebarRootHosts,
        showHost: (NavKey, List<NavKey>) -> Unit,
        selectPersona: () -> Unit,
    ): Boolean {
        // A chat opened from Messages belongs to that root. The generic LarkRoute -> Inbox mapping is for Inbox links;
        // inspecting the real host also covers a compact [Assistant, Messages] prefix.
        if (key is LarkRoute && hosts.current.firstOrNull { it != AssistantRoute } == LarkMessagesRoute) return false
        val place = placeRootFor(key) ?: return false
        pageRoots.markOpenedAsRoot(place)
        if (!measured) {
            pending = key
            return false
        }
        if (!regular) {
            pending = null
            // A fixed place that already has a host is shown there. Pushing it onto the conversation
            // as well would compose the same route twice (docs/design/sidebar-navigation.md).
            val path = if (key == place) listOf(place) else listOf(place, key)
            if (hosts.showRetainedCompact(place, path)) {
                selectPersona()
                return true
            }
            return false
        }
        pending = null
        if (pageRoots.current == place && key != place) return false
        val step = pageRoots.select(place, hosts.current.drop(1))
        // A child opened from outside replaces the saved path (AssistantView.swift:601–605).
        val next = if (key == place) step.stack else listOf(place, key)
        showHost(place, next)
        selectPersona()
        return true
    }

    /**
     * The stack is only a place root. Switch to the conversation without a push or a pop
     * (`leavePlace`, AssistantView.swift:682–686).
     */
    fun leavePlaceRoot(
        persona: Boolean,
        stack: List<NavKey>,
        pageRoots: SidebarPageRoots,
        showHost: (NavKey, List<NavKey>) -> Unit,
        allowCompactRoot: Boolean = false,
    ): Boolean {
        if ((!regular && !allowCompactRoot) || !persona) return false
        if (stack.size != 1) return false
        val root = stack.firstOrNull() ?: return false
        if (root == AssistantRoute) return false
        val step = pageRoots.select(AssistantRoute, emptyList())
        showHost(step.root, step.stack)
        return true
    }

    /**
     * Body of the shell's `LaunchedEffect(regular)`. Pending applies only when it was recorded
     * before the window was measured. A known compact frame clears it.
     */
    fun onWidth(
        regularNow: Boolean,
        hosts: SidebarRootHosts,
        assistantStack: MutableList<NavKey>,
        pageRoots: SidebarPageRoots,
        showHost: (NavKey, List<NavKey>) -> Unit,
        assignPersona: (MutableList<NavKey>) -> Unit,
    ) {
        if (regularNow) {
            val waiting = pending
            if (waiting != null) {
                pending = null
                val place = placeRootFor(waiting)
                if (place != null) {
                    pageRoots.markOpenedAsRoot(place)
                    // Keep a conversation restored before this push. The pending route (and its place
                    // parent) is the only suffix to cut. iOS puts the item on the Inbox path and leaves
                    // the conversation's path (`AssistantView.openFromOutside`, AssistantView.swift:597–605;
                    // `SidebarPageRoots.select`, SidebarPageRoots.swift:88–97).
                    dropPendingPush(assistantStack, place, waiting)
                    val next = if (waiting == place) listOf(place) else listOf(place, waiting)
                    pageRoots.adoptVisible(place)
                    showHost(place, next)
                    return
                }
            }
            // Folding prefixed this host in place. Drop that prefix without rebuilding the page.
            if (hosts.unfoldPrefixedPlace()) {
                pageRoots.adoptVisible(hosts.current.firstOrNull() ?: AssistantRoute)
                assignPersona(hosts.current)
                return
            }
            val promoted = if (hosts.current === assistantStack) {
                regularPromotion(assistantStack.toList(), pageRoots::openedAsRoot)
            } else null
            if (promoted != null) {
                replaceBackStack(assistantStack, listOf(AssistantRoute))
                pageRoots.adoptVisible(promoted.first())
                showHost(promoted.first(), promoted)
            } else if (hosts.current === assistantStack) {
                pageRoots.adoptVisible(assistantStack.firstOrNull() ?: AssistantRoute)
                assignPersona(assistantStack)
            }
        } else {
            if (measured) pending = null
            hosts.foldOntoConversation()
            assignPersona(hosts.current)
            val shown = hosts.current.firstOrNull { it != AssistantRoute } ?: hosts.current.firstOrNull() ?: AssistantRoute
            pageRoots.adoptVisible(shown)
        }
    }
}

/**
 * Root's [openPlace]. [showHost] runs for a regular-width switch. A compact retained host returns
 * true without [showHost], so the Persona stack is pointed at the host in front before the caller
 * continues (a restored chat must land on that host, not on the conversation list).
 */
internal fun openSidebarPlace(
    key: NavKey,
    window: SidebarPlaceWindow,
    pageRoots: SidebarPageRoots,
    hosts: SidebarRootHosts,
    showHost: (NavKey, List<NavKey>) -> Unit,
    assignPersona: (MutableList<NavKey>) -> Unit,
    selectPersona: () -> Unit,
): Boolean {
    val placed = window.openPlace(key, pageRoots, hosts, showHost, selectPersona)
    if (placed) assignPersona(hosts.current)
    return placed
}

/**
 * Remove the pending push from the conversation list. Anything before the place or the pending key
 * stays, including a chat restored at cold start. No such suffix: leave the list as it is.
 */
internal fun dropPendingPush(stack: MutableList<NavKey>, place: NavKey, pendingKey: NavKey) {
    val cut = stack.indexOfFirst { it == place || it == pendingKey }
    if (cut < 0) return
    val kept = stack.take(cut)
    replaceBackStack(stack, if (kept.isEmpty()) listOf(AssistantRoute) else kept)
}
