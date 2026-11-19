package persona.buildlogic

import java.io.File
import org.gradle.api.file.FileCollection
import org.gradle.api.tasks.Input
import org.gradle.api.tasks.InputDirectory
import org.gradle.api.tasks.InputFiles
import org.gradle.api.tasks.Internal
import org.gradle.api.tasks.PathSensitive
import org.gradle.api.tasks.PathSensitivity
import org.gradle.process.CommandLineArgumentProvider

// A system property set with Test.systemProperty, or a plain processor argument, is an input as written, so an absolute
// path in it makes every checkout's cache key different (#10712). These providers pass the same path while keeping it
// out of the key.

/**
 * `-D<property>=<absolute path>` for a location that is not an input itself, such as the repository root. Whatever a
 * test reads below it is declared on its own (the fixture domains its sources name, for one).
 */
class LocationProperty(
    @get:Input val property: String,
    @get:Internal val location: File,
) : CommandLineArgumentProvider {
    override fun asArguments(): List<String> = listOf("-D$property=${location.absolutePath}")
}

/**
 * `-D<property>=<absolute path>` for a single file whose contents are the input, wherever it is: a native library a
 * task builds, for example. The fingerprint ignores the path, and the collection carries the task that produces it.
 */
class FileContentProperty(
    @get:Input val property: String,
    @get:InputFiles @get:PathSensitive(PathSensitivity.NONE) val file: FileCollection,
) : CommandLineArgumentProvider {
    override fun asArguments(): List<String> = listOf("-D$property=${file.singleFile.absolutePath}")
}

/** `<name>=<absolute path>` (a processor argument) for a directory whose contents, not its location, are the input. */
class RelativeDirectoryArgument(
    @get:Input val name: String,
    @get:InputDirectory @get:PathSensitive(PathSensitivity.RELATIVE) val directory: File,
) : CommandLineArgumentProvider {
    override fun asArguments(): List<String> = listOf("$name=${directory.absolutePath}")
}
