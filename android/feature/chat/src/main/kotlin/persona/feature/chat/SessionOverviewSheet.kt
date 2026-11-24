package persona.feature.chat

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.heightIn
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.LazyListState
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.layout.Layout
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextOverflow
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.delay
import persona.core.contracts.GroupKeys
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatActions
import persona.core.contracts.api.HubConnection
import persona.core.contracts.parseTime
import persona.core.designsystem.AgentMarkKind
import persona.core.designsystem.AgentMarkSpec
import persona.core.designsystem.AgentSurface
import persona.core.designsystem.CopyAction
import persona.core.designsystem.GroupPosition
import persona.core.designsystem.JAgentMark
import persona.core.designsystem.JGroupItem
import persona.core.designsystem.JIcon
import persona.core.designsystem.JLoading
import persona.core.designsystem.JNotice
import persona.core.designsystem.JRow
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.JSheet
import persona.core.designsystem.JSpinner
import persona.core.designsystem.JStatusDot
import persona.core.designsystem.JTag
import persona.core.designsystem.LocalHubFeatures
import persona.core.designsystem.LocalizedFormats
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Status
import persona.core.designsystem.Tokens
import persona.core.designsystem.agentMark
import persona.core.designsystem.icons.PersonaIcons
import persona.core.designsystem.knownAgentMark
import persona.core.designsystem.operationFeedback
import persona.core.designsystem.rememberCopy
import persona.core.designsystem.rowPadding
import persona.core.domain.sort.OverviewSubagent
import persona.core.domain.sort.OverviewSubagentStatus
import persona.core.domain.sort.buildOverviewSubagents
import persona.core.domain.sort.listedModes
import persona.core.domain.sort.resumeCommand
import java.time.Duration
import java.time.Instant
import java.time.ZoneId

private const val MAX_ENDED_STACKED_MARKS = 4

/**
 * Unified Session Overview sheet ordered strictly per the multi-platform design specification:
 * Environment -> Context -> Subagents -> Background tasks.
 */
