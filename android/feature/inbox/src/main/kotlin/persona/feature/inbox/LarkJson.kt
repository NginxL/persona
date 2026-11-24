package persona.feature.inbox

import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.booleanOrNull
import kotlinx.serialization.json.contentOrNull
import kotlinx.serialization.json.longOrNull
import persona.core.inbox.*

// The pages' readers of the Lark gateway's JSON, the same as :core:inbox's own (LarkMessageMapping.kt).
internal fun JsonObject.text(key: String) = (get(key) as? JsonPrimitive)?.contentOrNull.orEmpty()
internal fun JsonObject.number(key: String) = (get(key) as? JsonPrimitive)?.longOrNull
internal fun JsonObject.flag(key: String) = (get(key) as? JsonPrimitive)?.booleanOrNull == true
internal fun JsonObject.cursor(key: String) = text(key).takeIf { it.isNotBlank() }
internal fun JsonObject.messages() = (get("messages") as? JsonArray).orEmpty().mapNotNull { it as? JsonObject }
