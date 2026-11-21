package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.api.*
import kotlinx.serialization.json.JsonObject
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response

/** Settings › Model configuration over the pairing's connection (docs/assistant/model-config.md "API"); 404 and 409 mean different things here. */
class HttpModelConfiguration internal constructor(
    private val request: suspend (String, String, RequestBody?, Long?) -> Response,
) : ModelConfiguration {
    constructor(connection: OkHttpHubConnection) : this({ path, method, body, timeout ->
        connection.request(path, method, body, readTimeoutSeconds = timeout)
    })

    override suspend fun catalog(): ModelCatalog = try {
        JsonCodec.decodeFromString(call(path(), "GET"))
    } catch (failure: HubFailure) {
        throw if (failure.status == 404) ModelConfigurationUnsupported() else failure
    }

    override suspend fun purpose(id: String): ModelPurposeDetail = try {
        JsonCodec.decodeFromString(call(path(id), "GET"))
    } catch (failure: HubFailure) {
        throw missing(failure)
    }

    override suspend fun save(purpose: ModelPurpose, draft: ModelConfigurationDraft): ModelPurposeSave = try {
        ModelPurposeSave.Saved(JsonCodec.decodeFromString(call(path(purpose.id), "PATCH", draft.patch(purpose))))
    } catch (failure: HubFailure) {
        if (failure.status == 409) ModelPurposeSave.Conflict else throw missing(failure)
    }

    // The hub gives the machine 70 s to start the launcher's CLI and answer (it keeps the list 10 minutes after).
    override suspend fun providerModels(provider: String, refresh: Boolean, machine: String?): ProviderModels? = try {
        val url = HttpUrl.Builder().scheme("https").host("example.com").addPathSegments("api/v1/model-config/providers")
            .addPathSegment(provider).addPathSegment("models").apply {
                if (refresh) addQueryParameter("refresh", "1")
                if (!machine.isNullOrBlank()) addQueryParameter("machine", machine)
            }.build()
        JsonCodec.decodeFromString(call(url.encodedPath + (url.encodedQuery?.let { "?$it" } ?: ""), "GET", timeout = 75))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun profiles(): ModelProfilesView? = try {
        JsonCodec.decodeFromString(call(profilePath(), "GET"))
    } catch (failure: HubFailure) {
        if (failure.status == 404) null else throw failure
    }

    override suspend fun applyProfile(id: String): ModelProfilesView =
        JsonCodec.decodeFromString(call(profilePath(id, "apply"), "POST", emptyBody = true))

    override suspend fun rollbackProfile(): ModelProfilesView =
        JsonCodec.decodeFromString(call(profilePath("rollback"), "POST", emptyBody = true))

    override suspend fun saveProfile(id: String, input: ModelProfileSaveInput): ModelProfileSave = try {
        ModelProfileSave.Saved(JsonCodec.decodeFromString(call(profilePath(id), "PUT", input.toJson())))
    } catch (failure: HubFailure) {
        if (failure.status == 409) ModelProfileSave.Conflict else throw failure
    }

    /** A hub without model configuration answers 404 from its router, without the JSON error of a missing purpose. */
    private fun missing(failure: HubFailure): Exception = when {
        failure.status != 404 -> failure
        failure.code == null && failure.message.startsWith("HTTP ") -> ModelConfigurationUnsupported()
        else -> ModelPurposeNotFound(failure.message)
    }

    private suspend fun call(path: String, method: String, body: JsonObject? = null, timeout: Long? = null, emptyBody: Boolean = false): String =
        request(path, method, body?.toString()?.toRequestBody(json) ?: if (emptyBody) ByteArray(0).toRequestBody(null) else null, timeout).use {
            if (!it.isSuccessful) throw HubFailure.from(it)
            it.body?.string().orEmpty()
        }

    private fun path(vararg purpose: String) = HttpUrl.Builder().scheme("https").host("example.com")
        .addPathSegments("api/v1/model-config").apply {
            purpose.forEach { addPathSegment("purposes").addPathSegment(it) }
        }.build().encodedPath

    private fun profilePath(vararg segments: String) = HttpUrl.Builder().scheme("https").host("example.com")
        .addPathSegments("api/v1/model-profiles").apply {
            segments.forEach { addPathSegment(it) }
        }.build().encodedPath

    private companion object {
        val json = "application/json; charset=utf-8".toMediaType()
    }
}
