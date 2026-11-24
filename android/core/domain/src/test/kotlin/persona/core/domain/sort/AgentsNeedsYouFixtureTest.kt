package persona.core.domain.sort

import java.io.File
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.add
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.BeforeClass
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.domain.FixtureReader

/**
 * The Agents tab's Needs you, one row per waiting session, and its badge (#3580) against spec/fixtures/needs-you, which
 * iOS reads too: the rows, the rows the badge counts (`badge_rows`, as ShellCounts.agentRows reads them), the number and
 * what the Agents page's Needs you header and filter show (`header`). A case marked as Android's known difference
 * (`known_differences.android`) is asserted at today's value; a mark on any client must name a decision and change what
 * that client is asserted at. With the counts report switch on (spec/fixtures/counts/README.md, "Lane reports"), each
 * case's badge and header, with their rows, also go to the Android lane's report.
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
        val badgeRows = needsYouBadgeRows(rows, byKey, mutes)
        val badge = needsYouBadge(rows, byKey, mutes)
        // The Agents page's Needs you header and filter count the section's rows (agentNeedsYouRows, by these rules).
        val header = rows.size
        // Reported before the assertions, so a case that fails is in the report with what Android counted.
        report("agents_tab", badge, badgeRows)
        report("agents_section", header, rows)
        assertEquals(name, want.getValue("rows").rows(), rows.ids())
        assertEquals("$name: badge rows", want.getValue("badge_rows").rows(), badgeRows.ids())
        assertEquals("$name: badge", want.getValue("badge").jsonPrimitive.int, badge)
        assertEquals("$name: header", want.getValue("header").jsonPrimitive.int, header)
    }

    /** One line of the Android lane's report, when the switch is on: what [surface] shows for this case, and its rows. */
    private fun report(surface: String, value: Int, rows: List<NeedsYouRow>) {
        val file = reportFile() ?: return
        val line = buildJsonObject {
            put("lane", "android")
            put("fixture", "needs-you")
            put("case", name)
            put("surface", surface)
            put("value", value)
            put("keys", buildJsonArray {
                rows.forEach { row ->
                    add(buildJsonObject {
                        put("id", row.id)
                        put("cards", buildJsonArray { row.cards.forEach { add(it.id) } })
                    })
                }
            })
        }
        file.appendText("$line\n")
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

        /**
         * The Android lane's report, `<directory>/android.jsonl`, when the counts report switch is on: the build passes
         * the directory as `persona.counts.report` while `PERSONA_COUNTS_REPORT` is set (core/domain's build script).
         */
        private fun reportFile(): File? = System.getProperty("persona.counts.report")?.takeIf { it.isNotBlank() }
            ?.let { File(it, "android.jsonl") }

        /** Each run's report starts empty: a run writes every case once. */
        @JvmStatic @BeforeClass fun startReport() {
            val file = reportFile() ?: return
            file.parentFile.mkdirs()
            file.writeText("")
        }

        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = FixtureReader.read("needs-you").map {
            arrayOf(it.value.getValue("name").jsonPrimitive.content, it)
        }
    }
}
