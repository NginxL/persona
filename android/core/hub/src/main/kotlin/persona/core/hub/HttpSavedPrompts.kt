package persona.core.hub

import kotlinx.coroutines.withContext
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.put
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import persona.core.contracts.JsonCodec
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.SavedPrompt
import persona.core.contracts.api.SavedPromptsApi

/** Follow the paired socket's route, explicitly naming this assistant even if the switcher changes. */
class HttpSavedPrompts(private val connection: OkHttpHubConnection, manager: String?) : SavedPromptsApi {
    private val owner = RequestManager(manager.orEmpty())

    override suspend fun list(): List<SavedPrompt>? = try {
        responseField(request("GET"), "prompts")
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun save(id: String?, name: String, text: String): SavedPrompt {
        val body = buildJsonObject { put("name", name); put("text", text) }.toString()
        val answer = if (id == null) request("POST", body = body) else request("PUT", id, body)
        return responseField(answer, "prompt")
    }

    override suspend fun reorder(ids: List<String>): List<SavedPrompt> {
        val body = buildJsonObject { put("ids", JsonCodec.encodeToJsonElement(ids)) }.toString()
        return responseField(request("PUT", "order", body), "prompts")
    }

    override suspend fun delete(id: String) {
        request("DELETE", id)
    }

    private inline fun <reified T> responseField(body: String, name: String): T =
        JsonCodec.decodeFromJsonElement(JsonCodec.parseToJsonElement(body).jsonObject.getValue(name))

    private suspend fun request(method: String, segment: String? = null, body: String? = null): String =
        withContext(owner) {
            val path = HttpUrl.Builder().scheme("https").host("example.com")
                .addPathSegments("api/v1/assistant/saved-prompts").apply { segment?.let(::addPathSegment) }
                .build().encodedPath
            connection.request(path, method, body?.toRequestBody("application/json".toMediaType()))
                .use { it.body?.string().orEmpty() }
        }
}
