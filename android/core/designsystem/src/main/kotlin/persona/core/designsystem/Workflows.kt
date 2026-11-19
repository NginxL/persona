package persona.core.designsystem

import androidx.compose.animation.animateColorAsState
import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.AnimationSpec
import androidx.compose.animation.core.FastOutSlowInEasing
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.snap
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.ProgressBarRangeInfo
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.progressBarRangeInfo
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import kotlinx.coroutines.delay
import persona.core.contracts.CoreText
import persona.core.contracts.Workflow
import persona.core.contracts.WorkflowAgent
import persona.core.contracts.WorkflowStatus
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.domain.sort.WorkflowSummary
import persona.core.domain.sort.WorkflowTimeline
import persona.core.domain.sort.WorkflowTimeline.AgentState
import persona.core.domain.sort.WorkflowTimeline.Part
import persona.core.domain.sort.WorkflowTimeline.PhaseBlock
import persona.core.domain.sort.WorkflowTimeline.PhaseState
import java.time.Instant

// A Claude Code Workflow run (#2935, #3435; docs/protocol/agent-states.md "Background workflows and Monitor") as a timeline card,
// the same as the web's (#3434): in the chat where the Workflow call that launched it was (JWorkflowCallCard), and in an
// agent's details (JWorkflowCard). Both draw the same parts:
// - a head: the mark, its name, its status, "169/185+" agents done of those started ("+" while it runs: the script
//   starts more as it goes), how long it has run;
// - while it runs "Now Verify: verify:chat-rich", once it has ended what it was for (and why it stopped);
// - a progress bar in parts: done, running, failed, stopped;
// - one block per phase: "done/started" ("+" while more may start there), its agents in the order they started, at most
//   WorkflowTimeline.PHASE_ROWS of them with the quiet rest folded into "n more", the earliest ones the hub no longer
//   lists said above them. A row opens the agent's conversation.
// The rules are core:domain's WorkflowTimeline. Things move only when a value changes, never on the first draw and not at
// all with reduced motion: counts count, colors go over, a block fades in over its full space and closes at once (#9574),
// a new agent fades in. Nothing moves while nothing changes: the status chip keeps its look, with no breathing.

/** The mark of a background workflow, wherever one is named. */
val WorkflowIcon: PhIcon get() = PhIcons.TreeStructure

/**
 * What the reader opened or folded in workflow cards (a run, a phase, a phase's "n more"), for as long as the page that
 * holds this lives: a card that scrolls away, or moves from a folded work line into its open steps, comes back as the
 * reader left it. Without a choice, each follows its card's rule (a run is open while it runs, a phase while it is lit
 * or has agents running).
 */
@Stable
class WorkflowFolds {
    private val chosen = mutableStateMapOf<String, Boolean>()
    operator fun get(key: String): Boolean? = chosen[key]
    operator fun set(key: String, open: Boolean) {
        chosen[key] = open
    }
}

/**
 * A run in the chat, in place of the Workflow call that launched it (#3435): its head and the line under it, and while
 * open its progress bar and phases. Open while it runs, folded to its head and what it was for once it has ended; what
 * the reader chose stays in [folds]. [onOpenAgent]: a row opens that agent's conversation; null where it cannot be opened.
 */
