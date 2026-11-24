package persona.core.domain.usage

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.api.UsageAccount
import persona.core.contracts.api.UsageWindow
import java.time.Instant

/** Per-account bars (#13173): grouping by account type, the order inside a card, the windows' order and the timeline's geometry. */
class UsageGroupsTest {
    private val now = Instant.parse("2026-11-11T12:00:00Z")
    private fun at(hours: Long) = now.plusSeconds(hours * 3600).toString()
    private fun win(label: String, minutes: Int, used: Double, resetHours: Long?, model: String? = null) =
        UsageWindow(label, minutes, used, resetsAt = resetHours?.let(::at), model = model)
    private fun account(id: String, provider: String = "claude", name: String = id, sampled: String = now.minusSeconds(60).toString(),
                        windows: List<UsageWindow> = emptyList()) = UsageAccount(id, provider, name, windows = windows, sampledAt = sampled)

    @Test fun everyAccountIsInOneCardPerType() {
        val groups = groupByProvider(listOf(
            account("c1", "codex"), account("a1", "claude"), account("x1", "xai"), account("a2", "Claude"), account("o1", ""),
        ))
        assertEquals(listOf("claude", "codex", USAGE_OTHER_PROVIDER, "xai"), groups.map { it.provider })
        assertEquals(listOf("a1", "a2"), groups.first().accounts.map { it.id })
        assertEquals("no account is dropped", 5, groups.sumOf { it.accounts.size })
    }

    @Test fun aCardCarriesTheNewestSample() {
        val newest = now.minusSeconds(10)
        val g = groupByProvider(listOf(account("a", sampled = now.minusSeconds(900).toString()), account("b", sampled = newest.toString()),
            account("c", sampled = "")))
        assertEquals(newest, g.single().newestSampledAt)
        assertNull(groupByProvider(listOf(account("c", sampled = ""))).single().newestSampledAt)
    }

    @Test fun accountsAreOrderedByTheWeeklyResetSoonestFirst() {
        fun week(id: String, resetHours: Long?, name: String = id, extra: List<UsageWindow> = emptyList()) =
            account(id, name = name, windows = listOfNotNull(win("5h", 300, 10.0, 2), resetHours?.let { win("7d", 10080, 30.0, it) }) + extra)
        val list = listOf(
            account("no-week", name = "Alpha", windows = listOf(win("5h", 300, 10.0, 2))),
            week("late", 120),
            week("no-reset", null, name = "Bravo"),
            week("soon", 6),
            week("tie-b", 48, name = "bravo"),
            week("tie-a", 48, name = "Alpha"),
            // a per-model weekly allowance is not the plan's weekly window
            account("model-only", name = "Aaa", windows = listOf(win("7d", 10080, 5.0, 1, model = "Large"))),
        )
        assertEquals(listOf("soon", "tie-a", "tie-b", "late", "model-only", "no-week", "no-reset"), orderedByWeeklyReset(list).map { it.id })
    }

    @Test fun theOrderInsideACardIsTheSameInsideGroupByProvider() {
        val soon = account("soon", windows = listOf(win("7d", 10080, 1.0, 3)))
        val late = account("late", windows = listOf(win("7d", 10080, 1.0, 90)))
        assertEquals(listOf("soon", "late"), groupByProvider(listOf(late, soon)).single().accounts.map { it.id })
    }

    @Test fun windowsAreFiveHoursThenAWeekThenTheRest() {
        val a = account("a", windows = listOf(win("30d", 43200, 1.0, 200), win("7d", 10080, 1.0, 100, model = "Large"), win("7d", 10080, 2.0, 100),
            win("1h", 60, 3.0, 1), win("5h", 300, 4.0, 3)))
        assertEquals(listOf("5h", "7d", "1h", "30d", "7d"), orderedWindows(a).map { it.label })
        assertEquals("the per-model one is last", "Large", orderedWindows(a).last().model)
    }

    @Test fun elapsedFractionIsHowFarTheWindowHasGone() {
        assertEquals(0.5, elapsedFraction(win("5h", 300, 0.0, null).copy(resetsAt = now.plusSeconds(150 * 60).toString()), now)!!, 1e-9)
        assertNull("no reset time", elapsedFraction(win("5h", 300, 0.0, null), now))
        assertNull("not yet started", elapsedFraction(win("5h", 300, 0.0, 6), now))
        assertNull("already over", elapsedFraction(win("5h", 300, 0.0, -1), now))
    }

