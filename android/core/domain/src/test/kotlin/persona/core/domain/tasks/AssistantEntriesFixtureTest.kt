package persona.core.domain.tasks

import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.FeedItem
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.TaskPlan
import persona.core.contracts.Todo
import persona.core.domain.FixtureReader
import java.time.Instant

/** The Assistant's Needs you / In progress / Finished split (#3387, #10922) against spec/fixtures/entries, which the web app
 *  and iOS read too. */
@RunWith(Parameterized::class)
class AssistantEntriesFixtureTest(private val name: String, private val fixture: FixtureReader.Case) {
    @Test fun splits() {
        val input = fixture.value.getValue("input").jsonObject
        val expected = fixture.value.getValue("expected").jsonObject
        val todos = input.list<Todo>("todos")
        val got = assistantEntries(
            conversation = input.getValue("conversation").jsonPrimitive.content,
            main = input["main"]?.jsonPrimitive?.content,
            todos = todos,
            sessions = input.list<Session>("sessions"),
            interactions = input.list<Interaction>("interactions"),
            plans = input.getValue("plans").jsonObject.values.map { JsonCodec.decodeFromJsonElement<TaskPlan>(it) },
            feed = input.list<FeedItem>("feed"),
            now = Instant.parse(input.getValue("now").jsonPrimitive.content).toEpochMilli(),
        )
        assertEquals(name, expected.keys("needs_you"), got.needsYou.map { it.key })
        assertEquals(name, expected.keys("running"), got.running.map { it.key })
        assertEquals(name, expected.keys("shelved"), got.shelved.map { it.key })
        // Finished (#10922): what the conversation handed out that finished in the last 24 hours, newest first, and which
        // of them nobody has opened since (drawn as new)
        assertEquals(name, expected.optionalKeys("finished"), got.finished.map { it.key })
        assertEquals(name, expected.optionalKeys("finished_new"), got.finished.filter { it.fresh }.map { it.key })
        // the In progress rows that say their supervisor is reviewing them
        assertEquals(name, expected.optionalKeys("supervisor_review"),
            got.running.filter { it is RunningEntry.Task && it.supervisorReview }.map { it.key })
        for ((key, value) in (expected["activity"] as? JsonObject).orEmpty()) {
            val row = got.running.first { it.key == key }
            val work = when (row) { is RunningEntry.Task -> row.activity; is RunningEntry.Agent -> row.activity }
            if (value == kotlinx.serialization.json.JsonNull) assertEquals(name, null, work)
            else {
                val wanted = value.jsonObject
                assertEquals(name, wanted.getValue("tasks").jsonPrimitive.content.toInt(), work?.tasks)
                assertEquals(name, wanted.getValue("subagents").jsonPrimitive.content.toInt(), work?.subagents)
                assertEquals(name, wanted.getValue("workflows").jsonPrimitive.content.toInt(), work?.workflows)
                wanted["phase"]?.let { assertEquals(name, it.jsonPrimitive.content, work?.workflow?.phase) }
                wanted["progress"]?.let { assertEquals(name, it.jsonPrimitive.content, work?.workflow?.let(persona.core.domain.sort.WorkflowTimeline::counts)) }
            }
            if (name == "external_wait_without_background_keeps_clock") assertTrue(name, (row as RunningEntry.Task).waiting)
        }
        // one entry each, and no agent of a listed task on its own
        val keys = got.needsYou.map { it.key } + got.running.map { it.key } + got.shelved.map { it.key } + got.finished.map { it.key }
        assertEquals(name, keys.size, keys.toSet().size)
        val taskAgents = todos.filter { !it.isDone && "task:${it.id}" in keys }
            .flatMap { t -> t.activeAgents.map { "agent:${it.machineId}/${it.sessionId}" } }.toSet()
        assertTrue(name, keys.none { it in taskAgents })
    }

    private inline fun <reified T> JsonObject.list(name: String): List<T> =
        getValue(name).jsonArray.map { JsonCodec.decodeFromJsonElement<T>(it) }

    private fun JsonObject.keys(name: String): List<String> = getValue(name).jsonArray.map { it.jsonPrimitive.content }

    /** An optional list of row keys: absent means none. */
    private fun JsonObject.optionalKeys(name: String): List<String> = this[name]?.jsonArray?.map { it.jsonPrimitive.content }.orEmpty()

    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = FixtureReader.read("entries").map {
            arrayOf(it.value.getValue("name").jsonPrimitive.content, it)
        }
    }
}
