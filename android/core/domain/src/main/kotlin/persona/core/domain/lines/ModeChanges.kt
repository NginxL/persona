package persona.core.domain.lines

import java.time.Instant
import persona.core.contracts.ModeChange
import persona.core.contracts.parseTime

/** Permission mode records join the visible transcript at their time; they never become wire items (#10297). */
object ModeChanges {
    private val kinds = setOf("set", "restart_done", "restart_failed", "restart_dropped")

    private data class Timed(val change: ModeChange, val at: Instant, val order: Int)

    /** Only a user's next input places a change: work and its answer stay in the exchange that was already running. */
    private fun inputTime(line: ChatLine): Instant? = when (line) {
        is ChatLine.Row -> line.row.takeIf { it.isUserInput }?.start
        is ChatLine.UserImages -> line.images.firstOrNull()?.time
        else -> null
    }

    /**
     * Put each valid change before the first later exchange, or after the last. While older history remains, [earliest]
     * holds changes from before its first loaded item for that page. IDs survive additions and removals elsewhere in the
     * record; an empty chat keeps its placeholder. A frozen view's [until] is the last shown item's time: newer changes
     * wait with the rest of the transcript.
     */
    fun merge(lines: List<ChatLine>, changes: List<ModeChange>, earliest: Instant? = null, until: Instant? = null): List<ChatLine> {
        if (changes.isEmpty()) return lines
        val shown = changes.mapIndexedNotNull { index, change ->
            val at = parseTime(change.at)
                ?.takeIf { earliest == null || !it.isBefore(earliest) }
                ?.takeIf { until == null || !it.isAfter(until) }
            if (change.kind !in kinds || at == null) null else Timed(change, at, index)
        }.sortedWith(compareBy<Timed> { it.at }.thenBy { it.order })
        if (lines.isEmpty() || shown.isEmpty()) return lines

        val counts = mutableMapOf<String, Int>()
        val keyed = shown.map { timed ->
            val base = "mode-${timed.at.toEpochMilli()}-${timed.change.kind}-${timed.change.to.orEmpty()}"
            val number = counts[base] ?: 0
            counts[base] = number + 1
            val id = if (number == 0) base else "$base-$number"
            ChatLine.ModeChange(id, timed.change)
        }
        val out = ArrayList<ChatLine>(lines.size + keyed.size)
        var next = 0
        for (line in lines) {
            inputTime(line)?.let { at ->
                while (next < shown.size && shown[next].at < at) out += keyed[next++]
            }
            out += line
        }
        while (next < keyed.size) out += keyed[next++]
        return out
    }
}
