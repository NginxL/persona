package persona.android.navigation

import androidx.navigation3.runtime.NavKey
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.InboxItemRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.LarkChatRoute
import persona.feature.inbox.InboxItemLookupResult
import persona.feature.inbox.InboxItemPages
import persona.feature.inbox.InboxLanding
import persona.core.inbox.InboxRecord
import persona.feature.inbox.InboxRepository
import persona.feature.inbox.LarkRoute
import persona.feature.inbox.inboxRowPage
import persona.feature.inbox.readInboxItem

/**
 * Deep links name an inbox item or a source chat; open them as the inbox list would (an event opens its event page first;
 * other items retain their source or extension page, [pages]). Null means the records do not show the target yet;
 * [loaded] says no later refresh will.
 */
internal fun inboxDestination(key: NavKey, records: List<InboxRecord>, loaded: Boolean, pages: InboxItemPages = InboxItemPages()): InboxDestination? = when (key) {
    is InboxItemRoute -> records.firstOrNull { it.item.id == key.itemId }?.let { record -> InboxDestination.Open(inboxRowPage(record, pages)) }
        ?: InboxDestination.Missing.takeIf { loaded }
    // A chat without a current item still opens at its latest messages.
    is LarkChatRoute -> records.firstOrNull { it.feedId == key.chatId }
        ?.let { InboxDestination.Open(LarkRoute(key.chatId, it.item.id, it.startPosition)) }
        ?: InboxDestination.Open(LarkRoute(key.chatId)).takeIf { loaded }
    else -> null
}

/**
 * A listed item first, then a single read for a historical or not-yet-listed one. A direct chat link never needs that read.
 * [found] hears the item it opens, when there is one, so the Inbox list can move to where it is (#9053).
 */
internal suspend fun inboxDestinationWithLookup(
    key: NavKey,
    records: List<InboxRecord>,
    loaded: Boolean,
    pages: InboxItemPages,
    repository: InboxRepository,
    found: (InboxRecord) -> Unit = {},
): InboxDestination? {
    if (key !is InboxItemRoute) return inboxDestination(key, records, loaded, pages)
        ?.also { if (it is InboxDestination.Open && key is LarkChatRoute) records.firstOrNull { r -> r.feedId == key.chatId }?.let(found) }
    records.firstOrNull { it.item.id == key.itemId }?.let { found(it); return InboxDestination.Open(inboxRowPage(it, pages)) }
    if (!loaded) return null
    return when (val result = readInboxItem(repository, key.itemId)) {
        is InboxItemLookupResult.Found -> InboxDestination.Open(inboxRowPage(result.record, pages)).also { found(result.record) }
        InboxItemLookupResult.Missing -> InboxDestination.Missing
        is InboxItemLookupResult.Failed -> InboxDestination.Failed
    }
}

internal sealed interface InboxDestination {
    data class Open(val key: NavKey) : InboxDestination
    data object Missing : InboxDestination
    data object Failed : InboxDestination
}

/**
 * Keeps each stack's landing in step with what the stack holds (#9053). A stack that no longer holds an Inbox list drops
 * its landing: the list under the item that set it was closed without showing (the drawer, a tab's root, a link that
 * went home), so nothing will take it, and a list entered later must open on Needs you, not where that item was. A
 * stack that still holds it puts back the choice of a list moved beside a page that has now closed.
 *
 * On the Persona tab the Inbox host stays composed while To-dos or the conversation is in front (#9667). [personaHosts]
 * is every host list. The landing follows the Inbox host, not whichever root is visible, so leaving that host does not
 * clear it, restore a page that is still on it, or drop a landing the list has not taken. iOS does not clear
 * `InboxBarLanding` when another root is in front. An empty [personaHosts] keeps the old rule: the visible stack alone.
 */
internal fun settleLandings(
    stacks: Map<AppTab, List<NavKey>>,
    landings: Map<AppTab, InboxLanding>,
    personaHosts: List<List<NavKey>> = emptyList(),
) {
    stacks.forEach { (tab, stack) ->
        val landing = landings[tab] ?: return@forEach
        val held = if (tab == AppTab.Persona) inboxHostStack(stack, personaHosts) else stack
        if (held != null && InboxRoute in held) landing.closed(held) else landing.clear()
    }
}

/**
 * The composed Inbox host, or the visible stack when the shell has no separate host list.
 * [hosts] follows [SidebarRootHosts.allStacks]: the conversation first, then each place host.
 * A compact place host is `[AssistantRoute, InboxRoute, …]`, so it does not start at the Inbox.
 * Prefer a place host over the conversation, or `closed` runs against the conversation and a
 * `returns` page still on the Inbox host counts as gone. Fall back to the conversation only when
 * no place host holds the Inbox. An empty [hosts] keeps the visible stack. Null when none hold it.
 */
internal fun inboxHostStack(visible: List<NavKey>, hosts: List<List<NavKey>>): List<NavKey>? {
    if (hosts.isEmpty()) return visible
    fun holds(stack: List<NavKey>) = stack.firstOrNull { it != AssistantRoute } == InboxRoute
    val places = hosts.drop(1).filter(::holds)
    places.firstOrNull { it.firstOrNull() == InboxRoute }?.let { return it }
    places.firstOrNull()?.let { return it }
    hosts.firstOrNull()?.takeIf(::holds)?.let { return it }
    if (InboxRoute in visible) return visible
    return null
}

