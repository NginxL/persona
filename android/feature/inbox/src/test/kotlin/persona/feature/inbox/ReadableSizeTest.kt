package persona.feature.inbox

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config

/** A mail attachment's size picks its unit after rounding, as the machine pages' `MetricFormat.bytes` does (#9890). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35], qualifiers = "en")
class ReadableSizeTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    @Test fun aSizeThatRoundsTo1024KbIsAMegabyte() {
        // 1023.5 KB and up round to 1,024 KB: that is 1.0 MB
        assertEquals("1.0 MB", readableSize(res, 1_048_064))
        assertEquals("1.0 MB", readableSize(res, 1_048_575))
        assertEquals("1.0 MB", readableSize(res, 1_048_576))
    }

    @Test fun belowThatItStaysInKilobytes() {
        assertEquals("1,023 KB", readableSize(res, 1_048_063))
        assertEquals("1 KB", readableSize(res, 1_024))
        assertEquals("1023 bytes", readableSize(res, 1_023))
    }
}
