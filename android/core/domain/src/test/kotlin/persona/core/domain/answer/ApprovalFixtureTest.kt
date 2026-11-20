package persona.core.domain.answer

import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class ApprovalFixtureTest(private val name: String, private val input: JsonObject, private val expected: JsonObject) {
    @Test fun unifiedPolicy() {
        val actions = ApprovalPolicy.actions(
            AnswerEntry.entries.single { it.wire == input.text("entry") }, input.text("kind")!!, input.flag("locked"))
        val actual = buildJsonObject {
            put("actions", JsonArray(actions.map { action -> buildJsonObject {
                assertEquals(name, AnswerRetry.Never, action.retry)
                put("id", action.id.wire)
                put("authentication", action.authentication.wire)
                put("requires_unlock", action.requiresUnlock?.let(::JsonPrimitive) ?: JsonNull)
                put("available_now", action.availableNow?.let(::JsonPrimitive) ?: JsonNull)
                action.mode?.let { put("mode", it) }
                if (action.requirements.isNotEmpty()) put("requirements", JsonArray(action.requirements.map { JsonPrimitive(it.wire) }))
                action.input?.let { put("input", it) }
                action.maxOptions?.let { put("max_options", it) }
            } }))
        }
        assertEquals(name, expected, actual)
    }

    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> {
            val all = fixtures("spec/fixtures/approval")
            fun key(input: JsonObject) = listOf(input.text("entry"), input.text("kind"), input.flag("locked").toString())
            val unified = all.filter { it.input.text("profile") == "unified" }.associateBy { key(it.input) }
            check(unified.size == AnswerEntry.entries.size * 4 * 2) { "Incomplete unified approval matrix" }
            // Replay every entry in both files against its explicit unified replacement; never enable legacy bypass modes.
            return all.map { arrayOf(it.name, it.input, unified.getValue(key(it.input)).expected) }
        }
    }
}
