package persona.android.navigation

import androidx.compose.animation.ContentTransform
import androidx.compose.animation.EnterTransition
import androidx.compose.animation.ExitTransition
import androidx.navigation3.runtime.NavKey
import androidx.navigation3.scene.Scene
import persona.core.contracts.SessionKey
import persona.core.navigation.AppTab
import persona.core.navigation.AssistantRoute
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.ExtensionUiRoute
import persona.core.navigation.InboxRoute
import persona.core.navigation.InteractionRoute
import persona.core.navigation.LarkMessagesRoute
import persona.core.navigation.MachinesRoute
import persona.core.navigation.TerminalRoute
import persona.core.navigation.TodoDetailRoute
import persona.core.navigation.TodosRoute
import persona.feature.assistant.AssistantSidebarShade
import persona.feature.knowledge.KnowledgeKindRoute
import persona.feature.knowledge.KnowledgeRoute

/**
 * The roots of the Assistant page beside the sidebar column, and the drill-down each was left with (#9667, iOS #8359).
 *
 * Pure: the shell asks what a pick means and applies [Step]. Copied from
 * `apple/Persona/Views/Shell/SidebarPageRoots.swift` (`select` 88–97, `drill` 100–104, `forget` 108–111,
 * `animated` / `closesOverlay` 72–76). A switch of root never animates; a drill-down's push and a re-pick's pop do.
 * The phone drawer does not use this class.
 */
internal class SidebarPageRoots {
    enum class Move { None, PopToRoot, SwitchRoot, DrillDown }

    /**
     * @param root the page's root after the step
     * @param path drill-downs above [root], not including it
     */
    data class Step(val root: NavKey, val path: List<NavKey>, val move: Move) {
        /** Only a drill-down's push and a re-pick's pop animate (SidebarPageRoots.swift:72–74). */
        val animated: Boolean get() = move == Move.PopToRoot || move == Move.DrillDown
        /** Any sidebar pick but a drill-down slides an overlaid column away (SidebarPageRoots.swift:75–76). */
        val closesOverlay: Boolean get() = move != Move.DrillDown
        val stack: List<NavKey> get() = listOf(root) + path
    }

    /** The root in front. Starts as the main conversation (`SidebarPageRoots.start`). */
    var current: NavKey = AssistantRoute
        private set

    private val saved = mutableMapOf<NavKey, List<NavKey>>()

    /**
     * Places opened as their own root (a sidebar or drawer pick, or a route to that place).
     * A drill opened from the conversation is not here, so unfolding does not promote it (#9667).
     */
    private val openedAsRootIds = mutableSetOf<String>()

    /**
     * [root] is picked while the page shows [path] over the current root.
     * [contentChanged]: the root is the one in front but shows something else (another conversation).
     */
    fun select(root: NavKey, path: List<NavKey>, contentChanged: Boolean = false): Step {
        if (root == current) {
            saved.remove(root)
            if (contentChanged) return Step(root, emptyList(), Move.SwitchRoot)
            return Step(root, emptyList(), if (path.isEmpty()) Move.None else Move.PopToRoot)
        }
        if (path.isEmpty()) saved.remove(current) else saved[current] = path
        current = root
        val restored = saved.remove(root) ?: emptyList()
        return Step(root, restored, Move.SwitchRoot)
    }

    /** [value] opened from inside the page in front, over [path]: a push on its root. Does not change [saved]. */
    fun drill(value: NavKey, over: List<NavKey>): Step =
        Step(current, over + value, Move.DrillDown)

    /** The user opened [key] as a root (sidebar, drawer, or a route to that place), not as a conversation drill. */
    fun markOpenedAsRoot(key: NavKey) {
        openedAsRootIds += sidebarHostId(key)
    }

    fun openedAsRoot(key: NavKey): Boolean = sidebarHostId(key) in openedAsRootIds

    /** Another assistant: back to the conversation, nothing kept (SidebarPageRoots.swift:108–111). */
    fun forget(): Step {
        current = AssistantRoute
        saved.clear()
        openedAsRootIds.clear()
        return Step(AssistantRoute, emptyList(), Move.SwitchRoot)
    }

    /** Forget one hidden optional place without disturbing the other retained roots. */
    fun forgetPlace(root: NavKey) {
        saved.remove(root)
        openedAsRootIds.remove(sidebarHostId(root))
        if (current == root) current = AssistantRoute
    }

