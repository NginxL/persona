package persona.core.data.dev

import java.io.File
import java.nio.file.Files
import java.time.Instant
import java.time.ZoneId
import java.util.zip.ZipFile
import kotlinx.serialization.json.JsonArray
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonArray
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionErrorReport

class DevPackageTest {
    private val directory: File = Files.createTempDirectory("dev-package").toFile()
    private val now: Instant = Instant.parse("2026-11-17T12:00:00Z")
    private val since: Instant = now.minusSeconds(3600)
    private val zone: ZoneId = ZoneId.of("Asia/Shanghai")
    private val device = DevDevice(osVersion = "16", model = "Pixel 9", appVersion = "1.4.0", build = "512", commit = "abc1234", utcOffsetMinutes = 480)
    @After fun cleanUp() { directory.deleteRecursively() }

    private fun store(): DevStore {
        val store = DevStore(File(directory, "records"))
        listOf("page", "minute", "request_error", "battery").forEach { kind ->
            store.appendEvents(listOf("""{"t":"$now","kind":"$kind","name":"chosen"}"""), now)
        }
        store.appendError(ExtensionErrorReport(id = "one", time = now.toString(), message = "token=shh"), now)
        return store
    }

    @Test fun theZipHasTheSharedNameFolderManifestAndEveryCategory() {
        val zip = DevPackage.exportZip(File(directory, "work"), store(), DevCategory.entries.toSet(), since, device,
            log = listOf("launch https://hub.example.com/a?token=shh", "ready"), now = now, zone = zone)
        assertEquals("persona-diagnostics-android-20261117-2000.zip", zip.name)
        val entries = readZip(zip)
        val root = "persona-diagnostics-android-20261117-2000/"
        assertEquals(setOf(root + "README.md", root + "manifest.json", root + "events.jsonl", root + "errors.jsonl", root + "log.txt"), entries.keys)

        val manifest = JsonCodec.parseToJsonElement(entries.getValue(root + "manifest.json")).jsonObject
        assertEquals("persona-diagnostics", manifest.getValue("format").jsonPrimitive.content)
        assertEquals(1, manifest.getValue("version").jsonPrimitive.int)
        assertEquals("android", manifest.getValue("platform").jsonPrimitive.content)
        assertEquals("16", manifest.getValue("os_version").jsonPrimitive.content)
        assertEquals("Pixel 9", manifest.getValue("device_model").jsonPrimitive.content)
        assertEquals("abc1234", manifest.getValue("commit").jsonPrimitive.content)
        assertEquals(480, manifest.getValue("utc_offset_minutes").jsonPrimitive.int)
        assertEquals(listOf("device", "logs", "errors", "energy"), manifest.getValue("categories").jsonArray.map { it.jsonPrimitive.content })
        assertEquals(4, manifest.getValue("counts").jsonObject.getValue("events").jsonPrimitive.int)
        assertEquals(1, manifest.getValue("counts").jsonObject.getValue("errors").jsonPrimitive.int)
        assertEquals(2, manifest.getValue("counts").jsonObject.getValue("log_lines").jsonPrimitive.int)
        assertEquals(since.toString(), manifest.getValue("recording_since").jsonPrimitive.content)
        assertEquals(now.toString(), manifest.getValue("exported_at").jsonPrimitive.content)

        assertEquals("launch /a\nready\n", entries.getValue(root + "log.txt"))
        assertFalse(entries.getValue(root + "errors.jsonl").contains("shh"))
        assertTrue(entries.getValue(root + "README.md").contains("ApplicationExitInfo"))
    }

