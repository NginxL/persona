package persona.feature.knowledge

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawBehind
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalResources
import androidx.compose.ui.res.pluralStringResource
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.Role
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.text.style.TextOverflow
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import persona.core.navigation.Navigator

/**
 * The knowledge map on a phone (iOS `KBTopicsMap`, the web's Knowledge map): the same tree as an outline. Topics hold their
 * subtopics and points; projects are grouped by status and importance. A tap on a point opens it, on a topic or project opens
 * its page, anything else folds; the chevron always folds.
 */
@Composable
internal fun KnowledgeMapScreen(model: KnowledgeMapViewModel, navigator: Navigator, owner: String) {
    val page by model.page.collectAsStateWithLifecycle()
    val words = rememberMapWords()
    // What the reader folded or opened, per side: kept as the choice itself, so the default moving as details arrive leaves it be.
    var topicOpened by rememberSaveable { mutableStateOf(listOf<String>()) }
    var topicClosed by rememberSaveable { mutableStateOf(listOf<String>()) }
    var projectOpened by rememberSaveable { mutableStateOf(listOf<String>()) }
    var projectClosed by rememberSaveable { mutableStateOf(listOf<String>()) }
    var openPoint by remember(page.scope) { mutableStateOf<MapNode?>(null) }
    val side = page.side
    val tree by produceState<MapNode?>(null, page.entries, page.details, side, words) {
        value = withContext(Dispatchers.Default) {
            if (side == MapSide.Topics) KnowledgeMap.topics(page.entries.map { MapTopic(it, page.details[it.id]) }, words)
            else KnowledgeMap.projects(page.entries, page.details, words)
        }
    }
    // The tree answers the side it was built for; the other's rows never sit under this side's chip while it loads.
    val root = tree?.takeIf { page.loaded && page.paired && it.id == if (side == MapSide.Topics) "root:topics" else "root:projects" }
    val opened = if (side == MapSide.Topics) topicOpened else projectOpened
    val closed = if (side == MapSide.Topics) topicClosed else projectClosed
    val open = remember(root, opened, closed) { root?.let { KnowledgeMap.openNodes(it, opened, closed) }.orEmpty() }
    val rows = remember(root, open) { root?.let { KnowledgeMap.rows(it, open) }.orEmpty() }
    val keys = remember(rows) { uniqueKeys(rows.map { it.node.id }) }
    fun toggle(id: String) {
        val wasOpen = id in open
        val nowOpened = if (wasOpen) opened - id else opened + id
        val nowClosed = if (wasOpen) closed + id else closed - id
        if (side == MapSide.Topics) { topicOpened = nowOpened; topicClosed = nowClosed }
        else { projectOpened = nowOpened; projectClosed = nowClosed }
    }
    val fresh = page.loading && root != null
    JListPage(stringResource(R.string.knowledge_map_title), onBack = navigator::back, largeTitle = false,
        refreshing = fresh, onRefresh = model::refresh,
        actions = { JIconButton(PersonaIcons.retry, stringResource(R.string.knowledge_refresh), model::refresh, enabled = !page.loading) }) {
        item("sides") {
            Row(Modifier.fillMaxWidth().horizontalScroll(rememberScrollState()).padding(vertical = Tokens.Space.xs),
                horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s)) {
                JChip(stringResource(R.string.knowledge_kind_topics), selected = side == MapSide.Topics, onClick = { model.side(MapSide.Topics) })
                JChip(stringResource(R.string.knowledge_kind_projects), selected = side == MapSide.Projects, onClick = { model.side(MapSide.Projects) })
            }
        }
        // A refresh that failed keeps the outline; this says it could not be read again (as the list and the entry's page do).
        if (page.paired && root != null) page.message?.let { message ->
            item("reload-error") { JNotice(stringResource(message), Modifier.padding(vertical = Tokens.Space.s), status = Status.NeedsYou) }
        }
        when {
            !page.paired -> item("unpaired") {
                JEmptyState(stringResource(R.string.knowledge_unpaired_title), stringResource(R.string.knowledge_unpaired_text))
            }
            page.message != null && root == null -> item("error") {
                RetryState(stringResource(R.string.knowledge_map_error), stringResource(requireNotNull(page.message)), model::refresh)
            }
            root == null -> item("loading") { JLoading() }
            rows.isEmpty() -> item("empty") {
                JEmptyState(
                    stringResource(if (side == MapSide.Topics) R.string.knowledge_map_no_topics else R.string.knowledge_map_no_projects),
                    stringResource(if (side == MapSide.Topics) R.string.knowledge_map_no_topics_text else R.string.knowledge_map_no_projects_text),
                    icon = if (side == MapSide.Topics) PersonaIcons.topic else PersonaIcons.knowledgeMap)
            }
            else -> {
                item("stats", contentType = "stats") { MapStats(side, root) }
                itemsIndexed(rows, key = { index, _ -> keys[index] }, contentType = { _, _ -> "row" }) { _, row ->
                    val node = row.node
                    OutlineRowView(row, node.id in open, onToggle = { toggle(node.id) }) {
                        val target = node.open
                        when {
                            node.point != null -> openPoint = node
                            target != null -> navigator.open(target.entry(), owner)
                            row.folds -> toggle(node.id)
                        }
                    }
                }
                if (side == MapSide.Topics && page.failed > 0) item("failed", contentType = "note") {
                    Note(pluralStringResource(R.plurals.knowledge_map_failed, page.failed, page.failed))
                }
            }
        }
    }
    openPoint?.let { node ->
        val point = requireNotNull(node.point)
        PointSheet(point, node.crumbs, node.links, { source -> openPoint = null; navigator.open(source, owner) }, { openPoint = null })
    }
}

