package persona.feature.assistant

import android.content.Context
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.RowScope
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.width
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.AgentState
import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionTask
import persona.core.contracts.SessionKey
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JDirectionStateLabel
import persona.core.designsystem.JDisclosureButton
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JRow
import persona.core.designsystem.LocalHomeDrawer
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.StatusLabel
import persona.core.designsystem.Tokens
import persona.core.domain.tasks.directionAgentFoldKey
import persona.core.domain.tasks.directionFoldKey
import persona.core.domain.tasks.directionTaskFoldKey

// A 常驻 lead's rows from the hub's direction tree (#4352, docs/android/android.md): the lead row itself stays the
// drawer's (AssistantScreen.kt's ConversationRow, its look and its 「在做 N · 等你 N」 from the same tree) and only gains a
// caret. Folded (the default, kept on this phone per lead) nothing else changes; open, its to-dos follow it, each with
// its state word and, at the row's end like the lead's, a caret folding it open to its Agents, an Agent with Agents under it folding open to those. A to-do opens
// its card, an Agent its session. The secretary group (书记官) has no row here: the main conversation's row is it.

/**
 * Which tree rows are open, kept on this phone (every row starts folded). One copy for the whole app, so every lead's
 * rows read and write the same set.
 */
private class DirectionFolds(context: Context) {
    private val preferences = context.getSharedPreferences("persona-assistant", Context.MODE_PRIVATE)

    var open: Set<String> by mutableStateOf(preferences.getStringSet(OPEN, null)?.toSet().orEmpty())
        private set

    fun toggle(key: String) {
        open = if (key in open) open - key else open + key
        preferences.edit().putStringSet(OPEN, open).apply()
    }

    companion object {
        private const val OPEN = "direction_rows_open"
        private var shared: DirectionFolds? = null

        fun of(context: Context): DirectionFolds =
            shared ?: DirectionFolds(context.applicationContext).also { shared = it }
    }
}

/** One line under an open lead, in the tree's order. */
private sealed interface DirectionLine {
    val key: String

    data class Task(val direction: String, val task: DirectionTask, val open: Boolean) : DirectionLine {
        override val key get() = "task/$direction/${task.id}"
    }
    data class Agent(val task: String, val agent: DirectionAgent, val depth: Int, val open: Boolean) : DirectionLine {
        override val key get() = "agent/$task/${agent.key.path}"
    }
}

/** The lines under [direction]'s open rows: its to-dos, and the Agents of each open one (children of open Agents too). */
private fun directionLines(direction: Direction, open: Set<String>): List<DirectionLine> = buildList {
    fun agents(task: String, list: List<DirectionAgent>, depth: Int) {
        for (agent in list) {
            val opened = directionAgentFoldKey(task, agent.key) in open
            add(DirectionLine.Agent(task, agent, depth, opened))
            if (opened) agents(task, agent.children, depth + 1)
        }
    }
    for (task in direction.tasks) {
        val opened = directionTaskFoldKey(direction.id, task.id) in open
        add(DirectionLine.Task(direction.id, task, opened))
        if (opened) agents(task.id, task.agents, 0)
    }
}

/**
 * A 常驻 lead's row ([row], drawn by the caller as before) and, while it is open, the rows of its to-dos from the tree's
 * [direction]. [row] gets the position its group item takes (the wide list's corners: open, the lead is no longer its
 * group's last row) and the caret to put on it; null (no tree from an older hub, or nothing open under it) leaves the
 * row exactly as it was. [title] is the lead's name, for the caret's TalkBack label ("To-dos of …"). Without
 * [onOpenTodo] / [onOpenAgent] a to-do's or Agent's row only folds.
 * [shadedPlace] selects the visible Agent's exact row; a sub-Agent still opens its parent without selecting both rows.
 */
