package persona.feature.settings

import java.io.File
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.double
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionGraphComponent

/** Replays every shared graph layout case, including both 200-node trees. */
class ExtensionGraphLayoutTest {
    @Test
    fun sharedCasesMatchEveryField() {
        val file = File(System.getProperty("persona.repo.root"), "spec/fixtures/graph-layout/cases.json")
        val document = JsonCodec.parseToJsonElement(file.readText()).jsonObject
        assertEquals(1, document.getValue("version").jsonPrimitive.int)
        val cases = document.getValue("cases").jsonArray
        assertTrue("The shared fixture has cases", cases.isNotEmpty())
        for (caseElement in cases) {
            val case = caseElement.jsonObject
            val name = case.getValue("name").jsonPrimitive.content
            val graph = JsonCodec.decodeFromJsonElement(
                ExtensionGraphComponent.serializer(),
                case.getValue("graph"),
            )
            val actual = ExtensionGraphLayout.layout(graph)
            assertCase(name, case.getValue("expected").jsonObject, actual)
        }
    }

    private fun assertCase(name: String, expected: JsonObject, actual: ExtensionGraphLayoutResult) {
        val expectedAt = expected.getValue("at").jsonObject
        assertEquals("$name: at ids", expectedAt.keys, actual.at.keys)
        for ((id, expectedPoint) in expectedAt) {
            val coordinates = expectedPoint.jsonArray
            assertEquals("$name: at[$id] coordinate count", 2, coordinates.size)
            val point = actual.at.getValue(id)
            assertNumber("$name: at[$id].x", coordinates[0], point.x)
            assertNumber("$name: at[$id].y", coordinates[1], point.y)
        }

        val expectedSize = expected.getValue("size").jsonArray
        assertEquals("$name: size coordinate count", 2, expectedSize.size)
        assertNumber("$name: size.width", expectedSize[0], actual.size.width)
        assertNumber("$name: size.height", expectedSize[1], actual.size.height)

        val expectedEdges = expected.getValue("edges").jsonArray.map { edge ->
            edge.jsonArray.map { it.jsonPrimitive.content }
        }
        assertEquals(
            "$name: edges",
            expectedEdges,
            actual.edges.map { listOf(it.from, it.to) },
        )
        assertEquals(
            "$name: reading order",
            expected.getValue("order").jsonArray.map { it.jsonPrimitive.content },
            actual.order,
        )
        assertNumberMap("$name: reading depth", expected.getValue("depth").jsonObject, actual.depth)
        assertNumberMap("$name: folds", expected.getValue("folds").jsonObject, actual.folds)

        val expectedFrames = expected.getValue("frames").jsonArray.map { frameElement ->
            val frame = frameElement.jsonObject
            frame.getValue("group").jsonPrimitive.content to
                frame.getValue("members").jsonArray.map { it.jsonPrimitive.content }
        }
        assertEquals(
            "$name: frames",
            expectedFrames,
            actual.frames.map { it.group to it.members },
        )
    }

    private fun assertNumberMap(label: String, expected: JsonObject, actual: Map<String, Int>) {
        assertEquals("$label ids", expected.keys, actual.keys)
        for ((id, value) in expected) {
            assertNumber("$label[$id]", value, actual.getValue(id).toDouble())
        }
    }

    private fun assertNumber(label: String, expected: JsonElement, actual: Double) {
        assertEquals(label, expected.jsonPrimitive.double, actual, 1e-9)
    }
}
