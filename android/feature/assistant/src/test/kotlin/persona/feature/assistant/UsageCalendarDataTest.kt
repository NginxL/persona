package persona.feature.assistant

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.api.UsageDay
import persona.core.contracts.api.UsageTokens

class UsageCalendarDataTest {
    @Test fun everyTokenKindCountsButRepliesDoNot() {
        val tokens = UsageTokens(input = 1, cacheWrite = 2, cacheRead = 4, output = 8, requests = 16)
        assertEquals(15L, tokens.total)
    }

    @Test fun heatStepsRankTiesAtTheirSharedUpperRank() {
        assertEquals(
            listOf(0, 1, 3, 3, 4),
            usageHeatSteps(listOf(0, 10, 20, 20, 40)),
        )
        assertEquals(listOf(4), usageHeatSteps(listOf(7)))
        assertEquals(listOf(0, 0), usageHeatSteps(listOf(0, 0)))
    }

    @Test fun calendarStartsMondayAndEndsInTheCurrentWeek() {
        val days = (11L..17L).map { day -> UsageDay(date = "2026-11-$day", input = day) }
        val weeks = usageCalendarWeeks(days)

        assertEquals(2, weeks.size)
        assertEquals(listOf(null, null) + days.take(5), weeks[0])
        assertEquals(days.drop(5) + List(5) { null }, weeks[1])
        assertEquals("2026-11-17", weeks.last().filterNotNull().last().date)
    }

    @Test fun malformedDatesAreSkippedWithoutChangingTheValidWeek() {
        val monday = UsageDay(date = "2026-11-16", input = 16)
        val wednesday = UsageDay(date = "2026-11-18", input = 18)
        val malformed = listOf(UsageDay(date = ""), UsageDay(date = "not-a-date"))
        val result = runCatching { usageCalendarWeeks(listOf(monday) + malformed + listOf(wednesday)) }
        assertTrue("malformed hub dates must not throw: ${result.exceptionOrNull()}", result.isSuccess)
        val weeks = result.getOrThrow()

        assertEquals(1, weeks.size)
        assertEquals(listOf(monday, null, wednesday, null, null, null, null), weeks.single())
    }
}