@Composable
fun JWorkflowCallCard(
    workflow: Workflow,
    modifier: Modifier = Modifier,
    folds: WorkflowFolds = remember { WorkflowFolds() },
    onOpenAgent: ((WorkflowAgent) -> Unit)? = null,
) {
    val palette = PersonaTheme.palette
    val key = workflow.foldKey()
    val open = folds[key] ?: workflow.isRunning
    // The clock goes every second on an open card of a run that runs; folded, at the rows' pace.
    val clock = rememberWorkflowClock(if (open) 1_000 else 30_000, workflow.isRunning)
    val title = workflowTitle(workflow)
    val action = stringResource(if (open) R.string.designsystem_workflow_collapse else R.string.designsystem_workflow_expand, title)
    val state = stringResource(if (open) R.string.designsystem_expanded else R.string.designsystem_collapsed)
    val shape = PersonaTheme.shapes.cardShape
    Column(modifier.fillMaxWidth().padding(vertical = 6.dp.dense).clip(shape).background(palette.surface).border(1.dp, palette.line, shape)
        .testTag("workflow-call")) {
        Column(
            Modifier.fillMaxWidth().clickable(role = Role.Button, onClickLabel = action) { folds[key] = !open }
                .semantics { stateDescription = state }
                .padding(horizontal = CardInset, vertical = 10.dp.dense),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs),
        ) {
            WorkflowHead(workflow, clock, kind = true, open = open)
            WorkflowSub(workflow, whole = open)
        }
        Unfold(open) { WorkflowBody(workflow, clock, folds, onOpenAgent, CardInset) }
    }
}

/**
 * A run in an agent's details: the same card, always open, as one item of a group ([position]). Its phases fold by the
 * same rules, and what the reader chose stays in [folds].
 */
@Composable
fun JWorkflowCard(
    workflow: Workflow,
    modifier: Modifier = Modifier,
    folds: WorkflowFolds = remember { WorkflowFolds() },
    onOpenAgent: ((WorkflowAgent) -> Unit)? = null,
    position: GroupPosition = GroupPosition.Single,
) {
    val clock = rememberWorkflowClock(1_000, workflow.isRunning)
    JGroupItem(position, modifier.testTag("workflow-card")) {
        Column(Modifier.fillMaxWidth().padding(start = Tokens.Space.m, end = Tokens.Space.m, top = Tokens.Space.m, bottom = Tokens.Space.s),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            WorkflowHead(workflow, clock, kind = false, open = null)
            WorkflowSub(workflow, whole = true)
        }
        WorkflowBody(workflow, clock, folds, onOpenAgent, Tokens.Space.m)
    }
}

/** The side room of the chat card's parts, as the plan card's. */
private val CardInset = 14.dp

/** A run's choices go under its run id, else its task id. */
private fun Workflow.foldKey(): String = "workflow:" + (runId ?: taskId)

@Composable
private fun workflowTitle(workflow: Workflow): String = workflow.title ?: stringResource(R.string.designsystem_workflow_untitled)

@Composable
private fun agentLabel(agent: WorkflowAgent): String =
    agent.label?.trim()?.takeIf(String::isNotEmpty) ?: stringResource(R.string.designsystem_workflow_agent)

/** The time now, every [periodMillis] while [running]; read it only in the leaves that show a time. */
@Composable
private fun rememberWorkflowClock(periodMillis: Long, running: Boolean): State<Instant> =
    produceState(Instant.now(), periodMillis, running) {
        while (running) {
            delay(periodMillis)
            value = Instant.now()
        }
    }

/** The head: the mark, ([kind] on a wide window) "Workflow", its name, status, counts and time; the caret when [open] is given. */
@Composable
private fun WorkflowHead(workflow: Workflow, clock: State<Instant>, kind: Boolean, open: Boolean?) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val spoken = WorkflowSummary.agentsDone(workflow).text()
    Row(verticalAlignment = Alignment.CenterVertically) {
        JIcon(WorkflowIcon, null, size = Tokens.Space.l, tint = palette.ai)
        Spacer(Modifier.width(Tokens.Space.s))
        if (kind && LocalWindowLayout.current.expanded) {
            Text(stringResource(R.string.designsystem_workflow_kind), style = type.micro, color = palette.ink2, maxLines = 1)
            Spacer(Modifier.width(Tokens.Space.s))
        }
        Text(workflowTitle(workflow), Modifier.weight(1f), style = type.label.copy(fontWeight = FontWeight.SemiBold), color = palette.ink,
            maxLines = 1, overflow = TextOverflow.Ellipsis)
        Spacer(Modifier.width(Tokens.Space.s))
        WorkflowStatusTag(WorkflowSummary.status(workflow).text(), workflow.state)
        Spacer(Modifier.width(Tokens.Space.s))
        JChangingText(WorkflowTimeline.counts(workflow), style = type.micro, color = palette.ink2,
            modifier = Modifier.semantics { contentDescription = spoken }.testTag("workflow-counts"))
        RunElapsed(workflow, clock)
        if (open != null) {
            Spacer(Modifier.width(Tokens.Space.xs))
            JIcon(if (open) PhIcons.CaretUp else PhIcons.CaretDown, null, size = 14.dp, tint = palette.ink3)
        }
    }
}

