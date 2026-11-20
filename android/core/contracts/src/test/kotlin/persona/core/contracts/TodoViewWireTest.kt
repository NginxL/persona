package persona.core.contracts

import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.generated.TodoCounts
import persona.core.contracts.generated.TodoDecidePush
import persona.core.contracts.generated.TodoItemDetail
import persona.core.contracts.generated.TodoPlaces
import persona.core.contracts.generated.TodoRecap
import persona.core.contracts.generated.TodoResult
import persona.core.contracts.generated.TodoSummary
import persona.core.contracts.generated.TodoTimeline

/**
 * The views, counts and places the shared to-do cases expect (spec/fixtures/todo-view and todo-counts, #10226), the
 * timelines, results and recaps (spec/fixtures/todo-rounds, #10465) with what the item route carries of them
 * (itemDetail), and the decision pushes' todo (spec/fixtures/todo-push, #10465) read into the generated types
 * (generated/TodoViewWire.kt) and write back as the same JSON. The codec writes required fields even at their
 * defaults and leaves absent optional ones out, as the hub does, and refuses a key a type lacks.
 */
@RunWith(Parameterized::class)
class TodoViewWireTest(private val name: String, private val definition: String, private val sample: JsonElement) {
    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun samples(): List<Array<Any>> {
            val root = File(System.getProperty("persona.repo.root"))
            // Written out as spec/fixtures/<domain>, so the build declares these domains as the test's inputs.
            return listOf("spec/fixtures/todo-view", "spec/fixtures/todo-counts", "spec/fixtures/todo-rounds",
                "spec/fixtures/todo-push").flatMap { path ->
                val domain = path.substringAfterLast('/')
                val files = requireNotNull(File(root, path).listFiles { f -> f.extension == "json" }).sortedBy { it.name }
                require(files.isNotEmpty()) { "Missing $domain fixtures" }
                files.flatMap { file ->
                    Json.parseToJsonElement(file.readText()).jsonObject.getValue("cases").jsonArray.flatMap { case ->
                        val c = case.jsonObject
                        val expected = c.getValue("expected").jsonObject
                        val where = "$domain/${file.name} ${c.getValue("name").jsonPrimitive.content}"
                        val results = expected["results"]?.jsonArray.orEmpty()
                            .mapIndexed { i, r -> arrayOf<Any>("$where TodoResult $i", "TodoResult", r) }
                        val detail = itemDetail(expected)?.let { listOf(arrayOf<Any>("$where TodoItemDetail", "TodoItemDetail", it)) }
                        listOf("view" to "TodoSummary", "counts" to "TodoCounts", "places" to "TodoPlaces",
                            "timeline" to "TodoTimeline", "recap" to "TodoRecap", "todo" to "TodoDecidePush")
                            .mapNotNull { (key, def) -> expected[key]?.let { arrayOf<Any>("$where $def", def, it) } } + results +
                            detail.orEmpty()
                    }
                }
            }
        }

        /**
         * What GET /api/v1/todos/{id} carries beside the row for a todo-rounds case (#10465): the rounds the paper shows
         * (the case's latest), how many there are, its recap and results; null for a case of another domain.
         */
        private fun itemDetail(expected: JsonObject): JsonObject? {
            val rounds = expected["timeline"]?.jsonObject?.getValue("rounds")?.jsonArray ?: return null
            val latest = expected["latest"]?.jsonArray.orEmpty().map { it.jsonPrimitive.content }
            return buildJsonObject {
                put("rounds", JsonArray(rounds.filter { it.jsonObject.getValue("id").jsonPrimitive.content in latest }))
                put("rounds_total", rounds.size)
                expected["recap"]?.let { put("recap", it) }
                expected["results"]?.let { put("results", it) }
            }
        }

        private val codec = Json { encodeDefaults = true; explicitNulls = false }
    }

    @Test fun writesTheSampleBack() {
        val written = when (definition) {
            "TodoSummary" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoSummary>(sample))
            "TodoCounts" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoCounts>(sample))
            "TodoPlaces" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoPlaces>(sample))
            "TodoTimeline" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoTimeline>(sample))
            "TodoResult" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoResult>(sample))
            "TodoRecap" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoRecap>(sample))
            "TodoDecidePush" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoDecidePush>(sample))
            "TodoItemDetail" -> codec.encodeToJsonElement(codec.decodeFromJsonElement<TodoItemDetail>(sample))
            else -> error("Unregistered definition: $definition")
        }
        assertEquals(name, sample, written)
    }
}
