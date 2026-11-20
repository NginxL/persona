package persona.core.domain.answer

import java.io.File
import persona.core.contracts.JsonCodec
import kotlinx.serialization.json.*

internal data class AnswerFixture(val name: String, val input: JsonObject, val expected: JsonObject)

/** The cases in [path], spelled out as "spec/fixtures/<domain>" so the build declares that domain as the tests' input. */
internal fun fixtures(path: String): List<AnswerFixture> {
    val root = File(System.getProperty("persona.repo.root") ?: error("persona.repo.root is required"))
    val directory = File(root, path)
    check(directory.isDirectory) { "Missing fixtures: $path" }
    val files = directory.listFiles { file -> file.extension == "json" }!!.sortedBy { it.name }
    check(files.isNotEmpty()) { "Empty fixtures: $path" }
    return files.flatMap { file ->
        val json = JsonCodec.parseToJsonElement(file.readText()).jsonObject
        check(json.getValue("schema_version").jsonPrimitive.int == 1)
        json.getValue("cases").jsonArray.map {
            val case = it.jsonObject
            AnswerFixture(case.text("name")!!, case.getValue("input").jsonObject, case.getValue("expected").jsonObject)
        }
    }.also { cases -> check(cases.map { it.name }.distinct().size == cases.size) }
}

internal fun JsonObject.text(key: String): String? = get(key)?.jsonPrimitive?.contentOrNull
internal fun JsonObject.flag(key: String, default: Boolean = false): Boolean = get(key)?.jsonPrimitive?.booleanOrNull ?: default
internal fun JsonObject.number(key: String): Int = get(key)?.jsonPrimitive?.int ?: 0
