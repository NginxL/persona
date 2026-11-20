package persona.core.domain.sort

import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.domain.FixtureReader

/**
 * The Agents tab's Needs you, one row per waiting session, and its badge (#3580) against spec/fixtures/needs-you, which
 * iOS reads too: the rows, the rows the badge counts (`badge_rows`, as ShellCounts.agentRows reads them) and the number.
 * A case marked as Android's known difference (`known_differences.android`) is asserted at today's value; a mark on any
 * client must name a decision and change what that client is asserted at.
 */
@RunWith(Parameterized::class)
class AgentsNeedsYouFixtureTest(private val name: String, private val fixture: FixtureReader.Case) {
    @Test fun rowsAndBadge() {
        val input = fixture.value.getValue("input").jsonObject
        val want = wanted()
        val quiet = input.strings("quiet").toSet()
        // The hub marks a quiet task's agents on the session; the fixture lists them apart.
        val sessions = input.list<Session>("sessions").map { if (it.key.path in quiet) it.copy(quiet = true) else it }
        val byKey = sessions.associateBy { it.key }
        val mutes = input.getValue("mutes").jsonObject.let {
            AgentPreferences(spaces = it.strings("spaces"), sessions = it.strings("sessions"), allowed = it.strings("allowed").toSet())
        }
        val rows = needsYouRows(needsYouCards(input.list<Interaction>("interactions"), byKey), needsYouWaiting(sessions))
        assertEquals(name, want.getValue("rows").rows(), rows.ids())
        assertEquals("$name: badge rows", want.getValue("badge_rows").rows(), needsYouBadgeRows(rows, byKey, mutes).ids())
        assertEquals("$name: badge", want.getValue("badge").jsonPrimitive.int, needsYouBadge(rows, byKey, mutes))
    }

    /** What Android is asserted at: the case's `expected`, with Android's known difference applied. */
    private fun wanted(): Map<String, JsonElement> {
        val expected = fixture.value.getValue("expected").jsonObject
        var want: Map<String, JsonElement> = expected
        fixture.value["known_differences"]?.jsonObject?.forEach { (lane, value) ->
            val mark = value.jsonObject
            assertTrue("$name: known difference for $lane, which does not replay the fixture", lane in LANES)
            assertTrue("$name: $lane's known difference names no decision",
                mark["decision"]?.jsonPrimitive?.content?.matches(Regex("D[0-9]+")) == true)
            assertTrue("$name: $lane's known difference does not say why", mark["why"]?.jsonPrimitive?.content?.isNotBlank() == true)
            val replace = mark.getValue("replace").jsonObject
            assertTrue("$name: $lane's known difference replaces ${replace.keys - expected.keys}", expected.keys.containsAll(replace.keys))
            val today = expected + replace
            assertNotEquals("$name: $lane's known difference changes nothing", expected.toMap(), today)
            if (lane == "android") want = today
        }
        return want
    }

    private fun List<NeedsYouRow>.ids(): List<Pair<String, List<String>>> = map { it.id to it.cards.map(Interaction::id) }

    private fun JsonElement.rows(): List<Pair<String, List<String>>> = jsonArray.map { row ->
        row.jsonObject.getValue("id").jsonPrimitive.content to row.jsonObject.strings("cards")
    }

    private inline fun <reified T> JsonObject.list(name: String): List<T> =
        getValue(name).jsonArray.map { JsonCodec.decodeFromJsonElement<T>(it) }

    private fun JsonObject.strings(name: String): List<String> = getValue(name).jsonArray.map { it.jsonPrimitive.content }

    companion object {
        /** The clients that replay the fixture, the only names a known difference may carry (the fixture's README). */
        private val LANES = setOf("android", "ios")

        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = FixtureReader.read("needs-you").map {
            arrayOf(it.value.getValue("name").jsonPrimitive.content, it)
        }
    }
}
