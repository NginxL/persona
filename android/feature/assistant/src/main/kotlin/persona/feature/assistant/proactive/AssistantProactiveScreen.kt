package persona.feature.assistant.proactive

import androidx.activity.compose.BackHandler
import androidx.compose.animation.AnimatedContent
import androidx.compose.foundation.layout.*
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.Saver
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.saveable.rememberSaveableStateHolder
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import persona.core.contracts.Machine
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.ScheduledTask
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcons
import persona.feature.assistant.LocalAssistantHost
import persona.feature.assistant.R
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import java.time.Duration
import java.time.Instant
import java.time.ZoneId

/**
 * Settings › Assistant › Proactive: the assistant's scheduled tasks (once, daily, every N minutes, docs/assistant/assistant.md "Scheduled tasks") and the
 * history of runs. The screen keeps its own sub-pages (a task, a new one, all one-offs, all runs) and back.
 *
 * [onOpenConversation] opens an assistant conversation: the one a run started, the one a follow-up continues, or the
 * new one "Run now" started. [onModelConfiguration] opens Settings › Models for a purpose (`schedule`, `task.<id>`); without
 * it the editor leaves the row out. [management] and [machines] default to the paired hub's (the Persona tab's host).
 * [initialPage] opens a sub-page directly, for debug captures: `routine:<id>`, `new`, `new:<template>`, `followups`,
 * `runs`, `runs:<routine id>`.
 */
@Composable
fun AssistantProactiveScreen(
    onBack: () -> Unit,
    onOpenConversation: (SessionKey) -> Unit,
    modifier: Modifier = Modifier,
    onModelConfiguration: ((String) -> Unit)? = null,
    management: AssistantManagement? = LocalAssistantHost.current?.management,
    machines: List<Machine> = LocalAssistantHost.current?.machines.orEmpty(),
    initialPage: String? = null,
) {
    if (management == null) {
        JListPage(title = stringResource(R.string.assistant_proactive_title), modifier = modifier, onBack = onBack) {
            item("unconnected") {
                JEmptyState(stringResource(R.string.assistant_proactive_unconnected_title), stringResource(R.string.assistant_proactive_unconnected_body),
                    icon = PhIcons.PlugsConnected)
            }
        }
        return
    }
    // Writes and runs the user started go on in the pairing's scope when the page is left halfway.
    val hostScope = LocalAssistantHost.current?.coroutineScope
    val pageScope = rememberCoroutineScope()
    val say = rememberProactiveText()
    // The shell's notice outlives the page: a write that fails after the page is left is still said.
    val feedback = LocalAppFeedback.current
    val store = remember(management, hostScope, say, feedback) {
        ProactiveStore(management.proactive(), hostScope ?: pageScope, say, unheard = { feedback?.notice(it) })
    }
    ProactiveScreen(store, onBack, onOpenConversation, modifier, machines, initialPage, onModelConfiguration)
}

/** One page of the screen; the key is what survives rotation and process death. */
internal sealed interface ProactivePage {
    val key: String

    data object Overview : ProactivePage { override val key = "overview" }
    data class EditRoutine(val id: String) : ProactivePage { override val key get() = "routine:$id" }
    /** [template] indexes [RoutineTemplates]; anything else is a blank routine. */
    data class AddRoutine(val template: Int) : ProactivePage { override val key get() = "new:$template" }
    data object FollowupList : ProactivePage { override val key = "followups" }
    data class RunList(val ref: String?) : ProactivePage { override val key get() = ref?.let { "runs:$it" } ?: "runs" }

    companion object {
        fun parse(key: String): ProactivePage? = when {
            key == "overview" -> Overview
            key == "followups" -> FollowupList
            key == "runs" -> RunList(null)
            key == "new" -> AddRoutine(-1)
            key.startsWith("routine:") -> key.removePrefix("routine:").takeIf { it.isNotBlank() }?.let(::EditRoutine)
            key.startsWith("new:") -> key.removePrefix("new:").toIntOrNull()?.let(::AddRoutine)
            key.startsWith("runs:") -> RunList(key.removePrefix("runs:").ifBlank { null })
            else -> null
        }
    }
}

