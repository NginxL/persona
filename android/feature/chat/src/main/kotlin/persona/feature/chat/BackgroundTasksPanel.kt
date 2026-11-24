package persona.feature.chat

import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.runtime.Composable
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.SideEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.key
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberUpdatedState
import androidx.compose.runtime.setValue
import androidx.compose.runtime.snapshotFlow
import androidx.compose.ui.Modifier
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.repeatOnLifecycle
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.delay
import kotlinx.coroutines.ensureActive
import kotlinx.coroutines.flow.drop
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.launch
import persona.core.contracts.BackgroundTask
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.SessionRowEvent
import persona.core.contracts.SessionsEvent
import persona.core.contracts.SessionsListEvent
import persona.core.contracts.Workflow
import persona.core.contracts.WorkflowStatus
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.ConnectionState
import persona.core.contracts.api.HubConnection
import persona.core.contracts.api.SessionFiles
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JFullScreen
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JLoading
import persona.core.designsystem.JNotice
import persona.core.designsystem.JPage
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JTopBar
import persona.core.designsystem.JWorkflowCard
import persona.core.designsystem.LocalHomeChrome
import persona.core.designsystem.LocalRootLeading
import persona.core.designsystem.Tokens
import persona.core.designsystem.WorkflowIcon
import persona.core.designsystem.hardwareShortcutDismiss
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.pagePadding
import persona.core.domain.sort.WorkflowTimeline
import java.time.Instant

/** Keep the user's entry attached to the same computer, session and manager. */
internal data class BackgroundPanelRequest(val session: SessionKey, val manager: String?, val target: BackgroundDestination)

internal data class BackgroundDetail(val row: Session? = null, val loading: Boolean = false, val failed: Boolean = false)

/** A summary omits individual tasks. Its push hints trigger a coalesced exact lookup while this panel is shown. */
@Composable
internal fun backgroundDetail(
    row: Session, actions: ChatActions?, connection: HubConnection?, exact: Boolean, retry: Int,
): BackgroundDetail {
    val latest by rememberUpdatedState(row)
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    var held by remember(row.key, row.manager, actions, connection) { mutableStateOf(BackgroundDetail()) }
    val summary = row.waiting != null
    // The session list can switch between full and bounded rows for this owner.
    SideEffect { if (!summary) held = BackgroundDetail(row) }
    LaunchedEffect(row.key, row.manager, summary, actions, connection, exact, retry, lifecycle) {
        if (!summary || !exact || actions == null) return@LaunchedEffect
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            coroutineScope {
                val requests = Channel<Unit>(Channel.CONFLATED)
                requests.trySend(Unit)
                if (connection != null) {
                    launch { connection.events.collect { event ->
                        val changed = when (event) {
                            is SessionsListEvent -> true
                            is SessionsEvent -> event.machineId == row.machineId
                            is SessionRowEvent -> event.session.key == row.key
                            else -> false
                        }
                        if (changed) requests.trySend(Unit)
                    } }
                    launch { connection.state.drop(1).collect { if (it == ConnectionState.Online) requests.trySend(Unit) } }
                    launch { connection.currentHello.drop(1).collect { if (it != null) requests.trySend(Unit) } }
                }
                for (ignored in requests) {
                    delay(100)
                    requests.tryReceive()
                    held = held.copy(loading = true, failed = false)
                    val fresh = try {
                        actions.completeRow(latest)?.takeIf {
                            it.key == row.key && it.manager == row.manager && it.waiting == null
                        }
                    } catch (cancelled: CancellationException) {
                        throw cancelled
                    } catch (_: Exception) {
                        null
                    }
                    currentCoroutineContext().ensureActive()
                    held = if (fresh != null) BackgroundDetail(row = fresh) else held.copy(loading = false, failed = true)
                }
            }
        }
    }
    return when {
        !summary -> BackgroundDetail(row)
        !exact || actions == null -> held.copy(loading = false, failed = true)
        else -> held.copy(loading = held.loading || held.row == null && !held.failed)
    }
}

