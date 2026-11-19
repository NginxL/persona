package persona.feature.knowledge

import persona.core.contracts.api.*
import java.time.LocalDate

// Topics and the knowledge map (docs/assistant/knowledge.md "Topics"): knowledge points Persona curated by subject, as the web's
// `lib/kb-points.ts` and `lib/kb-map.ts` and iOS `KBTopics.swift` read them. The phone draws the map as an outline, not the
// web's two-sided graph: one tree of topics (or of projects), folding open, reading at the system's text size.

private val KNOWN_POINT_KINDS = listOf("concept", "decision", "practice", "pitfall", "fact", "question")

/** What a knowledge point is; the hub may add kinds, and an unknown one reads as a plain point. */
internal enum class PointKind(val wire: String?) {
    Concept("concept"), Decision("decision"), Practice("practice"), Pitfall("pitfall"), Fact("fact"), Question("question"), Plain(null);

    /** Pitfalls and open questions are what to watch out for: they take the warning tone. */
    val warns: Boolean get() = this == Pitfall || this == Question

    companion object {
        fun of(kind: String): PointKind = entries.firstOrNull { it.wire != null && it.wire == kind } ?: Plain
        /** The kinds in the order the page lists them, the unknown ones last. */
        val ordered: List<PointKind> get() = entries.toList()
    }
}

/** A topic's points under one subtopic path ([] for the points directly under the topic), and its subtopics' groups. */
internal data class PointGroup(
    val path: List<String> = emptyList(),
    val points: List<TopicPoint> = emptyList(),
    val groups: List<PointGroup> = emptyList(),
) {
    val name: String get() = path.lastOrNull().orEmpty()
    /** Every point under it, its subtopics' too. */
    val size: Int get() = points.size + groups.sumOf { it.size }
    /** Every point under it, loose ones first. */
    fun flattened(): List<TopicPoint> = points + groups.flatMap { it.flattened() }
}

/**
 * A topic's points as the page shows them: the ones directly under the topic first, then one group per subtopic in the order
 * the subtopics first appear. Blank path parts are dropped, and points without text.
 */
internal fun groupPoints(all: List<TopicPoint>?): PointGroup {
    class Builder(val path: List<String>) {
        val points = mutableListOf<TopicPoint>()
        val groups = mutableListOf<Builder>()
        fun build(): PointGroup = PointGroup(path, this.points.toList(), this.groups.map { it.build() })
    }
    val root = Builder(emptyList())
    for (point in all.orEmpty()) {
        if (point.text.isEmpty()) continue
        var at = root
        for (raw in point.path.orEmpty()) {
            val name = raw.trim()
            if (name.isEmpty()) continue
            at = at.groups.firstOrNull { it.path.last() == name } ?: Builder(at.path + name).also { at.groups += it }
        }
        at.points += point
    }
    return root.build()
}

/** A point's sources that the topic links, by entry; ones it doesn't name are left out (no raw ids on screen). */
internal fun pointSources(point: TopicPoint, links: List<KnowledgeEntry>?): List<KnowledgeEntry> {
    val seen = HashSet<String>()
    val out = mutableListOf<KnowledgeEntry>()
    for (source in point.sources.orEmpty()) {
        val entry = links.orEmpty().firstOrNull { it.kind == source.kind && it.id == source.id } ?: continue
        if (seen.add("${source.kind}/${source.id}")) out += entry
    }
    return out
}

/** A point's day (YYYY-MM-DD) as a date, or null when it is not one. */
internal fun pointDate(day: String): LocalDate? =
    Regex("""^(\d{4})-(\d{2})-(\d{2})$""").matchEntire(day.trim())?.let { m ->
        runCatching { LocalDate.of(m.groupValues[1].toInt(), m.groupValues[2].toInt(), m.groupValues[3].toInt()) }.getOrNull()
    }

/** How many points a topic's list entry says it has (its `extra` is the count). */
internal val KnowledgeEntry.topicPoints: Int?
    get() = if (kind == KnowledgeKind.Topics.wire) extra?.trim()?.toIntOrNull() else null

// MARK: The map

/** The entry a map node opens. */
internal data class MapTarget(val kind: KnowledgeKind, val id: String, val name: String) {
    fun entry() = KnowledgeEntry(kind.wire, id, name)
}

internal enum class MapKind { Root, Branch, Group, Project, Section, Text, Link, Day, Topic, Point }