/** How long it has run: a leaf of its own, so the clock redraws only this. */
@Composable
private fun RunElapsed(workflow: Workflow, clock: State<Instant>) {
    val seconds = WorkflowSummary.elapsedSeconds(workflow, clock.value) ?: return
    Spacer(Modifier.width(Tokens.Space.s))
    JChangingText(WorkflowTimeline.took(seconds).text(), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3)
}

/**
 * Its state on a neutral surface (running in the AI color, done positive, failed danger, stopped neutral, gone quiet
 * caution), as before #3435; a new state's color goes over and its word fades in.
 */
@Composable
private fun WorkflowStatusTag(text: String, status: WorkflowStatus) {
    val palette = PersonaTheme.palette
    val tone = when (status) {
        WorkflowStatus.Running -> StatusColors(dot = palette.ai, text = palette.ai, tint = palette.statusSurface)
        WorkflowStatus.Completed -> palette.tone(Tone.Positive)
        WorkflowStatus.Failed -> palette.tone(Tone.Danger)
        WorkflowStatus.Killed -> null
        WorkflowStatus.Stalled -> palette.tone(Tone.Caution)
    }
    JTagColored(text, Modifier, tone, animated = true)
}

/**
 * The line under the head: where it is now while it runs; once ended, what it was for and why it stopped. One line each
 * while folded ([whole] false), all of them open.
 */
@Composable
private fun WorkflowSub(workflow: Workflow, whole: Boolean) {
    val palette = PersonaTheme.palette
    val style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Normal)
    val lines = if (whole) Int.MAX_VALUE else 1
    val now = WorkflowTimeline.now(workflow)
    if (now != null) {
        val agent = now.agent?.let { agentLabel(it) }
        val text = when {
            now.phase.isNotEmpty() && agent != null -> stringResource(R.string.designsystem_workflow_now_phase_agent, now.phase, agent)
            now.phase.isNotEmpty() -> stringResource(R.string.designsystem_workflow_now_phase, now.phase)
            else -> stringResource(R.string.designsystem_workflow_now_agent, agent.orEmpty())
        }
        FadeOnChange(text, Modifier.testTag("workflow-now")) { Text(text, style = style, color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis) }
        return
    }
    // Its summary names it when it has no name of its own: not said twice.
    val summary = workflow.summary?.trim()?.takeIf { it.isNotEmpty() && !workflow.name.isNullOrBlank() }
    val error = workflow.error?.trim()?.takeIf { it.isNotEmpty() && workflow.state != WorkflowStatus.Completed }
    summary?.let { Text(it, style = style, color = palette.ink2, maxLines = lines, overflow = TextOverflow.Ellipsis) }
    error?.let { Text(it, style = style, color = palette.danger.text, maxLines = lines, overflow = TextOverflow.Ellipsis) }
}

/** What opens below the head: the progress bar and the phases. */
@Composable
private fun WorkflowBody(workflow: Workflow, clock: State<Instant>, folds: WorkflowFolds, onOpenAgent: ((WorkflowAgent) -> Unit)?, inset: Dp) {
    Column(Modifier.fillMaxWidth()) {
        WorkflowProgress(workflow, inset)
        WorkflowTimeline.blocks(workflow).forEachIndexed { index, block ->
            key(index, block.title) { PhaseView(workflow, block, clock, folds, onOpenAgent, inset) }
        }
    }
}

