package persona.core.domain.sort

import persona.core.contracts.JsonCodec
import persona.core.contracts.Machine
import persona.core.contracts.Session
import persona.core.contracts.parseTime
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class AgentListFixtureTest internal constructor(private val fixture: ListFixture) {
    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = listFixtures("spec/fixtures/sort")
    }

    @Test fun matchesExpected() {
        val input = fixture.input
        val actual = when (fixture.operation) {
            "session_state" -> obj("sessions" to input.sessions().map {
                obj("id" to it.id, "state" to it.state.wire, "is_live" to it.isLive,
                    "is_quiet" to it.isQuiet, "is_waiting" to it.isWaiting, "sort_date" to it.sortDate?.toString())
            })
            "hub_state" -> obj("state" to deriveHubState(
                JsonCodec.decodeFromJsonElement(Session.serializer(), input.getValue("session")),
                parseTime(input.optional("seen_at")),
            ).wire)
            "agents_list" -> {
                val settings = input.obj("settings")
                val grouping = AgentGrouping.entries.single { it.name.equals(settings.string("grouping"), ignoreCase = true) }
                val result = buildAgentList(input.sessions(), input.objects("machines").map {
                    JsonCodec.decodeFromJsonElement(Machine.serializer(), it)
                }, input.obj("pins").preferences(), input.obj("mutes").preferences(), AgentListSettings(
                    grouping = grouping,
                    quick = AgentQuickFilter.entries.single { it.name.equals(settings.string("quick"), ignoreCase = true) },
                    machineFilter = settings.string("machine_filter"), query = settings.string("query"),
                    kept = settings.strings("kept").toSet(), folded = settings.strings("folded").toSet(),
                    expandedIdle = settings.strings("expanded_idle").toSet(), expandedQuiet = settings.strings("expanded_quiet").toSet(),
                ), held = settings.optional("held"))
                val output = linkedMapOf<String, Any?>(
                    "counts" to result.counts.filterKeys { it.source == null }.mapKeys { it.key.name.replaceFirstChar(Char::lowercase) },
                    "groups" to result.groups.map { group ->
                        linkedMapOf<String, Any?>(
                            "id" to group.id, "title" to group.title, "rows" to group.rows.map { it.id },
                            "visible_rows" to group.visibleRows.map { it.id }, "folded" to group.folded,
                        ).apply {
                            group.parent?.let { put("parent", it) }
                            group.stateCounts?.let { put("state_counts", it.mapKeys { entry -> entry.key.wire }) }
                            group.spoken?.let { put("spoken", it) }
                        }
                    },
                    "unread_shown" to result.unreadShown,
                )
                if (grouping == AgentGrouping.Space) output["quiet_title"] = result.quietTitle
                json(output)
            }
            else -> error("Unknown operation: ${fixture.operation}")
        }
        assertEquals(fixture.name, fixture.expected, actual)
    }
}