@Composable
internal fun SessionOverviewSheet(
    session: Session,
    machine: Machine?,
    machines: List<Machine>,
    childRows: List<Session>,
    state: ChatChromeState,
    actions: ChatActions? = null,
    connection: HubConnection? = null,
    scrollToSubagents: Boolean = false,
    onSubagentsScrolled: () -> Unit = {},
    endedExpanded: Boolean? = null,
    onEndedExpandedChange: ((Boolean) -> Unit)? = null,
    listState: LazyListState = rememberLazyListState(),
    onOpenTodo: ((String) -> Unit)? = null,
    onOpenSubagent: ((SessionKey) -> Unit)? = null,
    onOpenBackground: ((BackgroundDestination) -> Unit)? = null,
    onStopLoop: (() -> Unit)? = null,
    onDismiss: () -> Unit,
) {
    val copyText = rememberCopy()
    val res = LocalResources.current
    val uriHandler = LocalUriHandler.current
    val features = LocalHubFeatures.current
    val exact = "sessions_lookup" in features
    var info by remember(session.key) { mutableStateOf<SessionInfo?>(null) }
    var infoError by remember(session.key) { mutableStateOf<String?>(null) }
    var infoAttempt by remember(session.key) { mutableIntStateOf(0) }
    var bgRetry by remember(session.key) { mutableIntStateOf(0) }
    var localEndedExpanded by rememberSaveable(session.key.path) { mutableStateOf(false) }
    val isEndedExpanded = endedExpanded ?: localEndedExpanded
    val toggleEndedExpanded = {
        val next = !isEndedExpanded
        if (onEndedExpandedChange != null) onEndedExpandedChange(next) else localEndedExpanded = next
    }

    LaunchedEffect(session.key, infoAttempt) {
        infoError = null
        try {
            info = state.info(session)
        } catch (e: CancellationException) {
            throw e
        } catch (e: Exception) {
            infoError = res.getString(
                R.string.chat_info_unreadable,
                (e as? UnsupportedOperationException)?.message ?: operationFeedback(e).text.resolve(res),
            )
        }
    }

    // On bounded/paginated hubs, a summary=1 parent row omits embedded `subagents`, `background`, `workflows`, and `loop`.
    // When `completeDetail.row` arrives (summary=0), its detail fields are authoritative (e.g. `loop == null` means stopped).
    val completeDetail = backgroundDetail(session, actions, connection, exact, bgRetry)
    val effectiveSession = remember(session, completeDetail.row) {
        val full = completeDetail.row ?: return@remember session
        session.copy(
            subagents = full.subagents,
            background = full.background,
            backgroundEnded = full.backgroundEnded,
            workflows = full.workflows,
            loop = full.loop,
        )
    }

    val machineNames = remember(machines) { machines.associate { it.id to it.displayName } }
    val subagents = remember(effectiveSession, childRows, machineNames) {
        buildOverviewSubagents(effectiveSession, childRows, machineNames)
    }
    val hasRunningSubagents = subagents.running.isNotEmpty()
    val now by produceState(Instant.now(), hasRunningSubagents) {
        while (true) {
            delay(if (hasRunningSubagents) 1_000L else 30_000L)
            value = Instant.now()
        }
    }

    val bgDetail = completeDetail.takeIf { !effectiveSession.assistant }
    val bgSession = effectiveSession
    val bgSections = remember(bgSession, now) {
        if (!bgSession.assistant) backgroundSections(bgSession, now) else null
    }

    val envRows = remember(effectiveSession, machine, info, features, res) {
        overviewEnvironmentRows(res, effectiveSession, machine, info, features, onOpenTodo)
    }
    val contextRows = remember(effectiveSession, info, res) {
        overviewContextRows(res, effectiveSession, info)
    }

    val hasBackground = !effectiveSession.assistant && bgSections != null && (
        bgSession.loop != null ||
            bgSections.runningRuns.isNotEmpty() ||
            bgSections.runningCommands.isNotEmpty() ||
            bgSections.completedRuns.isNotEmpty() ||
            bgSections.completedCommands.isNotEmpty()
        )
    val loadingEmbeddedSubagents = subagents.isEmpty && completeDetail.loading && session.waiting != null
    val failedEmbeddedSubagents = subagents.isEmpty && completeDetail.failed && session.waiting != null && completeDetail.row == null
    val subagentsSectionMounted = !subagents.isEmpty || loadingEmbeddedSubagents || failedEmbeddedSubagents

    // Keep the scroll request pending until the Subagents section header is actually mounted in the list.
    LaunchedEffect(scrollToSubagents, subagentsSectionMounted, envRows.size, contextRows.size, info != null, infoError != null) {
        if (!scrollToSubagents || !subagentsSectionMounted) return@LaunchedEffect
        val index = computeSubagentsHeaderIndex(
            hasExternalLink = effectiveSession.externalLink != null,
            hasDelegated = !effectiveSession.assistant,
            envRowCount = envRows.size,
            hasInfoError = infoError != null && info == null,
            hasInfoLoading = info == null && infoError == null,
            contextRowCount = contextRows.size,
        )
        listState.scrollToItem(index)
        onSubagentsScrolled()
    }

    JSheet(onDismiss = onDismiss, title = stringResource(R.string.chat_overview_title), scrollable = false) {
        LazyColumn(
            modifier = Modifier.fillMaxWidth().testTag("session-overview-list"),
            state = listState,
            contentPadding = PaddingValues(bottom = Tokens.Space.xl),
            verticalArrangement = Arrangement.spacedBy(Tokens.Space.flush),
        ) {
            item(key = "header") {
                Column(
                    Modifier.fillMaxWidth().padding(bottom = Tokens.Space.s),
                    verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    Text(
                        session.displayTitle,
                        style = PersonaTheme.type.headline,
                        color = PersonaTheme.palette.ink,
                        maxLines = 2,
                        overflow = TextOverflow.Ellipsis,
                    )
                    val status = Status.entries.firstOrNull { it.name == session.agentState.name }
                    Text(
                        listOfNotNull(
                            status?.localizedLabel,
                            machine?.displayName ?: GroupKeys.raw(session.machineId),
                            session.spaceName?.takeIf(String::isNotBlank),
                        ).joinToString(" / "),
                        style = PersonaTheme.type.caption,
                        color = PersonaTheme.palette.ink2,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }

            session.externalLink?.let { url ->
                item(key = "external-link") {
                    JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.s)) {
                        JRow(
                            stringResource(R.string.chat_host_open_feishu),
                            leadingIcon = PersonaIcons.openExternal,
                            chevron = false,
                            onClick = { val _ = runCatching { uriHandler.openUri(url) } },
                        )
                    }
                }
            }

            if (!session.assistant) {
                item(key = "delegated-history") {
                    DelegatedHistorySection(session, state)
                }
            }

            // 1. Environment
            item(key = "section-environment") {
                JSectionHeader(
                    title = stringResource(R.string.chat_overview_section_environment),
                    leading = {
                        JIcon(PersonaIcons.machine, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2, decorative = true)
                    },
                )
            }
            itemsIndexed(envRows, key = { idx, row -> "env:$idx:${row.label}" }) { index, row ->
                OverviewFactGroupRow(row, index, envRows.size, copyText, res, onDismiss)
            }
            if (info == null && infoError != null) {
                item(key = "info-error") {
                    JNotice(
                        infoError.orEmpty(),
                        Modifier.padding(top = Tokens.Space.s),
                        status = Status.NeedsYou,
                        icon = PersonaIcons.error,
                        actionLabel = stringResource(R.string.chat_info_retry),
                        onAction = { infoAttempt++ },
                    )
                }
            } else if (info == null) {
                item(key = "info-loading") {
                    JLoading()
                }
            }

            // 2. Context
            if (contextRows.isNotEmpty()) {
                item(key = "section-context") {
                    JSectionHeader(
                        title = stringResource(R.string.chat_overview_section_context),
                        leading = {
                            JIcon(PersonaIcons.context, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2, decorative = true)
                        },
                    )
                }
                itemsIndexed(contextRows, key = { idx, row -> "ctx:$idx:${row.label}" }) { index, row ->
                    OverviewFactGroupRow(row, index, contextRows.size, copyText, res, onDismiss)
                }
            }

            // 3. Subagents
            if (subagentsSectionMounted) {
                item(key = "section-subagents") {
                    val countSummary = listOfNotNull(
                        subagents.running.size.takeIf { it > 0 }?.let { stringResource(R.string.chat_overview_subagents_running_count, it) },
                        subagents.ended.size.takeIf { it > 0 }?.let { stringResource(R.string.chat_overview_subagents_ended_count, it) },
                    ).joinToString(" · ").takeIf { it.isNotEmpty() }
                    JSectionHeader(
                        title = stringResource(R.string.chat_overview_section_subagents),
                        trailing = countSummary,
                        leading = {
                            JIcon(PersonaIcons.agents, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2, decorative = true)
                        },
                    )
                }

                if (loadingEmbeddedSubagents) {
                    item(key = "subagents-loading") {
                        JLoading()
                    }
                } else if (failedEmbeddedSubagents) {
                    item(key = "subagents-error") {
                        val canRetry = exact && actions != null
                        JNotice(
                            stringResource(R.string.chat_background_details_failed),
                            status = Status.NeedsYou,
                            icon = PersonaIcons.error,
                            actionLabel = if (canRetry) stringResource(R.string.chat_info_retry) else null,
                            onAction = if (canRetry) ({ bgRetry++ }) else null,
                        )
                    }
                }

                // Running · N (hidden completely when 0)
                if (subagents.running.isNotEmpty()) {
                    item(key = "subagents-running-header") {
                        JSectionHeader(
                            title = stringResource(R.string.chat_overview_subagents_running_group, subagents.running.size),
                            sub = true,
                        )
                    }
                    itemsIndexed(subagents.running, key = { _, item -> "sub-run:${item.id}" }) { index, item ->
                        OverviewSubagentRow(
                            item = item,
                            now = now,
                            position = GroupPosition.of(index, subagents.running.size),
                            onClick = onOpenSubagent?.let { open -> { open(item.targetKey) } },
                        )
                    }
                }

                // Ended · N (collapsed by default with up to 4 stacked harness brand icons)
                if (subagents.ended.isNotEmpty()) {
                    item(key = "subagents-ended-toggle") {
                        val marks = remember(subagents.ended) {
                            subagents.ended.map { it.agentMarkSpec() }.distinctBy { it.kind to it.name }.take(MAX_ENDED_STACKED_MARKS)
                        }
                        EndedSubagentsToggleRow(
                            count = subagents.ended.size,
                            marks = marks,
                            expanded = isEndedExpanded,
                            modifier = Modifier.padding(top = if (subagents.running.isNotEmpty()) Tokens.Space.s else Tokens.Space.flush),
                            onToggle = toggleEndedExpanded,
                        )
                    }
                    if (isEndedExpanded) {
                        itemsIndexed(subagents.ended, key = { _, item -> "sub-end:${item.id}" }) { index, item ->
                            OverviewSubagentRow(
                                item = item,
                                now = now,
                                position = GroupPosition.of(index, subagents.ended.size),
                                onClick = onOpenSubagent?.let { open -> { open(item.targetKey) } },
                            )
                        }
                    }
                }
            }

            // 4. Background tasks
            if (hasBackground && bgSections != null) {
                item(key = "section-background") {
                    JSectionHeader(
                        title = stringResource(R.string.chat_overview_section_background),
                        count = bgSections.runningCount.takeIf { it > 0 },
                        leading = {
                            JIcon(PersonaIcons.backgroundTasks, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2, decorative = true)
                        },
                    )
                }
                bgSession.loop?.let { loop ->
                    item(key = "bg-loop") {
                        BackgroundLoopStrip(loop, onStopLoop)
                    }
                }
                val runningRuns = bgSections.runningRuns
                itemsIndexed(runningRuns, key = { _, run -> "bg-run:${run.backgroundId()}" }) { index, run ->
                    BackgroundWorkflowRow(run, index, runningRuns.size, Modifier) {
                        onDismiss()
                        onOpenBackground?.invoke(BackgroundDestination.Run(run.backgroundId()))
                    }
                }
                val runningCmds = bgSections.runningCommands
                itemsIndexed(runningCmds, key = { _, cmd -> "bg-cmd:${cmd.task.id}" }) { index, cmd ->
                    BackgroundCommandRow(cmd, now, index, runningCmds.size, Modifier) {
                        onDismiss()
                        onOpenBackground?.invoke(BackgroundDestination.Command(cmd.task.id, cmd.task))
                    }
                }
                val completedRuns = bgSections.completedRuns
                itemsIndexed(completedRuns, key = { _, run -> "bg-done-run:${run.backgroundId()}" }) { index, run ->
                    BackgroundWorkflowRow(run, index, completedRuns.size, Modifier) {
                        onDismiss()
                        onOpenBackground?.invoke(BackgroundDestination.Run(run.backgroundId()))
                    }
                }
                val completedCmds = bgSections.completedCommands
                itemsIndexed(completedCmds, key = { _, cmd -> "bg-done-cmd:${cmd.task.id}" }) { index, cmd ->
                    BackgroundCommandRow(cmd, now, index, completedCmds.size, Modifier) {
                        onDismiss()
                        onOpenBackground?.invoke(BackgroundDestination.Command(cmd.task.id, cmd.task))
                    }
                }
                if (onOpenBackground != null) {
                    item(key = "bg-open-all") {
                        JGroupItem(GroupPosition.Single, Modifier.padding(top = Tokens.Space.xs)) {
                            JRow(
                                stringResource(R.string.chat_overview_background_open_all),
                                leadingIcon = PersonaIcons.backgroundTasks,
                                chevron = true,
                                onClick = {
                                    onDismiss()
                                    onOpenBackground(BackgroundDestination.List)
                                },
                            )
                        }
                    }
                }
            } else if (bgDetail?.failed == true && !session.assistant) {
                item(key = "section-background-failed") {
                    JSectionHeader(
                        title = stringResource(R.string.chat_overview_section_background),
                        leading = {
                            JIcon(PersonaIcons.backgroundTasks, null, size = Tokens.Size.markS, tint = PersonaTheme.palette.ink2, decorative = true)
                        },
                    )
                    JNotice(
                        stringResource(R.string.chat_background_details_failed),
                        actionLabel = if (exact && actions != null) stringResource(R.string.chat_background_retry) else null,
                        onAction = if (exact && actions != null) ({ bgRetry++ }) else null,
                    )
                }
            }
        }
    }
}

