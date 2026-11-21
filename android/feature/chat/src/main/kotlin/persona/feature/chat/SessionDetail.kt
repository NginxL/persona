package persona.feature.chat

import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.setValue

import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey
import persona.core.contracts.api.ChatActions

/** A history seed is display-only and must never cross a conversation or enter the live metadata cache. */
internal fun readOnlySessionMetadata(key: SessionKey, historyReadOnly: Boolean, seed: Session?): Session? =
    seed?.takeIf { historyReadOnly && it.key == key }

/**
 * A row summary updates live state, never erases detail already loaded by an open chat. [complete] is the session's
 * complete row read for this summary (`sessions_lookup`): what neither the summary nor `info` carries, the launcher
 * switch waiting for the next message (#5055); without one read, the one the chat held stays.
 */
internal fun sessionWithDetail(row: Session, previous: Session?, info: SessionInfo?, complete: Session? = null): Session {
    if (row.waiting == null) return row
    val held = previous?.takeIf { it.key == row.key }
    val read = complete?.takeIf { it.key == row.key }
    return row.copy(
        awaitingFirstMessage = row.awaitingFirstMessage || (read?.awaitingFirstMessage ?: held?.awaitingFirstMessage) == true,
        pendingSwitch = if (read != null) read.pendingSwitch else held?.pendingSwitch,
        launcher = read?.launcher ?: info?.launcher ?: held?.launcher,
        permissionMode = info?.permissionMode ?: held?.permissionMode,
        effort = info?.effort ?: held?.effort,
        model = row.model ?: info?.model ?: held?.model,
        efforts = held?.efforts.orEmpty(), ports = held?.ports, queued = held?.queued,
        background = held?.background, subagents = held?.subagents, workflows = held?.workflows,
        undelivered = row.undelivered ?: if (read != null) read.undelivered else held?.undelivered,
    )
}

/** A whole row can confirm an absent switch even when both its content and transcript revision are unchanged. */
internal class SessionDetailObservations(private val key: SessionKey) {
    var completeRow by mutableStateOf<Session?>(null)
        private set
    var switch by mutableStateOf<SwitchObservation?>(null)
        private set
    private var request = 0L

    private fun acceptSwitch(row: Session) {
        switch = SwitchObservation(row, (switch?.version ?: 0L) + 1L)
    }

    fun pushed(row: Session) {
        if (row.key != key || row.waiting != null) return
        // A push arriving after an HTTP read started takes precedence over that read.
        request++
        acceptSwitch(row)
    }

    suspend fun read(row: Session, actions: ChatActions, trackSwitch: Boolean) {
        val ticket = ++request
        val complete = actions.completeRow(row) ?: return
        if (request != ticket || complete.key != key || complete.waiting != null) return
        completeRow = complete
        if (trackSwitch) acceptSwitch(complete)
    }
}

internal data class SwitchObservation(val row: Session, val version: Long) {
    fun over(session: Session): Session =
        if (row.key == session.key && row.pendingSwitch != session.pendingSwitch) session.copy(pendingSwitch = row.pendingSwitch) else session
}
