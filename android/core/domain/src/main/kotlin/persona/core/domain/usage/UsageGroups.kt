package persona.core.domain.usage

import persona.core.contracts.api.UsageAccount
import persona.core.contracts.api.UsageWindow
import persona.core.contracts.parseTime
import java.time.Instant
import kotlin.math.roundToInt

// Per-account subscription bars (#13173, docs/server/usage-windows.md; the web's lib/usage-windows.ts and iOS `UsageWindows` read the
// same rules): accounts grouped by account type, every account listed with its windows, and the geometry of the reset timeline.
// All of it is computed on the client from the numbers `GET /api/v1/usage/windows` already carries.

/** One account type's card: its accounts (the soonest weekly reset first) and the newest sample among them. */
data class UsageProviderGroup(val provider: String, val accounts: List<UsageAccount>, val newestSampledAt: Instant?)

/** The key an account without a provider name is grouped under. */
const val USAGE_OTHER_PROVIDER = "other"

private const val WEEK_MINUTES = 10080
private const val FIVE_HOUR_MINUTES = 300

/** The longest span the timeline draws: a week. */
const val TIMELINE_CAP_MILLIS = 7L * 24 * 3600 * 1000

/** An account's weekly plan window (a per-model allowance is not it), if it has one. */
fun weeklyWindow(a: UsageAccount): UsageWindow? = a.windows.firstOrNull { it.model.isNullOrBlank() && windowMinutes(it) == WEEK_MINUTES }

/**
 * The order inside a card: by the weekly window's reset, soonest first; an account with no weekly window, or one with no reset time,
 * after those, then by name (case-insensitive) and id so the order is stable.
 */
fun orderedByWeeklyReset(accounts: List<UsageAccount>): List<UsageAccount> {
    fun reset(a: UsageAccount): Instant? = weeklyWindow(a)?.let { parseTime(it.resetsAt) }
    return accounts.sortedWith(
        compareBy<UsageAccount> { reset(it) == null }
            .thenBy { reset(it) ?: Instant.MAX }
            .thenBy { it.name.lowercase() }
            .thenBy { it.id },
    )
}

/** One card per account type, ordered by the type's key; the accounts in each as [orderedByWeeklyReset]; no account is left out. */
fun groupByProvider(accounts: List<UsageAccount>): List<UsageProviderGroup> =
    accounts.groupBy { it.provider.trim().lowercase().ifEmpty { USAGE_OTHER_PROVIDER } }
        .toSortedMap()
        .map { (provider, list) ->
            UsageProviderGroup(provider, orderedByWeeklyReset(list), list.mapNotNull { parseTime(it.sampledAt) }.maxOrNull())
        }

/**
 * The windows as a card lists them: the plan's own first, the 5-hour window then the weekly one then the others by length, and a
 * per-model allowance after them in the same order.
 */
fun orderedWindows(a: UsageAccount): List<UsageWindow> {
    fun rank(w: UsageWindow): Long = when (val m = windowMinutes(w)) {
        FIVE_HOUR_MINUTES -> 0L
        WEEK_MINUTES -> 1L
        else -> 2L + m
    }
    val (plan, scoped) = a.windows.partition { it.model.isNullOrBlank() }
    return plan.sortedBy(::rank) + scoped.sortedBy(::rank)
}

/** The fraction of the window that has passed, 0 to 1; null with no reset time or length, or when [now] is outside the window. */
fun elapsedFraction(w: UsageWindow, now: Instant): Double? {
    val total = windowMinutes(w) * 60_000L
    val resets = parseTime(w.resetsAt) ?: return null
    if (total <= 0) return null
    val elapsed = total - (resets.toEpochMilli() - now.toEpochMilli())
    return if (elapsed < 0 || elapsed > total) null else elapsed.toDouble() / total
}

/** The hours the shared axis is labelled at, after "now". */
val RESET_CHART_TICK_HOURS = listOf(6, 12, 24, 72, 120, 168)