    /** A width change shows [root] without treating it as a pick, so saved drill-downs stay. */
    fun adoptVisible(root: NavKey) {
        current = root
    }

    fun keeps(root: NavKey): Boolean = saved[root] != null
}

/**
 * Live composition while another root is in front: the conversation and the fixed sidebar places.
 * A to-do or an extension page keeps its path in [SidebarPageRoots] and is composed only while it
 * is in front. iOS keeps paths, not live views (docs/design/sidebar-navigation.md).
 */
internal fun retainsSidebarComposition(root: NavKey): Boolean = when (root) {
    AssistantRoute, InboxRoute, TodosRoute, KnowledgeRoute, MachinesRoute, LarkMessagesRoute, SidebarScheduleRoute -> true
    is KnowledgeKindRoute -> true
    else -> false
}

/** A sidebar place that is its own root at regular width. A pushed extension page (`depth > 0`) is a drill-down. */
internal fun isSidebarPlaceRoot(key: NavKey): Boolean = when (key) {
    InboxRoute, TodosRoute, KnowledgeRoute, MachinesRoute, LarkMessagesRoute, SidebarScheduleRoute -> true
    is KnowledgeKindRoute -> true
    is TodoDetailRoute -> true
    is ExtensionUiRoute -> key.depth == 0
    else -> false
}

/**
 * The shaded row for [root] (iOS `UnifiedSidebar.shownPlace`, UnifiedSidebar.swift:39–44).
 * [AssistantSidebarShade.None] is a root with no sidebar row (a to-do opened as its own root).
 */
internal fun sidebarShadeKey(root: NavKey): String = when (root) {
    AssistantRoute -> AssistantSidebarShade.Conversation
    InboxRoute -> AssistantSidebarShade.Inbox
    LarkMessagesRoute -> AssistantSidebarShade.Messages
    TodosRoute -> AssistantSidebarShade.Todos
    KnowledgeRoute -> AssistantSidebarShade.Knowledge
    is KnowledgeKindRoute -> AssistantSidebarShade.Knowledge
    MachinesRoute -> AssistantSidebarShade.Machines
    SidebarScheduleRoute -> AssistantSidebarShade.Schedule
    is ChatRoute -> if (root.kind == ConversationKind.Assistant) {
        AssistantSidebarShade.Conversation
    } else {
        AssistantSidebarShade.agent(SessionKey(root.machineId, root.sessionId))
    }
    is InteractionRoute -> sidebarShadeKey(root.chat)
    is TerminalRoute -> AssistantSidebarShade.agent(SessionKey(root.machineId, root.sessionId))
    is ExtensionUiRoute -> if (root.depth == 0) extensionSidebarKey(root.extensionId, root.uiId) else AssistantSidebarShade.None
    else -> AssistantSidebarShade.None
}

/**
 * The one shade for the visible page. An Agent opened from inside Persona is a drill-down and keeps Persona's root;
 * a direct Agents destination has its own key. Compact Persona keeps its existing conversation shading.
 */
internal fun visibleSidebarShade(
    regular: Boolean,
    selected: AppTab,
    personaRoot: NavKey,
    visible: NavKey?,
    visibleSource: AppTab?,
): String? = when {
    selected == AppTab.Agents && visibleSource == AppTab.Persona -> sidebarShadeKey(personaRoot)
    selected == AppTab.Agents -> visible?.let(::sidebarShadeKey) ?: AssistantSidebarShade.None
    regular -> sidebarShadeKey(personaRoot)
    else -> null
}

/** The same key the sidebar's extension row uses (`Root.extensionRailKey`). */
internal fun extensionSidebarKey(extensionId: String, uiId: String) = "extension:$extensionId/$uiId"

/**
 * True when the persona stack's root changed, so the page must not push, pop or fade
 * (UnifiedSidebar.swift:67–74 disables animations; SidebarPageRoots.Step.animated is false for a switch).
 * The root is the first entry of the whole stack: `previousEntries` then the visible entries
 * (SinglePaneScene keeps the entries under the top in `previousEntries`, in stack order).
 */
internal fun personaSceneRootSwapped(regular: Boolean, persona: Boolean, from: Scene<NavKey>, to: Scene<NavKey>): Boolean {
    if (!regular || !persona) return false
    val fromRoot = sceneRootContentKey(from) ?: return false
    val toRoot = sceneRootContentKey(to) ?: return false
    return fromRoot != toRoot
}

