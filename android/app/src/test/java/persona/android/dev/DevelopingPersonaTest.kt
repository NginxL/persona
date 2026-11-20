package persona.android.dev

import android.content.Context
import androidx.test.core.app.ApplicationProvider
import java.io.File
import java.time.Instant
import java.util.zip.ZipFile
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.runBlocking
import kotlinx.serialization.json.int
import kotlinx.serialization.json.jsonObject
import kotlinx.serialization.json.jsonPrimitive
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import persona.core.contracts.DevProbe
import persona.core.contracts.JsonCodec
import persona.core.data.dev.DevCategory

@RunWith(RobolectricTestRunner::class)
class DevelopingPersonaTest {
    private val context: Context = ApplicationProvider.getApplicationContext()
    private var now = Instant.parse("2026-11-17T12:00:00Z")
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var dev = newDev()
    @After fun cleanUp() { dev.close(); DevProbe.install(null) }

    private fun newDev() = DevelopingPersona(context, scope, clock = { now }, cpuMillis = { 1000 },
        logReader = { listOf("launch https://hub.example.com/a?token=shh") }, observeSystem = false)

    @Test fun switchRestoresRecordsAndOffDeletesThenStopsTheProbes() = runBlocking {
        dev.start()
        assertFalse(dev.state.value.enabled)
        dev.setEnabled(true)
        assertEquals(now, dev.state.value.since)
        DevProbe.frame("sessions", 3)
        dev.refresh()
        assertTrue("the enabled recorder writes events", (dev.state.value.recordedBytes ?: 0) > 0)

        dev.close()
        dev = newDev()
        dev.start()
        assertTrue("the local switch survives process recreation", dev.state.value.enabled)

        dev.setEnabled(false)
        assertFalse(dev.state.value.enabled)
        assertEquals(null, dev.state.value.since)
        assertEquals(0, dev.store.recordedBytes())
        DevProbe.frame("sessions", 3)
        dev.refresh()
        assertEquals(0, dev.store.recordedBytes())
    }

    @Test fun disablingDeletesExportedArchives() = runBlocking {
        dev.start()
        dev.setEnabled(true)
        val zip = dev.export(setOf(DevCategory.Logs))
        assertTrue(zip.isFile)

        dev.setEnabled(false)

        assertFalse(zip.exists())
        assertFalse(File(context.cacheDir, "dev-exports").exists())
    }

    @Test fun aSuccessfulExportRemovesEarlierExportWorkDirectories() = runBlocking {
        dev.start()
        dev.setEnabled(true)
        val first = dev.export(setOf(DevCategory.Logs))
        val second = dev.export(setOf(DevCategory.Logs))

        assertFalse(first.parentFile.exists())
        assertTrue(second.isFile)
        assertEquals(listOf(second.parentFile), File(context.cacheDir, "dev-exports").listFiles()?.toList())
    }

    @Test fun localExitReasonsAreRecordedWithoutDuplicatingAnrReports() = runBlocking {
        dev.start()
        dev.setEnabled(true)
        val exitAt = now.minusSeconds(60)
        dev.recordExit(android.app.ApplicationExitInfo.REASON_LOW_MEMORY, exitAt.toEpochMilli())
        dev.recordExit(android.app.ApplicationExitInfo.REASON_ANR, exitAt.toEpochMilli())
        val zip = dev.export(setOf(DevCategory.Logs))
        val events = ZipFile(zip).use { file ->
            file.entries().asSequence().single { it.name.endsWith("/events.jsonl") }
                .let { entry -> file.getInputStream(entry).bufferedReader().use { reader -> reader.readText() } }
        }
        assertTrue(events.contains("\"event\":\"exit\""))
        assertTrue(events.contains("\"reason\":\"low_memory\""))
        assertTrue(events.contains("\"t\":\"${exitAt}\""))
        assertFalse(events.contains("\"reason\":\"anr\""))
    }

    @Test fun exportContainsRecordedMetricsAndOnlyTheInjectedProcessLog() = runBlocking {
        dev.start()
        dev.setEnabled(true)
        DevProbe.request("GET", "https://hub.example.com/api/v1/sessions?token=shh", 503, 5, 3, 10)
        val zip = dev.export(DevCategory.entries.toSet())
        val entries = ZipFile(zip).use { file ->
            file.entries().asSequence().filter { !it.isDirectory }.associate { entry ->
                entry.name to file.getInputStream(entry).bufferedReader().use { it.readText() }
            }
        }
        val root = entries.keys.first().substringBefore('/') + '/'
        val manifest = JsonCodec.parseToJsonElement(entries.getValue(root + "manifest.json")).jsonObject
        assertEquals("android", manifest.getValue("platform").jsonPrimitive.content)
        assertEquals(2, manifest.getValue("counts").jsonObject.getValue("events").jsonPrimitive.int)
        assertEquals(1, manifest.getValue("counts").jsonObject.getValue("log_lines").jsonPrimitive.int)
        assertEquals("launch /a\n", entries.getValue(root + "log.txt"))
        assertFalse(entries.getValue(root + "events.jsonl").contains("token=shh"))
        assertTrue(entries.getValue(root + "events.jsonl").contains("/sessions"))
    }
}