@Composable
private fun partColor(part: Part): Color {
    val palette = PersonaTheme.palette
    return when (part) {
        Part.Done -> palette.ai
        Part.Running -> palette.ai.copy(alpha = 0.35f)
        Part.Failed -> palette.danger.dot
        Part.Stopped -> palette.ink3.copy(alpha = 0.45f)
    }
}

/**
 * Its agents as a bar in parts, done first, each part moving to its new place and length over the style's count
 * duration; under it, what each part counts (and, on a wide window, its tool calls).
 */
@Composable
private fun WorkflowProgress(workflow: Workflow, inset: Dp) {
    val tally = WorkflowTimeline.tally(workflow)
    if (tally.started == 0) return
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val segments = WorkflowTimeline.segments(tally)
    val spec: AnimationSpec<Float> = if (PersonaTheme.reduceMotion) snap() else tween(PersonaTheme.motion.count, easing = FastOutSlowInEasing)
    val starts = segments.map { animateFloatAsState(it.from, spec, label = "workflow part start") }
    val sizes = segments.map { animateFloatAsState(it.size, spec, label = "workflow part size") }
    val colors = segments.map { partColor(it.part) }
    val spoken = WorkflowSummary.agentsDone(workflow).text()
    val calls = workflow.calls?.takeIf { it > 0 && LocalWindowLayout.current.expanded }
    Column(Modifier.fillMaxWidth().padding(start = inset, end = inset, bottom = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(6.dp.dense)) {
        Box(
            Modifier.fillMaxWidth().height(4.dp).clip(CircleShape).background(palette.surfaceStrong)
                .semantics {
                    contentDescription = spoken
                    progressBarRangeInfo = ProgressBarRangeInfo(tally.done.toFloat(), 0f..tally.started.toFloat())
                }
                .testTag("workflow-progress")
                .drawBehind {
                    segments.indices.forEach { i ->
                        val width = sizes[i].value * size.width
                        if (width > 0f) drawRect(colors[i], Offset(starts[i].value * size.width, 0f), Size(width, size.height))
                    }
                },
        )
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            segments.filter { tally.of(it.part) > 0 }.forEach { segment ->
                key(segment.part) {
                    val count = tally.of(segment.part)
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Box(Modifier.size(6.dp).clip(CircleShape).background(partColor(segment.part)))
                        Spacer(Modifier.width(Tokens.Space.xs))
                        JChangingText(pluralStringResource(tallyWords(segment.part), count, count), style = type.micro, color = palette.ink3)
                    }
                }
            }
            if (calls != null) {
                Spacer(Modifier.weight(1f))
                JChangingText(pluralStringResource(R.plurals.designsystem_workflow_calls, calls, calls), style = type.micro, color = palette.ink3)
            }
        }
    }
}

private fun tallyWords(part: Part): Int = when (part) {
    Part.Done -> R.plurals.designsystem_workflow_tally_done
    Part.Running -> R.plurals.designsystem_workflow_tally_running
    Part.Failed -> R.plurals.designsystem_workflow_tally_failed
    Part.Stopped -> R.plurals.designsystem_workflow_tally_stopped
}

/**
 * A phase: its head (lit while the run is in it) and its agents, open per [PhaseBlock.opens] unless the reader chose.
 * Opening and folding move over the style's size motion; a new agent fades in where it started (start order: nothing
 * above it moves).
 */