@Composable
internal fun BackgroundTasksPanel(
    request: BackgroundPanelRequest,
    summary: Session,
    actions: ChatActions?,
    connection: HubConnection?,
    exact: Boolean,
    files: SessionFiles?,
    onStopLoop: (() -> Unit)?,
    onOpenFile: ((String) -> Unit)?,
    onOpenAgent: ((SessionKey) -> Unit)?,
    onDismiss: () -> Unit,
) {
    var retry by remember(request) { mutableIntStateOf(0) }
    val detail = backgroundDetail(summary, actions, connection, exact, retry)
    val session = detail.row
    val now by produceState(Instant.now()) {
        while (true) { delay(30_000); value = Instant.now() }
    }
    val sections = remember(session, now) { session?.let { backgroundSections(it, now) } }
    var selection by remember(request) { mutableStateOf(request.target) }
    var returned by remember(request) { mutableStateOf<String?>(null) }
    val listState = rememberLazyListState()
    val workflowScroll = key((selection as? BackgroundDestination.Run)?.id) { rememberLazyListState() }
    val commandScroll = key((selection as? BackgroundDestination.Command)?.id) { rememberScrollState() }
    val focus = remember { FocusRequester() }
    val listTitle = stringResource(R.string.chat_background_title)

    LaunchedEffect(returned, selection, sections) {
        val target = returned ?: return@LaunchedEffect
        val snapshot = sections ?: return@LaunchedEffect
        if (selection != BackgroundDestination.List) return@LaunchedEffect
        val keys = buildList {
            if (detail.failed) add("error")
            if (snapshot.runningCount > 0 || session?.loop != null) add("running")
            if (session?.loop != null) add("loop")
            snapshot.runningRuns.forEach { add("run:" + it.backgroundId()) }
            snapshot.runningCommands.forEach { add("command:" + it.task.id) }
            if (snapshot.completedRuns.isNotEmpty() || snapshot.completedCommands.isNotEmpty()) add("completed")
            snapshot.completedRuns.forEach { add("run:" + it.backgroundId()) }
            snapshot.completedCommands.forEach { add("command:" + it.task.id) }
        }
        val index = keys.indexOf(target)
        if (index < 0) {
            returned = null
        } else {
            val visible = snapshotFlow { listState.layoutInfo.visibleItemsInfo }.first { it.isNotEmpty() }
            if (visible.none { it.key == target }) listState.scrollToItem(index)
        }
    }

    LaunchedEffect(selection, sections, detail.failed) {
        if (selection == BackgroundDestination.LatestWorkflow && (sections != null || detail.failed))
            selection = sections?.latestWorkflow?.let { BackgroundDestination.Run(it.backgroundId()) } ?: BackgroundDestination.List
    }

    fun back() {
        when (val current = selection) {
            BackgroundDestination.List, BackgroundDestination.LatestWorkflow -> onDismiss()
            is BackgroundDestination.Run -> { returned = "run:" + current.id; selection = BackgroundDestination.List }
            is BackgroundDestination.Command -> { returned = "command:" + current.id; selection = BackgroundDestination.List }
        }
    }
    fun openRun(run: Workflow) { selection = BackgroundDestination.Run(run.backgroundId()) }
    fun openCommand(command: BackgroundCommand) {
        if (command.opens) selection = BackgroundDestination.Command(command.task.id, command.task)
    }

    val run = (selection as? BackgroundDestination.Run)?.let { sections?.workflow(it.id) }
    val chosen = selection as? BackgroundDestination.Command
    var last by remember(chosen?.id) { mutableStateOf(chosen?.initial) }
    val command = chosen?.let { destination -> sections?.command(destination.id, last ?: destination.initial) }
    SideEffect { if (command != null && command.outcome != BackgroundOutcome.Unknown) last = command.task }
    val title = when {
        run != null -> run.title ?: stringResource(persona.core.designsystem.R.string.designsystem_workflow_untitled)
        command != null -> ChatActivity.taskTitle(LocalResources.current, command.task)
        else -> listTitle
    }

    JFullScreen(onDismiss = ::back) {
        Box(Modifier.fillMaxSize().hardwareShortcutDismiss(::back).testTag("background-panel")) {
            JPage(title, topBar = {
                val showingList = selection == BackgroundDestination.List || selection == BackgroundDestination.LatestWorkflow
                CompositionLocalProvider(
                    LocalHomeChrome provides null,
                    LocalRootLeading provides { JIconButton(PersonaIcons.close, stringResource(R.string.chat_background_close), onDismiss) },
                ) {
                    JTopBar(title, onBack = if (showingList) null else ::back)
                }
            }, underTop = {
                when (selection) {
                    is BackgroundDestination.Run -> workflowScroll.canScrollBackward
                    is BackgroundDestination.Command -> commandScroll.canScrollBackward
                    else -> listState.canScrollBackward
                }
            }) { padding ->
                val insets = PaddingValues(
                    start = pagePadding, end = pagePadding, top = padding.calculateTopPadding() + Tokens.Space.s,
                    bottom = padding.calculateBottomPadding() + Tokens.Space.m,
                )
                when {
                    command != null -> BackgroundCommandDetail(session ?: summary, command, files, onOpenFile, insets, commandScroll)
                    run != null -> LazyColumn(Modifier.fillMaxSize(), state = workflowScroll, contentPadding = insets) {
                        item { JWorkflowCard(run, onOpenAgent = onOpenAgent?.let { open -> { agent ->
                            open(SessionKey(summary.machineId, agent.sessionId))
                        } }) }
                    }
                    selection is BackgroundDestination.Run && sections != null ->
                        LazyColumn(Modifier.fillMaxSize(), state = workflowScroll, contentPadding = insets) {
                            item { JNotice(stringResource(R.string.chat_background_run_unavailable)) }
                        }
                    else -> LazyColumn(Modifier.fillMaxSize().testTag("background-list"), state = listState, contentPadding = insets,
                        verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                        if (detail.loading && session == null) item(key = "loading") { JLoading() }
                        if (detail.failed) item(key = "error") {
                            val canRetry = exact && actions != null
                            JNotice(stringResource(R.string.chat_background_details_failed),
                                actionLabel = if (canRetry) stringResource(R.string.chat_background_retry) else null,
                                onAction = if (canRetry) ({ retry++ }) else null)
                        }
                        if (sections != null && session != null) {
                            val loop = session.loop
                            val running = sections.runningCount != 0 || loop != null
                            val ended = sections.completedRuns.isNotEmpty() || sections.completedCommands.isNotEmpty()
                            if (!running && !ended) item(key = "empty") {
                                JNotice(stringResource(R.string.chat_background_empty))
                            }
                            if (running) item(key = "running") { JSectionHeader(stringResource(R.string.chat_background_running)) }
                            if (loop != null) item(key = "loop") { BackgroundLoopStrip(loop, onStopLoop) }
                            itemsIndexed(sections.runningRuns, key = { _, value -> "run:" + value.backgroundId() }) { index, value ->
                                val id = "run:" + value.backgroundId()
                                BackgroundWorkflowRow(value, index, sections.runningRuns.size, itemFocus(id, returned, focus) { returned = null }) { openRun(value) }
                            }
                            itemsIndexed(sections.runningCommands, key = { _, value -> "command:" + value.task.id }) { index, value ->
                                val id = "command:" + value.task.id
                                BackgroundCommandRow(value, now, index, sections.runningCommands.size, itemFocus(id, returned, focus) { returned = null }) { openCommand(value) }
                            }
                            if (ended) item(key = "completed") { JSectionHeader(stringResource(R.string.chat_background_completed)) }
                            itemsIndexed(sections.completedRuns, key = { _, value -> "run:" + value.backgroundId() }) { index, value ->
                                val id = "run:" + value.backgroundId()
                                BackgroundWorkflowRow(value, index, sections.completedRuns.size, itemFocus(id, returned, focus) { returned = null }) { openRun(value) }
                            }
                            itemsIndexed(sections.completedCommands, key = { _, value -> "command:" + value.task.id }) { index, value ->
                                val id = "command:" + value.task.id
                                BackgroundCommandRow(value, now, index, sections.completedCommands.size, itemFocus(id, returned, focus) { returned = null }) { openCommand(value) }
                            }
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun itemFocus(id: String, returned: String?, requester: FocusRequester, used: () -> Unit): Modifier {
    if (id != returned) return Modifier
    LaunchedEffect(id) { requester.requestFocus(); used() }
    return Modifier.focusRequester(requester)
}

@Composable
internal fun BackgroundWorkflowRow(run: Workflow, index: Int, total: Int, modifier: Modifier, onOpen: () -> Unit) {
    val status = stringResource(when (run.state) {
        WorkflowStatus.Running -> R.string.chat_background_running
        WorkflowStatus.Failed, WorkflowStatus.Stalled -> R.string.chat_background_failed
        WorkflowStatus.Killed -> R.string.chat_background_stopped
        WorkflowStatus.Completed -> R.string.chat_background_completed
    })
    JGroupItem(GroupPosition.of(index, total)) {
        JRow(run.title ?: stringResource(persona.core.designsystem.R.string.designsystem_workflow_untitled),
            modifier = modifier, subtitle = stringResource(R.string.chat_background_workflow_meta, status, WorkflowTimeline.counts(run)),
            leadingIcon = WorkflowIcon, onClick = onOpen)
    }
}

@Composable
internal fun BackgroundCommandRow(command: BackgroundCommand, now: Instant, index: Int, total: Int, modifier: Modifier, onOpen: () -> Unit) {
    val res = LocalResources.current
    val elapsed = command.elapsed(now)?.let { backgroundDuration(it) }
    val subtitle = listOfNotNull(backgroundStatus(command.outcome), elapsed).joinToString(stringResource(R.string.chat_background_separator))
    JGroupItem(GroupPosition.of(index, total)) {
        JRow(ChatActivity.taskTitle(res, command.task), modifier = modifier.takeIf { command.opens } ?: Modifier,
            subtitle = subtitle,
            leadingIcon = when (command.task.kind) {
                "shell" -> PersonaIcons.terminal
                "monitor" -> PersonaIcons.backgroundMonitor
                else -> PersonaIcons.backgroundTasks
            },
            onClick = onOpen.takeIf { command.opens })
    }
}
