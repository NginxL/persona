package persona.feature.chat

import androidx.compose.foundation.border
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.ExperimentalLayoutApi
import androidx.compose.foundation.layout.FlowRow
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.selection.toggleable
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.HorizontalDivider
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.Constraints
import persona.core.contracts.PlanTask
import persona.core.contracts.TaskPlan
import persona.core.designsystem.JButton
import persona.core.designsystem.JButtonKind
import persona.core.designsystem.JButtonSize
import persona.core.designsystem.JCard
import persona.core.designsystem.JChip
import persona.core.designsystem.JIcon
import persona.core.designsystem.JNumberDisc
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.icons.PersonaIcons

// iOS's plan card on Glass (`PlanCard` in Views/Assistant/Tasks/TaskViews.swift, #4339 C13): what Persona draws in its
// conversation for a plan nobody started yet. Brief and Board keep `PlanCard`'s own card, and their plans behind the trays.

/**
 * An open plan as iOS's `PlanCard.open`: what it does ("Start like this", or "Split into N tasks, started like this") with the plan's
 * mark, each task as a numbered row (a number only when there are several) with where it would run, "Start these directly"
 * (the next ones like these start without asking), then **Start**, **Edit** and **Don't start**. While the hub starts it, the
 * choices wait; once it came back started the conversation draws the dispatched line in its place, and **Don't start** leaves
 * "Not started · N tasks".
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
internal fun GlassPlanCard(
    plan: TaskPlan, host: AssistantTaskHost, directly: Boolean, onDirectly: (Boolean) -> Unit, busy: Boolean, onEdit: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    JCard(Modifier.testTag("plan-card-${plan.id}"), padding = Tokens.Space.l) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                JIcon(PersonaIcons.taskPlan, decorative = true, size = Tokens.Size.markXS, tint = palette.ink2)
                Text(if (plan.tasks.size > 1) pluralStringResource(R.plurals.chat_tasks_plan_split, plan.tasks.size, plan.tasks.size)
                    else stringResource(R.string.chat_tasks_plan_single), Modifier.testTag("plan-heading"),
                    style = type.caption.copy(fontWeight = FontWeight.SemiBold), color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
            plan.tasks.forEachIndexed { n, task ->
                if (n > 0) HorizontalDivider(color = palette.line)
                GlassPlanTask(n + 1, task, numbered = plan.tasks.size > 1, host)
            }
            StartDirectly(directly, enabled = !busy, onDirectly)
            PlanActions(
                start = {
                    JButton(if (plan.tasks.size > 1) pluralStringResource(R.plurals.chat_tasks_plan_start_count, plan.tasks.size, plan.tasks.size)
                        else stringResource(R.string.chat_tasks_plan_start),
                        { host.tasks.startPlan(plan, directly, host.onError, manager = host.manager) },
                        Modifier.fillMaxWidth().testTag("plan-start-${plan.id}"), size = JButtonSize.Medium, loading = busy)
                },
                edit = {
                    JButton(stringResource(R.string.chat_plan_edit), onEdit, Modifier.fillMaxWidth().testTag("plan-edit-${plan.id}"),
                        kind = JButtonKind.Secondary, size = JButtonSize.Medium, enabled = !busy)
                },
                dismiss = {
                    JButton(stringResource(R.string.chat_tasks_plan_dismiss), { host.tasks.dismissPlan(plan, host.onError, host.manager) },
                        Modifier.fillMaxWidth().testTag("plan-dismiss-${plan.id}"), kind = JButtonKind.Secondary, size = JButtonSize.Medium,
                        enabled = !busy)
                },
            )
        }
    }
}

/**
 * Start, Edit and Don't start in iOS's order: one row, Start taking what the other two leave, while Start's whole label fits
 * beside them; else Start has a row of its own and the other two share the one under it. A label is never cut: iOS's
 * `MessageCard` stacks its buttons the same way (`ViewThatFits`), and a phone 360 dp wide or a longer label (ja, "Start 12
 * tasks") needs it. Each is measured once, the choice made from their widths without cutting.
 */
