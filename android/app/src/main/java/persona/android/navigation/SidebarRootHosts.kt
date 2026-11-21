package persona.android.navigation

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshots.SnapshotStateList
import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AssistantRoute

/**
 * One back stack per sidebar root that stays composed. Navigation 3 composes the top entry of each
 * stack, and removing an entry clears its saveable state.
 *
 * The conversation and the fixed places (Inbox, To-dos, Knowledge, Computers) stay in [visited]
 * while another root is in front. A to-do or an extension page is dropped from [visited] when left;
 * its path stays in `SidebarPageRoots` (iOS keeps paths, not live views,
 * docs/design/sidebar-navigation.md). The shell places [current] and leaves the others unplaced.
 *
 * The conversation list is the one `rememberNavBackStack` owns. Place lists are not saved: after
 * process death only the conversation stack comes back (#9667). iOS does not animate a switch
 * (`SidebarPageRoots.select`, SidebarPageRoots.swift:88–97; `UnifiedSidebar.apply`,
 * UnifiedSidebar.swift:67–74).
 *
 * Folding a fixed place prefixes the conversation on that same list, so the page is not rebuilt.
 * A to-do root is copied onto the conversation list and that host is dropped. Fixed places stay.
 */
internal class SidebarRootHosts(val conversation: MutableList<NavKey>) {
    private val places = mutableMapOf<String, SnapshotStateList<NavKey>>()

    /** Host ids composed at this width, conversation first when it is present. */
    val visited = mutableStateListOf(CONVERSATION)

    var current: MutableList<NavKey> by mutableStateOf(conversation)
        private set

    fun stack(id: String): MutableList<NavKey>? = if (id == CONVERSATION) conversation else places[id]

    fun allStacks(): List<MutableList<NavKey>> = listOf(conversation) + places.values

    /** Id of the host in front. Stable when a compact prefix is inserted under a place. */
    fun currentId(): String = hostIdOf(current)

    /** Show [root] with [next] (the root plus its drill-downs). Drops hosts that are not kept. */
    fun activate(root: NavKey, next: List<NavKey>) {
        if (next.isEmpty()) return
        if (sidebarHostId(root) == CONVERSATION) {
            replaceBackStack(conversation, next)
            current = conversation
            if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
        } else {
            val id = sidebarHostId(root)
            val list = places.getOrPut(id) { mutableStateListOf(root) }
            replaceBackStack(list, next)
            if (id !in visited) visited.add(id)
            current = list
        }
        releaseLeftHosts()
        trimComposedClashes()
    }

    /**
     * Folding. A fixed place stays on its own list: the conversation is inserted under it so Back
     * still reaches a conversation entry, and that list is not rebuilt. Anything else is copied
     * onto the conversation list and that host is dropped. Fixed places stay composed; [trimComposedClashes]
     * pops a duplicate top. iOS keeps each root's path (`docs/design/sidebar-navigation.md`).
     */
    fun foldOntoConversation() {
        if (current !== conversation) {
            val place = current.firstOrNull { it != AssistantRoute }
            if (place != null && retainsSidebarComposition(place)) {
                if (current.firstOrNull() != AssistantRoute) current.add(0, AssistantRoute)
                if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
                releaseLeftHosts()
                return
            }
            val visible = current.toList()
            val prefixed = if (visible.firstOrNull() == AssistantRoute) visible else listOf(AssistantRoute) + visible
            val dropped = hostIdOf(current)
            if (dropped != CONVERSATION) {
                visited.remove(dropped)
                places.remove(dropped)
            }
            if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
            replaceBackStack(conversation, prefixed)
            current = conversation
            releaseLeftHosts()
            trimComposedClashes()
        } else {
            compactPrefix(conversation.toList())?.let { replaceBackStack(conversation, it) }
        }
    }

    /**
     * Unfolding. Removes a compact conversation prefix from every fixed-place list without rebuilding
     * those entries. Returns true when the list in front was one of them, so the caller skips
     * [regularPromotion]. Returns false when the conversation list is in front: a compact push on
     * that list still uses [regularPromotion], which builds a new host.
     */
    fun unfoldPrefixedPlace(): Boolean {
        val prefixedCurrent = current !== conversation &&
            current.firstOrNull() == AssistantRoute && current.size >= 2
        places.values.forEach { list ->
            if (list.firstOrNull() == AssistantRoute && list.size >= 2) list.removeAt(0)
        }
        return prefixedCurrent
    }