/** A branch's color: topics cycle through three, projects' are by status. */
internal enum class MapTone { Accent, Green, Orange }

/**
 * One node of the knowledge map: a topic or subtopic, a point, a project and its parts. [collapsed] is how it starts. [meta]
 * sits beside the title: a count, or a project's importance and status. [count] is the number [meta] says, where one is.
 */
internal data class MapNode(
    val id: String,
    val title: String,
    val kind: MapKind,
    val tone: MapTone,
    val kids: List<MapNode> = emptyList(),
    val collapsed: Boolean = false,
    val meta: String? = null,
    val count: Int = 0,
    val body: String? = null,
    val open: MapTarget? = null,
    val point: TopicPoint? = null,
    /** The topic's links, where a point's sources are named. */
    val links: List<KnowledgeEntry> = emptyList(),
    /** The topic and subtopics above it, for the crumbs. */
    val crumbs: List<String> = emptyList(),
    /** A project's curated summary section: the "with a summary" count reads it. */
    val summary: Boolean = false,
) {
    /** Every point under it, its subtopics' too. */
    val pointsUnder: List<MapNode> get() = if (point != null) listOf(this) else kids.flatMap { it.pointsUnder }
}

/** A row of the outline: the node, how deep it is, and whether it has rows under it to open. */
internal data class OutlineRow(val node: MapNode, val depth: Int) {
    val folds: Boolean get() = node.kids.isNotEmpty()
}

/** The words the map's nodes are titled with, in the page's language. */
internal class MapWords(
    val knowledge: String,
    val byTopic: String,
    val other: String,
    val otherBody: (String) -> String,
    val notRated: String,
    val projects: String,
    val byStatus: String,
    val active: String,
    val done: String,
    val paused: String,
    val summary: String,
    val goal: String,
    val role: String,
    val latest: String,
    val notes: String,
    val folders: String,
    val chats: String,
    val people: String,
) {
    fun status(status: String): String = when (status) { "done" -> done; "paused" -> paused; else -> active }
}

internal data class MapTopic(val entry: KnowledgeEntry, val detail: KnowledgeDetail?)

internal object KnowledgeMap {
    /** Topics with more loose points than this, beside subtopics, keep them under one closed Other. */
    const val LOOSE_SHOWN = 3
    /** How many of the biggest topics start open. */
    const val OPEN_TOPICS = 6
    private val IMPORTANCE = listOf("P0", "P1", "P2", "P3")

    /** How many points a topic has: its detail's, else what its list entry says. */
    fun size(topic: MapTopic): Int = topic.detail?.points?.let { groupPoints(it).size } ?: (topic.entry.topicPoints ?: 0)

    /**
     * The topic tree: the root, one branch per topic (biggest first), subtopics, points. At first it shows structure: the
     * biggest topics open onto their subtopics, every subtopic closed.
     */
    fun topics(topics: List<MapTopic>, words: MapWords): MapNode {
        val tones = MapTone.entries
        val order = topics.sortedWith(compareByDescending<MapTopic> { size(it) }.thenBy(String.CASE_INSENSITIVE_ORDER) { it.entry.name })
        val branches = order.mapIndexed { i, topic ->
            val node = group(groupPoints(topic.detail?.points), topic.entry.name, "topic:${topic.entry.id}", tones[i % tones.size], 1,
                topic.detail?.links.orEmpty(), emptyList(), words)
            node.copy(
                body = topic.entry.oneLine,
                open = MapTarget(KnowledgeKind.Topics, topic.entry.id, topic.entry.name),
                collapsed = i >= OPEN_TOPICS,
                meta = if (topic.detail == null) size(topic).toString() else node.meta,
                count = if (topic.detail == null) size(topic) else node.count,
            )
        }
        return MapNode("root:topics", words.knowledge, MapKind.Root, MapTone.Accent, branches, meta = words.byTopic)
    }

    private fun group(g: PointGroup, name: String, id: String, tone: MapTone, depth: Int, links: List<KnowledgeEntry>,
        crumbs: List<String>, words: MapWords): MapNode {
        val here = crumbs + name
        val subs = g.groups.map { group(it, it.name, "$id/${it.name}", tone, depth + 1, links, here, words) }
        val points = g.points.map { MapNode("point:$id#${it.id}", it.text, MapKind.Point, tone, point = it, links = links, crumbs = here) }
        // Beside subtopics a handful of loose points stay; more go under one closed Other, so they don't bury the subtopics.
        val kids = if (subs.isNotEmpty() && points.size > LOOSE_SHOWN)
            subs + MapNode("$id#other", words.other, MapKind.Topic, tone, points, collapsed = true, meta = points.size.toString(), count = points.size,
                body = words.otherBody(name), crumbs = here)
        else subs + points
        return MapNode(id, name, if (depth == 1) MapKind.Branch else MapKind.Topic, tone, kids, collapsed = depth > 1,
            meta = g.size.toString(), count = g.size, crumbs = crumbs)
    }

