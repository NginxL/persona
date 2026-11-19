package persona.feature.knowledge

import persona.core.designsystem.text
import persona.core.domain.sort.workLine
import android.content.Intent
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.platform.LocalUriHandler
import androidx.compose.ui.platform.UriHandler
import androidx.compose.ui.text.AnnotatedString
import androidx.compose.ui.text.SpanStyle
import androidx.compose.ui.text.TextLinkStyles
import androidx.compose.ui.text.buildAnnotatedString
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.text.withStyle
import persona.core.designsystem.*
import persona.core.designsystem.icons.PhIcon
import persona.core.designsystem.icons.PhIcons
import persona.core.designsystem.icons.PersonaIcons
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.repeatOnLifecycle
import com.mikepenz.markdown.compose.components.markdownComponents
import com.mikepenz.markdown.compose.elements.MarkdownBulletList
import com.mikepenz.markdown.compose.elements.MarkdownOrderedList
import com.mikepenz.markdown.compose.elements.listDepth
import com.mikepenz.markdown.m3.Markdown
import com.mikepenz.markdown.m3.markdownColor
import com.mikepenz.markdown.m3.markdownTypography
import com.mikepenz.markdown.model.State as MarkdownState
import com.mikepenz.markdown.model.parseMarkdown
import com.mikepenz.markdown.compose.components.CurrentComponentsBridge
import persona.core.contracts.AgentState
import persona.core.contracts.Session
import persona.core.contracts.api.*
import persona.core.navigation.AppTab
import persona.core.navigation.ChatRoute
import persona.core.navigation.ConversationKind
import persona.core.navigation.Navigator
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import java.net.URI
import java.time.OffsetDateTime
import androidx.annotation.StringRes
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource

/** Timeline lines shown before "Show all", related entries of one kind before "All" (as iOS and the old app). */
private const val LOG_SHOWN = 6
private const val LINKS_SHOWN = 4

@Composable
internal fun KnowledgeScreen(model: KnowledgeViewModel, navigator: Navigator) {
    val page by model.page.collectAsStateWithLifecycle()
    // The list is the current assistant's (a model per assistant): an entry opens as its.
    val owner = persona.core.designsystem.currentAssistant().id
    var tagMenu by remember { mutableStateOf(false) }
    var ignored by rememberSaveable(page.scope?.path) { mutableStateOf(false) }
    val search = page.filter.query.isNotBlank()
    val shown = if (search) page.groups.isNotEmpty() else page.entries.isNotEmpty()
    // A regular-width root draws the sidebar button (LocalIsSidebarRoot); elsewhere this Back stays (iOS sidebarRootPage).
    JListPage(stringResource(R.string.knowledge_title), onBack = if (LocalIsSidebarRoot.current) null else navigator::back, largeTitle = true,
        refreshing = page.loading && shown, onRefresh = model::refresh,
        actions = {
            JIconButton(PersonaIcons.knowledgeMap, stringResource(R.string.knowledge_map_open), { navigator.navigate(KnowledgeMapRoute(owner)) })
            if (!search) JIconButton(PhIcons.FunnelSimple, page.filter.tag?.let { stringResource(R.string.knowledge_filter_tag_current, it) }
                ?: stringResource(R.string.knowledge_filter_tag), { tagMenu = true },
                badge = page.filter.tag != null)
            JIconButton(PhIcons.ArrowClockwise, stringResource(R.string.knowledge_refresh), model::refresh, enabled = !page.loading)
        }) {
        item("search") {
            JSearchField(page.filter.query, model::query, stringResource(R.string.knowledge_search), Modifier.padding(vertical = Tokens.Space.s))
        }
        // Search covers every kind: the chips (one of them lit) would say otherwise.
        if (!search) item("kinds") {
            KindChips(page.filter.kind, page.entries.size.takeIf { page.loaded }, model::kind)
            page.filter.tag?.let { JTag(stringResource(R.string.knowledge_tag_chip, it), Modifier.padding(top = Tokens.Space.s)) }
        }
        // A reload that failed over what is still shown says so above it (the whole-page error is for an empty page, #3991).
        if (page.paired && shown) page.message?.let { message -> item("reload-error") { JNotice(stringResource(message), Modifier.padding(vertical = Tokens.Space.s), status = Status.NeedsYou) } }
        when {
            !page.paired -> item("unpaired") {
                JEmptyState(stringResource(R.string.knowledge_unpaired_title), stringResource(R.string.knowledge_unpaired_text))
            }
            search -> searchResults(page, navigator, owner)
            else -> listContent(page, ignored, { ignored = !ignored }, navigator, model::refresh, owner)
        }
    }
    if (tagMenu) JActionSheet(listOf(JAction(stringResource(R.string.knowledge_all), checked = page.filter.tag == null) { model.tag(null) }) +
        page.tags.map { tag -> JAction("#$tag", checked = page.filter.tag == tag) { model.tag(tag) } },
        { tagMenu = false }, stringResource(R.string.knowledge_filter_tag))
}

