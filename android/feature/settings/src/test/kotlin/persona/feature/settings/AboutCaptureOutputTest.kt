package persona.feature.settings

import org.junit.Assert.assertFalse
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Rule
import org.junit.Test
import org.junit.rules.TemporaryFolder
import java.io.File
import java.util.concurrent.Callable
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit

class AboutCaptureOutputTest {
    @get:Rule val temp = TemporaryFolder()

    @Test fun defaultBaseSelectsUniqueChildrenAndReportsTheirExactPaths() {
        val defaultBase = File(temp.root, "default").path
        val reported = mutableListOf<File>()
        val first = newAboutCaptureRun(defaultBase, null, reported::add)
        val second = newAboutCaptureRun(defaultBase, null, reported::add)
        assertNotEquals(first, second)
        assertEquals(File(defaultBase).canonicalFile, first.parentFile)
        assertEquals(File(defaultBase).canonicalFile, second.parentFile)
        assertEquals(listOf(first, second), reported)
        assertTrue(first.isAbsolute)
        assertTrue(second.isAbsolute)
        assertTrue(first.name.startsWith("run-"))
        assertTrue(second.name.startsWith("run-"))
    }

    @Test fun overrideBaseIsUsedWithoutWritingTheDefaultBase() {
        val defaultBase = File(temp.root, "default").path
        val overrideBase = File(temp.root, "override")
        var reported: File? = null
        val overridden = newAboutCaptureRun(defaultBase, overrideBase.path) { reported = it }
        assertEquals(overrideBase.canonicalFile, overridden.parentFile)
        assertEquals(overridden, reported)
        assertFalse(File(defaultBase).exists())
        assertEquals(listOf(overridden), overridden.parentFile.listFiles()?.toList())
    }

    @Test fun freshRunExcludesStaleScenariosWithoutDeletingOtherRunDirectories() {
        val base = File(temp.root, "shared")
        val earlier = newAboutCaptureRun(base.path, null)
        val earlierCapture = File(earlier, "native-removed-scenario.png")
        earlierCapture.writeText("previous task")
        val staleBaseCapture = File(base, "generated-removed-scenario.png")
        staleBaseCapture.writeText("older layout")

        val current = newAboutCaptureRun(base.path, null)
        File(current, "native-current.png").writeText("current task")

        assertEquals(listOf("native-current.png"), current.listFiles()?.map { it.name })
        assertEquals("previous task", earlierCapture.readText())
        assertEquals("older layout", staleBaseCapture.readText())
        assertTrue(earlier.isDirectory)
    }

    @Test fun concurrentCallsWithTheSameOverrideNeverShareOutputPaths() {
        val base = File(temp.root, "same-override")
        val executor = Executors.newFixedThreadPool(4)
        try {
            val allocations = List(16) {
                Callable { newAboutCaptureRun("unused-default", base.path) }
            }
            val runs = executor.invokeAll(allocations).map { it.get(10, TimeUnit.SECONDS) }
            assertEquals(runs.size, runs.toSet().size)
            runs.forEach { run ->
                assertEquals(base.canonicalFile, run.parentFile)
                assertTrue(run.isDirectory)
                assertEquals(0, run.listFiles()?.size)
            }
            val sameNamedCaptures = runs.map { File(it, "native-same-scenario.png") }
            sameNamedCaptures.forEachIndexed { index, capture -> capture.writeText("run-$index") }
            assertEquals(sameNamedCaptures.size, sameNamedCaptures.map { it.canonicalPath }.toSet().size)
            sameNamedCaptures.forEachIndexed { index, capture ->
                assertEquals("run-$index", capture.readText())
            }
        } finally {
            executor.shutdownNow()
            assertTrue(executor.awaitTermination(10, TimeUnit.SECONDS))
        }
    }
}
