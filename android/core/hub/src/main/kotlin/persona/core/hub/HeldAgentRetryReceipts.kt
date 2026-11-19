package persona.core.hub

import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.catch
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import kotlinx.serialization.encodeToString
import okio.ByteString.Companion.encodeUtf8
import persona.core.contracts.HeldAgentMessage
import persona.core.contracts.JsonCodec
import persona.core.contracts.Session
import persona.core.contracts.SessionKey
import java.io.IOException

/** One pairing's accepted decisions. Only opaque fingerprints go in its private preferences. */
class HeldAgentRetryReceipts(
    private val records: Flow<Set<String>>,
    private val edit: suspend ((Set<String>) -> Set<String>) -> Unit,
) {
    private fun fingerprint(vararg fields: String?): String =
        JsonCodec.encodeToString(fields.toList()).encodeUtf8().sha256().hex()

    private fun sessionPrefix(key: SessionKey) = fingerprint(key.machineId, key.sessionId) + "/"
    private fun operation(key: SessionKey, id: String) = sessionPrefix(key) + fingerprint(id) + "/"

    // Unclear and unconfirmed are both "failed" at the original send time. The manager changes the status line.
    // A newer transcript rev, computer name or message preview does not authorize another POST.
    private fun decision(key: SessionKey, row: HeldAgentMessage) = operation(key, row.id) +
        fingerprint(row.at, row.state, row.error, row.deleteOnly.toString())

    fun canRetry(key: SessionKey, row: HeldAgentMessage): Flow<Boolean> {
        val stamp = decision(key, row)
        return records.map { stamp !in it }.distinctUntilChanged().catch { error ->
            if (error is IOException) emit(false) else throw error
        }
    }

    internal suspend fun claim(key: SessionKey, row: HeldAgentMessage): Boolean {
        val stamp = decision(key, row)
        var acquired = false
        edit { before ->
            acquired = stamp !in before
            if (acquired) before - "$stamp?" + stamp else before
        }
        return acquired
    }

    internal suspend fun release(key: SessionKey, row: HeldAgentMessage) {
        val stamp = decision(key, row)
        edit { it - stamp - "$stamp?" }
    }

    /** A cached paint never rearms an action. Only an observed manager waiting → failed cycle can repeat a decision. */
    suspend fun reconcile(previous: Session, session: Session) {
        if (previous.assistant || session.assistant || (session.waiting != null && session.undelivered == null)) return
        val rows = session.undelivered.orEmpty().associateBy { it.id }
        edit { saved ->
            if (saved.isEmpty()) return@edit saved
            saved.toMutableSet().apply {
                for (before in previous.undelivered.orEmpty()) {
                    if (before.computer.isNullOrBlank()) continue
                    val row = rows[before.id]
                    if (row != null && row.computer.isNullOrBlank()) continue
                    if (row == null) {
                        val prefix = operation(session.key, before.id)
                        removeAll { it.startsWith(prefix) }
                    } else if (before.state == "failed" && row.state == "waiting") {
                        // Only the previous row predates this wait; another screen may have accepted a newer decision.
                        val prior = decision(session.key, before)
                        if (prior in this) add("$prior?")
                    } else if (before.state == "waiting" && row.state == "failed") {
                        val next = decision(session.key, row)
                        if ("$next?" in this) remove(next)
                        val prefix = operation(session.key, before.id)
                        removeAll { it.startsWith(prefix) && it.endsWith('?') }
                    }
                }
            }
        }
    }
}