/** The six kinds as chips; the chosen one says how many it holds once loaded. */
@Composable
private fun KindChips(kind: KnowledgeKind, count: Int?, onPick: (KnowledgeKind) -> Unit) {
    Row(
        Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(vertical = Tokens.Space.xs),
        horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s),
    ) {
        KnowledgeKind.entries.forEach { option ->
            val on = option == kind
            JChip(option.label(), selected = on, onClick = { onPick(option) }, count = if (on) count else null)
        }
    }
}

private fun LazyListScope.listContent(page: KnowledgePage, showIgnored: Boolean, toggleIgnored: () -> Unit, navigator: Navigator, retry: () -> Unit,
    owner: String) {
    val kind = page.filter.kind
    when {
        page.entries.isEmpty() && page.message != null -> item("error") { KnowledgeFailure(R.string.knowledge_error_title, requireNotNull(page.message), retry) }
        page.entries.isEmpty() && !page.loaded -> item("loading") { JLoading() }
        page.entries.isEmpty() -> item("empty") {
            JEmptyState(page.filter.tag?.let { stringResource(kind.words.emptyTag, it) } ?: stringResource(kind.words.empty),
                stringResource(R.string.knowledge_empty_text), icon = kind.words.icon)
        }
        else -> {
            val sections = if (kind == KnowledgeKind.Projects) projectSections(page.entries) else kindSections(page.entries, page.filter.tag)
            sections.forEach { section ->
                item(key = "section/${section.key}", contentType = "section") {
                    JSectionHeader(section.tag?.let { "#$it" } ?: stringResource(section.title), count = section.entries.size,
                        onClick = if (section.collapsed) toggleIgnored else null, open = showIgnored, trailingIsFoldAction = true,
                        trailing = if (section.collapsed) stringResource(if (showIgnored) R.string.knowledge_collapse else R.string.knowledge_expand) else null)
                }
                if (!section.collapsed || showIgnored) items(section.entries, key = { "entry/${it.key}" }, contentType = { "entry" }) {
                    KnowledgeRow(it, onClick = { navigator.open(it, owner) })
                }
                section.note?.let { note -> item(key = "note/${section.key}", contentType = "note") { Note(stringResource(note)) } }
            }
        }
    }
}

private fun LazyListScope.searchResults(page: KnowledgePage, navigator: Navigator, owner: String) {
    val query = page.filter.query.trim()
    when {
        page.groups.isEmpty() && page.message != null -> item("error") { KnowledgeFailure(R.string.knowledge_search_error, requireNotNull(page.message), null) }
        page.groups.isEmpty() && !page.loaded -> item("searching") { JLoading() }
        page.groups.isEmpty() -> item("nothing") {
            JEmptyState(stringResource(R.string.knowledge_nothing_found), stringResource(R.string.knowledge_nothing_found_text, query), icon = PhIcons.MagnifyingGlass)
        }
        else -> searchSections(page.groups).forEach { (kind, groups) ->
            item(key = "hits/${kind.wire}", contentType = "section") { JSectionHeader(kind.label(), count = groups.size) }
            items(groups, key = { "hit/${it.key}" }, contentType = { "hit" }) { group ->
                KnowledgeRow(group.entry, metadata = group.named, name = if (group.named) group.entry.name else kind.label(), onClick = { navigator.open(group.entry, owner) }, trailing = {
                    if (group.hits.size > 2) Text(pluralStringResource(R.plurals.knowledge_hits, group.hits.size, group.hits.size), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1)
                }) {
                    group.hits.take(2).forEach { hit ->
                        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                            Text(hit.source.label(), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                            Text(marked(hit.text, query), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink2,
                                maxLines = 2, overflow = TextOverflow.Ellipsis)
                        }
                    }
                }
            }
        }
    }
}

/** [text] with every word of [query] set in the page's strong ink. */
@Composable
private fun marked(text: String, query: String): AnnotatedString {
    val strong = SpanStyle(color = PersonaTheme.palette.ink, fontWeight = FontWeight.SemiBold)
    return remember(text, query, strong) {
        val ranges = queryRanges(text, query)
        buildAnnotatedString {
            var at = 0
            ranges.forEach { range ->
                append(text, at, range.first)
                withStyle(strong) { append(text, range.first, range.last + 1) }
                at = range.last + 1
            }
            append(text, at, text.length)
        }
    }
}

@Composable
private fun KnowledgeRow(
    entry: KnowledgeEntry,
    onClick: () -> Unit,
    metadata: Boolean = true,
    name: String = entry.name,
    trailing: @Composable () -> Unit = {},
    extra: @Composable ColumnScope.() -> Unit = {},
) {
    JCard(Modifier.padding(bottom = Tokens.Space.m), onClick = onClick) {
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
            Row(verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                Text(name, style = PersonaTheme.type.title, color = PersonaTheme.palette.ink, maxLines = 2, overflow = TextOverflow.Ellipsis,
                    modifier = Modifier.weight(1f))
                trailing()
            }
            if (metadata) EntryMetadata(entry)
            entry.oneLine?.takeIf { it.isNotBlank() && metadata }?.let {
                Text(it, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink2, maxLines = 3, overflow = TextOverflow.Ellipsis)
            }
            extra()
        }
    }
}

