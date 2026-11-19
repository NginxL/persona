package persona.feature.assistant

import persona.core.contracts.api.UsageDay
import persona.core.contracts.api.UsageDays
import persona.core.contracts.api.UsageHours
import persona.core.designsystem.AgentMarkSpec
import persona.core.designsystem.knownAgentMark
import java.time.LocalDate

/** Usage has only the runtime id. An empty id has its own historical label and no mark. */
internal fun usageAgentMark(id: String): AgentMarkSpec? {
    val name = when (id.lowercase()) {
        "" -> return null
        "claude" -> "Claude Code"
        "codex" -> "Codex"
        "trae" -> "TRAE"
        "traex" -> "TRAEX"
        "omp" -> "Oh My Pi"
        "gemini" -> "Gemini CLI"
        "cursor" -> "Cursor"
        else -> id
    }
    return AgentMarkSpec(knownAgentMark(id), name, surface = null)
}

internal fun parseUsageDate(raw: String): LocalDate? = runCatching { LocalDate.parse(raw) }.getOrNull()

internal fun UsageDays.validDates(): UsageDays {
    val valid = days.filter { parseUsageDate(it.date) != null }
    return if (valid.size == days.size) this else copy(days = valid)
}

internal fun UsageHours.validDates(): UsageHours {
    val valid = hours.filter { parseUsageDate(it.date) != null }
    return if (valid.size == hours.size) this else copy(hours = valid)
}

/** Four quantile steps among the values with usage; ties share their upper rank and no usage is step zero. */
internal fun usageHeatSteps(values: List<Long>): List<Int> {
    val used = values.filter { it > 0 }.sorted()
    return values.map { value ->
        if (value <= 0) 0
        else (4 * used.count { it <= value } + used.size - 1) / used.size
    }
}

/** Complete Monday-first weeks; valid entries stay on their real dates and missing buckets stay empty. */
internal fun usageCalendarWeeks(days: List<UsageDay>): List<List<UsageDay?>> {
    val weeks = linkedMapOf<LocalDate, MutableList<UsageDay?>>()
    for (day in days) {
        val date = parseUsageDate(day.date) ?: continue
        val offset = date.dayOfWeek.value - 1
        val monday = date.minusDays(offset.toLong())
        weeks.getOrPut(monday) { MutableList(7) { null } }[offset] = day
    }
    return weeks.entries.sortedBy { it.key }.map { it.value }
}
