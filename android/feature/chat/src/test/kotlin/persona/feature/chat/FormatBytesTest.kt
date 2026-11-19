package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/**
 * A chat size rounds before it picks its unit, so a size that rounds up to the next unit is shown in it, as the machine
 * pages' `MetricFormat.bytes` does; kilobytes round to the nearest, not down (#9890).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class FormatBytesTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    @Test fun kilobytesRoundToTheNearest() {
        assertEquals("2 KB", formatBytes(1_999, res))
        assertEquals("1 KB", formatBytes(1_499, res))
        assertEquals("12 KB", formatBytes(12_345, res))
    }

    @Test fun aSizeThatRoundsUpToTheNextUnitIsShownInIt() {
        // 999.5 KB rounds to 1,000 KB: 1.0 MB
        assertEquals("1.0 MB", formatBytes(999_500, res))
        assertEquals("1.0 MB", formatBytes(999_999, res))
        assertEquals("999 KB", formatBytes(999_499, res))
        // 999.95 MB rounds to 1000.0 MB: 1.0 GB
        assertEquals("1.0 GB", formatBytes(999_950_000, res))
        assertEquals("999.9 MB", formatBytes(999_949_999, res))
    }

    @Test fun otherSizesReadAsBefore() {
        assertEquals("999 bytes", formatBytes(999, res))
        assertEquals("1 KB", formatBytes(1_000, res))
        assertEquals("3.4 MB", formatBytes(3_400_000, res))
        assertEquals("1.0 MB", formatBytes(1_000_000, res))
        assertEquals("2.5 GB", formatBytes(2_500_000_000, res))
    }
}
