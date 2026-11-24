package persona.core.hub

import persona.core.contracts.CodexTakeoverInfo
import persona.core.contracts.CodexTakeoverRequest
import persona.core.contracts.CodexTakeoverResponse
import persona.core.contracts.JsonCodec
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import persona.core.contracts.api.*
import kotlinx.coroutines.CancellationException
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.*
import okhttp3.HttpUrl.Companion.toHttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import java.util.UUID
import java.util.concurrent.TimeUnit

class HttpAgentOperations(
    override val scope: String,
    baseUrl: String,
    private val token: String,
    client: OkHttpClient,
    private val isCurrent: () -> Boolean = { true },
) : AgentOperations {
    private val base = baseUrl.toHttpUrl()
    private val http = client.newBuilder().followRedirects(false).followSslRedirects(false)
        .retryOnConnectionFailure(false).build()
    private val slowHttp = http.newBuilder().readTimeout(110, TimeUnit.SECONDS).build()

    private suspend fun request(method: String, segments: List<String>, body: JsonObject = buildJsonObject {}): JsonObject {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().apply { listOf("api", "v1").plus(segments).forEach(::addPathSegment) }.build()
        val request = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .method(method, if (method == "GET") null else body.toString().toRequestBody("application/json".toMediaType())).build()
        try {
            return http.newCall(request).awaitResponse().use { response ->
                if (!isCurrent()) throw CancellationException("Pairing changed")
                if (!response.isSuccessful) throw HubFailure.from(response)
                val text = response.body?.string().orEmpty()
                if (text.isBlank()) buildJsonObject {} else JsonCodec.parseToJsonElement(text).jsonObject
            }
        } catch (e: CancellationException) { throw e }
        catch (e: Exception) {
            val failure = e as? HubFailure
            throw AgentOperationFailure(method != "GET" && (failure?.status == null || failure.status >= 500 || failure.status == 408), e)
        }
    }

    private suspend fun preferences(kind: String): AgentOperationPreferences {
        val value = request("GET", listOf(kind))
        fun strings(key: String) = (value[key] as? JsonArray).orEmpty().map { it.jsonPrimitive.content }
        return AgentOperationPreferences(strings("spaces"), strings("sessions"), strings("allowed").toSet())
    }
    private suspend fun save(kind: String, value: AgentOperationPreferences) {
        request("PUT", listOf(kind), buildJsonObject {
            put("spaces", JsonArray(value.spaces.map(::JsonPrimitive)))
            put("sessions", JsonArray(value.sessions.map(::JsonPrimitive)))
            if (kind == "mutes") put("allowed", JsonArray(value.allowed.map(::JsonPrimitive)))
        })
    }
    override suspend fun pins() = preferences("pins")
    override suspend fun mutes() = preferences("mutes")
    override suspend fun savePins(value: AgentOperationPreferences) = save("pins", value)
    override suspend fun saveMutes(value: AgentOperationPreferences) = save("mutes", value)
    override suspend fun renameSession(key: SessionKey, title: String?, tabName: String?) {
        require(title?.isNotBlank() == true || tabName?.isNotBlank() == true)
        request("POST", listOf("sessions", key.machineId, key.sessionId, "rename"), buildJsonObject {
            title?.let { put("title", it) }; tabName?.let { put("tab_name", it) }
        })
    }
    override suspend fun renameSpace(machineId: String, workspaceId: String, name: String) {
        require(workspaceId.isNotBlank() && name.isNotBlank())
        request("POST", listOf("machines", machineId, "spaces", workspaceId, "rename"), buildJsonObject { put("name", name) })
    }
    override suspend fun closeSession(key: SessionKey) {
        request("POST", listOf("sessions", key.machineId, key.sessionId, "close"))
    }
    override suspend fun attachSession(key: SessionKey) {
        request("POST", listOf("sessions", key.machineId, key.sessionId, "attach"))
    }
    override suspend fun interruptSession(key: SessionKey) {
        request("POST", listOf("sessions", key.machineId, key.sessionId, "interrupt"))
    }
    override suspend fun deleteSession(key: SessionKey) {
        request("DELETE", listOf("sessions", key.machineId, key.sessionId))
    }
    override suspend fun forkSession(key: SessionKey): String =
        (request("POST", listOf("sessions", key.machineId, key.sessionId, "fork"), buildJsonObject { put("at_item_id", "") })["session_id"] as? JsonPrimitive)
            ?.contentOrNull?.takeIf(String::isNotBlank) ?: throw HubFailure(HubError.Protocol)

    private suspend fun directRequest(
        method: String,
        segments: List<String>,
        body: RequestBody? = null,
        slow: Boolean = false,
        operationId: String? = null,
    ): String {
        if (!isCurrent()) throw CancellationException("Pairing changed")
        val url = base.newBuilder().apply { listOf("api", "v1").plus(segments).forEach(::addPathSegment) }.build()
        val builder = Request.Builder().url(url).header("Authorization", "Bearer $token")
            .method(method, body)
        operationId?.let { id -> ProtocolCompatibility.operationHeaders(id).forEach { (name, value) -> builder.header(name, value) } }
        return (if (slow) slowHttp else http).newCall(builder.build()).awaitResponse().use { response ->
            if (!isCurrent()) throw CancellationException("Pairing changed")
            if (!response.isSuccessful) throw HubFailure.from(response)
            response.body?.string().orEmpty()
        }
    }

    override suspend fun codexTakeoverInfo(key: SessionKey): CodexTakeoverInfo {
        require(key.machineId.isNotBlank() && key.sessionId.isNotBlank())
        val text = directRequest("GET", listOf("machines", key.machineId, "codex", key.sessionId, "takeover"))
        return JsonCodec.decodeFromString<CodexTakeoverInfo>(text)
    }

    override suspend fun codexTakeover(key: SessionKey, token: String, confirmStop: Boolean): Session {
        require(key.machineId.isNotBlank() && key.sessionId.isNotBlank())
        val body = JsonCodec.encodeToString(CodexTakeoverRequest(token = token, confirmStop = confirmStop))
            .toRequestBody("application/json; charset=utf-8".toMediaType())
        val text = directRequest("POST", listOf("machines", key.machineId, "codex", key.sessionId, "takeover"), body, slow = true)
        val adopted = JsonCodec.decodeFromString<CodexTakeoverResponse>(text).session
        return if (adopted.machineId.isBlank()) adopted.copy(machineId = key.machineId) else adopted
    }

    override suspend fun startCodexContinuation(key: SessionKey, info: CodexTakeoverInfo, operationId: String, todoId: String?): AgentStartResult {
        require(key.machineId.isNotBlank() && info.cwd.isNotBlank())
        val body = CodexContinuationReceipt.startBody(key.machineId, info, todoId)
        return CodexContinuationReceipt.startWithReconciliation(
            machineId = key.machineId,
            operationId = operationId,
            post = {
                val text = directRequest("POST", listOf("sessions"), body, slow = true, operationId = operationId)
                JsonCodec.parseToJsonElement(text).jsonObject
            },
            fetchOperation = { opId -> codexContinuationOperation(key, opId) },
        )
    }

    override suspend fun codexContinuationOperation(key: SessionKey, operationId: String): AgentStartResult {
        UUID.fromString(operationId)
        val receipt = try {
            val text = directRequest("GET", listOf("ops", operationId))
            JsonCodec.parseToJsonElement(text).jsonObject
        } catch (failure: HubFailure) {
            CodexContinuationReceipt.operationFailure(failure)?.let { return it }
            throw failure
        }
        return CodexContinuationReceipt.receiptOutcome(receipt, key.machineId)
    }
}