    // MARK: Projects

    /** A timeline line the main chat wrote ("<time> · Persona · <text>"): [when] ("2026-11-02 21:30") and its cleaned [text]. */
    data class PersonaLine(val whenText: String, val text: String)

    private val LEAD = Regex("""^((\d{4}-)?\d{2}-\d{2}[ T]?)?(\d{2}:\d{2})?\s*(早间|午间|晚间|上午|下午)?\s*([·:：]\s*)?(Persona\s*·\s*)?""") // i18n-ignore: the part-of-day words the main chat writes in its Chinese timeline lines

    /** The line, or null when it is not the main chat's: repeats of the time and "Persona" at the start of its text go, and emphasis marks. */
    fun personaLine(raw: String): PersonaLine? {
        var line = raw.trim()
        if (line.startsWith("-")) line = line.drop(1).trim()
        val parts = line.split(" · ")
        if (parts.size < 3 || parts[1] != "Persona" || parts[0].length != 16 || !parts[0].first().isDigit()) return null
        var text = parts.drop(2).joinToString(" · ")
        while (true) {
            val next = text.removePrefix(LEAD.find(text)?.value.orEmpty())
            if (next == text || next.isEmpty()) break
            text = next
        }
        text = text.replace(Regex("[*`#]+"), "").replace(Regex("\\s+"), " ").trim()
        return PersonaLine(parts[0], text)
    }

    data class SummaryItem(val text: String, val kids: List<SummaryItem> = emptyList())
    data class SummarySection(val title: String, val items: List<SummaryItem>)

    private class ItemBuilder(val text: String, val kids: MutableList<ItemBuilder> = mutableListOf()) {
        fun build(): SummaryItem = SummaryItem(text, kids.map { it.build() })
    }

    private val SECTION = Regex("""^#{2,}\s+""")
    private val LIST_ITEM = Regex("""^(\s*)([-*+]|\d+[.)])\s+""")

    /**
     * A summary.md as its "## " sections of list items (nested two spaces a level; other lines items of their own). The "# <name>"
     * heading and emphasis go; text before the first section goes under [lead].
     */
    fun summary(markdown: String, lead: String): List<SummarySection> {
        class Section(val title: String, val items: MutableList<ItemBuilder> = mutableListOf())
        val out = mutableListOf<Section>()
        // Where each open list item is, by indent.
        val stack = mutableListOf<Pair<Int, ItemBuilder>>()
        for (raw in markdown.split("\n")) {
            val trimmed = raw.trim()
            if (trimmed.isEmpty() || raw.startsWith("# ")) continue
            val heading = SECTION.find(trimmed)
            if (heading != null) {
                out += Section(clean(trimmed.substring(heading.range.last + 1)))
                stack.clear()
                continue
            }
            if (out.isEmpty()) out += Section(lead)
            val marker = LIST_ITEM.find(raw)
            val indent = marker?.groupValues?.get(1)?.replace("\t", "  ")?.length ?: 0
            val text = clean(if (marker != null) raw.substring(marker.range.last + 1) else raw)
            if (text.isEmpty()) continue
            while (stack.isNotEmpty() && stack.last().first >= indent) stack.removeAt(stack.lastIndex)
            val item = ItemBuilder(text)
            val parent = stack.lastOrNull()
            if (marker != null && parent != null) parent.second.kids += item else out.last().items += item
            if (marker != null) stack += indent to item
        }
        return out.map { SummarySection(it.title, it.items.map(ItemBuilder::build)) }.filter { it.items.isNotEmpty() }
    }

    private fun clean(text: String): String = text.replace(Regex("""\*\*|__|`"""), "")
        .replace(Regex("""\[([^\]]+)]\([^)]+\)"""), "$1").replace(Regex("\\s+"), " ").trim()

    // The curated summary's section names are the ones its prompt asks for (server/internal/prompts/kb/project.md): text the
    // hub writes in Chinese, matched to know the goal and the sections open at first.
    private const val GOAL_TITLE = "目标" // i18n-ignore
    private val OPEN_TITLES = setOf("目标", "现状") // i18n-ignore
    private const val ROLE_TITLE = "我的角色" // i18n-ignore