@Composable
internal fun DirectionLeadRows(
    direction: Direction?, title: String, position: GroupPosition,
    onOpenTodo: ((String) -> Unit)?, onOpenAgent: ((SessionKey) -> Unit)?,
    shadedPlace: String?,
    row: @Composable (position: GroupPosition, disclosure: (@Composable () -> Unit)?) -> Unit,
) {
    if (direction == null || direction.tasks.isEmpty()) {
        row(position, null)
        return
    }
    val context = LocalContext.current
    val folds = remember(context) { DirectionFolds.of(context) }
    val foldKey = directionFoldKey(direction.id)
    val open = foldKey in folds.open
    val lines = if (open) directionLines(direction, folds.open) else emptyList()
    val drawer = LocalHomeDrawer.current
    val untitled = conversationWords().untitled
    Column {
        row(if (lines.isEmpty()) position else GroupPosition(first = position.first, last = false)) {
            JDisclosureButton(open, { folds.toggle(foldKey) }, stringResource(R.string.assistant_direction_todos_of, title))
        }
        lines.forEachIndexed { index, line ->
            val content: @Composable () -> Unit = {
                when (line) {
                    is DirectionLine.Task -> TaskRow(line.task, line.open, { folds.toggle(directionTaskFoldKey(line.direction, line.task.id)) },
                        onOpenTodo?.let { openTodo -> { openTodo(line.task.id) } })
                    is DirectionLine.Agent -> AgentRow(line.agent, line.depth, line.open, { folds.toggle(directionAgentFoldKey(line.task, line.agent.key)) },
                        onOpenAgent?.let { openAgent -> { openAgent(line.agent.openKey) } }, untitled,
                        selected = shadedPlace == AssistantSidebarShade.agent(line.agent.key))
                }
            }
            // The drawer's rows stand alone; the wide list's continue the lead's group.
            if (drawer) content()
            else JGroupItem(GroupPosition(first = false, last = position.last && index == lines.lastIndex)) { content() }
        }
    }
}

/** The space a row leaves before its title: one spacing step per level under the lead. */
@Composable
private fun RowScope.Indent(levels: Int) {
    if (levels > 0) Spacer(Modifier.width(Tokens.Space.l * levels))
}

/**
 * A child row's end: its state (if any), then its caret at the row's end as on the lead row, or the same width of
 * nothing when it has nothing to fold, so the state words line up.
 */
@Composable
private fun RowEnd(foldable: Boolean, open: Boolean, onToggle: () -> Unit, label: String, state: (@Composable () -> Unit)?) {
    Row(verticalAlignment = Alignment.CenterVertically) {
        state?.invoke()
        if (foldable) JDisclosureButton(open, onToggle, label) else Spacer(Modifier.width(Tokens.MinTouch.android))
    }
}

/** An open to-do of a lead: its title and state word; it opens the to-do's card, its caret lists its Agents. */
@Composable
private fun TaskRow(task: DirectionTask, open: Boolean, onToggle: () -> Unit, onOpen: (() -> Unit)?) {
    JRow(onClick = onOpen ?: onToggle.takeIf { task.agents.isNotEmpty() }, chevron = false,
        trailing = { RowEnd(task.agents.isNotEmpty(), open, onToggle, stringResource(R.string.assistant_direction_agents_of, task.title)) {
            JDirectionStateLabel(task.state) } }) {
        Indent(1)
        Text(task.title, Modifier.weight(1f), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** An Agent on a to-do (or one it started, a level further in): its name and, while it matters, its state in words. */
@Composable
private fun AgentRow(agent: DirectionAgent, depth: Int, open: Boolean, onToggle: () -> Unit, onOpen: (() -> Unit)?, untitled: String, selected: Boolean) {
    val title = agent.title.ifBlank { agent.agent.ifBlank { untitled } }
    val status = AgentState.of(agent.state.ifEmpty { null }, null).drawerStatus
    JRow(onClick = onOpen ?: onToggle.takeIf { agent.children.isNotEmpty() }, chevron = false, selected = selected,
        trailing = { RowEnd(agent.children.isNotEmpty(), open, onToggle, stringResource(R.string.assistant_direction_agents_of, title),
            if (status != null) ({ StatusLabel(status) }) else null) }) {
        Indent(2 + depth)
        Text(title, Modifier.weight(1f), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink2,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}

/** The states an Agent row names (as the compact history marks them): the rest say nothing. */
private val AgentState.drawerStatus: Status? get() = when (this) {
    AgentState.NeedsYou -> Status.NeedsYou
    AgentState.Working -> Status.Working
    AgentState.Unread -> Status.Unread
    else -> null
}
