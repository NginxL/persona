package persona.feature.settings

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import java.time.Instant
import java.time.ZoneOffset

/** Settings' "N days ago" counts the phone's midnights, as [SentText.ago] and the web's `ago` do, not 24-hour spans (#9889). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class AgoTextTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    private fun days(at: String, now: String, offsetHours: Int = 0) =
        agoText(res, Instant.parse(at), Instant.parse(now), ZoneOffset.ofHours(offsetHours), days = true)

    @Test fun daysAreCountedByTheCalendar() {
        // 30 hours back, two midnights ago
        assertEquals("2 天前", days("2026-11-03T19:00:00Z", "2026-11-05T01:00:00Z"))
        // 40 hours back, one midnight ago
        assertEquals("1 天前", days("2026-11-04T07:00:00Z", "2026-11-05T23:00:00Z"))
        // No upper limit: 80 midnights back
        assertEquals("80 天前", days("2026-08-17T23:00:00Z", "2026-11-05T01:00:00Z"))
        // The phone's calendar: the first case eight hours east is Nov 4 03:00 seen on Nov 5 09:00
        assertEquals("1 天前", days("2026-11-03T19:00:00Z", "2026-11-05T01:00:00Z", offsetHours = 8))
    }

    @Test fun underADayIsStillMinutesOrHours() {
        assertEquals("23 小时前", days("2026-11-04T02:00:00Z", "2026-11-05T01:00:00Z"))
        assertEquals("5 分钟前", days("2026-11-05T00:55:00Z", "2026-11-05T01:00:00Z"))
    }

    @Config(qualifiers = "en")
    @Test fun englishCountsTheSameDays() {
        assertEquals("2 days ago", days("2026-11-03T19:00:00Z", "2026-11-05T01:00:00Z"))
    }
}
