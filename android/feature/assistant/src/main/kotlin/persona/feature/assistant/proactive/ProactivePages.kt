package persona.feature.assistant.proactive

import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import persona.core.contracts.api.Run
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.feature.assistant.R
import java.time.Instant
import java.time.ZoneId

/** What the pages can ask for; the screen turns these into hub calls, navigation and confirmations. */
internal class ProactiveActions(
    val refresh: () -> Unit,
    val newTask: () -> Unit,
    val openTask: (String) -> Unit,
    val toggle: (String, Boolean) -> Unit,
    val taskMenu: (ScheduledTask) -> Unit,
    val openConversation: (SessionKey) -> Unit,
    val allFollowups: () -> Unit,
    val allRuns: (String?) -> Unit,
)

/** How many one-offs and runs the overview shows before "All …". */
private const val FollowupsShown = 3
private const val RunsShown = 3

/**
 * "Proactive": what runs next, the repeating tasks with their switches, the one-offs, and the latest runs (iOS
 * AssistantRoutinesView). Each part opens a page of its own.
 */
@Composable
internal fun OverviewPage(
    state: ProactiveState, machines: List<Machine>, now: Instant, zone: ZoneId, onBack: (() -> Unit)?, actions: ProactiveActions,
) {
    val tasks = state.shownTasks
    val repeating = state.repeating
    val oneOff = state.oneOff
    val runs = state.runList
    val say = rememberProactiveText()
    val retry = stringResource(R.string.assistant_proactive_retry)
    val timeline = remember(tasks, machines, say) { upcoming(say, tasks.orEmpty(), now, zone, machines) }
    val coming = timeline.filter { it.at.isAfter(now.minusSeconds(60)) }.take(4)
    JListPage(
        title = stringResource(R.string.assistant_proactive_title), summary = summaryLine(say, tasks, now, zone), onBack = onBack,
        largeTitle = true, actions = { JIconButton(PhIcons.Plus, stringResource(R.string.assistant_proactive_new_routine), actions.newTask) },
        refreshing = state.refreshing && state.loaded, onRefresh = actions.refresh,
    ) {
        if (!state.loaded) {
            val error = state.tasks.error
            if (error == null) item("loading") { JLoading(text = stringResource(R.string.assistant_proactive_loading)) }
            else item("read-error") {
                JNotice(error, title = stringResource(R.string.assistant_proactive_read_failed), status = Status.NeedsYou, icon = PhIcons.WarningCircle,
                    actionLabel = retry, onAction = actions.refresh)
            }
            return@JListPage
        }
        state.tasks.error?.let { error ->
            item("tasks-error") { JNotice(error, status = Status.NeedsYou, icon = PhIcons.WarningCircle, actionLabel = retry, onAction = actions.refresh) }
        }
        if (tasks.isNullOrEmpty()) item("empty") {
            JEmptyState(
                stringResource(R.string.assistant_proactive_empty_title), stringResource(R.string.assistant_proactive_empty_body),
                icon = PhIcons.CalendarBlank,
                action = { JButton(stringResource(R.string.assistant_proactive_new_routine), actions.newTask, icon = PhIcons.Plus) },
            )
        }
        if (coming.isNotEmpty()) {
            item("upcoming-header") { JSectionHeader(stringResource(R.string.assistant_proactive_upcoming)) }
            itemsIndexed(coming, key = { _, item -> "upcoming-${item.id}" }) { index, item ->
                UpcomingRow(item, now, zone, GroupPosition.of(index, coming.size), onClick = { actions.openTask(item.id) })
            }
        }
        if (!repeating.isNullOrEmpty()) {
            item("routines-header") { JSectionHeader(stringResource(R.string.assistant_proactive_routines), count = repeating.size) }
            itemsIndexed(repeating, key = { index, task -> "routine-$index-${task.id}" }) { index, task ->
                TaskRow(task, state.lastRunOf(task), say, now, zone, GroupPosition.of(index, repeating.size), state.busy(task.id),
                    onToggle = { actions.toggle(task.id, it) }, onOpen = { actions.openTask(task.id) },
                    onMore = { actions.taskMenu(task) })
            }
            item("routines-note") { SectionNote(stringResource(R.string.assistant_proactive_routines_note)) }
        }
        if (!oneOff.isNullOrEmpty()) {
            item("followups-header") { JSectionHeader(stringResource(R.string.assistant_proactive_followups), count = oneOff.size) }
            val shown = oneOff.take(FollowupsShown)
            val more = oneOff.size > FollowupsShown
            itemsIndexed(shown, key = { index, task -> "followup-$index-${task.id}" }) { index, task ->
                OneOffRow(task, machines, say, now, zone, GroupPosition(first = index == 0, last = index == shown.lastIndex && !more),
                    state.busy(task.id), onOpen = { actions.openTask(task.id) }, onMore = { actions.taskMenu(task) })
            }
            if (more) item("followups-more") { MoreRow(pluralStringResource(R.plurals.assistant_proactive_all_followups, oneOff.size, oneOff.size), GroupPosition(first = false, last = true), actions.allFollowups) }
            item("followups-note") { SectionNote(stringResource(R.string.assistant_proactive_followups_note)) }
        }
        if (!runs.isNullOrEmpty()) {
            item("runs-header") { JSectionHeader(stringResource(R.string.assistant_proactive_recent_runs)) }
            val latest = runs.take(RunsShown)
            itemsIndexed(latest, key = { index, run -> "run-$index-${run.id}" }) { index, run ->
                RunRow(run, run.displayTitle(say), parseTime(run.at)?.let { say.moment(it, now, zone) }.orEmpty(),
                    GroupPosition(first = index == 0, last = false), run.conversation(state.assistantMachine), say, actions.openConversation)
            }
            item("runs-more") { MoreRow(stringResource(R.string.assistant_proactive_all_runs), GroupPosition(first = false, last = true)) { actions.allRuns(null) } }
        } else state.runs.error?.let { error ->
            item("runs-header") { JSectionHeader(stringResource(R.string.assistant_proactive_recent_runs)) }
            item("runs-error") { JNotice(error, status = Status.NeedsYou, icon = PhIcons.WarningCircle, actionLabel = retry, onAction = actions.refresh) }
        }
    }
}

