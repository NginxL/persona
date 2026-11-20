package persona.feature.files

import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.robolectric.RuntimeEnvironment
import java.time.Instant
import java.time.ZoneId
import java.time.ZoneOffset
import java.util.Locale
import org.robolectric.annotation.Config

@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class FilesPathsTest {
    private val res get() = RuntimeEnvironment.getApplication().resources

    @Test fun pathsFromAConversationResolveAgainstTheSessionFolder() {
        val cwd = "/Users/me/code/weather"
        assertEquals("/Users/me/code/weather/Sources/App.swift", FilesPaths.resolve("Sources/App.swift", cwd))
        assertEquals("/Users/me/code/weather/README.md", FilesPaths.resolve("./README.md", cwd))
        assertEquals("/Users/me/code/other/a.txt", FilesPaths.resolve("../other/a.txt", cwd))
        assertEquals("/etc/hosts", FilesPaths.resolve("/etc//hosts", cwd))
        // "~" is the machine's home: only the hub knows it, so it travels as written (folded, never joined to cwd).
        assertEquals("~/.persona/personas", FilesPaths.resolve("~/.persona/personas/", cwd))
        assertEquals("~", FilesPaths.resolve("~", cwd))
        assertEquals("~", FilesPaths.resolve("~/x/..", cwd))
        assertEquals("/", FilesPaths.resolve("/../..", cwd))
        assertEquals(cwd, FilesPaths.resolve("  ", cwd))
        assertEquals("/Users/me/code/weather/Sources", FilesPaths.resolve("Sources/", cwd))
    }

    @Test fun aTrailingSlashOrTheHomeNamesAFolder() {
        assertTrue(FilesPaths.namesDirectory("Sources/"))
        assertTrue(FilesPaths.namesDirectory("~"))
        assertTrue(FilesPaths.namesDirectory("/"))
        assertFalse(FilesPaths.namesDirectory("Makefile"))
        assertFalse(FilesPaths.namesDirectory("~/notes.txt"))
    }

    @Test fun parentsGoUpToTheTop() {
        assertEquals("/Users/me/code", FilesPaths.parent("/Users/me/code/weather"))
        assertEquals("/Users/me/code", FilesPaths.parent("/Users/me/code/weather/"))
        assertEquals("/", FilesPaths.parent("/Users"))
        assertNull(FilesPaths.parent("/"))
        assertEquals("~", FilesPaths.parent("~/code"))
        assertNull(FilesPaths.parent("~"))
        assertEquals("weather", FilesPaths.name("/Users/me/code/weather/"))
        assertEquals("/", FilesPaths.name("/"))
        assertEquals("~", FilesPaths.name("~"))
    }

    @Test fun crumbsRunFromTheTopToTheFolderShown() {
        assertEquals(listOf("weather" to "/r/weather"), FilesPaths.crumbs("/r/weather", "/r/weather"))
        assertEquals(listOf("weather" to "/r/weather", "Sources" to "/r/weather/Sources", "App" to "/r/weather/Sources/App"),
            FilesPaths.crumbs("/r/weather", "/r/weather/Sources/App"))
        assertEquals(listOf("/" to "/", "Users" to "/Users", "me" to "/Users/me"), FilesPaths.crumbs("/", "/Users/me"))
        assertEquals(listOf("~" to "~", "code" to "~/code"), FilesPaths.crumbs("~", "~/code"))
        // A folder outside the top (a sibling with the same prefix) is its own single crumb.
        assertEquals(listOf("weather-site" to "/r/weather-site"), FilesPaths.crumbs("/r/weather", "/r/weather-site"))
        assertTrue(FilesPaths.isWithin("/r/weather/a", "/r/weather"))
        assertFalse(FilesPaths.isWithin("/r/weather-site", "/r/weather"))
        assertTrue(FilesPaths.isWithin("/anything", "/"))
    }

    @Test fun joiningNeverDoublesOrLosesTheSlash() {
        assertEquals("/a", joinRemotePath("/", "a"))
        assertEquals("/r/a", joinRemotePath("/r", "a"))
        assertEquals("/r/a", joinRemotePath("/r/", "a"))
        assertEquals("~/a", joinRemotePath("~", "a"))
    }

    @Test fun moveRejectsTheSameFolderAndAnyDestinationInsideTheSourceFolder() {
        val folder = MoveSource("/work/Sources", directory = true)
        assertFalse(folder.canMoveInto("/work/./"))
        assertFalse(folder.canMoveInto("/work/Sources/"))
        assertFalse(folder.canMoveInto("/work/Other/../Sources/Nested"))
        assertTrue(folder.canMoveInto("/work/Sources-archive"))
        assertTrue(folder.canMoveInto("/"))
        assertFalse(MoveSource("~/work/file.txt", directory = false).canMoveInto("~/work/"))
    }

    @Test fun nodeTimesWithTheMachinesOffsetAreRead() {
        // Shapes as a node sends them: RFC 3339 with the machine's own offset, nanoseconds on file times.
        assertEquals(Instant.parse("2026-10-31T05:58:40.205893083Z"), fileTime("2026-10-31T13:58:40.205893083+08:00"))
        assertEquals(Instant.parse("2026-10-31T05:58:40Z"), fileTime("2026-10-31T13:58:40+08:00"))
        assertEquals(Instant.parse("2026-10-31T05:58:40Z"), fileTime("2026-10-31T05:58:40Z"))
        assertEquals(Instant.parse("2026-10-31T19:58:40Z"), fileTime("2026-10-31T13:58:40-06:00"))
        // Go's zero time is "none", not year 1.
        assertNull(fileTime("0001-01-01T00:00:00Z"))
        assertNull(fileTime("0001-01-01T08:05:43+08:05"))
        assertNull(fileTime(""))
        assertNull(fileTime(null))
        assertNull(fileTime("yesterday"))
    }

    @Test fun listTimesAreRelativeThenDates() {
        val now = Instant.parse("2026-11-02T12:00:00Z")
        val zone = ZoneOffset.UTC
        assertEquals("刚刚", relativeTime(res, now.minusSeconds(30), now, zone))
        assertEquals("5 分钟前", relativeTime(res, now.minusSeconds(300), now, zone))
        assertEquals("3 小时前", relativeTime(res, now.minusSeconds(3 * 3600), now, zone))
        assertEquals("2 天前", relativeTime(res, now.minusSeconds(2 * 86_400), now, zone))
        assertEquals("10月1日", relativeTime(res, Instant.parse("2026-10-01T09:00:00Z"), now, zone))
        assertEquals("2025年12月30日", relativeTime(res, Instant.parse("2025-12-30T09:00:00Z"), now, zone))
        assertEquals("3 小时前", fileDate(res, "2026-11-02T17:00:00+08:00", now, zone))
        assertNull(fileDate(res, "0001-01-01T00:00:00Z", now, zone))
    }

    /** As the web's `ago` (#9889): past a day, days count the phone's midnights, not 24-hour spans, and from seven it is the date. */
    @Test fun daysAreCountedByTheCalendar() {
        val zone = ZoneOffset.UTC
        // 30 hours back, two midnights ago
        assertEquals("2 天前", relativeTime(res, Instant.parse("2026-11-03T19:00:00Z"), Instant.parse("2026-11-05T01:00:00Z"), zone))
        // 40 hours back, one midnight ago
        assertEquals("1 天前", relativeTime(res, Instant.parse("2026-11-04T07:00:00Z"), Instant.parse("2026-11-05T23:00:00Z"), zone))
        // 6 days and 2 hours back, seven midnights ago: the date
        assertEquals("11月1日", relativeTime(res, Instant.parse("2026-11-01T23:00:00Z"), Instant.parse("2026-11-08T01:00:00Z"), zone))
        assertEquals("6 天前", relativeTime(res, Instant.parse("2026-11-02T01:00:00Z"), Instant.parse("2026-11-08T23:00:00Z"), zone))
        // The phone's calendar: the first case eight hours east is Nov 4 03:00 seen on Nov 5 09:00
        assertEquals("1 天前", relativeTime(res, Instant.parse("2026-11-03T19:00:00Z"), Instant.parse("2026-11-05T01:00:00Z"), ZoneOffset.ofHours(8)))
    }

    @Test fun aCommitsWholeDateIsInThePhonesZone() {
        assertEquals("2026年10月31日 13:58", fullDate("2026-10-31T05:58:40Z", Locale.SIMPLIFIED_CHINESE, ZoneId.of("Asia/Shanghai")))
        assertEquals("2026年10月31日 13:58", fullDate("2026-10-31T13:58:40+08:00", Locale.SIMPLIFIED_CHINESE, ZoneId.of("Asia/Shanghai")))
        assertNull(fullDate("0001-01-01T00:00:00Z", Locale.SIMPLIFIED_CHINESE, ZoneOffset.UTC))
    }

    @Test fun sizesUseBinaryUnits() {
        assertEquals("0 B", fileSize(0, Locale.US))
        assertEquals("1,023 B", fileSize(1023, Locale.US))
        assertEquals("1 KB", fileSize(1024, Locale.US))
        assertEquals("1.5 KB", fileSize(1536, Locale.US))
        assertEquals("165 KB", fileSize(165 * 1024 + 100, Locale.US))
        assertEquals("2 MB", fileSize(2L shl 20, Locale.US))
        assertEquals("9.9 KB", fileSize(10_137, Locale.US))
        // The language's decimal mark.
        assertEquals("1,5 KB", fileSize(1536, Locale.GERMANY))
    }

    @Config(sdk = [35], qualifiers = "en")
    @Test fun listTimesInEnglish() {
        val now = Instant.parse("2026-11-02T12:00:00Z")
        val zone = ZoneOffset.UTC
        assertEquals("Just now", relativeTime(res, now.minusSeconds(30), now, zone))
        assertEquals("1 minute ago", relativeTime(res, now.minusSeconds(60), now, zone))
        assertEquals("3 hours ago", relativeTime(res, now.minusSeconds(3 * 3600), now, zone))
        assertEquals("Oct 1", relativeTime(res, Instant.parse("2026-10-01T09:00:00Z"), now, zone))
        assertEquals("Dec 30, 2025", relativeTime(res, Instant.parse("2025-12-30T09:00:00Z"), now, zone))
    }
}