    /**
     * A compact drawer pick or route back onto a fixed place that already has a host.
     * The place entry stays on that list, with the conversation inserted under it, so the page is
     * not rebuilt and the conversation list does not compose the same route.
     * Returns false when there is no such host; the caller then pushes on the conversation list.
     */
    fun showRetainedCompact(root: NavKey, path: List<NavKey>): Boolean {
        if (!retainsSidebarComposition(root) || path.isEmpty()) return false
        val id = sidebarHostId(root)
        val list = places[id] ?: return false
        if (list.firstOrNull() == AssistantRoute && list.size > 1) list.removeAt(0)
        replaceBackStack(list, path)
        if (list.firstOrNull() != AssistantRoute) list.add(0, AssistantRoute)
        if (id !in visited) visited.add(id)
        if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
        current = list
        releaseLeftHosts()
        trimComposedClashes()
        return true
    }

    /**
     * Compact Back from a prefixed fixed place: show the conversation and keep the place list,
     * without popping its page (the entry stays on that host). Returns false when Back should pop.
     */
    fun backFromCompactPrefix(regular: Boolean): Boolean {
        if (regular || current === conversation) return false
        if (current.size != 2 || current.firstOrNull() != AssistantRoute) return false
        val place = current[1]
        if (!retainsSidebarComposition(place)) return false
        current.removeAt(0)
        current = conversation
        if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
        return true
    }

    /**
     * Land on the conversation list for a compact drawer push or "show the conversation".
     * A retained place keeps its list, with the compact prefix removed.
     */
    fun showConversation() {
        if (current !== conversation) {
            if (current.firstOrNull() == AssistantRoute && current.size > 1) current.removeAt(0)
            current = conversation
        }
        if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
        releaseLeftHosts()
    }

    /** Another assistant: drop every place host. The caller resets the conversation stack. */
    fun forgetPlaces() {
        places.clear()
        visited.clear()
        visited.add(CONVERSATION)
        current = conversation
    }

    /** Remove a disabled optional place, its drill-downs and its retained composition, leaving every other place alone. */
    fun forgetPlace(root: NavKey) {
        val id = sidebarHostId(root)
        val wasCurrent = hostIdOf(current) == id
        places.remove(id)
        visited.remove(id)
        val at = conversation.indexOfFirst { it == root }
        if (at >= 0) {
            while (conversation.size > at) conversation.removeAt(conversation.lastIndex)
            if (conversation.isEmpty()) conversation.add(AssistantRoute)
        }
        if (wasCurrent) current = conversation
        if (conversation.isEmpty()) conversation.add(AssistantRoute)
        if (CONVERSATION !in visited) visited.add(0, CONVERSATION)
    }

    private fun hostIdOf(stack: MutableList<NavKey>): String {
        if (stack === conversation) return CONVERSATION
        val root = stack.firstOrNull { it != AssistantRoute } ?: stack.firstOrNull() ?: return CONVERSATION
        return sidebarHostId(root)
    }

    /** Fixed places and the conversation stay. The host in front stays. Others leave. */
    private fun releaseLeftHosts() {
        val currentId = hostIdOf(current)
        visited.toList().forEach { id ->
            if (id == CONVERSATION || id == currentId) return@forEach
            val root = stack(id)?.firstOrNull { it != AssistantRoute } ?: stack(id)?.firstOrNull()
            if (root == null || !retainsSidebarComposition(root)) {
                visited.remove(id)
                places.remove(id)
            }
        }
    }

    /**
     * Navigation 3 composes each host's top entry. Pop a host that is not in front while its top is
     * also on the visible stack, so that route is not composed twice. A stack is never emptied:
     * its last entry stays even when that entry is covered on the visible stack.
     */
    internal fun trimComposedClashes() {
        val visible = current.toList()
        fun trim(stack: MutableList<NavKey>) {
            if (stack === current) return
            while (stack.size > 1 && stack.last() in visible) stack.removeAt(stack.lastIndex)
        }
        trim(conversation)
        places.values.toList().forEach(::trim)
    }

    companion object {
        const val CONVERSATION = "conversation"
    }
}
