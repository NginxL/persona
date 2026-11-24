package persona.core.inbox

import persona.core.contracts.Session
import persona.core.domain.sort.canSend
import java.time.Instant

/** An agent "Hand to AI" can send to: the most recently active live ones, never Persona itself (iOS: eight). */
data class HandOffTarget(val machineId: String, val sessionId: String, val title: String, val machine: String) {
    val key: String get() = "$machineId/$sessionId"
}

/**
 * Live, non-assistant sessions, newest first, at most [limit]. Sessions that cannot take a message (read-only ones
 * from other runtimes) drop out once sessions say so; until then being live is the test.
 */
fun handOffTargets(sessions: List<Session>, machines: Map<String, String> = emptyMap(), limit: Int = 8): List<HandOffTarget> =
    // Only agents that take messages: a read-only session (Codex history, an attached source without send) is left out.
    sessions.filter { !it.assistant && it.isLive && it.canSend }
        .sortedWith(compareByDescending<Session> { it.updated ?: Instant.EPOCH }.thenBy { it.key.path })
        .take(limit)
        .map { HandOffTarget(it.machineId, it.sessionId, it.displayTitle, machines[it.machineId]?.takeIf { name -> name.isNotBlank() } ?: it.machineId) }
