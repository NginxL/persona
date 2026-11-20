package persona.feature.agents

import androidx.annotation.StringRes
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.draw.clip
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.clearAndSetSemantics
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons

/** A to-do's stage (#4636, iOS `TodoStage`): the six steps of the bar, in order. */
internal enum class TodoStage(val wire: String, @StringRes val label: Int) {
    Plan("plan", R.string.agents_todo_stage_plan),
    Develop("develop", R.string.agents_todo_stage_develop),
    Review("review", R.string.agents_todo_stage_review),
    Merge("merge", R.string.agents_todo_stage_merge),
    Deploy("deploy", R.string.agents_todo_stage_deploy),
    Verify("verify", R.string.agents_todo_stage_verify);

    companion object {
        /** The stage the hub named; null for none or a name this build doesn't know. */
        fun of(hub: String?): TodoStage? = entries.firstOrNull { it.wire == hub }
    }
}

/** The names of a bar's steps: the six, or once done (#11590) the ones the work went through and Done. */
internal fun todoStageSteps(stage: TodoStage, done: Boolean): List<Int> =
    if (done) TodoStage.entries.take(stage.ordinal + 1).map { it.label } + R.string.agents_todo_stage_done
    else TodoStage.entries.map { it.label }

/**
 * One to-do's stage as a bar (iOS `TodoStageBar`): Plan → Develop → Review → Merge → Deploy → Verified, the steps before
 * the current one ticked green, the current one in the accent color with a ring, the ones ahead hollow. Work closed with
 * nothing left to deploy ([done], the hub's `stage_done`, #11590) ends with Done after the steps it went through, every
 * one ticked: no current step is left behind, and no step it never took is ticked.
 */
@Composable
internal fun TodoStageBar(stage: TodoStage, modifier: Modifier = Modifier, done: Boolean = false) {
    val palette = PersonaTheme.palette
    val steps = todoStageSteps(stage, done)
    // Once done every step is passed: the current one is past the last.
    val current = if (done) steps.size else stage.ordinal
    val label = if (done) stringResource(R.string.agents_todo_stage_a11y, stringResource(R.string.agents_todo_stage_done), steps.size, steps.size)
    else stringResource(R.string.agents_todo_stage_a11y, stringResource(stage.label), current + 1, steps.size)
    Row(modifier.fillMaxWidth().testTag("todo-stage").clearAndSetSemantics { contentDescription = label }) {
        steps.forEachIndexed { index, step ->
            Column(Modifier.weight(1f), horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Connector(reached = index <= current, shown = index > 0, Modifier.weight(1f))
                    Dot(index, current)
                    Connector(reached = index < current, shown = index < steps.size - 1, Modifier.weight(1f))
                }
                Text(stringResource(step), maxLines = 1, overflow = TextOverflow.Ellipsis, textAlign = TextAlign.Center,
                    style = PersonaTheme.type.caption,
                    color = when { index == current -> palette.accent; index < current -> palette.ink; else -> palette.ink3 })
            }
        }
    }
}

@Composable
private fun Connector(reached: Boolean, shown: Boolean, modifier: Modifier) {
    Box(modifier.height(Tokens.Size.lineMedium).alpha(if (shown) 1f else 0f)
        .background(if (reached) PersonaTheme.palette.positive.copy(alpha = 0.5f) else PersonaTheme.palette.line))
}

@Composable
private fun Dot(index: Int, current: Int) {
    val palette = PersonaTheme.palette
    val size = Modifier.size(Tokens.Size.iconXS)
    when {
        index < current -> Box(size.clip(CircleShape).background(palette.positive), contentAlignment = Alignment.Center) {
            JIcon(PersonaIcons.done, null, size = Tokens.Size.dotXL, tint = palette.onAction)
        }
        index == current -> Box(size.border(Tokens.Size.lineMedium, palette.accent.copy(alpha = 0.35f), CircleShape).padding(Tokens.Space.xs)
            .clip(CircleShape).background(palette.accent))
        else -> Box(size.border(Tokens.Size.lineMedium, palette.line, CircleShape))
    }
}
