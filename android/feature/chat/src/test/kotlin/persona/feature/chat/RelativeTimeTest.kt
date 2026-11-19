package persona.feature.chat

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.Assert.assertEquals
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import java.time.Instant
import java.time.ZoneOffset

/** A chat time says "Yesterday" for the day before today on the local calendar, not for anything 24 to 48 hours back (#9878). */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class RelativeTimeTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources

    private fun at(text: String) = Instant.parse(text)

    @Test fun theDayBeforeYesterdayIsItsDateEvenWithin48Hours() {
        // 30 hours back, two midnights ago
        assertEquals("11月3日", relativeTime(at("2026-11-03T19:00:00Z"), at("2026-11-05T01:00:00Z"), res, ZoneOffset.UTC))
        // 47 hours back, two midnights ago
        assertEquals("11月3日", relativeTime(at("2026-11-03T01:30:00Z"), at("2026-11-05T00:30:00Z"), res, ZoneOffset.UTC))
    }

    @Test fun yesterdayIsTheCalendarDayBefore() {
        // 40 hours back, one midnight ago
        assertEquals("昨天 07:00", relativeTime(at("2026-11-04T07:00:00Z"), at("2026-11-05T23:00:00Z"), res, ZoneOffset.UTC))
        // 24.5 hours back, just past midnight
        assertEquals("昨天 00:30", relativeTime(at("2026-11-04T00:30:00Z"), at("2026-11-05T01:00:00Z"), res, ZoneOffset.UTC))
        // Under a day still counts hours, across midnight too
        assertEquals("23 小时前", relativeTime(at("2026-11-04T02:00:00Z"), at("2026-11-05T01:00:00Z"), res, ZoneOffset.UTC))
    }

    @Test fun theDayIsTheOneInTheGivenZone() {
        // The first case above, eight hours east: Nov 4 03:00 seen on Nov 5 09:00
        assertEquals("昨天 03:00", relativeTime(at("2026-11-03T19:00:00Z"), at("2026-11-05T01:00:00Z"), res, ZoneOffset.ofHours(8)))
    }

    @Config(qualifiers = "en")
    @Test fun englishNamesTheDate() {
        assertEquals("Nov 3", relativeTime(at("2026-11-03T19:00:00Z"), at("2026-11-05T01:00:00Z"), res, ZoneOffset.UTC))
    }
}