    @Test fun excludedCategoriesLeaveTheirFilesAndEventsOut() {
        val records = store()
        val logsOnly = readZip(DevPackage.exportZip(File(directory, "logs"), records, setOf(DevCategory.Logs), since, device, now = now, zone = zone))
        val logRoot = logsOnly.keys.first().substringBefore('/') + '/'
        assertFalse(logsOnly.containsKey(logRoot + "errors.jsonl"))
        val logEvents = logsOnly.getValue(logRoot + "events.jsonl").lines().filter(String::isNotBlank)
        assertEquals(listOf("page", "request_error"), logEvents.map { JsonCodec.parseToJsonElement(it).jsonObject.getValue("kind").jsonPrimitive.content })
        assertEquals("", logsOnly.getValue(logRoot + "log.txt")) // no process log was supplied
        val logManifest = JsonCodec.parseToJsonElement(logsOnly.getValue(logRoot + "manifest.json")).jsonObject
        assertEquals(JsonArray(listOf(kotlinx.serialization.json.JsonPrimitive("device"), kotlinx.serialization.json.JsonPrimitive("logs"))), logManifest.getValue("categories"))

        val energyAndErrors = readZip(DevPackage.exportZip(File(directory, "energy"), records, setOf(DevCategory.Energy, DevCategory.Errors), since, device,
            log = listOf("must not appear"), now = now, zone = zone))
        val energyRoot = energyAndErrors.keys.first().substringBefore('/') + '/'
        val energyEvents = energyAndErrors.getValue(energyRoot + "events.jsonl").lines().filter(String::isNotBlank)
        assertEquals(listOf("minute", "battery"), energyEvents.map { JsonCodec.parseToJsonElement(it).jsonObject.getValue("kind").jsonPrimitive.content })
        assertTrue(energyAndErrors.containsKey(energyRoot + "errors.jsonl"))
        assertFalse(energyAndErrors.containsKey(energyRoot + "log.txt"))
        assertFalse(energyAndErrors.values.any { "must not appear" in it })
    }

    @Test fun exportDropsHistoricalLinesButKeepsRecentOnes() {
        val records = DevStore(File(directory, "records"))
        val recent = now.minusSeconds(3600)
        val old = now.minusSeconds(25 * 3600)
        records.appendEvents(listOf("""{"t":"$recent","kind":"page","page":"chat"}"""), now)
        val current = File(File(directory, "records/events"), DevStore.hourName(now))
        java.io.FileOutputStream(current, true).bufferedWriter().use {
            it.write("""{"t":"$old","kind":"app","event":"exit","reason":"low_memory"}""")
            it.newLine()
        }
        val entries = readZip(DevPackage.exportZip(File(directory, "work"), records, setOf(DevCategory.Logs), since, device, now = now))
        val root = entries.keys.first().substringBefore('/') + '/'
        val lines = entries.getValue(root + "events.jsonl").lines().filter(String::isNotBlank)
        assertEquals(1, lines.size)
        assertTrue(lines.single().contains("chat"))
        assertFalse(lines.single().contains("low_memory"))
    }

    @Test fun sameHourHistoricalExitSortsBeforeNewerRecords() {
        val records = DevStore(File(directory, "records"))
        records.appendEvents(listOf(
            """{"t":"${now.minusSeconds(30)}","kind":"app","event":"startup"}""",
            """{"t":"${now.minusSeconds(20)}","kind":"app","event":"foreground"}""",
        ), now)
        val current = File(File(directory, "records/events"), DevStore.hourName(now.minusSeconds(30)))
        java.io.FileOutputStream(current, true).bufferedWriter().use {
            it.write("""{"t":"${now.minusSeconds(60)}","kind":"app","event":"exit"}""")
            it.newLine()
        }
        val entries = readZip(DevPackage.exportZip(File(directory, "work"), records, setOf(DevCategory.Logs), since, device, now = now))
        val root = entries.keys.first().substringBefore('/') + '/'
        val lines = entries.getValue(root + "events.jsonl").lines().filter(String::isNotBlank)
        assertEquals(listOf("exit", "startup", "foreground"), lines.map {
            JsonCodec.parseToJsonElement(it).jsonObject.getValue("event").jsonPrimitive.content
        })
    }

    private fun readZip(file: File): Map<String, String> {
        ZipFile(file).use { zip ->
            return zip.entries().asSequence().filter { !it.isDirectory }.associate { entry ->
                entry.name to zip.getInputStream(entry).bufferedReader().use { it.readText() }
            }
        }
    }
}
