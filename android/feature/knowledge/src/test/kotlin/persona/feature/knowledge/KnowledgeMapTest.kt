package persona.feature.knowledge

import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.put
import persona.core.contracts.api.*
import org.junit.Assert.*
import org.junit.Test
import java.time.LocalDate

class KnowledgeMapTest {
    private val words = MapWords(
        knowledge = "Knowledge", byTopic = "by topic", other = "Other", otherBody = { "Points under $it" }, notRated = "Not rated",
        projects = "Projects", byStatus = "by status", active = "Active", done = "Done", paused = "Paused", summary = "Summary",
        goal = "Goal", role = "My role", latest = "Latest", notes = "Main chat notes", folders = "Code folders", chats = "Chats", people = "People",
    )

    private fun point(id: String, text: String = "Point $id", path: List<String>? = null, kind: String = "concept", sources: List<TopicSource>? = null) =
        TopicPoint(id, path, kind, text, sources = sources)

    private fun topic(id: String, name: String, points: List<TopicPoint>?, links: List<KnowledgeEntry>? = null) =
        MapTopic(KnowledgeEntry("topics", id, name, extra = points?.size?.toString()),
            points?.let { KnowledgeDetail("topics", id, "data/topics/$id", points = it, links = links) })

    @Test fun pointsGroupByTheirSubtopicPathAndDropWhatIsBlank() {
        val root = groupPoints(listOf(
            point("1"), point("2", path = listOf("Layout")), point("3", path = listOf(" Layout ", "Grid")),
            point("4", path = listOf("", "Layout")), point("5", text = ""), point("6", path = listOf("Colour")),
        ))
        assertEquals(listOf("1"), root.points.map { it.id })
        assertEquals(listOf("Layout", "Colour"), root.groups.map { it.name })
        val layout = root.groups.first()
        assertEquals(listOf("2", "4"), layout.points.map { it.id })
        assertEquals(listOf("Grid"), layout.groups.map { it.name })
        assertEquals(listOf("2", "4", "3"), layout.flattened().map { it.id })
        assertEquals(5, root.size)
        assertEquals(0, groupPoints(null).size)
    }

    @Test fun pointKindsFallBackToPlainAndOnlyPitfallsAndQuestionsWarn() {
        assertEquals(PointKind.Pitfall, PointKind.of("pitfall"))
        assertEquals(PointKind.Plain, PointKind.of("something-new"))
        assertEquals(PointKind.Plain, PointKind.of(""))
        assertEquals(setOf(PointKind.Pitfall, PointKind.Question), PointKind.entries.filter { it.warns }.toSet())
    }

    @Test fun aPointNamesOnlySourcesTheTopicLinksOnceEach() {
        val chat = KnowledgeEntry("chats", "c1", "Weekly sync")
        val p = point("1", sources = listOf(TopicSource("chats", "c1", "2026-11-02"), TopicSource("chats", "c1", "2026-11-03"),
            TopicSource("chats", "gone", null), TopicSource("people", "c1", null)))
        assertEquals(listOf(chat), pointSources(p, listOf(chat)))
        assertTrue(pointSources(p, null).isEmpty())
    }

    @Test fun aConfirmedDayIsADateOrNothing() {
        assertEquals(LocalDate.of(2026, 11, 2), pointDate("2026-11-02"))
        assertEquals(LocalDate.of(2026, 11, 2), pointDate(" 2026-11-02 "))
        assertNull(pointDate("2026-13-02"))
        assertNull(pointDate("yesterday"))
    }

    @Test fun aTopicsListEntrySaysItsPointCountInExtra() {
        assertEquals(7, KnowledgeEntry("topics", "t", "T", extra = " 7 ").topicPoints)
        assertNull(KnowledgeEntry("topics", "t", "T", extra = "many").topicPoints)
        assertNull(KnowledgeEntry("projects", "p", "P", extra = "7").topicPoints)
    }

    @Test fun topicsAreBiggestFirstWithToneCyclingAndOnlyTheSixBiggestOpen() {
        val topics = (1..8).map { n -> topic("t$n", "Topic $n", (1..n).map { point("p$it") }) } + topic("t0", "Unread", null)
        val root = KnowledgeMap.topics(topics, words)
        assertEquals(listOf("Topic 8", "Topic 7", "Topic 6", "Topic 5", "Topic 4", "Topic 3", "Topic 2", "Topic 1", "Unread"), root.kids.map { it.title })
        assertEquals(listOf(MapTone.Accent, MapTone.Green, MapTone.Orange, MapTone.Accent), root.kids.take(4).map { it.tone })
        assertEquals(listOf(false, false, false, false, false, false, true, true, true), root.kids.map { it.collapsed })
        assertEquals("topic:t8", root.kids.first().id)
        assertEquals(KnowledgeKind.Topics, root.kids.first().open?.kind)
        // A topic whose detail is not read yet still has a row and says its size from the list.
        assertEquals("0", root.kids.last().meta)
        val shown = KnowledgeMap.initiallyOpen(root)
        // The root and the six biggest topics.
        assertEquals(7, shown.size)
        assertTrue("topic:t8" in shown && "topic:t3" in shown && "topic:t2" !in shown)
    }