@Composable
private fun PhaseView(workflow: Workflow, block: PhaseBlock, clock: State<Instant>, folds: WorkflowFolds,
                      onOpenAgent: ((WorkflowAgent) -> Unit)?, inset: Dp) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val key = workflow.foldKey() + "/phase:" + block.title
    val opens = block.agents.isNotEmpty()
    val open = opens && (folds[key] ?: block.opens)
    val all = folds["$key/all"] == true
    val rows = WorkflowTimeline.rows(block, all)
    val fewer = all && WorkflowTimeline.rows(block).folded > 0
    val fresh = rememberArrivals(rows.rows.map { it.id })
    val spec: AnimationSpec<Color> = if (PersonaTheme.reduceMotion) snap() else tween(PersonaTheme.motion.standard)
    val lit by animateColorAsState(if (block.current) palette.aiSoft else palette.aiSoft.copy(alpha = 0f), spec, label = "phase lit")
    val ink by animateColorAsState(when {
        block.current -> palette.ai
        block.state == PhaseState.Next -> palette.ink3
        else -> palette.ink
    }, spec, label = "phase ink")
    val state = stringResource(if (open) R.string.designsystem_expanded else R.string.designsystem_collapsed)
    Column(Modifier.fillMaxWidth().testTag("workflow-phase:" + block.title)) {
        Box(Modifier.fillMaxWidth().height(1.dp).background(palette.line))
        Row(
            Modifier.fillMaxWidth().drawBehind { drawRect(lit) }
                .then(if (opens) Modifier.clickable(role = Role.Button) { folds[key] = !open }.semantics { stateDescription = state } else Modifier)
                .heightIn(min = 40.dp.row).padding(horizontal = inset),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            PhaseMark(block)
            Spacer(Modifier.width(Tokens.Space.s))
            // its name as long as it needs, how many failed right after it; the count at the end
            Row(Modifier.weight(1f), verticalAlignment = Alignment.CenterVertically) {
                Text(phaseTitle(block), Modifier.weight(1f, fill = false),
                    style = type.caption.copy(fontWeight = if (block.state == PhaseState.Next) FontWeight.Normal else FontWeight.Medium),
                    color = ink, maxLines = 1, overflow = TextOverflow.Ellipsis)
                if (block.failed > 0) {
                    Spacer(Modifier.width(Tokens.Space.s))
                    Text(pluralStringResource(R.plurals.designsystem_workflow_phase_failed, block.failed, block.failed), style = type.micro,
                        color = palette.danger.text, maxLines = 1)
                }
            }
            if (block.started > 0) {
                Spacer(Modifier.width(Tokens.Space.s))
                JChangingText(block.count, style = type.micro, color = palette.ink2)
            }
            if (opens) {
                Spacer(Modifier.width(Tokens.Space.xs))
                JIcon(if (open) PhIcons.CaretUp else PhIcons.CaretDown, null, size = 12.dp, tint = palette.ink3)
            }
        }
        if (opens) Unfold(open) {
            Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.xs)) {
                val row: @Composable (WorkflowAgent) -> Unit = { agent ->
                    key(agent.id) {
                        AgentRow(agent, clock, agent.id in fresh, inset, onOpenAgent?.takeIf { agent.sessionId.isNotBlank() }?.let { open -> { open(agent) } })
                    }
                }
                if (rows.unlisted > 0) Note(pluralStringResource(R.plurals.designsystem_workflow_unlisted_agents, rows.unlisted, rows.unlisted), inset)
                rows.rows.take(rows.breakAt).forEach { row(it) }
                if (rows.folded > 0) TextAction(pluralStringResource(R.plurals.designsystem_workflow_more_agents, rows.folded, rows.folded), inset) {
                    folds["$key/all"] = true
                }
                rows.rows.drop(rows.breakAt).forEach { row(it) }
                if (fewer) TextAction(stringResource(R.string.designsystem_workflow_fewer_agents), inset) { folds["$key/all"] = false }
            }
        }
    }
}

@Composable
private fun phaseTitle(block: PhaseBlock): String = block.title.ifEmpty { stringResource(R.string.designsystem_workflow_phase_untitled) }

/**
 * Where a phase stands: a check once done (a red cross when some of its agents failed), a dot in the AI color while the
 * run is in it (paler while only some of its agents run), a ring before it starts, a square when the run ended with
 * some of its agents not back. A new mark fades in.
 */
