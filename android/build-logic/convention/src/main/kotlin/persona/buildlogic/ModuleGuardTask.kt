package persona.buildlogic

import org.gradle.api.DefaultTask
import org.gradle.api.GradleException
import org.gradle.api.file.ConfigurableFileCollection
import org.gradle.api.file.DirectoryProperty
import org.gradle.api.file.RegularFileProperty
import org.gradle.api.provider.ListProperty
import org.gradle.api.tasks.Input
import org.gradle.api.tasks.InputFile
import org.gradle.api.tasks.InputFiles
import org.gradle.api.tasks.Internal
import org.gradle.api.tasks.OutputFile
import org.gradle.api.tasks.PathSensitive
import org.gradle.api.tasks.PathSensitivity
import org.gradle.api.tasks.TaskAction

/** Runs [ModuleGuard] on the project dependencies [edges] (`from -> to`) and `:app`'s [appSources]. */
abstract class ModuleGuardTask : DefaultTask() {
    @get:Input abstract val edges: ListProperty<String>
    @get:InputFiles @get:PathSensitive(PathSensitivity.RELATIVE) abstract val appSources: ConfigurableFileCollection
    @get:Internal abstract val root: DirectoryProperty
    @get:InputFile @get:PathSensitive(PathSensitivity.RELATIVE) abstract val baseline: RegularFileProperty
    @get:OutputFile abstract val report: RegularFileProperty

    @TaskAction
    fun check() {
        val rootDir = root.get().asFile
        val sources = appSources.files.associate { ":app/" + it.relativeTo(rootDir.resolve("app")).invariantSeparatorsPath to it.readText() }
        val pairs = edges.get().map { it.substringBefore(" -> ") to it.substringAfter(" -> ") }
        val violations = ModuleGuard.violations(pairs, sources)
        val baselineFile = baseline.get().asFile
        val failures = ModuleGuard.check(violations, ModuleGuard.baseline(baselineFile.readText()))
        report.get().asFile.writeText(violations.joinToString("\n", postfix = "\n"))
        if (failures.isNotEmpty()) throw GradleException(
            "Module guard (docs/android/android.md \"Modules\"; baseline ${baselineFile.relativeTo(rootDir)} only shrinks):\n" +
                failures.joinToString("\n") { "  $it" })
        logger.lifecycle("Module guard: PASSED (${edges.get().size} project dependencies, ${violations.size} in the baseline)")
    }
}
