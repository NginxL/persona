package persona.feature.knowledge

import androidx.annotation.StringRes
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyListScope
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.res.stringResource
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.text.style.TextOverflow
import persona.core.contracts.api.KnowledgeDetail
import persona.core.contracts.api.KnowledgeEntry
import persona.core.contracts.api.TopicPoint
import persona.core.designsystem.*
import persona.core.designsystem.icons.PersonaIcons
import java.time.ZoneOffset

// A topic's knowledge points as the pages draw them: a row on the topic's page, a sheet for one point (iOS `KBPointSheet`,
// the web's point drawer). The words come from the hub's point kinds; an unknown kind reads as a plain point.

@get:StringRes
internal val PointKind.label: Int
    get() = when (this) {
        PointKind.Concept -> R.string.knowledge_point_concept
        PointKind.Decision -> R.string.knowledge_point_decision
        PointKind.Practice -> R.string.knowledge_point_practice
        PointKind.Pitfall -> R.string.knowledge_point_pitfall
        PointKind.Fact -> R.string.knowledge_point_fact
        PointKind.Question -> R.string.knowledge_point_question
        PointKind.Plain -> R.string.knowledge_point_plain
    }

/** The kind's small label; pitfalls and open questions in the caution tone, the others quiet. */
@Composable
internal fun PointPill(kind: PointKind, modifier: Modifier = Modifier) {
    val text = stringResource(kind.label)
    if (kind.warns) JTag(text, Tone.Caution, modifier) else JTag(text, modifier)
}

/** "Why: …" for most points, "Cause: …" for a pitfall. */
@Composable
internal fun pointReason(kind: PointKind, why: String): String =
    stringResource(if (kind == PointKind.Pitfall) R.string.knowledge_point_cause else R.string.knowledge_point_why, why)

@Composable
private fun confirmedText(day: String): String {
    val locale = currentLocale
    return remember(day, locale) {
        pointDate(day)?.let { LocalizedFormats.format(it.atStartOfDay(ZoneOffset.UTC).toInstant(), LocalizedFormats.DATE, locale, ZoneOffset.UTC) } ?: day
    }
}

/** One point on a topic's page: its kind, the text, why or the cause, and when it was last confirmed. A tap opens its sheet. */
@Composable
internal fun PointRow(point: TopicPoint, position: GroupPosition, onClick: () -> Unit) {
    val palette = PersonaTheme.palette
    val kind = PointKind.of(point.kind)
    JGroupItem(position) {
        JRow(onClick = onClick, chevron = false) {
            Column(Modifier.weight(1f), verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                    PointPill(kind)
                    point.confirmed?.takeIf { it.isNotBlank() }?.let {
                        Text(stringResource(R.string.knowledge_point_confirmed, confirmedText(it)), style = PersonaTheme.type.micro,
                            color = palette.ink3, maxLines = 1, overflow = TextOverflow.Ellipsis)
                    }
                }
                Text(point.text, style = PersonaTheme.type.body, color = palette.ink)
                point.why?.takeIf { it.isNotBlank() }?.let {
                    Text(pointReason(kind, it), style = PersonaTheme.type.caption, color = palette.ink2)
                }
            }
        }
    }
}

/**
 * One point in full: the kind, when it was confirmed, the text, where it sits ([crumbs]: the topic and subtopics above it),
 * why or the cause, and the sources the topic links (a tap opens that entry). Copy puts the text on the clipboard.
 */