    /** A project's status: its detail's, else the second half of its list `extra`, else active. */
    fun status(entry: KnowledgeEntry, detail: KnowledgeDetail?): String =
        detail?.meta.string("status").trim().ifEmpty { entry.projectStatus }

    /**
     * The projects tree: active ones grouped by importance (the first two groups open), then done and paused ones (closed).
     * Merged and "not a project" ones stay out, as in the list. A project without its detail still has a row.
     */
    fun projects(list: List<KnowledgeEntry>, details: Map<String, KnowledgeDetail>, words: MapWords): MapNode {
        fun rank(e: KnowledgeEntry) = IMPORTANCE.indexOf(e.importance).let { if (it < 0) 9 else it }
        fun state(e: KnowledgeEntry) = status(e, details[e.id])
        val live = list.filter { state(it) !in setOf("merged", "ignored") }
        val sorted = live.sortedWith(compareBy<KnowledgeEntry> { rank(it) }.thenByDescending { it.updated.orEmpty() })
        val active = sorted.filter { state(it) !in setOf("done", "paused") }
        val groups = LinkedHashMap<String, MutableList<MapNode>>()
        for (e in active) {
            val g = e.importance?.takeIf { it in IMPORTANCE } ?: words.notRated
            groups.getOrPut(g) { mutableListOf() } += project(e, details[e.id], MapTone.Accent, words)
        }
        val activeKids = groups.entries.mapIndexed { i, (g, nodes) ->
            MapNode("group:$g", g, MapKind.Group, MapTone.Accent, nodes, collapsed = i >= 2, meta = nodes.size.toString(), count = nodes.size)
        }
        val branches = mutableListOf<MapNode>()
        if (active.isNotEmpty()) branches += MapNode("branch:active", words.active, MapKind.Branch, MapTone.Accent, activeKids,
            meta = active.size.toString(), count = active.size)
        for ((key, title, tone) in listOf(Triple("done", words.done, MapTone.Green), Triple("paused", words.paused, MapTone.Orange))) {
            val these = sorted.filter { state(it) == key }
            if (these.isNotEmpty()) branches += MapNode("branch:$key", title, MapKind.Branch, tone,
                these.map { project(it, details[it.id], tone, words) }, collapsed = true, meta = these.size.toString(), count = these.size)
        }
        return MapNode("root:projects", words.projects, MapKind.Root, MapTone.Accent, branches, meta = words.byStatus)
    }