    @Test fun looseGroupsBesideSubtopicsGoUnderOneClosedOther() {
        val few = KnowledgeMap.topics(listOf(topic("a", "A", listOf(point("1"), point("2"), point("3"), point("4", path = listOf("Sub"))))), words)
        assertEquals(listOf("Sub", "Point 1", "Point 2", "Point 3"), few.kids.single().kids.map { it.title })
        val many = KnowledgeMap.topics(listOf(topic("a", "A", (1..4).map { point("$it") } + point("5", path = listOf("Sub")))), words)
        val kids = many.kids.single().kids
        assertEquals(listOf("Sub", "Other"), kids.map { it.title })
        val other = kids.last()
        assertEquals("topic:a#other", other.id)
        assertTrue(other.collapsed)
        assertEquals(4, other.count)
        assertEquals("Points under A", other.body)
        // Without subtopics every point stays where it is.
        val flat = KnowledgeMap.topics(listOf(topic("a", "A", (1..9).map { point("$it") })), words)
        assertEquals(9, flat.kids.single().kids.size)
    }

    @Test fun nodeIdsCrumbsAndStatsFollowTheTree() {
        val links = listOf(KnowledgeEntry("chats", "c1", "Weekly sync"))
        val root = KnowledgeMap.topics(listOf(
            topic("a", "Alpha", listOf(point("1", path = listOf("Layout", "Grid"), sources = listOf(TopicSource("chats", "c1"))), point("2",
                sources = listOf(TopicSource("chats", "c1"), TopicSource("chats", "c2")))), links),
            topic("b", "Beta", listOf(point("3"))),
        ), words)
        val alpha = root.kids.first()
        val grid = alpha.kids.first { it.title == "Layout" }.kids.single()
        assertEquals("topic:a/Layout/Grid", grid.id)
        assertTrue(grid.collapsed)
        val node = grid.kids.single()
        assertEquals("point:topic:a/Layout/Grid#1", node.id)
        assertEquals(listOf("Alpha", "Layout", "Grid"), node.crumbs)
        assertEquals(links, node.links)
        assertEquals(KnowledgeMap.TopicStats(topics = 2, points = 3, sources = 2), KnowledgeMap.topicStats(root))
    }

    @Test fun theOutlineShowsKidsOnlyOfOpenNodes() {
        val root = KnowledgeMap.topics(listOf(topic("a", "A", listOf(point("1", path = listOf("Sub")), point("2")))), words)
        val first = KnowledgeMap.rows(root, KnowledgeMap.initiallyOpen(root))
        assertEquals(listOf("A" to 0, "Sub" to 1, "Point 2" to 1), first.map { it.node.title to it.depth })
        assertEquals(listOf(true, true, false), first.map { it.folds })
        assertEquals(listOf("A" to 0), KnowledgeMap.rows(root, emptySet()).map { it.node.title to it.depth })
        assertEquals(4, KnowledgeMap.rows(root, KnowledgeMap.initiallyOpen(root) + "topic:a/Sub").size)
    }

    private fun project(id: String, name: String, importance: String?, status: String? = null, updated: String = "2026-11-01") =
        KnowledgeEntry("projects", id, name, importance = importance, updated = updated, extra = status?.let { "short · $it" })

    @Test fun projectsGroupByImportanceUnderActiveAndKeepDoneAndPausedClosed() {
        val list = listOf(project("a", "Alpha", "P0"), project("b", "Beta", "P1"), project("c", "Gamma", "P2"), project("d", "Delta", null),
            project("e", "Epsilon", "P0", "done"), project("f", "Zeta", "P1", "paused"), project("g", "Eta", "P0", "ignored"), project("h", "Theta", "P0", "merged"))
        val root = KnowledgeMap.projects(list, emptyMap(), words)
        assertEquals(listOf("Active", "Done", "Paused"), root.kids.map { it.title })
        val active = root.kids.first()
        assertEquals(listOf("P0", "P1", "P2", "Not rated"), active.kids.map { it.title })
        assertEquals(listOf(false, false, true, true), active.kids.map { it.collapsed })
        assertEquals(listOf(true, true), root.kids.drop(1).map { it.collapsed })
        val names = root.kids.flatMap { branch -> branch.kids.flatMap { if (it.kind == MapKind.Group) it.kids else listOf(it) } }.map { it.title }
        assertFalse("Eta" in names || "Theta" in names)
        assertEquals(KnowledgeMap.ProjectStats(active = 4, withSummary = 0, notes = 0), KnowledgeMap.projectStats(root))
        assertEquals(KnowledgeKind.Projects, active.kids.first().kids.single().open?.kind)
        assertEquals("P0 · Active", active.kids.first().kids.single().meta)
    }