    @Test fun theAxisGivesTheFirstDayHalfTheWidthAndTheWeekTheRest() {
        fun h(hours: Double) = (hours * 3_600_000).toLong()
        assertEquals(0f, resetChartAxis(0), 0f)
        assertEquals(0.225f, resetChartAxis(h(12.0)), 1e-6f)
        assertEquals("a day is 45%", 0.45f, resetChartAxis(h(24.0)), 1e-6f)
        assertEquals("four days: 3 of the 6 later days", 0.45f + 0.55f * 72 / 144, resetChartAxis(h(96.0)), 1e-6f)
        assertEquals("a week is the end", 1f, resetChartAxis(h(168.0)), 1e-6f)
        assertEquals("later is cut", 1f, resetChartAxis(h(24.0 * 20)), 0f)
        assertEquals("before now is now", 0f, resetChartAxis(-5), 0f)
    }

    @Test fun theChartHasARowPerAccountInTheGivenOrderWithItsTwoCapsules() {
        val rows = resetChartRows(listOf(
            account("a", windows = listOf(win("7d", 10080, 30.0, 96), win("5h", 300, 10.0, 4))),
            account("b", windows = listOf(win("7d", 10080, 30.0, 150))),
        ), now)!!
        assertEquals(listOf("a", "b"), rows.map { it.account.id })
        assertEquals(4 * 3600_000L, rows[0].five!!.resetsInMillis)
        assertEquals(resetChartAxis(4 * 3600_000L), rows[0].five!!.endFraction, 0f)
        assertEquals(resetChartAxis(96 * 3600_000L), rows[0].week!!.endFraction, 0f)
        assertNull("no 5h window, no 5h capsule", rows[1].five)
    }

    @Test fun aWeekLongerThanTheChartIsCutButItsResetIsKept() {
        val bar = resetChartRows(listOf(account("a", windows = listOf(win("7d", 10080, 1.0, 24 * 20)))), now)!!.single().week!!
        assertEquals(1f, bar.endFraction, 0f)
        assertEquals(24 * 20 * 3600_000L, bar.resetsInMillis)
    }

    @Test fun windowsWithoutAResetOrPastAndPerModelOnesGiveNoCapsule() {
        val rows = resetChartRows(listOf(
            account("none", windows = listOf(win("5h", 300, 1.0, null), win("7d", 10080, 1.0, -2))),
            account("model", windows = listOf(win("7d", 10080, 1.0, 30, model = "Large"))),
            account("ok", windows = listOf(win("5h", 300, 1.0, null), win("7d", 10080, 1.0, 48))),
        ), now)!!
        assertEquals(listOf("ok"), rows.map { it.account.id })
        assertNull(rows.single().five)
        assertNull(resetChartRows(listOf(account("a")), now))
        assertNull(resetChartRows(emptyList(), now))
    }

    @Test fun aCapsuleLabelIsShort() {
        fun m(minutes: Long) = chartDuration(minutes * 60_000)
        assertEquals(ChartDuration.Minutes(1), chartDuration(10))
        assertEquals(ChartDuration.Minutes(45), m(45))
        assertEquals(ChartDuration.Hours(15), m(90))
        assertEquals(ChartDuration.Hours(20), m(120))
        assertEquals("whole hours from three", ChartDuration.Hours(40), m(240))
        assertEquals(ChartDuration.Hours(180), m(18 * 60))
        assertEquals(ChartDuration.DaysHours(2, 2), m(50 * 60))
        assertEquals(ChartDuration.DaysHours(6, 6), m(6 * 24 * 60 + 6 * 60))
    }

    @Test fun theProtectionLineIsOnlyWhatTheHubSaysAndIsAPercentage() {
        assertNull("absent", protectLine(win("5h", 300, 1.0, 2)))
        assertEquals(98.0, protectLine(win("5h", 300, 1.0, 2).copy(protectPercent = 98.0))!!, 0.0)
        assertEquals(0.0, protectLine(win("5h", 300, 1.0, 2).copy(protectPercent = 0.0))!!, 0.0)
        assertNull("not a percentage", protectLine(win("5h", 300, 1.0, 2).copy(protectPercent = 140.0)))
        assertNull(protectLine(win("5h", 300, 1.0, 2).copy(protectPercent = -1.0)))
    }
}
