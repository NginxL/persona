package persona.feature.settings

import java.io.File
import java.nio.file.Files

/**
 * Allocate one fresh evidence directory for a capture invocation.
 *
 * The base is shared; the returned run directory is not. Atomic directory
 * creation keeps concurrent invocations apart without deleting any siblings.
 */
internal fun newAboutCaptureRun(
    defaultBase: String,
    overrideBase: String?,
    report: (File) -> Unit = {
        println("PERSONA_ABOUT_CAPTURE_DIR=${it.absolutePath}")
    },
): File {
    val base = File(overrideBase ?: defaultBase).toPath().toAbsolutePath().normalize()
    Files.createDirectories(base)
    val run = Files.createTempDirectory(base, "run-").toRealPath().toFile()
    report(run)
    return run
}
