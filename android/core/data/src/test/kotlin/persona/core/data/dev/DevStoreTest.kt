package persona.core.data.dev

import java.io.File
import java.nio.file.Files
import java.time.Instant
import kotlinx.serialization.decodeFromString
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.JsonCodec
import persona.core.contracts.generated.ExtensionErrorBreadcrumb
import persona.core.contracts.generated.ExtensionErrorException
import persona.core.contracts.generated.ExtensionErrorReport

class DevStoreTest {
    private val directory: File = Files.createTempDirectory("dev-store").toFile()
    private val now: Instant = Instant.parse("2026-11-17T12:00:00Z")
    @After fun cleanUp() { directory.deleteRecursively() }

    @Test fun errorCopiesAreScrubbedBeforeTheyAreStored() {
        val store = DevStore(directory)
        store.appendError(ExtensionErrorReport(
            id = "one", time = now.toString(), message = "open https://hub.example.com/a?token=shh",
            exceptions = listOf(ExtensionErrorException(type = "Error", value = "token=shh at 192.0.2.4")),
            breadcrumbs = listOf(ExtensionErrorBreadcrumb(message = "/home/alice/.persona")),
        ), now)

        val lines = store.errorLines(now)
        assertEquals(1, lines.size)
        val report = JsonCodec.decodeFromString<ExtensionErrorReport>(lines.single())
        assertEquals("open /a", report.message)
        assertEquals("token=<redacted> at <ip>", report.exceptions!!.single().value)
        assertEquals("~/.persona", report.breadcrumbs!!.single().message)
        assertFalse(lines.single().contains("alice"))
        assertFalse(lines.single().contains("shh"))
    }

    @Test fun oldEventsAndErrorsAreDroppedButRecentOnesStay() {
        val store = DevStore(directory)
        val old = now.minusSeconds(26 * 3600)
        val recent = now.minusSeconds(3600)
        store.appendEvents(listOf("""{"t":"$old","kind":"app"}"""), old)
        store.appendEvents(listOf("""{"t":"$recent","kind":"app"}"""), recent)
        store.appendError(ExtensionErrorReport(id = "old", time = now.minusSeconds(8 * 24 * 3600).toString()), now)
        store.appendError(ExtensionErrorReport(id = "new", time = now.minusSeconds(3600).toString()), now)
        store.trimRecords(now)

        assertEquals(listOf(DevStore.hourName(recent)), store.eventFiles().map(File::getName))
        val errors = store.errorLines(now).map { JsonCodec.decodeFromString<ExtensionErrorReport>(it).id }
        assertEquals(listOf("new"), errors)
        assertTrue(store.recordedBytes() > 0)
    }

    @Test fun aHistoricalLineIsNotFiledUnderTheCurrentHour() {
        val store = DevStore(directory)
        val old = now.minusSeconds(25 * 3600)
        store.appendEvents(listOf("""{"t":"$old","kind":"app","event":"exit"}"""), now)
        assertTrue(store.eventFiles().flatMap { it.readLines() }.isEmpty())
    }

    @Test fun theOldestHourGoesFirstOverTheByteCap() {
        val store = DevStore(directory, maxBytes = 1_600)
        val first = now.minusSeconds(2 * 3600)
        val second = now.minusSeconds(3600)
        listOf(first, second, now).forEach { at ->
            store.appendEvents(listOf("""{"t":"$at","kind":"page","name":"${"a".repeat(700)}"}"""), at)
        }

        assertEquals(listOf(DevStore.hourName(second), DevStore.hourName(now)), store.eventFiles().map(File::getName))
        assertTrue(store.recordedBytes() <= 1_600)
    }

    @Test fun recordsInTheSameHourAreAppendedNotReplaced() {
        val store = DevStore(directory)
        store.appendEvents(listOf("""{"t":"$now","kind":"app","name":"one"}"""), now)
        store.appendEvents(listOf("""{"t":"$now","kind":"app","name":"two"}"""), now)
        store.appendError(ExtensionErrorReport(id = "one", time = now.toString()), now)
        store.appendError(ExtensionErrorReport(id = "two", time = now.toString()), now)

        assertEquals(2, store.eventFiles().single().readLines().size)
        assertEquals(listOf("one", "two"), store.errorLines(now).map {
            JsonCodec.decodeFromString<ExtensionErrorReport>(it).id
        })
    }

    @Test fun theByteCapKeepsTheNewestCompleteErrorInsteadOfDeletingEveryError() {
        val store = DevStore(directory, maxBytes = 256)
        store.appendError(ExtensionErrorReport(id = "old", time = now.minusSeconds(60).toString(), message = "o".repeat(180)), now)
        store.appendError(ExtensionErrorReport(id = "new", time = now.toString(), message = "n".repeat(180)), now)

        assertEquals(listOf("new"), store.errorLines(now).map { JsonCodec.decodeFromString<ExtensionErrorReport>(it).id })
        assertTrue(store.recordedBytes() <= 256)
    }

    @Test fun anOversizedEventFileLeavesRoomForRecentErrors() {
        val store = DevStore(directory, maxBytes = 512)
        store.appendEvents(listOf(
            """{"t":"$now","kind":"app","event":"old","payload":"${"x".repeat(220)}"}""",
            """{"t":"$now","kind":"app","event":"new","payload":"${"y".repeat(40)}"}""",
        ), now)
        store.appendError(ExtensionErrorReport(id = "recent", time = now.toString(), message = "n".repeat(300)), now)

        val events = store.eventFiles().flatMap { it.readLines() }
        assertFalse(events.any { "\"event\":\"old\"" in it })
        assertTrue(events.any { "\"event\":\"new\"" in it })
        assertEquals(listOf("recent"), store.errorLines(now).map { JsonCodec.decodeFromString<ExtensionErrorReport>(it).id })
        assertTrue(store.recordedBytes() <= 512)
    }

    @Test fun clearDeletesEveryRecordedCategory() {
        val store = DevStore(directory)
        store.appendEvents(listOf("""{"t":"$now","kind":"app"}"""), now)
        store.appendError(ExtensionErrorReport(id = "one", time = now.toString()), now)
        assertTrue(store.recordedBytes() > 0)

        store.clear()

        assertEquals(0, store.recordedBytes())
        assertTrue(store.eventFiles().isEmpty())
        assertTrue(store.errorLines(now).isEmpty())
        assertFalse(directory.exists())
    }
}