/** Importance in its tone (P0 needs you, P1 is busy), the project's kind and status, relevance, the first tags. */
@OptIn(ExperimentalLayoutApi::class)
@Composable
private fun EntryMetadata(entry: KnowledgeEntry, allTags: Boolean = false) {
    FlowRow(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s - Tokens.Space.xxs), verticalArrangement = Arrangement.spacedBy(Tokens.Space.s - Tokens.Space.xxs)) {
        entry.importance?.takeIf { it.isNotBlank() }?.let { JTag(it, status = importanceStatus(it)) }
        if (entry.isProject) {
            JTag(stringResource(if (entry.projectKind == "long") R.string.knowledge_project_long else R.string.knowledge_project_short))
            entry.projectStatusLabel?.let { JTag(stringResource(it)) }
        }
        if (entry.isRecording) recordingDevice(entry.extra)?.let { JTag(it.label()) }
        if (entry.relevance > 0) JTag(stringResource(R.string.knowledge_relevance, entry.relevance.coerceIn(0, 100)), status = relevanceStatus(entry.relevance))
        entry.tags.orEmpty().let { if (allTags) it else it.take(3) }.forEach { JTag("#$it") }
    }
    val locale = currentLocale
    val updated = remember(entry.updated, locale) {
        runCatching { OffsetDateTime.parse(entry.updated).takeIf { it.year > 1970 }
            ?.let { LocalizedFormats.format(it.toInstant(), LocalizedFormats.DATE_TIME, locale) } }.getOrNull()
    }
    updated?.let { Text(stringResource(if (entry.isRecording) R.string.knowledge_recorded else R.string.knowledge_updated, it), style = PersonaTheme.type.micro, color = PersonaTheme.palette.ink3) }
}

/** P0 in the "Needs you" tone, P1 in the "Working" tone (iOS `ImportanceBadge`); P2 and P3 quiet. */
internal fun importanceStatus(importance: String?): Status? = when (importance) {
    "P0" -> Status.NeedsYou
    "P1" -> Status.Working
    else -> null
}

/** Relevance to me: high in the "Needs you" tone, middling in the "Working" tone, low quiet (iOS `RelevanceMeter`). */
private fun relevanceStatus(relevance: Int): Status? = when {
    relevance >= 70 -> Status.NeedsYou
    relevance >= 40 -> Status.Working
    else -> null
}