/** The first day takes this share of the axis' width; the other six days the rest. */
const val RESET_CHART_FIRST_DAY_SHARE = 0.45f

private const val DAY_HOURS = 24.0
private const val HOUR_MILLIS = 3_600_000.0

/**
 * Where a time [millis] after now falls on the shared axis, 0 to 1: the first 24 hours take [RESET_CHART_FIRST_DAY_SHARE] of the width
 * and the days after it, up to the week, the rest, each part linear; a later time is cut at the end.
 */
fun resetChartAxis(millis: Long): Float {
    val h = millis.coerceAtLeast(0) / HOUR_MILLIS
    val week = TIMELINE_CAP_MILLIS / HOUR_MILLIS
    return if (h <= DAY_HOURS) (h / DAY_HOURS * RESET_CHART_FIRST_DAY_SHARE).toFloat()
    else (RESET_CHART_FIRST_DAY_SHARE + (h - DAY_HOURS).coerceAtMost(week - DAY_HOURS) / (week - DAY_HOURS) * (1 - RESET_CHART_FIRST_DAY_SHARE)).toFloat()
}

/** One window's capsule on the chart: it runs from now to its reset, [resetsInMillis] away (uncut), ending at [endFraction] of the axis. */
data class ResetChartBar(val resetsInMillis: Long, val endFraction: Float)

/** An account's row on the chart: its 5-hour and its weekly capsule, whichever it has. */
data class ResetChartRow(val account: UsageAccount, val five: ResetChartBar?, val week: ResetChartBar?)

/**
 * One chart per card (#13173): a row per account, in the order given (the card's), each with the plan's 5-hour and weekly windows as
 * capsules from now to their resets on [resetChartAxis]. A window with no reset time, or one already past, has no capsule, and an
 * account with neither is left out. Null when no row is left.
 */
fun resetChartRows(accounts: List<UsageAccount>, now: Instant): List<ResetChartRow>? {
    fun bar(a: UsageAccount, minutes: Int): ResetChartBar? {
        val w = a.windows.firstOrNull { it.model.isNullOrBlank() && windowMinutes(it) == minutes } ?: return null
        val ms = (parseTime(w.resetsAt) ?: return null).toEpochMilli() - now.toEpochMilli()
        return if (ms <= 0) null else ResetChartBar(ms, resetChartAxis(ms))
    }
    return accounts.mapNotNull { a ->
        val five = bar(a, FIVE_HOUR_MINUTES)
        val week = bar(a, WEEK_MINUTES)
        if (five == null && week == null) null else ResetChartRow(a, five, week)
    }.ifEmpty { null }
}

/** How long until a reset, in the short form a capsule's label uses. */
sealed interface ChartDuration {
    data class Minutes(val m: Long) : ChartDuration
    /** [tenths] of an hour: 15 is an hour and a half. */
    data class Hours(val tenths: Long) : ChartDuration
    data class DaysHours(val d: Long, val h: Long) : ChartDuration
}

/** Under an hour minutes; under three hours in tenths (1.5h); under a day whole hours; then days and hours. */
fun chartDuration(millis: Long): ChartDuration {
    val minutes = kotlin.math.ceil(millis.coerceAtLeast(0) / 60_000.0).toLong()
    return when {
        minutes < 60 -> ChartDuration.Minutes(minutes.coerceAtLeast(1))
        minutes < 180 -> ChartDuration.Hours((minutes / 6.0).roundToInt().toLong())
        minutes < 24 * 60 -> ChartDuration.Hours((minutes / 60.0).roundToInt() * 10L)
        else -> (minutes / 60).let { ChartDuration.DaysHours(it / 24, it % 24) }
    }
}

/** A window's protection line, 0 to 100, when the hub says one; null when absent or not a percentage. */
fun protectLine(w: UsageWindow): Double? = w.protectPercent?.takeIf { it in 0.0..100.0 }