private fun computeSubagentsHeaderIndex(
    hasExternalLink: Boolean,
    hasDelegated: Boolean,
    envRowCount: Int,
    hasInfoError: Boolean,
    hasInfoLoading: Boolean,
    contextRowCount: Int,
): Int {
    var idx = 1 // header
    if (hasExternalLink) idx++
    if (hasDelegated) idx++
    idx += 1 + envRowCount // section-environment + rows
    if (hasInfoError || hasInfoLoading) idx++
    if (contextRowCount > 0) idx += 1 + contextRowCount // section-context + rows
    return idx
}

@Composable
private fun OverviewFactGroupRow(
    row: SessionInfoRow,
    index: Int,
    total: Int,
    copyText: CopyAction,
    res: android.content.res.Resources,
    onDismiss: () -> Unit,
) {
    JGroupItem(GroupPosition.of(index, total)) {
        val value = row.value ?: "—"
        val copy = row.copy?.takeIf { row.value != null }
        if (row.usage != null) {
            ContextUsageRow(row.label, value, row.usage)
        } else if (row.onClick != null) {
            JRow(
                row.label,
                subtitle = value,
                subtitleLines = 2,
                chevron = true,
                onClick = { onDismiss(); row.onClick.invoke() },
            )
        } else if (copy != null) {
            JRow(
                row.label,
                subtitle = value,
                subtitleLines = 2,
                trailing = {
                    JIcon(PersonaIcons.copy, stringResource(R.string.chat_info_copy_item, copy), tint = PersonaTheme.palette.ink2)
                },
                onClick = { copyText(value, res.getString(R.string.chat_info_copied_item, copy)) },
            )
        } else {
            JRow(row.label, value = value)
        }
        row.note?.let {
            Text(
                it,
                Modifier.padding(start = rowPadding, end = rowPadding, bottom = Tokens.Space.m),
                style = PersonaTheme.type.caption,
                color = PersonaTheme.palette.ink2,
            )
        }
    }
}

