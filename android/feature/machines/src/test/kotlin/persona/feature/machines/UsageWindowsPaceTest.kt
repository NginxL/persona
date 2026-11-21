package persona.feature.machines

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test
import persona.core.contracts.api.UsageWindow
import java.time.Instant

/** The pace a window's row shows, the same cases as the web's lib/usage-windows.test.ts ("accountPace"). */
class UsageWindowsPaceTest {
    private val now = Instant.ofEpochSecond(1_793_600_000L)

    /** A 5-hour window with 3 hours left: 40% of it has gone by. */
    private fun fiveHours(used: Double) = UsageWindow("5h", 300, used, now.plusSeconds(3 * 3600L).toString())

    @Test fun aFreshReadingHasItsPace() {
        assertEquals(PaceState.Over, pace(fiveHours(50.0), now, stale = false)?.state)
        assertEquals(PaceState.Under, pace(fiveHours(30.0), now, stale = false)?.state)
    }

    /** #12411: an out-of-date reading keeps its numbers but says nothing about how fast it is being used now. */
    @Test fun anOutOfDateReadingHasNone() {
        assertNull(pace(fiveHours(50.0), now, stale = true))
        assertNull(pace(fiveHours(30.0), now, stale = true))
    }
}
