package persona.core.data

import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.jsonObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.DirectionCounts
import persona.core.contracts.DirectionTaskState

// GET /api/v1/assistant/directions/tree (#4352): read as the hub built it, nothing regrouped or recounted.
class DirectionTreeParseTest {
    private fun json(text: String): JsonObject = Json.parseToJsonElement(text).jsonObject

    @Test fun readsDirectionsTasksAgentsAndChildren() {
        val tree = directionTreeOf(json("""{"directions": [
            {"id": "lead", "name": " Product ", "counts": {"working": 2, "needs_you": 1, "stuck": 0, "review": 3}, "running": true,
             "tasks": [{"id": "t1", "title": "Ship", "state": "needs_you", "agents": [
                {"machine_id": "m", "session_id": "s1", "title": "Claude", "agent": "claude", "state": "working",
                 "children": [{"machine_id": "m", "session_id": "s1@x", "title": "Search", "state": "working"}]}
             ]}, {"id": "t2", "title": "Later", "state": "something_new", "agents": []}]},
            {"id": "main", "name": "", "secretary": true, "counts": {"working": 0, "needs_you": 0, "stuck": 0, "review": 0}, "tasks": []}
        ]}"""))!!
        assertEquals(listOf("lead", "main"), tree.directions.map { it.id })
        val lead = tree.directions.first()
        assertEquals("Product", lead.name)
        assertFalse(lead.secretary)
        assertEquals(DirectionCounts(working = 2, needsYou = 1, stuck = 0, review = 3), lead.counts)
        assertEquals(DirectionTaskState.NeedsYou, lead.tasks[0].state)
        assertEquals(DirectionTaskState.Working, lead.tasks[1].state)
        val agent = lead.tasks[0].agents.single()
        assertEquals("claude", agent.agent)
        assertEquals("s1@x", agent.children.single().sessionId)
        assertTrue(tree.directions.last().secretary)
        assertEquals("main", tree.secretary?.id)
    }

    @Test fun aPlannedPieceReadsAsNotStartedAndItsCountIsKept() {
        // #5860: a leader's piece no Agent has ever worked on is "planned"; an older hub omits the count, which reads 0.
        val tree = directionTreeOf(json("""{"directions": [
            {"id": "lead", "name": "Product", "counts": {"working": 1, "needs_you": 0, "stuck": 0, "review": 0, "planned": 2},
             "tasks": [{"id": "t1", "title": "Ship", "state": "working"},
                       {"id": "t2", "title": "Piece", "state": "planned"}]}
        ]}"""))!!
        val lead = tree.directions.single()
        assertEquals(2, lead.counts.planned)
        assertEquals(DirectionTaskState.Planned, lead.tasks[1].state)
        assertEquals(persona.core.contracts.CoreText.Key.DirectionPlanned, lead.tasks[1].state.label)
    }

    @Test fun runningIsTheHubsAndAnOlderHubReadsTheWorkingCount() {
        // #6626: as the hub sent it, whatever the counts; a hub without it spins on a working task, as the rows did before
        fun running(direction: String) = directionTreeOf(json("""{"directions": [$direction]}"""))!!.directions.single().running
        assertFalse(running("""{"id": "a", "counts": {"working": 3}, "running": false}"""))
        assertTrue(running("""{"id": "a", "counts": {"needs_you": 1}, "running": true}"""))
        assertTrue(running("""{"id": "a", "counts": {"working": 1}}"""))
        assertFalse(running("""{"id": "a", "counts": {"needs_you": 1}}"""))
        assertFalse(running("""{"id": "a"}"""))
    }

    @Test fun anOlderHubsEmptyAnswerIsNoTree() {
        assertNull(directionTreeOf(json("{}")))
        assertEquals(0, directionTreeOf(json("""{"directions": []}"""))!!.directions.size)
    }

    @Test fun rowsWithoutIdsAndMissingCountsAreTolerated() {
        val tree = directionTreeOf(json("""{"directions": [
            {"id": "", "name": "Nobody"},
            {"id": "lead", "tasks": [{"id": "", "title": "x"}, {"id": "t", "agents": [{"machine_id": "", "session_id": "s"}]}]}
        ]}"""))!!
        val lead = tree.directions.single()
        assertEquals(DirectionCounts(), lead.counts)
        assertEquals(listOf("t"), lead.tasks.map { it.id })
        assertTrue(lead.tasks.single().agents.isEmpty())
    }

    @Test fun theSecretaryGroupKeepsItsTasksWithoutAMainConversation() {
        // A hub with no main conversation yet sends the secretary group with an empty id.
        val tree = directionTreeOf(json("""{"directions": [{"id": "", "secretary": true, "tasks": [{"id": "t"}]}]}"""))!!
        val secretary = tree.secretary!!
        assertEquals("", secretary.id)
        assertEquals(listOf("t"), secretary.tasks.map { it.id })
    }
}