internal fun overviewEnvironmentRows(
    res: android.content.res.Resources,
    session: Session,
    machine: Machine?,
    info: SessionInfo?,
    features: Set<String> = emptySet(),
    onOpenTodo: ((String) -> Unit)? = null,
): List<SessionInfoRow> {
    fun String?.orSession(fallback: String?) = this?.takeIf(String::isNotBlank) ?: fallback?.takeIf(String::isNotBlank)
    val cwd = info?.cwd.orSession(session.cwd)
    val branch = info?.gitBranch.orSession(session.branch)
    val id = info?.sessionId?.takeIf(String::isNotBlank) ?: session.sessionId
    val mode = info?.permissionMode.orSession(session.permissionMode)
    val directoryLabel = res.getString(R.string.chat_info_directory)
    val branchLabel = res.getString(R.string.chat_info_branch)
    val resumeLabel = res.getString(R.string.chat_info_resume_command)
    return buildList {
        session.todo?.let { todo ->
            add(
                SessionInfoRow(
                    res.getString(R.string.chat_info_todo),
                    todo.title,
                    copy = todo.id,
                    onClick = onOpenTodo?.let { open -> { open(todo.id) } },
                ),
            )
        }
        add(SessionInfoRow(res.getString(R.string.chat_overview_computer), machine?.displayName ?: GroupKeys.raw(session.machineId)))
        cwd?.let { add(SessionInfoRow(directoryLabel, it, copy = directoryLabel)) }
        branch?.let { add(SessionInfoRow(branchLabel, it, copy = branchLabel)) }
        info?.model.orSession(session.model)?.let { add(SessionInfoRow(res.getString(R.string.chat_info_model), it)) }
        info?.launcher.orSession(session.launcher)?.let { add(SessionInfoRow(res.getString(R.string.chat_info_launcher), it)) }
        runtimeStatus(res, session)?.let { add(SessionInfoRow(res.getString(R.string.chat_info_runtime), it)) }
        info?.version?.takeIf(String::isNotBlank)?.let {
            add(SessionInfoRow(if (session.isClaude) "Claude Code" else session.agentLabel, it))
        }
        session.codexRoute?.takeIf(String::isNotEmpty)?.let { route ->
            val appServer = route == "app_server"
            add(
                SessionInfoRow(
                    res.getString(R.string.chat_info_route),
                    res.getString(if (appServer) R.string.chat_info_route_app_server else R.string.chat_info_route_terminal),
                    note = session.codexRouteNote?.takeIf { !appServer && it.isNotBlank() },
                ),
            )
        }
        if (mode != null || session.listedModes != null) {
            val permissionText = session.listedModes?.let { listed ->
                SessionModes.listedOption(mode?.takeIf { it in listed } ?: "default").title(res)
            } ?: SessionModes.modeLabel(mode, res)
            permissionText?.let { add(SessionInfoRow(res.getString(R.string.chat_info_permission), it)) }
        }
        SessionModes.effortLabel(info?.effort.orSession(session.effort), res)?.let {
            add(SessionInfoRow(res.getString(R.string.chat_info_effort), it))
        }
        if (Ultracode.infoShown(session, features)) {
            add(SessionInfoRow(Ultracode.NAME, Ultracode.infoWord(session, res)))
        }
        add(SessionInfoRow("session id", id, copy = "session id"))
        session.resumeCommand(cwd = cwd.orEmpty())?.let { add(SessionInfoRow(resumeLabel, it, copy = resumeLabel)) }
    }
}