@Composable
internal fun PointSheet(
    point: TopicPoint,
    crumbs: List<String>,
    links: List<KnowledgeEntry>,
    onOpen: (KnowledgeEntry) -> Unit,
    onDismiss: () -> Unit,
) {
    val palette = PersonaTheme.palette
    val kind = PointKind.of(point.kind)
    val sources = remember(point, links) { pointSources(point, links) }
    val copy = rememberCopy()
    val copied = stringResource(R.string.knowledge_point_copied)
    JSheet(onDismiss = onDismiss, scrollable = true) {
        Column(Modifier.fillMaxWidth().padding(bottom = Tokens.Space.l), verticalArrangement = Arrangement.spacedBy(Tokens.Space.m)) {
            Row(horizontalArrangement = Arrangement.spacedBy(Tokens.Space.s), verticalAlignment = Alignment.CenterVertically) {
                PointPill(kind)
                point.confirmed?.takeIf { it.isNotBlank() }?.let {
                    Text(stringResource(R.string.knowledge_point_confirmed, confirmedText(it)), style = PersonaTheme.type.caption, color = palette.ink3, maxLines = 1)
                }
            }
            SelectionContainer { Text(point.text, style = PersonaTheme.type.title, color = palette.ink) }
            if (crumbs.isNotEmpty()) Text(crumbs.joinToString(" › "), style = PersonaTheme.type.caption, color = palette.ink3)
            point.why?.takeIf { it.isNotBlank() }?.let { why ->
                Column(verticalArrangement = Arrangement.spacedBy(Tokens.Space.xs)) {
                    Text(stringResource(if (kind == PointKind.Pitfall) R.string.knowledge_point_cause_title else R.string.knowledge_point_why_title),
                        style = PersonaTheme.type.caption, color = palette.ink2, modifier = Modifier.semantics { heading() })
                    SelectionContainer { Text(why, style = PersonaTheme.type.body, color = palette.ink) }
                }
            }
            if (sources.isNotEmpty()) Column {
                JSectionHeader(stringResource(R.string.knowledge_point_from), count = sources.size)
                sources.forEachIndexed { index, source ->
                    JGroupItem(GroupPosition.of(index, sources.size)) {
                        JRow(source.name, subtitle = source.oneLine?.takeIf { it.isNotBlank() }, subtitleLines = 1, onClick = { onOpen(source) })
                    }
                }
            }
            JButton(stringResource(R.string.knowledge_point_copy), { copy(point.text, copied) }, Modifier.fillMaxWidth(),
                kind = JButtonKind.Secondary, icon = PersonaIcons.copy)
        }
    }
}

/**
 * A topic's page body: the points directly under it as "Knowledge points", then a section per subtopic (a deeper subtopic's
 * points flatten under a small heading of its own). [onOpen] gets the point and where it sits, for its sheet's crumbs.
 */
internal fun LazyListScope.topicPoints(detail: KnowledgeDetail, topic: String, onOpen: (TopicPoint, List<String>) -> Unit) {
    val root = groupPoints(detail.points)
    if (root.size == 0) {
        item("points-empty", contentType = "empty") {
            JEmptyState(stringResource(R.string.knowledge_points_title), stringResource(R.string.knowledge_points_empty), icon = PersonaIcons.topic)
        }
        return
    }
    fun block(prefix: String, points: List<TopicPoint>) {
        itemsIndexed(points, key = { index, _ -> "$prefix/$index" }, contentType = { _, _ -> "point" }) { index, point ->
            PointRow(point, GroupPosition.of(index, points.size)) { onOpen(point, listOf(topic) + point.path.orEmpty().map { it.trim() }.filter { it.isNotEmpty() }) }
        }
    }
    if (root.points.isNotEmpty()) {
        item("points-header", contentType = "section") { JSectionHeader(stringResource(R.string.knowledge_points_title), count = root.points.size) }
        block("points", root.points)
    }
    root.groups.forEachIndexed { index, group ->
        item("points-group/$index", contentType = "section") { JSectionHeader(group.name, count = group.size) }
        block("points-g$index", group.points)
        group.groups.forEachIndexed { inner, deeper ->
            item("points-group/$index/$inner", contentType = "section") {
                Text(deeper.name, style = PersonaTheme.type.caption, color = PersonaTheme.palette.ink2,
                    modifier = Modifier.padding(horizontal = rowPadding, vertical = Tokens.Space.s).semantics { heading() })
            }
            block("points-g$index-$inner", deeper.flattened())
        }
    }
}
