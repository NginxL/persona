package persona.platform.phonetools

import androidx.health.connect.client.records.BloodPressureRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.metadata.DataOrigin
import androidx.health.connect.client.records.metadata.Device
import androidx.health.connect.client.records.metadata.Metadata
import androidx.health.connect.client.units.Pressure
import kotlinx.serialization.json.JsonPrimitive
import kotlinx.serialization.json.buildJsonArray
import kotlinx.serialization.json.buildJsonObject
import kotlinx.serialization.json.double
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import kotlinx.serialization.json.long
import kotlinx.serialization.json.put
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import java.time.Instant

/** The generic health tools on Health Connect (HealthRecords.kt, iOS `HealthRecords`, #13740). */
class HealthRecordsTest {
    private val meta = Metadata(
        id = "r1",
        dataOrigin = DataOrigin("com.example.band"),
        clientRecordId = "band-7",
        device = Device(type = Device.TYPE_WATCH, manufacturer = "Example", model = "W1"),
    )
    private val origins = HealthRecords.Origins(null)

    @Test fun namesAreUniqueAndIosNamesFindTheSameData() {
        val names = HealthRecords.types.map { it.name }
        assertEquals(names.size, names.toSet().size)
        assertTrue(names.all { n -> n.all { it.isLowerCase() || it.isDigit() || it == '_' } })
        assertEquals("steps", HealthRecords.type("step_count")?.name)
        assertEquals("weight", HealthRecords.type("BODY_MASS")?.name)
        assertEquals("heart_rate", HealthRecords.type("HeartRateRecord")?.name)
        assertNull(HealthRecords.type("no_such_type"))
        assertNull(HealthRecords.type(" "))
        assertEquals(HealthRecords.types, HealthRecords.typeList(buildJsonObject { put("types", buildJsonArray { add(JsonPrimitive("all")) }) }))
        assertEquals(listOf("steps"), HealthRecords.typeList(buildJsonObject {
            put("types", buildJsonArray { add(JsonPrimitive("steps")); add(JsonPrimitive("step_count")) })
        }).map { it.name })
    }

    /** Health Connect grants only what the manifest asks for, so every listed type must be declared there. */
    @Test fun everyTypesReadPermissionIsDeclared() {
        val manifest = File("src/main/AndroidManifest.xml").readText()
        for (type in HealthRecords.types) {
            assertTrue("${type.name}: ${type.permission} is not in the manifest", "\"${type.permission}\"" in manifest)
        }
    }

    @Test fun accessFollowsTheGrantAndTheManifest() {
        val steps = HealthRecords.type("steps")!!
        assertEquals("granted", HealthRecords.access(steps, setOf(steps.permission), setOf(steps.permission)))
        assertEquals("not_granted", HealthRecords.access(steps, emptySet(), setOf(steps.permission)))
        assertEquals("unavailable", HealthRecords.access(steps, emptySet(), emptySet()))
    }

    @Test fun anchorsRoundTrip() {
        val page = HealthRecords.Anchor.Page("page.token|1", "changes/2+")
        assertEquals(page, HealthRecords.parseAnchor(HealthRecords.anchorText(page)))
        val changes = HealthRecords.Anchor.Changes("c")
        assertEquals(changes, HealthRecords.parseAnchor(HealthRecords.anchorText(changes)))
        assertNull(HealthRecords.parseAnchor("a1.abc"))
        assertNull(HealthRecords.parseAnchor("garbage"))
        assertTrue(HealthRecords.anchorText(page).none { it == '+' || it == '/' || it == '=' })
    }

    @Test fun aRecordCarriesValueSourceDeviceAndMetadata() {
        val start = Instant.parse("2026-11-20T08:00:00Z")
        val steps = StepsRecord(startTime = start, startZoneOffset = null, endTime = start.plusSeconds(600), endZoneOffset = null,
            count = 812, metadata = meta)
        val json = HealthRecords.recordJson(steps, origins)
        assertEquals("r1", json["id"]!!.jsonPrimitive.content)
        assertEquals(812L, json["value"]!!.jsonPrimitive.long)
        assertEquals("com.example.band", json["source"]!!.jsonObject["package"]!!.jsonPrimitive.content)
        assertEquals("watch", json["device"]!!.jsonObject["type"]!!.jsonPrimitive.content)
        assertEquals("band-7", json["metadata"]!!.jsonObject["client_record_id"]!!.jsonPrimitive.content)
        assertEquals(iso(start), json["start"]!!.jsonPrimitive.content)
        assertEquals(iso(start.plusSeconds(600)), json["end"]!!.jsonPrimitive.content)
    }

    @Test fun seriesAndCorrelationsKeepTheirParts() {
        val t = Instant.parse("2026-11-20T08:00:00Z")
        val heart = HeartRateRecord(startTime = t, startZoneOffset = null, endTime = t.plusSeconds(60), endZoneOffset = null,
            samples = listOf(HeartRateRecord.Sample(t, 61), HeartRateRecord.Sample(t.plusSeconds(30), 64)), metadata = meta)
        val samples = HealthRecords.fields(heart)["samples"]!!.jsonArray
        assertEquals(listOf(61L, 64L), samples.map { it.jsonObject["value"]!!.jsonPrimitive.long })
        val pressure = BloodPressureRecord(time = t, zoneOffset = null, systolic = Pressure.millimetersOfMercury(121.0),
            diastolic = Pressure.millimetersOfMercury(79.0), metadata = meta)
        val fields = HealthRecords.fields(pressure)
        assertEquals(121.0, fields["systolic"]!!.jsonPrimitive.double, 0.001)
        assertEquals(79.0, fields["diastolic"]!!.jsonPrimitive.double, 0.001)
        assertEquals(t to t, HealthRecords.timeOf(pressure))
    }

    @Test fun windowsIncludeTheirEdges() {
        val s = Instant.parse("2026-11-20T00:00:00Z")
        val e = Instant.parse("2026-11-21T00:00:00Z")
        assertTrue(HealthRecords.inWindow(s, s, e))
        assertTrue(HealthRecords.inWindow(e, s, e))
        assertTrue(HealthRecords.inWindow(e.plusSeconds(3600), s, null))
        assertFalse(HealthRecords.inWindow(s.minusSeconds(1), s, e))
        assertFalse(HealthRecords.inWindow(e.plusSeconds(1), s, e))
    }

    @Test fun noDeviceMeansNoDeviceField() {
        assertNull(HealthRecords.deviceJson(null))
        assertNull(HealthRecords.deviceJson(Device(type = Device.TYPE_UNKNOWN)))
    }
}