internal fun startPages(initial: String?): List<ProactivePage> =
    listOf(ProactivePage.Overview) + listOfNotNull(initial?.let(ProactivePage::parse)?.takeIf { it != ProactivePage.Overview })

private val PagesSaver = Saver<List<ProactivePage>, ArrayList<String>>(
    save = { pages -> ArrayList(pages.map { it.key }) },
    restore = { keys -> keys.mapNotNull(ProactivePage::parse).ifEmpty { listOf(ProactivePage.Overview) } },
)

@Composable
internal fun ProactiveScreen(
    store: ProactiveStore,
    onBack: () -> Unit,
    onOpenConversation: (SessionKey) -> Unit,
    modifier: Modifier = Modifier,
    machines: List<Machine> = emptyList(),
    initialPage: String? = null,
    onModelConfiguration: ((String) -> Unit)? = null,
) {
    val state by store.state.collectAsStateWithLifecycle()
    val say = rememberProactiveText()
    val scope = rememberCoroutineScope()
    val feedback = LocalAppFeedback.current
    var notice by remember { mutableStateOf<String?>(null) }
    LaunchedEffect(store) { store.refresh() }
    LaunchedEffect(store, feedback) { store.messages.collect { message -> if (feedback?.notice(message) != true) notice = message } }
    // A run the user started opens its conversation only while this page is started, not after it was left (#3835).
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    val openRun by rememberUpdatedState(onOpenConversation)
    LaunchedEffect(store, lifecycle) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) { store.runsToOpen.collect { openRun(it) } }
    }
    LaunchedEffect(notice) { if (notice != null) { delay(5_000); notice = null } }
    // Relative times ("in 25 min") stay true while the page is open.
    val now by produceState(Instant.now()) { while (true) { delay(30_000); value = Instant.now() } }
    val zone = remember { ZoneId.systemDefault() }
    // Once the soonest thing is due, read again: it moves into the history and the routine's next run moves on.
    // Counted from the last read, not the ticking clock, so a tick cannot cancel the wait.
    val nextDue = remember(state.tasks.value, state.readAt) {
        state.readAt?.let { read -> upcoming(say, state.tasks.value.orEmpty()).firstOrNull { it.at.isAfter(read) }?.at }
    }
    LaunchedEffect(nextDue) {
        val due = nextDue ?: return@LaunchedEffect
        val wait = Duration.between(Instant.now(), due).toMillis() + 5_000
        if (wait in 1..Duration.ofHours(6).toMillis()) { delay(wait); store.refresh() }
    }

    var pages by rememberSaveable(stateSaver = PagesSaver) { mutableStateOf(startPages(initialPage)) }
    val holder = rememberSaveableStateHolder()
    fun push(page: ProactivePage) { if (pages.last() != page) pages = pages + page }
    fun pop() {
        if (pages.size <= 1) return onBack()
        holder.removeState(pages.last().key)
        pages = pages.dropLast(1)
    }
    BackHandler(enabled = pages.size > 1) { pop() }

    val runTask: (String) -> Unit = { id -> store.runNow(id) }
    var templates by remember { mutableStateOf(false) }
    var taskMenu by remember { mutableStateOf<ScheduledTask?>(null) }
    var deleting by remember { mutableStateOf<ScheduledTask?>(null) }
    val actions = ProactiveActions(
        refresh = { scope.launch { store.refresh() } },
        newTask = { templates = true },
        openTask = { push(ProactivePage.EditRoutine(it)) },
        toggle = store::toggle,
        taskMenu = { taskMenu = it },
        openConversation = onOpenConversation,
        allFollowups = { push(ProactivePage.FollowupList) },
        allRuns = { push(ProactivePage.RunList(it)) },
    )

    val pageTransitions = rememberPageTransitions()
    Box(modifier.fillMaxSize()) {
        // The same slide as the app's own pages, including RTL and reduced motion.
        AnimatedContent(
            pages, Modifier.fillMaxSize(), label = "proactive-pages", contentKey = { it.last().key },
            transitionSpec = {
                if (targetState.size >= initialState.size) pageTransitions.push else pageTransitions.pop
            },
        ) { stack ->
            val pageBody: @Composable () -> Unit = {
                val page = stack.last()
                holder.SaveableStateProvider(page.key) {
                    when (page) {
                        ProactivePage.Overview -> OverviewPage(state, machines, now, zone,
                            if (persona.core.designsystem.LocalIsSidebarRoot.current) null else ::pop, actions)
                        is ProactivePage.EditRoutine -> TaskEditorPage(page.id, null, state, store, now, zone, ::pop, onOpenConversation,
                            onAllRuns = { push(ProactivePage.RunList(it)) }, onRunNow = runTask, onModelConfiguration = onModelConfiguration)
                        is ProactivePage.AddRoutine -> TaskEditorPage(null, RoutineTemplates.getOrNull(page.template)?.draft(say) ?: BlankTask,
                            state, store, now, zone, ::pop, onOpenConversation, onAllRuns = { push(ProactivePage.RunList(it)) }, onRunNow = runTask,
                            onModelConfiguration = onModelConfiguration)
                        ProactivePage.FollowupList -> FollowupsPage(state, machines, now, zone, ::pop, actions)
                        is ProactivePage.RunList -> RunsPage(state, page.ref, now, zone, ::pop, actions)
                    }
                }
            }
            // Always wrapped. This page is already inside Root's chrome; the inner modifier sees that and does not
            // dim a second time. Outside Glass the modifier is identity, and the Box keeps editor state across a style change.
            Box(Modifier.fillMaxSize().glassPageChrome()) { pageBody() }
        }
        notice?.let { message ->
            JNotice(message, Modifier.align(Alignment.BottomCenter).navigationBarsPadding().padding(horizontal = pagePadding, vertical = Tokens.Space.l),
                status = Status.NeedsYou, icon = PhIcons.WarningCircle, actionLabel = stringResource(R.string.assistant_proactive_got_it), onAction = { notice = null })
        }
    }

    if (templates) JActionSheet(
        title = stringResource(R.string.assistant_proactive_new_routine), note = stringResource(R.string.assistant_proactive_templates_note),
        onDismiss = { templates = false },
        actions = RoutineTemplates.mapIndexed { index, template ->
            JAction(stringResource(template.name), template.icon) { push(ProactivePage.AddRoutine(index)) }
        } + JAction(stringResource(R.string.assistant_proactive_template_blank), PhIcons.NotePencil) { push(ProactivePage.AddRoutine(-1)) },
    )
    taskMenu?.let { task ->
        JActionSheet(
            title = task.displayTitle(say), note = say.rule(task, now, zone), onDismiss = { taskMenu = null },
            actions = listOfNotNull(
                JAction(stringResource(R.string.assistant_proactive_run_now), PhIcons.Play, enabled = !state.busy(task.id)) { runTask(task.id) },
                JAction(stringResource(R.string.assistant_proactive_edit), PhIcons.PencilSimple) { push(ProactivePage.EditRoutine(task.id)) },
                (task.origin ?: task.ownConversation)?.let { key -> JAction(stringResource(R.string.assistant_proactive_open_chat), PhIcons.ChatCircle) { onOpenConversation(key) } },
                JAction(stringResource(R.string.assistant_proactive_delete), PhIcons.Trash, destructive = true, enabled = !state.busy(task.id)) { deleting = task },
            ),
        )
    }
    deleting?.let { task ->
        JDialog(
            onDismiss = { deleting = null }, title = stringResource(R.string.assistant_proactive_delete_title, task.displayTitle(say)),
            text = stringResource(R.string.assistant_proactive_delete_body), destructive = true,
            confirm = stringResource(R.string.assistant_proactive_delete) to { deleting = null; store.launch { save(TaskChange.Delete(task.id)) } },
            dismiss = stringResource(R.string.assistant_proactive_cancel) to { deleting = null },
        )
    }
}