@Composable
internal fun KnowledgeDetailScreen(
    model: KnowledgeDetailViewModel,
    navigator: Navigator,
    openLinkInApp: (String) -> Unit,
    onNewAgent: ((KnowledgeAgentStart) -> Unit)? = null,
) {
    val page by model.page.collectAsStateWithLifecycle()
    val sending by model.sending.collectAsStateWithLifecycle()
    val tagging by model.tagging.collectAsStateWithLifecycle()
    val minutesWork by model.minutesWork.collectAsStateWithLifecycle()
    val listState = rememberLazyListState()
    val scrolls = rememberCoroutineScope()
    var allLog by rememberSaveable(page.scope?.path) { mutableStateOf(false) }
    var recent by rememberSaveable(page.scope?.path) { mutableStateOf(false) }
    var openLinks by rememberSaveable(page.scope?.path) { mutableStateOf(listOf<String>()) }
    var editing by rememberSaveable(page.scope?.path) { mutableStateOf(false) }
    var openPoint by remember(page.scope) { mutableStateOf<Pair<TopicPoint, List<String>>?>(null) }
    val defaultQuestion = stringResource(R.string.knowledge_question_default)
    var question by rememberSaveable(page.scope?.path) { mutableStateOf(defaultQuestion) }
    var linkMessage by remember(page.scope) { mutableStateOf<String?>(null) }
    val currentOpenLink by rememberUpdatedState(openLinkInApp)
    val context = LocalContext.current
    val resources = LocalResources.current
    val uriHandler = remember(page.scope) {
        object : UriHandler {
            override fun openUri(uri: String) {
                val parsed = runCatching { URI(uri) }.getOrNull()
                if (parsed != null && parsed.scheme?.lowercase() in setOf("http", "https") && !parsed.host.isNullOrBlank() && parsed.userInfo == null) {
                    currentOpenLink(uri)
                } else linkMessage = resources.getString(R.string.knowledge_link_unsupported)
            }
        }
    }
    val lifecycle = LocalLifecycleOwner.current.lifecycle
    LaunchedEffect(model, lifecycle, navigator) {
        lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            // The Persona page shows the current assistant's main conversation; another assistant's opens by itself.
            model.handoffs.collect { handoff ->
                val main = handoff.main
                if (main == null) navigator.selectTab(AppTab.Persona)
                else navigator.navigate(ChatRoute(main.machineId, main.sessionId, ConversationKind.Assistant))
            }
        }
    }
    LaunchedEffect(tagging.saved) { if (tagging.saved > 0) editing = false }
    // While the hub writes a recording's minutes, the page reads it again every few seconds (as the web's, every 5 s).
    val minutesPending = page.detail?.meta.string("minutes_state") == "pending"
    LaunchedEffect(minutesPending, lifecycle) {
        if (minutesPending) lifecycle.repeatOnLifecycle(Lifecycle.State.STARTED) {
            while (true) { delay(MinutesPollMillis); model.refresh() }
        }
    }
    val minutesCopied = stringResource(R.string.knowledge_minutes_copied)
    val copyText = rememberCopy()
    val detail = page.detail
    val entry = detail?.entry()
    val kindName = KnowledgeKind.fromWire(detail?.kind.orEmpty())?.label().orEmpty()
    fun startAgent(detail: KnowledgeDetail) {
        val start = agentStart(detail, kindName)
        if (onNewAgent != null) { onNewAgent(start); return }
        // Until the shell can open "New session" with these filled in: the context goes on the clipboard and Agents opens.
        copyText(start.prompt, start.cwd?.let { resources.getString(R.string.knowledge_context_copied_dir, pathTail(it)) }
            ?: resources.getString(R.string.knowledge_context_copied))
        navigator.selectTab(AppTab.Agents)
    }
    CompositionLocalProvider(LocalUriHandler provides uriHandler) {
        JListPage(entry?.name ?: stringResource(R.string.knowledge_detail_title), onBack = navigator::back,
            refreshing = page.loading && detail != null, onRefresh = model::refresh, state = listState,
            actions = {
                JIconButton(PhIcons.Hash, stringResource(R.string.knowledge_tag_action), { model.dismissTagError(); editing = true }, enabled = detail != null)
                JIconButton(PhIcons.ArrowClockwise, stringResource(R.string.knowledge_refresh_detail), model::refresh, enabled = !page.loading)
            }) {
            when {
                !page.paired -> item("unpaired") {
                    JEmptyState(stringResource(R.string.knowledge_unpaired_title), stringResource(R.string.knowledge_unpaired_detail))
                }
                detail == null && page.message != null -> item("error") {
                    KnowledgeFailure(R.string.knowledge_entry_error, requireNotNull(page.message), model::refresh)
                }
                detail == null || entry == null -> item("loading") { JLoading() }
                else -> {
                    // A reload that failed keeps the entry; this says it could not be read again (#3991).
                    page.message?.let { message -> item("reload-error") { JNotice(stringResource(message), Modifier.padding(vertical = Tokens.Space.s), status = Status.NeedsYou) } }
                    item("header", contentType = "header") {
                        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                            EntryMetadata(entry, allTags = true)
                            entry.oneLine?.takeIf { it.isNotBlank() }?.let { Text(it, style = PersonaTheme.type.body, color = PersonaTheme.palette.ink) }
                            // A field of the page: the chat's composer frame brings its own inset (and the board's rule
                            // across the page), so it stood out of line with the tags and the buttons.
                            JTextField(question, { question = it }, placeholder = stringResource(R.string.knowledge_ask_placeholder), enabled = !sending.busy)
                            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
                                JButton(stringResource(R.string.knowledge_ask), { model.ask(question, kindName) }, Modifier.weight(1f), icon = PhIcons.Sparkle,
                                    enabled = question.isNotBlank(), loading = sending.busy, loadingText = stringResource(R.string.knowledge_asking))
                                if (entry.isProject) JButton(stringResource(R.string.knowledge_start_agent), { startAgent(detail) }, Modifier.weight(1f),
                                    kind = JButtonKind.Secondary, icon = PhIcons.Robot)
                            }
                            sending.message?.let { JNotice(stringResource(it)) }
                            linkMessage?.let { JNotice(it) }
                        }
                    }
                    agents(page.agents, navigator)
                    if (detail.kind == KnowledgeKind.Topics.wire) topicPoints(detail, entry.name) { point, crumbs -> openPoint = point to crumbs }
                    val recording = entry.isRecording
                    val facts = if (recording) RecordingFacts.of(detail.meta) else null
                    facts?.let { recordingFacts(it) }
                    timeline(timeline(detail.log), allLog) { allLog = !allLog }
                    val profileText = profile(detail.summary, entry.oneLine)
                    // A card like the other sections (and iOS): bare text sat at the card edge, left of every heading.
                    if (profileText.isNotBlank()) item("profile", contentType = "markdown") {
                        JSectionHeader(stringResource(if (recording) R.string.knowledge_recording_summary else R.string.knowledge_profile))
                        JGroupItem(GroupPosition.Single) {
                            Box(Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m)) { KnowledgeMarkdown(profileText) }
                        }
                    }
                    facts?.let { recordingBookmarks(it) }
                    links(linkGroups(detail.links), openLinks, { kind ->
                        openLinks = if (kind in openLinks) openLinks - kind else openLinks + kind
                    }, navigator, model.owner)
                    dirs(detail.meta.strings("dirs"))
                    // A recording's minutes (#2738), right above its transcript as on iOS; a time scrolls to its line.
                    val lines = transcript(detail.segments)
                    facts?.let { recorded -> minutesView(detail, recorded)?.let { view ->
                        recordingMinutes(view, MinutesActions(
                            busy = minutesWork.busy || page.loading, message = minutesWork.message,
                            make = model::makeMinutes, add = model::addMinutesTodos,
                            copy = { markdown -> copyText(markdown, minutesCopied) },
                            jump = { at -> segmentAt(lines, at)?.let { index -> scrolls.launch { listState.scrollToKey(segmentKey(index)) } } },
                            dismiss = model::dismissMinutesMessage,
                            share = { markdown ->
                                val send = Intent(Intent.ACTION_SEND).setType("text/plain").putExtra(Intent.EXTRA_TEXT, markdown)
                                    .putExtra(Intent.EXTRA_SUBJECT, entry?.name.orEmpty())
                                runCatching { context.startActivity(Intent.createChooser(send, null)) }
                            },
                        )) { at -> minutesClock(recorded, at) }
                    } }
                    // A recording's `recent` is its transcript as text: the transcript below shows it line by line.
                    facts?.let { recordingTranscript(it, lines) }
                    if (!recording && !detail.recent.isNullOrBlank()) {
                        item("recent-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_chat_history)) }
                        item("recent-toggle", contentType = "row") {
                            JGroupItem(GroupPosition(first = true, last = !recent)) {
                                JRow(stringResource(R.string.knowledge_recent_messages), leadingIcon = PhIcons.ChatCircleDots, onClick = { recent = !recent }, chevron = false,
                                    trailing = { JIcon(if (recent) PhIcons.CaretUp else PhIcons.CaretDown, null, tint = PersonaTheme.palette.ink3) })
                            }
                        }
                        if (recent) item("recent", contentType = "text") {
                            JGroupItem(GroupPosition(first = false, last = true)) {
                                SelectionContainer {
                                    Text(detail.recent.orEmpty(), style = PersonaTheme.type.callout, color = PersonaTheme.palette.ink,
                                        modifier = Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
                                }
                            }
                        }
                    }
                    item("path", contentType = "text") {
                        JSectionHeader(stringResource(R.string.knowledge_directory))
                        JGroupItem(GroupPosition.Single) {
                            SelectionContainer {
                                Text(detail.path, style = PersonaTheme.type.code, color = PersonaTheme.palette.ink2,
                                    modifier = Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.m))
                            }
                        }
                        Note(stringResource(R.string.knowledge_directory_note))
                    }
                }
            }
        }
    }
    openPoint?.let { (point, crumbs) ->
        PointSheet(point, crumbs, detail?.links.orEmpty(), { source -> openPoint = null; navigator.open(source, model.owner) }, { openPoint = null })
    }
    if (editing && entry != null) KnowledgeTagEditor(entry, tagging, onSave = model::saveTags, onDismiss = { editing = false })
}

