package persona.core.domain.inbox

import persona.core.domain.sort.*
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class InboxListFixtureTest internal constructor(private val fixture: ListFixture) {
    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = listFixtures("spec/fixtures/inbox").filter { fixture ->
            // The display package's feature:inbox tests own the other operations; this test covers only list projections.
            (fixture[0] as ListFixture).operation in setOf("inbox_item_state", "hub_inbox_order", "inbox_list")
        }
    }

    @Test fun matchesExpected() {
        val input = fixture.input
        val items = input.objects("items").map(::inboxFixtureItem)
        val actual = when (fixture.operation) {
            "inbox_item_state" -> obj("items" to items.map {
                obj("id" to it.id, "triage" to it.triage.wire, "is_triaged" to it.isTriaged, "is_done" to it.isDone)
            })
            "hub_inbox_order" -> obj("ids" to orderHubInbox(items).map { it.id })
            "inbox_list" -> {
                val settings = input.obj("settings")
                val result = buildInboxList(items, input.objects("interactions").map(::inboxFixtureInteraction), input.sessions(), input.obj("mutes").preferences(), InboxListSettings(
                    source = settings.string("source"), kind = settings.string("kind"), query = settings.string("query"),
                    expanded = settings.strings("expanded").toSet(),
                ))
                fun rows(rows: List<InboxRow>) = rows.map { obj("type" to it.type.name.lowercase(), "id" to it.id) }
                obj("groups" to result.groups.map {
                    obj("id" to it.id, "title" to it.title, "count" to it.count, "rows" to rows(it.rows),
                        "visible_rows" to rows(it.visibleRows), "folded" to it.folded)
                }, "action_count" to result.actionCount, "filter_counts" to result.filterCounts)
            }
            else -> error("Unknown operation: ${fixture.operation}")
        }
        assertEquals(fixture.name, fixture.expected, actual)
    }
}