private fun sceneRootContentKey(scene: Scene<NavKey>): Any? =
    (scene.previousEntries.firstOrNull() ?: scene.entries.firstOrNull())?.contentKey

/** No page transition. Not a new motion token: iOS turns the navigation animation off for a root switch. */
internal fun noPageMotion(): ContentTransform =
    ContentTransform(EnterTransition.None, ExitTransition.None, sizeTransform = null)

/** Drop the uncommon suffix, then append, so one gesture is one stack value (not a pop followed by a push). */
internal fun replaceBackStack(stack: MutableList<NavKey>, next: List<NavKey>) {
    if (next.isEmpty() || stack == next) return
    var shared = 0
    while (shared < stack.size && shared < next.size && stack[shared] == next[shared]) shared++
    while (stack.size > shared) stack.removeAt(stack.lastIndex)
    for (index in shared until next.size) stack.add(next[index])
}

/**
 * The phone drawer, unchanged: pop to the conversation, then push the place
 * (docs/design/sidebar-navigation.md section 6, "the phone's drawer … behaves as before").
 */
internal fun pushCompactSidebarPlace(stack: MutableList<NavKey>, key: NavKey) {
    while (stack.size > 1) stack.removeAt(stack.lastIndex)
    if (stack.isEmpty()) stack.add(AssistantRoute)
    if (stack.last() != key) stack.add(key)
}

/** Folding: put the conversation under a place root so the drawer's Back still reaches it. Returns null when already that shape. */
internal fun compactPrefix(stack: List<NavKey>): List<NavKey>? {
    val root = stack.firstOrNull() ?: return null
    if (root == AssistantRoute || !isSidebarPlaceRoot(root)) return null
    return listOf(AssistantRoute) + stack
}

/**
 * Unfolding: a compact stack `[conversation, place, …drills]` becomes `[place, …drills]`
 * only when [openedAsRoot] says that place was opened as a root. A to-do or extension page
 * opened from the conversation stays a drill with Back (ShellChat opens `TodoDetailRoute`).
 * Returns null when the entry above the conversation is not a sidebar place.
 */
internal fun regularPromotion(stack: List<NavKey>, openedAsRoot: (NavKey) -> Boolean = { true }): List<NavKey>? {
    if (stack.size < 2 || stack.first() != AssistantRoute || !isSidebarPlaceRoot(stack[1])) return null
    if (!openedAsRoot(stack[1])) return null
    return stack.drop(1)
}

/**
 * The sidebar place a regular-width route should switch to, or null when [key] stays a drill
 * on whatever root is in front. Inbox children are included because `restoreSecondaryParents`
 * would otherwise insert `InboxRoute` under another root (Router.kt). A to-do opened from the
 * conversation is not a place route (item 6).
 */
internal fun placeRootFor(key: NavKey): NavKey? = when {
    key == LarkMessagesRoute -> LarkMessagesRoute
    isInboxDestination(key) -> InboxRoute
    key == TodosRoute -> TodosRoute
    key == KnowledgeRoute -> KnowledgeRoute
    key is KnowledgeKindRoute -> key
    key == MachinesRoute -> MachinesRoute
    else -> null
}

/** Stable id for the NavDisplay that keeps [root] composed while another root is in front. */
internal fun sidebarHostId(root: NavKey): String = when (root) {
    AssistantRoute -> SidebarRootHosts.CONVERSATION
    InboxRoute -> "inbox"
    LarkMessagesRoute -> "lark-messages"
    TodosRoute -> "todos"
    KnowledgeRoute -> "knowledge"
    is KnowledgeKindRoute -> "knowledge:${root.kind.wire}"
    MachinesRoute -> "machines"
    SidebarScheduleRoute -> "schedule"
    is TodoDetailRoute -> "todo:${root.todoId}"
    is ExtensionUiRoute -> "extension:${root.extensionId}/${root.uiId}"
    else -> "other:${root::class.qualifiedName ?: "route"}"
}

/** The leading control of a place root at regular width: the conversation's sidebar button, never Back. */
internal fun showsPlaceRootLeading(regular: Boolean, persona: Boolean, primary: Boolean, entryIsStackRoot: Boolean): Boolean =
    regular && persona && !primary && entryIsStackRoot
