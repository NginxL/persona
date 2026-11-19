package persona.feature.chat

import persona.core.contracts.Session
import persona.core.contracts.SessionInfo
import persona.core.contracts.SessionKey

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
        launcher = info?.launcher ?: held?.launcher,
        permissionMode = info?.permissionMode ?: held?.permissionMode,
        effort = info?.effort ?: held?.effort,
        model = row.model ?: info?.model ?: held?.model,
        efforts = held?.efforts.orEmpty(), ports = held?.ports, queued = held?.queued,
        background = held?.background, subagents = held?.subagents, workflows = held?.workflows,
        undelivered = row.undelivered ?: if (read != null) read.undelivered else held?.undelivered,
    )
}
