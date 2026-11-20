package persona.feature.agents

import androidx.datastore.preferences.core.stringPreferencesKey
import persona.core.contracts.AgentState
import persona.core.contracts.CoreText
import persona.core.contracts.Machine
import persona.core.domain.sort.*

/** Which sessions the list shows; its words are `AgentRange.label` (AgentsScreen.kt). */
enum class AgentRange { Running, All, History }

internal fun agentRangeKey(scope: String) = stringPreferencesKey("agent-range:$scope")

/** [ownRows]: rows opened under the quick filter while they were rows of their own, which stay so ([openedRowsOfTheirOwn]). */
internal fun scopedAgentList(
    rows: List<AgentSession>, machines: List<Machine>, preferences: AgentsPreferencesSnapshot, settings: AgentListSettings,
    todos: List<persona.core.contracts.Todo> = emptyList(), ownRows: Set<String> = emptySet(),
    /** The hub's direction tree, for "By direction" (#4352). */
    directions: persona.core.contracts.DirectionTree? = null,
    adapter: persona.core.data.AgentListAdapter? = null,
    held: String? = null,
    now: java.time.Instant = java.time.Instant.now(),
): AgentListResult {
    // The rows this range's list of running sessions draws, where it has one: a subtask among them hangs under its
    // parent only while a row above it is among them too, so a working agent whose creator was let go is not lost with
    // it (#4494, codexTaskForest). They are what scopedRootAgentList hands buildFlatAgentList, and what that draws.
    val allRunning = settings.copy(includeCodexHistory = true)
    val runs: ((AgentSession) -> Boolean)? = when (preferences.range) {
        AgentRange.Running -> { row -> settings.draws(row, preferences.pins) }
        AgentRange.All -> { row -> (row.session.runsInAll || allRunning.nativePinned(row.session)) && allRunning.draws(row, preferences.pins) }
        AgentRange.History -> null
    }
    val tree = codexTaskForest(foldRows(rows, settings.hierarchy), ownRows, runs)
    return scopedRootAgentList(tree.roots, machines, preferences, tree.searchSettings(settings), todos, directions,
        adapter, held, now)
        .let { it.copy(children = tree.children.plusChildren(it.children)) }
}

/** "All" lists these with the running ones (Codex threads of the past too), every other session with the history. */
private val persona.core.contracts.Session.runsInAll: Boolean get() = isLive || isCodexTask ||
    (children != null && foldedState != persona.core.contracts.AgentState.Offline)

/** [rows] are a forest's roots ([scopedAgentList]), so no list here hangs subtasks again. */
private fun scopedRootAgentList(
    rows: List<AgentSession>, machines: List<Machine>, preferences: AgentsPreferencesSnapshot, settings: AgentListSettings,
    todos: List<persona.core.contracts.Todo> = emptyList(),
    directions: persona.core.contracts.DirectionTree? = null,
    adapter: persona.core.data.AgentListAdapter? = null,
    held: String? = null,
    now: java.time.Instant = java.time.Instant.now(),
): AgentListResult {
    fun projected(rows: List<AgentSession>, effective: AgentListSettings) = adapter?.project(persona.core.data.AgentListAdapter.Input(
        rows, machines, preferences.pins, preferences.mutes, effective, todos, directions, held, now,
    )) ?: buildFlatAgentList(rows.map { it.copy(heldInList = it.id == held) }, machines, preferences.pins, preferences.mutes,
        effective, todos, directions)
    if (preferences.range == AgentRange.Running)
        return projected(rows, settings)
    val history = rows.filter { !it.session.isLive && !(preferences.range == AgentRange.All && (it.session.isCodexTask || settings.nativePinned(it.session))) && settings.admitsHistory(it.session) && !(settings.hideBotmux && it.session.isBotmux) }
    val filtered = history.filter { settings.quick.matches(it) }
    val groups = filtered.groupBy { it.session.machineId to if (it.session.isCodexTask) repoOf(it.session.cwd, it.session.repoRoot).repo else it.session.cwd }.map { (project, members) ->
        val id = "history/${project.first}/${project.second}"
        val ordered = members.sortedWith(compareByDescending<AgentSession> { it.updatedAt }.thenBy { it.id })
        val folded = id in settings.folded
        val title = if (settings.grouping == AgentGrouping.Repo)
            CoreText.joined(" · ", listOf(CoreText.plain(project.second), CoreText.of(CoreText.Key.StateNotRunning)))
        else CoreText.plain(project.second)
        project.first to AgentListGroup(id, title, ordered, if (folded) emptyList() else ordered, folded)
    }.sortedWith(compareByDescending<Pair<String, AgentListGroup>> { it.second.rows.first().updatedAt }.thenBy { it.second.id })
    val running = if (preferences.range == AgentRange.All)
        projected(rows.filter { it.session.runsInAll || settings.nativePinned(it.session) }, settings.copy(includeCodexHistory = true))
    else AgentListResult(emptyMap(), emptyList(), emptyList())
    val withHistory = if (settings.grouping == AgentGrouping.Repo && groups.isNotEmpty()) {
        val additions = groups.groupBy({ it.first }, { it.second })
        val current = running.machines.associateBy { it.machine.id }
        // By Repository keeps offline history in its computer tree, so a folded computer hides all of its groups.
        val merged = (running.machines.map { it.machine.id } + additions.keys).distinct().map { id ->
            val machine = current[id]
            val added = additions[id].orEmpty()
            val members = added.flatMap { it.rows }
            val states = AgentState.entries.associateWith { state ->
                (machine?.stateCounts?.get(state) ?: 0) + members.count { !it.isNestedSubtask && it.state == state }
            }
            if (machine != null) machine.copy(
                groups = machine.groups + added.map { it.id },
                sessionCount = machine.sessionCount + members.count { !it.isNestedSubtask },
                stateCounts = states,
            ) else AgentMachineGroup(
                machines.firstOrNull { it.id == id } ?: Machine(id, ""), added.map { it.id },
                members.count { !it.isNestedSubtask }, added.size, states,
                folded = "machine/$id" in settings.folded,
            )
        }
        running.copy(machines = merged)
    } else running
    return withHistory.copy(groups = withHistory.groups + groups.map { it.second }, counts = AgentQuickFilter.entries.associateWith { filter ->
        (running.counts[filter] ?: 0) + history.count { !it.isNestedSubtask && filter.matches(it) }
    })
}

private fun AgentListSettings.admitsHistory(s: persona.core.contracts.Session): Boolean =
    !s.assistant && (machineFilter.isEmpty() || machineFilter == s.machineId) &&
        (query.isEmpty() || s.key.path in queryAncestors || listOf(s.displayTitle, s.cwd, s.lastText, s.branch).any { it?.contains(query, true) == true })