/** "Agents on this project": the hub's live sessions in the project's code directories; a tap opens the conversation. */
private fun LazyListScope.agents(agents: List<Session>, navigator: Navigator) {
    if (agents.isEmpty()) return
    item("agents-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_agents), count = agents.size) }
    itemsIndexed(agents, key = { _, session -> "agent/${session.key.path}" }, contentType = { _, _ -> "agent" }) { index, session ->
        val status = session.agentState.status()
        AgentRow(session.displayTitle, status, onClick = { navigator.navigate(ChatRoute(session.machineId, session.sessionId, ConversationKind.Agent)) },
            position = GroupPosition.of(index, agents.size), preview = session.workLine(java.time.Instant.now())?.text() ?: session.lastText, statusText = status.localizedLabel,
            agent = session.agentLabel.takeIf { session.agent in setOf("codex", "traex") },
            facts = listOf(RowFact(listOf(session.machineId, session.folderName).filter { it.isNotBlank() }.joinToString(" / "))))
    }
}

private fun AgentState.status(): Status = when (this) {
    AgentState.NeedsYou -> Status.NeedsYou
    AgentState.Unread -> Status.Unread
    AgentState.Working -> Status.Working
    AgentState.Idle -> Status.Idle
    AgentState.Unknown -> Status.Unknown
    AgentState.Offline -> Status.Offline
}

/** Newest first; the newest in the accent; "Show all" past the first few. */
private fun LazyListScope.timeline(lines: List<KnowledgeLogLine>, all: Boolean, toggle: () -> Unit) {
    if (lines.isEmpty()) return
    val more = lines.size > LOG_SHOWN
    val shown = if (all) lines else lines.take(LOG_SHOWN)
    val count = shown.size + if (more) 1 else 0
    item("timeline-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_timeline), count = lines.size) }
    itemsIndexed(shown, key = { index, _ -> "log/$index" }, contentType = { _, _ -> "log" }) { index, line ->
        JGroupItem(GroupPosition.of(index, count)) { TimelineLine(line, newest = index == 0) }
    }
    if (more) item("timeline-more", contentType = "row") {
        JGroupItem(GroupPosition(first = false, last = true)) {
            JRow(if (all) stringResource(R.string.knowledge_collapse)
                else pluralStringResource(R.plurals.knowledge_show_all, lines.size, lines.size), onClick = toggle, chevron = false)
        }
    }
}

@Composable
private fun TimelineLine(line: KnowledgeLogLine, newest: Boolean) {
    val palette = PersonaTheme.palette
    JRow {
        JStatusDot(if (newest) Status.Working else Status.Idle, Modifier.align(Alignment.Top).padding(top = Tokens.Space.s))
        Spacer(Modifier.width(Tokens.Space.m))
        Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            if (line.whenText.isNotEmpty() || line.source.isNotEmpty()) Text(
                buildAnnotatedString {
                    if (line.whenText.isNotEmpty()) withStyle(SpanStyle(color = if (newest) palette.accent else palette.ink2, fontWeight = FontWeight.SemiBold)) {
                        append(line.shortWhen())
                    }
                    if (line.whenText.isNotEmpty() && line.source.isNotEmpty()) append("  ")
                    if (line.source.isNotEmpty()) withStyle(SpanStyle(color = palette.ink3)) { append(line.source) }
                },
                style = PersonaTheme.type.caption, maxLines = 1, overflow = TextOverflow.Ellipsis,
            )
            Text(line.text, style = PersonaTheme.type.body, color = palette.ink)
        }
    }
}

/** "Recording": when it started, how long, on which device, in which language; "Transcribing" until the transcript is done. */
private fun LazyListScope.recordingFacts(facts: RecordingFacts) {
    item("recording-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_recording_info)) }
    item("recording-facts", contentType = "facts") {
        val locale = currentLocale
        val started = remember(facts.startedAt, locale) { facts.startedAt?.let { LocalizedFormats.format(it, LocalizedFormats.DATE_TIME, locale) } }
        val language = remember(facts.language, locale) { languageName(facts.language, locale) }
        val rows = buildList<Pair<String, String>> {
            started?.let { add(stringResource(R.string.knowledge_recording_started) to it) }
            facts.length?.let { add(stringResource(R.string.knowledge_recording_duration) to it.label()) }
            recordingDevice(facts.device)?.let { add(stringResource(R.string.knowledge_recording_device) to it.label()) }
            language?.let { add(stringResource(R.string.knowledge_recording_language) to it) }
        }
        val count = rows.size + if (facts.complete) 0 else 1
        Column {
            rows.forEachIndexed { index, (title, value) ->
                JGroupItem(GroupPosition.of(index, count)) { JRow(title, value = value) }
            }
            if (!facts.complete) JGroupItem(GroupPosition.of(rows.size, count)) {
                JRow(stringResource(R.string.knowledge_recording_transcribing), subtitle = stringResource(R.string.knowledge_recording_transcribing_note),
                    leading = { JStatusDot(Status.Working) })
            }
        }
    }
}

/** "Bookmarks": the marks set while recording, at the time they were set. */
private fun LazyListScope.recordingBookmarks(facts: RecordingFacts) {
    if (facts.bookmarks.isEmpty()) return
    item("bookmarks-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_recording_bookmarks), count = facts.bookmarks.size) }
    itemsIndexed(facts.bookmarks, key = { index, _ -> "bookmark/$index" }, contentType = { _, _ -> "bookmark" }) { index, mark ->
        JGroupItem(GroupPosition.of(index, facts.bookmarks.size)) {
            JRow(mark.label ?: stringResource(R.string.knowledge_recording_bookmark), leadingIcon = PhIcons.BookmarkSimple,
                value = clockText(facts, mark.at))
        }
    }
}

/** The whole transcript, a row per line with its wall-clock time; each line's text can be selected and copied. */
private fun LazyListScope.recordingTranscript(facts: RecordingFacts, segments: List<KnowledgeSegment>) {
    item("transcript-header", contentType = "section") {
        JSectionHeader(stringResource(R.string.knowledge_recording_transcript), count = segments.size.takeIf { it > 0 })
    }
    if (segments.isEmpty()) {
        item("transcript-empty", contentType = "row") {
            JGroupItem(GroupPosition.Single) { JRow(stringResource(R.string.knowledge_recording_no_transcript), leadingIcon = PhIcons.Microphone) }
        }
        return
    }
    itemsIndexed(segments, key = { index, _ -> segmentKey(index) }, contentType = { _, _ -> "segment" }) { index, segment ->
        JGroupItem(GroupPosition.of(index, segments.size)) {
            JRow {
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
                    clockText(facts, segment.start)?.let {
                        Text(it, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3, maxLines = 1)
                    }
                    SelectionContainer { Text(segment.text.trim(), style = PersonaTheme.type.body, color = PersonaTheme.palette.ink) }
                }
            }
        }
    }
}

/** [offset] seconds into the recording as the phone's clock reads it, with seconds (14:03:27). */
@Composable
private fun clockText(facts: RecordingFacts, offset: Double): String? {
    val locale = currentLocale
    return remember(facts.startedAt, offset, locale) {
        facts.clock(offset)?.let { LocalizedFormats.format(it, LocalizedFormats.TIME_SECONDS, locale) }
    }
}

/** A BCP 47 tag (zh-CN) in the page's language (Chinese (China)); the tag itself when the phone has no name for it. */
private fun languageName(tag: String, locale: java.util.Locale): String? {
    val trimmed = tag.trim().takeIf { it.isNotEmpty() } ?: return null
    return java.util.Locale.forLanguageTag(trimmed).getDisplayName(locale).takeIf { it.isNotBlank() } ?: trimmed
}

@Composable
private fun RecordingDevice.label(): String = stringResource(when (this) {
    RecordingDevice.Phone -> R.string.knowledge_recording_phone
    RecordingDevice.Watch -> R.string.knowledge_recording_watch
})

@Composable
private fun RecordingLength.label(): String = when (this) {
    is RecordingLength.Hours -> stringResource(R.string.knowledge_duration_hours, hours.toInt(), minutes.toInt())
    is RecordingLength.Minutes -> stringResource(R.string.knowledge_duration_minutes, minutes.toInt(), seconds.toInt())
    is RecordingLength.Seconds -> stringResource(R.string.knowledge_duration_seconds, seconds.toInt())
}

/** Related entries by kind, a few each with "All" for the rest. */
private fun LazyListScope.links(groups: List<Pair<KnowledgeKind, List<KnowledgeEntry>>>, open: List<String>, toggle: (String) -> Unit, navigator: Navigator,
    owner: String?) {
    groups.forEach { (kind, entries) ->
        val all = kind.wire in open
        val more = entries.size > LINKS_SHOWN
        val shown = if (all) entries else entries.take(LINKS_SHOWN)
        val count = shown.size + if (more) 1 else 0
        item("links-header/${kind.wire}", contentType = "section") { JSectionHeader(stringResource(kind.words.linked), count = entries.size) }
        itemsIndexed(shown, key = { _, linked -> "link/${kind.wire}/${linked.key}" }, contentType = { _, _ -> "link" }) { index, linked ->
            JGroupItem(GroupPosition.of(index, count)) {
                JRow(linked.name, subtitle = linked.oneLine?.takeIf { it.isNotBlank() }, subtitleLines = 1, onClick = { navigator.open(linked, owner) })
            }
        }
        if (more) item("links-more/${kind.wire}", contentType = "row") {
            JGroupItem(GroupPosition(first = false, last = true)) {
                JRow(if (all) stringResource(R.string.knowledge_collapse)
                else pluralStringResource(R.plurals.knowledge_all_count, entries.size, entries.size), onClick = { toggle(kind.wire) }, chevron = false)
            }
        }
    }
}

/** A project's code directories: the path's last folders, and the machine they are on. */
private fun LazyListScope.dirs(dirs: List<String>) {
    if (dirs.isEmpty()) return
    item("dirs-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_code_dirs), count = dirs.size) }
    itemsIndexed(dirs, key = { index, _ -> "dir/$index" }, contentType = { _, _ -> "dir" }) { index, dir ->
        val (machine, path) = dirParts(dir)
        JGroupItem(GroupPosition.of(index, dirs.size)) { JRow(path, subtitle = machine, leadingIcon = PhIcons.FolderSimple) }
    }
}

@Composable
internal fun Note(text: String) {
    Text(text, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink3,
        modifier = Modifier.padding(horizontal = rowPadding).padding(top = Tokens.Space.s, bottom = Tokens.Space.xs))
}

@Composable
private fun KnowledgeMarkdown(text: String) {
    val parsed by produceState<MarkdownState?>(null, text) {
        value = withContext(Dispatchers.Default) { parseMarkdown(MathMarkdown.rewrite(text)) }
    }
    val palette = PersonaTheme.palette
    val type = PersonaTheme.type
    val body = type.body
    val formulas = rememberMathImages()
    SelectionContainer {
        // The conversation's look (ChatMarkdown, FileMarkdown): the library's own Material styles drew small bullets
        // sitting above the line and headings bigger than the page title.
        parsed?.let {
            Markdown(it, modifier = Modifier.fillMaxWidth(), imageTransformer = formulas,
                colors = markdownColor(text = palette.ink, codeBackground = palette.sunken, inlineCodeBackground = palette.sunken,
                    dividerColor = palette.line, tableBackground = palette.surface, darkTheme = palette.dark,
                    highlightText = palette.ink, highlightBackground = palette.marker),
                typography = markdownTypography(h1 = type.largeTitle, h2 = type.title, h3 = type.headline,
                    h4 = type.bodyStrong, h5 = type.callout, h6 = type.caption,
                    text = body, paragraph = body, code = type.code, inlineCode = type.code,
                    quote = type.callout.copy(color = palette.ink2), ordered = body,
                    bullet = body, list = body, table = body, alertTitle = type.bodyStrong,
                    textLink = TextLinkStyles(SpanStyle(color = palette.accent, textDecoration = TextDecoration.Underline))),
                components = markdownComponents(
                    orderedList = { model -> MarkdownOrderedList(model.content, model.node, depth = model.listDepth,
                        markerModifier = { firstBaselineAligned().padding(end = Tokens.Space.s) },
                        listModifier = { firstBaselineAligned(Modifier.weight(1f)) }) },
                    unorderedList = { model -> MarkdownBulletList(model.content, model.node, depth = model.listDepth,
                        markerModifier = { firstBaselineAligned().padding(end = Tokens.Space.s) },
                        listModifier = { firstBaselineAligned(Modifier.weight(1f)) }) },
                    codeFence = { model ->
                        MarkdownFence(model.content.substring(model.node.startOffset, model.node.endOffset)) { CurrentComponentsBridge.codeFence(model) }
                    },
                ))
        } ?: Text(text, style = body, color = palette.ink)
    }
}

/** [entry]'s page, kept [manager]'s (the assistant whose knowledge base listed it, or the entry's page it was linked from). */
internal fun Navigator.open(entry: KnowledgeEntry, manager: String?) {
    KnowledgeKind.fromWire(entry.kind)?.let { navigate(KnowledgeDetailRoute(it, entry.id, manager)) }
}

@Composable
internal fun KnowledgeFailure(title: Int, message: Int, retry: (() -> Unit)?) {
    val disabled = message == R.string.knowledge_disabled_text
    RetryState(stringResource(if (disabled) R.string.knowledge_disabled_title else title),
        stringResource(message), retry.takeUnless { disabled })
}

@Composable internal fun RetryState(title: String, message: String, retry: (() -> Unit)?) {
    JEmptyState(title, message, action = retry?.let { { JButton(stringResource(R.string.knowledge_retry), it, kind = JButtonKind.Secondary) } })
}

/** A kind's words: its name, and the empty list and the related entries that say it. */
private class KindWords(@StringRes val name: Int, @StringRes val empty: Int, @StringRes val emptyTag: Int, @StringRes val linked: Int,
    val icon: PhIcon? = null)

private val KnowledgeKind.words: KindWords
    get() = when (this) {
        KnowledgeKind.Projects -> KindWords(R.string.knowledge_kind_projects, R.string.knowledge_empty_projects,
            R.string.knowledge_empty_tag_projects, R.string.knowledge_linked_projects)
        KnowledgeKind.Topics -> KindWords(R.string.knowledge_kind_topics, R.string.knowledge_empty_topics,
            R.string.knowledge_empty_tag_topics, R.string.knowledge_linked_topics, icon = PersonaIcons.topic)
        KnowledgeKind.Chats -> KindWords(R.string.knowledge_kind_chats, R.string.knowledge_empty_chats,
            R.string.knowledge_empty_tag_chats, R.string.knowledge_linked_chats)
        KnowledgeKind.People -> KindWords(R.string.knowledge_kind_people, R.string.knowledge_empty_people,
            R.string.knowledge_empty_tag_people, R.string.knowledge_linked_people)
        KnowledgeKind.Docs -> KindWords(R.string.knowledge_kind_docs, R.string.knowledge_empty_docs,
            R.string.knowledge_empty_tag_docs, R.string.knowledge_linked_docs)
        KnowledgeKind.Meetings -> KindWords(R.string.knowledge_kind_meetings, R.string.knowledge_empty_meetings,
            R.string.knowledge_empty_tag_meetings, R.string.knowledge_linked_meetings)
        KnowledgeKind.Events -> KindWords(R.string.knowledge_kind_events, R.string.knowledge_empty_events,
            R.string.knowledge_empty_tag_events, R.string.knowledge_linked_events)
        KnowledgeKind.Recordings -> KindWords(R.string.knowledge_kind_recordings, R.string.knowledge_empty_recordings,
            R.string.knowledge_empty_tag_recordings, R.string.knowledge_linked_recordings, icon = PhIcons.Microphone)
    }

/** The kind's name in the page's language ("Projects" in English, the Chinese or Japanese word otherwise). */
@Composable
internal fun KnowledgeKind.label(): String = stringResource(words.name)

@Composable
private fun KnowledgeHitSource.label(): String = when (this) {
    is KnowledgeHitSource.Messages -> stringResource(R.string.knowledge_source_messages, day)
    KnowledgeHitSource.Profile -> stringResource(R.string.knowledge_profile)
    KnowledgeHitSource.Timeline -> stringResource(R.string.knowledge_timeline)
    KnowledgeHitSource.Details -> stringResource(R.string.knowledge_source_info)
    KnowledgeHitSource.Transcript -> stringResource(R.string.knowledge_recording_transcript)
    is KnowledgeHitSource.File -> name
}

/** How often a recording's page reads it again while its minutes are being written. */
internal const val MinutesPollMillis = 5_000L
