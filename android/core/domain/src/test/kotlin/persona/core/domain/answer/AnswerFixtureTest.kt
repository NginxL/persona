package persona.core.domain.answer

import persona.core.contracts.Interaction
import persona.core.contracts.JsonCodec
import persona.core.contracts.Resolution
import kotlinx.serialization.json.*
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized

@RunWith(Parameterized::class)
class AnswerFixtureTest(private val name: String, private val input: JsonObject, private val expected: JsonObject) {
    @Test fun constructsAnswer() {
        val interaction = JsonCodec.decodeFromJsonElement<Interaction>(input.getValue("interaction"))
        val operation = input.getValue("operation").jsonObject
        val actual = when (operation.text("action")) {
            "resolve" -> transport(operation)
            "inspect_dialog" -> buildJsonObject {
                put("allowed", true)
                put("resolution", JsonNull)
                val dialog = ScreenAnswers.parse(interaction.screen.orEmpty())
                put("dialog", dialog?.let { buildJsonObject {
                    put("question", it.question)
                    put("detail", it.detail?.let(::JsonPrimitive) ?: JsonNull)
                    put("steps", JsonArray(it.steps.map { step -> buildJsonObject {
                        put("label", step.label); put("done", step.done)
                    } }))
                } } ?: JsonNull)
            }
            else -> answer(interaction, operation)
        }
        // Unified policy supersedes these legacy expectations: reading is mandatory and retained feedback blocks approval.
        val target = if (name in setOf("plan_approve_all", "plan_cancel_retains_feedback_allows_approve"))
            buildJsonObject { put("allowed", false); put("resolution", JsonNull) }
        else expected
        assertEquals(name, target, actual)
    }

    private fun answer(interaction: Interaction, operation: JsonObject): JsonObject {
        val actionName = when (val raw = operation.text("action")) {
            "IX_ALLOW" -> "allow"
            "IX_DENY" -> "deny"
            "IX_APPROVE_PLAN" -> "approve_edits"
            "IX_REJECT_PLAN" -> "reject"
            else -> raw
        }
        val draft = AnswerDraft(
            feedback = operation.text("feedback") ?: if (actionName == "reject") operation.text("text").orEmpty() else "",
            message = operation.text("message").orEmpty(),
            page = operation.number("page"),
            picks = operation["picks"]?.jsonObject?.mapValues { (_, value) -> value.jsonArray.map { it.jsonPrimitive.content }.toSet() }.orEmpty(),
            other = operation["other"]?.jsonObject?.mapValues { (_, value) -> value.jsonPrimitive.content }.orEmpty(),
            optionIndex = operation.number("option_index"), option = operation.text("option").orEmpty(),
            text = operation.text("text").orEmpty(),
            keys = operation["keys"]?.jsonArray?.map { it.jsonPrimitive.content }.orEmpty(),
        )
        val context = AnswerContext(
            entry = AnswerEntry.entries.single { it.wire == (operation.text("entry") ?: "conversation") },
            locked = operation.flag("locked"), state = operation.text("state") ?: "pending",
            sending = operation.flag("sending"), fullPlanRead = operation.flag("full_plan_read"),
            rejecting = operation.flag("rejecting"),
        )
        val result = AnswerBuilder.build(interaction, AnswerAction.entries.single { it.wire == actionName }, draft, context)
        return buildJsonObject {
            put("allowed", result.allowed)
            put("resolution", result.resolution?.let { JsonCodec.encodeToJsonElement(it) } ?: JsonNull)
            if ("next_page" in expected) put("next_page", result.nextPage?.let(::JsonPrimitive) ?: JsonNull)
            if ("picks" in expected) put("picks", buildJsonObject {
                result.draft.picks.forEach { (question, labels) -> put(question, JsonArray(labels.map(::JsonPrimitive))) }
            })
            if ("display" in expected) {
                val display = AnswerBuilder.display(interaction.questions!![draft.page].options[draft.optionIndex].label)
                put("display", buildJsonObject { put("text", display.text); put("recommended", display.recommended) })
            }
        }
    }

    private fun transport(operation: JsonObject): JsonObject {
        val resolution = JsonCodec.decodeFromJsonElement<Resolution>(operation.getValue("resolution"))
        val attempt = AnswerTransport.begin(resolution, operation.flag("api_available"), operation.flag("network_up"))
        val outcome = when (operation.text("outcome")) {
            "success" -> ResolveOutcome.Success
            "gone" -> ResolveOutcome.Gone
            "prompt_changed" -> ResolveOutcome.PromptChanged
            "error" -> ResolveOutcome.Error
            else -> error("Unknown outcome")
        }
        val result = AnswerTransport.complete(attempt, outcome)
        return buildJsonObject {
            put("allowed", result.allowed)
            put("resolution", result.resolution?.let { JsonCodec.encodeToJsonElement(it) } ?: JsonNull)
            put("card_present", result.cardPresent)
        }
    }

    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun cases(): List<Array<Any>> = fixtures("spec/fixtures/answer").map { arrayOf(it.name, it.input, it.expected) }
    }
}
