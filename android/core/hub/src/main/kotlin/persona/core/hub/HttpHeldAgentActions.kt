package persona.core.hub

import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.async
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.flowOf
import okhttp3.HttpUrl
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import persona.core.contracts.HeldAgentMessage
import persona.core.contracts.SessionKey
import persona.core.contracts.api.AgentMessageHoldFeature
import persona.core.contracts.api.HeldAgentActions
import java.io.IOException

/** Bound to one pairing generation and its plain session key, even in a multi-hub group. */
class HttpHeldAgentActions(
    private val session: SessionKey,
    override val features: StateFlow<Set<String>>,
    private val receipts: HeldAgentRetryReceipts,
    private val pairingScope: CoroutineScope,
    private val request: suspend (String, String, RequestBody?) -> Response,
    private val completed: suspend (String, Boolean) -> Unit,
) : HeldAgentActions {
    private fun eligible(row: HeldAgentMessage) = row.id.isNotBlank() && !row.computer.isNullOrBlank() &&
        row.state == "failed" && !row.deleteOnly

    override fun canRetry(row: HeldAgentMessage) = if (eligible(row)) receipts.canRetry(session, row) else flowOf(false)

    override suspend fun retry(row: HeldAgentMessage) = pairingScope.async {
        require(AgentMessageHoldFeature in features.value && eligible(row))
        if (!receipts.claim(session, row)) return@async
        val present = try {
            act(row.id, retry = true)
        } catch (e: CancellationException) {
            // If the pairing closes after dispatch, the outcome may already be committed.
            throw e
        } catch (e: Exception) {
            val failure = HubFailure.from(e)
            if (e is IOException && failure.status == null && failure.kind in setOf(HubError.Timeout, HubError.Transport)) {
                // The manager may already have checked. Refresh its row before the owner makes another decision.
                completed(row.id, true)
            } else {
                receipts.release(session, row)
            }
            throw e
        }
        completed(row.id, present)
    }.await()

    override suspend fun cancel(id: String) {
        act(id, retry = false)
        completed(id, false)
    }

    private suspend fun act(id: String, retry: Boolean): Boolean {
        require(id.isNotBlank() && AgentMessageHoldFeature in features.value)
        val path = HttpUrl.Builder().scheme("https").host("example.com").addPathSegments("api/v1/sessions")
            .addPathSegment(session.machineId).addPathSegment(session.sessionId).addPathSegment("held").addPathSegment(id)
            .apply { if (retry) addPathSegment("retry") }.build().encodedPath
        try {
            request(path, if (retry) "POST" else "DELETE",
                if (retry) "{}".toRequestBody("application/json".toMediaType()) else null).use {
                if (!it.isSuccessful) throw HubFailure.from(it)
            }
        } catch (failure: HubFailure) {
            if (failure.status != 404) throw failure
            return false
        }
        return true
    }
}