/** A key per row: a point's id may repeat (or be empty), and a list refuses two rows with one key. */
private fun uniqueKeys(ids: List<String>): List<String> {
    val seen = HashMap<String, Int>()
    return ids.map { id -> seen.merge(id, 1, Int::plus)!!.let { if (it == 1) id else "$id~$it" } }
}

@Composable
private fun rememberMapWords(): MapWords {
    val resources = LocalResources.current
    val locale = currentLocale
    return remember(resources, locale) {
        MapWords(
            knowledge = resources.getString(R.string.knowledge_map_knowledge),
            byTopic = resources.getString(R.string.knowledge_map_by_topic),
            other = resources.getString(R.string.knowledge_map_other),
            otherBody = { resources.getString(R.string.knowledge_map_other_body, it) },
            notRated = resources.getString(R.string.knowledge_map_not_rated),
            projects = resources.getString(R.string.knowledge_kind_projects),
            byStatus = resources.getString(R.string.knowledge_map_by_status),
            active = resources.getString(R.string.knowledge_status_active),
            done = resources.getString(R.string.knowledge_status_done),
            paused = resources.getString(R.string.knowledge_status_paused),
            summary = resources.getString(R.string.knowledge_map_summary),
            goal = resources.getString(R.string.knowledge_map_goal),
            role = resources.getString(R.string.knowledge_map_role),
            latest = resources.getString(R.string.knowledge_map_latest),
            notes = resources.getString(R.string.knowledge_map_notes),
            folders = resources.getString(R.string.knowledge_code_dirs),
            chats = resources.getString(R.string.knowledge_kind_chats),
            people = resources.getString(R.string.knowledge_kind_people),
        )
    }
}

@Composable
private fun MapStats(side: MapSide, root: MapNode) {
    val stats = remember(side, root) {
        if (side == MapSide.Topics) KnowledgeMap.topicStats(root).let { listOf(it.topics, it.points, it.sources) }
        else KnowledgeMap.projectStats(root).let { listOf(it.active, it.withSummary, it.notes) }
    }
    val labels = if (side == MapSide.Topics) listOf(R.string.knowledge_map_stat_topics, R.string.knowledge_map_stat_points, R.string.knowledge_map_stat_sources)
    else listOf(R.string.knowledge_map_stat_active, R.string.knowledge_map_stat_summary, R.string.knowledge_map_stat_notes)
    val locale = currentLocale
    JStatGrid(labels.mapIndexed { i, label -> JStat(stringResource(label), java.text.NumberFormat.getIntegerInstance(locale).format(stats[i])) },
        Modifier.padding(vertical = Tokens.Space.s))
}

@Composable
private fun MapTone.color(): Color {
    val palette = PersonaTheme.palette
    return when (this) {
        MapTone.Accent -> palette.accent
        MapTone.Green -> palette.hue(JHue.Green).dot
        MapTone.Orange -> palette.hue(JHue.Orange).dot
    }
}

/** One row of the outline: a rail per level above it in the branch's color, the title with its count, and the chevron that folds it. */
@Composable
private fun OutlineRowView(row: OutlineRow, open: Boolean, onToggle: () -> Unit, onTap: () -> Unit) {
    val palette = PersonaTheme.palette
    val node = row.node
    val tone = node.tone.color()
    val indent = Tokens.Space.l
    val rail = tone.copy(alpha = 0.35f)
    val railWidth = Tokens.Size.line
    val point = node.point
    val kind = point?.let { PointKind.of(it.kind) }
    val state = if (row.folds) stringResource(if (open) R.string.knowledge_map_expanded else R.string.knowledge_map_collapsed) else null
    Row(
        Modifier.fillMaxWidth().heightIn(min = Tokens.MinTouch.android)
            .drawBehind {
                for (level in 0 until row.depth) {
                    val x = (indent * level + indent / 2).toPx()
                    drawLine(rail, Offset(x, 0f), Offset(x, size.height), railWidth.toPx())
                }
            }
            .clickable(onClick = onTap)
            .padding(start = indent * row.depth),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        if (row.depth == 0) Box(Modifier.padding(horizontal = Tokens.Space.s).size(Tokens.Size.dotL).background(tone, CircleShape))
        else Spacer(Modifier.width(Tokens.Space.s))
        Column(Modifier.weight(1f).padding(vertical = Tokens.Space.s), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xxs)) {
            Text(node.title, style = if (row.depth == 0) PersonaTheme.type.headline else PersonaTheme.type.body, color = palette.ink,
                maxLines = if (point != null) 3 else 2, overflow = TextOverflow.Ellipsis)
            node.body?.takeIf { it.isNotBlank() }?.let {
                Text(it, style = PersonaTheme.type.caption, color = palette.ink2, maxLines = 2, overflow = TextOverflow.Ellipsis)
            }
        }
        if (kind != null) Text(stringResource(kind.label), style = PersonaTheme.type.micro,
            color = if (kind.warns) palette.hue(JHue.Orange).text else palette.ink3, maxLines = 1, modifier = Modifier.padding(start = Tokens.Space.s))
        else node.meta?.takeIf { it.isNotBlank() }?.let {
            Text(it, style = PersonaTheme.type.micro, color = palette.ink3, maxLines = 1, modifier = Modifier.padding(start = Tokens.Space.s))
        }
        if (row.folds) Box(
            Modifier.size(Tokens.MinTouch.android)
                .clickable(onClickLabel = stringResource(R.string.knowledge_map_fold), role = Role.Button, onClick = onToggle)
                .semantics { stateDescription = state.orEmpty() },
            contentAlignment = Alignment.Center,
        ) { JIcon(if (open) PersonaIcons.expand else PersonaIcons.disclosure, null, tint = palette.ink3) }
        else Spacer(Modifier.width(Tokens.Space.s))
    }
}