/** Every one-off, soonest first. */
@Composable
internal fun FollowupsPage(
    state: ProactiveState, machines: List<Machine>, now: Instant, zone: ZoneId, onBack: () -> Unit, actions: ProactiveActions,
) {
    val oneOff = state.oneOff
    val say = rememberProactiveText()
    val retry = stringResource(R.string.assistant_proactive_retry)
    JListPage(title = stringResource(R.string.assistant_proactive_followups), onBack = onBack, refreshing = state.refreshing && oneOff != null, onRefresh = actions.refresh) {
        state.tasks.error?.let { error ->
            item("error") {
                JNotice(error, title = if (oneOff == null) stringResource(R.string.assistant_proactive_followups_read_failed) else null, status = Status.NeedsYou, icon = PhIcons.WarningCircle,
                    actionLabel = retry, onAction = actions.refresh)
            }
        }
        when {
            oneOff == null -> if (state.tasks.error == null) item("loading") { JLoading() }
            oneOff.isEmpty() -> item("empty") {
                JEmptyState(stringResource(R.string.assistant_proactive_no_followups), stringResource(R.string.assistant_proactive_followups_empty_body), icon = PhIcons.BellSimple)
            }
            else -> {
                itemsIndexed(oneOff, key = { index, task -> "followup-$index-${task.id}" }) { index, task ->
                    OneOffRow(task, machines, say, now, zone, GroupPosition.of(index, oneOff.size), state.busy(task.id),
                        onOpen = { actions.openTask(task.id) }, onMore = { actions.taskMenu(task) })
                }
                item("note") { SectionNote(stringResource(R.string.assistant_proactive_followups_page_note)) }
            }
        }
    }
}

/** Runs newest first, one section per day; [ref] narrows it to one routine (from its page). */
@Composable
internal fun RunsPage(
    state: ProactiveState, ref: String?, now: Instant, zone: ZoneId, onBack: () -> Unit, actions: ProactiveActions,
) {
    val runs = if (ref == null) state.runList else state.runsOf(ref)
    val routine = ref?.let(state::task)
    val say = rememberProactiveText()
    val retry = stringResource(R.string.assistant_proactive_retry)
    val unknownTime = stringResource(R.string.assistant_proactive_unknown_time)
    val days = remember(runs, now, say) {
        runs.orEmpty().fold(mutableListOf<Pair<String, MutableList<Run>>>()) { groups, run ->
            val day = parseTime(run.at)?.let { say.day(it, now, zone) } ?: unknownTime
            if (groups.lastOrNull()?.first == day) groups.last().second += run else groups += day to mutableListOf(run)
            groups
        }
    }
    JListPage(
        title = routine?.title?.takeIf { it.isNotBlank() }?.let { stringResource(R.string.assistant_proactive_routine_runs, it) } ?: stringResource(R.string.assistant_proactive_runs), onBack = onBack,
        refreshing = state.refreshing && runs != null, onRefresh = actions.refresh,
    ) {
        state.runs.error?.let { error ->
            item("error") {
                JNotice(error, title = if (runs == null) stringResource(R.string.assistant_proactive_runs_read_failed) else null, status = Status.NeedsYou, icon = PhIcons.WarningCircle,
                    actionLabel = retry, onAction = actions.refresh)
            }
        }
        when {
            runs == null -> if (state.runs.error == null) item("loading") { JLoading() }
            runs.isEmpty() -> item("empty") {
                JEmptyState(stringResource(R.string.assistant_proactive_no_runs),
                    if (ref == null) stringResource(R.string.assistant_proactive_no_runs_body) else stringResource(R.string.assistant_proactive_no_routine_runs_body),
                    icon = PhIcons.ListChecks)
            }
            else -> days.forEach { (day, list) ->
                item("day-$day") { JSectionHeader(day, count = list.size) }
                itemsIndexed(list, key = { index, run -> "run-$day-$index-${run.id}" }) { index, run ->
                    val time = parseTime(run.at)?.let { say.hm(it, zone) }.orEmpty()
                    RunRow(run, if (ref == null) run.displayTitle(say) else time.ifBlank { run.displayTitle(say) }, if (ref == null) time else "",
                        GroupPosition.of(index, list.size), run.conversation(state.assistantMachine), say, actions.openConversation)
                }
            }
        }
    }
}
