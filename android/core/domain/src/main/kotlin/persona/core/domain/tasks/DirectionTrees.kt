package persona.core.domain.tasks

import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionTree
import persona.core.contracts.SessionKey

// What the pages take from the hub's direction tree (#4352, docs/assistant/unified-entry.md). None of
// this regroups to-dos or works out a state: the hub's tree is the one answer, and these only pick from it.

/** The To-dos page's sections when grouped by direction: the tree's order, groups without a task left out. */
fun DirectionTree.todoSections(): List<Direction> = directions.filter { it.tasks.isNotEmpty() }

/**
 * The To-dos page's rows grouped by direction: each section of [todoSections] with the page's rows for its tasks, in the
 * tree's order. [placed] is the page's row for each to-do id it lists (after search and filter); a task the page does
 * not list and a terminal row ([isDone]: Completed or Cancelled stays in its own fold) are left out, and so is a section
 * left empty.
 */
fun <T> DirectionTree.todoRows(placed: Map<String, T>, isDone: (T) -> Boolean): List<Pair<Direction, List<T>>> =
    todoSections().mapNotNull { direction ->
        val rows = direction.tasks.mapNotNull { placed[it.id] }.filterNot(isDone)
        if (rows.isEmpty()) null else direction to rows
    }

/** Every agent the tree places (children too), by the session it opens. */
fun DirectionTree.agentKeys(): Set<SessionKey> =
    directions.flatMap { d -> d.tasks.flatMap { t -> t.agents.flatMap(DirectionAgent::flatten) } }.mapTo(LinkedHashSet()) { it.openKey }

/** The key a fold remembers for a direction's row ([id]: its leader conversation, or the main one for the secretary). */
fun directionFoldKey(id: String) = "direction:$id"

/** The key a fold remembers for a task's row under direction [direction]. */
fun directionTaskFoldKey(direction: String, task: String) = "direction-task:$direction/$task"

/** The key a fold remembers for an agent's row that has agents under it. */
fun directionAgentFoldKey(task: String, agent: SessionKey) = "direction-agent:$task/${agent.path}"