@Composable
private fun PlanActions(start: @Composable () -> Unit, edit: @Composable () -> Unit, dismiss: @Composable () -> Unit) {
    val gap = Tokens.Space.s
    Layout(content = { start(); edit(); dismiss() }, modifier = Modifier.fillMaxWidth()) { measurables, constraints ->
        val (startItem, editItem, dismissItem) = measurables
        val space = gap.roundToPx()
        val width = constraints.maxWidth
        val unbounded = Constraints.Infinity
        val startWants = startItem.maxIntrinsicWidth(unbounded)
        val editWants = editItem.maxIntrinsicWidth(unbounded)
        val dismissWants = dismissItem.maxIntrinsicWidth(unbounded)
        if (startWants + editWants + dismissWants + 2 * space <= width) {
            val startPlaceable = startItem.measure(Constraints.fixedWidth(width - editWants - dismissWants - 2 * space))
            val editPlaceable = editItem.measure(Constraints.fixedWidth(editWants))
            val dismissPlaceable = dismissItem.measure(Constraints.fixedWidth(dismissWants))
            val height = maxOf(startPlaceable.height, editPlaceable.height, dismissPlaceable.height)
            layout(width, height) {
                startPlaceable.placeRelative(0, (height - startPlaceable.height) / 2)
                editPlaceable.placeRelative(startPlaceable.width + space, (height - editPlaceable.height) / 2)
                dismissPlaceable.placeRelative(startPlaceable.width + editPlaceable.width + 2 * space, (height - dismissPlaceable.height) / 2)
            }
        } else {
            val half = (width - space) / 2
            val startPlaceable = startItem.measure(Constraints.fixedWidth(width))
            val editPlaceable = editItem.measure(Constraints.fixedWidth(half))
            val dismissPlaceable = dismissItem.measure(Constraints.fixedWidth(width - space - half))
            val second = maxOf(editPlaceable.height, dismissPlaceable.height)
            layout(width, startPlaceable.height + space + second) {
                startPlaceable.placeRelative(0, 0)
                editPlaceable.placeRelative(0, startPlaceable.height + space + (second - editPlaceable.height) / 2)
                dismissPlaceable.placeRelative(half + space, startPlaceable.height + space + (second - dismissPlaceable.height) / 2)
            }
        }
    }
}

/**
 * One task of an open plan (iOS `PlanTaskRow`): its title, where it would run (the computer, the project, the model or launcher
 * and, when one is asked for, that it only reads or the permission it starts with), each a chip that wraps, and why there.
 */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun GlassPlanTask(number: Int, task: PlanTask, numbered: Boolean, host: AssistantTaskHost) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
        Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            if (numbered) JNumberDisc(number)
            Text(task.title, Modifier.weight(1f, fill = false), style = type.bodyStrong, color = palette.ink)
        }
        FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
            JChip(host.machineName(task.machine), icon = PersonaIcons.laptop)
            JChip(task.project, icon = PersonaIcons.folder)
            JChip(planModelLabel(task), icon = PersonaIcons.cpu)
            // iOS: a read-only task says so, in the accent; else the permission it starts with, when one is set.
            if (task.readonly == true) JChip(stringResource(R.string.chat_tasks_read_only), icon = PersonaIcons.view, accent = true)
            else task.permission?.takeIf(String::isNotEmpty)?.let { JChip(permissionLabel(it)) }
        }
        task.why?.takeIf(String::isNotBlank)?.let { Text(it, style = type.caption, color = palette.ink2) }
        task.brief?.let { PlanTaskBrief(it, task.title) }
    }
}

/**
 * "Start these directly" (iOS's label button): a ring that becomes a check in the accent once chosen, with the words in the
 * accent too, one line. Such tasks (the same computer, in that folder or one under it, read or edit alike; #8095) then start without asking again.
 */
@Composable
private fun StartDirectly(on: Boolean, enabled: Boolean, onChange: (Boolean) -> Unit) {
    val palette = PersonaTheme.palette
    val tint = if (on) palette.accent else palette.ink2
    Row(Modifier.toggleable(value = on, enabled = enabled, role = Role.Checkbox, onValueChange = onChange).testTag("plan-directly"),
        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        if (on) JIcon(PersonaIcons.successFilled, decorative = true, size = Tokens.Size.markXS, tint = tint, selected = true)
        else Box(Modifier.size(Tokens.Size.markXS).border(Tokens.Size.line, tint, CircleShape))
        Text(stringResource(R.string.chat_tasks_plan_remember), style = PersonaTheme.type.caption, color = tint, maxLines = 1,
            overflow = TextOverflow.Ellipsis)
    }
}

/** A plan that was not started, as iOS's `settled`: its mark (a cross) and "Not started · N tasks", one line. */
@Composable
internal fun GlassPlanNotStarted(count: Int) {
    val palette = PersonaTheme.palette
    Row(Modifier.testTag("plan-not-started"), verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
        JIcon(PersonaIcons.close, decorative = true, size = Tokens.Size.markXS, tint = palette.ink2)
        Text(pluralStringResource(R.plurals.chat_tasks_plan_dismissed, count, count), style = PersonaTheme.type.callout,
            color = palette.ink2, maxLines = 1, overflow = TextOverflow.Ellipsis)
    }
}
