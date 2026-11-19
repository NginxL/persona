package persona.core.contracts

import java.io.File
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.junit.runners.Parameterized
import persona.core.contracts.generated.TodoCounts
import persona.core.contracts.generated.TodoPlaces
import persona.core.contracts.generated.TodoRecap
import persona.core.contracts.generated.TodoResult
import persona.core.contracts.generated.TodoSummary
import persona.core.contracts.generated.TodoTimeline

/**
 * The views, counts and places the shared to-do cases expect (spec/fixtures/todo-view and todo-counts, #10226), and the
 * timelines, results and recaps (spec/fixtures/todo-rounds, #10465), read into the generated types
 * (generated/TodoViewWire.kt) and write back as the same JSON. The codec writes required fields even at their defaults
 * and leaves absent optional ones out, as the hub does, and refuses a key a type lacks.
 */
@RunWith(Parameterized::class)
class TodoViewWireTest(private val name: String, private val definition: String, private val sample: JsonElement) {
    companion object {
        @JvmStatic @Parameterized.Parameters(name = "{0}")
        fun samples(): List<Array<Any>> {
            val root = File(System.getProperty("persona.repo.root"), "spec/fixtures")
            return listOf("todo-view", "todo-counts", "todo-rounds").flatMap { domain ->
                val files = requireNotNull(File(root, domain).listFiles { f -> f.extension == "json" }).sortedBy { it.name }
                require(files.isNotEmpty()) { "Missing $domain fixtures" }
                files.flatMap { file ->
                    Json.parseToJsonElement(file.readText()).jsonObject.getValue("cases").jsonArray.flatMap { case ->
                        val c = case.jsonObject
                        val expected = c.getValue("expected").jsonObject
                        val where = "$domain/${file.name} ${c.getValue("name").jsonPrimitive.content}"
                        val results = expected["results"]?.jsonArray.orEmpty()
                            .mapIndexed { i, r -> arrayOf<Any>("$where TodoResult $i", "TodoResult", r) }
                        listOf("view" to "TodoSummary", "counts" to "TodoCounts", "places" to "TodoPlaces",
                            "timeline" to "TodoTimeline", "recap" to "TodoRecap")
                            .mapNotNull { (key, def) -> expected[key]?.let { arrayOf<Any>("$where $def", def, it) } } + results
                    }
                }
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
            else -> error("Unregistered definition: $definition")
        }
        assertEquals(name, sample, written)
    }
}
