package persona.core.contracts.api

import persona.core.contracts.CodexTakeoverInfo
import persona.core.contracts.Session
import persona.core.contracts.SessionKey

/** Hub preferences use machine/space-name keys, while renaming uses workspace IDs. */
data class AgentOperationPreferences(
    val spaces: List<String> = emptyList(),
    val sessions: List<String> = emptyList(),
    val allowed: Set<String> = emptySet(),
)

interface AgentOperations {
    val scope: String
    /** Pairing scope of the hub that owns [key]; merged multi-hub services resolve the specific hub's scope. */
    fun scopeFor(key: SessionKey): String = scope
    suspend fun pins(): AgentOperationPreferences
    suspend fun mutes(): AgentOperationPreferences
    suspend fun savePins(value: AgentOperationPreferences)
    suspend fun saveMutes(value: AgentOperationPreferences)
    suspend fun renameSession(key: SessionKey, title: String?, tabName: String?)
    suspend fun renameSpace(machineId: String, workspaceId: String, name: String)
    suspend fun closeSession(key: SessionKey)
    /** Join a Codex app-server thread live (POST …/attach); only for a session whose capabilities offer it. */
    suspend fun attachSession(key: SessionKey): Unit = throw UnsupportedOperationException("Live attach unavailable")
    /** Stop the agent's turn (POST …/interrupt, as the chat's Stop); only for a session that [persona.core.contracts.Session] lets interrupt. */
    suspend fun interruptSession(key: SessionKey): Unit = throw UnsupportedOperationException("Interrupt unavailable")
    /** "Delete conversation" (DELETE /sessions/{mid}/{sid}): out of every app's list, the transcript stays; only for [persona.core.contracts.Session.canDelete]. */
    suspend fun deleteSession(key: SessionKey): Unit = throw UnsupportedOperationException("Delete session unavailable")
    /** "Branch to new chat" (POST /sessions/{mid}/{sid}/fork with empty at_item_id): copies the session into a new one and returns its id. */
    suspend fun forkSession(key: SessionKey): String = throw UnsupportedOperationException("Fork session unavailable")
    /** Inspect whether a read-only Codex CLI session can be resumed or continued (`GET /api/v1/machines/{mid}/codex/{sid}/takeover`). */
    suspend fun codexTakeoverInfo(key: SessionKey): CodexTakeoverInfo =
        throw UnsupportedOperationException("Codex takeover unavailable")
    /** Explicitly take over a read-only Codex CLI session (`POST /api/v1/machines/{mid}/codex/{sid}/takeover`). */
    suspend fun codexTakeover(key: SessionKey, token: String, confirmStop: Boolean): Session =
        throw UnsupportedOperationException("Codex takeover unavailable")
    /** Start an independent Codex continuation session carrying the source transcript extract and linked to-do under [operationId]. */
    suspend fun startCodexContinuation(key: SessionKey, info: CodexTakeoverInfo, operationId: String, todoId: String? = null): AgentStartResult =
        throw UnsupportedOperationException("Codex continuation unavailable")
    /** Reconcile an unconfirmed Codex continuation start (`GET /api/v1/ops/{operationId}`). */
    suspend fun codexContinuationOperation(key: SessionKey, operationId: String): AgentStartResult =
        throw UnsupportedOperationException("Codex continuation receipt unavailable")
}

/** An ambiguous write must be verified before repeating a one-shot action. */
class AgentOperationFailure(val uncertain: Boolean, cause: Throwable) : Exception(cause)
