package persona.feature.assistant

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.imePadding
import androidx.compose.foundation.layout.navigationBarsPadding
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.size
import androidx.compose.foundation.layout.statusBarsPadding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.focus.FocusRequester
import androidx.compose.ui.focus.focusRequester
import androidx.compose.ui.platform.LocalSoftwareKeyboardController
import androidx.compose.ui.platform.LocalWindowInfo
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import persona.core.contracts.ExternalDirection
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AssistantManagement
import persona.core.contracts.api.AssistantSearchAnswer
import persona.core.designsystem.JEmptyState
import persona.core.designsystem.JFullScreen
import persona.core.designsystem.JIcon
import persona.core.designsystem.JIconButton
import persona.core.designsystem.JRow
import persona.core.designsystem.JSearchField
import persona.core.designsystem.JSegment
import persona.core.designsystem.JSegmented
import persona.core.designsystem.JSectionHeader
import persona.core.designsystem.PersonaTheme
import persona.core.designsystem.Tokens
import persona.core.designsystem.rowPadding
import persona.core.designsystem.rowTitleStart
import persona.core.designsystem.icons.PersonaIcons
import persona.core.navigation.MessageJumps
import persona.core.domain.sort.AgentQuery
import persona.core.domain.sort.admits
import java.time.Instant

/**
 * Persona's chats for the full-window search, latest first. Archived chats stay out of local results, and a blank
 * query lists all non-archived chats; otherwise title, last message and a direction's name match (the same local
 * filter the sidebar uses).
 */
internal fun filterChatsForFullSearch(
    chats: List<Session>,
    query: String,
    words: ConversationWords,
    names: Map<String, String> = emptyMap(),
    backgroundId: String? = null,
    activeTag: String? = null,
    mainLabel: Pair<SessionKey, String>? = null,
): List<Session> {
    val ordered = chats.distinctBy { it.key }
        .filter { !it.archived }
        .withConversationTag(activeTag)
        .map { it to it.updated }
        .sortedWith(compareByDescending<Pair<Session, Instant?>> { it.second }.thenBy { it.first.key.path })
        .map { it.first }
    val q = query.trim()
    if (q.isEmpty()) return ordered
    return ordered.filter {
        it.conversationTitle(words).contains(q, ignoreCase = true) ||
            it.lastText.orEmpty().contains(q, ignoreCase = true) ||
            names[it.sessionId].orEmpty().contains(q, ignoreCase = true) ||
            (mainLabel != null && it.key == mainLabel.first && mainLabel.second.contains(q, ignoreCase = true)) ||
            (it.sessionId == backgroundId && words.background.contains(q, ignoreCase = true))
    }
}

/**
 * Rows the full-window search lists: local matches at once (latest first), and when the hub lists assistant search the
 * manager's answer merges in the same way as the sidebar ([mergeAssistantSearch]).
 */
internal fun fullSearchRows(
    query: String,
    chats: List<Session>,
    known: List<Session>,
    enabled: Boolean,
    answer: AssistantSearchAnswer?,
    words: ConversationWords,
    names: Map<String, String> = emptyMap(),
    backgroundId: String? = null,
    activeTag: String? = null,
    mainLabel: Pair<SessionKey, String>? = null,
): List<FoundChat> {
    val local = filterChatsForFullSearch(chats, query, words, names, backgroundId, activeTag, mainLabel)
    if (!enabled || query.trim().isEmpty()) return local.map { FoundChat(it, null) }
    return mergeAssistantSearch(local, answer, known)
        .filter { activeTag.isNullOrEmpty() || it.session.tags?.contains(activeTag) == true }
}

/**
 * Search over the whole window (#9171). Merged adds an Agents scope using the Agents list's matching rules for
 * sessions already on the device. Persona Chats still use the manager's search when it is available.
 */
