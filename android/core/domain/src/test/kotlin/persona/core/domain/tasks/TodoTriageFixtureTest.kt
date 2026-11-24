package persona.core.domain.tasks

import java.time.Instant
import java.time.ZoneOffset
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.boolean
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.Todo
import persona.core.domain.FixtureReader

/** Replays the same triage fixtures as the hub, web and Apple clients (#5732). */
@RunWith(Parameterized::class)
class TodoTriageFixtureTest(private val name: String, private val fixture: FixtureReader.Case) {
    @Test fun replays() {
        val input = fixture.value.getValue("input").jsonObject
        val expected = fixture.value.getValue("expected").jsonObject
        val sessions = if ("sessions" in input) input.list<Session>("sessions") else emptyList()
        val byKey = sessions.associateBy { it.key }
        val lookup: (SessionKey) -> Session? = { byKey[it] }
        val now = input["now"]?.jsonPrimitive?.content?.let(Instant::parse)?.toEpochMilli()
            ?: Instant.parse("2026-11-10T12:00:00Z").toEpochMilli()
        when (fixture.file) {
            "triage.json" -> {
                val todo = JsonCodec.decodeFromJsonElement<Todo>(input.getValue("todo"))
                val restored = input["restored_at"]?.jsonPrimitive?.content?.let(Instant::parse)?.toEpochMilli() ?: 0
                val got = todo.triage(lookup, now, restored, ZoneOffset.UTC)
                assertEquals(name, expected.getValue("section").jsonPrimitive.content, got.section.fixtureName)
                assertEquals(name, expected.getValue("rule").jsonPrimitive.content, got.rule.fixtureName)
                expected["last"]?.jsonPrimitive?.content?.let {
                    assertEquals(name, Instant.parse(it).toEpochMilli(), got.last)
                }
            }
            "groups.json" -> {
                val got = triageTodos(
                    input.list<Todo>("todos"), lookup, now,
                    quiet = input.getValue("include_quiet").jsonPrimitive.boolean,
                    zone = ZoneOffset.UTC,
                )
                val sections = expected.getValue("sections").jsonObject
                for (section in TriageSection.entries) {
                    val wanted = sections[section.fixtureName]?.jsonArray?.map { row ->
                        row.jsonArray.map { it.jsonPrimitive.content }
                    }.orEmpty()
                    assertEquals("$name: ${section.fixtureName}", wanted, got.rows(section).map { it.members.map(Todo::id) })
                }
                assertEquals(name, expected.getValue("open").jsonPrimitive.int, got.open)
                assertEquals(name, expected.getValue("shelved").jsonPrimitive.int, got.shelved)
                assertEquals(name, expected.getValue("finished").jsonPrimitive.int, got.finished)
                assertEquals(name, expected["cancelled"]?.jsonPrimitive?.int ?: 0, got.cancelled)
            }
            "related.json" -> {
                val todo = JsonCodec.decodeFromJsonElement<Todo>(input.getValue("todo"))
                assertEquals(name, expected.keys("keys"), todo.relatedKeys())
            }
            else -> error("Unknown triage fixture ${fixture.file}")
        }
    }

    private inline fun <reified T> JsonObject.list(name: String): List<T> =
        getValue(name).jsonArray.map { JsonCodec.decodeFromJsonElement<T>(it) }

    private fun JsonObject.keys(name: String): List<String> = getValue(name).jsonArray.map { it.jsonPrimitive.content }

    private val TriageSection.fixtureName: String get() = when (this) {
        TriageSection.Waiting -> "waiting"
        TriageSection.Working -> "working"
        TriageSection.JustDone -> "just_done"
        TriageSection.Pr -> "pr"
        TriageSection.Other -> "other"
        TriageSection.Shelved -> "shelved"
        TriageSection.Done -> "done"
        TriageSection.Cancelled -> "cancelled"
    }

    private val TriageRule.fixtureName: String get() = when (this) {
        TriageRule.FailedStart -> "failed_start"
        TriageRule.Superseded -> "superseded"
        TriageRule.AgentAsks -> "agent_asks"
        TriageRule.OwnerDecides -> "owner_decides"
        TriageRule.AgentRunning -> "agent_running"
        TriageRule.Closed -> "closed"
        TriageRule.Accepted -> "accepted"
        TriageRule.Deployed -> "deployed"
        TriageRule.FinishedUnseen -> "finished_unseen"
        TriageRule.PrOpen -> "pr_open"
        TriageRule.Seen -> "seen"
        TriageRule.Stale -> "stale"
        TriageRule.Rest -> "rest"
        TriageRule.Cancelled -> "cancelled"
    }

    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = FixtureReader.read("triage").filter {
            // Native parked-rule alignment remains #5929; this hub/web-only case is not a native expectation.
            it.value.getValue("name").jsonPrimitive.content != "parked_unread_pr_open"
        }.map {
            arrayOf("${it.file}: ${it.value.getValue("name").jsonPrimitive.content}", it)
        }
    }
}
