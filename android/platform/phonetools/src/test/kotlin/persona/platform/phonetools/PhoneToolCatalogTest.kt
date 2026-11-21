package persona.platform.phonetools

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import androidx.test.ext.junit.runners.AndroidJUnit4
import org.junit.runner.RunWith
import org.robolectric.annotation.Config
import kotlinx.serialization.json.*
import org.junit.Assert.*
import org.junit.Test
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId

/**
 * The hub merges phone tools of the same name from every phone and may send a call to either (server/internal/hub
 * mcpTools, phoneClient), so each tool Android declares must read exactly like the iOS app's. The fixture is exported
 * from apple/Persona/PhoneTools by scripts/assets/export_ios_phone_tools.py (`--check` tells when it is out of date).
 */
@RunWith(AndroidJUnit4::class)
@Config(sdk = [35])
class PhoneToolCatalogTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val ios: JsonObject = Json.parseToJsonElement(
        requireNotNull(javaClass.classLoader!!.getResource("ios-phone-tools.json")).readText()).jsonObject

    @Test fun sameToolsAsIosPlusTheAndroidNotificationList() {
        val android = PhoneToolCatalog.all.map { it.spec.name }
        assertEquals(android.size, android.toSet().size)
        assertEquals(ios.keys, android.toSet() - "phone_notifications_list")
    }

    /**
     * Sentences an iPhone description has that Android's does not, by tool name. The fixture is the iOS text verbatim,
     * so every difference is listed here with its reason instead of being trimmed by the exporter.
     */
    private val iosOnlyDescriptionSentences = mapOf(
        // iPhone-only behavior: a locked iPhone cannot read HealthKit and answers with the last snapshot it saved. Android
        // reads Health Connect and has no such snapshot, so promising one would mislead the model when this phone is the
        // one called. Drop this entry when the Health space gives Android the same behavior or the sentence changes.
        "phone_health_summary" to " When the iPhone is locked the app cannot read HealthKit: it returns the last snapshot " +
            "saved while the device was unlocked (\"source\": \"snapshot\" with \"as_of\"), or a device_locked error " +
            "when no snapshot exists yet.",
    )

    @Test fun everyToolIsDeclaredWordForWordAsOnIos() {
        for (tool in PhoneToolCatalog.all.filter { it.spec.name in ios }) {
            val want = ios.getValue(tool.spec.name).jsonObject
            val name = tool.spec.name
            val iosDescription = want.getValue("description").jsonPrimitive.content
            val extra = iosOnlyDescriptionSentences[name]
            // The listed sentence must still be on iOS's description (a stale entry fails) and is the only difference.
            if (extra != null) assertTrue("$name: the allowlisted sentence is no longer in the iOS text", iosDescription.endsWith(extra))
            assertEquals(name, iosDescription.removeSuffix(extra.orEmpty()), tool.spec.description)
            assertEquals(name, want.getValue("source").jsonPrimitive.content, tool.source.key)
            // iOS Schema.object: additionalProperties false, properties and required only when there are some.
            val properties = want.getValue("properties").jsonObject
            val required = want.getValue("required").jsonArray
            val schema = buildJsonObject {
                put("type", "object"); put("additionalProperties", false)
                if (properties.isNotEmpty()) put("properties", properties)
                if (required.isNotEmpty()) put("required", required)
            }
            assertEquals(name, schema, tool.spec.inputSchema)
        }
        // An allowlist entry for a tool Android does not declare, or one that no longer differs, is dead weight.
        for ((name, extra) in iosOnlyDescriptionSentences) {
            assertNotEquals(name, ios.getValue(name).jsonObject.getValue("description").jsonPrimitive.content,
                PhoneToolCatalog.all.single { it.spec.name == name }.spec.description)
            assertTrue(name, extra.isNotBlank())
        }
        // register_phone's wire shape.
        assertEquals(setOf("name", "description", "input_schema"), PhoneToolCatalog.all.first().spec.toWire().keys)
    }

    @Test fun onlyWritesAskFirstAndForegroundToolsAreTheOnesAndroidRestricts() {
        assertEquals(PhoneToolCatalog.confirmedWriteNames, PhoneToolCatalog.all
            .filter { it.confirmation != null || it.confirmationNeedsPreparation }.map { it.spec.name }.toSet())
        assertEquals(setOf("phone_calendar_create_event", "phone_calendar_update_event", "phone_calendar_delete_event",
            "phone_contacts_create", "phone_contacts_update", "phone_call_start"), PhoneToolCatalog.confirmedWriteNames)
        // Updates and deletes show before → after, so their card is prepared in the suspend path; the others stay pure.
        assertEquals(setOf("phone_calendar_update_event", "phone_calendar_delete_event", "phone_contacts_update"),
            PhoneToolCatalog.all.filter { it.confirmationNeedsPreparation }.map { it.spec.name }.toSet())
        assertTrue(PhoneToolCatalog.all.filter { it.confirmationNeedsPreparation }.all { it.confirmation == null })
        assertEquals(setOf("phone_location_current", "phone_clipboard_get", "phone_clipboard_set", "phone_call_start"),
            PhoneToolCatalog.all.filter { it.foregroundOnly }.map { it.spec.name }.toSet())
        assertEquals(listOf("phone_photos_recent", "phone_photo_get"),
            PhoneToolCatalog.all.filter { it.source == PhoneSource.Photos }.map { it.spec.name })
        assertEquals(listOf("phone_call_start"),
            PhoneToolCatalog.all.filter { it.source.key == "calls" }.map { it.spec.name })
    }

    @Test fun argumentsAreReadLikeTheIosApp() {
        val args = buildJsonObject {
            put("limit", 9999); put("days", 0); put("text", "  x "); put("blank", "  "); put("number", "7"); put("half", 2.6)
        }
        assertEquals(200, args.int("limit", 50, 1, 200))
        assertEquals(1, args.intOrNull("days", 1, 3650))
        assertNull(args.intOrNull("missing", 1, 3650))
        // A number sent as a string is no number: the default applies.
        assertEquals(20, args.int("number", 20, 1, 100))
        assertEquals(3, args.int("half", 20, 1, 100))
        assertEquals("x", args.text("text"))
        assertNull(args.text("blank"))
        assertEquals("  ", args.raw("blank"))
        assertFalse(args.bool("missing"))
    }

    @Test fun timesAndTheConfirmationSummary() {
        val zone = ZoneId.of("Asia/Shanghai")
        assertEquals(Instant.parse("2026-10-24T06:00:00Z"), parseTime("2026-10-24T14:00:00+08:00", zone))
        assertEquals(Instant.parse("2026-10-24T06:00:00Z"), parseTime("2026-10-24T06:00:00Z", zone))
        assertEquals(Instant.parse("2026-10-23T16:00:00Z"), parseTime("2026-10-24", zone))
        assertEquals(Instant.parse("2026-10-24T06:30:00Z"), parseTime("2026-10-24 14:30", zone))
        assertNull(parseTime(" ", zone))
        val failure = runCatching { parseTime("tomorrow", zone) }.exceptionOrNull() as PhoneToolFailure
        assertEquals("cannot read the time \"tomorrow\"; use ISO 8601 such as 2026-10-24T14:00:00+08:00", failure.message)
        assertEquals("2026-10-24T14:00:00+08:00", iso(Instant.parse("2026-10-24T06:00:00.250Z"), zone))

        val today = LocalDate.of(2026, 10, 1)
        val event = CalendarArguments.parse(buildJsonObject {
            put("title", " 周会 "); put("start", "2026-11-01T15:00:00+08:00"); put("end", "2026-11-01T15:30:00+08:00")
            put("location", "三楼"); put("calendar", "工作")
        }, zone)
        assertEquals("新建日程「周会」\n11月1日 15:00 → 15:30\n地点：三楼\n日历：工作", event.summary(res, zone, today))
        val overnight = event.copy(end = Instant.parse("2027-01-02T01:00:00Z"), location = null, calendar = null)
        assertEquals("新建日程「周会」\n11月1日 15:00 → 2027年1月2日 09:00", overnight.summary(res, zone, today))
        val allDay = CalendarArguments.parse(buildJsonObject {
            put("title", "出差"); put("start", "2026-11-03"); put("end", "2026-11-05"); put("all_day", true)
        }, zone)
        assertEquals("新建日程「出差」\n11月3日 → 11月4日（全天）", allDay.summary(res, zone, today))
        assertEquals(Instant.parse("2026-11-03T00:00:00Z").toEpochMilli(), allDay.startMillis(zone))
        assertEquals("新建日程「出差」\n11月3日（全天）",
            allDay.copy(end = parseTime("2026-11-04", zone)!!).summary(res, zone, today))
    }

    @Test fun photoIdsAndScreenshots() {
        assertEquals("image" to 42L, parsePhotoId("image:42"))
        assertEquals("video" to 7L, parsePhotoId("video:7"))
        assertNull(parsePhotoId("42"))
        assertNull(parsePhotoId("audio:1"))
        assertNull(parsePhotoId("image:x"))
        assertTrue(isScreenshot("Pictures/Screenshots/"))
        assertTrue(isScreenshot("DCIM/截屏/"))
        assertFalse(isScreenshot("DCIM/Camera/"))
        assertFalse(isScreenshot(null))
    }
}

/** The calendar confirmation in the other languages: words from the resources, dates from LocalizedFormats. */
@RunWith(AndroidJUnit4::class)
class CalendarSummaryLanguagesTest {
    private val res get() = ApplicationProvider.getApplicationContext<Context>().resources
    private val zone = ZoneId.of("Asia/Shanghai")
    private val event get() = CalendarArguments.parse(buildJsonObject {
        put("title", "Weekly sync"); put("start", "2026-11-01T15:00:00+08:00"); put("end", "2026-11-01T15:30:00+08:00")
        put("location", "3F")
    }, zone)

    @Config(sdk = [35], qualifiers = "en")
    @Test fun english() {
        val summary = event.summary(res, zone, LocalDate.of(2026, 10, 1))
        assertTrue(summary, summary.startsWith("New event “Weekly sync”\nNov 1 "))
        assertTrue(summary, summary.endsWith("\nLocation: 3F"))
    }

    @Config(sdk = [35], qualifiers = "ja")
    @Test fun japanese() {
        val summary = event.summary(res, zone, LocalDate.of(2026, 10, 1))
        assertEquals("新しい予定「Weekly sync」\n11月1日 15:00 → 15:30\n場所：3F", summary)
    }
}