internal fun overviewContextRows(
    res: android.content.res.Resources,
    session: Session,
    info: SessionInfo?,
    zone: ZoneId = ZoneId.systemDefault(),
): List<SessionInfoRow> {
    val locale = res.configuration.locales[0]
    val counted = info != null && !info.usageUnknown
    return buildList {
        val tokens = when {
            counted -> info!!.contextTokens
            else -> session.contextTokens
        }
        if (tokens != null && (counted || tokens > 0)) {
            add(SessionInfoRow(res.getString(R.string.chat_info_context), res.getString(R.string.chat_info_tokens, formatTokens(tokens, res))))
        }
        ContextUsage.of(session)?.let { usage ->
            add(
                SessionInfoRow(
                    res.getString(R.string.chat_info_context_left),
                    res.getString(R.string.chat_info_context_left_value, usage.left),
                    usage = usage,
                ),
            )
        }
        if (counted && info != null) {
            if (info.compactions > 0) {
                add(
                    SessionInfoRow(
                        res.getString(R.string.chat_info_compactions),
                        res.getQuantityString(R.plurals.chat_info_compaction_count, info.compactions, info.compactions),
                    ),
                )
            }
            add(SessionInfoRow(res.getString(R.string.chat_info_turns), res.getQuantityString(R.plurals.chat_info_turn_count, info.turns, info.turns)))
            add(SessionInfoRow(res.getString(R.string.chat_info_output), res.getString(R.string.chat_info_tokens, formatTokens(info.outputTokens, res))))
            parseTime(info.startedAt)?.let {
                add(
                    SessionInfoRow(
                        res.getString(R.string.chat_info_started),
                        LocalizedFormats.format(it, LocalizedFormats.MONTH_DAY_TIME, locale, zone),
                    ),
                )
            }
        }
    }
}

