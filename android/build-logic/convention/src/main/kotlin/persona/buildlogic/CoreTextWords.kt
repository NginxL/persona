package persona.buildlogic

import org.gradle.api.DefaultTask
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.file.RegularFileProperty
import org.gradle.api.tasks.InputFile
import org.gradle.api.tasks.OutputDirectory
import org.gradle.api.tasks.PathSensitive
import org.gradle.api.tasks.PathSensitivity
import org.gradle.api.tasks.TaskAction

/**
 * Puts core/designsystem's Simplified Chinese `strings_core.xml` at the root of a test resources directory, where
 * CoreTextFallback reads it (persona.coretext.zh). Only that file: the other strings files change more often and no
 * test reads them from the classpath.
 */
abstract class CoreTextWords : DefaultTask() {
    @get:InputFile @get:PathSensitive(PathSensitivity.NONE)
    abstract val words: RegularFileProperty

    @get:OutputDirectory abstract val outputDirectory: DirectoryProperty

    @TaskAction fun copy() {
        words.get().asFile.copyTo(outputDirectory.get().file("strings_core.xml").asFile, overwrite = true)
    }
}