    @Test fun aFoldTheReaderMadeSurvivesTheDefaultMovingWhileDetailsArrive() {
        fun tree(xCollapsed: Boolean, yCollapsed: Boolean) = MapNode("root", "Root", MapKind.Root, MapTone.Accent, listOf(
            MapNode("x", "X", MapKind.Branch, MapTone.Accent, listOf(MapNode("x1", "X1", MapKind.Text, MapTone.Accent)), collapsed = xCollapsed),
            MapNode("y", "Y", MapKind.Branch, MapTone.Accent, listOf(MapNode("y1", "Y1", MapKind.Text, MapTone.Accent)), collapsed = yCollapsed),
        ))
        // The reader opened X (closed by default) and closed Y (open by default); no choice on Z.
        val before = tree(xCollapsed = true, yCollapsed = false)
        assertEquals(setOf("root", "x"), KnowledgeMap.openNodes(before, opened = listOf("x"), closed = listOf("y")))
        // The details come in and both defaults flip: the choices stand, a flip of the default would have undone them.
        val after = tree(xCollapsed = false, yCollapsed = true)
        assertEquals(setOf("root", "x"), KnowledgeMap.openNodes(after, opened = listOf("x"), closed = listOf("y")))
        // Without a choice a node follows its default.
        assertEquals(setOf("root", "y"), KnowledgeMap.openNodes(before, emptyList(), emptyList()))
        assertEquals(setOf("root", "x"), KnowledgeMap.openNodes(after, emptyList(), emptyList()))
    }

    @Test fun projectsWithoutAnActiveOneHaveNoEmptyActiveBranch() {
        val onlyDone = KnowledgeMap.projects(listOf(project("e", "Epsilon", "P0", "done")), emptyMap(), words)
        assertEquals(listOf("Done"), onlyDone.kids.map { it.title })
        val none = KnowledgeMap.projects(listOf(project("g", "Eta", "P0", "ignored"), project("h", "Theta", "P0", "merged")), emptyMap(), words)
        assertTrue(none.kids.isEmpty())
        assertTrue(KnowledgeMap.rows(none, KnowledgeMap.initiallyOpen(none)).isEmpty())
        assertEquals(KnowledgeMap.ProjectStats(0, 0, 0), KnowledgeMap.projectStats(none))
    }

    @Test fun aProjectsPartsCarryItsSummaryNotesAndLinks() {
        val log = listOf(
            "- 2026-11-02 09:15 · Persona · Shipped the **first** build",
            "- 2026-11-02 18:40 · Persona · Waiting on review",
            "- 2026-11-03 10:00 · Someone · Not the main chat's",
        )
        val detail = KnowledgeDetail("projects", "a", "data/projects/a",
            meta = buildJsonObject { put("goal", "Ship it"); put("my_role", "Owner") },
            summary = "# Alpha\n\n## Status\n- Building\n  - Almost there\n## Risks\n- None",
            log = log, links = listOf(KnowledgeEntry("chats", "c1", "Weekly sync"), KnowledgeEntry("people", "p1", "Sam")))
        val root = KnowledgeMap.projects(listOf(project("a", "Alpha", "P0")), mapOf("a" to detail), words)
        val node = root.kids.first().kids.single().kids.single()
        assertEquals("project:a", node.id)
        val parts = node.kids.associateBy { it.id }
        assertEquals(listOf("Ship it"), parts.getValue("project:a#goal").kids.map { it.title })
        assertEquals(listOf("Status", "Risks"), node.kids.filter { it.summary }.map { it.title })
        assertEquals("Building", node.kids.first { it.title == "Status" }.kids.single().title)
        assertTrue(parts.getValue("project:a#notes").collapsed)
        assertEquals(2, parts.getValue("project:a#notes").count)
        assertEquals("Waiting on review", parts.getValue("project:a#latest").kids.single().title)
        assertEquals(listOf("Weekly sync"), parts.getValue("project:a#chats").kids.map { it.title })
        assertEquals(KnowledgeKind.Chats, parts.getValue("project:a#chats").kids.single().open?.kind)
        assertEquals(KnowledgeMap.ProjectStats(active = 1, withSummary = 1, notes = 2), KnowledgeMap.projectStats(root))
    }

    @Test fun theMainChatsTimelineLinesAreRecognisedAndCleaned() {
        val line = KnowledgeMap.personaLine("- 2026-11-02 09:15 · Persona · **Done:** `ship` it")
        assertEquals("2026-11-02 09:15", line?.whenText)
        assertEquals("Done: ship it", line?.text)
        assertNull(KnowledgeMap.personaLine("2026-11-02 09:15 · Someone · text"))
        assertNull(KnowledgeMap.personaLine("not a timeline line"))
    }

    @Test fun aSummaryReadsAsSectionsOfNestedItems() {
        val sections = KnowledgeMap.summary("# Name\nIntro line\n## Goal\n- One\n  - Two\n- Three\n## Empty\n", "Summary")
        assertEquals(listOf("Summary", "Goal"), sections.map { it.title })
        assertEquals(listOf("Intro line"), sections.first().items.map { it.text })
        assertEquals(listOf("One", "Three"), sections.last().items.map { it.text })
        assertEquals(listOf("Two"), sections.last().items.first().kids.map { it.text })
    }
}