@Composable
private fun PhaseMark(block: PhaseBlock) {
    val palette = PersonaTheme.palette
    val label = stringResource(when {
        block.current || block.state == PhaseState.Current -> R.string.designsystem_workflow_phase_current
        block.state == PhaseState.Done && block.failed > 0 -> R.string.designsystem_workflow_phase_done_failed
        block.state == PhaseState.Done -> R.string.designsystem_workflow_phase_done
        block.state == PhaseState.Stopped -> R.string.designsystem_workflow_phase_stopped
        else -> R.string.designsystem_workflow_phase_next
    })
    FadeOnChange(block.state to block.current, Modifier.size(Tokens.Space.Standard.l).semantics { contentDescription = label }) {
        Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
            when (block.state) {
                PhaseState.Done -> if (block.failed > 0) JIcon(PhIcons.X, null, size = 13.dp, tint = palette.danger.dot)
                    else JIcon(PhIcons.Check, null, size = 13.dp, tint = palette.positive)
                PhaseState.Current -> Box(Modifier.size(Tokens.Space.Standard.s).clip(CircleShape).background(if (block.current) palette.ai else palette.ai.copy(alpha = 0.5f)))
                PhaseState.Stopped -> Box(Modifier.size(Tokens.Space.Standard.s).clip(RoundedCornerShape(2.dp)).background(palette.ink3))
                PhaseState.Next -> Box(Modifier.size(Tokens.Space.Standard.s).border(1.5.dp, palette.ink3, CircleShape))
            }
        }
    }
}

/**
 * An agent: how it stands, its label, (on a wide window) its model and latest tool, how long it ran. [fresh]: it came in
 * after the card was drawn, so it fades in. [onOpen] opens its conversation.
 */
