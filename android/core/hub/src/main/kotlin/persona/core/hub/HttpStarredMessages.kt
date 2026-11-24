package persona.core.hub

import kotlinx.coroutines.withContext
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.JsonNull
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.jsonObject
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody
import persona.core.contracts.Item
import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.RequestManager
import persona.core.contracts.api.StarredIndex
import persona.core.contracts.api.StarredList
import persona.core.contracts.api.StarredMessagesApi
import persona.core.contracts.api.StarredSnapshot

/** Follows the paired socket's route, explicitly naming this assistant even if the switcher changes. */
class HttpStarredMessages(private val connection: OkHttpHubConnection, manager: String?) : StarredMessagesApi {
    private val owner = RequestManager(manager.orEmpty())

    override suspend fun index(): StarredIndex? = try {
        JsonCodec.decodeFromString<StarredIndex>(request("GET", query = "index" to "1"))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun list(): StarredList? = try {
        JsonCodec.decodeFromString<StarredList>(request("GET"))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun add(star: StarredSnapshot): StarredSnapshot {
        val body = JsonCodec.encodeToString(star.bounded())
        val answer = request("POST", body = body)
        return responseField(answer, "star")
    }

    override suspend fun remove(id: String): StarredSnapshot? {
        val answer = request("DELETE", segment = id)
        val obj = JsonCodec.parseToJsonElement(answer).jsonObject
        val removed = obj["removed"] ?: return null
        if (removed is JsonNull) return null
        return JsonCodec.decodeFromJsonElement(removed)
    }

    override suspend fun fullItem(session: SessionKey, itemId: String): Item = withContext(owner) {
        val path = HttpUrl.Builder().scheme("https").host("example.com")
            .addPathSegments("api/v1/sessions")
            .addPathSegment(session.machineId)
            .addPathSegment(session.sessionId)
            .addPathSegment("items")
            .addPathSegment(itemId)
            .addPathSegment("full")
            .build().encodedPath
        val body = connection.request(path, "GET", null).use { it.body?.string().orEmpty() }
        JsonCodec.decodeFromString(body)
    }

    private inline fun <reified T> responseField(body: String, name: String): T =
        JsonCodec.decodeFromJsonElement(JsonCodec.parseToJsonElement(body).jsonObject.getValue(name))

    private suspend fun request(
        method: String,
        segment: String? = null,
        query: Pair<String, String>? = null,
        body: String? = null,
    ): String = withContext(owner) {
        val url = HttpUrl.Builder().scheme("https").host("example.com")
            .addPathSegments("api/v1/assistant/starred")
            .apply {
                segment?.let(::addPathSegment)
                query?.let { (k, v) -> addQueryParameter(k, v) }
            }
            .build()
        val pathWithQuery = url.encodedPath + (url.encodedQuery?.let { "?$it" } ?: "")
        connection.request(pathWithQuery, method, body?.toRequestBody("application/json".toMediaType()))
            .use { it.body?.string().orEmpty() }
    }
}
