package persona.core.domain.tasks

import persona.core.contracts.AgentState
import persona.core.contracts.CoreText
import persona.core.contracts.Direction
import persona.core.contracts.DirectionAgent
import persona.core.contracts.DirectionCounts
import persona.core.contracts.DirectionTask
import persona.core.contracts.DirectionTaskState
import persona.core.contracts.DirectionTree
import persona.core.contracts.ExternalDirection
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.domain.sort.AgentGrouping
import persona.core.domain.sort.AgentListSettings
import persona.core.domain.sort.AgentSession
import persona.core.domain.sort.DirectionGroupPrefix
import persona.core.domain.sort.DirectionTaskPrefix
import persona.core.domain.sort.TodoNoneGroup
import persona.core.domain.sort.buildAgentList
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

// The direction tree (#4352) as the pages take it: they pick from the hub's tree and never regroup or recount it.
class DirectionTreeTest {
    private fun agent(session: String, vararg children: DirectionAgent, machine: String = "m") =
        DirectionAgent(machine, session, title = session, state = "working", children = children.toList())

    private val tree = DirectionTree(listOf(
        Direction("lead-a", "Product", counts = DirectionCounts(working = 1, needsYou = 1), tasks = listOf(
            DirectionTask("t1", "Ship login", DirectionTaskState.NeedsYou, listOf(agent("a1", agent("a1-child"), agent("a1@sub")))),
            DirectionTask("t2", "Write docs", DirectionTaskState.Working, listOf(agent("a2"))),
        )),
        Direction("lead-b", "Ops", external = ExternalDirection("work", "outside-agent")),
        Direction("main", secretary = true, counts = DirectionCounts(review = 1), tasks = listOf(
            DirectionTask("t3", "Check invoices", DirectionTaskState.Review, listOf(agent("a1"))),
        )),
    ))

    private fun row(session: String, state: String = "working") =
        AgentSession(Session(machineId = "m", sessionId = session, state = state, title = session, updatedAt = "2026-01-01T00:00:00Z"))

    @Test fun toDoSectionsKeepTheTreesOrderWithoutEmptyGroups() {
        assertEquals(listOf("lead-a", "main"), tree.todoSections().map { it.id })
        assertEquals(listOf("t1", "t2"), tree.todoSections().first().tasks.map { it.id })
    }

    @Test fun toDoRowsFollowTheTreeAndSkipUnlistedDoneAndEmptySections() {
        // The page lists t2 (open), t1 (done) and t3 (open), keyed by id; the tree's order decides, not the page's.
        val placed = linkedMapOf("t3" to ("t3" to false), "t2" to ("t2" to false), "t1" to ("t1" to true))
        val rows = tree.todoRows(placed) { (_, done) -> done }
        assertEquals(listOf("lead-a", "main"), rows.map { it.first.id })
        assertEquals(listOf("t2"), rows[0].second.map { it.first })
        assertEquals(listOf("t3"), rows[1].second.map { it.first })
        // A section whose to-dos the page does not list (a search, a filter) or are all done is left out.
        assertEquals(listOf("lead-a"), tree.todoRows(mapOf("t2" to ("t2" to false))) { (_, done) -> done }.map { it.first.id })
        assertTrue(tree.todoRows(mapOf("t1" to ("t1" to true))) { (_, done) -> done }.isEmpty())
    }

    @Test fun agentKeysTakeChildrenAndOpenSubAgentsAsTheirParent() {
        assertEquals(setOf(SessionKey("m", "a1"), SessionKey("m", "a1-child"), SessionKey("m", "a2")), tree.agentKeys())
        assertEquals(SessionKey("m", "a1"), agent("a1@sub").openKey)
        assertTrue(agent("a1@sub").isSubagent)
        assertFalse(agent("a1").isSubagent)
    }

    @Test fun mappingMachinesReachesEveryAgentButNotTheIds() {
        val mapped = tree.mapMachines { "$it~g" }
        assertEquals(setOf("m~g"), mapped.directions.flatMap { d -> d.tasks.flatMap { t -> t.agents.flatMap { it.flatten() } } }.map { it.machineId }.toSet())
        assertEquals(tree.directions.map { it.id }, mapped.directions.map { it.id })
        assertEquals(SessionKey("work~g", "outside-agent"), mapped.direction("lead-b")?.external?.target)
    }

    @Test fun unknownStatesReadAsWorking() {
        assertEquals(DirectionTaskState.Stuck, DirectionTaskState.of("stuck"))
        assertEquals(DirectionTaskState.Planned, DirectionTaskState.of("planned"))
        assertEquals(DirectionTaskState.Working, DirectionTaskState.of("later"))
        assertEquals(DirectionTaskState.Working, DirectionTaskState.of(null))
    }

    @Test fun groupingKeysAreStable() {
        assertEquals(TodoListGrouping.Status, TodoListGrouping.of(null))
        assertEquals(TodoListGrouping.Direction, TodoListGrouping.of("direction"))
        assertEquals(TodoListGrouping.Status, TodoListGrouping.of("folder"))
    }

