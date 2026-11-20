package persona.core.hub

import persona.core.contracts.JsonCodec
import persona.core.contracts.SessionKey
import persona.core.contracts.api.HubPreferenceSet
import persona.core.contracts.api.HubPreferencesTransport
import persona.core.contracts.api.AgentDisplayApi
import persona.core.contracts.api.AgentsDisplaySettings
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.encodeToString
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody.Companion.toRequestBody

/**
 * Uses the existing GET/PUT preference contract and POST seen acknowledgement.
 * Requests follow the live connection's selected route and credentials, so HTTP 401 revokes it.
 */
class HttpHubPreferencesTransport(
    private val connection: OkHttpHubConnection,
    private val isCurrent: () -> Boolean = { true },
) : HubPreferencesTransport, AgentDisplayApi {
    private suspend fun request(method: String, segments: List<String>, body: String? = null): String {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val path = HttpUrl.Builder().scheme("https").host("example.com").apply {
            addPathSegment("api"); addPathSegment("v1"); segments.forEach(::addPathSegment)
        }.build().encodedPath
        return connection.request(path, method, body?.toRequestBody("application/json".toMediaType())).use {
            if (!isCurrent()) throw CancellationException("Pairing changed")
            it.body?.string().orEmpty()
        }
    }
    override suspend fun pins() = JsonCodec.decodeFromString<HubPreferenceSet>(request("GET", listOf("pins")))
    override suspend fun mutes() = JsonCodec.decodeFromString<HubPreferenceSet>(request("GET", listOf("mutes")))
    override suspend fun putPins(value: HubPreferenceSet) { request("PUT", listOf("pins"), JsonCodec.encodeToString(value)) }
    override suspend fun putMutes(value: HubPreferenceSet) { request("PUT", listOf("mutes"), JsonCodec.encodeToString(value)) }
    override suspend fun seen(session: SessionKey) {
        request("POST", listOf("sessions", session.machineId, session.sessionId, "seen"), "{}")
    }
    override suspend fun read() = JsonCodec.decodeFromString<AgentsDisplaySettings>(request("GET", displayPath))
    override suspend fun migrateHidden() = JsonCodec.decodeFromString<AgentsDisplaySettings>(
        request("PUT", displayPath, """{"show_botmux":false,"only_if_unset":true}"""))

    private companion object { val displayPath = listOf("agents", "display-settings") }
}
