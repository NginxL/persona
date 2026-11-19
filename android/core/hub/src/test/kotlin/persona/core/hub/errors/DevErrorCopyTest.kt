package persona.core.hub.errors

import java.io.File
import java.nio.file.Files
import org.junit.After
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import persona.core.contracts.generated.ExtensionErrorReport

class DevErrorCopyTest {
    private val directory: File = Files.createTempDirectory("dev-error-copy").toFile()
    @After fun cleanUp() { directory.deleteRecursively() }
    private val report = ExtensionErrorReport(id = "one", time = "2026-11-17T12:00:00Z", message = "failed")

    @Test fun aSuccessfulQueueWriteOffersTheSameReportToTheLocalCopy() {
        val copies = mutableListOf<ExtensionErrorReport>()
        val queue = ErrorReportQueue(directory, localCopy = { copies += it })
        assertTrue(queue.write(report))

        assertEquals(listOf(report), copies)
        assertEquals(listOf("one"), queue.pending().map { it.report.id })
    }

    @Test fun aLocalCopyFailureDoesNotUndoTheExistingHubQueue() {
        var called = false
        val queue = ErrorReportQueue(directory, localCopy = { called = true; error("local disk full") })
        assertTrue(queue.write(report))

        assertTrue("the failing copy was actually attempted", called)
        assertEquals(listOf("one"), queue.pending().map { it.report.id })
    }
}