    @Test fun byDirectionIsDirectionThenTaskThenAgentWithTheRestUnderNoTodo() {
        val rows = listOf(row("a1"), row("a1-child"), row("a2"), row("loose", "needs_you"))
        val list = buildAgentList(rows, settings = AgentListSettings(grouping = AgentGrouping.Direction), directions = tree)
        val ids = list.groups.map { it.id }
        assertEquals(DirectionGroupPrefix + "lead-a", ids.first())
        assertTrue("${DirectionTaskPrefix}lead-a/t1" in ids)
        // an agent shows once: a1 is t1's, so t3 (where it shows again) has nothing of its own and the secretary group goes
        assertFalse(ids.any { it.startsWith(DirectionGroupPrefix + "main") })
        assertEquals(CoreText.plain("Product"), list.groups.first().title)
        assertEquals(listOf("m/a1"), list.groups.single { it.id == "${DirectionTaskPrefix}lead-a/t1" }.rows.map { it.id })
        // the child the tree puts under a1 is a1's child, not a row of its own
        assertEquals(listOf("m/a1-child"), list.children["m/a1"].orEmpty().map { it.id })
        assertTrue(TodoNoneGroup in ids)
        // none goes missing: every agent is a row or a child
        val shown = list.groups.flatMap { it.rows }.map { it.id } + list.children.values.flatten().map { it.id }
        assertEquals(rows.map { it.id }.toSet(), shown.toSet())
    }

    @Test fun aDirectionHeadingCountsChildrenOfChildren() {
        // a1 › a1-child › grandchild: the grandchild needs you, and the heading says so.
        val deep = DirectionTree(listOf(Direction("lead", "Product", tasks = listOf(
            DirectionTask("t", "Ship", DirectionTaskState.NeedsYou, listOf(agent("a1", agent("a1-child", agent("grandchild")))))))))
        val rows = listOf(row("a1"), row("a1-child"), row("grandchild", "needs_you"))
        val list = buildAgentList(rows, settings = AgentListSettings(grouping = AgentGrouping.Direction), directions = deep)
        val heading = list.groups.single { it.id == DirectionGroupPrefix + "lead" }
        assertEquals(1, heading.stateCounts?.get(AgentState.NeedsYou))
        assertEquals(2, heading.stateCounts?.get(AgentState.Working))
    }

    @Test fun theSecretaryHeadingIsNamedByTheApp() {
        val list = buildAgentList(listOf(row("a1")), settings = AgentListSettings(grouping = AgentGrouping.Direction),
            directions = DirectionTree(listOf(tree.directions.last())))
        assertEquals(CoreText.of(CoreText.Key.DirectionSecretary), list.groups.first { it.id == DirectionGroupPrefix + "main" }.title)
    }

    @Test fun anUnnamedLeadReadsAsAnUntitledChat() {
        // The hub sends "" for a lead whose conversation has no title: the heading is never blank.
        val list = buildAgentList(listOf(row("a2")), settings = AgentListSettings(grouping = AgentGrouping.Direction),
            directions = DirectionTree(listOf(Direction("lead-x", "", tasks = listOf(DirectionTask("t2", "Write docs", agents = listOf(agent("a2"))))))))
        assertEquals(CoreText.of(CoreText.Key.DirectionUntitled), list.groups.first { it.id == DirectionGroupPrefix + "lead-x" }.title)
        assertEquals(CoreText.plain("Product"), tree.directions.first().title)
        assertEquals(CoreText.of(CoreText.Key.DirectionUntitled), Direction("lead-y", "  ").title)
    }

    @Test fun theToDosTheTreeShowsLeaveTheirStatusSectionsAndTheRestStay() {
        // t1 and t2 share a row (t1 leads it); t3 is shelved on its own. The tree shows t1: t2 now leads that row in
        // its own section, and t3 stays where it was.
        fun triage(section: TriageSection, last: Long) = Triage(section, TriageRule.Rest, last)
        val t1 = Todo("t1"); val t2 = Todo("t2"); val t3 = Todo("t3")
        val shared = TriageGroup("t1", TriageSection.Waiting, t1, listOf(t1, t2),
            mapOf("t1" to triage(TriageSection.Waiting, 5), "t2" to triage(TriageSection.Working, 3)), 5)
        val shelved = TriageGroup("t3", TriageSection.Shelved, t3, listOf(t3), mapOf("t3" to triage(TriageSection.Shelved, 1)), 1)
        val result = TriageResult(
            TriageSection.entries.associateWith { emptyList<TriageGroup>() } + mapOf(TriageSection.Waiting to listOf(shared), TriageSection.Shelved to listOf(shelved)),
            TriageSection.entries.associateWith { 0 } + mapOf(TriageSection.Waiting to 1, TriageSection.Working to 1, TriageSection.Shelved to 1),
            open = 2, shelved = 1, finished = 0, cancelled = 0)
        val rest = result.without(setOf("t1"))
        assertTrue(rest.rows(TriageSection.Waiting).isEmpty())
        assertEquals(listOf("t2"), rest.rows(TriageSection.Working).map { it.lead.id })
        assertEquals(listOf("t3"), rest.rows(TriageSection.Shelved).map { it.lead.id })
        assertEquals(0, rest.todoCounts[TriageSection.Waiting])
        assertEquals(1, rest.todoCounts[TriageSection.Working])
        assertEquals(result, result.without(emptySet()))
        assertTrue(TriageSection.entries.all { result.without(setOf("t1", "t2", "t3")).rows(it).isEmpty() })
    }

    @Test fun withoutATreeEveryAgentIsUnderNoTodoAndOpen() {
        val rows = listOf(row("a1"), row("a2"))
        val list = buildAgentList(rows, settings = AgentListSettings(grouping = AgentGrouping.Direction))
        val none = list.groups.single { it.id == TodoNoneGroup }
        assertFalse(none.folded)
        assertEquals(rows.map { it.id }.toSet(), list.groups.flatMap { it.rows }.map { it.id }.toSet())
    }
}
