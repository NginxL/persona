package persona.core.domain.sort

import persona.core.contracts.AgentState
import persona.core.contracts.Session

// The agents of one herdr tab (#2320, docs/protocol/agent-states.md "Several panes in one tab"): an agent that splits its tab and
// starts more agents in the new panes works with them on one thing, so the clients show them as a group. The order and
// the initiator match the web (web/src/lib/herdr-tree.ts paneOrder, launcherOf).

/**
 * The order herdr opened the pane in, from its id: herdr counts its panes up in base 36 ("w4R:pW", "w4R:p2E"), Herdr
 * App numbers its terminals in decimal ("herdr-core:t1"). Ids without a number ("botmux:<uuid>") go last.
 */
fun paneNumber(session: Session): Long {
    val id = session.paneId.orEmpty()
    HerdrPane.find(id)?.let { match -> match.groupValues[1].lowercase().toLongOrNull(36)?.let { return it } }
    return TrailingNumber.find(id)?.groupValues?.get(1)?.toLongOrNull() ?: Long.MAX_VALUE
}

// Compiled once: paneNumber runs inside sort comparators, and compiling them per call froze the Agents list.
private val HerdrPane = Regex(":p([0-9A-Za-z]+)$")
private val TrailingNumber = Regex("(\\d+)$")

/**
 * A tab's panes in their order: their place in the tab's layout (`pane_index`; panes without one after), then the
 * order herdr opened them in ([paneNumber]), then the session id.
 */
val PaneOrder: Comparator<Session> = compareBy<Session> { it.paneIndex ?: Int.MAX_VALUE }
    .thenBy { paneNumber(it) }.thenBy { it.sessionId }

/**
 * The tab's initiator: the agent in its first pane (`pane_index` 0, top left, the one the others were split from). None
 * when the tab has one agent, or that pane is a shell or not placed: the agents are then peers.
 */
fun <T> launcherOf(panes: List<T>, session: (T) -> Session): T? =
    if (panes.size > 1) panes.firstOrNull { session(it).paneIndex == 0 } else null

/**
 * What a tab's row speaks for (docs/protocol/agent-states.md "Unified states", as the web): needs you > working > unread > awaiting
 * reply (#5096, not muted) > just finished (#9786, not muted) > seen.
 */
private fun tabUrgency(row: AgentSession, mutes: AgentPreferences): Int = when {
    row.awaitsReply(mutes) -> TabUrgency.indexOf(AgentState.Unread) + 1
    row.justFinished(mutes) -> TabUrgency.indexOf(AgentState.Unread) + 2
    else -> TabUrgency.indexOf(row.state).let { if (it > TabUrgency.indexOf(AgentState.Unread)) it + 2 else it }
}
private val TabUrgency = listOf(AgentState.NeedsYou, AgentState.Working, AgentState.Unread, AgentState.Idle, AgentState.Unknown, AgentState.Offline)

/**
 * The tab's name as the clients show it (iOS `Session.slotLabel`, web `tabLabel`): its herdr name, `#n` for herdr's
 * default number (a bare "2" reads as a count); null when the host gives neither.
 */
val Session.slotLabel: String? get() {
    val name = slotName?.takeIf(String::isNotBlank)
    val index = slotIndex
    return when {
        name == null -> index?.let { "#$it" }
        index != null && name == index.toString() -> "#$index"
        else -> name
    }
}

/** Which tab a session's pane sits in (same machine, space and slot); null for a host without tabs. */
val Session.tabKey: String? get() = if (slotId.isNullOrEmpty() || spaceId.isNullOrEmpty()) null
    else "$machineId/${spaceKind.orEmpty()}:$spaceId/$slotId"

/**
 * One tab's agents as they are listed together. [members] are in pane order ([PaneOrder]); [launcher] is the initiator
 * when the tab says; [lead] is the most urgent, the one the tab's row speaks for and opens. [foldKey] keeps whether the
 * group is folded by machine / space name / tab number, not the tab's name: a tab is renamed (the hub names a `#2` after its
 * task), two tabs may share a name, and herdr keeps the numbers across a restart (its ids it does not). A host without
 * numbers uses the tab's id.
 */
data class AgentTab(val key: String, val members: List<AgentSession>, val mutes: AgentPreferences = AgentPreferences()) {
    val launcher: AgentSession? get() = launcherOf(members) { it.session }
    /** What the tab's row speaks for: its most urgent agent ([tabUrgency] with [mutes]), the first in pane order on a tie. */
    val lead: AgentSession get() = members.minWith(compareBy<AgentSession> { tabUrgency(it, mutes) }.thenBy { members.indexOf(it) })
    val foldKey: String get() = members.first().session.let { s ->
        "tab:${s.machineId}/${s.spaceName ?: s.spaceId.orEmpty()}/${s.slotIndex?.toString() ?: s.slotId.orEmpty()}"
    }
    /** A pane the initiator split off: drawn under it. */
    fun isBranch(row: AgentSession): Boolean = launcher.let { it != null && it.id != row.id }
}

/** The tabs among [rows] that hold more than one agent, by [AgentTab.key]; each tab's agents in pane order. */
fun agentTabs(rows: List<AgentSession>, mutes: AgentPreferences = AgentPreferences()): Map<String, AgentTab> = rows
    .mapNotNull { row -> row.session.tabKey?.let { it to row } }
    .groupBy({ it.first }, { it.second })
    .filterValues { it.size > 1 }
    .mapValues { (key, members) -> AgentTab(key, members.sortedWith(compareBy(PaneOrder) { it.session }), mutes) }