@Composable
private fun AgentRow(agent: WorkflowAgent, clock: State<Instant>, fresh: Boolean, inset: Dp, onOpen: (() -> Unit)?) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val state = WorkflowTimeline.agentState(agent)
    val label = agentLabel(agent)
    val reduce = PersonaTheme.reduceMotion
    val alpha = remember { Animatable(if (fresh && !reduce) 0f else 1f) }
    // iOS WorkflowViews.swift:590: .animation(Theme.Motion.reveal, value: r.rows.map(\.id))
    LaunchedEffect(Unit) { if (alpha.value < 1f) alpha.animateTo(1f, SharedMotion.reveal()) }
    val word = CoreText.of(when (state) {
        AgentState.Running -> CoreText.Key.WorkflowRunning
        AgentState.Done -> CoreText.Key.WorkflowCompleted
        AgentState.Failed -> CoreText.Key.WorkflowFailed
        AgentState.Stopped -> CoreText.Key.WorkflowKilled
    }).text()
    val action = onOpen?.let { stringResource(R.string.designsystem_workflow_open_agent, label) }
    val wide = LocalWindowLayout.current.expanded
    Row(
        Modifier.fillMaxWidth().graphicsLayer { this.alpha = alpha.value }
            .then(if (onOpen != null) Modifier.clickable(role = Role.Button, onClickLabel = action, onClick = onOpen) else Modifier)
            .semantics(mergeDescendants = true) { stateDescription = word }
            .heightIn(min = 36.dp.row).padding(start = inset + Tokens.Space.xl, end = inset)
            .testTag("workflow-agent:" + agent.id),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        AgentDot(state)
        Spacer(Modifier.width(Tokens.Space.s))
        Text(label, Modifier.weight(1f), style = type.caption.copy(fontWeight = FontWeight.Normal),
            color = if (state == AgentState.Running) palette.ink else palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
        // On a phone a row is its label, dot and time; the model and latest tool are on the agent's own page.
        if (wide) listOfNotNull(agent.model?.trim()?.takeIf(String::isNotEmpty), WorkflowTimeline.agentTool(agent)).forEach {
            Spacer(Modifier.width(Tokens.Space.s))
            Text(it, Modifier.widthIn(max = 220.dp), style = type.micro, color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        AgentElapsed(agent, clock)
    }
}

/** An agent's time: only a running one reads the clock. */
@Composable
private fun AgentElapsed(agent: WorkflowAgent, clock: State<Instant>) {
    val seconds = WorkflowTimeline.agentSeconds(agent, if (agent.running) clock.value else Instant.EPOCH) ?: return
    Spacer(Modifier.width(Tokens.Space.s))
    Text(WorkflowTimeline.took(seconds).text(), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
}

/** The state dot of the session rows; a failed agent's in the danger color, shaped as the style's. */
@Composable
private fun AgentDot(state: AgentState) {
    when (state) {
        AgentState.Running -> JStatusDot(Status.Working)
        AgentState.Done -> JStatusDot(Status.Idle)
        AgentState.Stopped -> JStatusDot(Status.Offline)
        AgentState.Failed -> {
            val danger = PersonaTheme.palette.danger
            val shape = if (PersonaTheme.style == AppStyle.Board) RoundedCornerShape(2.dp) else CircleShape
            Box(Modifier.size(Tokens.Space.Standard.s).clip(shape).background(danger.dot).border(1.dp, danger.text, shape))
        }
    }
}

/** A quiet line among the rows ("12 earlier ones not listed"). */
@Composable
private fun Note(text: String, inset: Dp) {
    Text(text, Modifier.heightIn(min = 32.dp.row).padding(start = inset + Tokens.Space.xl, end = inset, top = Tokens.Space.s),
        style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
}

/** "n more" / "Show fewer" among the rows: words in the accent color, one line. */
@Composable
private fun TextAction(text: String, inset: Dp, onClick: () -> Unit) {
    Box(Modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick).heightIn(min = 36.dp.row)
        .padding(start = inset + Tokens.Space.xl, end = inset), contentAlignment = Alignment.CenterStart) {
        Text(text, style = PersonaTheme.type.caption.copy(fontWeight = FontWeight.Medium), color = PersonaTheme.palette.accent, maxLines = 1)
    }
}

/**
 * Opens [content] as the chat's unfolding: full height on the first frame, a Reveal fade, gone at once when it closes.
 * iOS `WorkflowCallCard` body (`WorkflowCallCard.swift:102`) and a phase's rows (`WorkflowViews.swift:498`), both through
 * `toggleInPlace` (`ChatPin.swift:707-726`). Not on the first draw. Without a chat pin iOS animates with
 * `Theme.Motion.reveal` instead (`WorkflowViews.swift:509-511`); these cards are in the chat, or always open.
 */
@Composable
private fun Unfold(open: Boolean, content: @Composable () -> Unit) {
    val edge = rememberOpenEdge()
    val revealing = edge.revealing(open)
    if (open) Box(Modifier.unfolding(revealing)) { content() }
}

/** [content] for [value]: drawn at once the first time; when [value] changes, the new one fades in (at once with reduced motion). */
@Composable
private fun FadeOnChange(value: Any, modifier: Modifier = Modifier, content: @Composable () -> Unit) {
    val reduce = PersonaTheme.reduceMotion
    val duration = PersonaTheme.motion.quick
    var drawn by remember { mutableStateOf(false) }
    key(value) {
        val alpha = remember { Animatable(if (drawn && !reduce) 0f else 1f) }
        LaunchedEffect(Unit) {
            drawn = true
            if (alpha.value < 1f) alpha.animateTo(1f, tween(duration, easing = FastOutSlowInEasing))
        }
        Box(modifier.graphicsLayer { this.alpha = alpha.value }) { content() }
    }
}

private class Seen {
    var ids: Set<String>? = null
}

/** The ids among [ids] that came in after the first draw: the rows that fade in. */
@Composable
private fun rememberArrivals(ids: List<String>): Set<String> {
    val seen = remember { Seen() }
    val before = seen.ids
    SideEffect { seen.ids = before.orEmpty() + ids }
    return if (before == null) emptySet() else ids.filterNotTo(mutableSetOf()) { it in before }
}