/** Collapsed-by-default Ended · N summary row with up to 4 stacked harness brand icons + count + chevron. */
@Composable
private fun EndedSubagentsToggleRow(
    count: Int,
    marks: List<AgentMarkSpec>,
    expanded: Boolean,
    onToggle: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val palette = PersonaTheme.palette
    val stateText = stringResource(if (expanded) R.string.chat_tasks_expanded else R.string.chat_tasks_collapsed)
    val label = stringResource(R.string.chat_overview_subagents_ended_count, count)
    JGroupItem(GroupPosition.Single, modifier) {
        Row(
            Modifier
                .fillMaxWidth()
                .heightIn(min = Tokens.MinTouch.android)
                .clickable(role = Role.Button, onClick = onToggle)
                .semantics(mergeDescendants = true) { stateDescription = stateText }
                .padding(horizontal = rowPadding, vertical = Tokens.Space.s)
                .testTag("overview-ended-toggle"),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            if (marks.isNotEmpty()) StackedHarnessMarks(marks)
            Text(
                label,
                Modifier.weight(1f),
                style = PersonaTheme.type.callout,
                color = palette.ink2,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            JIcon(
                if (expanded) PersonaIcons.collapse else PersonaIcons.expand,
                null,
                size = Tokens.Space.m,
                tint = palette.ink3,
                decorative = true,
            )
        }
    }
}

/** Up to 4 overlapping harness brand icons, each ringed with the sheet surface so stacked marks stay legible. */
@Composable
private fun StackedHarnessMarks(marks: List<AgentMarkSpec>, modifier: Modifier = Modifier) {
    val palette = PersonaTheme.palette
    val badgeSize = Tokens.Size.iconXS
    val step = Tokens.Space.Standard.m
    Layout(
        content = {
            marks.forEach { spec ->
                Box(
                    Modifier
                        .size(badgeSize)
                        .clip(CircleShape)
                        .background(palette.sunken)
                        .border(Tokens.Size.line, palette.surfaceOpaque, CircleShape),
                    contentAlignment = Alignment.Center,
                ) {
                    JAgentMark(spec, size = Tokens.Size.markS)
                }
            }
        },
        modifier = modifier,
    ) { measurables, constraints ->
        val sizePx = badgeSize.roundToPx()
        val stepPx = step.roundToPx()
        val fixed = constraints.copy(minWidth = sizePx, maxWidth = sizePx, minHeight = sizePx, maxHeight = sizePx)
        val placeables = measurables.map { it.measure(fixed) }
        val width = if (placeables.isEmpty()) 0 else sizePx + (placeables.size - 1) * stepPx
        layout(width, sizePx) {
            placeables.forEachIndexed { index, placeable ->
                placeable.placeRelative(index * stepPx, 0, zIndex = index.toFloat())
            }
        }
    }
}

/**
 * Single subagent row in Session Overview:
 * [20dp harness brand mark] Title (1-line) [origin chip?] ... Status >
 * Optional 2nd line ONLY for running items showing latest tool/activity summary.
 */
@Composable
private fun OverviewSubagentRow(
    item: OverviewSubagent,
    now: Instant,
    position: GroupPosition,
    onClick: (() -> Unit)?,
) {
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val res = LocalResources.current
    val title = item.title ?: stringResource(R.string.chat_subagent_untitled)
    val indent = when (item.depth) {
        0 -> Tokens.Space.flush
        1 -> Tokens.Space.l
        else -> Tokens.Space.xxl
    }
    val clickMod = onClick?.let { Modifier.clickable(role = Role.Button, onClick = it) } ?: Modifier
    JGroupItem(position) {
        Row(
            Modifier
                .fillMaxWidth()
                .heightIn(min = Tokens.MinTouch.android)
                .then(clickMod)
                .padding(start = rowPadding + indent, end = rowPadding, top = Tokens.Space.s, bottom = Tokens.Space.s)
                .testTag("overview-subagent-row:${item.id}"),
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
        ) {
            JAgentMark(item.agentMarkSpec(), size = Tokens.Size.iconXS)
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                Row(
                    verticalAlignment = Alignment.CenterVertically,
                    horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
                ) {
                    Text(
                        title,
                        Modifier.weight(1f, fill = false),
                        style = type.callout.copy(
                            fontWeight = if (item.status == OverviewSubagentStatus.NeedsYou) FontWeight.SemiBold else FontWeight.Medium,
                        ),
                        color = palette.ink,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    item.originChip?.let { chip ->
                        JTag(chip)
                    }
                }
                val activityLine = item.activityLine
                if (item.status.isActive && !activityLine.isNullOrBlank()) {
                    Text(
                        activityLine,
                        style = type.micro,
                        color = palette.ink3,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                }
            }
            OverviewSubagentStatusTrailing(item, now, res)
            JIcon(PersonaIcons.disclosure, null, size = Tokens.Space.m, tint = palette.ink3, decorative = true)
        }
    }
}

@Composable
private fun OverviewSubagentStatusTrailing(
    item: OverviewSubagent,
    now: Instant,
    res: android.content.res.Resources,
) {
    val palette = PersonaTheme.palette
    val micro = PersonaTheme.type.micro
    when (item.status) {
        OverviewSubagentStatus.NeedsYou -> Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        ) {
            JStatusDot(Status.NeedsYou, breathes = false)
            Text(
                stringResource(R.string.chat_overview_subagents_needs_you),
                style = micro.copy(fontWeight = FontWeight.Medium),
                color = palette.needsYou.text,
                maxLines = 1,
            )
        }
        OverviewSubagentStatus.Running -> Row(
            verticalAlignment = Alignment.CenterVertically,
            horizontalArrangement = Arrangement.spacedBy(Tokens.Space.xs),
        ) {
            JSpinner(Modifier.size(Tokens.Space.Standard.m), color = palette.working.dot, stroke = Tokens.Space.Standard.xxs)
            val elapsed = item.startedAt?.let { start ->
                val secs = Duration.between(start, now).seconds.coerceAtLeast(1)
                workDuration(res, secs)
            }
            if (elapsed != null) {
                Text(elapsed, style = micro, color = palette.ink2, maxLines = 1)
            }
        }
        OverviewSubagentStatus.Failed -> Text(
            stringResource(R.string.chat_subagent_status_failed),
            style = micro.copy(fontWeight = FontWeight.Medium),
            color = palette.danger.text,
            maxLines = 1,
        )
        OverviewSubagentStatus.Stopped, OverviewSubagentStatus.Ended -> {
            val timestamp = item.updatedAt ?: item.startedAt
            val text = timestamp?.let { relativeTime(it, now, res) }
                ?: if (item.status == OverviewSubagentStatus.Stopped) stringResource(R.string.chat_subagent_status_stopped) else null
            if (!text.isNullOrBlank()) {
                Text(text, style = micro, color = palette.ink3, maxLines = 1)
            }
        }
    }
}

internal fun OverviewSubagent.agentMarkSpec(): AgentMarkSpec {
    session?.agentMark?.let { return it }
    val surface = when (agentSurface) {
        "desktop" -> AgentSurface.Desktop
        "cli" -> AgentSurface.Cli
        else -> null
    }
    val kind = knownAgentMark(agent) ?: knownAgentMark(launcher) ?: knownAgentMark(agentLabelHint) ?: knownAgentMark(agentLabel)
        ?: AgentMarkKind.Claude.takeIf { agent.isNullOrBlank() || agent == "claude" }
    return AgentMarkSpec(kind, agentLabel.ifBlank { "Claude Code" }, surface)
}