    private fun project(e: KnowledgeEntry, d: KnowledgeDetail?, tone: MapTone, words: MapWords): MapNode {
        val base = "project:${e.id}"
        val goal = d?.meta.string("goal").trim()
        val role = d?.meta.string("my_role").trim()
        val lines = d?.log.orEmpty().mapNotNull(::personaLine).reversed()
        val sections = summary(d?.summary.orEmpty(), words.summary)
        fun item(i: SummaryItem, id: String): MapNode = MapNode(id, i.text, MapKind.Text, tone,
            i.kids.mapIndexed { n, kid -> item(kid, "$id.$n") }, collapsed = i.kids.isNotEmpty(), body = i.text)
        val kids = mutableListOf<MapNode>()
        // The summary's own sections first (its goal is the fuller one); the main chat's one-line goal / role only without.
        if (goal.isNotEmpty() && sections.none { it.title == GOAL_TITLE }) kids += MapNode("$base#goal", words.goal, MapKind.Section, tone,
            listOf(MapNode("$base#goal.0", goal, MapKind.Text, tone, body = goal)))
        sections.forEachIndexed { i, s ->
            val id = "$base#s$i"
            kids += MapNode(id, s.title, MapKind.Section, tone, s.items.mapIndexed { n, it -> item(it, "$id.$n") },
                collapsed = s.title !in OPEN_TITLES, meta = words.summary, summary = true)
        }
        if (role.isNotEmpty() && sections.none { it.title == ROLE_TITLE }) kids += MapNode("$base#role", words.role, MapKind.Section, tone,
            listOf(MapNode("$base#role.0", role, MapKind.Text, tone, body = role)))
        lines.firstOrNull()?.let { latest ->
            kids += MapNode("$base#latest", words.latest, MapKind.Section, tone,
                listOf(MapNode("$base#latest.0", latest.text, MapKind.Text, tone, meta = latest.whenText, body = latest.text)), meta = latest.whenText.drop(5))
            val byDay = lines.groupBy { it.whenText.take(10) }
            val days = byDay.map { (day, these) ->
                MapNode("$base#day$day", day.drop(5), MapKind.Day, tone, these.mapIndexed { j, l ->
                    MapNode("$base#day$day.$j", l.whenText.drop(11) + " · " + l.text, MapKind.Text, tone, meta = l.whenText, body = l.text)
                }, collapsed = true, meta = these.size.toString(), count = these.size)
            }
            kids += MapNode("$base#notes", words.notes, MapKind.Section, tone, days, collapsed = true, meta = lines.size.toString(), count = lines.size)
        }
        val dirs = d?.meta.strings("dirs")
        if (dirs.isNotEmpty()) kids += MapNode("$base#dirs", words.folders, MapKind.Section, tone,
            dirs.map { MapNode("$base#dir$it", it, MapKind.Text, tone) }, collapsed = true, meta = dirs.size.toString(), count = dirs.size)
        val links = d?.links.orEmpty()
        for ((kind, title) in listOf(KnowledgeKind.Chats to words.chats, KnowledgeKind.People to words.people)) {
            val these = links.filter { it.kind == kind.wire }
            if (these.isNotEmpty()) kids += MapNode("$base#${kind.wire}", title, MapKind.Section, tone, these.map {
                MapNode("$base#${kind.wire}.${it.id}", it.name, MapKind.Link, tone, open = KnowledgeKind.fromWire(it.kind)?.let { k -> MapTarget(k, it.id, it.name) })
            }, collapsed = true, meta = these.size.toString(), count = these.size)
        }
        return MapNode(base, e.name, MapKind.Project, tone, kids, collapsed = true,
            meta = listOf(e.importance.orEmpty(), words.status(status(e, d))).filter { it.isNotEmpty() }.joinToString(" · "),
            body = e.oneLine, open = MapTarget(KnowledgeKind.Projects, e.id, e.name))
    }

    // MARK: Outline

    /** The nodes the outline shows, depth first: a node's kids only while it is open. The root is not a row. */
    fun rows(root: MapNode, open: Set<String>): List<OutlineRow> {
        val out = mutableListOf<OutlineRow>()
        fun walk(node: MapNode, depth: Int) {
            out += OutlineRow(node, depth)
            if (node.id in open) node.kids.forEach { walk(it, depth + 1) }
        }
        root.kids.forEach { walk(it, 0) }
        return out
    }

    /** What starts open: every node that has kids and is not collapsed. */
    fun initiallyOpen(root: MapNode): Set<String> {
        val out = LinkedHashSet<String>()
        fun walk(node: MapNode) {
            if (node.kids.isNotEmpty() && !node.collapsed) out += node.id
            node.kids.forEach(::walk)
        }
        walk(root)
        return out
    }

    /**
     * The nodes that are open: what [initiallyOpen] says, except where the reader chose. [opened] and [closed] are those
     * choices, kept as what they chose (not as a flip of the default), so a default that moves while details arrive never
     * turns a fold the reader made into its opposite.
     */
    fun openNodes(root: MapNode, opened: Collection<String>, closed: Collection<String>): Set<String> =
        (initiallyOpen(root) - closed.toSet()) + opened.toSet()

    /** The numbers above the outline: topics, points and the sources they name. */
    data class TopicStats(val topics: Int, val points: Int, val sources: Int)
    /** Active projects, the ones with a curated summary, and the main chat's notes. */
    data class ProjectStats(val active: Int, val withSummary: Int, val notes: Int)

    fun topicStats(root: MapNode): TopicStats {
        val points = root.pointsUnder
        val sources = points.flatMap { it.point?.sources.orEmpty() }.mapTo(HashSet()) { "${it.kind}/${it.id}" }
        return TopicStats(root.kids.size, points.size, sources.size)
    }

    fun projectStats(root: MapNode): ProjectStats {
        val active = root.kids.firstOrNull { it.id == "branch:active" }?.kids.orEmpty().flatMap { it.kids }
        val projects = root.kids.flatMap { if (it.id == "branch:active") active else it.kids }
        return ProjectStats(active.size, projects.count { p -> p.kids.any { it.summary } },
            projects.sumOf { p -> p.kids.firstOrNull { it.id.endsWith("#notes") }?.count ?: 0 })
    }
}