@Composable
internal fun AssistantFullSearch(
    chats: List<Session>,
    known: List<Session>,
    searchOn: Boolean,
    management: AssistantManagement?,
    hubKey: String,
    words: ConversationWords,
    names: Map<String, String> = emptyMap(),
    /** Presentation-only outside directions; they never enter the manager's conversation search. */
    externalDirections: Map<String, ExternalDirection> = emptyMap(),
    backgroundId: String? = null,
    activeTag: String? = null,
    mainKey: SessionKey? = null,
    onOpen: (SessionKey) -> Unit,
    onDismiss: () -> Unit,
    /** Seeded for JVM shots; production leaves it blank. */
    initialQuery: String = "",
    /** False draws the page in place (JVM shots); production keeps the full-screen dialog. */
    overlay: Boolean = true,
    agents: List<Session>? = null,
    agentMachines: Map<String, String> = emptyMap(),
    pinnedAgents: Set<String> = emptySet(),
    agentsFirst: Boolean = false,
    onOpenAgent: ((SessionKey) -> Unit)? = null,
) {
    var query by rememberSaveable { mutableStateOf(initialQuery) }
    var agentScope by remember(agentsFirst) { mutableStateOf(agentsFirst) }
    val searchingAgents = agents != null && agentScope
    val search = rememberAssistantSearch(query, searchOn && !searchingAgents, management, hubKey)
    val answer = search.current(query).takeUnless { searchingAgents }
    val agentRows = remember(searchingAgents, agents, query, agentMachines, pinnedAgents) {
        if (!searchingAgents) emptyList() else {
            val filter = AgentQuery.parse(query)
            val now = Instant.now()
            agents.orEmpty().asSequence().distinctBy { it.key }
                .filter { !it.isSubagent && !it.archived && filter.admits(it, it.key.path in pinnedAgents, agentMachines[it.machineId], now) }
                .map { it to it.updated }
                .sortedByDescending { it.second }
                .take(50)
                .map { it.first }
                .toList()
        }
    }
    val partial = answer?.partial == true
    val mainTitle = stringResource(R.string.assistant_main_conversation)
    val mainLabel = mainKey?.let { it to mainTitle }
    val rows = remember(query, chats, known, searchOn, answer, words, names, backgroundId, activeTag, mainLabel) {
        fullSearchRows(query, chats, known, searchOn, answer, words, names, backgroundId, activeTag, mainLabel)
    }
    val focus = remember { FocusRequester() }
    val keyboard = LocalSoftwareKeyboardController.current
    val windowFocused = LocalWindowInfo.current.isWindowFocused
    var focusedOnce by remember { mutableStateOf(false) }
    LaunchedEffect(windowFocused) {
        if (focusedOnce || !windowFocused) return@LaunchedEffect
        focusedOnce = true
        withFrameNanos { }
        val _ = runCatching { focus.requestFocus() }
        keyboard?.show()
    }
    LaunchedEffect(Unit) {
        withFrameNanos { }
        if (!focusedOnce) {
            val _ = runCatching { focus.requestFocus() }
            keyboard?.show()
        }
    }
    val body: @Composable () -> Unit = {
        Column(
            Modifier
                .fillMaxSize()
                .background(PersonaTheme.palette.background)
                .then(if (overlay) Modifier.statusBarsPadding().imePadding() else Modifier)
                .testTag("full-search"),
        ) {
            if (agents != null) JSegmented(
                listOf(JSegment(stringResource(R.string.assistant_sidebar_search_chats)),
                    JSegment(stringResource(R.string.assistant_sidebar_search_agents))),
                if (searchingAgents) 1 else 0,
                { agentScope = it == 1 },
                modifier = Modifier.padding(horizontal = Tokens.Space.page, vertical = Tokens.Space.s),
            )
            val heading = if (query.isBlank()) {
                stringResource(R.string.assistant_recent)
            } else {
                pluralStringResource(R.plurals.assistant_full_search_chats, rows.size, rows.size)
            }
            LazyColumn(
                Modifier.weight(1f).fillMaxWidth().testTag("full-search-list"),
                contentPadding = PaddingValues(
                    start = Tokens.Space.page, end = Tokens.Space.page, bottom = Tokens.Space.m,
                ),
            ) {
                if (searchingAgents) {
                    item { JSectionHeader(stringResource(if (query.isBlank()) R.string.assistant_recent else R.string.assistant_sidebar_search_agents)) }
                    items(agentRows, key = { it.key.path }) { session ->
                        JRow(session.displayTitle, subtitle = agentMachines[session.machineId],
                            leadingIcon = PersonaIcons.agentChat, onClick = onOpenAgent?.let { open -> { open(session.key) } })
                    }
                    if (agentRows.isEmpty() && query.isNotBlank()) item {
                        JEmptyState(stringResource(R.string.assistant_sidebar_search_no_agents),
                            stringResource(R.string.assistant_sidebar_search_no_agents_detail), icon = PersonaIcons.search)
                    }
                } else {
                    item { JSectionHeader(heading) }
                    if (partial) item {
                        Text(
                            stringResource(R.string.assistant_search_partial),
                            Modifier.padding(horizontal = Tokens.Space.m, vertical = Tokens.Space.s),
                            style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2,
                        )
                    }
                    items(rows, key = { it.session.key.path }) { row ->
                        val isMain = row.session.key == mainKey
                        val external = externalDirections[row.session.sessionId]
                        val target = external?.target
                        val open: (() -> Unit)? = when {
                            external == null -> {{
                                row.hit?.let { MessageJumps.request(row.session.machineId, row.session.sessionId, it.itemId) }
                                onOpen(row.session.key)
                            }}
                            target != null && onOpenAgent != null -> {{ onOpenAgent(target) }}
                            else -> null
                        }
                        FullSearchRow(
                            row = row,
                            title = if (isMain) mainTitle else names[row.session.sessionId] ?: row.session.conversationTitle(words),
                            isMain = isMain,
                            onOpen = open,
                        )
                    }
                    if (rows.isEmpty() && query.isNotBlank()) item {
                        JEmptyState(
                            stringResource(R.string.assistant_full_search_no_results, query),
                            stringResource(R.string.assistant_no_match_detail),
                            icon = PersonaIcons.search,
                        )
                    }
                }
            }
            Row(
                Modifier
                    .fillMaxWidth()
                    .then(if (overlay) Modifier.navigationBarsPadding() else Modifier)
                    .padding(horizontal = Tokens.Space.l, vertical = Tokens.Space.s),
                verticalAlignment = Alignment.CenterVertically,
            ) {
                JSearchField(
                    query, { query = it }, stringResource(if (searchingAgents) R.string.assistant_sidebar_search_agent_field else R.string.assistant_search_chats),
                    Modifier.weight(1f).focusRequester(focus).testTag("full-search-field"),
                )
                Spacer(Modifier.width(Tokens.Space.m))
                JIconButton(
                    PersonaIcons.close, stringResource(persona.core.designsystem.R.string.designsystem_close),
                    onDismiss, raised = true, modifier = Modifier.testTag("full-search-close"),
                )
            }
        }
    }
    if (overlay) JFullScreen(onDismiss, body) else body()
}

@Composable
private fun FullSearchRow(row: FoundChat, title: String, isMain: Boolean, onOpen: (() -> Unit)?) {
    val said = row.hit?.let(::markedSnippet)
    val preview = row.session.lastText?.replace("\n", " ")?.takeIf { it.isNotBlank() }
    androidx.compose.foundation.layout.Column {
        JRow(
            title,
            onClick = onOpen,
            subtitle = if (!isMain && said == null) preview else null,
            subtitleMark = said,
            chevron = false,
            leading = {
                Box(
                    Modifier
                        .size(Tokens.MinTouch.android)
                        .clip(RoundedCornerShape(Tokens.Radius.control))
                        .background(PersonaTheme.palette.surfaceStrong),
                    contentAlignment = Alignment.Center,
                ) {
                    JIcon(if (isMain) PersonaIcons.home else PersonaIcons.conversation, null,
                        size = Tokens.Size.iconXS, tint = PersonaTheme.palette.ink2)
                }
            },
        )
        ConversationTagLabels(row.session, Modifier.padding(start = rowTitleStart, end = rowPadding, bottom = Tokens.Space.xs))
    }
}
