package persona.core.domain.sort

import persona.core.contracts.AgentState
import persona.core.contracts.FoldedAs
import persona.core.contracts.GroupKeys
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime

/**
 * Whether [this] session belongs under [parent] (same-machine or cross-machine folded child, or direct
 * orphan subagent of [parent] such as when the parent is archived).
 */
fun Session.belongsToParent(parent: SessionKey): Boolean {
    if (key == parent) return false
    foldedUnderKey?.let { return it == parent }
    if (isSubagent || !askingParentSessionId.isNullOrBlank()) {
        parentKey?.let { if (it == parent) return true }
        val askingMachine = parentMachineId?.takeIf(String::isNotBlank) ?: machineId
        if (askingParentSessionId == parent.sessionId && askingMachine == parent.machineId) return true
    }
    return false
}

/**
 * Hides folded children (same-machine and cross-machine via [Session.foldedUnderMachineId]) whenever their parent is
 * listed, and filters out all `isSubagent` rows on authoritative hierarchy hubs (or non-legacy subtasks) as a
 * client-side fallback so subagents never leak into the main Agents list. Side sessions (#7944) fold the same way,
 * and the parent counts the sides this list holds by their states here ([withHeldSides]).
 */
fun foldRows(rows: List<AgentSession>, hierarchy: Boolean = false): List<AgentSession> {
    val listed = rows.mapTo(HashSet()) { it.session.key }
    val knownGroups = rows.filter {
        it.hierarchyKnown || !it.session.foldedUnder.isNullOrBlank() || it.session.children != null ||
            !it.session.foldedUnderMachineId.isNullOrBlank() || !it.session.parentMachineId.isNullOrBlank() ||
            !it.session.a2aTaskId.isNullOrBlank() || !it.session.subagentOrigin.isNullOrBlank()
    }.mapTo(HashSet()) { GroupKeys.namespaceOf(it.session.machineId) }
    val sides = rows.filter { it.session.isFoldedSide }
        .mapNotNull { row -> row.session.foldedUnderKey?.let { it to row } }
        .groupBy({ it.first }, { it.second })
    return rows.filterNot { row ->
        val s = row.session
        val foldedIntoListed = s.foldedUnderKey?.let { it in listed } == true
        val known = hierarchy || row.hierarchyKnown || GroupKeys.namespaceOf(s.machineId) in knownGroups
        foldedIntoListed || (s.isSubagent && (known || !s.isSubtask))
    }.map { row ->
        val known = hierarchy || GroupKeys.namespaceOf(row.session.machineId) in knownGroups
        val rolled = row.session.withHeldSides(sides[row.session.key].orEmpty().map { it.session }.filter { it.key != row.session.key })
        val state = rolled.foldedState
        val session = rolled.let { if (it.agentState == state) it else it.copy(state = state.wire) }
        if (session === row.session && (!known || row.hierarchyKnown)) row else row.copy(session = session, hierarchyKnown = known || row.hierarchyKnown)
    }
}

/** List and dock copies carry the folded state without changing the repository's own session rows. */
fun foldedSessions(rows: List<Session>, hierarchy: Boolean = false): List<Session> = foldRows(rows.map(::AgentSession), hierarchy).map { row ->
    row.session.let { if (it.agentState == row.state) it else it.copy(state = row.state.wire) }
}

/**
 * Every depth is already assigned to its topmost ancestor by the hub (same-machine or cross-machine via
 * `folded_under_machine_id`), plus direct orphan subagents of an archived parent. With side folding ([sides], #7944)
 * the sides and the rows folded through one are not the parent's cards: the sides are in its Side sessions, the rest
 * in their side's chat ([sideChildren]).
 */
fun childSessions(rows: List<Session>, parent: SessionKey, sides: Boolean = false): List<Session> = rows
    .filter { it.belongsToParent(parent) && !(sides && it.sideHome != null) }
    .sortedWith(compareBy<Session> { parseTime(it.startedAt ?: it.updatedAt) }.thenBy { it.sessionId }.thenBy { it.machineId })

/** The rows folded through [side] (its own Codex children and workers), the cards of its own chat. */
fun sideChildren(rows: List<Session>, side: SessionKey): List<Session> = rows
    .filter { it.machineId == side.machineId && it.foldedSide == side.sessionId && it.key != side }
    .sortedWith(compareBy<Session> { parseTime(it.startedAt ?: it.updatedAt) }.thenBy { it.sessionId })

/** Priority of the single-line parent-row subagent rollup: NeedsYou wins over Running; all-ended is silent (null). */
enum class ChildRollupKind { NeedsYou, Running }

/** Unified parent-row subagent rollup shared across Agents list and chat chrome. */
data class ChildRollup(
    val kind: ChildRollupKind,
    val count: Int,
)

/**
 * Computes the single-line parent-row subagent rollup using the same deduplicated subagent identity as Session Overview:
 * 1. If any child subagent waits for the user -> [ChildRollupKind.NeedsYou] with count.
 * 2. Else if any embedded subagent or folded subagent child is running -> [ChildRollupKind.Running] with count
 *    (deduplicating embedded entries against held standalone children by [SessionKey], with summary fallback when
 *    embedded details are omitted).
 * 3. Else -> null (all ended; parent row stays quiet).
 *
 * Side sessions (`folded_as: "side"`) and tab workers (`folded_as: "worker"`) are excluded from subagent counts.
 */
fun childRollup(parent: Session, heldChildren: List<Session> = emptyList()): ChildRollup? {
    val heldSubagents = heldChildren.filter {
        it.belongsToParent(parent.key) && it.sideHome == null && it.foldedAs != FoldedAs.WORKER
    }.distinctBy { it.key }

    val hub = parent.children
    val hubNeedsYou = if (hub != null) (hub.needsYou - hub.sidesNeedsYou).coerceAtLeast(0) else 0
    val hubWorking = if (hub != null) (hub.working - hub.sidesWorking).coerceAtLeast(0) else 0

    val heldNeedsYou = heldSubagents.count { it.agentState == AgentState.NeedsYou }
    val needsYou = maxOf(heldNeedsYou, hubNeedsYou)
    if (needsYou > 0) return ChildRollup(ChildRollupKind.NeedsYou, needsYou)

    val heldKeys = heldSubagents.mapTo(HashSet()) { it.key }
    val embedded = parent.subagents
    val embeddedRunning = if (embedded != null) {
        embedded.asSequence()
            .filter { it.running }
            .map { sub ->
                val sid = sub.sessionId.takeIf(String::isNotBlank) ?: "${parent.sessionId}@${sub.id}"
                SessionKey(parent.machineId, sid)
            }
            .distinct()
            .count { it !in heldKeys }
    } else {
        parent.runningSubagents ?: 0
    }
    val heldWorking = heldSubagents.count { it.agentState == AgentState.Working || it.backgroundWorking == true }
    val running = embeddedRunning + maxOf(heldWorking, hubWorking)
    if (running > 0) return ChildRollup(ChildRollupKind.Running, running)

    return null
}
