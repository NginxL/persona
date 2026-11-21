package persona.core.domain.lines

import persona.core.contracts.EngineLeg
import persona.core.contracts.Item
import persona.core.contracts.SessionKey
import persona.core.contracts.parseTime
import java.time.Instant

/** A terminal session can be reused after switching back. Its items then belong to several time windows. */
data class EngineHistoryWindow(
    val index: Int,
    val session: SessionKey,
    val after: Instant?,
    val repeated: Boolean,
)

object EngineHistoryWindows {
    /** Earlier windows in reading order, followed by the current window. Invalid cutoffs keep the ordinary transcript. */
    fun of(current: SessionKey, history: List<EngineLeg>): List<EngineHistoryWindow> {
        if (history.isEmpty()) return emptyList()
        val begins = listOf<Instant?>(null) + history.map { parseTime(it.until) ?: return emptyList() }
        if (begins.drop(1).filterNotNull().zipWithNext().any { (earlier, later) -> earlier > later }) return emptyList()
        val sessions = history.map { leg ->
            if (leg.sessionId.isBlank()) return emptyList()
            current.copy(sessionId = leg.sessionId)
        } + current
        val counts = sessions.groupingBy { it }.eachCount()
        return sessions.mapIndexed { index, session ->
            EngineHistoryWindow(index, session, begins[index], counts.getValue(session) > 1)
        }
    }

    /**
     * A one-time session keeps its own transcript even if its clock differs. A reused session splits at the strictly
     * exclusive start of each later window. Untimed entries follow the previous timestamp, or the first timed entry.
     */
    fun split(items: List<Item>, windows: List<EngineHistoryWindow>, session: SessionKey): Map<Int, List<Item>> {
        val own = windows.filter { it.session == session }
        if (own.isEmpty()) return emptyMap()
        if (own.size == 1) return mapOf(own.first().index to items)
        val timestamps = items.map { parseTime(it.ts) }
        var time = timestamps.firstNotNullOfOrNull { it }
        val split = own.associate { it.index to mutableListOf<Item>() }
        items.forEachIndexed { index, item ->
            time = timestamps[index] ?: time
            val at = time
            val home = if (at == null) own.first() else own.lastOrNull { it.after == null || at > it.after } ?: own.first()
            split.getValue(home.index) += item
        }
        return split
    }

    /** Later runs of a reused session can be complete while an earlier run still has older pages. */
    fun complete(window: EngineHistoryWindow, windows: List<EngineHistoryWindow>, items: List<Item>, hasOlder: Boolean): Boolean {
        if (!hasOlder) return true
        val after = window.after ?: return false
        if (windows.firstOrNull { it.session == window.session } == window) return false
        return items.firstNotNullOfOrNull { parseTime(it.ts) }?.let { it <= after } == true
    }
}
