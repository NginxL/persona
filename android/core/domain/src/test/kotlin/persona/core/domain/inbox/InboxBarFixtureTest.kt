package persona.core.domain.inbox

import persona.core.domain.sort.*
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.intOrNull
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

/**
 * The section bar's shared cases (#9053, spec/fixtures/inbox README "Section bar"): the choices with their counts, the
 * sections drawn and the empty state (`bar.json`), where an item opened from outside lands (`bar_landing.json`), and how
 * a search moves the choice (`bar_search.json`).
 */
@RunWith(Parameterized::class)
class InboxBarFixtureTest internal constructor(private val fixture: ListFixture) {
    companion object {
        private val operations = setOf("inbox_bar", "inbox_bar_landing", "inbox_bar_search")

        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = listFixtures("spec/fixtures/inbox").filter { (it[0] as ListFixture).operation in operations }
            .also { cases -> check(cases.map { (it[0] as ListFixture).operation }.toSet() == operations) { "Missing a section bar fixture" } }
    }

    @Test fun matchesExpected() {
        val input = fixture.input
        val actual = when (fixture.operation) {
            "inbox_bar" -> {
                val settings = input.obj("settings")
                val list = InboxListSettings(source = settings.string("source"), kind = settings.string("kind"), query = settings.string("query"))
                val groups = buildInboxList(input.objects("items").map(::inboxFixtureItem), input.objects("interactions").map(::inboxFixtureInteraction),
                    input.sessions(), input.obj("mutes").preferences(), list).groups
                val bar = InboxBar.bar(groups, settings.string("chosen"), list, input["done_days_total"]?.jsonPrimitive?.intOrNull)
                obj("bar" to bar.choices.map { obj("id" to it.id, "count" to it.count, "tone" to it.tone?.let(::tone)) },
                    "shown_sections" to bar.shown, "empty" to bar.empty?.let(::empty))
            }
            "inbox_bar_landing" -> InboxBar.landing(inboxFixtureItem(input.obj("item"))).let {
                obj("choice" to it.choice, "open_collapsed" to it.openCollapsed)
            }
            "inbox_bar_search" -> {
                val start = input.obj("start")
                var state = InboxBar.State(start.string("chosen"), start.optional("saved"))
                var query = start.string("query")
                obj("after" to input.objects("steps").map { step ->
                    when {
                        "enter" in step -> { query = step.string("enter"); state = InboxBar.entered(query) }
                        "pick" in step -> state = InboxBar.picked(state, step.string("pick"))
                        else -> step.string("query").let { after -> state = InboxBar.searched(state, query, after); query = after }
                    }
                    obj("chosen" to state.chosen, "saved" to state.saved)
                })
            }
            else -> error("Unknown operation: ${fixture.operation}")
        }
        assertEquals(fixture.name, fixture.expected, actual)
    }

    private fun tone(tone: InboxBar.Tone) = when (tone) { InboxBar.Tone.Needs -> "needs"; InboxBar.Tone.Neutral -> "neutral" }

    private fun empty(empty: InboxBar.Empty) = when (empty) {
        InboxBar.Empty.CaughtUp -> "caught_up"
        InboxBar.Empty.Section -> "section"
        InboxBar.Empty.Filtered -> "filtered"
        InboxBar.Empty.NoMatch -> "no_match"
    }
}

/** A fixture's item as the list reads it (spec/fixtures/inbox README "Fields"). */
internal fun inboxFixtureItem(value: JsonObject) = InboxItem(
    id = value.string("id"), account = value.string("account"), kind = value.string("kind"),
    title = value.string("title"), sender = value.optional("sender"), preview = value.string("preview"),
    summary = value.optional("summary"), tier = value.optional("tier"),
    updatedAt = value.optional("updated_at"), doneAt = value.optional("done_at"),
    status = value.optional("status"), urgent = value.optional("urgent") == "true",
)

/** A fixture's waiting Agent card. */
internal fun inboxFixtureInteraction(value: JsonObject) = InboxInteraction(value.string("id"), value.string("machine_id"),
    value.string("session_id"), value.optional("created_at"), value.string("session_title"), value.optional("summary"), value.string("title"))