/**
 * The Inbox picked in the sidebar (#9481, #9667). iOS `inboxPicked` (AssistantView.swift:633–640):
 * `.none` and `.popToRoot` bump `model.inboxEntries` (InboxView.swift:206 `bar.enter`); `.switchRoot`
 * with a kept path calls `InboxBarLanding.broughtBack` (InboxBar.swift:222–224) and the new page lands
 * on that item (InboxView.swift:207–212, `land` at 977–981); an empty `.switchRoot` is a new page that
 * starts on Needs you; `.drillDown` does nothing. The phone drawer is `show` (AssistantView.swift:567–569):
 * no kept path bumps `inboxEntries`, a kept path calls `broughtBack`.
 *
 * Android keeps the Inbox host composed, so an empty switch also [InboxLanding.enter]s (Needs you).
 * A kept path enters, then [InboxLanding.land]s when the page is an item: iOS builds a new list on
 * Needs you and then lands (`InboxView.swift:209–212`), so a list left on All still moves. No item
 * on that path (a waiting-agent chat): enter only.
 */
internal fun inboxSidebarPicked(step: SidebarPageRoots.Step, landing: InboxLanding, item: InboxRecord?) {
    if (step.root != InboxRoute) return
    when (step.move) {
        SidebarPageRoots.Move.None, SidebarPageRoots.Move.PopToRoot -> landing.enter()
        SidebarPageRoots.Move.SwitchRoot -> {
            landing.enter()
            if (step.path.isNotEmpty()) item?.let(landing::land)
        }
        SidebarPageRoots.Move.DrillDown -> Unit
    }
}

/**
 * A phone-drawer pick of the Inbox (`AssistantView.show`, AssistantView.swift:561–569).
 * [host] is the retained list, or null when the place has no host yet. [inFront] means that list is showing.
 * Drills above the Inbox are the kept path. Already in front, or nothing kept: the root, which
 * [inboxSidebarPicked] enters on Needs you. Left with an item: that path, which lands on the item.
 */
internal fun compactInboxPick(host: List<NavKey>?, inFront: Boolean): SidebarPageRoots.Step {
    val drills = host.orEmpty().filter { it != AssistantRoute && it != InboxRoute }
    val kept = !inFront && drills.isNotEmpty()
    val move = when {
        inFront && drills.isEmpty() -> SidebarPageRoots.Move.None
        inFront -> SidebarPageRoots.Move.PopToRoot
        else -> SidebarPageRoots.Move.SwitchRoot
    }
    return SidebarPageRoots.Step(InboxRoute, if (kept) drills else emptyList(), move)
}

/**
 * The open item on a kept Inbox path. Walks from the top down and takes the first id [inboxItemId]
 * knows (the same pages [AppNavigator] treats as an item, including one under a deeper page).
 * The snapshot wins; [InboxLanding.lastLanded] covers a history or Done item the link already read.
 */
internal fun inboxRecordOnPath(path: List<NavKey>, records: List<InboxRecord>, landing: InboxLanding): InboxRecord? {
    val id = path.asReversed().firstNotNullOfOrNull { it.inboxItemId() } ?: return null
    return records.firstOrNull { it.item.id == id } ?: landing.lastLanded?.takeIf { it.item.id == id }
}

/**
 * What Root's sidebar pick does (#9667). Regular width asks [SidebarPageRoots.select]. The phone
 * drawer keeps a fixed place on its host ([SidebarRootHosts.showRetainedCompact]); the Inbox keeps
 * an item already open on that host.
 */
internal fun applySidebarDestination(
    key: NavKey,
    regular: Boolean,
    pageRoots: SidebarPageRoots,
    hosts: SidebarRootHosts,
    assistantStack: MutableList<NavKey>,
    showHost: (NavKey, List<NavKey>) -> Unit,
    assignPersona: (MutableList<NavKey>) -> Unit,
    landing: InboxLanding,
    records: List<InboxRecord>,
) {
    pageRoots.markOpenedAsRoot(key)
    if (!regular) {
        if (key == InboxRoute) {
            val host = hosts.stack(sidebarHostId(InboxRoute))
            val step = compactInboxPick(host?.toList(), inFront = host != null && hosts.current === host)
            if (!hosts.showRetainedCompact(InboxRoute, listOf(InboxRoute) + step.path)) {
                hosts.showConversation()
                pushCompactSidebarPlace(assistantStack, InboxRoute)
            }
            assignPersona(hosts.current)
            pageRoots.adoptVisible(AssistantRoute)
            inboxSidebarPicked(step, landing, inboxRecordOnPath(step.path, records, landing))
            return
        }
        if (!hosts.showRetainedCompact(key, listOf(key))) {
            hosts.showConversation()
            pushCompactSidebarPlace(assistantStack, key)
        }
        assignPersona(hosts.current)
        pageRoots.adoptVisible(AssistantRoute)
        return
    }
    val step = pageRoots.select(key, hosts.current.drop(1))
    showHost(step.root, step.stack)
    assignPersona(hosts.current)
    if (key == InboxRoute) inboxSidebarPicked(step, landing, inboxRecordOnPath(step.path, records, landing))
}
