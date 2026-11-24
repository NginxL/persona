package persona.core.domain.sort

import persona.core.contracts.AgentState
import persona.core.contracts.FoldedAs
import persona.core.contracts.Interaction
import persona.core.contracts.Session
import persona.core.contracts.SessionChildren
import persona.core.contracts.SessionKey

/**
 * Side sessions (#7944): a Claude desktop app's side chat or /btw question folds under its topmost listed parent on the
 * same machine (`folded_as: "side"`) and lives in that parent's chat. The hub decides the fold ([Session.foldedUnder]);
 * these rules are the client's part: which rows are a side's, the panel's order and the rollup the parent shows.
 */
object SideFold {
    /** The hub's hello lists it when every manager serving its machines folds side sessions. */
    const val FEATURE = "sessions_side_fold"
}

/** A side session folded under its topmost listed parent. */
val Session.isFoldedSide: Boolean get() = foldedAs == FoldedAs.SIDE && !foldedUnder.isNullOrBlank()

/** The side whose chat holds this row: the row itself when it is one, else the side it folds through ([Session.foldedSide]). */
val Session.sideHome: String? get() = foldedSide?.takeIf(String::isNotBlank) ?: sessionId.takeIf { isFoldedSide }

/** A card from a side session itself (not from a row folded through it). */
val Interaction.fromSide: Boolean get() = foldedAs == FoldedAs.SIDE && !foldedUnder.isNullOrBlank()

/** The side whose own chat shows this card: the side that asks, or the side its asking row folds through; null for none. */
val Interaction.sideHome: SessionKey? get() =
    foldedSide?.takeIf(String::isNotBlank)?.let { SessionKey(machineId, it) } ?: sessionKey.takeIf { fromSide }

/**
 * A side's state in its parent's panel: needs you when it or a row folded through it needs the user, works when either
 * works, else its own.
 */
fun sideState(side: Session, rows: List<Session>): AgentState {
    val own = side.agentState
    if (own == AgentState.NeedsYou || own == AgentState.Working) return own
    val through = rows.filter { it.machineId == side.machineId && it.foldedSide == side.sessionId && it.key != side.key }
    return when {
        through.any { it.agentState == AgentState.NeedsYou } -> AgentState.NeedsYou
        through.any { it.agentState == AgentState.Working } -> AgentState.Working
        else -> own
    }
}

/**
 * The side sessions the app holds folded under [parent], flat (sides of sides too), in the panel's order: needs you
 * first, then working, then by last activity, newest first; stopped ones last.
 */
fun sideSessions(rows: List<Session>, parent: SessionKey): List<Session> {
    val sides = rows.filter { it.machineId == parent.machineId && it.foldedUnder == parent.sessionId && it.isFoldedSide && it.key != parent }
        .distinctBy { it.key }
    val states = sides.associate { it.key to sideState(it, rows) }
    fun rank(side: Session) = when {
        states[side.key] == AgentState.NeedsYou -> 0
        states[side.key] == AgentState.Working -> 1
        !side.isLive -> 3
        else -> 2
    }
    return sides.sortedWith(compareBy<Session> { rank(it) }.thenByDescending { it.updated }.thenBy { it.sessionId })
}

/** What the parent's Side sessions control says: how many, and whether one needs the user. */
data class SideRollup(val count: Int = 0, val needsYou: Boolean = false)

/**
 * From the sides the app holds ([held], [sideSessions]), so that marking one seen or answering it shows at once; from
 * the hub's `children.sides*` only for a row whose sides it does not hold (a paged row).
 */
fun sideRollup(parent: Session?, held: List<Session>, rows: List<Session> = held): SideRollup = when {
    held.isNotEmpty() -> SideRollup(held.size, held.any { sideState(it, rows) == AgentState.NeedsYou })
    else -> parent?.children?.let { SideRollup(it.sides, it.sidesNeedsYou > 0) } ?: SideRollup()
}

/**
 * The rows ([AgentSession.id]) a search keeps for a folded side the query finds (#7944): a side is never a row, so the
 * hit is its topmost parent's row, which [listed] (the folded list) holds. Empty without a query.
 */
fun sideSearchHits(rows: List<AgentSession>, listed: List<AgentSession>, settings: AgentListSettings,
    machineNames: Map<String, String> = emptyMap()): Set<String> {
    if (settings.query.isEmpty()) return emptySet()
    val shown = listed.mapTo(HashSet()) { it.session.key }
    return rows.mapNotNull { row ->
        val s = row.session
        val parent = s.foldedUnderKey?.takeIf { s.sideHome != null }
        parent?.takeIf { it in shown && settings.admits(s, machineNames = machineNames) }?.path
    }.toSet()
}

/**
 * The side session a search found inside [parent]'s row, so that opening it lands with that side shown (#7944): when
 * the row did not match by itself, the first of its held sides the query finds. Null without a query or a side hit.
 * The hub's ranked search (`search=`, whose `search_hits[…].hit_session_id` names the side) is not one this app reads
 * yet; a paged list's `q` finds a parent by its own fields only.
 */
fun AgentListSettings.sideHit(parent: Session, rows: List<Session>, machineNames: Map<String, String> = emptyMap()): SessionKey? {
    if (query.isEmpty()) return null
    val plain = copy(queryAncestors = emptySet())
    if (plain.admits(parent, machineNames = machineNames)) return null
    return sideSessions(rows, parent.key).firstOrNull { plain.admits(it, machineNames = machineNames) }?.key
}

/**
 * The parent's rollup with the sides the list holds counted by their states as the list shows them (seen ones read),
 * in place of the hub's side counts; unchanged when it holds none. The other folded rows keep the hub's counts.
 */
internal fun Session.withHeldSides(held: List<Session>): Session {
    if (held.isEmpty()) return this
    val hub = children ?: SessionChildren()
    val needsYou = held.count { it.agentState == AgentState.NeedsYou }
    val working = held.count { it.agentState == AgentState.Working }
    val next = hub.copy(
        total = (hub.total - hub.sides).coerceAtLeast(0) + held.size,
        working = (hub.working - hub.sidesWorking).coerceAtLeast(0) + working,
        needsYou = (hub.needsYou - hub.sidesNeedsYou).coerceAtLeast(0) + needsYou,
        sides = held.size, sidesWorking = working, sidesNeedsYou = needsYou,
        sidesUnread = held.count { it.agentState == AgentState.Unread },
    )
    return if (next == children) this else copy(children = next)
}
