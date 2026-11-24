package persona.core.hub

import kotlinx.coroutines.CancellationException
import kotlinx.serialization.encodeToString
import kotlinx.serialization.json.*
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import persona.core.contracts.CodexTakeoverInfo
import persona.core.contracts.CoreText
import persona.core.contracts.CoreText.Key
import persona.core.contracts.JsonCodec
import persona.core.contracts.ProtocolCompatibility
import persona.core.contracts.Session
import persona.core.contracts.api.AgentStartRequest
import persona.core.contracts.api.AgentStartResult
import java.util.Base64
import java.util.UUID

/**
 * Shared operation-receipt helpers for Codex continuation starts (`POST /api/v1/sessions` + `GET /api/v1/ops/{id}`,
 * docs/protocol/protocol.md "Operation receipts (client_ops)"). Both `HttpChatActions` and `HttpAgentOperations` follow the exact
 * same receipt rules so lost responses reconcile against `/ops/{id}` rather than starting a duplicate agent.
 */
internal object CodexContinuationReceipt {
    private val DefiniteNonStartStatuses = setOf(400, 401, 403, 404, 422)

    fun startBody(
        machineId: String,
        info: CodexTakeoverInfo,
        todoId: String?,
    ): RequestBody {
        val payload = AgentStartRequest(
            machineId = machineId,
            cwd = info.cwd,
            launcher = "codex",
            prompt = info.continuationPrompt,
            agentType = "codex",
            async = false,
            todoId = todoId,
        )
        return compatibleRequestBody(
            "/api/v1/sessions",
            JsonCodec.encodeToString(payload).toRequestBody("application/json; charset=utf-8".toMediaType()),
        )!!
    }

    fun startHeaders(operationId: String): Map<String, String> {
        UUID.fromString(operationId)
        return ProtocolCompatibility.operationHeaders(operationId)
    }

    fun outcome(body: JsonObject, fallbackMachineId: String): AgentStartResult {
        val session = body["session"] as? JsonObject
        if (session != null) {
            val decoded = JsonCodec.decodeFromJsonElement<Session>(session)
            val resolved = if (decoded.machineId.isBlank()) decoded.copy(machineId = fallbackMachineId) else decoded
            if (resolved.machineId.isNotBlank() && resolved.sessionId.isNotBlank()) return AgentStartResult.Created(resolved)
        }
        return if (body["state"]?.jsonPrimitive?.contentOrNull == "pending") AgentStartResult.Pending else AgentStartResult.Unknown
    }

    fun receiptOutcome(receipt: JsonObject, fallbackMachineId: String): AgentStartResult {
        val state = receipt["state"]?.jsonPrimitive?.contentOrNull
        if (receipt["kind"]?.jsonPrimitive?.contentOrNull?.let { it != "start" } == true) return AgentStartResult.Unknown
        if (state == "pending") return AgentStartResult.Pending
        // Only terminal "done" or hub-confirmed "failed" resolve the receipt; "unknown" (including mid-call bus drop /
        // 503 assistant_offline per docs/protocol/protocol.md) must keep the receipt so the client never double-starts.
        if (state == "unknown") return AgentStartResult.Unknown
        val result = receipt["result"] as? JsonObject ?: return AgentStartResult.Unknown
        val encoded = result["body"]?.jsonPrimitive?.contentOrNull ?: return AgentStartResult.Unknown
        val body = runCatching {
            JsonCodec.parseToJsonElement(String(Base64.getDecoder().decode(encoded), Charsets.UTF_8)).jsonObject
        }.getOrNull() ?: return AgentStartResult.Unknown
        return when (state) {
            "done" -> outcome(body, fallbackMachineId)
            "failed" -> AgentStartResult.Rejected(rejectionText(body["error"]?.jsonPrimitive?.contentOrNull))
            else -> AgentStartResult.Unknown
        }
    }

    /** Maps `GET /api/v1/ops/{id}` HTTP failures: 404 means no receipt is recorded yet (Unknown); transport errors throw. */
    fun operationFailure(failure: HubFailure): AgentStartResult? =
        if (failure.status == 404) AgentStartResult.Unknown else null

    suspend fun startWithReconciliation(
        machineId: String,
        operationId: String,
        post: suspend (headers: Map<String, String>) -> JsonObject,
        fetchOperation: suspend (operationId: String) -> AgentStartResult,
    ): AgentStartResult {
        val headers = startHeaders(operationId)
        var definiteRefusal: HubFailure? = null
        val direct = try {
            outcome(post(headers), machineId)
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (failure: HubFailure) {
            if (isDefiniteNonStart(failure)) definiteRefusal = failure
            AgentStartResult.Unknown
        } catch (_: Exception) {
            AgentStartResult.Unknown
        }
        if (direct != AgentStartResult.Unknown) return direct
        val reconciled = try {
            fetchOperation(operationId)
        } catch (cancelled: CancellationException) {
            throw cancelled
        } catch (_: Exception) {
            AgentStartResult.Unknown
        }
        if (reconciled != AgentStartResult.Unknown) return reconciled
        val refusal = definiteRefusal ?: return AgentStartResult.Unknown
        return AgentStartResult.Rejected(rejectionText(refusal.message))
    }

    private fun rejectionText(raw: String?): CoreText =
        raw?.takeIf { it.isNotBlank() && !it.startsWith("HTTP ") }?.let(CoreText::plain) ?: CoreText.of(Key.StartRejected)

    private fun isDefiniteNonStart(failure: HubFailure): Boolean {
        // Per docs/protocol/protocol.md, 503 assistant_offline can follow a mid-call bus drop after the manager
        // forwarded session.start, so only explicit delivered="none" or terminal client/conflict refusals clear it.
        if (failure.delivered == "none") return true
        if (failure.status == 409 && failure.code != "op_id_reused") return true
        return failure.status in DefiniteNonStartStatuses && failure.delivered != "unknown"
    }
}
